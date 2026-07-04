#!/usr/bin/env python3
"""Validate archetypes/library.yaml against its JSON Schema plus the
cross-file rules the schema alone cannot express
(docs/specs/archetype-library.md §1, §6):

  1. JSON Schema conformance (archetypes/library.schema.json).
  2. Every required_signal anchor is a member of the anchors enum.
  3. Archetype ids are unique; signal ids are unique within an archetype.
  4. Every metric_pattern compiles (Python re approximation of RE2; the Go
     evaluator's tests are the authoritative RE2 compile check).

Requires: pyyaml, jsonschema (CI installs them; both are common locally).
"""
import json
import re
import sys
from pathlib import Path

import jsonschema
import yaml

ROOT = Path(__file__).resolve().parent.parent
LIB = ROOT / "archetypes" / "library.yaml"
SCHEMA = ROOT / "archetypes" / "library.schema.json"

errors: list[str] = []

doc = yaml.safe_load(LIB.read_text(encoding="utf-8"))
schema = json.loads(SCHEMA.read_text(encoding="utf-8"))

validator = jsonschema.Draft202012Validator(schema)
for err in sorted(validator.iter_errors(doc), key=lambda e: e.json_path):
    errors.append(f"schema: {err.json_path}: {err.message}")

if not errors:
    anchors = set(doc["anchors"])
    seen_archetypes: set[str] = set()

    def patterns_of(node):
        if isinstance(node, dict):
            if node.get("kind") == "metric_pattern":
                yield node["pattern"]
            for v in node.values():
                yield from patterns_of(v)
        elif isinstance(node, list):
            for item in node:
                yield from patterns_of(item)

    for arch in doc["archetypes"]:
        aid = arch["id"]
        if aid in seen_archetypes:
            errors.append(f"{aid}: duplicate archetype id")
        seen_archetypes.add(aid)

        seen_signals: set[str] = set()
        for sig in arch["required_signals"]:
            sid = sig["id"]
            if sid in seen_signals:
                errors.append(f"{aid}/{sid}: duplicate signal id")
            seen_signals.add(sid)
            if sig["anchor"] not in anchors:
                errors.append(f"{aid}/{sid}: anchor {sig['anchor']!r} not in anchors enum")

        for pattern in patterns_of(arch):
            try:
                re.compile(pattern)
            except re.error as exc:
                errors.append(f"{aid}: pattern {pattern!r} does not compile: {exc}")

if errors:
    print(f"archetypes: {len(errors)} violation(s)", file=sys.stderr)
    for e in errors:
        print(f"  {e}", file=sys.stderr)
    sys.exit(1)

print(
    f"archetypes: OK (library {doc['library_version']}, "
    f"{len(doc['archetypes'])} archetypes, {len(doc['anchors'])} anchors)"
)
