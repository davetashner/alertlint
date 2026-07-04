# Spec: Provider Adapters

- **Status:** Draft
- **Date:** 2026-07-04
- **Beads issue:** alertlint-md1
- **Requirements:** REQ-SRC-001..REQ-SRC-007, REQ-NOISE-001, REQ-HIST-001 (see [docs/requirements](../requirements/))
- **Decision records:** [ADR 0004 — Per-data-class provider interfaces with canonical models](../decision-records/0004-provider-adapter-interface.md)

## Problem

Alert configuration, firing history, and human response live in different vendor
systems with incompatible APIs, field names, and disposition codes. The scoring
core must stay vendor-agnostic (REQ-SRC-007): adding a vendor means adding an
adapter, never touching the engine. Without a fixed adapter contract, every
parser hardcodes vendor assumptions into scoring logic, and the six tier-1
integrations (REQ-SRC-001, REQ-SRC-003, REQ-SRC-005) cannot be built or tested
in parallel. This spec defines that contract: the three narrow provider
interfaces, the canonical data models they emit, and the snapshot cache that
makes pulls reproducible.

## Goals

- Define the three provider interfaces from ADR 0004 — `ConfigProvider`,
  `HistoryProvider`, `ActionProvider` — precisely enough that the six tier-1
  adapters can be implemented independently against fixtures.
- Fix the canonical model schemas: `AlertConfig`, `AlertEvent`,
  `ResponseRecord`, including the **fixed disposition taxonomy** inside
  `ResponseRecord` (it must be finalized early — every `ActionProvider` maps
  into it, so late changes fan out across adapters).
- Keep adapters ignorant of identity resolution: records carry
  **pre-resolution identity hints** only; adapters know nothing about the CMDB
  (resolution happens in the core, see `identity-resolution.md`).
- Specify canonical-schema versioning and how adapters declare the version
  they emit.
- Specify the snapshot cache keyed by `(source, scope, window)` so runs are
  reproducible (REQ-SCORE-007), diffable, and re-scorable offline.
- Enumerate the tier-1 adapter set and which interfaces each implements.

## Non-goals

- **Identity resolution.** Adapters emit hints; mapping hints to a ServiceNow
  CI is the core's first pipeline stage — see `identity-resolution.md` and
  [ADR 0002](../decision-records/0002-identity-resolution-strategy.md).
- **Scoring semantics.** How disposition, timing, and auto-resolve signals turn
  into noise/coverage/threshold sub-scores belongs to `scoring-engine.md`.
- **The per-service output JSON.** That is the CLI's downstream interface —
  see `output-contract.md`. This spec covers only the *upstream* canonical
  records.
- **Tier-2 adapters** (Dynatrace, Azure Monitor, GCP Cloud Monitoring, BMC
  Helix, Uptime Robot — REQ-SRC-002/004) and the CloudTrail corroboration
  source (REQ-SRC-006). The interfaces must accommodate them; implementing
  them is later work.
- **Raw time-series pulls** for metric-derived thresholds (REQ-NG-002); that
  later phase may add a fourth interface but is explicitly out of scope here.
- **Write-back of any kind** (REQ-NG-001) — all three interfaces are
  read-only.
- **Credential brokering.** v1 runs with the caller's own credentials
  (REQ-EXEC-001); adapters receive already-configured authenticated clients.

## Design

### 1. Interface shape

Per ADR 0004, there are three narrow interfaces, one per data class. A vendor
module implements whichever apply; the core discovers capabilities by which
interfaces a registered adapter satisfies. Signatures are **language-agnostic
pseudocode** — no implementation language has been chosen yet (see Open
questions).

```
record Scope:
    tenant:   string            # opaque account / instance / region identifier,
                                # taken verbatim from caller config
    selector: string?           # optional provider-native narrowing filter
                                # (e.g. a tag query, team, or assignment group),
                                # passed through opaquely

record TimeWindow:
    start: timestamp            # inclusive, UTC
    end:   timestamp            # exclusive, UTC
                                # default window: last 90 days (REQ-HIST-001)

interface ConfigProvider:
    provider_id()    -> string                  # e.g. "datadog"
    schema_version() -> string                  # canonical schema version emitted
    fetch_configs(scope: Scope, window: TimeWindow)
                     -> stream<AlertConfig>

interface HistoryProvider:
    provider_id()    -> string
    schema_version() -> string
    fetch_events(scope: Scope, window: TimeWindow)
                     -> stream<AlertEvent>       # events whose fired_at ∈ window

interface ActionProvider:
    provider_id()    -> string
    schema_version() -> string
    fetch_responses(scope: Scope, window: TimeWindow)
                     -> stream<ResponseRecord>   # responses to events in window
```

Notes:

- **Streams, not lists.** Adapters yield records incrementally; pagination,
  rate limiting, and retries are entirely the adapter's problem and invisible
  to the core. (The concrete stream idiom — iterator, channel, async
  generator — is language-dependent; see Open questions.)
- **`ConfigProvider` takes a window for cache-key symmetry**, but config is
  fetched as a *current snapshot* at pull time. v1 does not reconstruct
  historical config state within the window (config drift is an open
  question).
- **Errors are not empty results.** An adapter must distinguish "fetched
  successfully, zero records" from "fetch failed." A failed pull aborts that
  source's contribution for the run and is reported in run metadata so
  per-service source coverage (derivable from which providers contributed
  records — ADR 0004) is honest. Partial pages are never silently emitted as
  a complete result.
- **Read-only.** No interface method mutates anything in a source system.

### 2. Common record envelope

Every canonical record, regardless of model, carries the same envelope fields:

```
envelope:
  schema_version: string        # canonical schema version, e.g. "1.0"
  source:
    provider: string            # adapter id: "datadog" | "newrelic" | "cloudwatch"
                                #   | "splunk" | "servicenow" | "pagerduty" | ...
    tenant:   string            # opaque tenant identifier, echoed from Scope
  source_ref:
    kind:      string           # vendor artifact type, e.g. "monitor", "alarm",
                                #   "incident", "saved_search"
    native_id: string           # vendor's own identifier for the artifact
    url:       string?          # deep link for evidence trails, when available
  identity_hints:               # pre-resolution only — see §3
    tags:          map<string, string>   # vendor tags/labels, verbatim
    names:         list<string>          # candidate service/entity names, verbatim
    external_refs: list<{system: string, native_id: string}>
                                # cross-system references embedded in the artifact
                                # (e.g. a PagerDuty service id inside a Datadog
                                #  monitor's notification config)
```

`source_ref` is the raw-artifact reference required by ADR 0004 for evidence
trails: every finding downstream can point back to the exact vendor object it
came from.

### 3. Identity hints (pre-resolution)

Adapters know nothing about the CMDB. They emit whatever native identity
material the artifact carries — tags, labels, entity names, embedded
cross-system references — **verbatim and unresolved**. The core's layered
resolver (`identity-resolution.md`, ADR 0002) consumes these hints *after*
adaptation:

- `tags` feed the exact and convention strategies (explicit CI tags, org
  convention rules).
- `names` feed the convention and fuzzy strategies.
- `external_refs` let the resolver chain identities across systems (e.g. a
  ServiceNow record referencing the PagerDuty incident it was created from).

Adapters must not filter, normalize, or interpret hints beyond structural
extraction — which tag key means what is org-specific convention config owned
by the resolver, not adapter code.

### 4. Canonical models

Field lists are given as JSON pseudo-schema. `?` marks optional/nullable;
absent optional means *unknown*, never a default. All timestamps are UTC
RFC 3339.

#### 4.1 `AlertConfig` (from `ConfigProvider`)

One record per alert definition (monitor, alarm, alert condition, saved
search with alert action).

```
AlertConfig:
  <envelope>                            # §2
  name:            string
  description:     string?
  condition_raw:   string               # vendor-native condition/query, verbatim
  threshold:       number?              # extracted when parseable, else absent
  comparator:      ">" | ">=" | "<" | "<=" | "==" | "!=" | null
  duration_s:      integer?             # evaluation/for-duration in seconds,
                                        # when parseable
  severity:
    native:     string                  # vendor's own severity/priority value
    normalized: "critical" | "high" | "medium" | "low" | "info" | "unknown"
  routing:         list<{
    target_kind: "pagerduty_service" | "email" | "webhook" | "chat" | "other"
    target:      string                 # verbatim destination
  }>
  status:          "enabled" | "disabled" | "silenced"
  silenced_until:  timestamp?           # when status == "silenced" and known
  created_at:      timestamp?
  updated_at:      timestamp?
```

`condition_raw` is always present so no information is lost; `threshold`,
`comparator`, and `duration_s` are best-effort extractions the
behavior-inferred threshold heuristics (`scoring-engine.md`, REQ-THRESH-001)
and level-B proposals need. How deep adapters parse vendor query languages is
an open question — a missing extraction is legal and means "not parseable,"
never a guessed value.

`status` matters for REQ-HIST-002: a silenced/disabled monitor with zero fires
must be distinguishable from a healthy dormant one.

Example:

```json
{
  "schema_version": "1.0",
  "source": { "provider": "datadog", "tenant": "acct-primary" },
  "source_ref": {
    "kind": "monitor",
    "native_id": "84312077",
    "url": "https://app.datadoghq.com/monitors/84312077"
  },
  "identity_hints": {
    "tags": { "service": "checkout-api", "env": "prod", "team": "payments" },
    "names": ["checkout-api"],
    "external_refs": [
      { "system": "pagerduty", "native_id": "PXYZ123" }
    ]
  },
  "name": "[prod] checkout-api p95 latency high",
  "description": "Pages when p95 latency exceeds SLO threshold.",
  "condition_raw": "avg(last_10m):p95:trace.http.request.duration{service:checkout-api,env:prod} > 2.5",
  "threshold": 2.5,
  "comparator": ">",
  "duration_s": 600,
  "severity": { "native": "P2", "normalized": "high" },
  "routing": [
    { "target_kind": "pagerduty_service", "target": "PXYZ123" }
  ],
  "status": "enabled",
  "silenced_until": null,
  "created_at": "2025-11-02T14:31:00Z",
  "updated_at": "2026-03-18T09:12:44Z"
}
```

#### 4.2 `AlertEvent` (from `HistoryProvider`)

One record per firing episode (trigger → resolve) inside the window.

```
AlertEvent:
  <envelope>
  alert_ref: {                          # best-effort reference to the
    provider:  string?                  # originating alert definition;
    native_id: string?                  # the core joins this to AlertConfig,
    name:      string?                  # falling back to name text
  }
  fired_at:         timestamp
  resolved_at:      timestamp?          # absent = still open at pull time
  auto_resolved:    boolean?            # true = resolved by the monitoring
                                        # system, not a human; absent = unknown
                                        # (primary noise signal, REQ-NOISE-001)
  occurrence_count: integer             # >= 1; grouped/deduped firings folded
                                        # into one episode by the source
  severity:
    native:     string?
    normalized: "critical" | "high" | "medium" | "low" | "info" | "unknown"
```

`alert_ref` is deliberately best-effort: when history comes from PagerDuty or
ServiceNow, the originating monitor is a different vendor and only whatever
reference survived the integration is available. Joining `AlertEvent` to
`AlertConfig` (exact by `(provider, native_id)`, else by name/hints) is core
logic, not adapter logic.

Example:

```json
{
  "schema_version": "1.0",
  "source": { "provider": "pagerduty", "tenant": "acct-primary" },
  "source_ref": {
    "kind": "incident",
    "native_id": "Q1ABC2DEF3GHI4",
    "url": "https://example.pagerduty.com/incidents/Q1ABC2DEF3GHI4"
  },
  "identity_hints": {
    "tags": {},
    "names": ["checkout-api"],
    "external_refs": [
      { "system": "datadog", "native_id": "84312077" }
    ]
  },
  "alert_ref": {
    "provider": "datadog",
    "native_id": "84312077",
    "name": "[prod] checkout-api p95 latency high"
  },
  "fired_at": "2026-05-14T03:22:10Z",
  "resolved_at": "2026-05-14T03:29:41Z",
  "auto_resolved": true,
  "occurrence_count": 1,
  "severity": { "native": "high", "normalized": "high" }
}
```

#### 4.3 `ResponseRecord` (from `ActionProvider`)

One record per human-response trail attached to an alert event (a ServiceNow
incident's lifecycle fields, a PagerDuty incident's ack/resolve log).

```
ResponseRecord:
  <envelope>
  event_ref: {                          # which AlertEvent this responds to
    provider:  string?
    native_id: string?
  }
  acked_at:           timestamp?        # absent = never acknowledged
  closed_at:          timestamp?
  disposition:        <see fixed taxonomy below>
  disposition_native: string?           # vendor's raw close code, verbatim,
                                        # kept for evidence and mapping audits
  reassignment_count: integer           # 0 if never reassigned
  actor_ref:          string?           # opaque assignee/resolver identifier
                                        # (PII handling is an open question)
  linked_records: list<{
    kind:      "change" | "incident" | "problem" | "other"
    native_id: string
    url:       string?
  }>
```

Time-to-ack, time-to-close, and never-acked (REQ-NOISE-002) are **derived by
the core** from `acked_at` / `closed_at` / the joined event's `fired_at`;
adapters emit timestamps, never computed durations.

**Fixed disposition taxonomy.** Every `ActionProvider` maps vendor close codes
into exactly one of these values (ADR 0004 requires this be finalized early —
late changes fan out across all action adapters):

| Value | Meaning |
|-------|---------|
| `no_action` | Closed with an explicit no-action-taken code (the primary REQ-NOISE-001 signal, e.g. ServiceNow "Closed – No Action") |
| `action_taken` | A human intervened: fix, restart, config change, remediation |
| `escalated` | Handed to an incident / major-incident / problem process |
| `duplicate` | Closed as duplicate of another record |
| `known_issue` | Closed against an existing problem / known-error record |
| `auto_closed` | Closed by automation or timeout with no human involvement |
| `unknown` | Vendor code exists but maps to nothing above (raw code preserved in `disposition_native`); or no close code at all |

The taxonomy is closed: adapters must never invent values. An unmappable code
maps to `unknown` — classifying what `unknown` + timing signals mean for noise
(including the never-acked + fast-auto-resolve ambiguity default,
REQ-NOISE-003) is scoring-engine logic per the deterministic boundary
([ADR 0003](../decision-records/0003-deterministic-inference-boundary.md)),
not adapter logic. Adapters translate codes; they do not judge.

Example:

```json
{
  "schema_version": "1.0",
  "source": { "provider": "servicenow", "tenant": "instance-prod" },
  "source_ref": {
    "kind": "incident",
    "native_id": "INC0451923",
    "url": "https://example.service-now.com/incident.do?sys_id=abc123"
  },
  "identity_hints": {
    "tags": {},
    "names": ["checkout-api"],
    "external_refs": [
      { "system": "pagerduty", "native_id": "Q1ABC2DEF3GHI4" }
    ]
  },
  "event_ref": { "provider": "pagerduty", "native_id": "Q1ABC2DEF3GHI4" },
  "acked_at": null,
  "closed_at": "2026-05-14T04:05:02Z",
  "disposition": "no_action",
  "disposition_native": "Closed/Resolved - No Action Taken",
  "reassignment_count": 2,
  "actor_ref": "sys_user:7f3a09",
  "linked_records": []
}
```

### 5. Schema versioning

- The three canonical models share **one canonical-schema version** (single
  version string, e.g. `"1.0"`), versioned **alongside the output contract**
  (ADR 0004) — see `output-contract.md` for the shared versioning policy.
- Every record carries `schema_version`; every adapter declares the version it
  emits via `schema_version()`. The core refuses records whose major version
  it does not support — no silent coercion.
- Compatibility rule: **minor** version = additive optional fields only;
  **major** version = anything that removes, renames, retypes a field, or
  changes an enum (including the disposition taxonomy). Adding a disposition
  value is a major change precisely because every `ActionProvider` mapping
  table must be revisited.
- Cache entries record the schema version of their materialized canonical
  records (see §6), so a schema bump invalidates derived canonical files but
  not the cached raw pulls beneath them.

### 6. Snapshot cache

Raw pulls are cached to disk **keyed by `(source, scope, window)`** — where
`source` = `(provider, tenant)` and `window` = `(start, end)` — making runs
reproducible (REQ-SCORE-007), diffable, and re-scorable offline while
iterating on scoring logic without re-pulling APIs (ADR 0004).

Layout (cache root configurable; default under the tool's local data
directory):

```
<cache-root>/
  <provider>/<key-hash>/            # key-hash = stable hash of
    manifest.json                   #   (provider, tenant, selector, start, end)
    raw/                            # verbatim vendor responses, page by page
      page-0001.json
      page-0002.json
      ...
    canonical/                      # derived, regenerable from raw/
      records.jsonl                 # one canonical record per line
```

- `manifest.json` records the full key tuple in the clear, `fetched_at`, the
  adapter version, the canonical `schema_version` of `canonical/`, record
  counts, and completeness status (complete | failed — failed pulls are never
  presented as usable snapshots).
- **Raw is the source of truth; canonical is derived.** When an adapter's
  mapping logic or the canonical schema changes, `canonical/` is regenerated
  from `raw/` without re-hitting vendor APIs.
- A run in offline/replay mode reads only from the cache and fails loudly on
  a missing key — it never falls back to a live pull.
- The cache **doubles as the test-fixture format** (ADR 0004): adapter golden
  tests are checked-in `raw/` directories with expected `canonical/` output.
  Later central deployment populates the same cache shape from scheduled
  pulls ([ADR 0001](../decision-records/0001-fleet-vs-laptop-scope.md)).
- Cache-key stability for "now"-anchored windows (e.g. rounding `end` to an
  hour boundary so back-to-back runs share a snapshot) is an open question.

### 7. Tier-1 adapter set

| Adapter | ConfigProvider | HistoryProvider | ActionProvider | Requirement |
|--------------|:---:|:---:|:---:|-------------|
| Datadog | ✓ | — | — | REQ-SRC-001 |
| New Relic | ✓ | — | — | REQ-SRC-001 |
| CloudWatch | ✓ | — | — | REQ-SRC-001 |
| Splunk | ✓ | — | — | REQ-SRC-001 |
| ServiceNow | — | ✓ | ✓ | REQ-SRC-003, REQ-SRC-005 |
| PagerDuty | — | ✓ | ✓ | REQ-SRC-003, REQ-SRC-005 |

Partial coverage is natural, not a special case: per-service source coverage
is derived from which providers contributed records. The narrow interfaces
mean a later Datadog `HistoryProvider` (Datadog has some history — ADR 0004
context) or tier-2 vendors are pure additions.

Per-adapter mapping sketch (vendor artifact → canonical model; all field
mapping, disposition translation, pagination, and rate limiting live inside
the adapter):

- **Datadog** — monitors → `AlertConfig`; monitor tags and query scope tags →
  `identity_hints.tags`; notification targets → `routing` and
  `external_refs`; mute status → `status: silenced`.
- **New Relic** — alert policies/conditions → `AlertConfig` (one record per
  condition); entity tags → hints; workflow/notification destinations →
  `routing`.
- **CloudWatch** — alarms → `AlertConfig`; alarm dimensions, resource tags →
  hints; threshold/comparator/period map directly (structured, fully
  parseable); SNS actions → `routing`; `actions_enabled`/disabled →
  `status`.
- **Splunk** — saved searches with alert actions → `AlertConfig`;
  `condition_raw` = the SPL; threshold extraction is often not parseable and
  legitimately absent; app/owner metadata → hints.
- **ServiceNow** — alert-originated incident/event records → `AlertEvent`
  (open/resolve times) and `ResponseRecord` (ack/close times, close code →
  disposition taxonomy, reassignment count, linked change/problem records).
  CMDB CI fields on the record are emitted **as hints only** — resolution
  still happens in the core so ServiceNow gets no privileged path.
- **PagerDuty** — incidents → `AlertEvent` (trigger/resolve, auto-resolution
  by the integration → `auto_resolved`, covering the monitor-side
  auto-resolve signal of REQ-NOISE-001) and `ResponseRecord`
  (acknowledgements, resolver, reassignments via log entries); PD service →
  `identity_hints` and `external_refs`.

## Alternatives considered

The load-bearing alternatives were decided in
[ADR 0004](../decision-records/0004-provider-adapter-interface.md): one
monolithic per-vendor interface (rejected — forces stubs and capability
flags) and ETL-to-files with a file-only core (rejected as a required stage —
too heavy for laptop v1; retained as the snapshot cache). Spec-level choices
made here:

- **One generic `Record` type with a `kind` field** instead of three typed
  models — rejected: it pushes per-kind validation into every consumer and
  makes the disposition taxonomy a stringly-typed convention instead of a
  schema guarantee.
- **Disposition mapping in the core** (adapters emit raw codes only) —
  rejected: ADR 0004 puts all vendor normalization in adapters; a central
  code-mapping table would grow vendor conditionals inside the engine. The
  raw code is still preserved (`disposition_native`) for audits.
- **Caching canonical records only** (discard raw) — rejected: adapter
  mapping bugs would require re-pulling APIs to fix historical runs; raw
  retention makes canonical output regenerable and mapping changes cheap.
- **Computed durations in `ResponseRecord`** (time-to-ack etc.) — rejected:
  duplicating derivations across six adapters invites inconsistency; the core
  computes all timing statistics per ADR 0003 ("the CLI computes").

## Testing & acceptance

**Strategy**

- **Golden fixtures per adapter:** checked-in `raw/` snapshots (recorded,
  sanitized vendor payloads) with expected `canonical/records.jsonl`;
  regenerating canonical from raw must be byte-identical (determinism).
- **Shared contract test suite** run against every adapter: envelope fields
  always present; `schema_version` matches the declared `schema_version()`;
  enums only contain schema values; timestamps valid RFC 3339 UTC; no record
  emitted from a failed/partial pull.
- **Disposition mapping tables** for ServiceNow and PagerDuty tested
  exhaustively: every known vendor close code → expected taxonomy value;
  unknown codes → `unknown` with `disposition_native` preserved.
- **Isolation check:** adapter modules have no dependency on identity
  resolution, CMDB, or scoring modules (enforced by dependency lint / import
  rules once a language is chosen).
- **Cache round-trip:** pull → cache → offline replay produces identical
  canonical records and identical downstream input; offline mode on a missing
  key fails loudly.

**Acceptance criteria**

1. All six tier-1 adapters implement their interfaces per §7 and pass the
   shared contract suite and their golden-fixture tests.
2. The scoring core compiles/runs against canonical models only — zero
   vendor-specific types or conditionals outside adapter modules.
3. A full run against cached snapshots completes with network access disabled
   and reproduces prior canonical output exactly.
4. Every emitted record carries `source_ref` sufficient to open the
   originating vendor artifact (evidence-trail requirement).
5. Adding a mock seventh vendor (test-only adapter) requires no change to
   core code — registration only.

## Open questions

- **Implementation language.** No language has been chosen; interface
  signatures above are pseudocode, and the streaming idiom (iterator vs.
  async generator vs. channel), plugin/registration mechanism, and dependency
  lint tooling all depend on it. Owner: dave — resolve before any adapter
  implementation starts.
- **Disposition mapping tables need empirical sampling.** The taxonomy is
  fixed, but which real ServiceNow close codes (org-configurable) and
  PagerDuty resolution states map to which value must be validated against
  real records; likely needs a per-org override file (config, not code) since
  ServiceNow close codes are customizable per instance. Owner: dave — sample
  during ServiceNow adapter development.
- **Normalized severity scale.** The proposed 5-level + `unknown` enum must
  be validated against all four config vendors' native scales (and PagerDuty
  urgency); mapping tables may also need per-org overrides. Plan: fix during
  the first two ConfigProvider implementations.
- **Config drift within the window.** Configs are pulled as a current
  snapshot, but a 90-day window can span threshold edits; is
  `updated_at`-based flagging enough for v1, or do we need config revision
  history where vendors expose it? Plan: revisit when `scoring-engine.md`
  defines threshold heuristics.
- **Cache key stability for now-anchored windows.** Whether/how to round
  `window.end` so consecutive runs share snapshots, and cache TTL/expiry
  policy. Plan: decide during cache implementation; affects only ergonomics,
  not correctness.
- **Actor identifiers and PII.** `actor_ref` may be a real user identifier;
  decide whether adapters hash it, pass an opaque vendor sys-id, or drop it
  (reassignment_count may be signal enough). Owner: dave — resolve before
  the first ActionProvider ships.
- **Is PagerDuty's auto-resolution signal sufficient for REQ-NOISE-001?**
  The requirement names "auto-resolve status from the monitor/metric system";
  v1 captures it via PagerDuty's integration-resolved flag. If services page
  through paths that bypass PagerDuty/ServiceNow, a monitor-side
  HistoryProvider (e.g. Datadog) may need promoting into v1. Plan: validate
  against early real-data runs.
- **Condition parse depth.** How far each ConfigProvider goes extracting
  `threshold` / `comparator` / `duration_s` from vendor query languages
  (CloudWatch is trivially structured; Splunk SPL often is not). The
  contract permits absence; the question is per-adapter effort vs. value for
  level-B proposals. Plan: settle per adapter, driven by what
  `scoring-engine.md` threshold heuristics actually consume.
