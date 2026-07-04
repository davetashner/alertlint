# Requirements

The source of truth for **what** alertlint must do and **why**. Specs in [docs/specs](../specs/) describe *how* a requirement will be implemented; decision records in [docs/decision-records](../decision-records/) capture the significant choices made along the way.

## Conventions

- One requirements document per file, numbered: `NNNN-short-kebab-title.md` (e.g., `0001-initial-requirements.md`).
- Every individual requirement gets a **stable ID**: `REQ-NNN` (e.g., `REQ-001`). IDs are never reused or renumbered — if a requirement is dropped, mark it `Withdrawn` and leave it in place.
- Each requirement states one testable capability or constraint. If you can't imagine an acceptance test for it, it's context, not a requirement.
- Requirement status: `Proposed` → `Accepted` → (`Implemented` | `Withdrawn`).

## Traceability

The chain is: **requirement → spec → decision records → implementation.**

- Every spec lists the `REQ-NNN` IDs it addresses in its header (see the [spec template](../specs/TEMPLATE.md)).
- Every accepted requirement should eventually be covered by at least one spec; the table below tracks coverage.
- Decisions that shape how a requirement is met get an ADR, referenced from the spec.
- Commits and beads issues reference spec/requirement IDs where relevant.

## Documents

| Doc | Title | Status |
|-----|-------|--------|
| _0001 pending_ | Initial requirements | — |

## Coverage

| Requirement | Covered by spec(s) |
|-------------|--------------------|
| _populated once 0001 lands_ | |
