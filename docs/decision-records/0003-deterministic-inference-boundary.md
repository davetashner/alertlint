# 0003 — Deterministic/inference boundary: CLI computes, skill judges

- **Status:** Accepted
- **Date:** 2026-07-04
- **Deciders:** dave
- **Resolves:** D-3 in [requirements 0001 §14](../requirements/0001-initial-requirements.md)
- **Requirements:** REQ-ARCH-002, REQ-ARCH-003, REQ-ARCH-004, REQ-NOISE-003, REQ-NOISE-004, REQ-COV-003

## Context

REQ-ARCH-004 makes the deterministic/inference boundary a first-class design concern, but the requirements left the rule-by-rule split open. An unclear boundary produces the two classic failure modes: LLM judgment inside what should be a reproducible pipeline (scores that change between runs on identical input), or brittle deterministic rules trying to encode judgment they can't (e.g., hardcoding "self-healing vs. noise").

## Decision

**The litmus test:** if two runs on the same input must produce the same answer and the logic needs no service-specific context, it belongs in the CLI. If it requires judgment, external context, or generation, it belongs in the skill. Two hard rules follow: **the CLI never calls an LLM**, and **the skill never recomputes statistics** — it reasons over what the CLI emitted.

Rule-by-rule split:

| Concern | CLI (deterministic) | Skill (judgment) |
|---------|---------------------|------------------|
| Identity | Exact + convention resolution; fuzzy *candidates* | Confirming fuzzy candidates; repo-context enrichment |
| Firing stats | All counting: frequency, time-to-ack/close, auto-resolve rates, reassignment counts | — |
| Disposition | Classification per the fixed taxonomy, incl. the ambiguity default (never-acked + fast auto-resolve → noise, low-confidence) per REQ-NOISE-003 | Adjudicating low-confidence cases (self-healing vs. noise) using service context |
| Coverage | Archetype inference from existing telemetry (rule-based, path A of REQ-COV-003); missing-signal findings | Archetype enrichment from repo/OpenAPI (path C); overriding applicability |
| Thresholds | Behavior-inferred heuristics (fire frequency × no-action ratio) with evidence | Deciding what the *better* threshold/duration is; requesting the later-phase metric deep pass |
| Scores | Sub-scores, composite, criticality-weighted priority — all of it | Never adjusts a score; may *contextualize* in narrative |
| Recommendations | Finding emission (level A flags) with evidence | Level-B `proposed_change` generation; prioritization narrative |

## Options considered

The alternative boundaries — "CLI emits raw data only, skill does all classification" (maximizes flexibility, but destroys reproducibility and makes scores non-comparable across runs, violating REQ-SCORE-007) and "CLI does everything including proposed changes via templates" (reproducible, but produces mechanical, context-blind recommendations exactly where judgment adds value) — both fail the litmus test in one direction or the other.

## Consequences

- Scores are reproducible and regression-testable with plain fixtures; the CLI can run in CI with no LLM dependency or cost.
- The output contract must carry `confidence` and `evidence` on every determination (REQ-NOISE-004, REQ-OUT-003) — that is the handoff that lets the skill judge without recomputing.
- Low-confidence findings are the *designed* seam between the two halves, not an error state; the skill's first job on any service is triaging them.
- If a judgment rule later proves mechanical (e.g., a reliable self-healing detector emerges), it migrates skill → CLI in a versioned change; migration in the other direction should be treated as a design smell.
