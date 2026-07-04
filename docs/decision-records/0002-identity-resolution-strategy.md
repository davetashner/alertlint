# 0002 — Layered identity resolution that assumes messy tagging

- **Status:** Accepted
- **Date:** 2026-07-04
- **Deciders:** dave
- **Resolves:** D-2 in [requirements 0001 §14](../requirements/0001-initial-requirements.md)
- **Requirements:** REQ-ID-001, REQ-ID-002, REQ-ID-003, REQ-ID-004

## Context

Every artifact from every source must resolve to a ServiceNow CMDB CI (REQ-ID-001), but config, firing history, and action-taken live in systems that do not share identifiers. The open question was empirical: how much CMDB tagging discipline actually exists across Datadog, PagerDuty, etc.? If tagging were clean, v1 could assume direct CI references; if not, v1 must bootstrap the mapping itself. We cannot verify fleet-wide tagging discipline before building, and any single team's discipline doesn't generalize.

## Options considered

1. **Assume clean CI references** — resolve only exact CI tags; everything else errors out. Pros: trivial resolver. Cons: in any realistically messy estate, most artifacts fail to resolve and the tool is useless precisely where alerting hygiene is worst — the services it exists to find.
2. **Full fuzzy bootstrap** — aggressive name-similarity matching to force every artifact onto some CI. Pros: high mapping coverage. Cons: silent wrong joins corrupt scores undetectably; a service scored on another service's alerts is worse than no score.
3. **Layered resolver with confidence, unmappables as findings** — try resolution strategies in decreasing-confidence order; every mapping records its method and confidence; what can't be mapped becomes a `type: identity` finding (REQ-ID-003) rather than a silent drop or a forced guess.

## Decision

Option 3. The resolver is the first pipeline stage (REQ-ID-002) and applies strategies in order:

1. **Exact** — artifact carries an explicit CI identifier (tag, custom field, integration link). Confidence: high.
2. **Convention** — deterministic org-specific rules (e.g., `service:<name>` tag → CI name, PagerDuty service name → CI name), maintained as versioned config. Confidence: medium.
3. **Fuzzy** — normalized name-similarity match, **suggestion-only**: emitted as a candidate inside the identity finding for agent/human confirmation, never used to join data for scoring.

Design for messy reality: v1 assumes tagging discipline is poor until proven otherwise. Per-service mapping coverage is always reported, and scores computed from partial mappings say so.

## Consequences

- The tool degrades gracefully instead of failing where hygiene is worst; poor mapping coverage is itself a legible, actionable output.
- Wrong-join risk is confined: only exact and convention matches feed scoring; fuzzy candidates require confirmation (consistent with the human-in-the-loop path in REQ-COV-003).
- The convention rule set is a first-class, versioned repo asset with test fixtures — adding an org convention is config, not code.
- Confirmed fuzzy matches should be persistable back into the convention layer (a mapping file), so resolution coverage ratchets upward run over run.
- The resolver needs its own spec and is on the critical path: nothing downstream can be integration-tested end-to-end without it.
