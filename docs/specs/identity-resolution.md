# Spec: Identity Resolution

- **Status:** Draft
- **Date:** 2026-07-04
- **Beads issue:** alertlint-md1
- **Requirements:** REQ-ID-001, REQ-ID-002, REQ-ID-003, REQ-ID-004 (see [docs/requirements](../requirements/))
- **Decision records:** [ADR 0002 — Layered identity resolution that assumes messy tagging](../decision-records/0002-identity-resolution-strategy.md)

## Problem

Alert config, firing history, and action-taken records live in systems that do
not share identifiers (Datadog tags, PagerDuty service names, ServiceNow
tickets). Scoring a *service* requires joining all three onto one canonical
identity: the ServiceNow CMDB Configuration Item (CI) (REQ-ID-001). Every run
of the tool hits this problem before any scoring can happen — the resolver is
the first pipeline stage (REQ-ID-002) and everything downstream
([scoring-engine.md](scoring-engine.md), [output-contract.md](output-contract.md))
is blocked without it.

Tagging discipline across the estate is assumed to be poor until proven
otherwise (ADR 0002). A resolver that only accepts clean CI references fails
exactly where alerting hygiene is worst — the services this tool exists to
find. A resolver that force-matches everything silently corrupts scores with
wrong joins. This spec defines the layered, confidence-annotated middle path.

## Goals

- Resolve every canonical record emitted by provider adapters
  ([provider-adapters.md](provider-adapters.md), ADR 0004) to a CMDB CI, or
  produce a `type: identity` finding explaining why it could not be resolved
  (REQ-ID-003).
- Apply strategies in decreasing-confidence order — exact CI reference,
  convention rules, fuzzy name-similarity — with every mapping recording its
  **method** and **confidence**.
- Keep fuzzy matches **suggestion-only**: emitted as candidates inside identity
  findings for agent/human confirmation, never used to join data for scoring.
- Report per-service **mapping coverage** so partially mapped services are
  legible, and propagate a partial-mapping signal into the scoring stage.
- Let confirmed fuzzy matches persist into a mapping file so coverage ratchets
  upward run over run.
- Look up the **criticality tier** from the resolved CI (REQ-ID-004), with a
  defined fallback for missing/unknown criticality (REQ-CRIT-003).

## Non-goals

- **No write-back to source systems.** The resolver never fixes tags in
  Datadog/PagerDuty/etc.; it only records mappings locally. (Consistent with
  REQ-NG-001.)
- **No automatic acceptance of fuzzy matches**, at any similarity score. 0.99
  is still a suggestion. Confirmation is an agent/human step (REQ-COV-003's
  human-in-the-loop pattern).
- **No CMDB curation.** Duplicate, retired, or mis-tiered CIs are surfaced as
  findings where they block resolution, but fixing the CMDB is out of scope.
- **No cross-CI artifact splitting in v1.** An artifact maps to at most one CI
  per run (see Open questions for shared monitors).

## Design

### Position in the pipeline

```
adapters (ADR 0004)          resolver (this spec)           scoring
─ canonical records with  →  ─ strategy chain           →   ─ joins only on
  native identity hints        exact > confirmed >            resolved mappings
  (tags, service names,        convention > fuzzy            ─ reads coverage +
  integration links)         ─ mapping table + coverage       criticality tier
                             ─ identity findings
```

Adapters know nothing about the CMDB; they attach **pre-resolution native
identity hints** to every canonical record (ADR 0004). The resolver consumes
those hints plus a **CI inventory snapshot** and produces:

1. a **mapping table** (artifact → CI, method, confidence),
2. **per-service coverage metrics**,
3. **identity findings** for everything unmappable or ambiguous,
4. the **criticality tier** per resolved CI.

### Inputs

**Canonical records.** Every `AlertConfig`, `AlertEvent`, and `ResponseRecord`
carries (per ADR 0004):

```yaml
source: datadog                  # provider that emitted the record
raw_ref: "monitor/4812007"       # evidence-trail reference into the source
identity_hints:                  # native, pre-resolution
  tags: ["service:payments-api", "env:prod", "team:core-payments"]
  service_name: null             # e.g. PagerDuty service display name
  ci_ref: null                   # explicit CI id if the source carries one
  integration_links: []          # cross-system links (e.g. PD ↔ ServiceNow)
```

**CI inventory snapshot.** A normalized extract of CMDB CIs, pulled through the
ServiceNow integration and stored in the snapshot cache (ADR 0004) so runs are
reproducible and offline-re-scorable:

```yaml
# ci-inventory snapshot record (pseudo-schema)
ci_id: "CI00012345"              # sys_id or CI number — canonical key
name: "payments-api"
aliases: ["payments API", "svc-payments"]   # if the CMDB carries them
criticality_tier: 1              # null when absent in the CMDB
status: operational              # operational | retired | ...
```

Whether this arrives via a fourth narrow provider interface or an extension of
the ServiceNow adapter is deferred to
[provider-adapters.md](provider-adapters.md) (see Open questions).

**Convention rules file** and **confirmed-mappings file** — both described
below.

### The strategy chain

Numeric confidences below are internal to the resolver and preserved in the mapping table and finding evidence; the per-service output contract surfaces them as bands per [output-contract.md](output-contract.md) — exact (1.0) and confirmed (0.95) map to `high`, convention (default 0.8) to `medium`.

Strategies run in strictly decreasing confidence order. The first strategy
that produces a scoring-eligible mapping wins; later strategies are skipped
for that artifact. Fuzzy runs only when nothing else matched, and its output
is never a mapping — only a finding.

| # | Strategy   | Basis                                         | Confidence | Feeds scoring joins? |
|---|------------|-----------------------------------------------|------------|----------------------|
| 1 | `exact`    | Explicit CI identifier on the artifact        | 1.0        | Yes |
| 2 | `confirmed`| Entry in the confirmed-mappings file          | 0.95       | Yes |
| 3 | `convention` | Versioned org-specific rule                 | per-rule, default 0.8 | Yes |
| 4 | `fuzzy`    | Normalized name similarity                    | = similarity score | **No — suggestion only** |

Confidence values for `exact`/`confirmed` are fixed by the tool; convention
rules may override their default per rule. These values ride along on every
mapping and into evidence blocks, satisfying REQ-NOISE-004's spirit for
identity: every determination carries confidence and the evidence that
produced it.

#### Strategy 1 — exact CI reference

The artifact carries an explicit CI identifier: a tag whose key is declared as
a CI-id tag in config (e.g. `ci_id:CI00012345`), a custom field, or an
integration link that names the CI directly. The value is validated against
the CI inventory:

- **Found, status operational** → mapping `{method: exact, confidence: 1.0}`.
- **Found, status retired** → identity finding `stale_ci_reference`; no mapping.
- **Not found in inventory** → identity finding `dangling_ci_reference`
  (the artifact claims a CI that does not exist); no mapping, and the artifact
  does **not** fall through to fuzzy — a wrong explicit reference is a data
  quality problem to surface, not to paper over.
- **Two conflicting explicit CI hints on one artifact** → identity finding
  `ambiguous_ci_reference` listing both; no mapping (see Open questions).

#### Strategy 2 — confirmed mappings (the ratchet)

Before convention rules run, the resolver consults the
**confirmed-mappings file**: pinned artifact→CI mappings produced by
confirming earlier fuzzy suggestions (or entered manually). This is how
resolution coverage ratchets upward run over run (ADR 0002 consequences).

**File format** — `identity-mappings.yaml`, versioned in the repo next to the
convention rules:

```yaml
version: 1
mappings:
  - artifact:                     # stable identity of the source artifact
      source: datadog
      kind: monitor
      key: "monitor/4812007"      # the adapter's stable raw_ref
    ci_id: "CI00012345"
    confirmed_by: "dave"          # human or agent identity
    confirmed_at: "2026-07-04"
    origin:
      method: fuzzy               # what suggested it
      score: 0.91
      hint: "service:payments_api_v2"
    note: "legacy tag predates payments-api rename"
```

**Lifecycle:**

1. **Creation.** A fuzzy candidate inside an identity finding is confirmed by
   the agent (with human sign-off per REQ-COV-003 D) or a human directly. The
   confirmation appends an entry. The CLI provides a
   `alertlint identity confirm <finding-ref> <ci_id>` subcommand so the write
   is structured, not hand-edited (hand edits remain legal — it is a plain
   YAML file with schema validation on load).
2. **Use.** On every run the file is loaded and applied as strategy 2. Entries
   match on the artifact's `(source, kind, key)` triple — never on names, so a
   renamed monitor with a stable key keeps its mapping.
3. **Staleness.** On load, each entry is validated: if the `ci_id` no longer
   exists or is retired in the CI inventory, or the artifact key no longer
   appears in any pulled record for two consecutive runs, the resolver emits an
   identity finding `stale_confirmed_mapping` referencing the entry. Entries
   are never auto-deleted; removal is a human/agent edit, keeping the file's
   history honest under version control.
4. **Versioning.** The file carries a schema `version` and lives in git; a run
   records the file's content hash in output metadata (REQ-OUT-002
   reproducibility) so two runs can be compared knowing which mapping state
   each used.

#### Strategy 3 — convention rules

Deterministic org-specific rules maintained as **versioned config with test
fixtures** (ADR 0002: adding an org convention is config, not code).

**File format** — `identity-conventions.yaml`:

```yaml
version: 1
rules:
  - id: dd-service-tag            # stable id, referenced in mapping evidence
    description: "Datadog service tag equals CMDB CI name"
    source: datadog               # or "*" for any source
    match:
      hint: tag                   # tag | service_name | integration_link
      key: service                # for tags: the tag key
    transform:                    # applied to the hint value, in order
      - lowercase
      - strip_suffix: ["-prod", "-production"]
    lookup:
      field: name                 # CI inventory field to equal-match against
      also: aliases               # optional secondary field
    confidence: 0.8               # optional; default 0.8

  - id: pd-service-name
    description: "PagerDuty service display name equals CI name"
    source: pagerduty
    match: { hint: service_name }
    transform: [lowercase, collapse_whitespace]
    lookup: { field: name }
```

Semantics:

- Rules are evaluated **in file order**; first rule whose transformed hint
  equal-matches exactly one CI wins.
- A rule that matches **more than one CI** does not map; it emits an identity
  finding `ambiguous_convention_match` carrying the rule id and all candidate
  CIs. Deterministic rules must be deterministic in outcome.
- Transforms are drawn from a fixed vocabulary implemented in the tool
  (`lowercase`, `strip_prefix`, `strip_suffix`, `replace`,
  `collapse_whitespace`, ...). No regex-with-capture-groups in v1 — the rule
  file stays auditable. (Escape hatch is an Open question.)
- Every convention mapping records `rule_id` in its evidence, so a bad rule is
  greppable across the whole corpus and fixable in one place.
- The rules file version + content hash go into run metadata, same as the
  mappings file.

The example rules above illustrate the **format**; the actual rule content is
org-specific and must be authored per estate (see Open questions).

#### Strategy 4 — fuzzy name similarity (suggestion-only)

Runs only for artifacts still unmapped after strategies 1–3.

1. **Normalize** both sides (artifact hints and CI names/aliases): lowercase,
   strip separators (`-`, `_`, spaces), strip a configurable stop-token list
   (`svc`, `prod`, `v2`, ... — configurable, defaults TBD).
2. **Score** each artifact hint against each CI name/alias with a string
   similarity metric (default metric and threshold are an Open question;
   the config surface is `identity.fuzzy.metric`,
   `identity.fuzzy.min_score`, `identity.fuzzy.max_candidates`).
3. **Emit** the top candidates (score ≥ `min_score`, at most
   `max_candidates`, default 3) as a candidate list **inside the identity
   finding** for that artifact.

Hard invariant, enforced structurally: the fuzzy stage's output type is a
finding payload, not a mapping record. There is no code path from a fuzzy
score into the mapping table — the only route is through the
confirmed-mappings file (strategy 2) after explicit confirmation. Tests assert
this (see Testing).

### Mapping record (pseudo-schema)

Every successful resolution produces one mapping record; the set of them is
the join table the scoring stage uses:

```yaml
artifact: { source: datadog, kind: monitor, key: "monitor/4812007" }
ci_id: "CI00012345"
method: convention               # exact | confirmed | convention
confidence: 0.8
evidence:
  rule_id: dd-service-tag        # convention only
  hint: "service:payments-api"
  matched_value: "payments-api"
resolved_at: "2026-07-04T14:02:11Z"
```

### Per-service mapping coverage

Reported per CI in the output document's `identity` block
([output-contract.md](output-contract.md), REQ-OUT-002):

```yaml
identity:
  ci_id: "CI00012345"
  ci_name: "payments-api"
  criticality_tier: 1
  criticality_source: cmdb        # cmdb | default (see below)
  mapping:
    resolved:                     # counts by data class and method
      config:  { exact: 3, confirmed: 1, convention: 8 }
      history: { exact: 0, confirmed: 0, convention: 41 }
      action:  { exact: 0, confirmed: 0, convention: 17 }
    suggested:                    # fuzzy candidates pointing at this CI,
      config: 2                   #   pending confirmation (not joined)
    coverage:
      config: 0.86                # resolved / (resolved + suggested), per class
      history: 1.0
      action: 1.0
      overall: 0.97
    min_confidence: 0.8           # weakest method feeding this service's joins
```

Definitions:

- **Per-class coverage** = artifacts resolved to this CI ÷ (resolved +
  fuzzy-suggested for this CI), per data class. Fuzzy suggestions are the
  visible, countable "probably yours but unjoined" set; artifacts with *no*
  candidate at all cannot be attributed to any service and are reported at
  corpus level (below).
- **`min_confidence`** = the lowest mapping confidence among artifacts joined
  for this service. A service scored entirely on convention matches shows 0.8,
  not 1.0.

**Corpus-level residue.** Artifacts with no mapping and no candidate ≥
`min_score` belong to no service document; the run summary reports them as an
`unattributed` list (count by source + the individual identity findings), so
nothing is silently dropped (REQ-ID-003).

**Downstream scoring transparency.** The scoring stage
([scoring-engine.md](scoring-engine.md)) must:

1. copy `coverage` and `min_confidence` into score metadata, and
2. set `partial_mapping: true` on the scores block whenever any per-class
   coverage < 1.0, so a consumer can never read a score without seeing that it
   was computed on an incomplete join (ADR 0002: "scores computed from partial
   mappings say so"). How coverage further modulates score confidence is
   scoring-engine.md's decision; this spec only guarantees the signal reaches
   it.

### Identity findings

All resolver failure modes become `type: identity` findings in the standard
finding shape (REQ-OUT-003). Subtypes defined by this spec:

| subtype | meaning | severity default |
|---|---|---|
| `unmapped_artifact` | no strategy produced a mapping; candidates may be attached | medium |
| `dangling_ci_reference` | explicit CI hint not in inventory | high |
| `stale_ci_reference` | explicit CI hint points at a retired CI | medium |
| `ambiguous_ci_reference` | conflicting explicit CI hints on one artifact | high |
| `ambiguous_convention_match` | rule matched multiple CIs | medium |
| `stale_confirmed_mapping` | confirmed-mapping entry no longer validates | low |
| `missing_criticality` | resolved CI has no usable criticality tier | medium |

Example — an unmapped artifact with fuzzy candidates:

```json
{
  "type": "identity",
  "subtype": "unmapped_artifact",
  "severity": "medium",
  "confidence": 1.0,
  "rationale": "Datadog monitor 'Payments API p95 latency' carries no CI reference and matched no convention rule; 1 fuzzy candidate found.",
  "evidence": {
    "artifact": { "source": "datadog", "kind": "monitor", "key": "monitor/4812007" },
    "hints_tried": ["service:payments_api_v2"],
    "rules_tried": ["dd-service-tag"],
    "candidates": [
      { "ci_id": "CI00012345", "ci_name": "payments-api", "score": 0.91 }
    ]
  },
  "proposed_change": {
    "kind": "confirm_mapping",
    "command": "alertlint identity confirm datadog:monitor/4812007 CI00012345"
  }
}
```

The `proposed_change` block is the level-B recommendation (REQ-REC-002): the
concrete action that, once taken, moves this artifact into strategy 2 on the
next run.

### Criticality tier lookup (REQ-ID-004)

Once an artifact set resolves to a CI, the resolver reads the criticality tier
from the CI inventory record and attaches it to the service's identity block.
Scoring uses it for normalization and priority weighting (REQ-SCORE-005/006);
it is resolved *here* because the CI record is already in hand and the tier
must be present before any score is computed.

**Missing/unknown criticality (REQ-CRIT-003):**

- Tier absent, or a value outside the org's tier scale →
  - emit identity finding `missing_criticality` against the service, and
  - assign the configurable default middle tier
    (`identity.criticality.default_tier` in config; the concrete value depends
    on the org's tier scale — Open question), and
  - set `criticality_source: default` in the identity block so downstream
    consumers and humans can distinguish "CMDB says tier 3" from "we assumed
    tier 3".

The service is thus neither hidden nor over-prioritized, and the gap itself is
actionable output.

### Worked example — one Datadog monitor through the chain

Datadog monitor `monitor/4812007`, name "Payments API p95 latency", tags
`["service:payments_api_v2", "env:prod", "team:core-payments"]`.

**Run 1:**

1. *Exact*: no configured CI-id tag key present → no match.
2. *Confirmed*: `identity-mappings.yaml` has no entry for
   `datadog:monitor/4812007` → no match.
3. *Convention*: rule `dd-service-tag` fires on `service:payments_api_v2`,
   transforms to `payments_api_v2`, equal-matches against CI `name`/`aliases`
   → no CI named `payments_api_v2` → no match.
4. *Fuzzy*: normalize `payments_api_v2` → `paymentsapi` (separators stripped,
   `v2` in stop-token list); CI `payments-api` normalizes to `paymentsapi`;
   similarity 0.91 ≥ `min_score` → **identity finding** (the JSON example
   above). The monitor's config record is **not** joined to CI00012345;
   `payments-api`'s config coverage shows one `suggested` artifact.

**Between runs:** the agent surfaces the finding; a human confirms;
`alertlint identity confirm datadog:monitor/4812007 CI00012345` appends the
mappings-file entry shown earlier.

**Run 2:**

1. *Exact*: no match.
2. *Confirmed*: entry matches on `(datadog, monitor, monitor/4812007)` and
   `CI00012345` validates as operational → mapping
   `{method: confirmed, confidence: 0.95}`. The monitor's config and its
   fire history now join into `payments-api`'s scoring; config coverage for
   the service ratchets up accordingly. Criticality tier 1 is read from the
   CI record; `criticality_source: cmdb`.

If instead the tag had been `service:payments-api`, strategy 3 would have
mapped it on run 1 (`method: convention, confidence: 0.8, rule_id:
dd-service-tag`). If the monitor had carried `ci_id:CI00012345`, strategy 1
maps it at confidence 1.0.

## Alternatives considered

The strategic alternatives — assume-clean-references and full fuzzy bootstrap —
were considered and rejected in [ADR 0002](../decision-records/0002-identity-resolution-strategy.md);
this spec does not relitigate them. Spec-level alternatives:

- **Auto-accept fuzzy matches above a high threshold (e.g. ≥ 0.95).** Rejected:
  reintroduces silent wrong joins, exactly the failure mode ADR 0002 option 2
  was rejected for; a wrong join at 0.96 is still a corrupted score with no
  tell.
- **Fold confirmed mappings into the convention rules file.** Rejected: rules
  are *patterns* authored by humans, mappings are *point facts* accumulated by
  the tool; they have different schemas, churn rates, and review needs.
  Separate files, same directory, both versioned.
- **Regex-based convention rules.** Deferred: a fixed transform vocabulary
  keeps rules auditable and testable; a regex escape hatch can be added behind
  an explicit rule flag if the vocabulary proves insufficient (Open question).
- **Resolve identity inside each adapter.** Rejected by ADR 0004: adapters know
  nothing about the CMDB; resolution is core logic applied uniformly after
  adaptation, so a convention or mapping fix applies to all sources at once.

## Testing & acceptance

**Test strategy**

- **Fixture-driven unit tests per strategy.** Canonical-record fixtures (the
  same snapshot-cache format as ADR 0004) plus a synthetic CI inventory
  fixture; golden expected outputs for the mapping table, coverage block, and
  findings list.
- **Convention rule-set tests.** The rules file ships with test fixtures
  (ADR 0002): each rule carries example hints that must match and near-miss
  hints that must not. `alertlint identity test-rules` runs them; CI gates on
  it, so editing a rule without updating its fixtures fails the build.
- **The fuzzy invariant.** A test corpus where fuzzy candidates score 1.0
  asserts the mapping table still contains zero fuzzy-method entries and the
  candidates appear only inside findings.
- **Ratchet round-trip.** Integration test: run 1 emits an
  `unmapped_artifact` finding → `identity confirm` → run 2 maps via
  `confirmed`, coverage strictly increases, finding disappears.
- **Staleness.** Fixture with a retired CI referenced by a confirmed mapping →
  `stale_confirmed_mapping` finding emitted, mapping not applied.
- **Criticality fallback.** Fixture CI with no tier → `missing_criticality`
  finding, default tier applied, `criticality_source: default`.
- **Determinism.** Same inputs (records + inventory + both config files) →
  byte-identical mapping table and findings across runs (REQ-SCORE-007's
  reproducibility discipline applied to identity).

**Acceptance criteria**

1. Every input artifact appears in exactly one of: the mapping table, an
   identity finding, or the corpus-level `unattributed` list — no silent drops
   (REQ-ID-003).
2. Every mapping record carries `method` and `confidence`; no mapping ever has
   `method: fuzzy`.
3. Per-service identity block contains per-class coverage, `min_confidence`,
   criticality tier, and `criticality_source`, matching
   [output-contract.md](output-contract.md).
4. Scores emitted for a service with any per-class coverage < 1.0 carry
   `partial_mapping: true`.
5. Confirming a fuzzy candidate is a one-command operation and takes effect on
   the next run with no code change.
6. Rules and mappings file hashes appear in run metadata.

## Open questions

- **CI inventory access shape.** Fourth narrow provider interface
  (`CiProvider`) vs. an extension of the ServiceNow adapter — ADR 0004 defined
  three interfaces for the three data classes and the CI inventory fits none.
  Owner: dave; resolve in [provider-adapters.md](provider-adapters.md) before
  adapter work starts.
- **Org tier scale and CMDB field names.** How many criticality tiers the org's
  CMDB uses, which field carries the tier, which field is the canonical CI
  name, and whether aliases exist — all org-specific facts needed to set
  `identity.criticality.default_tier` (the "configurable middle tier") and the
  inventory extract. Owner: dave, against the real ServiceNow tenant.
- **Fuzzy metric and defaults.** Which similarity metric (Jaro-Winkler,
  token-set ratio, ...), default `min_score`, and the default stop-token list.
  Plan: evaluate against a labeled fixture set of real artifact/CI name pairs
  once a sample extract exists; until then ship conservative placeholders
  clearly marked non-final.
- **Confirmed-mappings file location under the laptop execution model.** ADR
  0001 makes v1 per-caller; if each caller keeps a local mappings file the
  ratchet fragments per laptop. Options: repo-versioned shared file with PR
  review vs. local file with a merge/export command. Owner: dave.
- **Conflicting explicit CI hints.** When one artifact carries two different
  explicit CI references, is there ever a deterministic precedence (e.g.
  integration link over tag), or is `ambiguous_ci_reference` + human
  resolution always the answer? Owner: dave; default to the finding until a
  real case motivates precedence.
- **One artifact, many services.** Shared monitors (one Datadog monitor
  covering several services via `group by`) break the artifact→single-CI
  assumption. v1 maps to at most one CI and flags the pattern if detectable;
  a multi-CI mapping model is deferred. Owner: dave; revisit with
  [scoring-engine.md](scoring-engine.md) since it changes join semantics.
- **Regex escape hatch for convention rules.** Add only if the fixed transform
  vocabulary proves insufficient against real estate conventions. Owner: dave;
  decide after the first real rules file is authored.
- **Retired/merged CI churn cadence.** How aggressively to invalidate mappings
  when CIs are retired or merged in the CMDB (immediate finding vs. grace
  period). Owner: dave; current spec says immediate `stale_*` findings, no
  auto-deletion.
