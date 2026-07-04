# Spec: Scoring Engine

- **Status:** Draft
- **Date:** 2026-07-04
- **Beads issue:** alertlint-md1
- **Requirements:** REQ-SCORE-001, REQ-SCORE-002, REQ-SCORE-003, REQ-SCORE-004, REQ-SCORE-005, REQ-SCORE-006, REQ-SCORE-007, REQ-NOISE-001, REQ-NOISE-002, REQ-NOISE-003, REQ-NOISE-004, REQ-CRIT-001, REQ-CRIT-002, REQ-CRIT-003, REQ-THRESH-001, REQ-THRESH-002, REQ-HIST-001, REQ-HIST-002, REQ-HIST-003, REQ-HIST-004 (see [docs/requirements](../requirements/))
- **Decision records:** [ADR 0001 — Per-service atomic scoring](../decision-records/0001-fleet-vs-laptop-scope.md), [ADR 0003 — Deterministic/inference boundary](../decision-records/0003-deterministic-inference-boundary.md)

## Problem

Alerting quality is uneven across the fleet and there is no consistent way to decide where engineering attention goes first. The scoring engine is the deterministic core that turns a service's alert configuration, firing history, and human-response signals into three sub-scores, a composite, and a criticality-weighted priority score — the number the worklist ranks by. Every downstream consumer (the skill, the aggregation layer, service owners reading the agent's output) depends on these scores being reproducible, evidence-backed, and comparable across runs and across corpora of any size.

The engine sits between the normalized input bundle produced by the adapters ([provider-adapters.md](provider-adapters.md)) and identity resolution ([identity-resolution.md](identity-resolution.md)), and the per-service JSON document defined in [output-contract.md](output-contract.md). Per ADR 0003, everything in this spec is deterministic CLI logic: no LLM calls, no judgment — judgment happens downstream in the skill ([skill-integration.md](skill-integration.md)).

## Goals

- Compute the three sub-scores — **Noise**, **Coverage**, **Threshold quality** — deterministically from the normalized per-service input bundle (REQ-SCORE-001..003).
- Combine them into a composite using configurable, versioned weights (default **Noise 45 / Coverage 30 / Threshold 25**, REQ-SCORE-004, REQ-SCORE-007).
- Compute a **criticality-weighted priority score** with a fixed, corpus-independent formula (REQ-SCORE-005, ADR 0001).
- Normalize by traffic/criticality tier using **per-service facts and config constants only** — never statistics of the corpus being ranked (REQ-SCORE-006, ADR 0001).
- Classify every fire's disposition per the fixed noise taxonomy, including the ambiguity default, with **confidence and evidence on every determination** (REQ-NOISE-001..004).
- Infer threshold problems from behavior only (fire frequency × no-action ratio heuristics) with tunable, versioned parameters (REQ-THRESH-001).
- Emit explicit **dormant/healthy** and **insufficient-data** states that are never conflated with a healthy score (REQ-HIST-001..004).
- Be fully reproducible: same input bundle + same config version = byte-identical scores and findings, regression-tested with fixtures.

## Non-goals

- **Metric-derived threshold math.** No time-series pulls; the later-phase deep pass is requested by the skill, not this engine (REQ-THRESH-002, REQ-NG-002).
- **Corpus-relative normalization of any kind.** No percentile-within-corpus grading, no peer curves. Ruled out by ADR 0001; peer-relative insight, if ever wanted, is an aggregation-layer annotation.
- **Adjudicating low-confidence cases.** The self-healing-vs-noise distinction is the skill's job (ADR 0003); this engine only applies the ambiguity default and tags it.
- **Archetype applicability inference.** Which signals a service *should* have is defined in [archetype-library.md](archetype-library.md); this spec consumes its applicability result and computes the Coverage sub-score from it.
- **Ranking/aggregation.** Sorting the corpus by priority score is a distinct component (ADR 0001); this engine emits one service's scores.
- **Level-B `proposed_change` generation.** The engine emits level-A findings with evidence; concrete change proposals are skill territory ([skill-integration.md](skill-integration.md)).

## Design

### Inputs and outputs

**Input** (one service at a time):

- `identity`: resolved CMDB CI, criticality tier (or `unknown`), mapping coverage — from [identity-resolution.md](identity-resolution.md).
- `alerts[]`: normalized alert configs with creation timestamps — from [provider-adapters.md](provider-adapters.md).
- `fires[]`: firing events joined to alerts, each with timestamps, ack/close times, auto-resolve flag and duration, disposition code (if any), reassignment count, linked change/incident refs.
- `coverage_input`: applicable archetypes and required-signal presence/absence — from [archetype-library.md](archetype-library.md).
- `window`: analysis window (default 90 days, REQ-HIST-001) and run reference time, both passed in explicitly — the engine never reads the wall clock.
- `scoring_config`: the versioned parameter set (below).

**Output**: the `scores` block and `noise`/`threshold` findings of the per-service JSON defined in [output-contract.md](output-contract.md), plus the config version and weights actually applied (metadata, REQ-SCORE-007).

### Scoring config (versioned)

All constants live in a single config document with a `scoring_config_version` recorded in every output (REQ-SCORE-007). Changing any constant is a version bump; two runs with the same version and input are byte-identical. Defaults shown below are **proposed starting points pending calibration** (see Open questions).

```yaml
scoring_config_version: 1
weights: { noise: 45, coverage: 30, threshold: 25 }   # REQ-SCORE-004
window_days: 90                                        # REQ-HIST-001
cold_start:
  min_alert_age_days: 14        # younger => insufficient-data (REQ-HIST-003)
  min_fires_to_score: 3         # fewer classified fires => insufficient-data
noise:
  fast_auto_resolve_minutes: 10
  never_acked_grace_minutes: 30
  high_reassignment_count: 3
  tier_noise_budget_per_week: { tier1: 2.0, tier2: 3.0, tier3: 5.0, tier4: 8.0 }
threshold:
  chatty_fires_per_week: 10
  chatty_no_action_ratio: 0.70
  flap_p50_auto_resolve_minutes: 5
  flap_fires_per_week: 3
  burst_fires_per_day: 6
  burst_min_days: 3
criticality:
  multiplier: { tier1: 2.0, tier2: 1.5, tier3: 1.0, tier4: 0.7 }
  default_tier: tier3           # for unknown criticality (REQ-CRIT-003)
confidence:                     # per-rule confidence values (REQ-NOISE-004)
  disposition_no_action: 0.90
  linked_change_or_incident: 0.90
  acked_manual_close: 0.70
  ambiguity_default: 0.35
low_confidence_ceiling: 0.50    # determinations at/below this are tagged low_confidence
confidence_bands:               # numeric -> contract band mapping (see output-contract.md)
  high_floor: 0.85              # >= high_floor -> "high"; <= low_confidence_ceiling -> "low"; else "medium"
```

### Cold-start states (REQ-HIST-002..004)

Evaluated first, per alert, before any scoring:

- **`insufficient_data`** — alert created less than `min_alert_age_days` before the window end, or fewer than `min_fires_to_score` classified fires despite firing. Excluded from all three sub-scores.
- **`dormant_healthy`** — config exists, alert age ≥ `min_alert_age_days`, and **zero fires** in the window. Not penalized, excluded from Noise/Threshold, but emitted as an explicit per-alert state so a dead or silenced monitor cannot hide.
- **`scoreable`** — everything else.

Service-level rollup: if *all* alerts are `dormant_healthy`, the service state is `dormant`; if all are `insufficient_data` (or the service has no scoreable alerts), the service state is `insufficient_data` and no composite/priority is computed — the output carries the state instead of a score. These are distinct enum values in the output contract, never rendered as a high (or any) score (REQ-HIST-004). Dormant alerts still participate in Coverage (their signals count as present).

### Noise sub-score (weight 45)

**Per-fire disposition classification** — a fixed, ordered, first-match-wins decision table. Every classification carries `{class, rule_id, confidence, evidence}` (REQ-NOISE-004). Evidence is the raw facts the rule matched: disposition code, ack/close deltas, auto-resolve duration, reassignment count, linked record refs.

| Rule | Condition (in order) | Class | Confidence |
|------|----------------------|-------|------------|
| N-D1 | Disposition code is "Closed – No Action" (or taxonomy equivalent) | noise | 0.90 |
| N-D2 | A change or incident record is linked to the fire | actionable | 0.90 |
| N-D3 | Acked, manually closed, substantive disposition code present | actionable | 0.70 |
| N-D4 | Auto-resolved AND acked after auto-resolve (human arrived to a self-closed alert) | noise | 0.60 |
| N-T1 | Never acked within `never_acked_grace_minutes` AND auto-resolved in ≤ `fast_auto_resolve_minutes` AND no disposition code | noise, **low_confidence** | 0.35 |
| N-T2 | Never acked, no auto-resolve, closed without disposition code | noise, low_confidence | 0.45 |
| N-T3 | Acked but reassigned ≥ `high_reassignment_count` times before close | noise-leaning (`unclear`) | 0.40 |
| N-T4 | Anything else | unclassified | 0.20 |

N-D1/N-D2/N-D3 are the primary disposition-taxonomy signals (REQ-NOISE-001). N-T1..N-T3 are the timing fallbacks that apply only when disposition fields are mushy (REQ-NOISE-002). **N-T1 is the ambiguity default** mandated by REQ-NOISE-003: fired, never acked, fast auto-resolve, no disposition code → scored noise, tagged `low_confidence` so the skill can adjudicate self-healing vs. noise (the designed seam of ADR 0003). Fires classified `unclassified` are excluded from ratios but counted in evidence.

**Per-alert rollup:** for each scoreable alert, `noise_ratio = Σ(confidence of noise-classed fires) / Σ(confidence of all noise- or actionable-classed fires)` — confidence-weighted so a pile of low-confidence noise moves the ratio less than firm "Closed – No Action" evidence. An alert with `noise_ratio ≥ 0.5` and ≥ `min_fires_to_score` fires emits a `type: noise` finding whose confidence is the confidence-weighted mean of its contributing classifications and whose evidence includes the full per-class fire counts.

**Service sub-score with tier normalization (REQ-SCORE-006, ADR 0001):** absolute counts are never compared. The service's *noise burden* is a rate:

```
noisy_fires_per_week = Σ over scoreable alerts (confidence-weighted noise fires) / window_weeks
burden               = noisy_fires_per_week / tier_noise_budget_per_week[criticality_tier]
noise_score          = 100 × (1 − min(1, burden))
```

The budget table is a config constant keyed by the service's own criticality tier — a per-service fact from the CMDB CI — so a high-volume tier-1 service and a quiet tier-4 one are graded against their own budgets, and the score is identical whether the service is scored alone or among 5,000 others. No corpus statistic ever enters. (Whether a separate *traffic* tier fact exists in v1 or criticality tier proxies for it is an open question.)

### Coverage sub-score (weight 30)

Applicability and presence are computed by [archetype-library.md](archetype-library.md) (path A inference; skill enrichment per REQ-COV-003 never changes the CLI score). This engine computes:

```
coverage_score = 100 × (present_required_signals / applicable_required_signals)
```

If `applicable_required_signals = 0` (no archetype matched), coverage is `not_applicable` and its weight is redistributed (see composite). Missing signals are emitted by the archetype component as `type: coverage` findings; this engine only aggregates the ratio. Identity-mapping gaps (unmapped artifacts from [identity-resolution.md](identity-resolution.md)) cap the coverage finding confidence, since unseen telemetry may exist.

### Threshold sub-score (weight 25) — behavior-inferred only (REQ-THRESH-001)

Deterministic heuristics over fire frequency × disposition, per scoreable alert. All parameters tunable via config. Each match emits a `type: threshold` finding with rule ID, severity, confidence, and the measured statistics as evidence.

| Rule | Shape (defaults from config) | Interpretation | Severity |
|------|------------------------------|----------------|----------|
| TH-1 chatty-no-action | fires/week ≥ `chatty_fires_per_week` AND no-action ratio ≥ `chatty_no_action_ratio` | threshold too tight or duration too short | high |
| TH-2 flappy | p50 auto-resolve duration ≤ `flap_p50_auto_resolve_minutes` AND fires/week ≥ `flap_fires_per_week` | evaluation duration too short (condition recovers before a human could act) | medium |
| TH-3 bursty | ≥ `burst_fires_per_day` fires in a calendar day on ≥ `burst_min_days` days | missing dedup/grouping/inhibition rather than a wrong threshold | medium |
| TH-4 never-actioned | every classified fire in window is noise-classed (any confidence), fires ≥ `min_fires_to_score` | alert provides no operational value as tuned | high |

The *no-action ratio* in TH-1/TH-4 reuses the noise classifications above — the engine computes statistics once and both sub-scores read them, so evidence is consistent across findings. What the *better* threshold value would be is out of scope: that is skill judgment (ADR 0003) or the later-phase metric deep pass (REQ-THRESH-002).

```
flagged_weight    = Σ over flagged alerts (1.0 if any high-severity match, else 0.5)
threshold_score   = 100 × (1 − min(1, flagged_weight / scoreable_alert_count))
```

### Composite and priority score

**Composite** (REQ-SCORE-004): weighted mean of the available sub-scores. If a sub-score is unavailable (`not_applicable` coverage, or noise/threshold with zero scoreable alerts), its weight is redistributed proportionally across the remaining sub-scores and the output records both the configured and the effective weights, plus a `partial_score: true` flag — a partially-scored service is visibly partial, never silently complete.

```
composite = Σ(subscore_i × effective_weight_i) / Σ(effective_weight_i)
```

**Priority score** (REQ-SCORE-005, REQ-CRIT-001..003) — proposed formula:

```
priority = (100 − composite) × criticality.multiplier[tier]
```

- `100 − composite` is the *badness* — worklists rank by how much fixing is needed, not how good a service is.
- `tier` comes from the CMDB CI (REQ-CRIT-001). Unknown/missing criticality → `criticality.default_tier` (proposed `tier3`) plus a `type: identity` finding, so the service is neither hidden nor over-prioritized (REQ-CRIT-003).
- Proposed multipliers `{tier1: 2.0, tier2: 1.5, tier3: 1.0, tier4: 0.7}` give the required behavior that a mediocre tier-1 service (e.g., composite 60 → priority 80.0) outranks a terrible tier-4 one (composite 20 → priority 56.0). Exact constants are calibration targets, not final (Open questions).
- The formula uses only per-service facts and config constants — fixed regardless of corpus size (REQ-EXEC-003, ADR 0001). Ranking merges from different callers stay consistent by construction.

All scores are rounded half-even to one decimal at emission; ties in downstream ranking break deterministically on CI sys_id (specified in [output-contract.md](output-contract.md)).

### Determinism guarantees (ADR 0003)

- Pure function: `(input bundle, scoring config, window) → (scores, findings)`. No LLM, no network, no wall clock, no randomness.
- All iteration over alerts/fires is in a canonical sort order (alert ID, then fire timestamp, then fire ID) so floating-point accumulation order — and therefore output — is stable across platforms and input orderings.
- Every determination carries `confidence` and `evidence` (REQ-NOISE-004); determinations at or below `low_confidence_ceiling` carry the `low_confidence` tag — the explicit handoff to the skill ([skill-integration.md](skill-integration.md)).

## Alternatives considered

- **Corpus-percentile normalization** (grade each service against the distribution of the scored corpus): statistically attractive, but a service's score would change depending on who else was scored, breaking merge-and-rank and reproducibility. Rejected by ADR 0001; not relitigated here.
- **Probabilistic noise classifier / learned weights**: better recall on mushy dispositions, but not reproducible or explainable, and violates the ADR 0003 litmus test. The decision-table + confidence design gets the ambiguous cases to the skill instead.
- **Boolean noise classification (no confidence weighting)**: simpler rollup, but a service full of low-confidence ambiguity-default calls would score identically to one full of hard "Closed – No Action" evidence, and the skill would lose the signal it needs for triage. Confidence-weighting keeps the score honest about evidence quality.
- **Scoring insufficient-data alerts with a penalty or a neutral 50**: either punishes new alerts or launders thin evidence into a real-looking number. Explicit states (REQ-HIST-004) cost an enum in the contract and prevent both.
- **Multiplicative sub-score combination** (`noise × coverage × threshold`): punishes any single bad dimension harshly, but makes weights unintuitive and the composite hypersensitive near zero. Weighted mean with versioned weights matches REQ-SCORE-004 directly.

## Testing & acceptance

**Fixture-based golden regression tests** (the primary strategy, enabled by determinism):

- A `fixtures/scoring/` corpus of synthetic per-service input bundles, each exercising one behavior: every noise rule N-D1..N-T4 individually; every threshold rule TH-1..TH-4; the ambiguity default; each cold-start state; unknown criticality; partial-score weight redistribution; a kitchen-sink service combining all of it.
- Each fixture pairs with a committed expected-output JSON. The test runs the engine and asserts **byte-identical** output after canonical serialization (sorted keys, fixed float formatting). Any diff is either a bug or an intentional change that must bump `scoring_config_version` or the tool version and regenerate goldens in the same commit.
- Runs in CI with no credentials, no network, no LLM (ADR 0003 consequence).

**Property tests:**

- *Permutation invariance*: shuffling `fires[]` and `alerts[]` input order never changes output.
- *Corpus independence*: scoring a service alone equals scoring it as part of any batch (guards the ADR 0001 constraint structurally).
- *Monotonicity*: adding a "Closed – No Action" fire never raises the noise score; removing a required signal never raises coverage; raising a criticality multiplier never lowers priority.
- *State exclusivity*: no alert or service is simultaneously scored and in a cold-start state; `dormant_healthy` and `insufficient_data` never appear alongside a composite for an all-cold service.

**Acceptance criteria:**

1. All golden fixtures pass byte-identical; two consecutive runs on the same input produce identical bytes.
2. Every emitted noise/threshold finding carries `type`, `severity`, `confidence`, `rationale`, and non-empty `evidence` (REQ-OUT-003 shape, validated against [output-contract.md](output-contract.md)'s schema).
3. Every N-T1 match is tagged `low_confidence` and classed noise (REQ-NOISE-003 verified by fixture).
4. Output metadata records `scoring_config_version`, window, and effective weights for every run (REQ-SCORE-007).
5. Default weights are 45/30/25 and overriding them via config changes the composite as specified, with the override visible in metadata.

## Open questions

- **Numeric calibration of all defaults** — tier noise budgets, `chatty_fires_per_week`, `chatty_no_action_ratio`, `flap_*`, `burst_*`, cold-start thresholds, and the criticality multipliers are proposed starting points only. Plan: run the engine read-only over one pilot team's real 90-day window, review score distributions with service owners, and ratify constants as `scoring_config_version: 1` before any worklist is circulated. Owner: dave.
- **Per-rule confidence values** — the table (0.90/0.70/0.35/…) is directionally right but the exact numbers, and whether confidence should be discrete levels instead of a continuum, should be settled with the skill team since the skill's triage behavior keys off them ([skill-integration.md](skill-integration.md)). Owner: dave.
- **Traffic tier as a distinct fact** — REQ-SCORE-006 says "traffic/criticality tier." Does the CMDB (or any tier-1 source) expose a usable per-service traffic tier in practice, or does criticality tier proxy for volume in v1 as this spec assumes? If a real traffic fact exists, the noise budget table should key on `(criticality, traffic)` instead. Depends on findings from [identity-resolution.md](identity-resolution.md) and [provider-adapters.md](provider-adapters.md) field surveys. Owner: dave.
- **Partial-score weight redistribution vs. abstention** — redistributing weights keeps every service rankable but can rest a composite on a single sub-score. Alternative: emit `partial` composites but rank them in a separate worklist section. This is an aggregation/contract presentation choice; resolve with [output-contract.md](output-contract.md). Owner: dave.
- **Reassignment count (N-T3) as classifier vs. evidence-only** — high reassignment may indicate routing problems rather than noise; v1 proposes a weak `unclear` class, but demoting it to evidence-only (feeding a future `type: identity`/routing finding) may be cleaner. Decide during pilot calibration. Owner: dave.
- **Priority of dormant services** — a fully dormant service has no composite, hence no priority. Should the worklist surface dormant services in a dedicated section (proposed) or synthesize a low nonzero priority so they never vanish? Resolve with [output-contract.md](output-contract.md) aggregation design. Owner: dave.
