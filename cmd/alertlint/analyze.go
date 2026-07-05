package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/adapter/datadog"
	"github.com/davetashner/alertlint/internal/adapter/newrelic"
	"github.com/davetashner/alertlint/internal/adapter/pagerduty"
	"github.com/davetashner/alertlint/internal/adapter/servicenow"
	"github.com/davetashner/alertlint/internal/archetype"
	"github.com/davetashner/alertlint/internal/identity"
	"github.com/davetashner/alertlint/internal/output"
	"github.com/davetashner/alertlint/internal/pipeline"
	"github.com/davetashner/alertlint/internal/score"
)

// runAnalyze wires the live pipeline from flags and environment
// credentials (REQ-EXEC-001: the caller's own credentials, no broker).
// Adapters register only when their credentials are present, so a
// partial-credential run analyzes what it can reach and says so.
func runAnalyze(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "out", "output directory for per-service documents")
	tenant := fs.String("tenant", "", "tenant identifier recorded in snapshots (required)")
	selector := fs.String("selector", "", "provider-native narrowing filter, passed through opaquely")
	windowDays := fs.Int("window-days", adapter.DefaultWindowDays, "analysis window length in days")
	scoringPath := fs.String("scoring-config", "configs/scoring.yaml", "scoring config file")
	libraryPath := fs.String("archetype-library", "archetypes/library.yaml", "archetype library file")
	conventionsPath := fs.String("identity-conventions", "configs/identity-conventions.yaml", "identity convention rules file")
	ciTagKeys := fs.String("ci-tag-keys", "cmdb_ci,ci_id", "comma-separated tag keys treated as explicit CI references")
	replayDir := fs.String("replay", "", "offline mode: read canonical JSONL fixtures from this corpus directory instead of live APIs")
	overridesPath := fs.String("archetype-overrides", "", "archetype override file (paths C/D of REQ-COV-003)")
	mappingsPath := fs.String("identity-mappings", "", "confirmed-mappings file (strategy 2; missing file = empty ratchet)")
	runTimestamp := fs.String("run-timestamp", "", "RFC3339 run timestamp override (deterministic runs; default now)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *tenant == "" {
		fmt.Fprintln(stderr, "alertlint analyze: --tenant is required")
		return 2
	}

	cfg, err := score.LoadConfig(*scoringPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	lib, err := archetype.LoadLibrary(*libraryPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	conv, err := identity.LoadConventions(*conventionsPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var confirmed []identity.ConfirmedMapping
	if *mappingsPath != "" {
		_, confirmed, err = identity.LoadMappings(*mappingsPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	var overrides []archetype.Override
	if *overridesPath != "" {
		overrides, err = archetype.LoadAllOverrides(*overridesPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	var registry *adapter.Registry
	if *replayDir != "" {
		registry, err = loadReplayRegistry(*replayDir)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else {
		registry, err = liveRegistry(stderr)
		if registry == nil {
			return 2
		}
		if err != nil {
			return 1
		}
	}

	now := time.Now().UTC()
	if *runTimestamp != "" {
		parsed, perr := time.Parse(time.RFC3339, *runTimestamp)
		if perr != nil {
			fmt.Fprintf(stderr, "alertlint analyze: bad --run-timestamp: %v\n", perr)
			return 2
		}
		now = parsed.UTC()
	}
	sum := sha256.Sum256([]byte(now.Format(time.RFC3339Nano) + *tenant))
	opts := pipeline.Options{
		Registry:   registry,
		Scope:      adapter.Scope{Tenant: *tenant, Selector: *selector},
		Window:     adapter.TimeWindow{Start: now.AddDate(0, 0, -*windowDays), End: now},
		Config:     cfg,
		Library:    lib,
		Overrides:  overrides,
		Convention: conv,
		Confirmed:  confirmed,
		Resolver:   identity.ResolverConfig{CIIDTagKeys: splitComma(*ciTagKeys)},
		Fuzzy:      identity.DefaultFuzzyConfig(),
		OutDir:     *out,
		RunMeta: output.Run{
			Timestamp:    now,
			ToolVersion:  version,
			InvocationID: hex.EncodeToString(sum[:])[:8],
		},
	}
	res, err := pipeline.Run(opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "analyzed %d service(s), %d unresolved artifact(s); %d document(s) written to %s\n",
		res.Services, res.Unresolved, len(res.Documents), *out)
	return 0
}

// liveRegistry builds the registry from environment credentials. A nil
// registry means no credentials were found (exit 2).
func liveRegistry(stderr io.Writer) (*adapter.Registry, error) {
	registry := adapter.NewRegistry()
	registered := 0
	if key := os.Getenv("DD_API_KEY"); key != "" {
		if err := registry.Register(&datadog.Adapter{APIKey: key, AppKey: os.Getenv("DD_APP_KEY")}); err != nil {
			fmt.Fprintln(stderr, err)
			return nil, err
		}
		registered++
	}
	if key := os.Getenv("NEW_RELIC_API_KEY"); key != "" {
		if err := registry.Register(&newrelic.Adapter{APIKey: key}); err != nil {
			fmt.Fprintln(stderr, err)
			return nil, err
		}
		registered++
	}
	if token := os.Getenv("PAGERDUTY_TOKEN"); token != "" {
		if err := registry.Register(&pagerduty.Adapter{Token: token}); err != nil {
			fmt.Fprintln(stderr, err)
			return nil, err
		}
		registered++
	}
	if base := os.Getenv("SERVICENOW_URL"); base != "" {
		if err := registry.Register(&servicenow.Adapter{
			BaseURL: base, User: os.Getenv("SERVICENOW_USER"), Password: os.Getenv("SERVICENOW_PASSWORD"),
		}); err != nil {
			fmt.Fprintln(stderr, err)
			return nil, err
		}
		registered++
	}
	if registered == 0 {
		fmt.Fprintln(stderr, "alertlint analyze: no source credentials found — set DD_API_KEY/DD_APP_KEY, NEW_RELIC_API_KEY, PAGERDUTY_TOKEN, and/or SERVICENOW_URL/SERVICENOW_USER/SERVICENOW_PASSWORD (or use --replay)")
		return nil, nil
	}
	return registry, nil
}

func splitComma(s string) []string {
	var out []string
	for _, part := range splitAndTrim(s) {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func splitAndTrim(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			part := s[start:i]
			for len(part) > 0 && part[0] == ' ' {
				part = part[1:]
			}
			for len(part) > 0 && part[len(part)-1] == ' ' {
				part = part[:len(part)-1]
			}
			out = append(out, part)
			start = i + 1
		}
	}
	return out
}
