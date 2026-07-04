# Triage: adjudicating low-confidence findings

## Self-healing vs noise (the N-T1 ambiguity default)

The CLI scored the alert as noise at low confidence because it fired,
was never acknowledged, auto-resolved fast, and carried no disposition
code (REQ-NOISE-003). Decide which story is true:

**Probable self-healing** — look for a mechanism that explains recovery:
- Autoscaling (HPA/ASG) events correlated with the alert's metric
  (CPU, memory, queue depth on a scalable pool)
- Retry/backoff behavior upstream of the measured symptom
- Scheduled load (batch windows) matching the fire timestamps in
  `evidence` day/hour patterns

Verdict: annotate as probable self-healing. Proposed change: usually
`routing_change` (stop paging, keep recording) or `duration_update`
(outlast the self-healing window). Do NOT propose deletion — the signal
has diagnostic value.

**Confirmed noise** — no recovery mechanism exists; the threshold simply
sits inside normal variance:
- Fire times uncorrelated with load or scaling
- Threshold within the metric's steady-state range (owner knowledge or
  repo dashboards; you may not pull time series yourself)

Verdict: keep the noise classification. Proposed change:
`threshold_update` or `duration_update` with a concrete value derived
from the evidence (e.g., "fires ~daily at 250ms with 87% no-action;
propose 400ms" only when a factual basis for 400 exists in the document
or repo — otherwise escalate to the owner with the question framed).

## Partial-mapping coverage findings

`coverage_note: "partial"` means unmapped artifacts exist. Before
proposing a missing alert, check whether an unmapped artifact (see
`_unresolved.json` and identity findings) plausibly IS the missing
signal. If so, the right fix is an identity confirmation, not a new
alert.

## What "escalate" means

One batched message per service owner, each item: context line, the two
candidate answers, your lean and why. Never a vague "please review".
