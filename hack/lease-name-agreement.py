#!/usr/bin/env python3
"""lease-name-agreement.py — the Go Lease name and the generated Role's grant must be the same string.

    python3 -I hack/lease-name-agreement.py

WHY THIS IS A SCRIPT AND NOT ONLY A GO TEST, which is the part worth reading.

A generated controller computes its Lease name in Go and is PERMITTED to touch that Lease by a
`resourceNames` entry in a YAML template. If they disagree, every generated controller fails to acquire
and controller-runtime EXITS -- N kinds, not one provider.

There IS a Go test for this (go/rest-dynamic-controller/lease_name_agreement_test.go) and it works. But
the YAML lives OUTSIDE the Go module, so it is not a tracked input of that package -- and `go test`
CACHES the result. Measured: reverse the template's order, re-run without `-count=1`, and the stale PASS
is served:

    $ go test -tags=unit -run TestTheLeaseNameMatchesTheGeneratedRole .
    ok   github.com/krateo-platformops/rest-dynamic-controller   (cached)

    $ go test -count=1 -tags=unit -run TestTheLeaseNameMatchesTheGeneratedRole .
    --- FAIL  the Go Lease name and the Role's grant disagree:
                go:   RES-GRP-VER
                role: VER-GRP-RES

So the Go test is correct and its RESULT is cacheable against the exact change it exists to catch. A gate
that depends on the invoker remembering a flag is not a gate, and "run it with -count=1" is a note nobody
reads twice. This script is outside the Go cache entirely.

EXIT STATUS
  0  the two sides agree, and the controls held
  1  they disagree -- every generated controller would fail to acquire its Lease
  2  THE RESULT MEANS NOTHING: a control failed, or a file could not be read or parsed
"""
import os
import re
import sys

ROLE = os.path.join("helm", "oasgen-provider", "assets", "rdc", "rbac", "role.yaml")
MAIN = os.path.join("go", "rest-dynamic-controller", "main.go")

# The provider-side actions the Role template uses, mapped to sentinels. The comparison is about ORDER
# and SEPARATORS, so the sentinels are deliberately not real values.
SENTINELS = {"{{ .resource }}": "RES", "{{ .apiGroup }}": "GRP", "{{ .apiVersion }}": "VER"}


def unescape_helm(text):
    """Stage 1 is helm's: `{{`{{ .resource }}`}}` renders to `{{ .resource }}`."""
    return text.replace("{{`", "").replace("`}}", "")


def role_grant_and_name(text):
    provider = unescape_helm(text)
    grant = re.search(r"(?m)^\s*resourceNames:\s*\n\s*-\s*(\S.*?)\s*$", provider)
    name = re.search(r"(?m)^\s*name:\s*(\S.*?)\s*$", provider)
    return (grant.group(1) if grant else None), (name.group(1) if name else None)


def substitute(template):
    out = template
    for action, sentinel in SENTINELS.items():
        out = out.replace(action, sentinel)
    return out


def go_lease_name(text):
    """The format string and argument order of generatedControllerLeaseName, read from the source."""
    fn = re.search(
        r"func generatedControllerLeaseName\(([^)]*)\)\s*string\s*\{\s*\n\s*return fmt\.Sprintf\("
        r"\"([^\"]*)\",\s*([^)]*)\)",
        text,
    )
    if not fn:
        return None, "generatedControllerLeaseName not found, or its shape changed"
    params = [p.strip().split()[0] for p in fn.group(1).split(",") if p.strip()]
    fmtstr, args = fn.group(2), [a.strip() for a in fn.group(3).split(",") if a.strip()]
    if fmtstr.count("%s") != len(args):
        return None, f"{fmtstr!r} has {fmtstr.count('%s')} verbs for {len(args)} arguments"
    # Map each parameter name to the sentinel the Role uses for the same concept.
    by_param = {"resource": "RES", "apiGroup": "GRP", "apiVersion": "VER"}
    if sorted(params) != sorted(by_param):
        return None, f"parameters {params} are not the three the Role templates ({sorted(by_param)})"
    try:
        return fmtstr % tuple(by_param[a] for a in args), None
    except KeyError as e:
        return None, f"argument {e} is not a known parameter"


def main():
    # ---- CONTROLS FIRST. Each is a different way this script could report agreement without checking.
    g, n = role_grant_and_name(
        "metadata:\n  name: {{`{{ .resource }}`}}-x\nrules:\n  - apiGroups: [\"coordination.k8s.io\"]\n"
        "    resourceNames:\n      - {{`{{ .resource }}`}}-x\n"
    )
    if g != "{{ .resource }}-x" or n != "{{ .resource }}-x":
        print(f"CONTROLS FAILED: planted role did not parse: grant={g!r} name={n!r}", file=sys.stderr)
        return 2
    if substitute("{{ .resource }}-{{ .apiGroup }}-{{ .apiVersion }}") != "RES-GRP-VER":
        print("CONTROLS FAILED: substitution does not produce the sentinels", file=sys.stderr)
        return 2
    planted_go, err = go_lease_name(
        'func generatedControllerLeaseName(resource, apiGroup, apiVersion string) string {\n'
        '\treturn fmt.Sprintf("%s-%s-%s", resource, apiGroup, apiVersion)\n}'
    )
    if planted_go != "RES-GRP-VER" or err:
        print(f"CONTROLS FAILED: planted Go parsed as {planted_go!r} ({err})", file=sys.stderr)
        return 2
    reversed_go, err = go_lease_name(
        'func generatedControllerLeaseName(resource, apiGroup, apiVersion string) string {\n'
        '\treturn fmt.Sprintf("%s-%s-%s", apiVersion, apiGroup, resource)\n}'
    )
    if reversed_go != "VER-GRP-RES" or err:
        print(f"CONTROLS FAILED: a reversed Go order parsed as {reversed_go!r}, so argument order is "
              f"not being read ({err})", file=sys.stderr)
        return 2
    print("CONTROLS HELD: role parsed, sentinels substituted, Go argument ORDER read (a reversal differs)")

    for p in (ROLE, MAIN):
        if not os.path.isfile(p):
            print(f"RESULT MEANS NOTHING: {p} not found. Run from the repository root.", file=sys.stderr)
            return 2

    with open(ROLE, "r", encoding="utf-8") as f:
        role_text = f.read()
    with open(MAIN, "r", encoding="utf-8") as f:
        main_text = f.read()

    grant, name = role_grant_and_name(role_text)
    if grant is None:
        print("FAIL: no `resourceNames:` entry in the generated Role. The Lease grant must be pinned to "
              "this controller's own Lease: a blanket leases grant would let any generated controller "
              "take any other's.", file=sys.stderr)
        return 1
    if name is None:
        print("RESULT MEANS NOTHING: no `name:` found in the Role", file=sys.stderr)
        return 2
    if grant != name:
        print(f"FAIL: the Lease grant and the Role's own name differ:\n  grant: {grant}\n  name:  {name}",
              file=sys.stderr)
        return 1

    expected = substitute(grant)
    if "{{" in expected:
        print(f"RESULT MEANS NOTHING: the grant carries an action this script cannot substitute: "
              f"{expected!r}. Teach it the action rather than loosening the comparison.", file=sys.stderr)
        return 2

    got, err = go_lease_name(main_text)
    if err:
        print(f"RESULT MEANS NOTHING: {err}", file=sys.stderr)
        return 2

    print(f"  role grant : {grant}  ->  {expected}")
    print(f"  go function: generatedControllerLeaseName  ->  {got}")
    if got != expected:
        print(f"\nFAIL: the Go Lease name and the Role's grant disagree.\n  go:   {got}\n  role: {expected}\n"
              f"Every generated controller would fail to acquire its Lease and EXIT.", file=sys.stderr)
        return 1
    print("\nAGREE: one convention, two sides, compared from their own text rather than from a copy.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
