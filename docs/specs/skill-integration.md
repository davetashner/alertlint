# Spec: Skill Integration

- **Status:** Draft
- **Date:** 2026-07-04
- **Beads issue:** alertlint-md1
- **Requirements:** REQ-ARCH-001, REQ-ARCH-002, REQ-ARCH-003, REQ-ARCH-004, REQ-REC-001, REQ-REC-002, REQ-REC-003 (see [docs/requirements](../requirements/))
- **Decision records:** [ADR 0003 — Deterministic/inference boundary](../decision-records/0003-deterministic-inference-boundary.md)

## Problem

The CLI emits per-service JSON that is deliberately optimized for machine consumption (REQ-ARCH-001, REQ-NG-003): typed findings, confidence values, evidence blocks, scores. On its own that output is a pile of flags, not a plan. Somebody still has to decide which low-confidence findings are real, whether an archetype actually applies to *this* service, what the concrete fix is, and how to explain the priorities to the team that owns the service.

That somebody is the Claude skill — the **primary consumer** of the CLI's output and the second half of the architecture split in [ADR 0003](../decision-records/0003-deterministic-inference-boundary.md): the CLI computes, the skill judges. Without a specified skill workflow, the boundary decays in practice: either the agent starts second-guessing the CLI's arithmetic (irreproducible scores), or it passes raw findings through unjudged (mechanical, context-blind recommendations). Service owners hit this every time they receive output: they are secondary consumers who read the agent's narrative, never the raw stdout (REQ-NG-003).

## Goals

- Define the skill's end-to-end workflow over per-service JSON: acquire → triage low-confidence findings → enrich coverage applicability → escalate ambiguity → generate level-B `proposed_change` content → produce the prioritized owner-facing narrative.
- Make the low-confidence seam operational: specify how the skill adjudicates the self-healing-vs-noise distinction (REQ-NOISE-003) and every other low-confidence finding class, using service context the CLI cannot have.
- Specify how the skill performs path C of REQ-COV-003 (archetype enrichment from the service's repo / OpenAPI spec) and path D (human-in-the-loop confirmation for ambiguous cases).
- Specify level-B recommendation generation (REQ-REC-002): concrete config diffs, computed values, and dedup/grouping/inhibition suggestions attached to findings' `proposed_change` blocks.
- Enforce ADR 0003's hard rules in the skill's own instructions: the skill **never recomputes statistics and never adjusts scores** — it reasons over emitted evidence.
- Define the skill packaging shape: this repo is the skill's home; the skill ships as a `SKILL.md` with the alertlint CLI as its tool.

## Non-goals

- **Level C (auto-PR) is out of scope for v1** (REQ-REC-003, REQ-NG-001). The `proposed_change` block is the designed seam for later PR generation, but in v1 the skill must not open PRs, push branches, or write to alert-config repositories in any form. See "What the skill must NOT do" below.
- No metric-derived threshold math (REQ-NG-002). The skill may *note* that a service would benefit from the later-phase metric deep pass; it cannot pull time-series or compute percentile thresholds in v1.
- No human-operated CLI UX (REQ-NG-003). This spec does not add human-facing flags, TUI, or report formatting to the CLI itself; human readability lives in the skill's narrative output.
- Not a redefinition of the JSON contract or the scoring math — those are owned by [output-contract.md](output-contract.md) and [scoring-engine.md](scoring-engine.md) respectively. This spec consumes them.

## Design

### Position in the architecture

Per REQ-ARCH-002/003/004 and ADR 0003, the division of labor is:

- **CLI (deterministic):** source pulls, identity resolution, firing/disposition statistics, archetype inference from telemetry (path A), score computation, level-A finding emission with `confidence` + `evidence` on every determination.
- **Skill (judgment):** everything requiring service-specific context or generation — low-confidence adjudication, archetype enrichment (path C), escalation (path D), level-B `proposed_change` content, and the prioritization narrative.

The handoff artifact is the per-service JSON defined in [output-contract.md](output-contract.md). The `confidence` and `evidence` fields are what let the skill judge *without* recomputing — the skill treats every number in the document as ground truth.

### Skill workflow

The skill executes six stages per run. Stages 2–5 operate per service; stage 6 operates over the corpus.

**Stage 1 — Acquire input.** The skill either (a) invokes the alertlint CLI directly as its tool, with the caller's credentials per REQ-EXEC-001, or (b) consumes an existing corpus of per-service JSON documents the caller already generated. Both paths yield the same input; the skill validates each document's `metadata` block (tool version, archetype-library version, weight config) and refuses to mix documents from incompatible contract versions in one analysis.

**Stage 2 — Triage low-confidence findings first.** This is the designed seam (ADR 0003: "the skill's first job on any service is triaging them"). For each finding with low confidence, the skill adjudicates using service context that the CLI structurally lacks:

- *Noise vs. self-healing* (REQ-NOISE-003): the CLI's ambiguity default scores never-acked + fast-auto-resolve alerts as noise, low-confidence. The skill examines service context — runbooks referenced by the alert, auto-remediation configuration, deployment/scaling automation visible in the repo, the alert's stated intent — and adjudicates: **uphold** (genuinely noise), **reinterpret** (self-healing by design; the alert may still be miswired as a page instead of a ticket), or **insufficient context** (proceed to stage 4).
- *Identity candidates*: fuzzy CI-mapping candidates emitted by the CLI (per ADR 0003's identity row) are confirmed or rejected from repo/ownership context.
- *Threshold heuristics*: low-confidence behavior-inferred threshold findings are checked against what the service actually does (e.g., a batch job that legitimately spikes at 02:00).

Adjudication outcomes are recorded as skill annotations layered on the finding — **never** as edits to the finding's `confidence`, `severity`, `evidence`, or any score. A reinterpreted noise finding still contributes to the noise sub-score exactly as the CLI computed it; the skill's narrative explains the reinterpretation and redirects the recommendation (e.g., "convert to non-paging ticket" instead of "delete the alert").

**Stage 3 — Enrich archetype applicability (path C of REQ-COV-003).** For `coverage` findings and for archetypes the CLI's telemetry-based inference (path A) marked uncertain, the skill reads the service's repository and API surface: OpenAPI/Swagger specs (REST API archetype and which endpoints matter), connection/socket configuration (socket archetype), and business-transaction definitions (transaction archetype), per the library in [archetype-library.md](archetype-library.md). Outcomes: confirm an archetype applies (strengthening a missing-signal finding's narrative), or conclude it does not apply (the finding is contextualized as inapplicable in the narrative — the coverage score itself is not modified).

**Stage 4 — Escalate ambiguous cases to human confirmation (path D of REQ-COV-003).** When stages 2–3 end in "insufficient context," the skill produces a compact, decision-ready question for a human: the finding, the CLI's evidence verbatim, the specific ambiguity, and the concrete options with consequences (e.g., "Is `payment-reconciler` a business-transaction service? If yes, it is missing reconciliation-lag alerting; if no, I will mark the archetype inapplicable."). Escalations are batched at the end of the narrative, never scattered mid-analysis. Unresolved escalations are carried in the output as explicitly *pending* — never silently defaulted.

**Stage 5 — Generate level-B `proposed_change` content (REQ-REC-002).** For findings the skill upholds and can fix concretely, it fills or extends the finding's `proposed_change` block (shape owned by [output-contract.md](output-contract.md)) with:

- **Concrete config diffs** in the vendor's native format (e.g., a Datadog monitor JSON fragment changing `evaluation_window` from 5m to 15m), derived only from the CLI's emitted evidence — e.g., "fired 214 times, 96% auto-resolved within 4 minutes" justifies lengthening the duration to exceed the observed self-resolution time already present in the evidence's timing stats.
- **Computed values** where the evidence contains them (durations, ratios, `for:` windows). The skill selects and transcribes values *from evidence*; it does not derive new statistics from raw data (that would violate the recompute rule — and metric-derived values are a later phase per REQ-THRESH-002).
- **Dedup / grouping / inhibition suggestions** where the evidence shows correlated firing (e.g., N per-host alerts that always fire together → one grouped monitor; a downstream alert that always accompanies an upstream one → inhibition rule).

Every `proposed_change` cites the finding's evidence it is based on, so a human (or a future level-C pipeline) can audit the chain from statistic → judgment → diff. This block is the level-C seam: it must remain mechanically applyable in principle, even though nothing applies it in v1.

**Stage 6 — Produce the prioritized narrative.** The skill assembles the owner-facing deliverable (REQ-NG-003): services ordered by the CLI's criticality-weighted priority score (the skill never reorders by its own scoring — it may annotate rank context, e.g., "priority driven almost entirely by the noise sub-score"), and within each service, findings ordered by expected leverage: upheld high-severity findings with ready `proposed_change` blocks first, then flags needing owner judgment, then pending escalations, then contextualized/reinterpreted findings with the reasoning shown. Dormant/insufficient-data states (REQ-HIST-002/003) are surfaced in their own section, never conflated with healthy.

### Hard rules (from ADR 0003) — what the skill must NOT do in v1

These are encoded as explicit constraints in the skill's instructions, and violation is a defect:

1. **Never recompute statistics.** No re-counting fires, re-deriving ratios, re-bucketing timings — even "to double-check." If evidence looks wrong, that is a finding about the CLI, reported as such.
2. **Never adjust scores.** Sub-scores, composite, priority score, and their ordering are the CLI's verbatim. The skill contextualizes in narrative only.
3. **Never mutate CLI-emitted fields.** `type`, `severity`, `confidence`, `evidence`, `rationale` are read-only; skill judgment is additive annotation.
4. **Never write back** (REQ-NG-001, REQ-REC-003): no PRs, no branches, no commits to alert-config repos, no API calls that mutate monitors/alerts in any source system. `proposed_change` is content, not action.
5. **Never pull raw time-series** (REQ-NG-002): the metric deep pass is a later phase; in v1 the skill can only recommend that it be run once it exists.
6. Reverse migration is a design smell: if the skill finds itself repeatedly making the same mechanical judgment, the correct move is a versioned rule migration into the CLI (ADR 0003 consequence), not a smarter prompt.

### Skill packaging

This repository is the skill's home; the skill and CLI version together, which keeps the JSON contract and the skill's expectations in lockstep (the `metadata.tool_version` check in stage 1 makes drift detectable).

Shape:

- A **`SKILL.md`** defining the workflow above as agent instructions — the triage-first ordering, the hard rules, the narrative format, and escalation etiquette.
- The **alertlint CLI as the skill's tool**: `SKILL.md` documents the invocation(s) the skill may run (analysis over a service set, or reading an existing output directory) and the output contract version it expects.
- Supporting reference material as needed (e.g., a pointer to [archetype-library.md](archetype-library.md) semantics for path-C enrichment and to the `proposed_change` schema in [output-contract.md](output-contract.md)).

The skill is authored and iterated with Anthropic's [skill-creator](https://github.com/anthropics/skills/blob/main/skills/skill-creator/SKILL.md), following its conventions: a `skills/alertlint/` directory containing `SKILL.md` (YAML frontmatter with `name` and a trigger-oriented `description`, body under 500 lines) plus bundled resources split for progressive disclosure — `references/` for the path-C enrichment guidance and `proposed_change` authoring rules, `scripts/` if helper tooling emerges. skill-creator's test-and-evaluate loop provides the evaluation harness for the acceptance criteria below, and its `package_skill.py` bundles the distributable `.skill` artifact.

### Example: low-confidence noise finding, end to end

CLI emits (abridged; full schema in [output-contract.md](output-contract.md)):

```json
{
  "type": "noise",
  "severity": "medium",
  "confidence": "low",
  "rationale": "Fired 214 times in window; never acknowledged; auto-resolved (median 3.6m); no Closed-No Action code present. Scored as noise per ambiguity default (REQ-NOISE-003).",
  "evidence": {
    "alert": "checkout-api / high-cpu-warning",
    "fires": 214,
    "acked": 0,
    "auto_resolved_ratio": 0.96,
    "median_time_to_resolve_s": 216,
    "linked_change_or_incident": 0
  },
  "proposed_change": null
}
```

Skill (stage 2): reads the checkout-api repo, finds an HPA scaling policy targeting the same CPU signal with a ~3-minute stabilization window — the "alert" is watching a condition the autoscaler resolves by design. Adjudication: **reinterpret — self-healing**, with the caveat that a paging alert on an auto-remediated condition is still misconfigured. Stage 5 output:

```json
{
  "adjudication": "self-healing",
  "reasoning": "HPA on cpu>70% with 180s stabilization resolves the condition the alert pages on; median self-resolution (216s) matches the HPA window in evidence.",
  "proposed_change": {
    "kind": "config_diff",
    "target": "datadog monitor high-cpu-warning",
    "diff": "priority: P2 -> P5; notify: @pagerduty-checkout -> @slack-checkout-fyi; evaluation window: 5m -> 15m (exceeds observed 3.6m median self-resolution)",
    "based_on_evidence": ["auto_resolved_ratio", "median_time_to_resolve_s"]
  }
}
```

Stage 6 narrative entry: explains that this alert is not deletable noise but a paging-policy defect, quantifies the interruption cost from the evidence (214 pages, zero actions), and presents the diff. The noise sub-score is untouched.

## Alternatives considered

- **Skill re-derives statistics for "verification."** Rejected by [ADR 0003](../decision-records/0003-deterministic-inference-boundary.md): destroys reproducibility, makes runs disagree on identical input, and duplicates the CLI. The skill trusts evidence; suspected evidence bugs are reported, not corrected inline.
- **CLI generates `proposed_change` from templates, skill only narrates.** Rejected in ADR 0003's options: reproducible but context-blind exactly where judgment adds value (a template cannot know an autoscaler makes an alert self-healing).
- **Skill lives in a separate repo from the CLI.** Rejected: the skill's correctness depends tightly on the output contract version and archetype-library semantics; co-versioning in one repo makes the compatibility check in stage 1 trivial and keeps spec/ADR traceability in one place.
- **Skill reorders the worklist using its own judgment of importance.** Rejected: REQ-EXEC-003 fixes the priority formula regardless of corpus, and ADR 0003 forbids score adjustment. The skill annotates ranking context instead.

## Testing & acceptance

**Strategy.** The skill is tested against **fixture corpora**: hand-built per-service JSON documents (valid against [output-contract.md](output-contract.md)) plus synthetic service-context fixtures (a fake repo with an OpenAPI spec, an HPA config, runbooks). Because the CLI side is deterministic and LLM-free (ADR 0003), fixtures are stable; skill-side evaluation uses transcript assertions (did the skill do X / refrain from Y) rather than exact-output matching.

**Acceptance criteria:**

1. **Low-confidence noise finding, end to end (the gating scenario).** Given a fixture service whose JSON contains a low-confidence noise finding (never-acked, fast auto-resolve, per REQ-NOISE-003) and a context fixture containing auto-remediation config:
   - the skill triages this finding before acting on any high-confidence finding for the service;
   - it adjudicates *self-healing* citing the context artifact and the finding's own evidence fields — without issuing any data-source query or recomputing any statistic;
   - it emits a level-B `proposed_change` (paging-policy/duration change) whose values are traceable to fields present in `evidence`;
   - the scores block and all CLI-emitted finding fields in its output are byte-identical to the input;
   - the narrative presents the service at the CLI-given priority rank and explains the reinterpretation.
   Variant: same finding with a context fixture containing *no* remediation signal → the skill upholds noise and proposes deletion/downgrade. Variant: ambiguous context → the skill emits a batched path-D escalation with concrete options, marked pending, and does not guess.
2. **Coverage enrichment (path C).** Given a coverage finding for a missing latency alert and a fixture repo with an OpenAPI spec, the skill confirms the REST API archetype applies and strengthens the recommendation; given a repo showing the service is a queue consumer with no HTTP surface, it marks the archetype inapplicable in narrative while leaving the coverage score untouched.
3. **Hard-rule negative tests.** Across all fixtures: no skill output contains a modified score, sub-score, confidence, severity, or evidence value (checked by diffing every CLI-emitted field); no tool invocation in the transcript writes to any config source or pulls time-series.
4. **Input validation.** Given a corpus mixing two incompatible contract versions in `metadata`, the skill refuses the mixed analysis and says why.
5. **Level-B completeness (REQ-REC-001/002).** Every upheld finding in the narrative is at least level A (flag with rationale); every upheld finding for which the evidence contains sufficient concrete values carries a level-B `proposed_change`; a dedup fixture (N correlated per-host findings) yields a grouping/inhibition suggestion.
6. **Secondary-consumer readability (REQ-NG-003).** The narrative is reviewed against a checklist: ordered by priority score, leverage-ordered within service, escalations batched, dormant/insufficient-data segregated, no raw JSON dumps.

## Open questions

- ~~Skill file layout~~ — resolved: authored with Anthropic's skill-creator under `skills/alertlint/` (SKILL.md + `references/` for on-demand guidance); see Skill packaging above.
- **CLI invocation vs. pre-generated corpus.** Should v1 `SKILL.md` allow the skill to invoke the CLI itself (credentials, runtime, and permission-prompt implications on the caller's laptop, REQ-EXEC-001), or only consume an existing output directory in its first iteration? Plan: decide during first end-to-end dry run.
- **Vendor-native diff format for `proposed_change`.** Is `kind: config_diff` free-form text in v1, or per-vendor structured formats (Datadog monitor JSON, Terraform, CloudWatch alarm JSON) aligned with the adapter interface (ADR 0004)? Owned jointly with [output-contract.md](output-contract.md); structured formats matter for the level-C seam.
- **Persistence of path-D answers and adjudications.** Where do human escalation answers and repeated skill adjudications live so the next run doesn't re-ask/re-derive them — and so recurring mechanical adjudications can be detected and migrated skill → CLI per ADR 0003's consequence? Candidate: a per-service annotations file consumed as skill context. Owner: dave; needs a small design note or ADR.
- **Narrative delivery format.** Markdown file per run vs. per service, and where it lands (stdout, `out/` directory, issue tracker). Blocks nothing; resolve with the first real corpus.
- **Skill-side evaluation harness.** Plan of record is skill-creator's test-and-evaluate loop; remaining specifics are fixture location and whether evals run in CI given LLM cost. Owner: dave; resolve before declaring criterion 1 automated rather than manual.
