# 0005 — Implementation language: Go

- **Status:** Accepted
- **Date:** 2026-07-04
- **Deciders:** dave
- **Requirements:** REQ-ARCH-002, REQ-EXEC-001, REQ-SCORE-007
- **Resolves:** the implementation-language open question in [provider-adapters.md](../specs/provider-adapters.md)

## Context

Every spec deferred the implementation language, and it blocks concrete work: adapter interface idiom (iterator vs. async generator vs. channel), plugin registration, dependency linting, and the distribution story for a CLI that runs on engineers' laptops with their own credentials (REQ-EXEC-001). The CLI is the deterministic half of the system (REQ-ARCH-002): API pulls against six vendor systems, joins, statistics, and rule evaluation — no LLM calls, reproducible output (REQ-SCORE-007).

## Decision

**alertlint is written primarily in Go.** "Primarily" means the CLI, adapters, resolver, scoring engine, and aggregator are Go; non-code assets stay in their natural formats (archetype library and convention rules in YAML, the skill in Markdown per skill-creator conventions), and incidental tooling may use whatever fits.

Mapping onto the specs:

- The three provider interfaces become Go interfaces; capability discovery (which interfaces a vendor module satisfies) is idiomatic type assertion. Streaming idiom: iterator-style (`iter.Seq` / paged pulls), not channels, for deterministic ordering.
- Archetype `metric_pattern` rules were already specified as RE2 — which is exactly Go's `regexp` engine, so the spec's pattern semantics are native.
- Single static binary distribution fits the laptop execution model: no runtime or dependency install for callers, trivial cross-compilation for macOS/Linux/Windows.
- Concurrency for multi-vendor pulls is a strength, but concurrency must stop at the snapshot cache boundary: everything downstream of adapters is sequential/deterministic.

## Consequences

- **Determinism footgun to engineer around:** Go randomizes map iteration order by design. The scoring spec requires canonical iteration order for byte-identical output, so all map traversals feeding output or scores MUST sort keys explicitly; the golden-fixture regression suite is the enforcement backstop.
- JSON marshaling of the output contract needs explicit field ordering discipline (struct-based marshaling, not maps) for byte-identical documents.
- Provider SDKs: official Go SDKs exist for Datadog, PagerDuty, and the AWS SDK covers CloudWatch; ServiceNow, New Relic, and Splunk adapters will likely wrap REST APIs directly — acceptable given adapters are thin normalizers by design (ADR 0004).
- Plugin model for tier-2 adapters is compile-time registration (imports), not dynamic loading — consistent with "adding a vendor = adding an adapter" meaning a code contribution, not a runtime plugin.
- The skill remains Markdown + the Go binary as its tool; no language coupling between the two halves (ADR 0003 boundary unaffected).
