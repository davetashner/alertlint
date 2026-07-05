# MVP acceptance dry run — 2026-07-04

The MVP gate (beads `alertlint-2ku`): analyze over a multi-vendor fixture
corpus, worklist over the output, and the skill's acceptance scenario over
a low-confidence noise finding. All three stages passed.

## Corpus

`fixtures/demo/` — canonical JSONL for four providers, replayed offline:

- **datadog**: 3 monitor configs (checkout-api CPU + latency, payments-api settlement lag)
- **pagerduty**: 22 incidents — 18 never-acked 4-minute auto-resolves against the CPU monitor (the REQ-NOISE-003 shape), 4 well-handled latency incidents with acks and linked records
- **servicenow**: 6 payments-api incidents all closed "Closed/Resolved – No Action Taken", plus the 3-CI CMDB inventory (one CI deliberately missing its criticality tier)
- **newrelic**: 1 policy no strategy can resolve (exercises `_unresolved.json`)
- `identity-conventions.yaml`: demo-estate rules incl. a servicenow rule the starter set does not ship

## Stage 1 — analyze

```
alertlint analyze --replay fixtures/demo --tenant demo --out out \
  --run-timestamp 2026-07-04T18:00:00Z \
  --identity-conventions fixtures/demo/identity-conventions.yaml
→ analyzed 2 service(s), 1 unresolved artifact(s); 3 document(s) written
```

Byte-identical across repeated runs with the same `--run-timestamp`
(guarded by `cmd/alertlint/replay_test.go` in CI).

## Stage 2 — worklist

```
rank  ci_id      ci_name       tier  priority  composite  findings
1     CI0002222  payments-api  2     76.9      48.7       4
2     CI0001111  checkout-api  1     67.0      66.5       3
# 1 unresolved artifact(s) — see _unresolved.json
```

The criticality-weighted design intent is visible in real output:
payments-api (tier 2, firm no-action noise, quality 48.7) outranks
checkout-api (tier 1, quality 66.5).

## Stage 3 — skill acceptance scenario

checkout-api carried the designed seam: a **low-confidence noise finding**
("100% of the confidence-weighted evidence over 18 fires in 90d says
noise… 18 auto-resolved, 0 acked") plus a TH-4 threshold finding on the
same alert.

Adjudication per `skills/alertlint/references/triage.md`: all 18 fires
cluster in 14:00–16:00 scale-up windows on a CPU metric and auto-resolve
in a uniform ~4 minutes — an autoscaler recovering before a human could
act. Verdict: **probable self-healing**; level-B `proposed_change` of kind
`routing_change` (de-page, keep the signal for capacity trending), not
deletion.

Hard-rule verification (ADR 0003): with the appended `proposed_change`
nulled back out, the enriched document compares **deep-equal to the CLI
original** — scores, evidence, and confidence untouched; enrichment was
strictly additive.

## Gaps found and filed

- `alertlint-o7p` (pre-existing, confirmed again): archetype metric
  patterns miss vendor query suffixes like `{tags}`; the demo works
  because fixture queries carry pattern-friendly metric names.
- Convention rules are genuinely per-estate: the starter file resolves
  nothing from ServiceNow. Documented here and in the demo conventions
  file rather than filed — the spec already owns this ("actual rule
  content is org-specific").

## Remaining post-MVP backlog

New Relic / CloudWatch / Splunk config adapters, fuzzy suggester +
identity confirm, coverage override input file, jq checks in CI,
archetype pattern fix, README quickstart.
