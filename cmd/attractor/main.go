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
	case "serve":
		exit(cli.Serve(os.Args[2:]))
	case "automations":
		exit(cli.Automations(os.Args[2:]))
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
  serve            run the HTTP pipeline server
  automations      list or run saved automations (list | run <name>)
  version          print version
  help             print this message`)
}
