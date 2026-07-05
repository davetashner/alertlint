---
title: "Alert Analysis Skill + CLI — Initial Requirements"
slug: alert-analysis-skill
status: draft
version: 0.1
stage: requirements            # requirements -> spec -> implementation
owner: dave
consumers: [claude-skill (primary), service-owners (secondary)]
traceability:
  requirement_id_prefix: REQ
  spec_ready: true             # D-1..D-4 resolved by ADRs 0001-0004
last_updated: 2026-07-04   # D-1..D-4 ratified
---

# Alert Analysis Skill + CLI — Initial Requirements (v0.1)

> Working name TBD (`alertlint` / `signalcheck` / `pagermind`). Naming does not
> block spec generation; referred to below as **the tool**.

## 1. Problem & purpose

Alerting quality across the fleet is uneven: some services are drowning in
non-actionable noise, others are missing obvious low-hanging-fruit alerts, and
many have thresholds that no longer reflect reality. There is no consistent way
to **prioritize where engineering attention should go first** across a large,
multi-vendor estate.

The tool scores each service on alerting quality and emits a **prioritized
worklist** so the organization can direct improvement effort to the
highest-leverage services first. It reduces alert noise, surfaces missing
archetype-appropriate alerts, and (behavior-inferred in v1) recommends better
thresholds.

## 2. Goals / non-goals

**Goals**
- `REQ-GOAL-001` Score a service's alerting quality from its alert configuration,
  firing history, and human response ("action taken").
- `REQ-GOAL-002` Produce sub-scores in distinct dimensions that roll into a
  **criticality-weighted priority score** used to rank services.
- `REQ-GOAL-003` Emit typed, evidence-backed **findings** an agent can reason
  over and turn into fixes.
- `REQ-GOAL-004` Recommend at two levels: **flag** (A) and **propose specific
  change** (B). Auto-PR (C) is out of scope for v1.

**Non-goals (v1)**
- `REQ-NG-001` No auto-PR / write-back to alert-config repos.
- `REQ-NG-002` No metric-derived threshold math (pulling raw time-series);
  deferred to a later phase.
- `REQ-NG-003` Not a human-operated CLI UX; humans are secondary consumers who
  read the agent's output, not the raw stdout.
- `REQ-NG-004` Not a real-time/streaming system; it runs as a batch analysis over
  a historical window.

## 3. Consumers & division of labor

- `REQ-ARCH-001` **Primary consumer is the Claude skill / agent.** The CLI's
  structured stdout **is the interface**; it is optimized for machine
  consumption, not human reading.
- `REQ-ARCH-002` **CLI = deterministic heavy lifting.** Pulls data from source
  systems, resolves identity, computes firing/disposition statistics, applies
  deterministic rules, and emits per-service JSON.
- `REQ-ARCH-003` **Skill = reasoning + recommendation.** Consumes CLI JSON plus
  per-service context, decides which findings matter, reasons over
  low-confidence cases, and generates level-B proposed changes.
- `REQ-ARCH-004` The deterministic/inference boundary is a first-class design
  concern: anything reproducible and cheap belongs in the CLI; anything requiring
  judgment or service-specific context belongs in the skill.

## 4. Canonical identity & the join problem

Config, firing history, and action-taken live in different systems that do not
share identifiers. Scoring a *service* requires joining alert → fires → human
response.

- `REQ-ID-001` **The ServiceNow CMDB Configuration Item (CI) is the canonical
  service identity.** Every artifact from every source must resolve to a CI.
- `REQ-ID-002` Each non-ServiceNow source needs a **mapping to a CI** (e.g.
  Datadog monitor tags → CI, PagerDuty service → CI). The CLI performs this
  resolution as its first pipeline stage.
- `REQ-ID-003` **Unmappable artifacts are themselves a finding** (`type:
  identity`), not a silent drop. Coverage of the mapping is reported per service.
- `REQ-ID-004` Criticality tier is read from the CMDB CI and drives score
  normalization and ranking (see §7).
- `REQ-ID-005` **Shared monitors attribute per event** (added 2026-07-04,
  SRE assessment; ADR 0006): a multi-service monitor's fires land on the
  service whose group produced them, its config joins every service whose
  events reference it, and sharing is visible in output — never divided,
  never duplicated, never silently single-homed.

## 5. Data sources (tiered)

Tier-1 sources are required for v1; lower tiers are pluggable adapters added
later behind a common interface.

**Alert configuration**
- `REQ-SRC-001` Tier-1: Datadog, New Relic, CloudWatch, Splunk.
- `REQ-SRC-002` Tier-2 (later): Dynatrace, Azure Monitor, GCP Cloud Monitoring.

**Alerting history (what fired, when, how often)**
- `REQ-SRC-003` Tier-1: ServiceNow, PagerDuty.
- `REQ-SRC-004` Tier-2 (later): BMC Helix, Uptime Robot.
- `REQ-SRC-008` **Monitor-side firing history is Tier-1** (added 2026-07-04,
  SRE assessment): alerts that never page — chat/email-only routing — must
  still contribute firing history and auto-resolve signals (REQ-NOISE-001),
  or the noisiest alert class is invisible to scoring. First source: Datadog
  alert events. When paging history and monitor history carry the same
  episodes, paging history is authoritative (richer response trail).

**Action taken (human response signal)**
- `REQ-SRC-005` Tier-1: ServiceNow, PagerDuty.
- `REQ-SRC-006` Investigate: CloudTrail (as a corroborating source for whether a
  change/action actually followed an alert). Treated as optional enrichment.

- `REQ-SRC-007` All source integrations sit behind a **provider-adapter
  interface** so the scoring core is vendor-agnostic. Adding a vendor = adding an
  adapter, not touching the scoring engine.

## 6. Scoring model

Three sub-scores, computed deterministically by the CLI from behavior signals.

- `REQ-SCORE-001` **Noise** — what fraction of what fires is actionable.
  *Heaviest weight in v1* (alert fatigue is the presenting problem).
- `REQ-SCORE-002` **Coverage** — are archetype-appropriate signals present for
  this service (see §8).
- `REQ-SCORE-003` **Threshold quality** — are existing alerts tuned. **v1 =
  behavior-inferred only** (from fire frequency + disposition). Metric-derived is
  a later phase.
- `REQ-SCORE-004` Default sub-score weights: **Noise 45 / Coverage 30 / Threshold
  25**, overridable via config.

**Ranking (the actual deliverable)**
- `REQ-SCORE-005` The output is a **prioritized worklist**, not a report card.
  Services are ranked by a **criticality-weighted priority score** that combines
  the sub-scores with the CMDB criticality tier, so a mediocre tier-1 service
  outranks a terrible tier-4 one for engineering attention.
- `REQ-SCORE-006` Normalization: raw counts are normalized by
  traffic/criticality tier so a high-volume service and a low-volume one are
  graded comparably. Absolute alert counts are never compared directly.
- `REQ-SCORE-007` Weights and the priority formula are defined in config and
  versioned, so scores are reproducible and comparable across runs.

### 6.1 Noise inference (disposition taxonomy)

- `REQ-NOISE-001` Primary signals: **"Closed – No Action"** disposition
  (semi-reliable ServiceNow field) and **auto-resolve** status from the
  monitor/metric system.
- `REQ-NOISE-002` Timing signals as fallback when disposition fields are mushy:
  time-to-ack, time-to-close, never-acked, fast-auto-resolve, reassignment count,
  whether a change/incident record was actually linked.
- `REQ-NOISE-003` **Ambiguity default:** an alert that fired, was never acked,
  and auto-resolved quickly with no "Closed – No Action" code is scored as
  **noise**, but tagged **low-confidence** so the agent can reason over the
  self-healing-vs-noise distinction.
- `REQ-NOISE-004` Every noise determination carries a **confidence** value and
  the evidence that produced it.
- `REQ-NOISE-005` **Maintenance awareness** (added 2026-07-04, SRE
  assessment): fires that occur inside a declared maintenance window
  (monitor downtimes, paging maintenance windows) must not count toward
  noise burden by default — deploy-window flapping otherwise inflates
  scores. Suppression is configurable and every suppressed fire remains
  visible in evidence (never a silent drop).

## 7. Criticality & normalization

- `REQ-CRIT-001` Criticality tier is sourced from the CMDB CI.
- `REQ-CRIT-002` Criticality enters the ranking (priority weighting), not just
  the per-service normalization — see `REQ-SCORE-005`.
- `REQ-CRIT-003` Missing/unknown criticality is a finding and defaults to a
  configurable middle tier so the service is neither hidden nor over-prioritized.

## 8. Coverage & the archetype library

- `REQ-COV-001` The tool checks each service against an **opinionated,
  versioned archetype → required-signal library** that is a **first-class repo
  asset** (methodology-anchored: golden signals, saturation, error-budget burn,
  cert expiry, etc.).
- `REQ-COV-002` Archetypes include at least: REST API (latency / error-rate /
  saturation), socket connections (connection-pool / liveness), and business
  transactions (success-rate / volume-anomaly / reconciliation lag). The library
  is extensible.
- `REQ-COV-003` **Signal-type applicability is determined per service.** Sources
  of per-service context, in precedence order:
  - (A, default) **CLI infers** applicable archetypes from the telemetry that
    already exists for the service — works even with no manifest.
  - (C) **Agent enriches** by reading the service's repo / OpenAPI spec.
  - (D) **Human-in-the-loop** confirmation for ambiguous cases.
- `REQ-COV-004` A missing archetype-appropriate alert is a `type: coverage`
  finding with the specific signal that's absent and why it applies.

## 9. Threshold quality (v1 = behavior-inferred)

- `REQ-THRESH-001` v1 infers threshold problems from behavior only: e.g. an alert
  that fires very frequently with a high no-action/auto-resolve ratio implies a
  threshold too tight or a duration too short.
- `REQ-THRESH-002` **Metric-derived thresholds are a later phase.** When enabled,
  they pull raw time-series from CloudWatch / Datadog / New Relic to compute
  percentile-based recommendations. This is an **opt-in deeper pass** the agent
  requests only for services that already score badly — never a fleet-wide series
  pull.

## 10. History window & cold-start handling

- `REQ-HIST-001` Default analysis window: **90 days.** Configurable.
- `REQ-HIST-002` **Config exists, zero fires in window → "dormant / healthy"**:
  not penalized, but surfaced separately so a dead/silenced monitor cannot hide.
- `REQ-HIST-003` **Newly created alert with little history → "insufficient
  data"**: excluded from scoring rather than scored on thin evidence.
- `REQ-HIST-004` Both states are explicit values in the output, never conflated
  with "healthy."

## 11. Output contract (per-service JSON)

- `REQ-OUT-001` The CLI emits **one JSON document per service**; this is the
  stable interface the skill consumes.
- `REQ-OUT-002` Each document contains:
  - **identity**: CI, resolved source artifacts, mapping coverage/gaps.
  - **scores**: the three sub-scores, composite, criticality tier, and
    criticality-weighted priority score.
  - **findings[]**: typed, greppable, evidence-backed items.
  - **metadata**: window, run timestamp, tool + archetype-library versions,
    weight config used (for reproducibility).
- `REQ-OUT-003` **Finding taxonomy is stable and defined now** (so findings are
  greppable and CI-checkable — consistent with CI-enforced traceability). Each
  finding has:
  - `type` — one of `noise | coverage | threshold | identity`.
  - `severity`.
  - `confidence`.
  - `rationale` — human-readable.
  - `evidence` — fire counts, disposition ratios, timing stats, etc.
  - `proposed_change` — optional level-B block (concrete diff / computed value)
    when a specific fix is available.
- `REQ-OUT-004` Fleet aggregation consumes a **corpus of per-service JSON** and
  ranks by priority score (see §12). Per-service scoring is the atomic unit; the
  worklist is an aggregation layer.

## 12. Execution & auth model

- `REQ-EXEC-001` v1 runs as a **CLI on the caller's laptop using the caller's own
  credentials** to each source tenant. No central credential broker in v1.
- `REQ-EXEC-002` Consequence: a single invocation only sees the services the
  caller can reach. The **atomic unit is per-service scoring**; the "prioritized
  worklist" is an **aggregation over the set of per-service outputs collected** —
  team-scoped on a laptop today, fleet-scoped if run centrally later.
- `REQ-EXEC-003` The **priority formula is fixed regardless of corpus size**, so
  rankings are consistent whether the corpus is one team or the whole fleet.

## 13. Recommendation levels

- `REQ-REC-001` Level A (**flag**): "this alert is noisy" / "this threshold looks
  wrong" / "this archetype signal is missing." Required in v1.
- `REQ-REC-002` Level B (**propose specific change**): concrete config diff,
  computed value, or dedup/grouping/inhibition suggestion, carried in
  `proposed_change`. Required in v1.
- `REQ-REC-003` Level C (**auto-PR**): deferred. Design should not preclude it —
  the `proposed_change` block is the natural seam for later PR generation.

## 14. Open architecture decisions (resolve before spec_ready)

These load-bearing forks were deliberately left open in v0.1; all four are now resolved by the linked decision records.

- `D-1` **Fleet vs laptop scope.** *Resolved by [ADR 0001](../decision-records/0001-fleet-vs-laptop-scope.md).* Recommendation: per-service atomic scoring +
  separate aggregation layer; v1 worklist is team-scoped (caller's reachable
  services), fleet-scope arrives with an optional later central deployment. Fixed
  priority formula either way. — *needs ratification.*
- `D-2` **Identity resolution robustness.** *Resolved by [ADR 0002](../decision-records/0002-identity-resolution-strategy.md).* How much CMDB tagging discipline
  actually exists across Datadog/PD/etc.? Determines whether v1 must *bootstrap*
  significant mapping (heuristics, tag conventions, fuzzy match) or can assume
  clean CI references.
- `D-3` **Deterministic/inference boundary.** *Resolved by [ADR 0003](../decision-records/0003-deterministic-inference-boundary.md).* Exactly which noise/threshold rules
  are deterministic CLI logic vs. agent judgment. Draft split above; needs a
  concrete rule-by-rule pass.
- `D-4` **Adapter interface shape.** *Resolved by [ADR 0004](../decision-records/0004-provider-adapter-interface.md).* The provider-adapter contract for the three
  data classes (config, history, action) that keeps the scoring core
  vendor-agnostic. Blocks any parser work.

## 15. Phasing

- **v1**: tier-1 sources; ServiceNow-CI identity; behavior-inferred noise +
  coverage + threshold sub-scores; criticality-weighted worklist; findings with
  level-A/B recommendations; per-service JSON; laptop + caller creds.
- **Later**: metric-derived thresholds (opt-in deep pass); tier-2 source
  adapters; central deployment for fleet-scope; level-C auto-PR.
