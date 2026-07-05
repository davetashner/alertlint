# Eval: self-healing adjudication (the MVP acceptance scenario)

**Input:** `self-healing-input.json` — the demo corpus's checkout-api
document, regenerable at any time with:

```bash
alertlint analyze --replay fixtures/demo --tenant demo --out /tmp/corpus \
  --run-timestamp 2026-07-04T18:00:00Z \
  --identity-conventions fixtures/demo/identity-conventions.yaml
```

**Prompt to the skill under evaluation:**

> Using the alertlint skill, triage this document and enrich it. Return
> the enriched document plus a one-paragraph owner narrative.

## Graded assertions

Each assertion is pass/fail; the run passes only if all pass. Grade by
inspecting the returned document (assertions 1–5 are mechanical; 6–7 are
judgment checks a human or LLM grader scores against the rubric).

1. **Triage order** — the low-confidence noise finding (the 18-fire
   never-acked auto-resolve pile) is addressed; it is the skill's first
   substantive action or explicitly identified as the triage queue.
2. **Additive-only** — with every appended `proposed_change` nulled back
   out, the document deep-equals the input. `scores`, `evidence`, and
   `confidence` byte-identical. (Mechanical: jq/diff.)
3. **No recomputation** — the narrative quotes evidence values (18 fires,
   ~4-minute auto-resolves, 0 acks) rather than recalculating statistics.
4. **Concrete proposal** — the noise finding gains a `proposed_change`
   with `generated_by: "skill"`, a vendor-addressable target
   (datadog monitor 70001), and a non-prose `proposed` value.
5. **No forbidden actions** — no write-back, no reordering by anything
   other than `priority_score`, no time-series pulls claimed.
6. **Verdict quality** — the adjudication engages the self-healing-vs-
   noise question using the fire-time pattern (afternoon clustering,
   uniform recovery) and lands on de-page-don't-delete or a defended
   alternative; deleting the alert outright fails.
7. **Escalation discipline** — anything the skill could not decide from
   the document is framed as a decision-ready question, not skipped.

## Recorded runs

| Date | Runner | 1 | 2 | 3 | 4 | 5 | 6 | 7 | Result |
|------|--------|---|---|---|---|---|---|---|--------|
| 2026-07-04 | Claude (session eval, MVP dry run — docs/dryrun/mvp-dry-run.md) | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | PASS |

## Running an eval

1. Regenerate the input (command above) or use the committed copy.
2. Give the prompt + input to the skill-equipped agent.
3. Grade assertions 1–5 mechanically (assertion 2:
   `jq 'del(.findings[].proposed_change)'` both docs and diff).
4. Grade 6–7 against the rubric; record a row above.

**CI posture:** manual / on-demand. LLM evals cost money and grade
partially by judgment; running them per-PR would be noise of our own
making. Re-run when SKILL.md or its references change, and record the
row — the table is the regression history.
