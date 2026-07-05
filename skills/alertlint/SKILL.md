---
name: alertlint
description: Analyze a service fleet's alerting quality from alertlint CLI output — triage low-confidence findings, adjudicate self-healing vs noise, enrich archetype coverage from service repos, and propose concrete alert-config changes. Use when the user asks to analyze alert noise, review alerting quality, interpret alertlint output, or turn an alertlint worklist into fixes.
compatibility: Requires the alertlint CLI on PATH (or a pre-generated output directory of per-service JSON documents).
---

# alertlint — alerting-quality analysis skill

You are the primary consumer of the alertlint CLI's per-service JSON
documents (docs/specs/output-contract.md). The CLI did the deterministic
work: statistics, classifications, scores, and level-A findings, each with
evidence and a confidence band. Your job is judgment: triage what the CLI
could not decide, enrich what it could not see, and turn findings into
concrete proposed changes.

## Hard rules (ADR 0003 — violating any of these corrupts the output)

1. **Never recompute statistics.** Reason over `evidence` blocks as given.
2. **Never adjust scores** or any CLI-emitted value. Enrichment is
   strictly additive: fill optional blocks (`proposed_change`), never
   mutate `scores`, `evidence`, or `confidence`.
3. **Never rank by anything except `scores.priority_score`.** Do not
   reorder the worklist by your own judgment; contextualize in prose.
4. **No write-back.** v1 proposes changes; it never applies them to
   vendor systems, opens PRs, or mutates alert configs (level C is out of
   scope; `proposed_change` is its seam).
5. **No time-series pulls.** Metric-derived threshold math is a later
   phase (REQ-THRESH-002); work only with what the documents carry.
6. **Repeated mechanical judgments are migration candidates.** If you
   find yourself applying the same rule three times, note it in your
   report as a candidate for deterministic CLI logic — do not silently
   keep judging.

## Workflow

### 1. Acquire input

Run `alertlint analyze --tenant <t> --out <dir>` (credentials via env:
`DD_API_KEY`/`DD_APP_KEY`, `PAGERDUTY_TOKEN`, `SERVICENOW_URL`/`_USER`/
`_PASSWORD`), or consume an existing output directory. Check
`contract_version` starts with `1.` before reading anything else. Then
`alertlint worklist <dir> --format json` for the ranked order.

### 2. Triage low-confidence findings first

`jq '.findings[] | select(.confidence=="low")' <doc>` is your queue —
these are the cases the CLI deliberately deferred to you:

- **Noise findings from the ambiguity default** (never acked + fast
  auto-resolve + no disposition code): decide *self-healing vs noise*
  using service context. A CPU alert that auto-resolves on autoscaling
  events is the canonical self-healing false positive — read
  `references/triage.md` for the adjudication checklist.
- **Coverage findings capped by partial mapping**: unseen telemetry may
  exist behind unmapped artifacts; confirm the gap is real before
  proposing an alert.
- **Identity candidates**: fuzzy matches awaiting confirmation. Verify,
  then record via `alertlint identity confirm` (never edit scoring joins
  by hand).

### 3. Enrich archetype applicability (path C)

For services whose archetype inference was weak or absent, read the
service's repo (OpenAPI spec, module names, queue/settlement code) and
write `enriched` entries to an archetype-overrides file, then re-run the
CLI — overrides are input data; the CLI recomputes (see
`references/enrichment.md`). Never assert an archetype from telemetry
alone that the CLI already declined.

### 4. Escalate ambiguity (path D)

Cases you cannot decide from repo context become batched, decision-ready
questions for a human: one line of context, the two candidate answers,
your lean. Record answers as `confirmed` overrides or confirmed mappings.

### 5. Propose level-B changes

For findings that survive triage, fill `proposed_change`: concrete values
computed from evidence (never prose), vendor-addressable targets,
`generated_by: "skill"`. Formats and per-kind rules are in
`references/proposed-changes.md`.

### 6. Report

Produce the owner-facing narrative ordered by `priority_score`
(descending, exactly as the worklist ranks): per service, what the score
means, the top findings with rationale quoted, proposed changes, and what
was escalated. Humans read your output, not the raw JSON (REQ-NG-003).

## Acceptance scenario (the MVP gate)

Given a document containing a low-confidence noise finding for a
never-acked, fast-auto-resolving alert: adjudicate it using service
context (e.g., an HPA that explains the pattern), either annotate it as
probable self-healing with a `proposed_change` to depage it, or confirm
it as noise with a threshold/duration proposal — and leave every
CLI-emitted field byte-identical.
