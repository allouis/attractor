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

	"github.com/fabro/attractor/internal/dot"
	"github.com/fabro/attractor/internal/engine"
	"github.com/fabro/attractor/internal/graph"
	"github.com/fabro/attractor/internal/handler"
	"github.com/fabro/attractor/internal/interviewer"
	"github.com/fabro/attractor/internal/lint"
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

// Run executes a pipeline end-to-end. Without a configured backend, the
// codergen handler runs in simulation mode (deterministic synthetic
// responses). Future iterations wire ClaudeCodeBackend via flags.
func Run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	logs := fs.String("logs", "", "directory for run artefacts (default: ./.attractor-runs/<run-id>)")
	jsonOut := fs.Bool("json", false, "emit one JSON event per line on stdout")
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
	registry := defaultRegistry()
	logsRoot := *logs
	if logsRoot == "" {
		logsRoot = ".attractor-runs/" + engineRunID()
	}
	if err := os.MkdirAll(logsRoot, 0o755); err != nil {
		return err
	}
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

// Render is the SVG renderer. Stubbed here; implementation lives in
// internal/render/svg.go and is wired in a later commit.
func Render(args []string) error {
	return fmt.Errorf("render: not implemented yet (wired in a follow-up commit)")
}

// Serve is the HTTP server CLI hook. Stubbed; implementation lives in
// internal/server and is wired in a later commit.
func Serve(args []string) error {
	return fmt.Errorf("serve: not implemented yet (wired in a follow-up commit)")
}

// ---------------------------------------------------------------------------

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

func defaultRegistry() *engine.Registry {
	r := engine.NewRegistry()
	r.Register("start", handler.Start{})
	r.Register("exit", handler.Exit{})
	r.Register("conditional", handler.Conditional{})
	r.Register("wait.human", handler.WaitHuman{Interviewer: interviewer.AutoApprove{}})
	codergen := handler.Codergen{Backend: nil} // simulation mode
	r.Register("codergen", codergen)
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

// engineRunID returns a short random run id; we re-export the engine's
// helper here so the CLI doesn't need its own RNG plumbing.
func engineRunID() string {
	// Lean on engine.New's default RunID by constructing a throwaway
	// engine when we only need the ID. This avoids duplicating crypto
	// imports here.
	tmp := engine.New(engine.Config{Registry: engine.NewRegistry(), LogsRoot: os.TempDir()})
	// Drain the events channel so the goroutine doesn't leak. New does
	// not start a run, but it does create a buffered channel.
	go func() { for range tmp.Events() { } }()
	return tmp.RunID
}
