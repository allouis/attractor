// Command attractor is the Attractor pipeline CLI.
package main

import (
	"fmt"
	"os"

	"github.com/allouis/attractor/internal/cli"
	"github.com/allouis/attractor/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Printf("%s (rev %s)\n", version.Number, version.Get())
	case "help", "--help", "-h":
		printUsage()
	case "validate":
		exit(cli.Validate(os.Args[2:]))
	case "run":
		exit(cli.Run(os.Args[2:]))
	case "render":
		exit(cli.Render(os.Args[2:]))
	case "hub":
		exit(cli.HubCmd(os.Args[2:]))
	case "runs":
		exit(cli.Runs(os.Args[2:]))
	case "view":
		exit(cli.View(os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "attractor: unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}
}

func exit(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "attractor:", err)
	os.Exit(1)
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `attractor — DOT-based AI pipeline runner

usage:
  attractor <command> [args...]

commands:
  run <file>       parse, validate, and execute a pipeline
  validate <file>  lint a pipeline without executing
  render <file>    render a pipeline graph as SVG
  hub              run the pull-based run directory: announce + scrape + archive
  runs             list local runs from the runs root (id, graph, status, started)
  view <dir>       re-serve a run directory read-only over the run --ui binding
  version          print version
  help             print this message`)
}
