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
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	"github.com/allouis/attractor/internal/version"
)

// defaultMaxConcurrentRuns bounds runs executing at once when the config
// leaves it unset (service-spec §3).
const defaultMaxConcurrentRuns = 4

// Server is the HTTP-mode Attractor service.
type Server struct {
	addr          string
	logsRoot      string
	listener      net.Listener
	httpsrv       *http.Server
	registry      *runRegistry
	makeHandlers  HandlerFactory
	authToken     string
	dispatcher    *dispatcher
	launcher      Launcher            // default launcher
	launchers     map[string]Launcher // named launchers for per-run override
	defaultRunner string              // launcher name a dispatch defaults to (--runner)
	sources       map[string]source.Source
	repos         items.Repos

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
	// Repos maps `owner/name` to a local checkout — a boot snapshot of the
	// config.json repo registry. The run form (POST /workflows/{name}/run)
	// resolves cwd live from config.json instead, so a repo added at runtime
	// is runnable without a restart; this field is the daemon's start-time view.
	Repos items.Repos
	// WorkflowsDir is the catalog root scanned by GET /workflows for
	// `<name>/pipeline.dot` definitions (web-ui-spec W2). Empty defaults to
	// ~/.attractor/pipelines.
	WorkflowsDir string
	// Launcher selects where runs execute by default. Nil defaults to the
	// in-process `direct` launcher (decision D5); `local`/`vm` spawn
	// phone-home subprocesses / VMs.
	Launcher Launcher
	// Launchers are named launchers a submission may select per run via a
	// `runner` field (V18), e.g. {"vm": …} so one daemon can run most
	// pipelines `direct` but a test run in a `vm`. Unknown names fall back
	// to Launcher.
	Launchers map[string]Launcher
	// DefaultRunner names the launcher a dispatch lands in when neither the
	// submission nor the repo config declares one — the daemon --runner
	// default (per-repo VM config, VM3). Empty defaults to "direct", matching
	// the default Launcher.
	DefaultRunner string
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
		launcher:       cfg.Launcher,
		launchers:      cfg.Launchers,
		defaultRunner:  cfg.DefaultRunner,
		automationsDir: cfg.AutomationsDir,
		workflowsDir:   cfg.WorkflowsDir,
		sources:        cfg.Sources,
		repos:          cfg.Repos,
	}
	if s.launcher == nil {
		s.launcher = directLauncher{}
	}
	if s.defaultRunner == "" {
		s.defaultRunner = "direct"
	}
	// Stamp the run's host jj change-id range around launch (T9c): the base at
	// start and the tip once the run's commits have landed, so GET /diff can
	// serve `jj diff --from base --to tip`. Both probes skip VM/non-jj runs.
	s.dispatcher.launch = func(r *Run) {
		r.recordRevBase()
		_ = s.launcherFor(r.placement).Launch(r, s.reportURL())
		r.recordRevTip()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /pipelines", s.submitPipeline)
	mux.HandleFunc("GET /pipelines", s.listPipelines)
	mux.HandleFunc("GET /pipelines/{id}", s.getPipeline)
	mux.HandleFunc("GET /pipelines/{id}/events", s.streamEvents)
	mux.HandleFunc("POST /pipelines/{id}/cancel", s.cancelPipeline)
	mux.HandleFunc("POST /pipelines/{id}/restart", s.restartPipeline)
	mux.HandleFunc("POST /pipelines/{id}/events", s.ingestEvent)
	mux.HandleFunc("GET /pipelines/{id}/control", s.control)
	mux.HandleFunc("POST /pipelines/{id}/artifacts/{path...}", s.putArtifact)
	mux.HandleFunc("GET /pipelines/{id}/graph", s.getGraph)
	mux.HandleFunc("GET /pipelines/{id}/diff", s.getRunDiff)
	mux.HandleFunc("GET /pipelines/{id}/artifacts", s.listArtifacts)
	mux.HandleFunc("GET /pipelines/{id}/artifacts/{path...}", s.getArtifact)
	mux.HandleFunc("GET /pipelines/{id}/stages/{node}", s.getStage)
	mux.HandleFunc("GET /pipelines/{id}/questions", s.listQuestions)
	mux.HandleFunc("POST /pipelines/{id}/questions/{qid}/answer", s.answerQuestion)
	mux.HandleFunc("GET /pipelines/{id}/checkpoint", s.getCheckpoint)
	mux.HandleFunc("GET /pipelines/{id}/context", s.getContext)
	httpapi.Register(mux, itemsDeps{s})
	mux.HandleFunc("GET /config", s.getConfig)
	mux.HandleFunc("PUT /config", s.putConfig)
	mux.HandleFunc("GET /repos", s.listRepos)
	mux.HandleFunc("GET /workflows", s.listWorkflows)
	mux.HandleFunc("GET /workflows/{name}", s.getWorkflow)
	mux.HandleFunc("POST /workflows/{name}/run", s.runWorkflow)
	mux.HandleFunc("GET /workflows/{name}/graph", s.getWorkflowGraph)
	mux.HandleFunc("GET /automations", s.listAutomations)
	mux.HandleFunc("POST /automations/{name}/run", s.runAutomation)
	mux.HandleFunc("GET /ui", s.serveUI)
	mux.HandleFunc("GET /ui/", s.serveUI)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /version", s.getVersion)
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
	var cwd, placement, image string
	var itemRef *items.ItemRef
	if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
		var payload struct {
			Dot     string            `json:"dot"`
			Vars    map[string]string `json:"vars"`
			Cwd     string            `json:"cwd"`
			Runner  string            `json:"runner"`
			Image   string            `json:"image"`
			ItemRef *items.ItemRef    `json:"item_ref"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		source = payload.Dot
		vars = payload.Vars
		cwd = payload.Cwd
		placement = payload.Runner
		image = payload.Image
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
	// A raw dot submission carries no catalog path, so no workflow_name, and
	// no repo ref — seedChecks backfills one from cwd when it is a registered
	// checkout, else the checks fall to the no-op defaults.
	id, err := s.submit(source, vars, cwd, "", tag, "", "", placement, image)
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
func (s *Server) submit(source string, vars map[string]string, cwd, repo, itemRef, workflowName, baseDir, placement, image string) (string, error) {
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
	// Resolve where this run executes: submission override > the repo's
	// declared runner/image > the daemon default (per-repo VM config, VM3),
	// rejecting an unknown runner/image before a run is registered. Read the
	// same live config seam seedChecks uses so UI edits apply without a
	// restart; a load failure leaves an empty doc, so only the submission and
	// daemon default apply.
	var doc config.Document
	if home, err := os.UserHomeDir(); err == nil {
		doc, _ = config.LoadDocument(home)
	}
	ref := resolveRepoRef(doc, repo, cwd)
	resolved, err := resolvePlacement(
		placementReq{runner: placement, image: image},
		placementReq{runner: doc.RepoRunner(ref), image: doc.RepoImage(ref)},
		s.defaultRunner,
		s.launchers,
	)
	if err != nil {
		return "", err
	}
	seed := seedChecks(seedContext(vars, itemRef), repo, cwd)
	run := s.registry.NewRun(source, prepared.Graph, prepared, s.logsRoot, s.makeHandlers, itemRef, workflowName, repo, seed)
	run.placement = resolved.runner
	run.image = resolved.image
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
// command from the central config.json entry of the run's repo (keyed by
// its owner/name ref), and falling back to a no-op (echo … ; success) when
// the run resolves to no registered repo or that repo configures none, so a
// work pipeline can reference them unconditionally (config-screen-spec C3).
//
// A cwd-only dispatch — an automation or a raw-dot run, neither of which
// carries a repo ref — backfills its identity by reverse-matching cwd
// against the registered checkouts, so it keeps the gating checks it ran
// before C3 re-keyed by ref. A non-empty repo that names no registered
// entry is logged: its checks silently no-op, which for a gate is a
// misconfiguration worth a signal (the items dispatch path already 422s an
// unmapped ref; automations do not).
// resolveRepoRef returns the repo identity a dispatch resolves to: its
// explicit owner/name ref when set, else the registered checkout whose path
// is cwd (backfilling a cwd-only automation / raw-dot dispatch that carries
// no ref), else "". Shared by static-check seeding and dispatch placement so
// both key off the same identity (config-screen-spec C3, per-repo VM config).
func resolveRepoRef(doc config.Document, repo, cwd string) string {
	if repo != "" {
		return repo
	}
	if r, ok := doc.RepoForPath(cwd); ok {
		return r
	}
	return ""
}

func seedChecks(seed map[string]string, repo, cwd string) map[string]string {
	if seed == nil {
		seed = map[string]string{}
	}
	var configured map[string]string
	if home, err := os.UserHomeDir(); err == nil {
		if doc, err := config.LoadDocument(home); err == nil {
			if repo != "" {
				if _, ok := doc.Repos[repo]; !ok {
					log.Printf("seedChecks: run names unregistered repo %q; static checks fall to no-op defaults", repo)
				}
			}
			configured = doc.ChecksForRepo(resolveRepoRef(doc, repo, cwd))
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
			// Only the parent run's own terminal ends the stream. A forwarded
			// child pipeline terminal (a nested review loop's completion/failure)
			// is not the run's — closing here would cut the stream mid-run and
			// leave a looped node stuck "running" (T9a). Mirrors the frontend.
			if isOwnTerminal(ev) {
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
	svg, err := render.SVG([]byte(run.Source()), graphEngine(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write(svg)
}

// getRunDiff serves GET /pipelines/{id}/diff: the run's produced change as a
// unified diff, derived from the jj change-id range stamped around launch
// (ui-tailwind-spec T9c). A run with no recorded range (VM / non-jj cwd)
// serves an empty 200 body so the frontend falls back to a *.diff artifact.
func (s *Server) getRunDiff(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	base, tip := run.RevBase(), run.RevTip()
	if base == "" || tip == "" || run.cwd == "" {
		return // no range: empty body, frontend falls back to the artifact
	}
	out, err := jjDiff(run.cwd, base, tip)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(out)
}

// jjDiff renders `jj diff --from base --to tip` of dir as a git-format unified
// diff (the format the UI's diffHtml parses). base/tip are daemon-recorded
// change-ids, not user input, and are passed as argv (no shell).
func jjDiff(dir, base, tip string) ([]byte, error) {
	cmd := exec.Command("jj", "-R", dir, "diff", "--git", "--from", base, "--to", tip)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("jj diff %s..%s in %q: %w", base, tip, dir, err)
	}
	return out, nil
}

// graphEngine screens an untrusted `?engine=` against the render allowlist,
// returning "" (graphviz default, dot) for a missing or unknown value so an
// unknown engine renders like the default instead of erroring or passing an
// arbitrary flag to exec. Shared by the run-graph and workflow-graph endpoints.
func graphEngine(r *http.Request) string {
	e := r.URL.Query().Get("engine")
	if render.ValidEngine(e) {
		return e
	}
	return ""
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
	if run.logsRoot == "" {
		http.NotFound(w, r) // empty root cleans to "." — never serve from CWD
		return
	}
	root := filepath.Clean(run.logsRoot)
	// Leading slash makes Clean collapse any leading ".." segments.
	full := filepath.Join(root, filepath.Clean("/"+r.PathValue("path")))
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}
	// The prefix check above is textual — it does not follow symlinks. Now that
	// the listing puts every file one click away, an artifact that is a symlink
	// to a host file must not be served: resolve links and confirm the real
	// target still lives under the (also resolved) logs root.
	if resolved, err := filepath.EvalSymlinks(full); err == nil {
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil || (resolved != realRoot && !strings.HasPrefix(resolved, realRoot+string(filepath.Separator))) {
			http.NotFound(w, r)
			return
		}
	}
	http.ServeFile(w, r, full)
}

// artifactEntry is one node of a run's logs tree, its path relative to the
// run's logsRoot (web-ui-v2-spec U4). Directories carry is_dir so the browser
// can group; files carry their size.
type artifactEntry struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"is_dir"`
}

// listArtifacts serves GET /pipelines/{id}/artifacts: the whole logs tree as a
// flat, sorted list. This is the browser's SSH replacement — every file the run
// produced (stage dirs, uploaded artifacts, events.jsonl) is discoverable and
// then fetched individually via getArtifact.
func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	entries := []artifactEntry{}
	// An empty logsRoot cleans to "." — walking that would return the daemon's
	// whole working tree as this run's artifacts. Serve nothing instead.
	if run.logsRoot == "" {
		writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
		return
	}
	root := filepath.Clean(run.logsRoot)
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || path == root {
			return nil
		}
		// Don't advertise symlinks: getArtifact refuses to follow them, so a
		// listed link would only ever 404 — and listing it hints at a target
		// outside the run. WalkDir never descends a symlinked dir either.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		e := artifactEntry{Path: filepath.ToSlash(rel), IsDir: d.IsDir()}
		if info, err := d.Info(); err == nil {
			e.Size = info.Size()
		}
		entries = append(entries, e)
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
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

// getVersion reports the running build's release version and git revision.
// Unauthenticated like /healthz so any probe can read the running build.
func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  version.Number,
		"revision": version.Get(),
	})
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
		if s.authToken == "" || r.URL.Path == "/healthz" || r.URL.Path == "/version" || isLoopback(r.RemoteAddr) {
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
