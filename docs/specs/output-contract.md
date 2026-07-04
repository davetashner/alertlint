# Spec: Per-service output contract

- **Status:** Draft
- **Date:** 2026-07-04
- **Beads issue:** alertlint-md1
- **Requirements:** REQ-OUT-001, REQ-OUT-002, REQ-OUT-003, REQ-OUT-004, REQ-EXEC-001, REQ-EXEC-002, REQ-EXEC-003 (see [docs/requirements](../requirements/))
- **Decision records:** [ADR 0001 — per-service atomic scoring + separate aggregation](../decision-records/0001-fleet-vs-laptop-scope.md), [ADR 0003 — deterministic/inference boundary](../decision-records/0003-deterministic-inference-boundary.md)

## Problem

The CLI's structured stdout **is the interface** (REQ-ARCH-001): the Claude skill consumes it directly, and the fleet worklist is an aggregation over a corpus of these documents (REQ-OUT-004, ADR 0001). Today there is no defined shape for that document. Without a frozen contract:

- the skill cannot be built in parallel with the CLI ([skill-integration.md](skill-integration.md) is blocked);
- findings are not greppable or CI-checkable (REQ-OUT-003), so nothing downstream can be regression-tested;
- corpora produced by different callers on different laptops (REQ-EXEC-001/002) cannot be merged into one worklist, because merge semantics depend on stable identity and versioning fields.

Everyone who touches the project hits this: the CLI writes it, the skill reads it, the aggregator sorts it, CI validates it, and humans grep it. It is the single highest-fan-out artifact in the design.

## Goals

- Define the complete schema of the **one-JSON-document-per-service** output (REQ-OUT-001): identity, scores, findings, metadata (REQ-OUT-002).
- Freeze the **finding taxonomy** (`noise | coverage | threshold | identity`) and the per-finding required fields (REQ-OUT-003) so findings are greppable and CI-checkable from day one.
- Carry `confidence` and `evidence` on every determination — the CLI→skill handoff mandated by ADR 0003 ("the skill never recomputes statistics").
- Define `proposed_change` as the level-B recommendation block and the seam for future level-C auto-PR (REQ-REC-002/003).
- Make every document **self-describing and reproducible**: window, timestamps, tool/archetype-library/convention-ruleset versions, and the weight config used (REQ-SCORE-007).
- Define the **aggregation layer**: corpus in, ranked worklist out, deterministic merge/dedup, zero scoring logic (REQ-OUT-004, REQ-EXEC-003, ADR 0001).
- Define contract versioning and evolution rules (additive vs. breaking).

## Non-goals

- **How scores are computed.** Formulas, normalization, and the ambiguity defaults are [scoring-engine.md](scoring-engine.md)'s job; this spec only fixes where the results live.
- **How artifacts get resolved to a CI.** Resolution strategies and the convention ruleset are [identity-resolution.md](identity-resolution.md); this spec fixes how resolution *results* (method, confidence, gaps) are reported.
- **Canonical source-record schemas** (`AlertConfig`, `AlertEvent`, `ResponseRecord`) — those belong to [provider-adapters.md](provider-adapters.md) per ADR 0004; this document references them only via evidence pointers.
- **The skill's own output** (narrative, prioritization prose). The skill *enriches* this document (see §Design, "Provenance"); what it renders for humans is [skill-integration.md](skill-integration.md).
- **Level-C auto-PR mechanics.** Out of scope for v1 (REQ-NG-001); `proposed_change` is designed not to preclude it, nothing more.
- **Human-facing formatting.** Humans are secondary consumers (REQ-NG-003). No table rendering, no color, no summaries in this contract.

## Design

### Overview

The CLI emits exactly one JSON document per resolved service (CI). A **corpus** is a directory of such documents. The **aggregator** is a separate component that reads a corpus, deduplicates, and re-sorts — it never computes or adjusts a score (ADR 0001).

Top-level shape:

```
{
  "contract_version": "1.0.0",
  "identity":  { ... },      // who this is and how sure we are
  "scores":    { ... },      // the numbers, incl. the ranking key
  "findings":  [ ... ],      // typed, evidence-backed, greppable
  "metadata":  { ... }       // everything needed to reproduce this document
}
```

Consumers MUST be tolerant readers: unknown fields anywhere in the document are ignored, never an error. Producers MUST emit every field marked **required** below.

### Annotated schema

Presented in JSON-Schema style with inline annotations. `R` = required, `O` = optional. All timestamps are RFC 3339 UTC. All identifiers are strings.

#### Top level

| Field | Type | Req | Notes |
|---|---|---|---|
| `contract_version` | string (semver) | R | Version of *this contract*. See "Versioning & evolution". |
| `identity` | object | R | See below. |
| `scores` | object | R | See below. |
| `findings` | array | R | May be empty (`[]`), never absent. |
| `metadata` | object | R | See below. |

#### `identity` — CI, resolved artifacts, mapping coverage

```jsonc
"identity": {
  // The canonical service identity (REQ-ID-001). null ONLY in the reserved
  // unresolved document (see "The unresolved document" below).
  "ci": {                                  // R
    "id": "CI0012345",                     // R  CMDB sys_id or CI number — the corpus dedup key
    "name": "payments-api",                // R  human-readable CI name
    "criticality_tier": 1,                 // R  integer 1 (highest) .. N, from the CMDB CI (REQ-ID-004)
    "criticality_source": "cmdb"           // R  "cmdb" | "default" — "default" means the configurable
                                           //    middle-tier fallback was applied (REQ-CRIT-003), and a
                                           //    type:identity finding MUST accompany it
  },

  // Every source artifact that resolved to this CI (REQ-OUT-002). One entry per
  // artifact, not per source. Canonical-record details live with the adapters
  // (provider-adapters.md); this is the join record.
  "artifacts": [                           // R  may be empty for the unresolved document
    {
      "source": "datadog",                 // R  adapter id (provider-adapters.md registry)
      "kind": "alert_config",              // R  "alert_config" | "alert_event" | "response_record"
      "native_id": "monitor:4821337",      // R  vendor-native identifier, stable within the source
      "native_name": "payments-api p99 latency high",  // O  display aid
      "resolution": {                      // R  how this artifact was joined to the CI
        "method": "exact",                 // R  "exact" | "confirmed" | "convention"  (fuzzy NEVER joins — ADR 0002;
                                           //    fuzzy candidates surface only inside identity findings)
        "confidence": "high",              // R  "high" | "medium"  (exact/confirmed=high, convention=medium)
        "rule": null                       // O  convention-rule id when method=="convention"
      },
      "analysis_state": "scored"           // R  "scored" | "dormant" | "insufficient_data"
                                           //    (REQ-HIST-002/003/004; only meaningful for
                                           //    kind=="alert_config", null otherwise)
    }
  ],

  // Per-service mapping coverage (REQ-ID-002/003). Ratios are computed by the
  // CLI over artifacts *attributable to this service's sources scope*; the
  // denominator semantics are owned by identity-resolution.md.
  "mapping": {                             // R
    "resolved_count": 14,                  // R  artifacts joined via exact/confirmed/convention
    "candidate_count": 1,                  // R  artifacts with fuzzy candidates awaiting confirmation
    "coverage_note": "partial",            // R  "full" | "partial" — partial means scores were computed
                                           //    from an incomplete join and MUST be read accordingly
    "by_source": {                         // R  per-source resolved counts, for gap triage
      "datadog": 9, "pagerduty": 3, "servicenow": 2
    }
  }
}
```

#### `scores` — sub-scores, composite, tier, priority

All scores are floats in `[0, 100]`. Sub-scores and composite are **quality** scores: higher is better. `priority_score` is an **attention** score: higher means "work on this sooner" — it is the one and only ranking key (REQ-SCORE-005). Formulas and normalization live in [scoring-engine.md](scoring-engine.md); per ADR 0001 they use only per-service facts, never corpus statistics, so the numbers here are identical whether the service was scored alone or among 5,000 others (REQ-EXEC-003).

```jsonc
"scores": {
  "noise": 31.5,                 // R  REQ-SCORE-001
  "coverage": 58.0,              // R  REQ-SCORE-002
  "threshold": 44.2,             // R  REQ-SCORE-003 (v1: behavior-inferred only)
  "composite": 42.6,             // R  weighted by metadata.config.weights
  "criticality_tier": 1,         // R  duplicated from identity.ci for one-hop jq ranking
  "priority_score": 86.1,        // R  criticality-weighted attention score — THE ranking key
  "inputs": {                    // R  what fed the sub-scores (REQ-HIST-004: states are explicit)
    "alerts_scored": 11,
    "alerts_dormant": 2,         //    config exists, zero fires in window — NOT penalized, NOT hidden
    "alerts_insufficient_data": 1
  }
}
```

If **every** alert config for the service is `dormant`/`insufficient_data`, sub-scores that lack input are emitted as `null` and `priority_score` is `null`; the aggregator lists such services in a separate "not ranked" section rather than sorting nulls (REQ-HIST-004 — never conflated with healthy).

#### `findings[]` — the stable taxonomy

The taxonomy is frozen **now** (REQ-OUT-003): `type` is exactly one of `noise | coverage | threshold | identity`. Every finding is an independent, self-contained record — greppable without joining back to anything else.

```jsonc
{
  "id": "ald-3f9c2e71",          // R  deterministic content hash of (type, subject, window) —
                                 //    stable across reruns on the same input, so diffs between
                                 //    runs show real change, not churn
  "type": "noise",               // R  "noise" | "coverage" | "threshold" | "identity"
  "severity": "high",            // R  "critical" | "high" | "medium" | "low"
                                 //    (evidence→severity mapping is scoring-engine.md's job)
  "confidence": "low",           // R  "high" | "medium" | "low" — the ADR 0003 handoff signal.
                                 //    low-confidence findings are the DESIGNED seam: the skill's
                                 //    first job on any service is triaging them.
  "subject": {                   // R  what the finding is about
    "source": "datadog",         // O  absent for coverage findings about a *missing* signal
    "native_id": "monitor:4821337", // O  ditto
    "signal": "p99_latency"      // O  archetype signal name, for coverage findings
  },
  "rationale": "Fired 41 times in 90d; never acked; median auto-resolve 4m; no Closed–No Action code present.",
                                 // R  one or two human-readable sentences; the skill quotes it,
                                 //    humans grep it
  "evidence": { ... },           // R  type-specific object, see table below. This is the raw
                                 //    material the skill reasons over WITHOUT recomputing (ADR 0003).
  "proposed_change": { ... }     // O  level-B block, see below
}
```

**Required evidence keys per type.** Evidence objects are open (additive keys allowed) but each type has a minimum set the CLI MUST emit, so the skill and CI checks can rely on them:

| `type` | Required evidence keys |
|---|---|
| `noise` | `fire_count`, `window_days`, `acked_count`, `no_action_count`, `auto_resolved_count`, `median_time_to_resolve_s`, `disposition_counts` (map of disposition code → count) |
| `coverage` | `archetype`, `archetype_source` (`"inferred"` \| `"enriched"` \| `"confirmed"` — REQ-COV-003 paths A/C/D), `missing_signal`, `applicability_basis` (which telemetry implied the archetype) |
| `threshold` | `fire_count`, `window_days`, `no_action_ratio`, `current_threshold`, `current_duration_s` (both nullable when the source config omits them) |
| `identity` | `unresolved_artifact` (`{source, kind, native_id, native_name}`), `candidates[]` (`{ci_id, ci_name, match_score, method:"fuzzy"}`; may be empty), `reason` (`"no_ci_reference"` \| `"ambiguous_candidates"` \| `"criticality_missing"`) |

**`proposed_change` — the level-B block and the level-C seam (REQ-REC-002/003).**

```jsonc
"proposed_change": {
  "kind": "threshold_update",    // R  "threshold_update" | "duration_update" | "delete_alert" |
                                 //    "add_alert" | "routing_change" | "grouping" | "mapping_add"
  "target": {                    // R  what to change — vendor-addressable
    "source": "datadog",
    "native_id": "monitor:4821337",
    "path": "options.thresholds.critical"   // O  field path within the vendor config
  },
  "current": 250,                // R  present value (null for kind=="add_alert"/"mapping_add")
  "proposed": 400,               // R  concrete value or config fragment — never prose
  "rationale": "p99 has sat at 320–360ms for 90d; 250ms fires ~daily with 87% no-action.",  // R
  "generated_by": "skill",       // R  "cli" | "skill" — provenance (see below)
  "diff": null                   // O  reserved for level-C: a rendered vendor-config diff.
                                 //    v1 producers emit null/omit; the field exists so auto-PR
                                 //    later needs no schema break.
}
```

**Provenance and enrichment.** Per ADR 0003, the CLI emits level-A findings with evidence; the **skill** generates level-B `proposed_change` blocks and may append them to findings in the document it re-emits. An enriched document is still schema-valid — same contract, more optional blocks filled in, `generated_by` recording who filled them. The CLI sets `generated_by:"cli"` only for the rare purely mechanical proposals (e.g., `mapping_add` from a confirmed convention). The skill MUST NOT modify `scores`, `evidence`, `confidence`, or any CLI-emitted value — enrichment is strictly additive (ADR 0003: the skill never recomputes; scores are never adjusted).

#### `metadata` — reproducibility (REQ-SCORE-007)

Everything needed to reproduce or explain the document:

```jsonc
"metadata": {
  "run": {
    "timestamp": "2026-07-04T17:22:09Z",   // R  used by the aggregator for recency dedup
    "tool_version": "0.4.2",               // R
    "invocation_id": "9d2c7a1e"            // R  shared by all documents from one CLI run
  },
  "window": {                              // R  the analysis window (REQ-HIST-001)
    "start": "2026-04-05T00:00:00Z",
    "end": "2026-07-04T00:00:00Z",
    "days": 90
  },
  "config": {
    "weights": { "noise": 45, "coverage": 30, "threshold": 25 },  // R  REQ-SCORE-004
    "priority_formula_version": "1",       // R  fixed, corpus-independent (REQ-EXEC-003)
    "config_hash": "sha256:1c0e…"          // R  hash of the full effective scoring config
  },
  "archetype_library_version": "2026.06.2",  // R  REQ-COV-001
  "convention_ruleset_version": "2026.06.5", // R  identity conventions (ADR 0002 / identity-resolution.md)
  "sources": [                             // R  which adapters contributed (provider-adapters.md)
    { "source": "datadog",    "adapter_version": "0.4.2", "canonical_schema_version": "1",
      "snapshot_key": "datadog/acme-prod/2026-04-05_2026-07-04" },
    { "source": "pagerduty",  "adapter_version": "0.4.2", "canonical_schema_version": "1",
      "snapshot_key": "pagerduty/acme/2026-04-05_2026-07-04" },
    { "source": "servicenow", "adapter_version": "0.4.2", "canonical_schema_version": "1",
      "snapshot_key": "servicenow/acme/2026-04-05_2026-07-04" }
  ]
}
```

`snapshot_key` points into the ADR 0004 snapshot cache, so a document can be re-derived offline from the cached pulls.

#### The unresolved document

Artifacts with **no** CI candidate at all cannot live in any per-service document, yet must not be dropped silently (REQ-ID-003). Each run therefore emits at most one **reserved document** with `identity.ci: null`, `scores` all `null`, and one `type: identity` finding per unmappable artifact (`reason: "no_ci_reference"`). Its filename is `_unresolved.json`. The aggregator never ranks it; it lists it as a mapping-debt appendix. Artifacts that have fuzzy *candidates* instead appear as identity findings inside the candidate CI's own document (ADR 0002: suggestion-only, never joined for scoring).

### Full example document

`payments-api.CI0012345.json` — a tier-1 REST API with a noisy monitor, a missing archetype signal, a mistuned threshold (skill-enriched with a level-B proposal), and one fuzzy identity candidate:

```json
{
  "contract_version": "1.0.0",
  "identity": {
    "ci": {
      "id": "CI0012345",
      "name": "payments-api",
      "criticality_tier": 1,
      "criticality_source": "cmdb"
    },
    "artifacts": [
      {
        "source": "datadog",
        "kind": "alert_config",
        "native_id": "monitor:4821337",
        "native_name": "payments-api p99 latency high",
        "resolution": { "method": "exact", "confidence": "high", "rule": null },
        "analysis_state": "scored"
      },
      {
        "source": "datadog",
        "kind": "alert_config",
        "native_id": "monitor:4821440",
        "native_name": "payments-api disk usage",
        "resolution": { "method": "convention", "confidence": "medium", "rule": "dd-service-tag-v3" },
        "analysis_state": "dormant"
      },
      {
        "source": "pagerduty",
        "kind": "response_record",
        "native_id": "service:PX7Q2K1",
        "native_name": "Payments API",
        "resolution": { "method": "convention", "confidence": "medium", "rule": "pd-name-eq-ci-name" },
        "analysis_state": null
      },
      {
        "source": "servicenow",
        "kind": "alert_event",
        "native_id": "em_alert:pmt-api-*",
        "native_name": null,
        "resolution": { "method": "exact", "confidence": "high", "rule": null },
        "analysis_state": null
      }
    ],
    "mapping": {
      "resolved_count": 14,
      "candidate_count": 1,
      "coverage_note": "partial",
      "by_source": { "datadog": 9, "pagerduty": 3, "servicenow": 2 }
    }
  },
  "scores": {
    "noise": 31.5,
    "coverage": 58.0,
    "threshold": 44.2,
    "composite": 42.6,
    "criticality_tier": 1,
    "priority_score": 86.1,
    "inputs": { "alerts_scored": 11, "alerts_dormant": 2, "alerts_insufficient_data": 1 }
  },
  "findings": [
    {
      "id": "ald-3f9c2e71",
      "type": "noise",
      "severity": "high",
      "confidence": "low",
      "subject": { "source": "datadog", "native_id": "monitor:4821337", "signal": null },
      "rationale": "Fired 41 times in 90d; never acked; median auto-resolve 4m; no Closed–No Action code present. Scored as noise by the ambiguity default; could be self-healing.",
      "evidence": {
        "fire_count": 41,
        "window_days": 90,
        "acked_count": 0,
        "no_action_count": 0,
        "auto_resolved_count": 41,
        "median_time_to_resolve_s": 240,
        "disposition_counts": { "auto_resolved": 41 }
      },
      "proposed_change": null
    },
    {
      "id": "ald-88a1b0cd",
      "type": "coverage",
      "severity": "high",
      "confidence": "high",
      "subject": { "source": null, "native_id": null, "signal": "error_rate" },
      "rationale": "Service matches the rest_api archetype (HTTP request telemetry present in Datadog) but has no error-rate alert.",
      "evidence": {
        "archetype": "rest_api",
        "archetype_source": "inferred",
        "missing_signal": "error_rate",
        "applicability_basis": "datadog metrics trace.http.request.{hits,errors} present in window"
      },
      "proposed_change": null
    },
    {
      "id": "ald-1d4e9f02",
      "type": "threshold",
      "severity": "medium",
      "confidence": "high",
      "subject": { "source": "datadog", "native_id": "monitor:4821337", "signal": null },
      "rationale": "High fire frequency with 87% no-action ratio implies the threshold is too tight or the duration too short.",
      "evidence": {
        "fire_count": 41,
        "window_days": 90,
        "no_action_ratio": 0.87,
        "current_threshold": 250,
        "current_duration_s": 60
      },
      "proposed_change": {
        "kind": "threshold_update",
        "target": { "source": "datadog", "native_id": "monitor:4821337", "path": "options.thresholds.critical" },
        "current": 250,
        "proposed": 400,
        "rationale": "p99 has sat at 320–360ms for 90d per fire-time snapshots; 250ms fires ~daily with 87% no-action.",
        "generated_by": "skill",
        "diff": null
      }
    },
    {
      "id": "ald-c07d5a33",
      "type": "identity",
      "severity": "low",
      "confidence": "low",
      "subject": { "source": "newrelic", "native_id": "policy:998811", "signal": null },
      "rationale": "New Relic policy 'pmts-api-golden' has no CI reference; fuzzy match suggests this CI. Confirmation required before its data can join scoring.",
      "evidence": {
        "unresolved_artifact": { "source": "newrelic", "kind": "alert_config", "native_id": "policy:998811", "native_name": "pmts-api-golden" },
        "candidates": [ { "ci_id": "CI0012345", "ci_name": "payments-api", "match_score": 0.83, "method": "fuzzy" } ],
        "reason": "ambiguous_candidates"
      },
      "proposed_change": null
    }
  ],
  "metadata": {
    "run": { "timestamp": "2026-07-04T17:22:09Z", "tool_version": "0.4.2", "invocation_id": "9d2c7a1e" },
    "window": { "start": "2026-04-05T00:00:00Z", "end": "2026-07-04T00:00:00Z", "days": 90 },
    "config": {
      "weights": { "noise": 45, "coverage": 30, "threshold": 25 },
      "priority_formula_version": "1",
      "config_hash": "sha256:1c0e7b9aa4f2d8c1"
    },
    "archetype_library_version": "2026.06.2",
    "convention_ruleset_version": "2026.06.5",
    "sources": [
      { "source": "datadog", "adapter_version": "0.4.2", "canonical_schema_version": "1", "snapshot_key": "datadog/acme-prod/2026-04-05_2026-07-04" },
      { "source": "pagerduty", "adapter_version": "0.4.2", "canonical_schema_version": "1", "snapshot_key": "pagerduty/acme/2026-04-05_2026-07-04" },
      { "source": "servicenow", "adapter_version": "0.4.2", "canonical_schema_version": "1", "snapshot_key": "servicenow/acme/2026-04-05_2026-07-04" }
    ]
  }
}
```

### Versioning & evolution

`contract_version` is semver, incremented independently of the tool version.

**Additive (minor bump).** Safe because consumers are tolerant readers:
- new **optional** fields anywhere;
- new keys inside any `evidence` object (the per-type *required* sets only grow additively);
- new `proposed_change.kind` values — consumers MUST skip unknown kinds, not fail;
- new `identity` finding `reason` values (same skip rule).

**Breaking (major bump).** Requires a migration note and a deprecation cycle:
- removing or renaming any field, or changing a field's type or units;
- changing the meaning or range of any score, or the direction of `priority_score`;
- **adding or removing a member of `findings[].type`** — the taxonomy is the greppable surface CI checks match exhaustively (REQ-OUT-003), so even an addition is breaking by policy;
- adding or removing members of `severity`, `confidence`, `analysis_state`, or `resolution.method`;
- changing finding-`id` derivation (breaks cross-run diffing).

Rules of engagement: the CLI emits exactly one contract version per release; the skill and aggregator declare the major version they support and refuse (with a clear error) a document from a different major. Canonical source-model versions (ADR 0004) evolve alongside but independently — they appear here only as metadata.

### Aggregation layer (ADR 0001)

A separate subcommand (`alertlint aggregate <dir>...`) — deliberately dumb:

- **Input:** one or more directories of per-service documents (a corpus). Filename convention: `<ci_name>.<ci_id>.json`, plus at most one `_unresolved.json` per run. The `ci_id` in the filename is convenience only; the JSON field is authoritative.
- **Merge across corpora** (different callers, different laptops — REQ-EXEC-002): deduplicate on `identity.ci.id`; when the same CI appears more than once, **the document with the newest `metadata.run.timestamp` wins whole**; documents are never field-merged. Mixed contract *majors* in one merge are an error; mixed minors are fine.
- **Rank:** sort descending by `scores.priority_score`. Ties break deterministically: lower `criticality_tier` first, then lexicographic `ci.id`. Documents with `priority_score: null` (all-dormant / insufficient data) and `_unresolved.json` go to separate appended sections, never interleaved into the ranking.
- **No scoring logic, ever.** The aggregator never reads `evidence`, never touches weights, never normalizes against the corpus. Because the priority formula is fixed and corpus-independent (REQ-EXEC-003), concatenate-and-sort is the *entire* algorithm; a team corpus and a fleet corpus rank identically for the services they share. Peer-relative annotations, if ever wanted, are aggregation-layer decoration and never feed back into scores (ADR 0001).
- **Output:** the ranked worklist as NDJSON of `{ci_id, ci_name, criticality_tier, priority_score, finding_counts_by_type, source_document}` — itself greppable, and traceable back to each source document.

### CLI surface (contract-relevant only)

- `alertlint scan … --out <dir>` writes one document per resolved CI plus `_unresolved.json` into `<dir>`; with a single `--service`, the document also goes to stdout (REQ-ARCH-001).
- `alertlint validate <file|dir>` validates documents against the bundled JSON Schema for the current contract version — the same schema published in-repo for CI use.
- `alertlint aggregate <dir>... [--format ndjson]` as above.

## Alternatives considered

- **One monolithic run-level document** (all services in one JSON). Loses the atomic unit: corpora from different callers can't be merged by file, partial runs aren't resumable, and a single service's document can't be handed to the skill alone. Rejected by REQ-OUT-001/004 and ADR 0001.
- **NDJSON stream of findings (no per-service envelope).** Maximally greppable, but destroys the identity/scores/metadata cohesion the skill needs in one read, and reproducibility metadata would be duplicated per line or lost. The aggregator's *output* is NDJSON; the contract is not.
- **Numeric confidence (0.0–1.0) on findings.** More expressive, but false precision from heuristics, and it ruins greppability (`select(.confidence < 0.4)` thresholds drift into de-facto contract). Bands (`high|medium|low`) are the stable CLI→skill signal per ADR 0003; the numbers that produced the band live in `evidence`. Revisit only with data (see Open questions).
- **Skill writes its output as a separate document type.** Two schemas to version and the aggregator would need join logic. Instead the skill enriches in place under the same schema with `generated_by` provenance — one contract, additive enrichment.
- **Open finding taxonomy (adding types is a minor bump).** Rejected: CI checks and grep patterns enumerate the taxonomy (REQ-OUT-003); silent growth would make "no unknown types" checks impossible. Frozen enum, additions are major.

## Testing & acceptance

**Strategy**

- A JSON Schema (draft 2020-12) file, `schemas/output-contract-v1.json`, lives in-repo, versioned with this spec; `alertlint validate` and CI both use it. Golden-fixture documents (including the example above and an `_unresolved.json`) are round-tripped in CI.
- Determinism test: two `scan` runs against the same ADR 0004 snapshot cache produce byte-identical documents modulo `metadata.run.{timestamp,invocation_id}` (ADR 0003 — no LLM in the CLI).
- Merge test: two overlapping corpora with different run timestamps aggregate to the newest document per CI, ranking unchanged versus scoring the union directly (REQ-EXEC-003).
- Tolerant-reader test: a fixture with unknown extra fields at every level validates and aggregates cleanly.

**Acceptance criteria — every one is a runnable jq check (REQ-OUT-003: greppable, CI-checkable):**

```bash
# 1. Contract version is present and major-1
jq -e '.contract_version | startswith("1.")' doc.json

# 2. Only the frozen taxonomy appears — fails on any unknown finding type
jq -e '([.findings[].type] - ["noise","coverage","threshold","identity"]) == []' doc.json

# 3. Every finding carries rationale, evidence, severity, confidence
jq -e '.findings | all(.rationale and .evidence and .severity and .confidence)' doc.json

# 4. The ADR 0003 handoff works: list the skill's triage queue (low-confidence findings)
jq -r '.findings[] | select(.confidence=="low") | [.id,.type,.rationale] | @tsv' doc.json

# 5. Level-B proposals are concrete, never prose-only
jq -e '[.findings[].proposed_change | select(. != null)] | all(.kind and .target and (.proposed != null) and .generated_by)' doc.json

# 6. Reproducibility metadata is complete
jq -e '.metadata | .window.days and .run.tool_version and .archetype_library_version and .config.weights and .config.config_hash' doc.json

# 7. Fuzzy matches never join scoring (ADR 0002): no artifact resolved by "fuzzy"
jq -e '[.identity.artifacts[].resolution.method] - ["exact","confirmed","convention"] == []' doc.json

# 8. The whole aggregation layer, falsifiably: rank a corpus with no scoring logic
jq -s 'map(select(.scores.priority_score != null)) | sort_by(-.scores.priority_score)
       | .[] | [.identity.ci.id, .scores.priority_score] | @tsv' -r corpus/*.json

# 9. Fleet grep: count noise findings across a corpus in one line
jq -s '[.[].findings[] | select(.type=="noise")] | length' corpus/*.json
```

Done = schema file merged, `validate` and `aggregate` pass all nine checks against the golden fixtures in CI, and [skill-integration.md](skill-integration.md) can be drafted against the frozen schema without open placeholders.

## Open questions

- **Denominator for `identity.mapping` coverage.** Resolved artifacts are countable, but "artifacts that *should* have mapped to this CI" is not knowable per service from the CI side (unmapped artifacts have no service). Current shape reports counts + a `coverage_note`; whether a true per-service ratio is computable, and how, is owned by [identity-resolution.md](identity-resolution.md). Owner: dave, resolve during identity-resolution spec review.
- **`_unresolved.json` at fleet scale.** One reserved document per run is fine on a laptop; when corpora from many callers merge, unresolved documents from different runs can't dedup on `ci.id` (it's null). Plan: aggregator concatenates them keyed by `(source, native_id)`; confirm when the aggregator is implemented. Owner: aggregation implementation bead.
- **Enriched-document placement.** Does the skill overwrite the document in place or write a sibling (e.g., `*.enriched.json`)? In-place keeps the corpus single-file-per-CI (aggregator stays dumb) but destroys the pristine CLI output; sibling preserves both but needs a dedup rule. Leaning in-place with the snapshot cache as the pristine source of truth. Owner: [skill-integration.md](skill-integration.md).
- **Confidence bands vs. numbers.** Bands chosen for v1 (see Alternatives); revisit after the skill has triaged real corpora — if the skill routinely needs finer grain, add an optional numeric `evidence.confidence_score` (additive change). Owner: dave, post-v1 review.
- **Severity scale semantics.** Four levels are fixed here; the evidence→severity mapping (and whether criticality tier influences finding severity or only priority score) belongs to [scoring-engine.md](scoring-engine.md) and is unresolved. Owner: scoring-engine spec.
- **`proposed_change.diff` format for level-C.** Reserved as opaque string; unified diff vs. structured JSON-patch-per-vendor is deferred until auto-PR design starts (REQ-REC-003). Must be decided before any level-C work. Owner: future level-C bead.
- **Filename sanitization.** `<ci_name>.<ci_id>.json` assumes CI names are filesystem-safe; define the sanitization rule (or drop the name component) before implementation. Owner: CLI implementation bead.
