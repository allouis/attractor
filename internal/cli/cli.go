// Package cli wires the attractor binary's subcommands. Keeping the
// glue in a package (rather than main) lets tests exercise the
// commands directly.
package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/allouis/attractor/internal/backend"
	acpbackend "github.com/allouis/attractor/internal/backend/acp"
	"github.com/allouis/attractor/internal/backend/claudecode"
	"github.com/allouis/attractor/internal/backend/router"
	"github.com/allouis/attractor/internal/config"
	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/ingest"
	"github.com/allouis/attractor/internal/interviewer"
	"github.com/allouis/attractor/internal/items"
	"github.com/allouis/attractor/internal/items/source"
	"github.com/allouis/attractor/internal/lint"
	"github.com/allouis/attractor/internal/render"
	"github.com/allouis/attractor/internal/report"
	"github.com/allouis/attractor/internal/server"
	"github.com/allouis/attractor/internal/setup"
)

func cryptoRandRead(b []byte) (int, error) { return rand.Read(b) }
func hexEncode(b []byte) string            { return hex.EncodeToString(b) }

// BackendChoice selects the codergen backend wired into the CLI's
// default handler set. Selection is always explicit: there is no
// auto-detection, so a run never spawns an agent the user didn't ask
// for.
type BackendChoice string

const (
	BackendClaude     BackendChoice = "claude"
	BackendACP        BackendChoice = "acp"
	BackendSimulation BackendChoice = "simulation"
)

// Validate parses and lints the supplied .dot file. Exit-code semantics:
// any ERROR-severity diagnostic yields a non-nil error from this
// function (and the binary exits 1). Warnings are printed but do not
// cause failure.
func Validate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	positional, err := parseFlexible(fs, args)
	if err != nil {
		return err
	}
	if len(positional) < 1 {
		return fmt.Errorf("validate: expected a .dot path argument")
	}
	g, err := loadGraph(positional[0])
	if err != nil {
		return err
	}
	cfg, err := loadProviderConfig()
	if err != nil {
		return err
	}
	diags, err := lint.ValidateOrError(g, providerLintRules(cfg)...)
	printDiagnostics(os.Stdout, diags)
	return err
}

// providerLintRules returns the config-aware lint rules (service-spec
// §1). They are not part of lint.BuiltIn() because they depend on the
// machine-local provider config; callers pass them as extra rules.
func providerLintRules(cfg config.Config) []lint.Rule {
	return []lint.Rule{
		lint.ProviderKnownRule{Config: cfg},
		lint.ModelEnvMissingRule{Config: cfg},
	}
}

// Run executes a pipeline end-to-end. By default each codergen node is
// routed to a backend via the provider config (service-spec §1); the
// --backend / --acp-cmd flags are run-wide overrides that bypass it.
// The positional argument is either a path to a .dot file or a pipeline
// name that resolves via lookup (see resolvePipelinePath).
func Run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	logs := fs.String("logs", "", "directory for run artefacts (default: $XDG_DATA_HOME/attractor/runs/<run-id> or ~/.attractor/runs/<run-id>)")
	jsonOut := fs.Bool("json", false, "emit one JSON event per line on stdout")
	backendFlag := fs.String("backend", "simulation", "codergen backend: claude | acp | simulation")
	acpCmd := fs.String("acp-cmd", "", "ACP agent command for --backend acp (fallback when the graph sets no acp_command attribute)")
	hookshim := fs.String("hookshim", "", "path to hookshim binary (default: sibling of attractor)")
	humanFlag := fs.String("human", "auto", "interviewer for wait.human nodes: auto | console | approve")
	reportTo := fs.String("report-to", "", "daemon base URL to phone home to (events + artifacts); enables report mode")
	runID := fs.String("run-id", "", "run id the daemon assigned (report mode)")
	reportToken := fs.String("report-token", "", "phone-home auth token for the run (report mode)")
	baseDir := fs.String("base-dir", "", "directory @file prompts and child pipelines resolve against (default: the .dot file's directory)")
	cwd := fs.String("cwd", "", "working tree the pipeline operates in (graph-level cwd default; report mode)")
	var vars varFlags
	fs.Var(&vars, "var", "set a pipeline variable (repeatable): -var name=value")
	positional, err := parseFlexible(fs, args)
	if err != nil {
		return err
	}
	if len(positional) < 1 {
		return fmt.Errorf("run: expected a pipeline name or .dot path")
	}
	dotPath, err := resolvePipelinePath(positional[0])
	if err != nil {
		return err
	}
	src, err := os.ReadFile(dotPath)
	if err != nil {
		return err
	}
	base := *baseDir
	if base == "" {
		base = filepath.Dir(dotPath)
	}
	prepared, err := setup.Prepare(setup.Options{
		Source:  string(src),
		BaseDir: base,
		Cwd:     *cwd,
	})
	if err != nil {
		return err
	}
	g := prepared.Graph
	logsRoot := *logs
	if logsRoot == "" {
		logsRoot = filepath.Join(defaultLogsRoot(), engine.NewRunID())
	}
	if err := os.MkdirAll(logsRoot, 0o755); err != nil {
		return err
	}

	// Resolve the codergen backend. Explicit --backend / --acp-cmd are
	// run-wide overrides (debugging) that bypass provider config;
	// otherwise each codergen node is routed per its llm_provider /
	// llm_model through ./.attractor/config.toml (service-spec §1).
	var ingestSrv *ingest.Server
	var codergenBackend backend.CodergenBackend
	if flagSet(fs, "backend") || flagSet(fs, "acp-cmd") {
		choice, err := parseBackendChoice(*backendFlag)
		if err != nil {
			return err
		}
		ingestSrv, codergenBackend, err = startCodergen(choice, g, logsRoot, *hookshim, *acpCmd)
		if err != nil {
			return err
		}
	} else {
		codergenBackend, err = providerBackend(g)
		if err != nil {
			return err
		}
	}
	if ingestSrv != nil {
		defer ingestSrv.Close()
	}

	// Report mode: phone home to the daemon that launched us. There's no
	// TTY in a subprocess/VM, so a wait.human gate is answered through the
	// daemon: the PollInterviewer blocks until a human answers via the
	// daemon's /questions surface (spec §6). `--human approve` forces
	// non-interactive AutoApprove for unattended runs.
	if *reportTo != "" {
		if *runID == "" {
			return fmt.Errorf("run: --report-to requires --run-id")
		}
		client := report.New(*reportTo, *runID, *reportToken)
		var iv interviewer.Interviewer = report.NewPollInterviewer(client, 0)
		if *humanFlag == "approve" {
			iv = interviewer.AutoApprove{}
		}
		rep := &reportSink{client: client, runID: *runID}
		return runEngineReporting(prepared, codergenBackend, iv, logsRoot, *jsonOut, vars, rep)
	}

	iv := resolveInterviewer(*humanFlag)
	return runEngine(prepared, codergenBackend, iv, logsRoot, *jsonOut, vars)
}

// providerBackend builds the config-routed codergen backend used when no
// --backend override is given (service-spec §1), surfacing config-aware
// warnings (unknown provider, missing model_env) to stderr so --json
// stdout stays clean. Shared by `run` and `automations run`.
func providerBackend(g *graph.Graph) (backend.CodergenBackend, error) {
	cfg, err := loadProviderConfig()
	if err != nil {
		return nil, err
	}
	for _, rule := range providerLintRules(cfg) {
		printDiagnostics(os.Stderr, rule.Apply(g))
	}
	return router.New(cfg), nil
}

// runEngine wires the built-in handlers around a codergen backend and
// executes prepared to completion, streaming events to stdout (one JSON
// object per line when jsonOut). Shared standalone-run core of `run` and
// `automations run`. initialContext seeds the run's context with the
// `-var`/automation vars so `$context.<var>` resolves at runtime (C3).
func runEngine(prepared *engine.PreparedGraph, cb backend.CodergenBackend, iv interviewer.Interviewer, logsRoot string, jsonOut bool, initialContext map[string]string) error {
	return runEngineReporting(prepared, cb, iv, logsRoot, jsonOut, initialContext, nil)
}

// reportSink phones a run's activity home to a daemon (phone-home mode).
// nil means a standalone `attractor run` that only prints to stdout.
type reportSink struct {
	client *report.Client
	runID  string
}

// runEngineReporting is runEngine plus optional phone-home reporting: when
// rep is non-nil it stamps the daemon's run id on the engine, forwards
// every event to the daemon as it happens, and uploads the run's stage
// artifacts on completion (skipping files the daemon owns itself).
func runEngineReporting(prepared *engine.PreparedGraph, cb backend.CodergenBackend, iv interviewer.Interviewer, logsRoot string, jsonOut bool, initialContext map[string]string, rep *reportSink) error {
	cfg := engine.Config{Registry: buildRegistryWith(handler.Codergen{Backend: cb}, iv), LogsRoot: logsRoot, InitialContext: initialContext}
	if rep != nil {
		cfg.RunID = rep.runID
	}
	eng := engine.New(cfg)
	done := make(chan struct{})
	uploaded := false
	go func() {
		for ev := range eng.Events() {
			// Upload the run's stage artifacts before forwarding the terminal
			// event. The daemon marks the run terminal the moment it ingests
			// pipeline_completed/failed (registry.finishFromEvent), so a run
			// reported terminal must already carry its artifacts — the run-
			// detail UI and the phone-home end-to-end contract read them as
			// soon as status flips. Every node's status.json/prompt/response
			// is flushed by now (the engine writes them before this event).
			if rep != nil && !uploaded && isTerminalEvent(ev.Kind) {
				_ = rep.client.UploadDir(logsRoot, daemonOwnedArtifact)
				uploaded = true
			}
			// Per-stage sync: when a stage ends, upload just that node's dir
			// (logsRoot/<node>/, nested child dirs included) before forwarding
			// the event, so GET /stages/{node} serves the completed stage of a
			// still-live run under every runner. Same goroutine as the event
			// forward → race-free; best-effort since the terminal sweep above
			// is the catch-all if a per-stage upload fails.
			if rep != nil && isStageDoneEvent(ev.Kind) && ev.NodeID != "" {
				_ = rep.client.UploadStageDir(logsRoot, ev.NodeID, daemonOwnedArtifact)
			}
			if rep != nil {
				_ = rep.client.Event(ev)
			}
			if jsonOut {
				_ = json.NewEncoder(os.Stdout).Encode(ev)
				continue
			}
			fmt.Printf("[%s] %-22s node=%s %s\n",
				ev.Timestamp.Format("15:04:05.000"), ev.Kind, ev.NodeID, ev.Message)
		}
		close(done)
	}()
	outcome, runErr := eng.Run(prepared)
	<-done
	// Fallback: a run that ended without a terminal event (an engine error
	// before pipeline_completed) still uploads whatever it produced.
	if rep != nil && !uploaded {
		_ = rep.client.UploadDir(logsRoot, daemonOwnedArtifact)
	}
	fmt.Printf("\npipeline %s logs=%s\n", outcome.Status, logsRoot)
	if runErr != nil {
		return runErr
	}
	if outcome.Status == engine.StatusFail {
		return fmt.Errorf("pipeline failed: %s", outcome.FailureReason)
	}
	return nil
}

// isTerminalEvent reports whether ev ends the run — the event whose ingest
// flips the daemon's run to a terminal state (registry.finishFromEvent).
func isTerminalEvent(k engine.EventKind) bool {
	return k == engine.EventPipelineCompleted || k == engine.EventPipelineFailed
}

// isStageDoneEvent reports whether ev ends a single stage — the point at
// which that node's dir is final enough to phone home incrementally. A
// failed stage still has files worth serving, so it uploads too.
func isStageDoneEvent(k engine.EventKind) bool {
	return k == engine.EventStageCompleted || k == engine.EventStageFailed
}

// daemonOwnedArtifact reports whether a run-dir file is one the daemon
// writes itself and must not be overwritten by a phone-home upload: the
// daemon builds events.jsonl from ingested events, and stamps
// manifest.json / source.dot at run creation.
//
// run.json is deliberately NOT here: post-P5a it is the CHILD's identity
// record (engine.writeManifest), disjoint from the daemon's manifest.json, so
// uploading it is harmless and keeps the daemon's run dir a faithful copy of
// the child's — consistent with every other child-owned artifact.
func daemonOwnedArtifact(rel string) bool {
	switch rel {
	case "events.jsonl", "manifest.json", "source.dot":
		return true
	}
	return false
}

// Render shells the input .dot file through graphviz to produce SVG.
// The output destination defaults to stdout; -o writes to a file.
func Render(args []string) error {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	output := fs.String("o", "", "output path (default: stdout)")
	positional, err := parseFlexible(fs, args)
	if err != nil {
		return err
	}
	if len(positional) < 1 {
		return fmt.Errorf("render: expected a .dot path argument")
	}
	src, err := os.ReadFile(positional[0])
	if err != nil {
		return err
	}
	svg, err := render.SVG(src, "")
	if err != nil {
		return err
	}
	if *output == "" {
		_, err := os.Stdout.Write(svg)
		return err
	}
	return os.WriteFile(*output, svg, 0o644)
}

// Serve runs the Attractor HTTP server (§9.5). Loopback is the default
// bind; non-loopback binds require either `--auth-token` (bearer-token
// gate) or `--insecure` (Tailscale-pattern, where network ACLs do the
// gatekeeping).
func Serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	bind := fs.String("bind", "127.0.0.1:7681", "TCP bind address; non-loopback requires --auth-token or --insecure")
	logs := fs.String("logs", defaultLogsRoot(), "base directory for pipeline run artefacts")
	authToken := fs.Bool("auth-token", false, "enable bearer-token auth (token at ~/.attractor/api-key, auto-generated on first use)")
	insecure := fs.Bool("insecure", false, "allow non-loopback bind without auth (network layer is responsible)")
	maxConcurrent := fs.Int("max-concurrent-runs", 4, "maximum runs executing at once; the rest queue FIFO")
	runner := fs.String("runner", "direct", "where runs execute: direct (in-process) | local (subprocess) | vm (nixos VM)")
	var vmRunners repeatedString
	fs.Var(&vmRunners, "vm-runner", "run-nixos-vm boot script for --runner vm; `name=path` registers a named image, a bare path is the default image (repeatable; default: build .#vm-runner)")
	vmDir := fs.String("vm-dir", "", "root for per-run VM disks + job dirs (--runner vm; default: <logs>/vms)")
	vmRetention := fs.Duration("vm-retention", 72*time.Hour, "how long a completed VM persists before the reaper GCs it (--runner vm)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	vmDirResolved := defaultString(*vmDir, filepath.Join(*logs, "vms"))
	cliImages, err := parseVMImages(vmRunners)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	configImages, err := serveVMImages(home)
	if err != nil {
		return err
	}
	launcher, launchers, err := resolveLaunchers(*runner, cliImages, configImages, vmDirResolved)
	if err != nil {
		return err
	}
	if _, hasVM := launchers["vm"]; hasVM {
		server.StartVMReaper(vmDirResolved, *vmRetention)
		fmt.Fprintf(os.Stderr, "attractor: VM reaper on — completed VMs GC'd after %s\n", *vmRetention)
	}
	if !bindIsLoopback(*bind) && !*authToken && !*insecure {
		return fmt.Errorf("serve: bind %q is not loopback; pass --auth-token (recommended) or --insecure", *bind)
	}
	token := ""
	if *authToken {
		t, err := loadOrGenerateAPIKey()
		if err != nil {
			return err
		}
		token = t
		fmt.Fprintf(os.Stderr, "attractor: bearer token stored at %s — clients must send `Authorization: Bearer <token>`\n", apiKeyPath())
	}
	handlers, err := ServeHandlerFactory()
	if err != nil {
		return err
	}
	sources, err := serveSources()
	if err != nil {
		return err
	}
	repos, err := serveRepos(home)
	if err != nil {
		return err
	}
	srv := server.New(server.Config{
		Addr:              *bind,
		LogsRoot:          *logs,
		MakeHandlers:      handlers,
		AuthToken:         token,
		MaxConcurrentRuns: *maxConcurrent,
		AutomationsDir:    automationsDir(),
		Sources:           sources,
		Repos:             repos,
		Launcher:          launcher,
		Launchers:         launchers,
		DefaultRunner:     *runner,
	})
	if err := srv.Start(); err != nil {
		return err
	}
	fmt.Println("attractor serving on", srv.URL())
	select {}
}

// repeatedString collects a repeatable string flag (each --flag appends).
type repeatedString []string

func (r *repeatedString) String() string { return strings.Join(*r, ",") }

func (r *repeatedString) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// parseVMImages turns the repeatable --vm-runner flag into a name -> boot
// script registry (VM1). A `name=path` entry registers that named image; a
// bare path (no `=`) registers the "default" image. An empty name or a
// duplicate name is rejected so a misconfiguration fails loudly at startup.
func parseVMImages(flags []string) (map[string]string, error) {
	images := map[string]string{}
	for _, f := range flags {
		name, path := "default", f
		if before, after, found := strings.Cut(f, "="); found {
			name, path = before, after
		}
		if name == "" {
			return nil, fmt.Errorf("serve: --vm-runner %q has an empty image name", f)
		}
		if path == "" {
			return nil, fmt.Errorf("serve: --vm-runner image %q has an empty boot-script path", name)
		}
		if _, dup := images[name]; dup {
			return nil, fmt.Errorf("serve: --vm-runner image %q set more than once", name)
		}
		images[name] = path
	}
	return images, nil
}

// resolveLaunchers builds the named launcher registry and picks the
// default per --runner. All built launchers are available for per-run
// override via a submission's `runner` field (V18). Activation of the vm
// launcher is gated on *explicit* intent — `--runner vm` or a --vm-runner
// flag (cliImages) — never on mere config presence: a config.json vm_images
// edit must not silently start the reaper, create vmDir, or trigger a
// blocking `nix build .#vm-runner` at startup (which would break serve when
// nix is absent). When VMs are enabled, configImages supply the registry
// and cliImages override by name; a missing "default" is filled by building
// .#vm-runner (VM1).
func resolveLaunchers(choice string, cliImages, configImages map[string]string, vmDir string) (def server.Launcher, all map[string]server.Launcher, err error) {
	all = map[string]server.Launcher{
		"direct": server.NewDirectLauncher(),
		"local":  server.NewLocalLauncher(),
	}
	if choice == "vm" || len(cliImages) > 0 {
		images := make(map[string]string, len(configImages)+len(cliImages)+1)
		for name, script := range configImages {
			images[name] = script
		}
		for name, script := range cliImages {
			images[name] = script
		}
		if images["default"] == "" {
			script, berr := buildVMRunner()
			if berr != nil {
				return nil, nil, berr
			}
			images["default"] = script
		}
		if err = os.MkdirAll(vmDir, 0o755); err != nil {
			return nil, nil, err
		}
		all["vm"] = server.NewVMLauncherWithImages(images, "default", vmDir)
	}
	def, ok := all[choice]
	if !ok {
		return nil, nil, fmt.Errorf("serve: unknown --runner %q (want direct | local | vm)", choice)
	}
	return def, all, nil
}

// buildVMRunner builds the .#vm-runner flake output and returns the path to
// its run-nixos-vm boot script.
func buildVMRunner() (string, error) {
	fmt.Fprintln(os.Stderr, "attractor: building .#vm-runner (first run may take a while)…")
	out, err := exec.Command("nix", "build", "--no-link", "--print-out-paths", ".#vm-runner").Output()
	if err != nil {
		return "", fmt.Errorf("build .#vm-runner: %w", err)
	}
	return filepath.Join(strings.TrimSpace(string(out)), "bin", "run-nixos-vm"), nil
}

// defaultString returns v, or def when v is empty.
func defaultString(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// ServeHandlerFactory builds the server's handler factory from the
// machine-local provider config (service-spec §1–2): each codergen node
// is routed to a backend via the router, giving `serve` the same real
// backends as `run`. This replaces the former simulation-only wiring.
func ServeHandlerFactory() (server.HandlerFactory, error) {
	cfg, err := loadProviderConfig()
	if err != nil {
		return nil, err
	}
	return server.DefaultHandlers(handler.Codergen{Backend: router.New(cfg)}), nil
}

// serveSources builds the daemon's Source map (items-spec §8): GitHub
// via the machine-authed `gh` CLI (always available), and Linear when a
// config API key is present. GET /items dispatches to these by name.
func serveSources() (map[string]source.Source, error) {
	cfg, err := loadProviderConfig()
	if err != nil {
		return nil, err
	}
	sources := map[string]source.Source{"github": source.NewGitHub()}
	if cfg.LinearAPIKey != "" {
		sources["linear"] = source.NewLinear(cfg.LinearAPIKey)
	}
	return sources, nil
}

// serveRepos projects the daemon-owned config.json onto the repo→path map
// (items-spec I3) the server boots with. A missing config.json is not an
// error: the fresh default's empty map.
func serveRepos(home string) (items.Repos, error) {
	doc, err := config.LoadDocument(home)
	if err != nil {
		return nil, err
	}
	return items.Repos(doc.ReposMap()), nil
}

// serveVMImages projects the daemon-owned config.json onto the named VM
// boot-image registry (VM1). A missing config.json is not an error: the
// fresh default has no images and the vm launcher falls back to building
// .#vm-runner. Returns a fresh map so serve can overlay --vm-runner flags.
func serveVMImages(home string) (map[string]string, error) {
	doc, err := config.LoadDocument(home)
	if err != nil {
		return nil, err
	}
	images := make(map[string]string, len(doc.VMImages))
	for name, script := range doc.VMImages {
		if script == "" {
			return nil, fmt.Errorf("serve: config vm_images[%q] has an empty boot-script path", name)
		}
		images[name] = script
	}
	return images, nil
}

// bindIsLoopback returns true for 127.0.0.1, ::1, or localhost bind
// strings of the form host:port.
func bindIsLoopback(bind string) bool {
	host := bind
	if i := strings.LastIndex(bind, ":"); i >= 0 {
		host = bind[:i]
	}
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	switch host {
	case "127.0.0.1", "::1", "localhost", "":
		return true
	}
	return false
}

// apiKeyPath returns the canonical location for the bearer token.
func apiKeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".attractor", "api-key")
}

// loadOrGenerateAPIKey returns the existing bearer token (rejecting any
// trailing whitespace) or generates a new one persisted with 0600 mode.
func loadOrGenerateAPIKey() (string, error) {
	path := apiKeyPath()
	if data, err := os.ReadFile(path); err == nil {
		token := strings.TrimSpace(string(data))
		if token != "" {
			return token, nil
		}
	}
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

func randomToken(byteLen int) (string, error) {
	buf := make([]byte, byteLen)
	if _, err := cryptoRandRead(buf); err != nil {
		return "", err
	}
	return hexEncode(buf), nil
}

// defaultLogsRoot returns the persistent location for run artefacts:
// $XDG_DATA_HOME/attractor/runs or ~/.attractor/runs.
func defaultLogsRoot() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "attractor", "runs")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".attractor", "runs")
	}
	return ".attractor-runs"
}

// ---------------------------------------------------------------------------

// flagSet reports whether the named flag was explicitly provided on the
// command line (as opposed to sitting at its default).
func flagSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// loadProviderConfig reads the provider routing config, projected from
// the daemon-owned ~/.attractor/config.json (config-screen-spec). The CLI
// is a read-only consumer; the daemon owns the writes.
func loadProviderConfig() (config.Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	doc, err := config.LoadDocument(home)
	if err != nil {
		return config.Config{}, err
	}
	return doc.ProviderConfig(), nil
}

// parseBackendChoice validates the --backend flag value.
func parseBackendChoice(raw string) (BackendChoice, error) {
	switch c := BackendChoice(raw); c {
	case BackendClaude, BackendACP, BackendSimulation:
		return c, nil
	}
	return "", fmt.Errorf("run: unknown backend %q (valid: claude, acp, simulation)", raw)
}

// startCodergen constructs the codergen backend matching `choice`. For
// the Claude path it also starts an ingest server scoped to the run so
// hook events flow back into the engine and tool_hooks.pre/post fire.
// The ACP path needs neither: tool visibility comes over the protocol
// itself. The returned ingest.Server may be nil.
func startCodergen(choice BackendChoice, g *graph.Graph, logsRoot, hookshimOverride, acpCmd string) (*ingest.Server, backend.CodergenBackend, error) {
	if choice == BackendSimulation {
		return nil, nil, nil
	}
	if choice == BackendACP {
		return nil, &acpbackend.Backend{Command: acpCmd}, nil
	}
	shim := hookshimOverride
	if shim == "" {
		shim = findHookshim()
	}
	srv, err := ingest.StartWith(ingest.Config{
		LogsRoot:    logsRoot,
		PreToolCmd:  g.Attrs["tool_hooks.pre"],
		PostToolCmd: g.Attrs["tool_hooks.post"],
	})
	if err != nil {
		return nil, nil, fmt.Errorf("ingest: %w", err)
	}
	be := &claudecode.Backend{
		HookShimBin: shim,
		IngestURL:   srv.URL(),
		FallbackDir: filepath.Join(logsRoot, "_ingest_fallback"),
	}
	return srv, be, nil
}

// findHookshim looks for the hookshim binary sibling-of-attractor first
// (the nix build places both in /bin), then on PATH. Returns the empty
// string when not found; the Claude backend tolerates this with reduced
// (no-hook) functionality.
func findHookshim() string {
	if exe, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(exe), "hookshim")
		if _, err := os.Stat(sibling); err == nil {
			return sibling
		}
	}
	if p, err := exec.LookPath("hookshim"); err == nil {
		return p
	}
	return ""
}

func loadGraph(path string) (*graph.Graph, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	file, err := dot.Parse(string(src))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return graph.Build(file)
}

// buildRegistryWith wires every built-in handler, parameterised by the
// codergen handler and the interviewer used by wait.human nodes.
func buildRegistryWith(codergen handler.Codergen, iv interviewer.Interviewer) *engine.Registry {
	r := engine.NewRegistry()
	r.Register("start", handler.Start{})
	r.Register("exit", handler.Exit{})
	r.Register("conditional", handler.Conditional{})
	r.Register("wait.human", handler.WaitHuman{Interviewer: iv})
	r.Register("tool", handler.Tool{})
	r.Register("parallel", handler.Parallel{})
	r.Register("parallel.fan_in", handler.FanIn{})
	r.Register("stack.manager_loop", handler.ManagerLoop{})
	r.Register("codergen", codergen)
	r.Register("codergen.claude", codergen)
	r.Register("codergen.acp", codergen)
	r.SetDefault(codergen)
	return r
}

// resolveInterviewer picks the wait.human implementation per the
// --human flag. `auto` uses the console when stdin is a TTY, else
// auto-approve so non-interactive runs (CI, server contexts) don't
// stall.
func resolveInterviewer(choice string) interviewer.Interviewer {
	switch choice {
	case "console":
		return interviewer.Console{}
	case "approve":
		return interviewer.AutoApprove{}
	}
	if isStdinTTY() {
		return interviewer.Console{}
	}
	return interviewer.AutoApprove{}
}

// isStdinTTY reports whether stdin is connected to a terminal.
func isStdinTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// parseFlexible processes flag arguments interleaved with positional
// arguments. Go's stdlib flag parser stops at the first non-flag, so
// `attractor run bug-fix --var foo=bar` would leave `--var` unparsed.
// We call Parse in a loop: each pass consumes flags until a positional
// appears, we save it, then continue with the remaining args.
func parseFlexible(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	remaining := args
	for {
		if err := fs.Parse(remaining); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		remaining = fs.Args()[1:]
	}
}

func printDiagnostics(w io.Writer, diags []lint.Diagnostic) {
	for _, d := range diags {
		ref := d.NodeID
		if ref == "" && (d.EdgeFrom != "" || d.EdgeTo != "") {
			ref = d.EdgeFrom + "->" + d.EdgeTo
		}
		fmt.Fprintf(w, "%-7s %-30s %-30s %s\n", d.Severity, d.Rule, ref, d.Message)
	}
}

// varFlags is a repeatable -var name=value flag. The CLI seeds this map
// into the run's initial context, so `$context.<name>` interpolates at
// runtime (spec §4.5).
type varFlags map[string]string

// String returns a stable comma-joined form for `-h` help output.
func (v varFlags) String() string {
	if v == nil {
		return ""
	}
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ","
		}
		out += k + "=" + v[k]
	}
	return out
}

// Set implements flag.Value; called once per -var occurrence.
func (v *varFlags) Set(raw string) error {
	if *v == nil {
		*v = map[string]string{}
	}
	idx := strings.IndexByte(raw, '=')
	if idx <= 0 {
		return fmt.Errorf("-var expects name=value, got %q", raw)
	}
	(*v)[raw[:idx]] = raw[idx+1:]
	return nil
}

// resolvePipelinePath turns a name-or-path argument into an absolute
// .dot path. Lookup order when arg is a bare name (no `/`, no `.dot`
// suffix):
//
//  1. ./pipelines/<name>/pipeline.dot
//  2. ./pipelines/<name>.dot
//  3. ~/.attractor/pipelines/<name>/pipeline.dot
//  4. ~/.attractor/pipelines/<name>.dot
//
// Paths with a separator or .dot extension are returned verbatim
// (relative paths resolved against the current working directory).
func resolvePipelinePath(arg string) (string, error) {
	if strings.ContainsAny(arg, string(os.PathSeparator)) || strings.HasSuffix(arg, ".dot") {
		if _, err := os.Stat(arg); err != nil {
			return "", err
		}
		abs, err := filepath.Abs(arg)
		if err != nil {
			return arg, nil
		}
		return abs, nil
	}
	var tried []string
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join("pipelines", arg, "pipeline.dot"),
		filepath.Join("pipelines", arg+".dot"),
	}
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".attractor", "pipelines", arg, "pipeline.dot"),
			filepath.Join(home, ".attractor", "pipelines", arg+".dot"),
		)
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			abs, err := filepath.Abs(c)
			if err != nil {
				return c, nil
			}
			return abs, nil
		}
		tried = append(tried, c)
	}
	return "", fmt.Errorf("pipeline %q not found; tried:\n  %s", arg, strings.Join(tried, "\n  "))
}
