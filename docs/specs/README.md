# Specifications

Feature and design specs for alertlint. A spec describes **what** a capability does and **how** it will work in enough detail to implement and test it — before the code exists.

## Conventions

- One spec per file: `short-kebab-title.md` (e.g., `service-scoring.md`, `alert-history-ingestion.md`).
- Start from [`TEMPLATE.md`](TEMPLATE.md).
- Specs are living documents while a feature is under development; once shipped, they become reference documentation and significant changes get a new spec or an ADR.
- Decisions with lasting architectural weight belong in [decision-records](../decision-records/), not buried in a spec.

## Index

| Spec | Scope | Status |
|------|-------|--------|
| [provider-adapters](provider-adapters.md) | Provider interfaces, canonical models, disposition taxonomy, snapshot cache | Draft |
| [identity-resolution](identity-resolution.md) | Layered artifact→CI resolver, mapping files, coverage metrics | Draft |
| [scoring-engine](scoring-engine.md) | Noise/coverage/threshold sub-scores, priority formula, cold-start states | Draft |
| [archetype-library](archetype-library.md) | Versioned archetype→required-signal library and applicability inference | Draft |
| [output-contract](output-contract.md) | Per-service JSON contract, finding taxonomy, aggregation/worklist | Draft |
| [skill-integration](skill-integration.md) | Skill workflow: triage, enrichment, level-B recommendations, packaging | Draft |
| [ci-pipeline](ci-pipeline.md) | GitHub Actions: doc links, traceability enforcement, Go build/test gates | Shipped |

## Reading order

`provider-adapters` → `identity-resolution` → `scoring-engine` (+ `archetype-library`) → `output-contract` → `skill-integration` mirrors the pipeline: adapt, resolve, score, emit, reason.
