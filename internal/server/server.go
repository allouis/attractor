// Package server hosts the Attractor HTTP service described in spec
// §9.5: pipeline submission, SSE event streaming, remote human gates,
// and SVG rendering. The server is intentionally minimal — it pins a
// loopback address by default and provides no authentication.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/allouis/attractor/internal/automation"
	"github.com/allouis/attractor/internal/config"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/interviewer"
	"github.com/allouis/attractor/internal/items"
	"github.com/allouis/attractor/internal/items/httpapi"
	"github.com/allouis/attractor/internal/items/source"
	"github.com/allouis/attractor/internal/render"
	"github.com/allouis/attractor/internal/scheduler"
	"github.com/allouis/attractor/internal/setup"
)

// defaultMaxConcurrentRuns bounds runs executing at once when the config
// leaves it unset (service-spec §3).
const defaultMaxConcurrentRuns = 4

// Server is the HTTP-mode Attractor service.
type Server struct {
	addr         string
	logsRoot     string
	listener     net.Listener
	httpsrv      *http.Server
	registry     *runRegistry
	makeHandlers HandlerFactory
	authToken    string
	dispatcher   *dispatcher
	sources      map[string]source.Source
	repos        items.Repos

	automationsDir string
	workflowsDir   string
	sched          *scheduler.Scheduler

	mu          sync.RWMutex
	automations []automation.Automation
}

// HandlerFactory builds an engine.Registry suitable for executing a
// pipeline. Callers inject this so the server can run pipelines against
// custom handler/backend setups (Claude Code, fake backend, etc).
type HandlerFactory func(iv interviewer.Interviewer) *engine.Registry

// Config configures a Server.
type Config struct {
	Addr         string         // TCP bind address; defaults to "127.0.0.1:0"
	LogsRoot     string         // base directory for run logs
	MakeHandlers HandlerFactory // optional; defaults to a simulation-mode registry
	// AuthToken enables bearer-token auth. When non-empty, every
	// request must carry `Authorization: Bearer <token>` unless the
	// request originated from the loopback interface (so local tools
	// don't need to know the token). Empty disables auth entirely.
	AuthToken string
	// MaxConcurrentRuns bounds how many submitted runs execute at once;
	// the rest queue FIFO. Zero or negative uses defaultMaxConcurrentRuns.
	MaxConcurrentRuns int
	// AutomationsDir holds the TOML automation files (service-spec §5).
	// Empty disables the automations endpoints and cron scheduler.
	AutomationsDir string
	// Sources maps a source name (github, linear) to its Source, backing
	// GET /items (items-spec §11). Empty disables the endpoint's sources.
	Sources map[string]source.Source
	// Repos maps `owner/name` to a local checkout, resolving a dispatched
	// item's repo to the run's cwd (items-spec I3/I4). Empty means no repo
	// resolves, so POST /items/run rejects any non-PR / unmapped item.
	Repos items.Repos
	// WorkflowsDir is the catalog root scanned by GET /workflows for
	// `<name>/pipeline.dot` definitions (web-ui-spec W2). Empty defaults to
	// ~/.attractor/pipelines.
	WorkflowsDir string
}

// New constructs an unstarted server.
func New(cfg Config) *Server {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:0"
	}
	if cfg.LogsRoot == "" {
		cfg.LogsRoot = ".attractor-runs"
	}
	if cfg.MakeHandlers == nil {
		cfg.MakeHandlers = DefaultHandlers(handler.Codergen{})
	}
	if cfg.WorkflowsDir == "" {
		cfg.WorkflowsDir = defaultWorkflowsDir()
	}
	s := &Server{
		addr:           cfg.Addr,
		logsRoot:       cfg.LogsRoot,
		registry:       newRunRegistry(cfg.LogsRoot),
		makeHandlers:   cfg.MakeHandlers,
		authToken:      cfg.AuthToken,
		dispatcher:     newDispatcher(cfg.MaxConcurrentRuns),
		automationsDir: cfg.AutomationsDir,
		workflowsDir:   cfg.WorkflowsDir,
		sources:        cfg.Sources,
		repos:          cfg.Repos,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /pipelines", s.submitPipeline)
	mux.HandleFunc("GET /pipelines", s.listPipelines)
	mux.HandleFunc("GET /pipelines/{id}", s.getPipeline)
	mux.HandleFunc("GET /pipelines/{id}/events", s.streamEvents)
	mux.HandleFunc("POST /pipelines/{id}/cancel", s.cancelPipeline)
	mux.HandleFunc("GET /pipelines/{id}/graph", s.getGraph)
	mux.HandleFunc("GET /pipelines/{id}/artifacts/{path...}", s.getArtifact)
	mux.HandleFunc("GET /pipelines/{id}/stages/{node}", s.getStage)
	mux.HandleFunc("GET /pipelines/{id}/questions", s.listQuestions)
	mux.HandleFunc("POST /pipelines/{id}/questions/{qid}/answer", s.answerQuestion)
	mux.HandleFunc("GET /pipelines/{id}/checkpoint", s.getCheckpoint)
	mux.HandleFunc("GET /pipelines/{id}/context", s.getContext)
	httpapi.Register(mux, itemsDeps{s})
	mux.HandleFunc("GET /workflows", s.listWorkflows)
	mux.HandleFunc("GET /workflows/{name}/graph", s.getWorkflowGraph)
	mux.HandleFunc("GET /automations", s.listAutomations)
	mux.HandleFunc("POST /automations/{name}/run", s.runAutomation)
	mux.HandleFunc("GET /ui", s.serveUI)
	mux.HandleFunc("GET /ui/", s.serveUI)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	s.httpsrv = &http.Server{
		Handler:           withRecoverer(s.withAuth(mux)),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// Start binds the address and begins serving in a goroutine. The
// returned Addr returns the live address (useful for ephemeral ports).
func (s *Server) Start() error {
	if s.automationsDir != "" {
		autos, err := automation.Load(s.automationsDir)
		if err != nil {
			return fmt.Errorf("load automations: %w", err)
		}
		s.mu.Lock()
		s.automations = autos
		s.mu.Unlock()
		s.sched = scheduler.New(autos, s.fireAutomation, time.Now)
	}
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.listener = ln
	s.addr = ln.Addr().String()
	go s.dispatcher.run()
	if s.sched != nil {
		go s.sched.Run()
	}
	go s.httpsrv.Serve(ln)
	return nil
}

// URL returns the http:// URL clients should use.
func (s *Server) URL() string { return "http://" + s.addr }

// Close shuts down the HTTP server gracefully.
func (s *Server) Close() error {
	if s.sched != nil {
		s.sched.Stop()
	}
	s.dispatcher.close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.httpsrv.Shutdown(ctx)
}

// DefaultHandlers returns a HandlerFactory that wires the built-in
// handler set with the supplied codergen backend (nil = simulation
// mode). The wait.human handler uses the interviewer passed at runtime
// so per-run RemoteInterviewer instances can be substituted.
func DefaultHandlers(coderBackend handler.Codergen) HandlerFactory {
	return func(iv interviewer.Interviewer) *engine.Registry {
		r := engine.NewRegistry()
		r.Register("start", handler.Start{})
		r.Register("exit", handler.Exit{})
		r.Register("conditional", handler.Conditional{})
		r.Register("wait.human", handler.WaitHuman{Interviewer: iv})
		r.Register("tool", handler.Tool{})
		r.Register("parallel", handler.Parallel{})
		r.Register("parallel.fan_in", handler.FanIn{})
		r.Register("stack.manager_loop", handler.ManagerLoop{})
		r.Register("codergen", coderBackend)
		r.SetDefault(coderBackend)
		return r
	}
}

// ---------------------------------------------------------------------------
// Request handlers
// ---------------------------------------------------------------------------

func (s *Server) submitPipeline(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	r.Body.Close()
	source := string(body)
	var vars map[string]string
	var cwd string
	var itemRef *items.ItemRef
	if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
		var payload struct {
			Dot     string            `json:"dot"`
			Vars    map[string]string `json:"vars"`
			Cwd     string            `json:"cwd"`
			ItemRef *items.ItemRef    `json:"item_ref"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		source = payload.Dot
		vars = payload.Vars
		cwd = payload.Cwd
		if payload.ItemRef != nil {
			if payload.ItemRef.Source == "" || payload.ItemRef.Type == "" || payload.ItemRef.ExternalID == "" {
				http.Error(w, "item_ref requires source, type, and external_id", http.StatusBadRequest)
				return
			}
			itemRef = payload.ItemRef
		}
	}
	tag := ""
	if itemRef != nil {
		tag = itemRef.String()
	}
	// A raw dot submission carries no catalog path, so no workflow_name.
	id, err := s.submit(source, vars, cwd, tag, "", "")
	if err != nil {
		http.Error(w, "validate: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// itemsDeps adapts the server to httpapi.Deps: it exposes the daemon's
// sources, repo map, admission path, and item-linked runs to the items
// HTTP layer without leaking the run registry's types. The typed
// items.ItemRef stays inside internal/items; the server sees only the tag.
type itemsDeps struct{ s *Server }

func (d itemsDeps) Source(name string) (source.Source, bool) {
	src, ok := d.s.sources[name]
	return src, ok
}

func (d itemsDeps) SourceNames() []string {
	names := make([]string, 0, len(d.s.sources))
	for name := range d.s.sources {
		names = append(names, name)
	}
	return names
}

func (d itemsDeps) RepoPath(repo string) (string, bool) { return d.s.repos.Path(repo) }

func (d itemsDeps) Submit(dot string, vars map[string]string, cwd, tag, workflowName, baseDir string) (string, error) {
	return d.s.submit(dot, vars, cwd, tag, workflowName, baseDir)
}

func (d itemsDeps) LinkedRuns(tag string) []httpapi.LinkedRun {
	runs := d.s.registry.RunsForItem(tag)
	out := make([]httpapi.LinkedRun, 0, len(runs))
	for _, run := range runs {
		status := run.Status()
		out = append(out, httpapi.LinkedRun{
			ID:     run.ID,
			Status: string(status),
			Active: status == RunQueued || status == RunRunning,
		})
	}
	return out
}

// submit runs the shared setup path (service-spec §2), registers the run,
// and enqueues it, returning the new run id. It is the single admission
// point shared by HTTP submission (POST /pipelines), manual automation
// triggers (POST /automations/{name}/run), and the cron scheduler, so
// every route enqueues runs identically. @file prompts resolve against
// cwd, submitted vars seed the run context (so `$context.*` interpolates
// at runtime), and cwd becomes the graph-level cwd default (node/graph
// attrs still win). itemRef, when non-empty, is the opaque item tag that
// stamps the run with the external Item that spawned it (items-spec I1);
// automation and cron callers pass "". workflowName, when non-empty, is the
// catalog directory the run was dispatched from — the run→workflow backlink
// handle (web-ui-spec W6); raw dot and automation callers pass "".
func (s *Server) submit(source string, vars map[string]string, cwd, itemRef, workflowName, baseDir string) (string, error) {
	// The pipeline's own files (@prompt refs, a manager_loop child_dotfile)
	// resolve against baseDir — the directory the pipeline was loaded from —
	// so a workflow dispatched from the catalog can reference its prompts and
	// sub-pipelines relative to itself. cwd stays the work directory the
	// agent operates in (the target repo). A raw-dot submit has no on-disk
	// pipeline, so baseDir falls back to cwd.
	if baseDir == "" {
		baseDir = cwd
	}
	prepared, err := setup.Prepare(setup.Options{
		Source:  source,
		BaseDir: baseDir,
		Cwd:     cwd,
	})
	if err != nil {
		return "", err
	}
	seed := seedChecks(seedContext(vars, itemRef), cwd)
	run := s.registry.NewRun(source, prepared.Graph, prepared, s.logsRoot, s.makeHandlers, itemRef, workflowName, seed)
	s.dispatcher.enqueue(run)
	return run.ID, nil
}

// seedContext builds the run's initial context: the submitted vars under
// plain names (so `$context.k` resolves at runtime, C3) plus, when an Item
// spawned the run, the tag-derived item.type/item.source/item.id the router
// branches on (router-spec §"Seeded initial context"). Returns nil only
// when there is nothing to seed — no vars and no Item — so a bare run
// starts unseeded.
// checkNames are the static checks a work pipeline (implement, bug-fix)
// can gate on, each seeded into the run context as $context.check.<name>.
var checkNames = []string{"deps", "typecheck", "lint", "test"}

// seedChecks adds $context.check.<name> for every static check, taking the
// command from the target repo's .attractor/config.toml [checks] table and
// falling back to a no-op (echo … ; success) when a repo configures none,
// so a work pipeline can reference them unconditionally.
func seedChecks(seed map[string]string, cwd string) map[string]string {
	if seed == nil {
		seed = map[string]string{}
	}
	var configured map[string]string
	if cwd != "" {
		if home, err := os.UserHomeDir(); err == nil {
			if cfg, err := config.Load(home, cwd); err == nil {
				configured = cfg.Checks
			}
		}
	}
	for _, name := range checkNames {
		cmd := configured[name]
		if cmd == "" {
			cmd = "echo '" + name + ": not configured for this repo (skipped)'"
		}
		seed["check."+name] = cmd
	}
	return seed
}

func seedContext(vars map[string]string, itemRef string) map[string]string {
	if len(vars) == 0 && itemRef == "" {
		return nil
	}
	seed := make(map[string]string, len(vars)+3)
	for k, v := range vars {
		seed[k] = v
	}
	if ref, err := items.ParseItemRef(itemRef); err == nil {
		seed["item.type"] = ref.Type
		seed["item.source"] = ref.Source
		seed["item.id"] = ref.ExternalID
	}
	return seed
}

// listPipelines returns registry summaries newest-first (service-spec
// §3): the run index the UI's run list is built from.
func (s *Server) listPipelines(w http.ResponseWriter, r *http.Request) {
	runs := s.registry.List()
	summaries := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		summaries = append(summaries, run.Summary())
	}
	writeJSON(w, http.StatusOK, map[string]any{"pipelines": summaries})
}

func (s *Server) getPipeline(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, run.Summary())
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	stream := run.Subscribe(since)
	defer run.Unsubscribe(stream)
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-stream:
			if !ok {
				return
			}
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "event: %s\n", ev.Kind)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			if ev.Kind == engine.EventPipelineCompleted || ev.Kind == engine.EventPipelineFailed {
				return
			}
		}
	}
}

func (s *Server) cancelPipeline(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	run.Cancel()
	writeJSON(w, http.StatusAccepted, map[string]any{"cancelled": true})
}

func (s *Server) getGraph(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	svg, err := render.SVG([]byte(run.Source()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write(svg)
}

// getArtifact serves a read-only file from the run's logs directory,
// backing the UI node pane's prompt.md / response.md / tool_calls/ links
// (service-spec §4). The requested path is cleaned and confined to the
// run's logsRoot so it cannot escape into the wider filesystem.
func (s *Server) getArtifact(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	root := filepath.Clean(run.logsRoot)
	// Leading slash makes Clean collapse any leading ".." segments.
	full := filepath.Join(root, filepath.Clean("/"+r.PathValue("path")))
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, full)
}

func (s *Server) listQuestions(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"questions": run.PendingQuestions()})
}

func (s *Server) answerQuestion(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	var payload AnswerPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := run.SubmitAnswer(r.PathValue("qid"), payload); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

func (s *Server) getCheckpoint(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, ok := run.Checkpoint()
	if !ok {
		http.Error(w, "no checkpoint yet", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (s *Server) getContext(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, run.Context())
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func withRecoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				http.Error(w, fmt.Sprintf("panic: %v", rec), http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withAuth gates non-loopback requests behind a bearer token when one
// is configured. Loopback callers and /healthz always pass through.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authToken == "" || r.URL.Path == "/healthz" || isLoopback(r.RemoteAddr) {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if len(auth) <= len(prefix) || auth[:len(prefix)] != prefix || auth[len(prefix):] != s.authToken {
			w.Header().Set("WWW-Authenticate", `Bearer realm="attractor"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopback reports whether the RemoteAddr (host:port) is 127.0.0.1
// or ::1. Used to bypass bearer-token checks for local tools.
func isLoopback(remoteAddr string) bool {
	host := remoteAddr
	if i := strings.LastIndex(remoteAddr, ":"); i >= 0 {
		host = remoteAddr[:i]
	}
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	switch host {
	case "127.0.0.1", "::1", "":
		return true
	}
	return false
}

// newRunID returns a short hex run id used both as URL path component
// and as the engine's RunID.
func newRunID() string {
	b := make([]byte, 8)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(256))
		b[i] = byte(n.Int64())
	}
	return hex.EncodeToString(b)
}

// AnswerPayload is the request body for POST /answer.
type AnswerPayload struct {
	Text  string `json:"text,omitempty"`
	Key   string `json:"key,omitempty"`
	Label string `json:"label,omitempty"`
}
