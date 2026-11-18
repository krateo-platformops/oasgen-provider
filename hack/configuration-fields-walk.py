#!/usr/bin/env python3
"""configuration-fields-walk.py — inventory the configurationFields entries that #512 and #828 will REFUSE.

    python3 -I hack/configuration-fields-walk.py --oas-dir <dir> <restdefinition.yaml|dir> [...]

WHY A STATIC WALK AND WHY BEFORE THE REFUSAL. Both #512 and #828 make generation REFUSE something it
accepts today, so either can stop a RestDefinition that reconciles right now. The repo's rule for a
tightening is: detect statically, count the live instances, then decide -- never ship the refusal and
discover the count from a consumer's broken cluster.

WHAT #512 WILL REFUSE
    offset limit filter sort projection, as a configurationField bound to the FINDBY action.
    A findby is how the controller decides whether a resource EXISTS. Narrowing that listing makes the
    check answer "absent" for a resource that is present, and on first contact a create follows.
    Measured on one vendor: offset was honoured at 1, 2, 10, 50, 1000 and 10000 with NO clamping, each
    returning 0 items with total=1. A successful 200 with an empty list is indistinguishable from absence.
    They stay legitimate on every OTHER action: narrowing a list a user asked for is a feature.

WHAT #828 WILL REFUSE
    an entry naming a parameter the document serving that action does not DECLARE. Today
    configuration_builder.go skips it silently (`// TODO: Consider logging a warning here.`), so no
    property reaches the generated Configuration CRD and a Configuration CR setting it is rejected three
    layers away, naming the CR rather than the cause.

THE TWO ARE DIFFERENT REFUSALS and this script reports them separately: #512 is "you may not expose this",
#828 is "this does not exist". A corpus can trip either, both, or neither.

`*` IS EXPANDED, because the generator expands it (render/helper.go expandWildcardActions). An entry bound
to ["*"] applies to every action the resource declares, so a five-name hit on a wildcard entry IS a #512
instance even though the word "findby" never appears in the RestDefinition.

WHAT THIS SCRIPT CANNOT DO. It cannot resolve `configmap://` or `http://` oasPath values, which is why
--oas-dir is required. Every document under it is indexed by its FULL PATH -- not its basename, because a
corpus keeps one document per kind directory and they are routinely all called the same thing, and a
basename key silently indexes one of them and then answers confidently over a fraction of the corpus. A
verb path found in NO indexed document is reported as UNRESOLVED and makes the run VOID rather than clean:
"no #828 instances" over documents nobody read is the exact false-looking zero this repo keeps paying for.

Multi-document files are read document by document, so `helm template` output can be passed directly.

EXIT STATUS
  0  the corpus trips neither refusal, AND every control held
  1  at least one instance found: the counts are in the output, decide from them
  2  THE RESULT MEANS NOTHING -- a control failed: a file would not parse, a document was
     unresolved, or no configurationFields entry was seen anywhere (nothing was tested)
"""
import json
import os
import sys

try:
    import yaml
except ImportError:
    print("configuration-fields-walk: PyYAML is required", file=sys.stderr)
    sys.exit(2)

# #512's list, verbatim from the issue.
VIEW_NARROWING = ("offset", "limit", "filter", "sort", "projection")
FINDBY = "findby"


def load_docs(path):
    with open(path, encoding="utf-8") as fh:
        return [d for d in yaml.safe_load_all(fh) if isinstance(d, dict)]


def oas_index(oas_dir):
    """basename -> parsed document. Both .json and .yaml/.yml, because assets use both."""
    idx = {}
    for root, _, files in os.walk(oas_dir):
        for f in files:
            if not f.lower().endswith((".json", ".yaml", ".yml")):
                continue
            p = os.path.join(root, f)
            key = p  # NOT the basename: a corpus keeps one document per kind directory and they
                     # are routinely all called the same thing. Keying by basename silently indexes
                     # ONE of them and then reports a confident answer over a fraction of the corpus.
            try:
                with open(p, encoding="utf-8") as fh:
                    text = fh.read()
            except Exception as exc:
                idx[key] = ("UNREADABLE", type(exc).__name__)
                continue
            try:
                # MULTI-DOCUMENT, because a charted consumer's natural input is `helm template` output
                # and splitting it first is a step every one of them would have to discover. A single
                # safe_load on a multi-doc stream raises ComposerError, which this walk correctly reported
                # as a VOID -- correct and unhelpful: the first consumer to run it hit exactly that, and
                # so would every other chart-based corpus.
                if f.lower().endswith(".json"):
                    docs_in_file = [json.loads(text)]
                else:
                    docs_in_file = [d for d in yaml.safe_load_all(text) if d is not None]
            except Exception as exc:
                # A file that will not parse is a CONTROL FAILURE only if it CLAIMS to be an OAS
                # document. --oas-dir is a directory a consumer points at, and it will contain
                # Kubernetes manifests, kustomizations and READMEs; voiding the run on one of those
                # makes the VOID fire spuriously, and a control that cries wolf gets ignored -- which
                # is worse than not having it. Claimed-and-broken still voids: that one IS a document
                # whose parameters nobody read.
                claims = any(k in text[:4096] for k in ('openapi', 'swagger', '"paths"', 'paths:'))
                idx[key] = ("UNPARSED", type(exc).__name__) if claims else ("NOT-OAS", "does not claim to be one")
                continue
            multi = len(docs_in_file) > 1
            indexed_any = False
            for di, doc in enumerate(docs_in_file):
                dkey = f"{key}#{di}" if multi else key
                if isinstance(doc, dict) and ("openapi" in doc or "swagger" in doc or "paths" in doc):
                    idx[dkey] = ("OK", doc)
                    indexed_any = True
                    continue
            # AND A CONFIGMAP COUNTS, because that is how an oasPath of `configmap://` is served and so
            # it is the shape a real corpus has. This repo's own examples embed every document in a
            # ConfigMap `data` value; a walk that reads only bare .json/.yaml documents indexes zero of
            # them and then reports "no #828 instances" over nothing -- the false zero this script
            # exists to refuse. Each data value is parsed on its own and keyed `<file>:<data-key>`.
                if isinstance(doc, dict) and doc.get("kind") == "ConfigMap":
                    for dk, dv in (doc.get("data") or {}).items():
                        if not isinstance(dv, str):
                            continue
                        try:
                            inner = json.loads(dv) if dv.lstrip().startswith("{") else yaml.safe_load(dv)
                        except Exception as exc:
                            idx[dkey + ":" + dk] = ("UNPARSED", type(exc).__name__)
                            indexed_any = True
                            continue
                        if isinstance(inner, dict) and ("openapi" in inner or "swagger" in inner or "paths" in inner):
                            inner["__cm_name__"] = (doc.get("metadata") or {}).get("name")
                            idx[dkey + ":" + dk] = ("OK", inner)
                            indexed_any = True
            if not indexed_any:
                idx[key] = ("NOT-OAS", "parsed, but declares no openapi/swagger/paths")
    return idx


def resolve_oas(oas_path, idx):
    """The document an oasPath NAMES, not the first one that happens to contain the verb's path.

    THIS WAS A FALSE-POSITIVE FACTORY. The walk used to search every indexed document for the verb's
    path and take the FIRST match. On a corpus carrying two versions of one API -- which is the normal
    shape, and exactly what #108 is about -- both documents declare the same path and only one declares
    the parameter. Measured on a consumer's 36-definition corpus: the walk reported `api-version` as
    undeclared on a findby, and the document the RestDefinition actually names declares it. Their
    independent implementation said 0 and was right.

    A FALSE POSITIVE ON A REFUSAL GATE IS THE WORST KIND. It would either block a refusal that should
    ship, or get "fixed" by a consumer deleting a legitimate entry.

    Two spellings are resolved:
        configmap://<namespace>/<configmap-name>/<data-key>   -> that ConfigMap's data key
        anything else                                          -> matched on basename
    """
    if not oas_path:
        return None
    if oas_path.startswith("configmap://"):
        parts = oas_path[len("configmap://"):].split("/")
        if len(parts) < 3:
            return None
        cm_name, data_key = parts[1], "/".join(parts[2:])
        for k, v in idx.items():
            if v[0] != "OK" or not k.endswith(":" + data_key):
                continue
            # the index key is "<file>#<i>:<data-key>" or "<file>:<data-key>"; confirm the ConfigMap NAME
            if v[1].get("__cm_name__") == cm_name:
                return (k, v[1])
        # name did not match any indexed ConfigMap: fall back to the data key alone, but only if it is
        # unambiguous -- two ConfigMaps with the same key is the collision this walk already refuses
        cands = [(k, v[1]) for k, v in idx.items() if v[0] == "OK" and k.endswith(":" + data_key)]
        return cands[0] if len(cands) == 1 else None
    base = os.path.basename(oas_path)
    cands = [(k, v[1]) for k, v in idx.items() if v[0] == "OK" and os.path.basename(k.split("#")[0]) == base]
    return cands[0] if len(cands) == 1 else None


def declared_query_params(doc, path, method):
    """Parameter names the document declares for one operation, merging the path item's own.

    Mirrors the provider's mergeParameters(pathItem.Parameters, operation.Parameters): a document that
    declares its parameters on the PATH ITEM is as valid as one that declares them per-operation, and a
    walk that reads only the operation reports a false "undeclared" for the former (#388 is the same bug
    in the provider).
    """
    names = set()
    item = (doc.get("paths") or {}).get(path)
    if not isinstance(item, dict):
        return None  # the path is not in this document at all
    for src in (item.get("parameters"), (item.get(method.lower()) or {}).get("parameters")):
        for p in src or []:
            if isinstance(p, dict) and p.get("name"):
                names.add(p["name"])
    return names


def main(argv):
    if "--oas-dir" not in argv:
        print(__doc__.strip(), file=sys.stderr)
        return 2
    i = argv.index("--oas-dir")
    oas_dir = argv[i + 1] if i + 1 < len(argv) else None
    targets = [a for j, a in enumerate(argv) if j not in (i, i + 1)]
    if not oas_dir or not os.path.isdir(oas_dir) or not targets:
        print("configuration-fields-walk: --oas-dir <dir> and at least one target are required",
              file=sys.stderr)
        return 2

    idx = oas_index(oas_dir)
    unparsed = [(k, v[1]) for k, v in idx.items() if v[0] == "UNPARSED"]

    files = []
    for t in targets:
        if os.path.isdir(t):
            for root, _, fs in os.walk(t):
                files += [os.path.join(root, f) for f in fs if f.lower().endswith((".yaml", ".yml"))]
        else:
            files.append(t)

    rds = 0
    entries_seen = 0
    findbys = 0
    i512, i828, unresolved, unparsed_rd = [], [], [], []

    for f in sorted(set(files)):
        try:
            docs = load_docs(f)
        except Exception as exc:
            unparsed_rd.append((f, type(exc).__name__))
            continue
        for d in docs:
            if d.get("kind") != "RestDefinition":
                continue
            rds += 1
            spec_oas = (d.get("spec") or {}).get("oasPath")
            res = (d.get("spec") or {}).get("resource") or {}
            verbs = res.get("verbsDescription") or []
            all_actions = [v.get("action") for v in verbs if isinstance(v, dict) and v.get("action")]
            findbys += sum(1 for a in all_actions if a == FINDBY)
            by_action = {v.get("action"): v for v in verbs if isinstance(v, dict)}
            name = d.get("metadata", {}).get("name", "?")

            for ci, cf in enumerate(res.get("configurationFields") or []):
                entries_seen += 1
                pname = (cf.get("fromOpenAPI") or {}).get("name")
                pin = (cf.get("fromOpenAPI") or {}).get("in")
                acts = (cf.get("fromRestDefinition") or {}).get("actions") or []
                # the generator expands "*" to every action the resource declares
                acts = all_actions if "*" in acts else acts

                if pname in VIEW_NARROWING and FINDBY in acts:
                    i512.append((f, name, ci, pname, pin, "via *" if "*" in
                                 ((cf.get("fromRestDefinition") or {}).get("actions") or []) else "explicit"))

                for a in acts:
                    v = by_action.get(a) or {}
                    vpath, vmethod = v.get("path"), v.get("method")
                    if not vpath or not vmethod:
                        continue
                    # THE DOCUMENT THIS ACTION IS SERVED BY -- the verb's own oasPath if it has one,
                    # else the resource's. This is what the provider does per action (#108), and
                    # guessing by first-path-match instead produced a measured false positive on a
                    # corpus carrying two versions of one API.
                    doc_hit = resolve_oas(v.get("oasPath") or spec_oas, idx)
                    if doc_hit is None:
                        unresolved.append((f, name, a, v.get("oasPath") or spec_oas or "<no oasPath>",
                                           "oasPath did not resolve to exactly one indexed document"))
                        continue
                    got = declared_query_params(doc_hit[1], vpath, vmethod)
                    if got is None:
                        unresolved.append((f, name, a, vpath,
                                           f"path absent from the document it names ({doc_hit[0]})"))
                        continue
                    if pname not in got:
                        i828.append((f, name, ci, pname, pin, a, doc_hit[0]))

    print(f"restdefinitions read      {rds}")
    print(f"configurationFields seen  {entries_seen}")
    print(f"oas documents indexed     {sum(1 for v in idx.values() if v[0] == 'OK')}")
    skipped = sum(1 for v in idx.values() if v[0] == "NOT-OAS")
    if skipped:
        print(f"files skipped, not OAS   {skipped}  (no openapi/swagger/paths key)")
    print()
    # WHAT THE WALK COULD HAVE FOUND, not only what it did. A corpus with no findby cannot trip #512
    # and a corpus with no configurationFields cannot trip #828, so a clean verdict over such a corpus
    # is UNTESTABLE rather than safe. A consumer refused a run of this walk for exactly that reason --
    # "an arm that cannot fail is worse than no arm, because it licenses the words 'tested on a second
    # corpus'" -- and a reader seeing `0  0` beside a clean verdict will not make the distinction unless
    # it is printed.
    print(f"#512 TESTABLE HERE: {'yes' if findbys else 'NO -- no findby verb in this corpus'}"
          f"   ({findbys} findby verb(s))")
    print(f"#828 TESTABLE HERE: {'yes' if entries_seen else 'NO -- no configurationFields entry'}")
    print()
    print(f"#512 instances (view-narrowing on a findby)  {len(i512)}")
    for f, n, ci, p, pin, how in i512:
        print(f"    {n}  configurationFields[{ci}]  {p} (in: {pin})  {how}   {f}")
    print(f"#828 instances (named but not declared)      {len(i828)}")
    for f, n, ci, p, pin, a, base in i828:
        print(f"    {n}  configurationFields[{ci}]  {p} (in: {pin})  action={a}  not in {base}   {f}")

    print()
    bad = False
    if unparsed:
        bad = True
        print(f"CONTROL FAILED: {len(unparsed)} oas document(s) would not parse")
        for k, why in unparsed:
            print(f"    {k}: {why}")
    if unparsed_rd:
        bad = True
        print(f"CONTROL FAILED: {len(unparsed_rd)} target file(s) would not parse")
        for k, why in unparsed_rd:
            print(f"    {k}: {why}")
    if unresolved:
        bad = True
        print(f"CONTROL FAILED: {len(unresolved)} operation(s) whose path is in NO indexed document")
        for f, n, a, p, m in unresolved[:10]:
            print(f"    {n}  action={a}  {p}   {m}   {f}")
        print("    -> a '#828 clean' verdict over unread documents is a false zero; fix --oas-dir")
    if entries_seen == 0:
        bad = True
        print("CONTROL FAILED: no configurationFields entry anywhere -- nothing was tested.")
        print("    -> 'no instances' here means the corpus has no configurationFields at all,")
        print("       which answers neither #512 nor #828.")
    if bad:
        print("\nVERDICT: VOID -- the result means nothing")
        return 2
    if i512 or i828:
        print("VERDICT: instances found -- decide the refusals from the counts above")
        return 1
    print("VERDICT: clean, and every control held")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
