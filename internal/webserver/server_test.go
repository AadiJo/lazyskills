package webserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alvinunreal/lazyskills/internal/model"
	"github.com/alvinunreal/lazyskills/internal/runner"
)

type mutableScanner struct {
	mu       sync.Mutex
	snapshot model.ScanResult
}

func (m *mutableScanner) scan(_ string) (model.ScanResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	payload, _ := json.Marshal(m.snapshot)
	var clone model.ScanResult
	_ = json.Unmarshal(payload, &clone)
	for i, skill := range m.snapshot.Skills {
		if skill != nil && i < len(clone.Skills) {
			clone.Skills[i].Preview = skill.Preview
		}
	}
	return clone, nil
}

func (m *mutableScanner) mutateDescription(value string) {
	m.mu.Lock()
	m.snapshot.Skills[0].Description = value
	m.mu.Unlock()
}

func testSnapshot(t *testing.T) model.ScanResult {
	t.Helper()
	return model.ScanResult{Cwd: t.TempDir(), Skills: []*model.Skill{{
		Name: "deploy", Description: "Deploy safely", Scope: model.ScopeProject,
		CanonicalPath: "/tmp/deploy", SkillPath: "/tmp/deploy/SKILL.md", Preview: "# Deploy\n",
		ObservedPaths: []model.ObservedPath{{Path: "/tmp/deploy", Scope: model.ScopeProject, Agent: "codex", Status: model.StatusCanonical}},
		LocalLock:     &model.LocalLockEntry{Source: "owner/repo", SourceType: "git"},
	}}, Agents: []model.AgentState{{Name: "codex", Display: "Codex", Detected: true}}}
}

func newTestServer(t *testing.T, readOnly bool, run func(context.Context, runner.ExecSpec, runner.StreamOptions, func(runner.StreamEvent)) runner.Result) (*Server, *mutableScanner) {
	t.Helper()
	scanner := &mutableScanner{snapshot: testSnapshot(t)}
	if run == nil {
		run = func(_ context.Context, spec runner.ExecSpec, _ runner.StreamOptions, emit func(runner.StreamEvent)) runner.Result {
			if emit != nil {
				emit(runner.StreamEvent{Stream: "stdout", Data: "done"})
			}
			return runner.Result{Program: spec.Program, Args: spec.Args, Cwd: spec.Cwd, ExitCode: 0, Stdout: "done"}
		}
	}
	server, err := New(Config{Cwd: scanner.snapshot.Cwd, Token: "test-token-123", ReadOnly: readOnly, Scanner: scanner.scan, RunCommand: run})
	if err != nil {
		t.Fatal(err)
	}
	return server, scanner
}

func request(t *testing.T, server *Server, method, path string, body any, auth bool) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Host = "localhost"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("X-Lazyskills-Token", server.Token())
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	return recorder
}

func previewRemove(t *testing.T, server *Server) previewResponse {
	t.Helper()
	response := request(t, server, http.MethodPost, "/api/actions/preview", previewRequest{Action: "remove", Skills: []skillRef{{Scope: model.ScopeProject, Name: "deploy"}}}, true)
	if response.Code != http.StatusOK {
		t.Fatalf("preview failed: %d %s", response.Code, response.Body.String())
	}
	var preview previewResponse
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	return preview
}

func waitDone(t *testing.T, server *Server, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := server.jobs.get(id)
		if ok {
			job.mu.Lock()
			done := job.done
			job.mu.Unlock()
			if done {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("job did not finish")
}

func TestSecurityRequiresLocalHostAndToken(t *testing.T) {
	server, _ := newTestServer(t, false, nil)
	if got := request(t, server, http.MethodGet, "/api/scan", nil, false); got.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", got.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/scan", nil)
	req.Host = "evil.example"
	req.Header.Set("X-Lazyskills-Token", server.Token())
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected spoofed Host rejection, got %d", recorder.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/actions/preview", strings.NewReader(`{"action":"remove"}`))
	req.Host = "localhost"
	req.Header.Set("X-Lazyskills-Token", server.Token())
	req.Header.Set("Origin", "https://evil.example")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected spoofed Origin rejection, got %d", recorder.Code)
	}
}

func TestSecurityProbeLoopbackAndRequestBoundaries(t *testing.T) {
	listeners, _, err := listenLoopbacks(0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, listener := range listeners {
		defer listener.Close()
		address, ok := listener.Addr().(*net.TCPAddr)
		if !ok || !address.IP.IsLoopback() {
			t.Fatalf("listener escaped loopback: %v", listener.Addr())
		}
	}
	server, _ := newTestServer(t, false, nil)
	endpoints := []struct{ method, path string }{
		{http.MethodGet, "/api/scan"},
		{http.MethodGet, "/api/events"},
		{http.MethodGet, "/api/registry/search?q=deploy"},
		{http.MethodGet, "/api/sources/not-found/skills"},
		{http.MethodGet, "/api/skills/content?scope=project&name=deploy"},
		{http.MethodPost, "/api/actions/preview"},
		{http.MethodPost, "/api/actions/execute"},
		{http.MethodGet, "/api/jobs/not-found/events"},
		{http.MethodGet, "/api/update"},
	}
	for _, endpoint := range endpoints {
		name := endpoint.method + " " + endpoint.path
		t.Run(name, func(t *testing.T) {
			if response := request(t, server, endpoint.method, endpoint.path, nil, false); response.Code != http.StatusUnauthorized {
				t.Fatalf("tokenless request returned %d", response.Code)
			}
			spoofed := httptest.NewRequest(endpoint.method, endpoint.path, nil)
			spoofed.Host = "attacker.example"
			spoofed.Header.Set("X-Lazyskills-Token", server.Token())
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, spoofed)
			if response.Code != http.StatusForbidden {
				t.Fatalf("spoofed Host returned %d", response.Code)
			}
			if endpoint.method == http.MethodPost {
				crossOrigin := httptest.NewRequest(endpoint.method, endpoint.path, nil)
				crossOrigin.Host = "localhost"
				crossOrigin.Header.Set("X-Lazyskills-Token", server.Token())
				crossOrigin.Header.Set("Origin", "https://attacker.example")
				response = httptest.NewRecorder()
				server.Handler().ServeHTTP(response, crossOrigin)
				if response.Code != http.StatusForbidden {
					t.Fatalf("spoofed Origin returned %d", response.Code)
				}
			}
			base := strings.Split(endpoint.path, "?")[0]
			traversal := request(t, server, endpoint.method, base+"/%252e%252e/%252e%252e/etc/passwd", nil, true)
			if traversal.Code < http.StatusBadRequest {
				t.Fatalf("traversal request returned %d: %s", traversal.Code, traversal.Body.String())
			}
		})
	}
}

func TestExplicitPortDoesNotFallBack(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	if listeners, _, err := listenLoopbacks(port, false); err == nil {
		for _, listener := range listeners {
			_ = listener.Close()
		}
		t.Fatal("explicit occupied port unexpectedly fell back")
	}
	listeners, actual, err := listenLoopbacks(port, true)
	if err != nil {
		t.Fatalf("derived port did not fall back: %v", err)
	}
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	if actual == port {
		t.Fatalf("fallback reused occupied port %d", port)
	}
}

func TestAllowedOriginAlsoAllowsItsProxyHost(t *testing.T) {
	scanner := &mutableScanner{snapshot: testSnapshot(t)}
	server, err := New(Config{Cwd: scanner.snapshot.Cwd, Token: "test-token-123", Scanner: scanner.scan, AllowedOrigins: []string{"https://dev.example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/actions/preview", strings.NewReader(`{"action":"remove","skills":[{"scope":"project","name":"deploy"}]}`))
	req.Host = "dev.example.test"
	req.Header.Set("Origin", "https://dev.example.test")
	req.Header.Set("X-Lazyskills-Token", server.Token())
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected allow-listed proxy request, got %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestStartupTokenBecomesStrictCookie(t *testing.T) {
	server, _ := newTestServer(t, false, nil)
	response := request(t, server, http.MethodGet, "/?token="+server.Token(), nil, false)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/" {
		t.Fatalf("unexpected token exchange response: %d %+v", response.Code, response.Header())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected session cookie: %+v", cookies)
	}
}

func TestScanPreviewExecuteAndSSE(t *testing.T) {
	server, _ := newTestServer(t, false, nil)
	scanResponse := request(t, server, http.MethodGet, "/api/scan", nil, true)
	if scanResponse.Code != http.StatusOK || !strings.Contains(scanResponse.Body.String(), `"generation":1`) {
		t.Fatalf("unexpected scan response: %d %s", scanResponse.Code, scanResponse.Body.String())
	}
	preview := previewRemove(t, server)
	execResponse := request(t, server, http.MethodPost, "/api/actions/execute", executeRequest{PreviewHash: preview.Hash, Generation: preview.Generation, IdempotencyKey: "request-0001"}, true)
	if execResponse.Code != http.StatusAccepted {
		t.Fatalf("execute failed: %d %s", execResponse.Code, execResponse.Body.String())
	}
	var queued struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(execResponse.Body.Bytes(), &queued)
	waitDone(t, server, queued.JobID)
	events := request(t, server, http.MethodGet, "/api/jobs/"+queued.JobID+"/events", nil, true)
	if events.Code != http.StatusOK || !strings.Contains(events.Body.String(), "event: output") || !strings.Contains(events.Body.String(), "event: complete") {
		t.Fatalf("unexpected SSE replay: %d %s", events.Code, events.Body.String())
	}
}

func TestStalePreviewIsRejected(t *testing.T) {
	server, scanner := newTestServer(t, false, nil)
	preview := previewRemove(t, server)
	scanner.mutateDescription("changed outside the browser")
	response := request(t, server, http.MethodPost, "/api/actions/execute", executeRequest{PreviewHash: preview.Hash, Generation: preview.Generation, IdempotencyKey: "request-stale"}, true)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "scan changed") {
		t.Fatalf("expected stale generation conflict, got %d %s", response.Code, response.Body.String())
	}
}

func TestIdempotencyReturnsOriginalJob(t *testing.T) {
	var runs atomic.Int32
	server, scanner := newTestServer(t, false, func(_ context.Context, spec runner.ExecSpec, _ runner.StreamOptions, _ func(runner.StreamEvent)) runner.Result {
		runs.Add(1)
		time.Sleep(20 * time.Millisecond)
		return runner.Result{Program: spec.Program, ExitCode: 0}
	})
	preview := previewRemove(t, server)
	body := executeRequest{PreviewHash: preview.Hash, Generation: preview.Generation, IdempotencyKey: "same-request-key"}
	first := request(t, server, http.MethodPost, "/api/actions/execute", body, true)
	var queued struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(first.Body.Bytes(), &queued)
	waitDone(t, server, queued.JobID)
	scanner.mutateDescription("changed after the original job")
	second := request(t, server, http.MethodPost, "/api/actions/execute", body, true)
	if first.Code != http.StatusAccepted || second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"existing":true`) {
		t.Fatalf("unexpected idempotency responses: %d %s / %d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	if runs.Load() != 1 {
		t.Fatalf("expected one execution, got %d", runs.Load())
	}
	conflict := request(t, server, http.MethodPost, "/api/actions/execute", executeRequest{PreviewHash: "different-preview", Generation: body.Generation, IdempotencyKey: body.IdempotencyKey}, true)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "different request") {
		t.Fatalf("idempotency key reuse was not rejected: %d %s", conflict.Code, conflict.Body.String())
	}
}

func TestQueuedMutationRevalidatesGeneration(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var runs atomic.Int32
	var scanner *mutableScanner
	var server *Server
	server, scanner = newTestServer(t, false, func(_ context.Context, spec runner.ExecSpec, _ runner.StreamOptions, _ func(runner.StreamEvent)) runner.Result {
		runs.Add(1)
		once.Do(func() { close(started) })
		<-release
		scanner.mutateDescription("first queued action changed state")
		return runner.Result{Program: spec.Program, ExitCode: 0}
	})
	one := previewRemove(t, server)
	two := previewRemove(t, server)
	first := request(t, server, http.MethodPost, "/api/actions/execute", executeRequest{PreviewHash: one.Hash, Generation: one.Generation, IdempotencyKey: "queued-stale-one"}, true)
	<-started
	second := request(t, server, http.MethodPost, "/api/actions/execute", executeRequest{PreviewHash: two.Hash, Generation: two.Generation, IdempotencyKey: "queued-stale-two"}, true)
	close(release)
	var firstJob, secondJob struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(first.Body.Bytes(), &firstJob)
	_ = json.Unmarshal(second.Body.Bytes(), &secondJob)
	waitDone(t, server, firstJob.JobID)
	waitDone(t, server, secondJob.JobID)
	if runs.Load() != 1 {
		t.Fatalf("stale queued command ran; executions=%d", runs.Load())
	}
	events := request(t, server, http.MethodGet, "/api/jobs/"+secondJob.JobID+"/events", nil, true)
	if !strings.Contains(events.Body.String(), "queued action became stale") {
		t.Fatalf("stale completion was not reported: %s", events.Body.String())
	}
}

func TestMutationQueueSerializesJobs(t *testing.T) {
	var running atomic.Int32
	var maxRunning atomic.Int32
	server, _ := newTestServer(t, false, func(_ context.Context, spec runner.ExecSpec, _ runner.StreamOptions, _ func(runner.StreamEvent)) runner.Result {
		current := running.Add(1)
		for {
			max := maxRunning.Load()
			if current <= max || maxRunning.CompareAndSwap(max, current) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		running.Add(-1)
		return runner.Result{Program: spec.Program, ExitCode: 0}
	})
	previewOne := previewRemove(t, server)
	previewTwo := previewRemove(t, server)
	first := request(t, server, http.MethodPost, "/api/actions/execute", executeRequest{PreviewHash: previewOne.Hash, Generation: previewOne.Generation, IdempotencyKey: "serial-job-one"}, true)
	second := request(t, server, http.MethodPost, "/api/actions/execute", executeRequest{PreviewHash: previewTwo.Hash, Generation: previewTwo.Generation, IdempotencyKey: "serial-job-two"}, true)
	var one, two struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(first.Body.Bytes(), &one)
	_ = json.Unmarshal(second.Body.Bytes(), &two)
	waitDone(t, server, one.JobID)
	waitDone(t, server, two.JobID)
	if maxRunning.Load() != 1 {
		t.Fatalf("expected serialized executions, max concurrent=%d", maxRunning.Load())
	}
}

func TestReadOnlyRejectsExecute(t *testing.T) {
	server, _ := newTestServer(t, true, nil)
	response := request(t, server, http.MethodPost, "/api/actions/execute", executeRequest{}, true)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected read-only rejection, got %d", response.Code)
	}
}

func TestSkillContentIsSanitized(t *testing.T) {
	server, scanner := newTestServer(t, false, nil)
	scanner.mu.Lock()
	scanner.snapshot.Skills[0].Preview = "# Safe\n<script>alert(1)</script>\n<a href=\"javascript:alert(2)\" onclick=\"alert(3)\">bad</a>"
	scanner.mu.Unlock()
	response := request(t, server, http.MethodGet, "/api/skills/content?scope=project&name=deploy", nil, true)
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, "<script") || strings.Contains(body, "javascript:") || strings.Contains(body, "onclick") || !strings.Contains(body, "Safe") {
		t.Fatalf("unsafe rendered content: %d %s", response.Code, body)
	}
}

func TestInstallPreviewRequiresOpaqueServerCandidate(t *testing.T) {
	server, _ := newTestServer(t, false, nil)
	direct := request(t, server, http.MethodPost, "/api/actions/preview", map[string]any{"action": "install_skill", "source": "/tmp/attacker", "slug": "payload"}, true)
	if direct.Code != http.StatusBadRequest || !strings.Contains(direct.Body.String(), "unknown field") {
		t.Fatalf("client-supplied install arguments were accepted: %d %s", direct.Code, direct.Body.String())
	}
	id := server.candidates.Put("owner/repo", "safe-skill", "Safe Skill")
	approved := request(t, server, http.MethodPost, "/api/actions/preview", previewRequest{Action: "install_skill", CandidateIDs: []string{id}}, true)
	if approved.Code != http.StatusOK || !strings.Contains(approved.Body.String(), "owner/repo") || !strings.Contains(approved.Body.String(), "safe-skill") {
		t.Fatalf("server-issued candidate failed: %d %s", approved.Code, approved.Body.String())
	}
}

func TestInstallPreviewRevalidatesCandidateAgainstFreshScan(t *testing.T) {
	server, scanner := newTestServer(t, false, nil)
	id := server.candidates.Put("owner/repo", "new-skill", "New Skill")
	scanner.mu.Lock()
	scanner.snapshot.Skills = append(scanner.snapshot.Skills, &model.Skill{
		Name:  "new-skill",
		Scope: model.ScopeGlobal,
		GlobalLock: &model.GlobalLockEntry{
			Source: "https://github.com/owner/repo.git",
		},
	})
	scanner.mu.Unlock()
	response := request(t, server, http.MethodPost, "/api/actions/preview", previewRequest{Action: "install_skill", CandidateIDs: []string{id}}, true)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "already installed") {
		t.Fatalf("stale candidate was accepted: %d %s", response.Code, response.Body.String())
	}
}

func TestLocalSourceDiscoveryUsesOpaqueGroupAndLockedMetadata(t *testing.T) {
	cwd := t.TempDir()
	root := filepath.Join(cwd, "checkout")
	installed := filepath.Join(root, "skills", "deploy")
	available := filepath.Join(root, "skills", "observe")
	for path, content := range map[string]string{installed: "---\nname: deploy\ndescription: Deploy safely\n---\n", available: "---\nname: observe\ndescription: Observe safely\n---\n"} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := model.ScanResult{Cwd: cwd, Skills: []*model.Skill{{Name: "deploy", Scope: model.ScopeProject, CanonicalPath: installed, LocalLock: &model.LocalLockEntry{Source: root, SourceType: "directory", SkillPath: "skills/deploy/SKILL.md", Ref: "locked/ref"}}}}
	scanner := &mutableScanner{snapshot: snapshot}
	server, err := New(Config{Cwd: cwd, Token: "test-token-123", Scanner: scanner.scan})
	if err != nil {
		t.Fatal(err)
	}
	groups := server.sourceGroups(snapshot)
	if len(groups) != 1 || !groups[0].Discoverable || groups[0].ref != "locked/ref" || groups[0].ID == root {
		t.Fatalf("unexpected opaque source group: %+v", groups)
	}
	response := request(t, server, http.MethodGet, "/api/sources/"+groups[0].ID+"/skills", nil, true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"name":"observe"`) || !strings.Contains(response.Body.String(), `"candidate_id"`) {
		t.Fatalf("local source discovery failed: %d %s", response.Code, response.Body.String())
	}
	var discovered struct {
		Skills []discoveredCandidate `json:"skills"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &discovered); err != nil {
		t.Fatal(err)
	}
	sawInstalled, sawAvailable := false, false
	for _, skill := range discovered.Skills {
		if skill.Name == "deploy" && (!skill.Installed || skill.CandidateID != "") {
			t.Fatalf("installed source skill remained installable: %+v", skill)
		}
		if skill.Name == "deploy" {
			sawInstalled = true
		}
		if skill.Name == "observe" && (skill.Installed || skill.CandidateID == "") {
			t.Fatalf("new source skill was not installable: %+v", skill)
		}
		if skill.Name == "observe" {
			sawAvailable = true
		}
	}
	if !sawInstalled || !sawAvailable {
		t.Fatalf("expected installed and available source entries, got %+v", discovered.Skills)
	}
	pathLike := request(t, server, http.MethodGet, "/api/sources/"+url.PathEscape(root)+"/skills", nil, true)
	if pathLike.Code != http.StatusNotFound {
		t.Fatalf("client path was accepted as a source identifier: %d %s", pathLike.Code, pathLike.Body.String())
	}
}

func TestCandidateInstalledNormalizesSourceSpellings(t *testing.T) {
	snapshot := model.ScanResult{Skills: []*model.Skill{{Name: "Deploy", Scope: model.ScopeGlobal, GlobalLock: &model.GlobalLockEntry{Source: "git+https://github.com/Owner/Repo.git"}}}}
	if !candidateInstalled(snapshot, "owner/repo", "deploy", "Deploy Skill") {
		t.Fatal("equivalent installed source was not recognized")
	}
	if candidateInstalled(snapshot, "other/repo", "deploy") || candidateInstalled(snapshot, "owner/repo", "other") {
		t.Fatal("unrelated candidate was marked installed")
	}
}

func TestJobReplayIsBoundedAndSlowSubscribersReconnect(t *testing.T) {
	job := &Job{ID: "bounded", subscribers: make(map[chan JobEvent]struct{})}
	_, slow, cancel, _, _ := job.subscribe(0)
	defer cancel()
	for i := 0; i < maxJobEvents+100; i++ {
		job.emit("output", map[string]string{"data": strings.Repeat("x", 2048)})
	}
	job.mu.Lock()
	eventCount, eventBytes := len(job.events), job.eventBytes
	stillSubscribed := len(job.subscribers) != 0
	job.mu.Unlock()
	if eventCount > maxJobEvents || eventBytes > maxJobEventBytes {
		t.Fatalf("job replay exceeded bounds: events=%d bytes=%d", eventCount, eventBytes)
	}
	if stillSubscribed {
		t.Fatal("slow subscriber was left connected while events were dropped")
	}
	for range slow {
	}
	replay, _, replayCancel, _, truncated := job.subscribe(0)
	replayCancel()
	if !truncated || len(replay) == 0 {
		t.Fatalf("bounded replay did not signal truncation: events=%d truncated=%v", len(replay), truncated)
	}
}

func TestScanEventsReportsExternalSkillChange(t *testing.T) {
	cwd := t.TempDir()
	skillDir := filepath.Join(cwd, ".agents", "skills", "deploy")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("# Before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scanner := func(_ string) (model.ScanResult, error) {
		content, err := os.ReadFile(skillFile)
		if err != nil {
			return model.ScanResult{}, err
		}
		return model.ScanResult{Cwd: cwd, Skills: []*model.Skill{{Name: "deploy", Description: string(content), Scope: model.ScopeProject}}}, nil
	}
	server, err := New(Config{Cwd: cwd, Token: "test-token-123", Scanner: scanner})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Lazyskills-Token", server.Token())
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatal(readErr)
		}
		if line == "\n" {
			break
		}
	}
	if err := os.WriteFile(skillFile, []byte("# After\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for {
		line, readErr := reader.ReadString('\n')
		if strings.HasPrefix(line, "event: scan") {
			return
		}
		if readErr != nil {
			t.Fatalf("live refresh event not received: %v", readErr)
		}
	}
}

func TestScanEventsReadyCatchesChangesDuringDisconnect(t *testing.T) {
	cwd := t.TempDir()
	skillDir := filepath.Join(cwd, ".agents", "skills", "deploy")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("# Before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scanner := func(_ string) (model.ScanResult, error) {
		content, err := os.ReadFile(skillFile)
		return model.ScanResult{Cwd: cwd, Skills: []*model.Skill{{Name: "deploy", Description: string(content), Scope: model.ScopeProject}}}, err
	}
	server, err := New(Config{Cwd: cwd, Token: "test-token-123", Scanner: scanner})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	readyGeneration := func() (uint64, func()) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/events", nil)
		if requestErr != nil {
			cancel()
			t.Fatal(requestErr)
		}
		req.Header.Set("X-Lazyskills-Token", server.Token())
		response, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			cancel()
			t.Fatal(requestErr)
		}
		reader := bufio.NewReader(response.Body)
		var generation uint64
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				response.Body.Close()
				cancel()
				t.Fatal(readErr)
			}
			if strings.HasPrefix(line, "data: ") {
				var data struct {
					Generation uint64 `json:"generation"`
				}
				if decodeErr := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &data); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				generation = data.Generation
			}
			if line == "\n" {
				break
			}
		}
		return generation, func() { response.Body.Close(); cancel() }
	}
	before, disconnect := readyGeneration()
	disconnect()
	if err := os.WriteFile(skillFile, []byte("# During disconnect\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, closeSecond := readyGeneration()
	closeSecond()
	if after <= before {
		t.Fatalf("reconnect ready generation did not advance: before=%d after=%d", before, after)
	}
}
