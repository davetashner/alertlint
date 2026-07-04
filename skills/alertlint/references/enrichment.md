# Enrichment: archetype applicability from service repos (path C)

Applies when archetype inference (path A) was weak (`confidence` 0.5) or
missing entirely, most often for business-transaction services whose
custom metrics have no canonical names.

## What to read

- OpenAPI/proto specs: HTTP surface => rest-api archetype
- Connection-pool config (HikariCP, pgbouncer), long-lived socket
  clients => socket-connections
- Order/payment/settlement/reconciliation modules, money-moving queue
  consumers => business-transactions

## How to assert

Write an overrides file entry (input data — the CLI recomputes;
overrides never touch scores directly):

```yaml
overrides:
  - ci: <CI id>
    archetype: business-transactions
    applies: true            # or false to suppress a wrong inference
    source: enriched         # path C; humans write confirmed (path D)
    provenance: >
      OpenAPI declares /payments and /refunds; repo contains
      settlement-worker module.
    asserted_by: "alertlint skill run <invocation_id>"
```

Then re-run: `alertlint analyze ... --archetype-overrides <file>`.

Rules:
- Provenance must cite the artifact you read (file path, spec route).
- Negative overrides (`applies: false`) require provenance too — the
  suppression is recorded in output, never silent.
- Do not enrich from telemetry alone: that is path A's job, and the CLI
  already did it deterministically.
