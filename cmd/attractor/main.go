package main

import (
	"fmt"
	"os"
)

const Version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Println(Version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "attractor: unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}
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
  version          print version
  help             print this message`)
}
