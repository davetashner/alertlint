// Command alertlint analyzes a service's alert configurations, firing
// history, and responder actions to score alerting quality and emit a
// prioritized improvement worklist. See docs/specs/ for the full design.
package main

import (
	"fmt"
	"io"
	"os"
)

const version = "0.0.1-dev"

const usageText = `alertlint — alerting-quality analysis

Usage:
  alertlint <command> [flags]

Commands:
  analyze    Score services and emit per-service JSON documents (docs/specs/output-contract.md)
  worklist   Aggregate per-service documents into a prioritized worklist
  identity   Manage artifact-to-CI identity mappings (docs/specs/identity-resolution.md)
  version    Print the alertlint version
  help       Show this help
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return 0
	case "analyze":
		return runAnalyze(args[1:], stdout, stderr)
	case "worklist":
		return runWorklist(args[1:], stdout, stderr)
	case "identity":
		fmt.Fprintf(stderr, "alertlint %s: not implemented yet — see docs/specs/\n", args[0])
		return 1
	default:
		fmt.Fprintf(stderr, "alertlint: unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, usageText)
		return 2
	}
}
