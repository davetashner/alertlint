package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	cloudwatchsdk "github.com/aws/aws-sdk-go-v2/service/cloudwatch"

	"github.com/davetashner/alertlint/internal/cache"

	"github.com/davetashner/alertlint/internal/adapter"
	cwadapter "github.com/davetashner/alertlint/internal/adapter/cloudwatch"
	"github.com/davetashner/alertlint/internal/adapter/datadog"
	"github.com/davetashner/alertlint/internal/adapter/newrelic"
	"github.com/davetashner/alertlint/internal/adapter/pagerduty"
	"github.com/davetashner/alertlint/internal/adapter/servicenow"
	"github.com/davetashner/alertlint/internal/adapter/splunk"
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
	cacheDir := fs.String("cache-dir", "", "record live pulls as replayable snapshots under this directory (ADR 0004)")
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
	var recorders map[string]recorderSetter
	if *replayDir != "" {
		registry, err = loadReplayRegistry(*replayDir)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else {
		registry, recorders, err = liveRegistry(stderr)
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
		Log:        stderr,
		RunMeta: output.Run{
			Timestamp:    now,
			ToolVersion:  version,
			InvocationID: hex.EncodeToString(sum[:])[:8],
		},
	}
	if *cacheDir != "" && *replayDir == "" {
		store, serr := cache.NewStore(*cacheDir)
		if serr != nil {
			fmt.Fprintln(stderr, serr)
			return 1
		}
		opts.Cache = map[string]*pipeline.SourceCache{}
		for providerID, setRecorder := range recorders {
			key := cache.NewKey(providerID, opts.Scope, opts.Window)
			w, werr := store.NewWriter(key)
			if werr != nil {
				fmt.Fprintln(stderr, werr)
				return 1
			}
			setRecorder(w) // raw pages and canonical records share the writer
			opts.Cache[providerID] = &pipeline.SourceCache{Writer: w, Key: key}
		}
	}
	started := time.Now()
	res, err := pipeline.Run(opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "analyzed %d service(s), %d unresolved artifact(s); %d document(s) written to %s in %s\n",
		res.Services, res.Unresolved, len(res.Documents), *out, time.Since(started).Round(time.Millisecond))
	return 0
}

// recorderSetter lets the cache wiring attach a page recorder to a
// concrete adapter after construction (raw pages, ADR 0004).
type recorderSetter func(w *cache.Writer)

// liveRegistry builds the registry from environment credentials. A nil
// registry means no credentials were found (exit 2).
func liveRegistry(stderr io.Writer) (*adapter.Registry, map[string]recorderSetter, error) {
	registry := adapter.NewRegistry()
	recorders := map[string]recorderSetter{}
	registered := 0
	if key := os.Getenv("DD_API_KEY"); key != "" {
		a := &datadog.Adapter{APIKey: key, AppKey: os.Getenv("DD_APP_KEY")}
		if err := registry.Register(a); err != nil {
			fmt.Fprintln(stderr, err)
			return nil, nil, err
		}
		recorders["datadog"] = func(w *cache.Writer) { a.Recorder = w }
		registered++
	}
	if key := os.Getenv("NEW_RELIC_API_KEY"); key != "" {
		a := &newrelic.Adapter{APIKey: key}
		if err := registry.Register(a); err != nil {
			fmt.Fprintln(stderr, err)
			return nil, nil, err
		}
		recorders["newrelic"] = func(w *cache.Writer) { a.Recorder = w }
		registered++
	}
	if token := os.Getenv("PAGERDUTY_TOKEN"); token != "" {
		a := &pagerduty.Adapter{Token: token}
		if err := registry.Register(a); err != nil {
			fmt.Fprintln(stderr, err)
			return nil, nil, err
		}
		recorders["pagerduty"] = func(w *cache.Writer) { a.Recorder = w }
		registered++
	}
	if os.Getenv("AWS_REGION") != "" || os.Getenv("AWS_PROFILE") != "" {
		awsCfg, cfgErr := awsconfig.LoadDefaultConfig(context.Background())
		if cfgErr != nil {
			fmt.Fprintln(stderr, cfgErr)
			return nil, nil, cfgErr
		}
		if err := registry.Register(&cwadapter.Adapter{Client: cloudwatchsdk.NewFromConfig(awsCfg)}); err != nil {
			fmt.Fprintln(stderr, err)
			return nil, nil, err
		}
		// CloudWatch goes through the SDK, not raw HTTP: canonical records
		// are cached by the pipeline; there are no raw pages to record.
		registered++
	}
	if base := os.Getenv("SPLUNK_URL"); base != "" {
		a := &splunk.Adapter{BaseURL: base, Token: os.Getenv("SPLUNK_TOKEN")}
		if err := registry.Register(a); err != nil {
			fmt.Fprintln(stderr, err)
			return nil, nil, err
		}
		recorders["splunk"] = func(w *cache.Writer) { a.Recorder = w }
		registered++
	}
	if base := os.Getenv("SERVICENOW_URL"); base != "" {
		a := &servicenow.Adapter{
			BaseURL: base, User: os.Getenv("SERVICENOW_USER"), Password: os.Getenv("SERVICENOW_PASSWORD"),
		}
		if err := registry.Register(a); err != nil {
			fmt.Fprintln(stderr, err)
			return nil, nil, err
		}
		recorders["servicenow"] = func(w *cache.Writer) { a.Recorder = w }
		registered++
	}
	if registered == 0 {
		fmt.Fprintln(stderr, "alertlint analyze: no source credentials found — set DD_API_KEY/DD_APP_KEY, NEW_RELIC_API_KEY, AWS_REGION/AWS_PROFILE, SPLUNK_URL/SPLUNK_TOKEN, PAGERDUTY_TOKEN, and/or SERVICENOW_URL/SERVICENOW_USER/SERVICENOW_PASSWORD (or use --replay)")
		return nil, nil, nil
	}
	return registry, recorders, nil
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
