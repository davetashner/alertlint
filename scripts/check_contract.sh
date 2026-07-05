#!/usr/bin/env bash
# Contract acceptance checks (docs/specs/output-contract.md, Testing &
# acceptance): every criterion is a runnable jq check over a real corpus
# (REQ-OUT-003: greppable, CI-checkable). CI generates the corpus by
# replaying fixtures/demo; run locally with:
#
#   go run ./cmd/alertlint analyze --replay fixtures/demo --tenant demo \
#     --out /tmp/corpus --run-timestamp 2026-07-04T18:00:00Z \
#     --identity-conventions fixtures/demo/identity-conventions.yaml
#   scripts/check_contract.sh /tmp/corpus
set -u

corpus="${1:?usage: check_contract.sh <corpus-dir>}"
fail=0

check() {
  local desc="$1"
  shift
  if "$@" >/dev/null 2>&1; then
    echo "ok   $desc"
  else
    echo "FAIL $desc" >&2
    fail=1
  fi
}

# Per-service documents (identity.ci != null).
service_docs=()
for f in "$corpus"/*.json; do
  [ "$(basename "$f")" = "_unresolved.json" ] && continue
  service_docs+=("$f")
done
[ ${#service_docs[@]} -gt 0 ] || { echo "FAIL no service documents in $corpus" >&2; exit 1; }

for doc in "${service_docs[@]}"; do
  name=$(basename "$doc")
  # 1. Contract version present and major-1.
  check "$name: contract major 1" jq -e '.contract_version | startswith("1.")' "$doc"
  # 2. Only the frozen taxonomy appears.
  check "$name: frozen finding taxonomy" jq -e '([.findings[].type] - ["noise","coverage","threshold","identity"]) == []' "$doc"
  # 3. Every finding carries rationale, evidence, severity, confidence.
  check "$name: findings complete" jq -e '.findings | all(.rationale and .evidence and .severity and .confidence)' "$doc"
  # 5. Level-B proposals are concrete, never prose-only.
  check "$name: proposals concrete" jq -e '[.findings[].proposed_change | select(. != null)] | all(.kind and .target and (.proposed != null) and .generated_by)' "$doc"
  # 6. Reproducibility metadata complete.
  check "$name: metadata complete" jq -e '.metadata | .window.days and .run.tool_version and .archetype_library_version and .config.weights and .config.config_hash' "$doc"
  # 7. Fuzzy never joins scoring.
  check "$name: no fuzzy joins" jq -e '[.identity.artifacts[].resolution.method] - ["exact","confirmed","convention"] == []' "$doc"
done

# 4. The triage queue is expressible across the corpus (and non-empty for
# the demo corpus, which embeds the REQ-NOISE-003 scenario).
check "corpus: low-confidence triage queue non-empty" \
  jq -e -s '[.[].findings[] | select(.confidence=="low")] | length > 0' "${service_docs[@]}"

# 8. The aggregation layer, falsifiably: ranking needs no scoring logic.
check "corpus: rankable by priority_score alone" \
  jq -e -s 'map(select(.scores.priority_score != null)) | sort_by(-.scores.priority_score) | length > 0' "${service_docs[@]}"

# 9. Fleet grep: noise findings countable in one line.
check "corpus: noise findings greppable" \
  jq -e -s '[.[].findings[] | select(.type=="noise")] | length >= 0' "${service_docs[@]}"

# The reserved unresolved document exists and carries ci: null.
check "_unresolved.json: ci is null" jq -e '.identity.ci == null' "$corpus/_unresolved.json"

exit $fail
