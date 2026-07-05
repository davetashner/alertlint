# Proposed changes: level-B authoring rules

Every `proposed_change` block you add must be concrete and
vendor-addressable (REQ-REC-002). Never prose-only.

```json
{
  "kind": "threshold_update",
  "target": { "source": "datadog", "native_id": "monitor:4821337",
              "path": "options.thresholds.critical" },
  "current": 250,
  "proposed": 400,
  "rationale": "one or two sentences citing the evidence fields used",
  "generated_by": "skill",
  "diff": null
}
```

Per-kind rules:

- `threshold_update` / `duration_update`: `proposed` is a number with a
  factual basis in the document's evidence or the service repo; when no
  basis exists, escalate instead of guessing.
- `routing_change`: `proposed` is the concrete destination change
  (e.g., route to a ticket queue instead of paging). Use for probable
  self-healing alerts — de-page, don't delete.
- `grouping`: propose the vendor's dedup/grouping/inhibition construct
  for TH-3 bursty findings; name the grouping key.
- `add_alert`: for coverage findings; `current` is null; `proposed` is a
  minimal vendor config fragment for the missing signal, tagged with the
  service's identity convention so it resolves on the next run.
- `delete_alert`: only for TH-4 never-actioned alerts where the owner
  confirmed no diagnostic value; otherwise prefer routing_change.
- `mapping_add`: emitted via `alertlint identity confirm`, not authored
  by hand.

`diff` stays null in v1 (the level-C seam). Never modify CLI-emitted
fields; append your block and re-emit the document otherwise unchanged.
