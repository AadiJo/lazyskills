package webserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/alvinunreal/lazyskills/internal/actions"
	"github.com/alvinunreal/lazyskills/internal/model"
	"github.com/alvinunreal/lazyskills/internal/registry"
	"github.com/alvinunreal/lazyskills/internal/runner"
	"github.com/alvinunreal/lazyskills/internal/scan"
	"github.com/alvinunreal/lazyskills/internal/selfupdate"
	"github.com/alvinunreal/lazyskills/internal/skillops"
)

const sessionCookie = "lazyskills_session"

// Config configures the local-only web server.
type Config struct {
	Cwd            string
	Token          string
	ReadOnly       bool
	AllowedOrigins []string
	Scanner        func(string) (model.ScanResult, error)
	Registry       *registry.Client
	RunCommand     func(context.Context, runner.ExecSpec, runner.StreamOptions, func(runner.StreamEvent)) runner.Result
	CommandTimeout time.Duration
	StallTimeout   time.Duration
	// AllowPortFallback permits a derived default port to fall back to an
	// ephemeral port when it is occupied. Explicit CLI ports leave this false.
	AllowPortFallback bool
}

// Server owns one local project API and its serialized mutation queue.
type Server struct {
	cfg        Config
	token      string
	scans      *scanStore
	previews   *previewStore
	candidates *candidateStore
	jobs       *jobManager
	handler    http.Handler
}

func New(config Config) (*Server, error) {
	if config.Cwd == "" {
		return nil, fmt.Errorf("web server cwd is required")
	}
	if config.Scanner == nil {
		config.Scanner = scan.Run
	}
	if config.Registry == nil {
		config.Registry = registry.NewClient()
	}
	if config.RunCommand == nil {
		config.RunCommand = runner.RunStreaming
	}
	if config.CommandTimeout <= 0 {
		config.CommandTimeout = 5 * time.Minute
	}
	if config.StallTimeout <= 0 {
		config.StallTimeout = 90 * time.Second
	}
	if config.Token == "" {
		var err error
		config.Token, err = newToken()
		if err != nil {
			return nil, err
		}
	}
	s := &Server{cfg: config, token: config.Token}
	s.scans = newScanStore(config.Cwd, config.Scanner)
	s.previews = newPreviewStore(config.Token)
	s.candidates = newCandidateStore()
	s.jobs = newJobManager(s.executeJob)
	s.handler = s.routes()
	return s, nil
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate startup token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Server) Token() string { return s.token }

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/scan", s.handleScan)
	mux.HandleFunc("GET /api/events", s.handleScanEvents)
	mux.HandleFunc("GET /api/registry/search", s.handleRegistrySearch)
	mux.HandleFunc("GET /api/sources/{id}/skills", s.handleSourceSkills)
	mux.HandleFunc("GET /api/skills/content", s.handleSkillContent)
	mux.HandleFunc("POST /api/actions/preview", s.handlePreview)
	mux.HandleFunc("POST /api/actions/execute", s.handleExecute)
	mux.HandleFunc("GET /api/jobs/{id}/events", s.handleJobEvents)
	mux.HandleFunc("GET /api/update", s.handleUpdate)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "API endpoint not found")
	})
	mux.Handle("/", s.staticHandler())
	return s.security(mux)
}

func (s *Server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r.Host) {
			writeError(w, http.StatusForbidden, "request Host must be localhost")
			return
		}
		if changesState(r.Method) && !s.originAllowed(r) {
			writeError(w, http.StatusForbidden, "request Origin is not allowed")
			return
		}
		if r.URL.Path == "/" && r.Method == http.MethodGet && r.URL.Query().Get("token") != "" {
			if !constantToken(r.URL.Query().Get("token"), s.token) {
				writeError(w, http.StatusUnauthorized, "invalid startup token")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if !s.authenticated(r) {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) hostAllowed(raw string) bool {
	if isLocalHost(raw) {
		return true
	}
	for _, allowed := range s.cfg.AllowedOrigins {
		if parsed, err := url.Parse(allowed); err == nil && strings.EqualFold(parsed.Host, raw) {
			return true
		}
	}
	return false
}

func constantToken(got, want string) bool {
	if len(got) != len(want) {
		return false
	}
	var diff byte
	for i := range got {
		diff |= got[i] ^ want[i]
	}
	return diff == 0
}

func (s *Server) authenticated(r *http.Request) bool {
	if token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); token != "" && constantToken(token, s.token) {
		return true
	}
	if token := r.Header.Get("X-Lazyskills-Token"); token != "" && constantToken(token, s.token) {
		return true
	}
	cookie, err := r.Cookie(sessionCookie)
	return err == nil && constantToken(cookie.Value, s.token)
}

func isLocalHost(raw string) bool {
	host := raw
	if parsed, _, err := net.SplitHostPort(raw); err == nil {
		host = parsed
	} else if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		host = strings.Trim(raw, "[]")
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func changesState(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err == nil && isLocalHost(u.Host) && strings.EqualFold(u.Host, r.Host) {
		return true
	}
	for _, allowed := range s.cfg.AllowedOrigins {
		if origin == strings.TrimSuffix(allowed, "/") {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request: "+err.Error())
		return false
	}
	return true
}

func (s *Server) handleScan(w http.ResponseWriter, _ *http.Request) {
	snapshot, generation, err := s.scans.Scan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"generation": generation, "read_only": s.cfg.ReadOnly, "result": snapshot, "sources": s.sourceGroups(snapshot)})
}

func (s *Server) handleRegistrySearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	results, err := s.cfg.Registry.Search(r.Context(), query, 50)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if results == nil {
		results = []registry.Skill{}
	}
	snapshot, _, err := s.scans.Scan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type resultView struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		Slug        string `json:"slug"`
		Source      string `json:"source"`
		Installs    int    `json:"installs"`
		Invalid     bool   `json:"invalid,omitempty"`
		Reason      string `json:"reason,omitempty"`
		CandidateID string `json:"candidate_id,omitempty"`
		Installed   bool   `json:"installed"`
	}
	views := make([]resultView, 0, len(results))
	for _, result := range results {
		installed := candidateInstalled(snapshot, result.Source, result.Slug, result.DisplayName)
		view := resultView{ID: result.ID, DisplayName: result.DisplayName, Slug: result.Slug, Source: result.Source, Installs: result.Installs, Invalid: result.Invalid, Reason: result.Reason, Installed: installed}
		if installed {
			view.Reason = "already installed from this source"
		}
		if !result.Invalid && !installed {
			view.CandidateID = s.candidates.Put(result.Source, result.Slug, result.DisplayName)
			if view.CandidateID == "" {
				writeError(w, http.StatusInternalServerError, "could not create install candidate")
				return
			}
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": query, "results": views})
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	plan, err := selfupdate.Plan(ctx, false, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// Run serves on both loopback families when available and blocks until a
// termination signal. The returned URL always uses literal IPv4 loopback.
func Run(config Config, port int, open bool) error {
	s, err := New(config)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return s.serve(ctx, port, open)
}

func (s *Server) serve(ctx context.Context, port int, open bool) error {
	listeners, actualPort, err := listenLoopbacks(port, s.cfg.AllowPortFallback)
	if err != nil {
		return err
	}
	launchURL := fmt.Sprintf("http://127.0.0.1:%d/?token=%s", actualPort, url.QueryEscape(s.token))
	fmt.Fprintf(os.Stdout, "LazySkills web UI: %s\nProject: %s\n", launchURL, s.cfg.Cwd)
	if open {
		if err := OpenBrowser(launchURL); err != nil {
			fmt.Fprintf(os.Stderr, "Could not open browser: %v\n", err)
		}
	}
	servers := make([]*http.Server, 0, len(listeners))
	errCh := make(chan error, len(listeners))
	for _, listener := range listeners {
		srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
		servers = append(servers, srv)
		go func() {
			if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, srv := range servers {
			_ = srv.Shutdown(shutdown)
		}
		return nil
	case err := <-errCh:
		return err
	}
}

func listenLoopbacks(port int, allowFallback bool) ([]net.Listener, int, error) {
	v4, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil && port != 0 && allowFallback {
		v4, err = net.Listen("tcp4", "127.0.0.1:0")
	}
	if err != nil {
		return nil, 0, err
	}
	actualPort := v4.Addr().(*net.TCPAddr).Port
	listeners := []net.Listener{v4}
	if v6, err := net.Listen("tcp6", net.JoinHostPort("::1", strconv.Itoa(actualPort))); err == nil {
		listeners = append(listeners, v6)
	}
	return listeners, actualPort, nil
}

func (s *Server) executeJob(job *Job, action actions.CommandPreview, snapshot model.ScanResult, expectedGeneration uint64) {
	_, currentGeneration, scanErr := s.scans.Scan()
	if scanErr != nil {
		job.complete(map[string]any{"result": runner.Result{Program: action.Exec.Program, ExitCode: -1, Err: "could not revalidate queued action: " + scanErr.Error()}, "stale": true})
		return
	}
	if currentGeneration != expectedGeneration {
		job.complete(map[string]any{"result": runner.Result{Program: action.Exec.Program, ExitCode: -1, Err: "queued action became stale; preview it again"}, "stale": true, "generation": currentGeneration})
		return
	}
	job.emit("started", map[string]any{"title": action.Title})
	var result runner.Result
	partial := false
	if internal, wasPartial, handled := skillops.ExecuteInternal(action, s.cfg.Cwd, snapshot); handled {
		result, partial = internal, wasPartial
	} else if len(action.Exec.Batch) > 0 {
		result, partial = s.executeBatch(job, action.Exec.Batch)
	} else if action.Exec.Interactive {
		result = runner.Result{Program: action.Exec.Program, Args: action.Exec.Args, Cwd: s.cfg.Cwd, ExitCode: -1, Err: "interactive commands are not supported in web mode"}
	} else {
		result = s.runExternal(job, action.Exec)
		partial = skillops.CleanupLockAfterRemove(action, s.cfg.Cwd, &result)
	}
	fresh, generation, scanErr := s.scans.Scan()
	completion := map[string]any{"result": result, "partial_success": partial, "generation": generation}
	if scanErr != nil {
		completion["scan_error"] = scanErr.Error()
	} else {
		completion["skill_count"] = len(fresh.Skills)
	}
	job.complete(completion)
}

func (s *Server) executeBatch(job *Job, batch []actions.ExecSpec) (runner.Result, bool) {
	lines := make([]string, 0, len(batch))
	for i, spec := range batch {
		job.emit("progress", map[string]any{"current": i + 1, "total": len(batch), "program": spec.Program})
		result := s.runExternal(job, spec)
		if result.ExitCode != 0 || result.Err != "" {
			result.Stdout = strings.Join(append(lines, fmt.Sprintf("%d/%d %s failed", i+1, len(batch), spec.Program)), "\n")
			return result, i > 0
		}
		lines = append(lines, fmt.Sprintf("%d/%d %s ok", i+1, len(batch), spec.Program))
	}
	return runner.Result{Program: "bulk", Cwd: s.cfg.Cwd, ExitCode: 0, Stdout: strings.Join(lines, "\n")}, false
}

func (s *Server) runExternal(job *Job, spec actions.ExecSpec) runner.Result {
	return s.cfg.RunCommand(context.Background(), runner.ExecSpec{Program: spec.Program, Args: spec.Args, Cwd: s.cfg.Cwd}, runner.StreamOptions{Timeout: s.cfg.CommandTimeout, StallTimeout: s.cfg.StallTimeout}, func(event runner.StreamEvent) {
		job.emit("output", event)
	})
}
