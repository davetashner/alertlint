package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/davetashner/alertlint/internal/output"
)

// runWorklist aggregates one or more analyze output directories into the
// prioritized worklist (ADR 0001: dumb aggregation, zero scoring logic).
func runWorklist(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("worklist", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "tsv", "output format: tsv | json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dirs := fs.Args()
	if len(dirs) == 0 {
		fmt.Fprintln(stderr, "alertlint worklist: at least one corpus directory is required")
		return 2
	}

	wl, err := output.Aggregate(dirs)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(wl); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	case "tsv":
		fmt.Fprintln(stdout, "rank\tci_id\tci_name\ttier\tpriority\tcomposite\tfindings\toffhours")
		for i, e := range wl.Ranked {
			composite := ""
			if e.Composite != nil {
				composite = fmt.Sprintf("%.1f", *e.Composite)
			}
			fmt.Fprintf(stdout, "%d\t%s\t%s\t%d\t%.1f\t%s\t%d\t%d\n",
				i+1, e.CIID, e.CIName, e.Tier, *e.PriorityScore, composite, e.Findings, e.FiresOffHours)
		}
		for _, e := range wl.NotRanked {
			fmt.Fprintf(stdout, "-\t%s\t%s\t%d\tnot ranked\t\t%d\n", e.CIID, e.CIName, e.Tier, e.Findings)
		}
		if wl.UnresolvedArtifacts > 0 {
			fmt.Fprintf(stdout, "# %d unresolved artifact(s) — see %s\n", wl.UnresolvedArtifacts, output.UnresolvedDocumentName)
		}
	default:
		fmt.Fprintf(stderr, "alertlint worklist: unknown format %q\n", *format)
		return 2
	}
	return 0
}
