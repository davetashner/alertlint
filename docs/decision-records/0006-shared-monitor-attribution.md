# 0006 — Shared monitors: per-event attribution, config membership by reference

- **Status:** Accepted
- **Date:** 2026-07-04
- **Deciders:** dave
- **Resolves:** the multi-CI mapping open question in [identity-resolution.md](../specs/identity-resolution.md); beads alertlint-o4i
- **Requirements:** REQ-ID-001, REQ-ID-002, REQ-ID-005, REQ-SCORE-006

## Context

Real estates are full of shared monitors: one Datadog multi-alert monitor (`avg by {service}`) covers dozens of services, one synthetic check watches a shared gateway. Today an alert config resolves to at most one CI, so a shared monitor's noise lands entirely on whichever service its tags happen to name — or on none. The identity spec deferred multi-CI mapping; a pilot cannot ship with the fleet's most common monitor shape mis-attributed.

## Options considered

1. **Config fan-out in adapters** — emit one `AlertConfig` per group value. Rejected: adapters would need group semantics (violating the thin-translator rule of ADR 0004), native ids would need synthetic suffixes, and coverage counts would inflate with duplicated configs.
2. **Multi-CI mapping records in the resolver** — let one artifact map to N CIs. Rejected as the primary mechanism: it breaks the mapping table's join-key invariant (one artifact, one row), complicates coverage ratios, and still doesn't say *which* fires belong to *which* service.
3. **Per-event attribution + config membership by reference** — accepted, below.

## Decision

**Fires attribute per event; configs join every service whose events reference them.**

- **Events already carry the truth.** A multi-alert monitor's firing episodes are per group, and each episode's identity hints carry that group's tags (`service:checkout-api`). The resolver already maps every event artifact independently — per-event attribution requires no resolver change. Noise burden therefore lands on the service that actually fired, by construction.
- **Configs join by event reference.** In the pipeline join, an alert config becomes a member of every CI that owns at least one event referencing it (by `alert_ref` id or name), in addition to any CI its own tags resolve to. Membership is not a mapping-table row: the config's single mapping (or unmapped state) is unchanged; membership is a join-stage fact.
- **Sharing is visible.** A config that joins more than one CI is marked `shared: true` in each document's artifact entry, and each document's cold-start/threshold context uses only that CI's own fires. Coverage treats a shared config's signals as present for every member (the signal genuinely covers them).
- **Scoring stays per-service facts only** (ADR 0001): a shared monitor contributes to each service exactly the fires that service's groups produced — never divided, never duplicated.

## Consequences

- No adapter changes and no resolver changes: the work is confined to the pipeline join plus an output-contract additive field (`shared` on artifact entries).
- A shared monitor's threshold findings (TH rules) are evaluated per member service over that member's fires — the same monitor can be correctly chatty for one service and quiet for another; proposals must therefore name the group scope, which the skill's `proposed_change` target `path` accommodates.
- Un-grouped fires of a shared monitor (no service-identifying hints) still fall to the owning CI when one exists, else `_unjoined_events` — visible, not lost.
- REQ-ID-005 is added to the requirements document; the identity spec's open question closes.
- Coverage double-counts a shared signal across members by design: each member genuinely has the signal. If calibration later shows this inflates coverage scores misleadingly, a dampening factor is a scoring_config decision, not an identity redesign.
