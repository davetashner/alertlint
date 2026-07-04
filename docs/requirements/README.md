# Requirements

The source of truth for **what** alertlint must do and **why**. Specs in [docs/specs](../specs/) describe *how* a requirement will be implemented; decision records in [docs/decision-records](../decision-records/) capture the significant choices made along the way.

## Conventions

- One requirements document per file, numbered: `NNNN-short-kebab-title.md` (e.g., `0001-initial-requirements.md`).
- Every individual requirement gets a **stable, categorized ID**: `REQ-<CATEGORY>-NNN` (e.g., `REQ-SCORE-001`, `REQ-NOISE-003`). IDs are never reused or renumbered — if a requirement is dropped, mark it `Withdrawn` and leave it in place.
- Each requirement states one testable capability or constraint. If you can't imagine an acceptance test for it, it's context, not a requirement.
- Open architecture decisions are labeled `D-N` inside the requirements doc; each is resolved by an ADR in [decision-records](../decision-records/), after which the doc's `spec_ready` frontmatter flips to `true`.
- Requirement status: `Proposed` → `Accepted` → (`Implemented` | `Withdrawn`).

## Traceability

The chain is: **requirement → spec → decision records → implementation.**

- Every spec lists the requirement IDs it addresses in its header (see the [spec template](../specs/TEMPLATE.md)).
- Every accepted requirement should eventually be covered by at least one spec; the table below tracks coverage by category.
- Decisions that shape how a requirement is met get an ADR, referenced from the spec.
- Commits and beads issues reference spec/requirement IDs where relevant.

## Documents

| Doc | Title | Status |
|-----|-------|--------|
| [0001](0001-initial-requirements.md) | Alert Analysis Skill + CLI — Initial Requirements (v0.1) | Accepted — D-1..D-4 ratified (ADRs 0001-0004), specs drafted |

## Coverage

Coverage is tracked per requirement category; specs list the individual IDs they address.

| Category | Scope | Covered by spec(s) |
|----------|-------|--------------------|
| REQ-GOAL / REQ-NG | Goals and non-goals | [scoring-engine](../specs/scoring-engine.md), [skill-integration](../specs/skill-integration.md) |
| REQ-ARCH | CLI/skill division of labor | [skill-integration](../specs/skill-integration.md) |
| REQ-ID | Canonical identity (CMDB CI) and join problem | [identity-resolution](../specs/identity-resolution.md) |
| REQ-SRC | Data-source tiers and provider adapters | [provider-adapters](../specs/provider-adapters.md) |
| REQ-SCORE | Scoring model and priority ranking | [scoring-engine](../specs/scoring-engine.md) |
| REQ-NOISE | Noise inference and disposition taxonomy | [scoring-engine](../specs/scoring-engine.md), [provider-adapters](../specs/provider-adapters.md) |
| REQ-CRIT | Criticality and normalization | [scoring-engine](../specs/scoring-engine.md), [identity-resolution](../specs/identity-resolution.md) |
| REQ-COV | Coverage and archetype library | [archetype-library](../specs/archetype-library.md) |
| REQ-THRESH | Threshold quality (behavior-inferred v1) | [scoring-engine](../specs/scoring-engine.md) |
| REQ-HIST | History window and cold-start handling | [scoring-engine](../specs/scoring-engine.md), [provider-adapters](../specs/provider-adapters.md) |
| REQ-OUT | Per-service JSON output contract | [output-contract](../specs/output-contract.md) |
| REQ-EXEC | Execution and auth model | [output-contract](../specs/output-contract.md) |
| REQ-REC | Recommendation levels A/B/C | [skill-integration](../specs/skill-integration.md), [output-contract](../specs/output-contract.md) |
