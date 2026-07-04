# 0001 — Per-service atomic scoring with a separate aggregation layer

- **Status:** Accepted
- **Date:** 2026-07-04
- **Deciders:** dave
- **Resolves:** D-1 in [requirements 0001 §14](../requirements/0001-initial-requirements.md)
- **Requirements:** REQ-EXEC-001, REQ-EXEC-002, REQ-EXEC-003, REQ-OUT-004, REQ-SCORE-005

## Context

v1 runs as a CLI on the caller's laptop with the caller's own credentials (REQ-EXEC-001), so a single invocation can only see the services that caller can reach. But the product's deliverable is a *prioritized worklist* (REQ-SCORE-005), which is most valuable at fleet scope. We must decide whether the tool is architected around fleet-wide analysis, laptop-scoped analysis, or something that spans both without redesign.

## Options considered

1. **Fleet-scope central service from day one** — a deployed service with brokered credentials scoring everything. Pros: the worklist is immediately org-wide. Cons: credential brokering, deployment, and multi-tenant auth are large up-front costs that block all scoring work; contradicts the v1 execution model already fixed in REQ-EXEC-001.
2. **Laptop-scope as the permanent model** — the corpus is always whatever one caller can reach. Pros: simplest. Cons: bakes a ceiling into the design; fleet ranking would require a redesign later.
3. **Per-service scoring as the atomic unit + a separate aggregation layer** — one service in, one JSON document out (REQ-OUT-004); the worklist is an aggregation over whatever corpus of per-service outputs has been collected. Team-scoped on a laptop today, fleet-scoped if the same outputs are collected centrally later.

## Decision

Option 3, as recommended in the requirements draft. Two load-bearing rules make it work:

- **The priority formula is fixed and corpus-independent** (REQ-EXEC-003). Normalization inputs (criticality tier, traffic tier) come from per-service facts, never from statistics of the corpus being ranked (no percentile-within-corpus normalization). A service's priority score is therefore identical whether it was scored alone or among 5,000 others.
- **Aggregation is a distinct component** that consumes a directory/corpus of per-service JSON documents and sorts by priority score. It holds no scoring logic.

## Consequences

- Scoring work can start immediately with zero deployment infrastructure; fleet scope later is a collection problem, not a scoring redesign.
- Rankings merge trivially: corpora produced by different callers can be concatenated and re-sorted, enabling incremental fleet coverage.
- The constraint "no corpus-relative normalization" must be enforced in the scoring spec — it rules out some otherwise-attractive techniques (e.g., grading a service on a curve against peers). If peer-relative insight is ever wanted, it must be an aggregation-layer annotation, never an input to the score.
- Central deployment (later phase) reuses the CLI unchanged; only credentials and scheduling differ.
