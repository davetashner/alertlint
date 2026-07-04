# Spec: Archetype Library

- **Status:** Draft
- **Date:** 2026-07-04
- **Beads issue:** alertlint-md1
- **Requirements:** REQ-COV-001, REQ-COV-002, REQ-COV-003, REQ-COV-004 (see [docs/requirements](../requirements/))
- **Decision records:** [ADR 0003 — Deterministic/inference boundary](../decision-records/0003-deterministic-inference-boundary.md)

## Problem

Coverage scoring (REQ-SCORE-002) needs an answer to "what alerts *should* this
service have?" Today that answer lives in tribal knowledge: every team invents
its own idea of baseline alerting, so the fleet has no consistent yardstick and
"missing an obvious alert" is invisible until an incident exposes it.

REQ-COV-001 demands an **opinionated, versioned archetype → required-signal
library** as a first-class repo asset. This spec defines that asset: its file
format, its seed content (REQ-COV-002), how the CLI consumes it
deterministically to infer applicability from existing telemetry (path A of
REQ-COV-003, per ADR 0003), how the skill and humans override applicability
(paths C and D), and how a missing signal becomes a `type: coverage` finding
(REQ-COV-004). Every service-owner whose service is scored hits this on every
run; the library is the single source of the tool's alerting opinions.

## Goals

- Define a YAML file format for the library: archetype id, description,
  deterministic applicability rules, and required signals each carrying a
  methodology anchor, rationale, and severity-of-absence.
- Ship full worked entries for the three seed archetypes from REQ-COV-002:
  REST API, socket connections, business transactions.
- Specify the deterministic CLI evaluation algorithm (path A) — rule-based,
  LLM-free, reproducible, per ADR 0003 — including how applicability evidence
  and confidence are derived from the rules themselves.
- Specify the override mechanism by which skill enrichment (path C) and human
  confirmation (path D) take precedence over inference, without breaking
  determinism: overrides are *input data* to the CLI, never post-hoc edits.
- Define the shape of the `type: coverage` finding: the specific absent signal
  and why the archetype applies (REQ-COV-004), aligned with the finding
  taxonomy in [output-contract.md](output-contract.md).
- Define library versioning: the version is stamped into output metadata for
  reproducibility (REQ-OUT-002); adding archetypes is additive and
  non-breaking.

## Non-goals

- **No time-series analysis for inference.** Path A operates on the normalized
  telemetry/alert-config inventory from the provider adapters, never on raw
  metric data (REQ-NG-002).
- **No threshold values in the library.** The library says *a latency alert
  must exist*, not *latency must alert at 500 ms*. Threshold quality is a
  separate sub-score ([scoring-engine.md](scoring-engine.md)) and concrete
  values are level-B skill output ([skill-integration.md](skill-integration.md)).
- **No alert-config generation.** The library drives findings; proposed
  configs are the skill's level-B job, and auto-PR is deferred (REQ-NG-001).
- **No per-organization overlay/extension mechanism in v1.** One canonical
  library ships in the repo; org-specific archetypes are an open question.
- **Coverage sub-score math.** How signal satisfaction rolls into the coverage
  sub-score belongs to [scoring-engine.md](scoring-engine.md); this spec only
  guarantees the inputs (per-signal satisfied/missing with severity).

## Design

### 1. Repo asset & file format

The library is a single YAML file, versioned in this repo and bundled with CLI
releases:

```
archetypes/library.yaml        # the asset (REQ-COV-001)
archetypes/library.schema.json # JSON Schema, enforced in CI
```

Top-level structure:

```yaml
schema_version: 1          # format of this file; bump = CLI upgrade required
library_version: "1.0.0"   # content version; stamped into output metadata

# Closed enum of methodology anchors for this schema_version.
# Extension is additive-only (see §6).
anchors:
  - golden-signals/latency
  - golden-signals/errors
  - golden-signals/traffic
  - golden-signals/saturation
  - error-budget-burn
  - cert-expiry
  - liveness
  - data-integrity

archetypes: []             # entries defined below
```

Each archetype entry has four parts:

| Field | Purpose |
|-------|---------|
| `id`, `description` | Stable slug + human-readable definition of the archetype |
| `applies_when` | Deterministic predicates over the service's *existing* telemetry inventory — path A inference rules (REQ-COV-003) |
| `required_signals[]` | The opinions: each signal has `anchor` (methodology), `rationale`, `absence_severity`, and `satisfied_by` match rules |
| — | `satisfied_by` decides whether an existing alert config already covers the signal; if nothing matches, a coverage finding is emitted (§5) |

**Predicate language.** `applies_when` and `satisfied_by` share one small,
deterministic vocabulary, evaluated against the normalized inventory the
adapter layer produces (ADR 0004):

- `kind: signal_class` — matches a normalized telemetry class the adapter
  assigns (e.g. `http_server`, `tcp_check`). Strongest, vendor-agnostic.
- `kind: metric_pattern` — RE2 regex (no backtracking; pinned flavor for
  determinism) over metric names / query strings referenced by the service's
  monitors.
- `kind: monitor_type` — membership in a normalized monitor-type enum.
- `kind: tag` — exact match on a resource tag; used for explicit opt-in
  (`alertlint:archetype=<id>`).

Predicates combine with `any` / `all` / `none`. Each `applies_when` predicate
carries `strength: strong | weak`, which maps to the finding `confidence`
(§4) — the confidence values are data in the library, so they are
deterministic, not judgment.

### 2. Seed archetypes (REQ-COV-002) — full worked entries

```yaml
archetypes:
  # ───────────────────────────────────────────────────────────────
  - id: rest-api
    description: >
      Service exposing synchronous HTTP endpoints to callers (public or
      internal). Callers experience its health as request latency and
      error rate; the service experiences load as saturation.
    applies_when:
      any:
        - kind: signal_class
          equals: http_server
          strength: strong
        - kind: monitor_type
          in: [apm_latency, apm_error_rate, synthetics_http]
          strength: strong
        - kind: tag
          equals: "alertlint:archetype=rest-api"
          strength: strong
        - kind: metric_pattern
          pattern: '(?i)(^|[._])(http|request|endpoint)s?[._](latency|duration|count|rate|errors?|[45]xx)([._]|$)'
          strength: strong
        - kind: metric_pattern
          pattern: '(?i)(^|[._])(elb|alb|apigateway|nginx|envoy|istio)([._]|$)'
          strength: weak
    required_signals:
      - id: latency
        anchor: golden-signals/latency
        rationale: >
          Latency degradation is the earliest caller-visible symptom of an
          unhealthy HTTP service and routinely precedes hard failure; a REST
          API with no latency alert is blind to its primary SLO dimension.
        absence_severity: high
        satisfied_by:
          any:
            - kind: monitor_type
              in: [apm_latency, synthetics_http]
            - kind: metric_pattern
              pattern: '(?i)(latency|duration|response[._]?time|p(50|9[059]))'
      - id: error-rate
        anchor: golden-signals/errors
        rationale: >
          Elevated 5xx/error ratio is the direct signal that callers are
          failing; error-rate alerting is the minimum bar for any service
          with an availability expectation.
        absence_severity: high
        satisfied_by:
          any:
            - kind: monitor_type
              in: [apm_error_rate]
            - kind: metric_pattern
              pattern: '(?i)(error[._]?(rate|ratio|count|pct)|[._]5xx|status[._]?5)'
      - id: saturation
        anchor: golden-signals/saturation
        rationale: >
          Saturation (CPU, memory, thread/connection pools, queue depth)
          predicts imminent latency and error problems; alerting on it buys
          lead time before caller impact.
        absence_severity: medium
        satisfied_by:
          any:
            - kind: metric_pattern
              pattern: '(?i)(cpu|memory|mem[._]|heap|thread[._]?pool|queue[._]?(depth|length)|utilization|saturation)'

  # ───────────────────────────────────────────────────────────────
  - id: socket-connections
    description: >
      Service holding long-lived TCP/socket connections — connection pools
      to databases or brokers, or persistent client sockets. Failure modes
      are pool exhaustion and silent connection death rather than
      per-request errors.
    applies_when:
      any:
        - kind: signal_class
          equals: connection_pool
          strength: strong
        - kind: monitor_type
          in: [tcp_check, port_check]
          strength: strong
        - kind: tag
          equals: "alertlint:archetype=socket-connections"
          strength: strong
        - kind: metric_pattern
          pattern: '(?i)(^|[._])(hikari|pgbouncer|jdbc|odbc)([._]|$)|connections?[._](active|idle|open|count|max|wait)'
          strength: strong
    required_signals:
      - id: connection-pool
        anchor: golden-signals/saturation
        rationale: >
          Pool exhaustion is the dominant failure mode for connection-holding
          services and manifests as a cliff, not a slope; alerting on pool
          utilization/wait is the only pre-cliff warning available.
        absence_severity: high
        satisfied_by:
          any:
            - kind: metric_pattern
              pattern: '(?i)(pool[._]?(usage|utilization|active|pending|wait|exhaust)|connections?[._](active|wait|pending|max))'
      - id: liveness
        anchor: liveness
        rationale: >
          A long-lived socket can be dead without producing request errors
          (half-open connections, silent broker drop); an explicit
          liveness/reachability check is the only signal that catches this
          class of failure.
        absence_severity: high
        satisfied_by:
          any:
            - kind: monitor_type
              in: [tcp_check, port_check, heartbeat]
            - kind: metric_pattern
              pattern: '(?i)(liveness|heartbeat|keep[._]?alive|reachab|connection[._]?(up|alive|status))'

  # ───────────────────────────────────────────────────────────────
  - id: business-transactions
    description: >
      Service whose primary output is completed business transactions
      (orders, payments, settlements, transfers). Technical health metrics
      can be green while the business function silently fails; coverage
      must therefore include business-outcome signals.
    applies_when:
      any:
        - kind: tag
          equals: "alertlint:archetype=business-transactions"
          strength: strong
        - kind: metric_pattern
          pattern: '(?i)(^|[._])(order|payment|transaction|txn|checkout|settlement|transfer|invoice)s?[._]'
          strength: weak   # custom metrics have no canonical names; path A is
                           # deliberately low-confidence here — see §4 and
                           # skill-integration.md for the path-C escalation
    required_signals:
      - id: success-rate
        anchor: golden-signals/errors
        rationale: >
          Transaction success ratio is the business-level error signal; HTTP
          error rate does not capture transactions that complete technically
          but fail commercially (declines, validation rejects, partner
          failures).
        absence_severity: high
        satisfied_by:
          any:
            - kind: metric_pattern
              pattern: '(?i)(success[._]?(rate|ratio|pct)|(fail(ure|ed)?|decline|reject)[._]?(rate|ratio|count))'
      - id: volume-anomaly
        anchor: golden-signals/traffic
        rationale: >
          A transaction flow that quietly drops to zero (upstream outage,
          bad deploy, feed stoppage) produces no errors at all; only a
          volume/anomaly alert catches "the business stopped."
        absence_severity: high
        satisfied_by:
          any:
            - kind: monitor_type
              in: [anomaly, outlier, forecast]
            - kind: metric_pattern
              pattern: '(?i)(volume|count|rate|throughput)'   # must co-occur with
                                                              # applies_when biz-metric
                                                              # match; see §3 step 5
      - id: reconciliation-lag
        anchor: data-integrity
        rationale: >
          Transactions that complete but never reconcile (ledger mismatch,
          stuck settlement queue) are invisible to request-path alerting;
          reconciliation lag/backlog is the canonical integrity signal for
          money-moving flows.
        absence_severity: medium
        satisfied_by:
          any:
            - kind: metric_pattern
              pattern: '(?i)(reconcil|settlement[._]?(lag|delay|backlog|queue)|ledger[._]?(diff|mismatch)|unmatched)'
```

### 3. Deterministic CLI consumption (path A)

Per ADR 0003, archetype inference from telemetry is CLI-side and rule-based;
the CLI never calls an LLM. The evaluation pipeline, per service:

1. **Load & pin.** Load the bundled `library.yaml` (or `--archetype-library
   <path>` override), validate against the schema, record `library_version`
   for output metadata.
2. **Build inventory.** Assemble the normalized telemetry inventory from
   adapter outputs (ADR 0004): every monitor's metric references, query
   strings, monitor type, tags, and adapter-assigned `signal_class`es.
3. **Apply overrides first.** Read the override file (§4). For any
   (service, archetype) pair with a `confirmed` (path D) or `enriched`
   (path C) assertion, use it and skip inference; record provenance.
4. **Infer.** For remaining archetypes, evaluate `applies_when` against the
   inventory. Any match ⇒ archetype applies, with
   `archetype_source: inferred`, `confidence` from the strongest matched
   predicate (`strong` → 0.9, `weak` → 0.5 — values fixed in the CLI per
   library `schema_version`), and the matched predicates + matching artifacts
   recorded as `applicability_evidence`.
5. **Check signals.** For each applicable archetype, evaluate every required
   signal's `satisfied_by` against the service's alert configs, scoped to the
   artifacts that matched `applies_when` where a scoping comment says so
   (e.g. `volume-anomaly` must match against the business metrics, not any
   metric). Satisfied ⇒ record the satisfying alert ids; unsatisfied ⇒ emit a
   coverage finding (§5).
6. **Hand off.** Per-archetype, per-signal results (satisfied/missing ×
   `absence_severity`) feed the coverage sub-score in
   [scoring-engine.md](scoring-engine.md); findings and metadata flow into
   the per-service JSON per [output-contract.md](output-contract.md).

Determinism guarantee: same inventory + same library file + same override
file ⇒ byte-identical archetype results. Regex evaluation is RE2 (linear
time, no backtracking, no environment-dependent behavior).

A known and accepted blind spot: path A infers from telemetry that *exists*,
so a service with no HTTP-shaped monitors at all will not be inferred as
`rest-api` even if it serves HTTP. That is precisely the gap paths C and D
close — inference gets the cheap 80%, enrichment gets the rest.

### 4. Overrides: skill enrichment (path C) and human confirmation (path D)

Overrides are **input data**, not post-processing — this keeps the CLI
deterministic while letting judgment change outcomes (ADR 0003: the skill
judges, then the CLI recomputes). The override file:

```yaml
# archetype-overrides.yaml — passed via --archetype-overrides
overrides:
  - ci: SVC0012345                     # canonical CMDB CI (REQ-ID-001)
    archetype: business-transactions
    applies: true                      # or false, to suppress
    source: enriched                   # enriched = path C | confirmed = path D
    provenance: >
      OpenAPI spec declares /payments and /refunds; repo contains
      settlement-worker module.
    asserted_by: "skill run 2026-07-04T12:00Z"
```

Precedence: `confirmed` (D) > `enriched` (C) > `inferred` (A). Rules:

- A positive override makes the archetype apply even with zero telemetry
  match; findings from it carry `archetype_source: enriched|confirmed` and
  confidence 0.95 (enriched) / 1.0 (confirmed).
- A negative override (`applies: false`) suppresses the archetype's coverage
  findings, but the suppression itself is recorded in the service's output
  (never a silent drop — same principle as REQ-ID-003).
- Loop shape (detailed in [skill-integration.md](skill-integration.md)): the
  skill reads low-confidence applicability from a first CLI run, enriches
  from the service repo / OpenAPI spec, writes `enriched` entries, and
  re-invokes the CLI; ambiguous cases are queued for a human, whose answers
  land as `confirmed` entries. Overrides never touch scores directly — they
  change applicability inputs, and the CLI recomputes everything.

### 5. Coverage findings (REQ-COV-004)

Each unsatisfied required signal of an applicable archetype produces exactly
one finding, conforming to the taxonomy in REQ-OUT-003 /
[output-contract.md](output-contract.md):

```json
{
  "type": "coverage",
  "severity": "high",
  "confidence": 0.9,
  "rationale": "REST API without an error-rate alert: elevated 5xx/error ratio is the direct signal that callers are failing; error-rate alerting is the minimum bar for any service with an availability expectation.",
  "evidence": {
    "archetype": "rest-api",
    "archetype_source": "inferred",
    "applicability_evidence": [
      {
        "predicate": "metric_pattern:(?i)(^|[._])(http|request|endpoint)s?[._](latency|...)",
        "matched": ["monitor dd:1234 metric http.request.duration"]
      }
    ],
    "missing_signal": "error-rate",
    "anchor": "golden-signals/errors",
    "absence_severity": "high",
    "checked_alerts": 14,
    "library_version": "1.0.0"
  },
  "proposed_change": null
}
```

- `severity` comes straight from the library's `absence_severity`;
  `confidence` from the applicability determination (§3–4). Both the *specific
  absent signal* and *why the archetype applies* are machine-readable
  (`missing_signal` + `applicability_evidence`), satisfying REQ-COV-004.
- `rationale` is assembled deterministically from the library's `rationale`
  text — no generation.
- `proposed_change` is empty at the CLI level; the skill may attach a level-B
  concrete alert proposal ([skill-integration.md](skill-integration.md)).

### 6. Versioning

- **`library_version` (semver)** is stamped into every per-service output's
  metadata block (REQ-OUT-002), alongside tool version and weight config, so
  any score is reproducible against the exact opinions that produced it.
- **Additive = non-breaking:** adding an archetype, adding an `applies_when`
  predicate that only broadens strength/evidence, adding a new anchor to the
  enum, or adding a required signal to a *new* archetype ⇒ **minor** bump.
- **Opinion changes = breaking for comparability:** changing an existing
  signal's `absence_severity`, adding a required signal to an *existing*
  archetype, or removing/renaming anything ⇒ **major** bump, because scores
  before and after are not comparable (the coverage analog of REQ-SCORE-007).
  Cross-run comparisons must only be made within a major version.
- **`schema_version`** governs the file format and predicate vocabulary; the
  CLI refuses a library with a newer `schema_version` than it understands.
- CI enforces a library lint on every change: schema-valid, unique archetype
  and signal ids, all anchors in the enum, all patterns RE2-compilable, and a
  changelog entry justifying the version bump class.

## Alternatives considered

- **Archetypes as code (rules implemented in the CLI language).** Rejected:
  the library is meant to be a reviewable, versionable *opinion asset*
  (REQ-COV-001) that non-CLI-developers can extend; code entangles opinions
  with releases and makes the version stamp meaningless.
- **LLM-based archetype classification.** Rejected outright by ADR 0003 —
  inference from telemetry must be reproducible; judgment enters only through
  the path C/D override channel.
- **Full expression language (CEL/Rego) for applicability rules.** Deferred:
  four predicate kinds + three combinators cover the seed archetypes; a
  general evaluator adds attack/complexity surface before there is demand.
  Revisit if a real archetype cannot be expressed.
- **Per-archetype confidence declared as free numbers in YAML.** Rejected in
  favor of the two-level `strength` enum mapped to fixed values — keeps
  confidence semantics uniform across the library and out of contributors'
  hands.
- **Skill mutates findings directly instead of the override file.** Rejected:
  post-hoc mutation breaks the "skill never recomputes / CLI output is the
  record" boundary; feeding overrides back as input preserves determinism and
  leaves an audit trail.

## Testing & acceptance

**Test strategy**

- **Schema/lint tests (CI):** `library.yaml` validates against
  `library.schema.json`; lint rules from §6 pass; a deliberately broken
  fixture library is rejected with actionable errors.
- **Golden fixtures:** synthetic normalized inventories (per ADR 0004
  adapter output shape) for at least: a fully-covered REST API (zero
  findings), a REST API missing error-rate, a connection-pool service missing
  liveness, a tagged business-transaction service missing all three signals,
  a service matching only weak predicates (low-confidence output), and a
  service matching nothing. Snapshot-tested expected JSON.
- **Determinism test:** two runs over the same fixtures produce byte-identical
  archetype/finding output; property test that predicate evaluation order
  never changes results.
- **Override tests:** enriched/confirmed positive and negative overrides for
  each precedence transition (A→C, A→D, C→D), including recorded suppression.
- **Version-stamp test:** `library_version` appears in output metadata and
  changes when the library file changes.

**Acceptance criteria**

1. `archetypes/library.yaml` exists with the three seed archetypes exactly as
   specified in §2 and passes CI lint.
2. CLI run over the golden fixtures emits `type: coverage` findings carrying
   `missing_signal`, `anchor`, `applicability_evidence`, `absence_severity`-
   derived severity, and `library_version` (REQ-COV-004, REQ-OUT-002/003).
3. Overrides change applicability outcomes with correct precedence and
   provenance, and negative overrides are visibly recorded, all without any
   nondeterminism.
4. Adding a fourth archetype to the library requires no CLI code change and
   only a minor version bump (demonstrated in a test).

## Open questions

- **Override file ownership and persistence.** Where do `enriched`/`confirmed`
  overrides live between runs — the service's repo, a caller-local store, or
  (later, with central deployment per ADR 0001) a shared store? Affects
  whether human confirmations survive across callers. *Owner: dave; resolve
  before implementing [skill-integration.md](skill-integration.md).*
- **Metric-name extraction fidelity.** `metric_pattern` runs over metric
  references and query strings; provider query languages (Datadog, Splunk
  SPL, NRQL) differ enough that regex over raw queries may misfire. Do
  adapters need to parse queries into structured metric references (pushing
  work into ADR 0004's contract), or is RE2-over-strings acceptable for v1?
  *Plan: measure false-positive rate on real fixture corpora during adapter
  implementation.*
- **"No archetype applies" semantics.** Is a service that matches nothing
  (and has no overrides) a finding, a neutral coverage score, or excluded
  from the coverage sub-score entirely? Interacts with coverage-score math in
  [scoring-engine.md](scoring-engine.md). *Owner: dave; must be decided with
  that spec.*
- **Business-transactions inference viability.** Path A for this archetype is
  weak-only by design (custom metrics lack canonical names). Should the tool
  treat business-transactions as C/D-primary — i.e., never emit its findings
  above low confidence without enrichment — or is the weak-pattern list worth
  hardening? *Plan: evaluate precision on real tenant data during pilot.*
- **Anchor enum governance.** New anchors change severity semantics fleet-
  wide. Who approves additions, and does adding an anchor warrant a minor or
  major bump when existing signals are re-anchored? *Owner: dave; codify in
  the library changelog policy.*
- **Org-specific extension.** v1 ships one canonical library; large adopters
  will want company archetypes (e.g. batch pipelines, Kafka consumers). An
  overlay/merge mechanism has versioning implications (whose version gets
  stamped?). *Deferred; revisit after v1 pilot feedback.*
