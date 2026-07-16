package webserver

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alvinunreal/lazyskills/internal/agents"
	"github.com/alvinunreal/lazyskills/internal/locks"
	"github.com/fsnotify/fsnotify"
)

type scanWatch struct {
	watcher *fsnotify.Watcher
	targets []string
	files   map[string]struct{}
	added   map[string]struct{}
}

func newScanWatch(cwd string) (*scanWatch, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &scanWatch{watcher: watcher, files: make(map[string]struct{}), added: make(map[string]struct{})}
	seen := make(map[string]struct{})
	for _, location := range agents.Locations(cwd) {
		target := filepath.Clean(location.Root)
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		w.targets = append(w.targets, target)
		if err := w.watchTarget(target); err != nil {
			watcher.Close()
			return nil, err
		}
	}
	for _, name := range []string{locks.ProjectLockPath(cwd), locks.GlobalLockPath()} {
		clean := filepath.Clean(name)
		w.files[clean] = struct{}{}
		if err := w.watchNearestParent(filepath.Dir(clean)); err != nil {
			watcher.Close()
			return nil, err
		}
	}
	return w, nil
}

func (w *scanWatch) watchTarget(target string) error {
	info, err := os.Stat(target)
	if err == nil && info.IsDir() {
		return filepath.WalkDir(target, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || !entry.IsDir() {
				return nil
			}
			return w.add(path)
		})
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return w.watchNearestParent(filepath.Dir(target))
}

func (w *scanWatch) watchNearestParent(path string) error {
	for {
		info, err := os.Stat(path)
		if err == nil && info.IsDir() {
			return w.add(path)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func (w *scanWatch) add(path string) error {
	path = filepath.Clean(path)
	if _, ok := w.added[path]; ok {
		return nil
	}
	if err := w.watcher.Add(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	w.added[path] = struct{}{}
	return nil
}

func (w *scanWatch) relevant(path string) bool {
	path = filepath.Clean(path)
	if _, ok := w.files[path]; ok {
		return true
	}
	for _, target := range w.targets {
		if path == target || strings.HasPrefix(path, target+string(filepath.Separator)) || strings.HasPrefix(target, path+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (w *scanWatch) addCreatedDirectory(path string) {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return
	}
	for _, target := range w.targets {
		if path == target || strings.HasPrefix(path, target+string(filepath.Separator)) || strings.HasPrefix(target, path+string(filepath.Separator)) {
			_ = w.watchTarget(path)
			return
		}
	}
}

func (s *Server) handleScanEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	watch, err := newScanWatch(s.cfg.Cwd)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer watch.watcher.Close()
	_, generation, err := s.scans.Scan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	fmt.Fprintf(w, "event: ready\ndata: {\"generation\":%d}\n\n", generation)
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	var debounce *time.Timer
	var debounceC <-chan time.Time
	schedule := func() {
		if debounce == nil {
			debounce = time.NewTimer(250 * time.Millisecond)
		} else {
			if !debounce.Stop() {
				select {
				case <-debounce.C:
				default:
				}
			}
			debounce.Reset(250 * time.Millisecond)
		}
		debounceC = debounce.C
	}
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()

	for {
		select {
		case event, open := <-watch.watcher.Events:
			if !open {
				return
			}
			if !watch.relevant(event.Name) {
				continue
			}
			if event.Has(fsnotify.Create) {
				watch.addCreatedDirectory(event.Name)
			}
			schedule()
		case _, open := <-watch.watcher.Errors:
			if !open {
				return
			}
			schedule()
		case <-debounceC:
			debounceC = nil
			_, next, scanErr := s.scans.Scan()
			if scanErr != nil {
				payload, _ := json.Marshal(map[string]string{"error": scanErr.Error()})
				fmt.Fprintf(w, "event: scan-error\ndata: %s\n\n", payload)
				flusher.Flush()
				continue
			}
			if next != generation {
				generation = next
				fmt.Fprintf(w, "id: %d\nevent: scan\ndata: {\"generation\":%d}\n\n", generation, generation)
				flusher.Flush()
			}
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
