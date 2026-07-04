#!/usr/bin/env python3
"""Enforce docs traceability conventions (see docs/specs/ci-pipeline.md).

Checks, in order:
  1. Specs carry the Status / Date / Requirements header fields.
  2. ADRs carry a Status field and Context / Decision / Consequences sections.
  3. Every REQ-<CATEGORY>-NNN referenced in a spec exists in docs/requirements/.
  4. Every ADR file appears in the decision-records README index.

Stdlib only. Exit 1 with one line per violation.
"""
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
REQ_ID = re.compile(r"REQ-[A-Z]+-\d{3}")
SKIP = {"README.md", "TEMPLATE.md", "0000-template.md"}

errors: list[str] = []


def rel(p: Path) -> str:
    return str(p.relative_to(ROOT))


specs = [p for p in sorted((ROOT / "docs" / "specs").glob("*.md")) if p.name not in SKIP]
adrs = [p for p in sorted((ROOT / "docs" / "decision-records").glob("*.md")) if p.name not in SKIP]
requirements_text = "\n".join(
    p.read_text(encoding="utf-8") for p in (ROOT / "docs" / "requirements").glob("*.md")
)
known_req_ids = set(REQ_ID.findall(requirements_text))

for spec in specs:
    text = spec.read_text(encoding="utf-8")
    for field in ("- **Status:**", "- **Date:**", "- **Requirements:**"):
        if field not in text:
            errors.append(f"{rel(spec)}: missing spec header field `{field}`")
    for req_id in sorted(set(REQ_ID.findall(text)) - known_req_ids):
        errors.append(f"{rel(spec)}: references {req_id}, not found in docs/requirements/")

adr_index = (ROOT / "docs" / "decision-records" / "README.md").read_text(encoding="utf-8")
for adr in adrs:
    text = adr.read_text(encoding="utf-8")
    if "- **Status:**" not in text:
        errors.append(f"{rel(adr)}: missing ADR header field `- **Status:**`")
    for section in ("## Context", "## Decision", "## Consequences"):
        if section not in text:
            errors.append(f"{rel(adr)}: missing section `{section}`")
    if adr.name not in adr_index:
        errors.append(f"{rel(adr)}: not listed in docs/decision-records/README.md index")

if errors:
    print(f"traceability: {len(errors)} violation(s)", file=sys.stderr)
    for e in errors:
        print(f"  {e}", file=sys.stderr)
    sys.exit(1)

print(f"traceability: OK ({len(specs)} specs, {len(adrs)} ADRs, {len(known_req_ids)} requirement IDs)")
