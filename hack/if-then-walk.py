#!/usr/bin/env python3
"""if-then-walk.py — inventory the if/then/else shapes that crdgen #212 TIGHTENED.

    python3 -I hack/if-then-walk.py <file-or-dir> [...]

WHY A STRUCTURAL WALK AND NOT A GREP. `if` is a schema KEY, and a grep for it matches the English word in
every description, every Go snippet in a doc, and `if` inside an unrelated vendor extension. It also misses
nothing useful: the three spellings a grep would have to enumerate (`if:`, `{if: ...}`, `"if":`) all parse
to the same key. Same reason hack/not-walk.py exists, and this is its sibling.

WHAT #212 CHANGED, and why a warning cannot be the detector. Before crdgen #212, a property constraint
inside an if/then/else branch other than const/enum was silently reduced to a bare `has()`:

  ENFORCED NOW, SILENT BEFORE   minLength maxLength minimum maximum exclusiveMinimum exclusiveMaximum
                                minItems maxItems minProperties maxProperties
    -> an object ADMITTED while the constraint was dropped can be REFUSED at its next write
  WARNS NOW, SILENT BEFORE      pattern format multipleOf, and nested objects inside a branch
    -> ConstraintDropped, naming the property
  BRANCH SELECTION CHANGED      in a condition, an absent property that is neither `required` nor
                                defaulted now MATCHES, as JSON Schema says
    -> a different branch can apply to an object that was admitted under the old reading

**The version that dropped these emitted NO warning for either change**, so the old render's warnings do
not list the affected paths. A warning-absence is not evidence here; the walk is.

EXIT STATUS
  0  no if/then/else carrying a tightened clause was found, and the controls held
  1  at least one was found: upgrade with a render pair, not on this script's word
  2  THE RESULT MEANS NOTHING -- a control failed, or a file in scope would not parse
"""
import json
import os
import sys

try:
    import yaml
except ImportError:
    print("FATAL: PyYAML not installed; a text grep would need all three spellings", file=sys.stderr)
    sys.exit(2)

# Keywords #212 now ENFORCES in CEL, having previously reduced them to has().
ENFORCED = (
    "minLength", "maxLength", "minimum", "maximum", "exclusiveMinimum",
    "exclusiveMaximum", "minItems", "maxItems", "minProperties", "maxProperties",
)
# Keywords #212 now WARNS about (ConstraintDropped) rather than dropping in silence.
WARNS = ("pattern", "format", "multipleOf")


def branch_clauses(branch):
    """Yield (property, keyword) for every constraint in a then/else branch's properties."""
    if not isinstance(branch, dict):
        return
    props = branch.get("properties")
    if not isinstance(props, dict):
        return
    for prop, spec in props.items():
        if not isinstance(spec, dict):
            continue
        for kw in ENFORCED + WARNS:
            if kw in spec:
                yield prop, kw
        # A nested object inside a branch is itself in the warns set.
        if isinstance(spec.get("properties"), dict):
            yield prop, "nested-object"


def condition_optionals(node):
    """Yield properties named by `if` that are neither required nor defaulted at this level.

    These are the ones whose ABSENCE now matches, which can select a different branch than before.
    """
    cond = node.get("if")
    if not isinstance(cond, dict):
        return
    cprops = cond.get("properties")
    if not isinstance(cprops, dict):
        return
    required = node.get("required") or []
    if not isinstance(required, list):
        required = []
    own = node.get("properties") or {}
    for prop in cprops:
        if prop in required:
            continue
        spec = own.get(prop) if isinstance(own, dict) else None
        if isinstance(spec, dict) and "default" in spec:
            continue
        yield prop


def walk(node, path, hits):
    if isinstance(node, dict):
        if "if" in node:
            found = []
            for key in ("then", "else"):
                for prop, kw in branch_clauses(node.get(key)):
                    found.append((key, prop, kw))
            optionals = list(condition_optionals(node))
            if found or optionals:
                hits.append({"path": path or "<root>", "clauses": found, "optional_conditions": optionals})
        for k, v in node.items():
            walk(v, f"{path}.{k}" if path else k, hits)
    elif isinstance(node, list):
        for i, v in enumerate(node):
            walk(v, f"{path}[{i}]", hits)


def load(p):
    with open(p, "r", encoding="utf-8") as f:
        txt = f.read()
    try:
        if p.endswith(".json"):
            return [json.loads(txt)], None
        return list(yaml.safe_load_all(txt)), None
    except Exception as e:  # noqa: BLE001 - the reason is printed, not acted on
        return None, str(e)


def collect(targets):
    files = []
    for t in targets:
        if os.path.isfile(t):
            files.append(t)
        else:
            for root, _, names in os.walk(t):
                for n in names:
                    if n.endswith((".yaml", ".yml", ".json")):
                        files.append(os.path.join(root, n))
    return sorted(files)


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        return 2

    # ---- CONTROLS FIRST. A walk that returns zero because it cannot match is the failure mode, and it
    # ---- looks exactly like a clean corpus.
    planted_enforced = {"components": {"schemas": {"X": {
        "type": "object",
        "properties": {"mode": {"type": "string"}, "name": {"type": "string"}},
        "required": ["mode"],
        "if": {"properties": {"mode": {"const": "named"}}},
        "then": {"properties": {"name": {"minLength": 1}}},
    }}}}
    h = []
    walk(planted_enforced, "", h)
    if len(h) != 1 or ("then", "name", "minLength") not in h[0]["clauses"]:
        print(f"CONTROLS FAILED: planted minLength-in-then produced {h}", file=sys.stderr)
        return 2

    planted_optional = {"X": {
        "type": "object",
        "properties": {"create": {"type": "boolean"}},
        "if": {"properties": {"create": {"const": False}}},
        "then": {"required": ["name"]},
    }}
    h2 = []
    walk(planted_optional, "", h2)
    if len(h2) != 1 or h2[0]["optional_conditions"] != ["create"]:
        print(f"CONTROLS FAILED: planted optional condition produced {h2}", file=sys.stderr)
        return 2

    # A schema with an if/then carrying ONLY const must NOT be reported: const/enum were always enforced.
    planted_clean = {"X": {
        "type": "object",
        "properties": {"mode": {"type": "string", "default": "a"}},
        "required": ["mode"],
        "if": {"properties": {"mode": {"const": "a"}}},
        "then": {"properties": {"other": {"const": "b"}}},
    }}
    h3 = []
    walk(planted_clean, "", h3)
    if h3:
        print(f"CONTROLS FAILED: a const-only if/then was reported as affected: {h3}", file=sys.stderr)
        return 2

    print("CONTROLS HELD: enforced clause found, optional condition found, const-only not reported")

    files = collect(sys.argv[1:])
    if not files:
        print("CONTROLS FAILED: no .yaml/.yml/.json file in scope", file=sys.stderr)
        return 2

    total = 0
    unparseable = []
    for p in files:
        # Helm templates are not schemas; `{{ }}` is not YAML and parsing them proves nothing.
        with open(p, "r", encoding="utf-8") as f:
            head = f.read(4096)
        if "{{" in head:
            continue
        docs, err = load(p)
        if err:
            unparseable.append((p, err))
            continue
        hits = []
        for d in docs or []:
            walk(d, "", hits)
        for hit in hits:
            total += 1
            print(f"\nFOUND  {p}\n       at {hit['path']}")
            for key, prop, kw in hit["clauses"]:
                sev = "ENFORCED NOW (an admitted object can be REFUSED)" if kw in ENFORCED else "WARNS NOW"
                print(f"         {key}.{prop}: {kw}   <- {sev}")
            for prop in hit["optional_conditions"]:
                print(f"         if.{prop}: neither required nor defaulted   <- ABSENT NOW MATCHES, "
                      f"so a different branch can apply")

    if unparseable:
        print(f"\nRESULT MEANS NOTHING: {len(unparseable)} file(s) in scope would not parse:", file=sys.stderr)
        for p, err in unparseable[:5]:
            print(f"  {p}: {err[:120]}", file=sys.stderr)
        return 2

    print(f"\nSCANNED {len(files)} file(s). AFFECTED if/then/else shapes: {total}")
    if total:
        print("UPGRADE WITH A RENDER PAIR: generate the CRDs on both plumbing versions and diff them.")
        return 1
    print("CLEAR for crdgen #212's tightening, on this corpus, by structural walk rather than by "
          "warning-absence.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
