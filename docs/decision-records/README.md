# Decision Records

Architecture Decision Records (ADRs) for alertlint. Each record captures one significant decision: the context that forced it, the options considered, the decision made, and its consequences.

## Conventions

- One decision per file, numbered sequentially: `NNNN-short-kebab-title.md` (e.g., `0001-cli-language-choice.md`).
- Start from [`0000-template.md`](0000-template.md).
- Records are immutable once accepted. If a decision is reversed, write a new record that supersedes the old one and update the old record's status to `Superseded by NNNN`.

## Index

| # | Title | Status |
|---|-------|--------|
| [0000](0000-template.md) | Template | — |
| [0001](0001-fleet-vs-laptop-scope.md) | Per-service atomic scoring with a separate aggregation layer | Accepted |
| [0002](0002-identity-resolution-strategy.md) | Layered identity resolution that assumes messy tagging | Accepted |
| [0003](0003-deterministic-inference-boundary.md) | Deterministic/inference boundary: CLI computes, skill judges | Accepted |
| [0004](0004-provider-adapter-interface.md) | Per-data-class provider interfaces with canonical models | Accepted |
| [0005](0005-implementation-language-go.md) | Implementation language: Go | Accepted |
| [0006](0006-shared-monitor-attribution.md) | Shared monitors: per-event attribution, config membership by reference | Accepted |
