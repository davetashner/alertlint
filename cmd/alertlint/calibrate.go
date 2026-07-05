package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"

	"github.com/davetashner/alertlint/internal/output"
	"github.com/davetashner/alertlint/internal/score"
)

// runCalibrate reports observed distributions from a corpus against the
// scoring config's constants. Read-only by construction: it has no code
// path that writes configuration — adopting a suggestion is a deliberate
// edit plus a scoring_config_version bump.
func runCalibrate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "text", "output format: text | json")
	scoringPath := fs.String("scoring-config", "configs/scoring.yaml", "scoring config the corpus was scored with")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dirs := fs.Args()
	if len(dirs) == 0 {
		fmt.Fprintln(stderr, "usage: alertlint calibrate <corpus-dir>... [--scoring-config file]")
		return 2
	}
	cfg, err := score.LoadConfig(*scoringPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	report, err := output.Calibrate(dirs, cfg.Noise.TierNoiseBudgetPerWeek)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	case "text":
		fmt.Fprintf(stdout, "calibration over %d service(s), %d ranked (scoring config v%d)\n\n",
			report.Services, report.Ranked, cfg.ScoringConfigVersion)
		fmt.Fprintln(stdout, "tier\tservices\tbudget/wk\tobserved noisy/wk p50/p75/p90\tsuggested budget (p75)")
		tiers := make([]string, 0, len(report.PerTier))
		for t := range report.PerTier {
			tiers = append(tiers, t)
		}
		sort.Strings(tiers)
		for _, t := range tiers {
			tc := report.PerTier[t]
			fmt.Fprintf(stdout, "%s\t%d\t%.1f\t%.2f / %.2f / %.2f\t%.2f\n",
				t, tc.Services, tc.CurrentBudget,
				tc.ObservedRatePcts.P50, tc.ObservedRatePcts.P75, tc.ObservedRatePcts.P90,
				tc.SuggestedBudget)
		}
		ne := report.NoiseEvidence
		fmt.Fprintf(stdout, "\nnoise findings: %d\n", ne.Findings)
		fmt.Fprintf(stdout, "  fire_count p50/p90/max: %.0f / %.0f / %.0f\n",
			ne.FireCountPcts.P50, ne.FireCountPcts.P90, ne.FireCountPcts.Max)
		fmt.Fprintf(stdout, "  off_hours_ratio p50/p90: %.2f / %.2f\n",
			ne.OffHoursRatioPcts.P50, ne.OffHoursRatioPcts.P90)
		fmt.Fprintf(stdout, "  median_time_to_resolve_s p50/p90: %.0f / %.0f\n",
			ne.MedianResolvePcts.P50, ne.MedianResolvePcts.P90)
		fmt.Fprintln(stdout, "\nAdopting a suggestion = edit configs/scoring.yaml + bump scoring_config_version.")
	default:
		fmt.Fprintf(stderr, "alertlint calibrate: unknown format %q\n", *format)
		return 2
	}
	return 0
}
