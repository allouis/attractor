// Package cli wires the attractor binary's subcommands. Keeping the
// glue in a package (rather than main) lets tests exercise the
// commands directly.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/allouis/attractor/internal/backend"
	acpbackend "github.com/allouis/attractor/internal/backend/acp"
	"github.com/allouis/attractor/internal/backend/claudecode"
	"github.com/allouis/attractor/internal/backend/router"
	"github.com/allouis/attractor/internal/config"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/hub"
	"github.com/allouis/attractor/internal/interviewer"
	"github.com/allouis/attractor/internal/lint"
	"github.com/allouis/attractor/internal/render"
	"github.com/allouis/attractor/internal/runserver"
	"github.com/allouis/attractor/internal/runview"
	"github.com/allouis/attractor/internal/setup"
)

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

// Validate parses, transforms, and lints the supplied .dot file. The
// standard transforms run first so validation sees what the engine
// would execute: @file prompts are inlined (their $context.* references
// count for the context_refs rule, and a missing prompt file fails
// here instead of mid-run). Exit-code semantics: any ERROR-severity
// diagnostic yields a non-nil error from this function (and the binary
// exits 1). Warnings are printed but do not cause failure.
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
	path := positional[0]
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	pg, err := setup.Prepare(setup.Options{
		Source:  string(src),
		BaseDir: filepath.Dir(path),
	})
	if err != nil {
		return err
	}
	cfg, err := loadProviderConfig()
	if err != nil {
		return err
	}
	diags, err := lint.ValidateOrError(pg.Graph, providerLintRules(cfg)...)
	printDiagnostics(os.Stdout, diags)
	return err
}

// providerLintRules returns the config-aware lint rules. They are not
// part of lint.BuiltIn() because they depend on the
// machine-local provider config; callers pass them as extra rules.
func providerLintRules(cfg config.Config) []lint.Rule {
	return []lint.Rule{
		lint.ProviderKnownRule{Config: cfg},
		lint.ModelEnvMissingRule{Config: cfg},
	}
}

// Run executes a pipeline end-to-end. By default each codergen node is
// routed to a backend via the provider config (docs/provider-config.md); the
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
	humanFlag := fs.String("human", "auto", "interviewer for wait.human nodes: auto | console | approve")
	baseDir := fs.String("base-dir", "", "directory @file prompts and child pipelines resolve against (default: the .dot file's directory)")
	cwd := fs.String("cwd", "", "working tree the pipeline operates in (graph-level cwd default)")
	ui := fs.Bool("ui", false, "serve this run's own read-only API + waterfall UI while it runs (local-first single-run server)")
	uiAddr := fs.String("ui-addr", "127.0.0.1:0", "listen address for --ui; overrides the default auto behaviour (ephemeral loopback port, plus the Tailscale tailnet IP when present). A public/LAN --ui-addr requires --ui-token")
	announce := fs.String("announce", "", "hub base URL: register this run's --ui URL at start and ship the run-dir archive on completion (implies --ui)")
	uiToken := fs.String("ui-token", "", "require `Authorization: Bearer <token>` on the loopback and any public/LAN bind (mandatory for a public/LAN --ui-addr); the tailnet bind stays token-free. Shared with the hub via announce")
	var vars varFlags
	fs.Var(&vars, "var", "set a pipeline variable (repeatable): -var name=value")
	var stylesheets stringListFlag
	fs.Var(&stylesheets, "stylesheet", "external model stylesheet file (repeatable; later files cascade over earlier): --stylesheet models.css")
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
	sheet, err := readStylesheets(stylesheets)
	if err != nil {
		return err
	}
	prepared, err := setup.Prepare(setup.Options{
		Source:     string(src),
		BaseDir:    base,
		Cwd:        *cwd,
		Stylesheet: sheet,
	})
	if err != nil {
		return err
	}
	g := prepared.Graph
	// Mint the run id once and name the default run dir after it, so the
	// directory `attractor runs`/`view` operate on matches the RunID the
	// engine stamps in run.json (and the hub announces/archives under).
	localRunID := engine.NewRunID()
	logsRoot := *logs
	if logsRoot == "" {
		logsRoot = filepath.Join(defaultLogsRoot(), localRunID)
	}
	if err := os.MkdirAll(logsRoot, 0o755); err != nil {
		return err
	}

	// Resolve the codergen backend. Explicit --backend / --acp-cmd are
	// run-wide overrides (debugging) that bypass provider config;
	// otherwise each codergen node is routed per its llm_provider /
	// llm_model through ~/.attractor/config.json (see docs/provider-config.md).
	var codergenBackend backend.CodergenBackend
	if flagSet(fs, "backend") || flagSet(fs, "acp-cmd") {
		choice, err := parseBackendChoice(*backendFlag)
		if err != nil {
			return err
		}
		codergenBackend, err = buildCodergen(choice, *acpCmd)
		if err != nil {
			return err
		}
	} else {
		codergenBackend, err = providerBackend(g, len(stylesheets) > 0)
		if err != nil {
			return err
		}
	}

	iv := resolveInterviewer(*humanFlag)
	if *ui || *announce != "" {
		srv := runserver.New(logsRoot)
		srv.Meta = nodeMeta(g)
		srv.Token = *uiToken
		// Gates are answered over the run's own /answer endpoint unless
		// the user explicitly asked for a console/approve interviewer.
		if !flagSet(fs, "human") {
			gate := runserver.NewGate()
			srv.Answer = gate.Answer
			iv = gate
		}
		// Classify trust against the host's tailnet whenever the UI server
		// starts (--ui or --announce), so an explicit tailnet --ui-addr is
		// trusted the same regardless of --ui. Only the automatic second
		// bind is gated on --ui, so an announce-only run opens no extra port.
		tailnet := hostTailnetIPs()
		lns, primary, err := serveRunUI(srv, *uiAddr, flagSet(fs, "ui-addr"), *ui, tailnet, os.Stderr)
		if err != nil {
			return err
		}
		for _, ln := range lns {
			defer ln.Close()
		}
		if *announce != "" {
			// One-shot registration (not telemetry): the hub pulls
			// everything else from this run's own API. Announce the
			// primary (loopback) URL + token, as today.
			if err := hub.Announce(*announce, localRunID, "http://"+primary.Addr().String(), *uiToken); err != nil {
				fmt.Fprintf(os.Stderr, "run: hub announce failed (continuing standalone): %v\n", err)
			}
		}
	}
	runErr := runEngineWithID(prepared, codergenBackend, iv, logsRoot, *jsonOut, vars, localRunID)
	if *announce != "" {
		// Archive-on-complete (any outcome): the tar IS the permanent
		// record; ship before any teardown.
		if err := hub.ShipArchive(*announce, localRunID, logsRoot); err != nil {
			fmt.Fprintf(os.Stderr, "run: hub archive failed (run dir remains at %s): %v\n", logsRoot, err)
		}
	}
	return runErr
}

// nodeMeta extracts the per-node metadata the run server attaches to
// spans (D5: self-describing spans so lane grouping is a frontend
// groupBy). Class buckets the handler type for coarse grouping.
func nodeMeta(g *graph.Graph) map[string]runview.NodeMeta {
	meta := make(map[string]runview.NodeMeta, len(g.Nodes))
	for id, n := range g.Nodes {
		typ := n.Attrs["type"]
		if typ == "" {
			switch {
			case n.Shape() == "Mdiamond" || id == "start" || id == "Start":
				typ = "start"
			case n.Shape() == "Msquare" || id == "exit" || id == "end":
				typ = "exit"
			case n.Shape() == "hexagon":
				typ = "wait.human"
			case n.Shape() == "diamond":
				typ = "conditional"
			case n.Shape() == "component":
				typ = "parallel"
			default:
				typ = "codergen"
			}
		}
		class := "flow"
		switch {
		case strings.HasPrefix(typ, "codergen"):
			class = "agent"
		case typ == "tool":
			class = "tool"
		case typ == "wait.human":
			class = "gate"
		case strings.HasPrefix(typ, "parallel"):
			class = "parallel"
		}
		meta[id] = runview.NodeMeta{
			Type:     typ,
			Class:    class,
			Model:    n.Attrs["llm_model"],
			ThreadID: n.Attrs["thread_id"],
		}
	}
	return meta
}

// providerBackend builds the config-routed codergen backend used when no
// --backend override is given (docs/provider-config.md), surfacing config-aware
// warnings (unknown provider, missing model_env) to stderr so --json
// stdout stays clean.
func providerBackend(g *graph.Graph, stylesheetProvided bool) (backend.CodergenBackend, error) {
	cfg, err := loadProviderConfig()
	if err != nil {
		return nil, err
	}
	for _, rule := range providerLintRules(cfg) {
		printDiagnostics(os.Stderr, rule.Apply(g))
	}
	// Strict when the run clearly intends real agents: a stylesheet was
	// supplied, or a default_provider is configured. A bare dev run stays
	// lenient so a model-less graph can still simulate.
	strict := stylesheetProvided || strings.TrimSpace(cfg.DefaultProvider) != ""
	return router.NewStrict(cfg, strict), nil
}

// runEngine wires the built-in handlers around a codergen backend and
// executes prepared to completion, streaming events to stdout (one JSON
// object per line when jsonOut). initialContext seeds the run's context
// with the `-var` vars so `$context.<var>` resolves at runtime (C3).
func runEngine(prepared *engine.PreparedGraph, cb backend.CodergenBackend, iv interviewer.Interviewer, logsRoot string, jsonOut bool, initialContext map[string]string) error {
	return runEngineWithID(prepared, cb, iv, logsRoot, jsonOut, initialContext, "")
}

// runEngineWithID is runEngine with an explicit run id, so the single-run
// server and hub announce/archive speak the same id the engine stamps on
// its events and run.json. Empty means mint one.
func runEngineWithID(prepared *engine.PreparedGraph, cb backend.CodergenBackend, iv interviewer.Interviewer, logsRoot string, jsonOut bool, initialContext map[string]string, runID string) error {
	return runEngineFull(prepared, cb, iv, logsRoot, jsonOut, initialContext, runID)
}

// runEngineFull is the shared engine-run core.
func runEngineFull(prepared *engine.PreparedGraph, cb backend.CodergenBackend, iv interviewer.Interviewer, logsRoot string, jsonOut bool, initialContext map[string]string, runID string) error {
	cfg := engine.Config{Registry: buildRegistryWith(handler.Codergen{Backend: cb}, iv), LogsRoot: logsRoot, InitialContext: initialContext, RunID: runID}
	if logsRoot != "" {
		// Persist the executed topology so the run server's /graph
		// endpoint (and the archive) can render it. Best-effort.
		_ = os.WriteFile(filepath.Join(logsRoot, "graph.dot"), render.MinimalDOT(prepared.Graph), 0o644)
	}
	eng := engine.New(cfg)
	done := make(chan struct{})
	go func() {
		for ev := range eng.Events() {
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
	fmt.Printf("\npipeline %s logs=%s\n", outcome.Status, logsRoot)
	if runErr != nil {
		return runErr
	}
	if outcome.Status == engine.StatusFail {
		return fmt.Errorf("pipeline failed: %s", outcome.FailureReason)
	}
	return nil
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

// loadProviderConfig reads the provider routing config from
// ~/.attractor/config.json (see docs/provider-config.md).
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

// buildCodergen constructs the codergen backend matching `choice`. ACP
// tool visibility comes over the protocol itself; the claude CLI wrap
// needs no side channels either.
func buildCodergen(choice BackendChoice, acpCmd string) (backend.CodergenBackend, error) {
	switch choice {
	case BackendSimulation:
		return nil, nil
	case BackendACP:
		return &acpbackend.Backend{Command: acpCmd}, nil
	case BackendClaude:
		return &claudecode.Backend{}, nil
	}
	return nil, fmt.Errorf("run: unknown backend %q", choice)
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
// stringListFlag is a repeatable string flag preserving order (used for
// --stylesheet, where later files cascade over earlier ones).
type stringListFlag []string

func (s stringListFlag) String() string { return strings.Join(s, ",") }

func (s *stringListFlag) Set(raw string) error {
	*s = append(*s, raw)
	return nil
}

// readStylesheets reads each --stylesheet file and concatenates them in
// order (later files cascade over earlier ones at equal specificity),
// returning the combined source. No files → empty (no overlay).
func readStylesheets(paths []string) (string, error) {
	var b strings.Builder
	for _, p := range paths {
		resolved, err := resolveStylesheetPath(p)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			return "", fmt.Errorf("stylesheet %q: %w", p, err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// resolveStylesheetPath resolves a --stylesheet argument. It prefers the
// path relative to the current working directory; if that file is absent
// and ATTRACTOR_PIPELINES is set, it falls back to the shipped bundle
// (where models.css lives). Only genuine absence (fs.ErrNotExist)
// triggers the fallback — a permission error or other I/O failure is
// returned as-is, so callers can errors.Is it, matching the prior
// behaviour when ATTRACTOR_PIPELINES is unset.
//
// The bundle root already IS .../share/attractor/pipelines, so the
// documented `--stylesheet pipelines/models.css` would double-join; a
// leading `pipelines/` is stripped to key off the file within the
// bundle. The fallback is skipped for absolute paths, which would join
// to nonsense.
func resolveStylesheetPath(path string) (string, error) {
	_, err := os.Stat(path)
	if err == nil {
		return path, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("stylesheet %q: %w", path, err)
	}
	bundle := os.Getenv("ATTRACTOR_PIPELINES")
	if bundle != "" && !filepath.IsAbs(path) {
		candidate := filepath.Join(bundle, strings.TrimPrefix(path, "pipelines/"))
		if _, cerr := os.Stat(candidate); cerr == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("stylesheet %q not found: %w", path, err)
}

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
//  5. $ATTRACTOR_PIPELINES/<name>/pipeline.dot   (the shipped bundle)
//  6. $ATTRACTOR_PIPELINES/<name>.dot
//
// The bundle candidates (5, 6) come last so cwd and ~/.attractor still
// win; they are skipped entirely when ATTRACTOR_PIPELINES is unset.
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
	if bundle := os.Getenv("ATTRACTOR_PIPELINES"); bundle != "" {
		candidates = append(candidates,
			filepath.Join(bundle, arg, "pipeline.dot"),
			filepath.Join(bundle, arg+".dot"),
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
