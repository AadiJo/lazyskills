package webserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/alvinunreal/lazyskills/internal/actions"
	"github.com/alvinunreal/lazyskills/internal/model"
)

type executeRequest struct {
	PreviewHash    string `json:"preview_hash"`
	Generation     uint64 `json:"generation"`
	IdempotencyKey string `json:"idempotency_key"`
}

type jobTask struct {
	job        *Job
	action     actions.CommandPreview
	snapshot   model.ScanResult
	generation uint64
}

type JobEvent struct {
	ID   int64           `json:"id"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
	At   time.Time       `json:"at"`
}

type Job struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created_at"`

	mu          sync.Mutex
	events      []JobEvent
	subscribers map[chan JobEvent]struct{}
	done        bool
	completed   time.Time
	nextEventID int64
	eventBytes  int
}

const (
	maxJobEvents     = 512
	maxJobEventBytes = 512 << 10
	maxJobs          = 128
	jobRetention     = 30 * time.Minute
)

func (j *Job) emit(kind string, value any) {
	payload, _ := json.Marshal(value)
	j.mu.Lock()
	j.appendLocked(kind, payload, false)
	j.mu.Unlock()
}

func (j *Job) appendLocked(kind string, payload []byte, done bool) {
	j.nextEventID++
	event := JobEvent{ID: j.nextEventID, Type: kind, Data: payload, At: time.Now().UTC()}
	j.events = append(j.events, event)
	j.eventBytes += len(payload)
	for len(j.events) > maxJobEvents || j.eventBytes > maxJobEventBytes {
		j.eventBytes -= len(j.events[0].Data)
		j.events = j.events[1:]
	}
	if done {
		j.done = true
		j.completed = time.Now().UTC()
	}
	for subscriber := range j.subscribers {
		select {
		case subscriber <- event:
		default:
			delete(j.subscribers, subscriber)
			close(subscriber)
		}
	}
}

func (j *Job) complete(value any) {
	payload, _ := json.Marshal(value)
	j.mu.Lock()
	j.appendLocked("complete", payload, true)
	j.mu.Unlock()
}

func (j *Job) subscribe(after int64) ([]JobEvent, <-chan JobEvent, func(), bool, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	truncated := len(j.events) > 0 && after < j.events[0].ID-1
	var replay []JobEvent
	for _, event := range j.events {
		if event.ID > after {
			replay = append(replay, event)
		}
	}
	ch := make(chan JobEvent, 32)
	if !j.done {
		j.subscribers[ch] = struct{}{}
	}
	cancel := func() {
		j.mu.Lock()
		delete(j.subscribers, ch)
		j.mu.Unlock()
	}
	return replay, ch, cancel, j.done, truncated
}

type idempotencyRecord struct {
	jobID       string
	previewHash string
	generation  uint64
}

type jobManager struct {
	mu          sync.Mutex
	jobs        map[string]*Job
	idempotency map[string]idempotencyRecord
	queue       chan jobTask
	execute     func(*Job, actions.CommandPreview, model.ScanResult, uint64)
	next        uint64
}

func newJobManager(execute func(*Job, actions.CommandPreview, model.ScanResult, uint64)) *jobManager {
	m := &jobManager{jobs: make(map[string]*Job), idempotency: make(map[string]idempotencyRecord), queue: make(chan jobTask, 64), execute: execute}
	go m.worker()
	return m
}

func (m *jobManager) worker() {
	for task := range m.queue {
		m.execute(task.job, task.action, task.snapshot, task.generation)
	}
}

func (m *jobManager) lookup(key, previewHash string, generation uint64) (*Job, bool, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked()
	record, ok := m.idempotency[key]
	if !ok {
		return nil, false, false
	}
	job := m.jobs[record.jobID]
	if job == nil {
		delete(m.idempotency, key)
		return nil, false, false
	}
	if record.previewHash != previewHash || record.generation != generation {
		return nil, false, true
	}
	return job, true, false
}

func (m *jobManager) enqueue(key, previewHash string, generation uint64, action actions.CommandPreview, snapshot model.ScanResult) (*Job, bool, bool) {
	m.mu.Lock()
	m.cleanupLocked()
	if record, ok := m.idempotency[key]; ok {
		job := m.jobs[record.jobID]
		if job != nil && record.previewHash == previewHash && record.generation == generation {
			m.mu.Unlock()
			return job, true, false
		}
		m.mu.Unlock()
		return nil, false, true
	}
	m.next++
	id := fmt.Sprintf("job-%d-%d", time.Now().Unix(), m.next)
	job := &Job{ID: id, Created: time.Now().UTC(), subscribers: make(map[chan JobEvent]struct{})}
	job.emit("queued", map[string]any{"title": action.Title})
	m.jobs[id] = job
	m.idempotency[key] = idempotencyRecord{jobID: id, previewHash: previewHash, generation: generation}
	m.mu.Unlock()
	m.queue <- jobTask{job: job, action: action, snapshot: snapshot, generation: generation}
	return job, false, false
}

func (m *jobManager) get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked()
	job, ok := m.jobs[id]
	return job, ok
}

func (m *jobManager) cleanupLocked() {
	cutoff := time.Now().Add(-jobRetention)
	for id, job := range m.jobs {
		job.mu.Lock()
		expired := job.done && job.completed.Before(cutoff)
		job.mu.Unlock()
		if expired {
			delete(m.jobs, id)
		}
	}
	for len(m.jobs) > maxJobs {
		var oldestID string
		var oldest time.Time
		for id, job := range m.jobs {
			job.mu.Lock()
			completed := job.completed
			done := job.done
			job.mu.Unlock()
			if done && (oldestID == "" || completed.Before(oldest)) {
				oldestID, oldest = id, completed
			}
		}
		if oldestID == "" {
			break
		}
		delete(m.jobs, oldestID)
	}
	for key, record := range m.idempotency {
		if m.jobs[record.jobID] == nil {
			delete(m.idempotency, key)
		}
	}
}

func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ReadOnly {
		writeError(w, http.StatusForbidden, "web server is running in read-only mode")
		return
	}
	var request executeRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if headerKey := r.Header.Get("Idempotency-Key"); headerKey != "" {
		request.IdempotencyKey = headerKey
	}
	if len(request.IdempotencyKey) < 8 || len(request.IdempotencyKey) > 200 {
		writeError(w, http.StatusBadRequest, "an idempotency key of 8-200 characters is required")
		return
	}
	if job, existing, conflict := s.jobs.lookup(request.IdempotencyKey, request.PreviewHash, request.Generation); conflict {
		writeError(w, http.StatusConflict, "idempotency key was already used for a different request")
		return
	} else if existing {
		writeJSON(w, http.StatusOK, map[string]any{"job_id": job.ID, "existing": true, "events_url": "/api/jobs/" + job.ID + "/events"})
		return
	}
	stored, ok := s.previews.Get(request.PreviewHash)
	if !ok {
		writeError(w, http.StatusNotFound, "preview expired or was not found")
		return
	}
	_, generation, err := s.scans.Scan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if request.Generation != stored.generation || generation != stored.generation {
		writeError(w, http.StatusConflict, "scan changed; rescan and preview the action again")
		return
	}
	job, existing, conflict := s.jobs.enqueue(request.IdempotencyKey, request.PreviewHash, request.Generation, stored.action, stored.snapshot)
	if conflict {
		writeError(w, http.StatusConflict, "idempotency key was already used for a different request")
		return
	}
	status := http.StatusAccepted
	if existing {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"job_id": job.ID, "existing": existing, "events_url": "/api/jobs/" + job.ID + "/events"})
}

func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobs.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	replay, events, cancel, done, truncated := job.subscribe(after)
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	writeEvent := func(event JobEvent) {
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.Type, event.Data)
		flusher.Flush()
	}
	if truncated {
		fmt.Fprint(w, "event: replay-reset\ndata: {\"message\":\"older job output was truncated\"}\n\n")
		flusher.Flush()
	}
	for _, event := range replay {
		writeEvent(event)
	}
	if done {
		return
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case event, open := <-events:
			if !open {
				return
			}
			writeEvent(event)
			if event.Type == "complete" {
				return
			}
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
