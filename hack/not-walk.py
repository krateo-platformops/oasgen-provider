#!/usr/bin/env python3
"""
not-walk.py -- does a chart's OAS assets contain a schema key named `not`?

A `not` over an object's own `required`/`min`/`maxProperties` is DROPPED by crdgen before
plumbing v1.93.0 (oasgen-provider <= 0.78.0) and ENFORCED from v1.93.0 (>= 0.79.0). So a CR
admitted on the older version can be REFUSED on the newer one. No `not` in the input means
nothing to enforce, whatever the version does.

This walks parsed structure, not text, so it is immune to the three spellings (`not:`,
`{not: ...}`, `"not":`) that a grep has to enumerate separately.

Usage:   not-walk.py <dir-or-file> [<dir-or-file> ...]
Exit:    0 = no `not` found and the controls held
         1 = `not` found (paths printed)
         2 = THE CONTROLS DID NOT HOLD -- the result means nothing, do not read it
"""
import json, sys, os

try:
    import yaml
except ImportError:
    print("FATAL: PyYAML not installed; a text grep would need all three spellings", file=sys.stderr)
    sys.exit(2)

def walk(node, path, hits, refs):
    if isinstance(node, dict):
        for k, v in node.items():
            here = f"{path}.{k}" if path else k
            if k == "not":
                hits.append(here)
            if k == "$ref" and isinstance(v, str) and not v.startswith("#"):
                refs.append((here, v))   # an EXTERNAL ref can carry a `not` we cannot see
            walk(v, here, hits, refs)
    elif isinstance(node, list):
        for i, v in enumerate(node):
            walk(v, f"{path}[{i}]", hits, refs)

def load(p):
    with open(p, "r", encoding="utf-8") as f:
        txt = f.read()
    try:
        return list(yaml.safe_load_all(txt)), None
    except Exception as e:
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
        print(__doc__); return 2

    # ---- CONTROLS FIRST. A walk that returns zero because it cannot match is the failure mode.
    planted = {"components": {"schemas": {"X": {"type": "object",
               "properties": {"a": {"type": "string"}},
               "not": {"required": ["a"]}}}}}
    h, r = [], []
    walk(planted, "", h, r)
    if len(h) != 1:
        print(f"CONTROLS FAILED: the walker found {len(h)} `not` in a planted schema, want 1")
        return 2
    clean = {"components": {"schemas": {"X": {"type": "object", "required": ["a"],
             "allOf": [{"type": "object"}], "properties": {"notes": {"type": "string"},
             "annotation": {"type": "string"}}}}}}
    h2, _ = [], []
    walk(clean, "", h2, [])
    if h2:
        print(f"CONTROLS FAILED: the walker found `not` in a clean schema: {h2}")
        return 2
    print("CONTROLS HELD   planted -> 1 hit;  clean (incl. a `notes` key) -> 0 hits")

    files = collect(sys.argv[1:])
    if not files:
        print("CONTROLS FAILED: no .yaml/.yml/.json files under the given paths")
        return 2
    print(f"files walked: {len(files)}")

    hits, refs, unreadable, templated, keys = [], [], [], [], 0
    for p in files:
        with open(p, "r", encoding="utf-8", errors="replace") as fh:
            raw = fh.read()
        # A Helm TEMPLATE is not an OAS asset. Classifying it as "unreadable" would fail the
        # controls for a file that was never in scope -- a different thing from a file in scope
        # that could not be read, which must still fail them.
        if "{{" in raw and "}}" in raw:
            templated.append(p); continue
        docs, err = load(p)
        if err:
            unreadable.append((p, err)); continue
        for d in docs:
            if d is None: continue
            h, r = [], []
            walk(d, "", h, r)
            keys += 1
            hits += [(p, x) for x in h]
            refs += [(p, a, b) for a, b in r]

    if templated:
        print(f"skipped {len(templated)} Helm template(s) -- not OAS assets. Point this at the chart's "
              f"assets directory, not its templates.")
    if unreadable:
        print(f"CONTROLS FAILED: {len(unreadable)} file(s) could not be parsed, so their content is UNKNOWN:")
        for p, e in unreadable[:5]:
            print(f"  {p}: {e[:90]}")
        return 2

    if refs:
        print(f"CAVEAT: {len(refs)} EXTERNAL $ref(s) -- their targets are not walked, so a `not` could hide there:")
        for p, a, b in refs[:5]:
            print(f"  {p}  {a} -> {b}")

    if hits:
        print(f"FOUND {len(hits)} `not` key(s) -- these kinds may REFUSE existing CRs after the upgrade:")
        for p, x in hits:
            print(f"  {p}  {x}")
        return 1

    print(f"NO `not` FOUND across {keys} document(s). Unaffected by the crdgen tightening.")
    return 0

if __name__ == "__main__":
    sys.exit(main())
