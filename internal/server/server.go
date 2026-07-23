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
	"strings"
	"time"

	"github.com/fabro/attractor/internal/engine"
	"github.com/fabro/attractor/internal/handler"
	"github.com/fabro/attractor/internal/interviewer"
	"github.com/fabro/attractor/internal/render"
	"github.com/fabro/attractor/internal/setup"
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
	s := &Server{
		addr:         cfg.Addr,
		logsRoot:     cfg.LogsRoot,
		registry:     newRunRegistry(cfg.LogsRoot),
		makeHandlers: cfg.MakeHandlers,
		authToken:    cfg.AuthToken,
		dispatcher:   newDispatcher(cfg.MaxConcurrentRuns),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /pipelines", s.submitPipeline)
	mux.HandleFunc("GET /pipelines", s.listPipelines)
	mux.HandleFunc("GET /pipelines/{id}", s.getPipeline)
	mux.HandleFunc("GET /pipelines/{id}/events", s.streamEvents)
	mux.HandleFunc("POST /pipelines/{id}/cancel", s.cancelPipeline)
	mux.HandleFunc("GET /pipelines/{id}/graph", s.getGraph)
	mux.HandleFunc("GET /pipelines/{id}/questions", s.listQuestions)
	mux.HandleFunc("POST /pipelines/{id}/questions/{qid}/answer", s.answerQuestion)
	mux.HandleFunc("GET /pipelines/{id}/checkpoint", s.getCheckpoint)
	mux.HandleFunc("GET /pipelines/{id}/context", s.getContext)
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
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.listener = ln
	s.addr = ln.Addr().String()
	go s.dispatcher.run()
	go s.httpsrv.Serve(ln)
	return nil
}

// Addr returns the listening address.
func (s *Server) Addr() string { return s.addr }

// URL returns the http:// URL clients should use.
func (s *Server) URL() string { return "http://" + s.addr }

// Close shuts down the HTTP server gracefully.
func (s *Server) Close() error {
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
	if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
		var payload struct {
			Dot  string            `json:"dot"`
			Vars map[string]string `json:"vars"`
			Cwd  string            `json:"cwd"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		source = payload.Dot
		vars = payload.Vars
		cwd = payload.Cwd
	}
	// Run the shared setup path (service-spec §2): @file prompts resolve
	// against the submission cwd, $vars expand, and the payload cwd
	// becomes the graph-level cwd default (node/graph attrs still win).
	prepared, err := setup.Prepare(setup.Options{
		Source:  source,
		Vars:    vars,
		BaseDir: cwd,
		Cwd:     cwd,
	})
	if err != nil {
		http.Error(w, "validate: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	run := s.registry.NewRun(source, prepared.Graph, prepared, s.logsRoot, s.makeHandlers)
	s.dispatcher.enqueue(run)
	writeJSON(w, http.StatusCreated, map[string]any{"id": run.ID})
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

	stream := run.Subscribe()
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
