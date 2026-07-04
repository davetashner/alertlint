# 0004 — Per-data-class provider interfaces with canonical models

- **Status:** Accepted
- **Date:** 2026-07-04
- **Deciders:** dave
- **Resolves:** D-4 in [requirements 0001 §14](../requirements/0001-initial-requirements.md)
- **Requirements:** REQ-SRC-001..007, REQ-OUT-002, REQ-SCORE-007

## Context

The scoring core must stay vendor-agnostic (REQ-SRC-007): adding a vendor means adding an adapter, not touching the engine. There are three distinct data classes — alert **config**, firing **history**, and **action** taken — and vendors cover them unevenly (Datadog has config + some history; ServiceNow has history + action; CloudWatch has config only). This decision blocks all parser work.

## Options considered

1. **One monolithic adapter interface per vendor** — each vendor implements a single `Provider` with all methods. Cons: forces every vendor to stub data classes it doesn't have; the interface accretes optional methods and capability flags.
2. **Three narrow interfaces, implemented selectively** — `ConfigProvider`, `HistoryProvider`, `ActionProvider`; a vendor module implements whichever apply. The core discovers capabilities by which interfaces a registered adapter satisfies.
3. **ETL to canonical files, core reads only files** — adapters are separate fetch programs writing normalized files; the core never talks to a vendor. Pros: strongest decoupling. Cons: heavier v1 workflow for laptop use (two-step invocation).

## Decision

Option 2 for the interface shape, borrowing the persistence idea from option 3 as a cache rather than a required pipeline stage.

- **Three narrow interfaces.** Each takes a scope (org/tenant + time window from REQ-HIST-001) and returns streams of **canonical records**:
  - `ConfigProvider` → `AlertConfig` — condition, threshold, duration, severity, routing, monitor status (enabled/silenced).
  - `HistoryProvider` → `AlertEvent` — fired/resolved timestamps, auto-resolve flag, source alert reference.
  - `ActionProvider` → `ResponseRecord` — ack/close timestamps, disposition code (mapped to the fixed taxonomy), assignee/reassignment count, linked change/incident references.
- **Canonical models are the contract.** Adapters own all vendor-specific normalization (field mapping, disposition-code translation, pagination, rate limits). Every canonical record carries `source`, a raw-artifact reference for evidence trails, and **pre-resolution native identity hints** (tags, service names) — identity resolution (ADR [0002](0002-identity-resolution-strategy.md)) happens in the core *after* adaptation, so adapters know nothing about the CMDB.
- **Snapshot cache.** Raw pulls are cached to disk keyed by (source, scope, window), making runs reproducible (REQ-SCORE-007), diffable, and re-scorable offline while iterating on scoring logic without re-pulling APIs.
- Canonical model schemas are **versioned alongside the output contract**; an adapter declares which schema version it emits.

## Consequences

- Parser work is unblocked: the four tier-1 config/history/action adapters (Datadog, New Relic, CloudWatch, Splunk, ServiceNow, PagerDuty) can be built and tested independently against canonical-model fixtures.
- Vendors with partial coverage are natural, not special cases; per-service source coverage is derivable from which providers contributed records.
- The disposition taxonomy (REQ-NOISE-001) must be finalized in the canonical `ResponseRecord` schema early — every ActionProvider maps into it, so late changes fan out across adapters.
- The snapshot cache doubles as the test-fixture format, and later central deployment can populate the same cache shape from scheduled pulls (consistent with ADR [0001](0001-fleet-vs-laptop-scope.md)).
