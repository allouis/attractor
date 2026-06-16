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

	"github.com/fabro/attractor/internal/backend"
	"github.com/fabro/attractor/internal/backend/claudecode"
	"github.com/fabro/attractor/internal/dot"
	"github.com/fabro/attractor/internal/engine"
	"github.com/fabro/attractor/internal/graph"
	"github.com/fabro/attractor/internal/handler"
	"github.com/fabro/attractor/internal/ingest"
	"github.com/fabro/attractor/internal/interviewer"
	"github.com/fabro/attractor/internal/lint"
	"github.com/fabro/attractor/internal/render"
	"github.com/fabro/attractor/internal/server"
	"github.com/fabro/attractor/internal/transform"
)

func cryptoRandRead(b []byte) (int, error) { return rand.Read(b) }
func hexEncode(b []byte) string            { return hex.EncodeToString(b) }

// BackendChoice selects the codergen backend wired into the CLI's
// default handler set.
type BackendChoice string

const (
	BackendAuto       BackendChoice = "auto"
	BackendClaude     BackendChoice = "claude"
	BackendSimulation BackendChoice = "simulation"
)

// Validate parses and lints the supplied .dot file. Exit-code semantics:
// any ERROR-severity diagnostic yields a non-nil error from this
// function (and the binary exits 1). Warnings are printed but do not
// cause failure.
func Validate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("validate: expected a .dot path argument")
	}
	g, err := loadGraph(fs.Arg(0))
	if err != nil {
		return err
	}
	diags, err := lint.ValidateOrError(g)
	printDiagnostics(os.Stdout, diags)
	return err
}

// Run executes a pipeline end-to-end. The --backend flag selects the
// codergen backend; auto picks claude if `claude` is on PATH and auth
// is detected, else falls back to simulation mode with a stderr note.
// The positional argument is either a path to a .dot file or a
// pipeline name that resolves via lookup (see resolvePipelinePath).
func Run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	logs := fs.String("logs", "", "directory for run artefacts (default: ./.attractor-runs/<run-id>)")
	jsonOut := fs.Bool("json", false, "emit one JSON event per line on stdout")
	backendFlag := fs.String("backend", "auto", "codergen backend: auto | claude | simulation")
	hookshim := fs.String("hookshim", "", "path to hookshim binary (default: sibling of attractor)")
	humanFlag := fs.String("human", "auto", "interviewer for wait.human nodes: auto | console | approve")
	var vars varFlags
	fs.Var(&vars, "var", "set a pipeline variable (repeatable): -var name=value")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("run: expected a pipeline name or .dot path")
	}
	dotPath, err := resolvePipelinePath(fs.Arg(0))
	if err != nil {
		return err
	}
	g, err := loadGraph(dotPath)
	if err != nil {
		return err
	}
	if err := requireDeclaredVars(g, vars); err != nil {
		return err
	}
	dotDir := filepath.Dir(dotPath)
	prepared, err := engine.Prepare(g,
		transform.PromptFile{BaseDir: dotDir},
		transform.VariableExpansion{Vars: vars},
	)
	if err != nil {
		return err
	}
	logsRoot := *logs
	if logsRoot == "" {
		logsRoot = ".attractor-runs/" + engine.NewRunID()
	}
	if err := os.MkdirAll(logsRoot, 0o755); err != nil {
		return err
	}

	// Resolve backend + auxiliary ingest server.
	choice := resolveBackend(BackendChoice(*backendFlag))
	ingestSrv, codergenBackend, err := startCodergen(choice, g, logsRoot, *hookshim)
	if err != nil {
		return err
	}
	if ingestSrv != nil {
		defer ingestSrv.Close()
	}

	iv := resolveInterviewer(*humanFlag)
	registry := buildRegistryWith(handler.Codergen{Backend: codergenBackend}, iv)
	eng := engine.New(engine.Config{Registry: registry, LogsRoot: logsRoot})
	done := make(chan struct{})
	go func() {
		for ev := range eng.Events() {
			if *jsonOut {
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("render: expected a .dot path argument")
	}
	src, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	svg, err := render.SVG(src)
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
	if err := fs.Parse(args); err != nil {
		return err
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
	srv := server.New(server.Config{
		Addr:         *bind,
		LogsRoot:     *logs,
		MakeHandlers: server.DefaultHandlers(handler.Codergen{Backend: nil}),
		AuthToken:    token,
	})
	if err := srv.Start(); err != nil {
		return err
	}
	fmt.Println("attractor serving on", srv.URL())
	select {}
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

// resolveBackend turns `auto` into a concrete choice based on what's
// detectable on the host.
func resolveBackend(choice BackendChoice) BackendChoice {
	if choice != BackendAuto {
		return choice
	}
	if _, err := exec.LookPath("claude"); err != nil {
		fmt.Fprintln(os.Stderr, "attractor: claude binary not on PATH — falling back to simulation mode")
		return BackendSimulation
	}
	if !claudecode.AvailableAuth() {
		fmt.Fprintln(os.Stderr, "attractor: no Claude auth detected — falling back to simulation mode")
		return BackendSimulation
	}
	return BackendClaude
}

// startCodergen constructs the codergen backend matching `choice`. For
// the Claude path it also starts an ingest server scoped to the run so
// hook events flow back into the engine and tool_hooks.pre/post fire.
// The returned ingest.Server may be nil (simulation mode).
func startCodergen(choice BackendChoice, g *graph.Graph, logsRoot, hookshimOverride string) (*ingest.Server, backend.CodergenBackend, error) {
	if choice == BackendSimulation {
		return nil, nil, nil
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

// buildRegistry wires every built-in handler with the AutoApprove
// interviewer. Kept for callers that don't need to customise human
// gates (tests, simple smoke runs).
func buildRegistry(codergen handler.Codergen) *engine.Registry {
	return buildRegistryWith(codergen, interviewer.AutoApprove{})
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

func printDiagnostics(w io.Writer, diags []lint.Diagnostic) {
	for _, d := range diags {
		ref := d.NodeID
		if ref == "" && (d.EdgeFrom != "" || d.EdgeTo != "") {
			ref = d.EdgeFrom + "->" + d.EdgeTo
		}
		fmt.Fprintf(w, "%-7s %-30s %-30s %s\n", d.Severity, d.Rule, ref, d.Message)
	}
}

// varFlags is a repeatable -var name=value flag. The CLI honours
// pipeline-level $name placeholders by passing this map into the
// VariableExpansion transform.
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

// requireDeclaredVars checks that every name listed in the graph's
// `vars` attribute has a corresponding -var entry. Missing vars are a
// hard error so a pipeline doesn't silently run with empty
// substitutions.
func requireDeclaredVars(g *graph.Graph, supplied varFlags) error {
	raw := strings.TrimSpace(g.Attr("vars"))
	if raw == "" {
		return nil
	}
	var missing []string
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := supplied[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("pipeline declares vars %v but missing -var %v", strings.Split(raw, ","), missing)
}

// resolvePipelinePath turns a name-or-path argument into an absolute
// .dot path. Lookup order when arg is a bare name (no `/`, no `.dot`
// suffix):
//
//  1. ./pipelines/<name>/pipeline.dot
//  2. ./pipelines/<name>.dot
//  3. ~/attractor-pipelines/<name>/pipeline.dot
//  4. ~/attractor-pipelines/<name>.dot
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
			filepath.Join(home, "attractor-pipelines", arg, "pipeline.dot"),
			filepath.Join(home, "attractor-pipelines", arg+".dot"),
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
