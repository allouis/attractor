// Package cli wires the attractor binary's subcommands. Keeping the
// glue in a package (rather than main) lets tests exercise the
// commands directly.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

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
)

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
func Run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	logs := fs.String("logs", "", "directory for run artefacts (default: ./.attractor-runs/<run-id>)")
	jsonOut := fs.Bool("json", false, "emit one JSON event per line on stdout")
	backendFlag := fs.String("backend", "auto", "codergen backend: auto | claude | simulation")
	hookshim := fs.String("hookshim", "", "path to hookshim binary (default: sibling of attractor)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("run: expected a .dot path argument")
	}
	g, err := loadGraph(fs.Arg(0))
	if err != nil {
		return err
	}
	prepared, err := engine.Prepare(g)
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

	registry := buildRegistry(handler.Codergen{Backend: codergenBackend})
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

// Serve runs the Attractor HTTP server (§9.5).
func Serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addr := fs.String("addr", "127.0.0.1:7681", "TCP bind address")
	logs := fs.String("logs", ".attractor-runs", "base directory for pipeline run artefacts")
	if err := fs.Parse(args); err != nil {
		return err
	}
	srv := server.New(server.Config{
		Addr:         *addr,
		LogsRoot:     *logs,
		MakeHandlers: server.DefaultHandlers(handler.Codergen{Backend: nil}),
	})
	if err := srv.Start(); err != nil {
		return err
	}
	fmt.Println("attractor serving on", srv.URL())
	select {}
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

// buildRegistry wires every built-in handler, parameterised by the
// codergen handler so callers can supply an alternate backend (e.g.
// real Claude vs simulation).
func buildRegistry(codergen handler.Codergen) *engine.Registry {
	r := engine.NewRegistry()
	r.Register("start", handler.Start{})
	r.Register("exit", handler.Exit{})
	r.Register("conditional", handler.Conditional{})
	r.Register("wait.human", handler.WaitHuman{Interviewer: interviewer.AutoApprove{}})
	r.Register("tool", handler.Tool{})
	r.Register("parallel", handler.Parallel{})
	r.Register("parallel.fan_in", handler.FanIn{})
	r.Register("stack.manager_loop", handler.ManagerLoop{})
	r.Register("codergen", codergen)
	r.Register("codergen.claude", codergen)
	r.SetDefault(codergen)
	return r
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
