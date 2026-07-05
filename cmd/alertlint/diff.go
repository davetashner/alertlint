package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/davetashner/alertlint/internal/output"
)

// runDiff compares two analyze output corpora run-over-run.
func runDiff(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "tsv", "output format: tsv | json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fmt.Fprintln(stderr, "usage: alertlint diff <old-dir> <new-dir> [--format tsv|json]")
		return 2
	}
	res, err := output.Diff([]string{rest[0]}, []string{rest[1]})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch *format {
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	case "tsv":
		fmt.Fprintln(stdout, "ci_id\tci_name\tpriority_delta\trank_move\tnew\tresolved\tpersisting")
		for _, sd := range res.Changed {
			delta := ""
			if sd.PriorityDelta != nil {
				delta = fmt.Sprintf("%+.1f", *sd.PriorityDelta)
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%+d\t%d\t%d\t%d\n",
				sd.CIID, sd.CIName, delta, sd.RankMove, len(sd.NewFindings), len(sd.ResolvedFindings), sd.Persisting)
			for _, f := range sd.NewFindings {
				fmt.Fprintf(stdout, "  + %s %s/%s\t%s\n", f.ID, f.Type, f.Severity, f.Rationale)
			}
			for _, f := range sd.ResolvedFindings {
				fmt.Fprintf(stdout, "  - %s %s/%s\t%s\n", f.ID, f.Type, f.Severity, f.Rationale)
			}
		}
		for _, e := range res.NewServices {
			fmt.Fprintf(stdout, "%s\t%s\tNEW SERVICE\t\t%d\t\t\n", e.CIID, e.CIName, e.Findings)
		}
		for _, e := range res.RemovedServices {
			fmt.Fprintf(stdout, "%s\t%s\tREMOVED\t\t\t\t\n", e.CIID, e.CIName)
		}
	default:
		fmt.Fprintf(stderr, "alertlint diff: unknown format %q\n", *format)
		return 2
	}
	return 0
}
