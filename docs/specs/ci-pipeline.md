# Spec: CI pipeline

- **Status:** Shipped
- **Date:** 2026-07-04
- **Beads issue:** alertlint-7d5
- **Requirements:** — (infrastructure; enforces the repo's traceability conventions now, and REQ-SCORE-007 reproducibility via golden-fixture tests once Go code lands)
- **Decision records:** [ADR 0005](../decision-records/0005-implementation-language-go.md) (Go toolchain)

## Problem

The repo is currently documentation-heavy (requirements, ADRs, specs with dense cross-links and REQ-ID traceability) and about to grow a Go implementation. Nothing today catches broken doc links, specs that drop their traceability headers, or references to requirement IDs that don't exist — and when Go code arrives there must already be a build/test/lint gate. Branch protection requires PRs but no status checks, so a red-anything can merge.

## Goals

- Every PR into `main` runs: internal doc-link validation, spec/ADR structure + REQ-ID traceability checks, and Go build/vet/lint/test.
- The pipeline is green on a docs-only repo **today** and picks up Go work **automatically** when `go.mod` appears — no workflow edit needed at that transition.
- Checks are deterministic: no network-dependent link checking (external URLs are skipped), pinned action majors, `-count=1` tests.
- The three job names become required status checks in the existing branch ruleset, so "behind main or red = not mergeable".
- Checks are runnable locally (`python3 scripts/check_traceability.py`; standard Go tooling).

## Non-goals

- No external-URL link checking (flaky: rate limits, transient outages). Offline/internal links only.
- No release/publish pipeline, no artifact builds, no coverage gates — later, once there is code worth releasing.
- No CD: this repo deploys nothing.
- No markdown style linting (prose style is not worth CI friction at this stage).

## Design

One workflow, `.github/workflows/ci.yml`, name `ci`, triggered on `pull_request` and on `push` to `main`, with per-ref concurrency cancellation and read-only `contents` permission. Three jobs:

### `docs-links`
`lycheeverse/lychee-action@v2` in `--offline` mode over `*.md` and `docs/**/*.md`: validates every relative file link (the specs' cross-reference web, ADR back-links, coverage-table links) and skips all remote URLs by design.

### `traceability`
Runs `scripts/check_traceability.py` (stdlib-only Python, no dependencies), which enforces:

1. Every spec in `docs/specs/` (excluding `README.md`/`TEMPLATE.md`) carries the `Status`, `Date`, and `Requirements` header fields from the template.
2. Every ADR in `docs/decision-records/` (excluding `README.md` and the `0000` template) carries a `Status` field and the `Context` / `Decision` / `Consequences` sections.
3. Every `REQ-<CATEGORY>-NNN` ID referenced anywhere in a spec exists in `docs/requirements/` — a spec cannot claim to implement a requirement that doesn't exist.
4. Every ADR file is listed in the decision-records README index.

Exit non-zero with one line per violation; violations are greppable by file path.

### `go`
Checkout, then a detect step: if `go.mod` is absent, the job logs "no Go module yet" and succeeds (the check still reports green, so it can be a required check before implementation starts). When `go.mod` exists, the same job runs, in order: `actions/setup-go@v5` pinned to `go-version-file: go.mod`, `gofmt -l` (fails on unformatted files), `go vet ./...`, `golangci-lint` (`golangci/golangci-lint-action@v6`), `go build ./...`, `go test -race -count=1 ./...`. Race detector is on from day one because the adapter layer is concurrent by design (ADR 0005); `-count=1` defeats test caching for honest signal.

### Required checks
After the workflow lands on `main`, the existing branch ruleset (`require-pr-to-main`) gains a `required_status_checks` rule listing `docs-links`, `traceability`, and `go`. Strict up-to-date-ness stays off for now (solo repo; rebase burden outweighs stale-merge risk at current velocity).

## Alternatives considered

- **Online link checking** — catches dead external references but is nondeterministic in CI; rejected per Goals. External links can be swept manually or on a schedule later.
- **Path-filtered jobs** (docs jobs only on `docs/**`, Go only on `**.go`) — rejected: path filters + required checks interact badly (skipped required checks block merges or need dummy-success shims). All three jobs are cheap enough to always run.
- **Traceability via grep in workflow YAML** — a script in `scripts/` is locally runnable, testable, and diffable; inline YAML bash is none of those.
- **Waiting for Go code before adding CI** — rejected: the workflow must be on `main` before required-check rules make sense, and doc integrity is worth gating now.

## Testing & acceptance

- The PR introducing this workflow runs it: all three jobs green on the docs-only tree is the primary acceptance test.
- Negative tests performed locally before merge: a spec stripped of its `Requirements` field and a fabricated requirement reference (`REQ-FAKE-` plus digits — spelled out here in pieces because the checker scans this spec too) both fail `check_traceability.py`; a broken relative link fails lychee offline.
- After merge: `gh api` confirms the ruleset lists the three contexts; a subsequent PR shows them as required.

## Open questions

- **golangci-lint config** (`.golangci.yml` ruleset selection) — decide when the first Go code lands; the action runs with defaults until then.
- **Scheduled external-link sweep** — worth a weekly `lychee` online run as a non-blocking workflow? Revisit if stale external links become a real problem.
- **Coverage reporting** — add when there is enough code for a threshold to mean something; candidate: `go test -cover` + a ratchet file, no third-party service.
