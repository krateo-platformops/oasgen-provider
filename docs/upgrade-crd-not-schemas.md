---
type: Runbook
title: The `not` keyword in generated CRDs changes with plumbing 1.60.0
description: What the plumbing v1.33.0 to v1.60.0 bump changes about `not` subschemas in CRDs generated from an OpenAPI document, what it leaves alone, how a dropped constraint surfaces, and the census that found zero affected documents across six consumer corpora.
resource: oci://ghcr.io/krateo-platformops/charts/oasgen-provider
tags: [kog, upgrade, crd, generation]
timestamp: 2026-11-12T00:00:00Z
---

# The `not` keyword in generated CRDs changes with plumbing 1.60.0

**No operator action is required for any document we have seen, and that is measured rather than expected.**
A census of six consumer corpora — 96 OpenAPI documents — found zero documents whose generated CRD changes.
But the change is **not hypothetical elsewhere**: plumbing's own note for this work (plumbing#102) names
`agentgateway-policies` 0.2.20, whose `not: {pattern: ...}` on four sampling fields was generated unenforced
on 057. If a document of yours has a `not` carrying anything other than `const` or `enum`, read on.

Nothing in this repository can catch this change. `plumbing/crdgen` is imported at exactly one place —
`internal/tools/render/render.go` — and it generates **consumers' CRDs at runtime** from their OpenAPI
documents. CI's `CRDs drift guard` covers the hand-written `RestDefinition` CRD, which comes from
`controller-gen` and is untouched here. There is no in-repo artifact whose change would have signalled this,
which is why it was censused against real documents instead.

---

## 1. What changed

One function: `(*transpiler).emitNot` in `crdgen/transpile_keywords.go`. It exists under that name at both
versions, and **none of the machinery it now uses is new**.

At **v1.33.0** it hand-built its subschema from the forbidden values alone, and dropped the constraint
outright if there were none:

```go
values := n.Enum
if n.Const != nil { values = []any{n.Const} }
if len(values) == 0 {
    t.warn(path, KindConstraintDropped, "not (non-trivial) unsupported -> dropped")
    return
}
// ...
out.Not = &apiextensionsv1.JSONSchemaProps{Enum: forbidden}
```

At **v1.60.0** it calls the shared junctor builder that every other junctor member already used, adds
`format`, then appends the forbidden values:

```go
sub := junctorMemberSchema(n)
sub.Description = ""
sub.Format = n.Format
// ... append const/enum to sub.Enum ...
if isVacuousJunctorMember(*sub) {
    t.warn(path, KindConstraintDropped, "not (non-trivial) unsupported -> dropped")
    return
}
out.Not = sub
```

`junctorMemberSchema` (`crdgen/junctor.go:84`), `isVacuousJunctorMember` (`junctor.go:106`) and
`copyScalarValidation` (`transpile.go:594`) **all exist at v1.33.0 too**. So this is not new capability: it
is a hand-built literal that bypassed the shared builder, now using it. What a `not` subschema can express
did not change; what `emitNot` bothered to put in one did.

### Which keywords now reach a `not`

| Keyword | v1.33.0 | v1.60.0 | via |
|---|---|---|---|
| `const`, `enum` | yes | yes | `emitNot` directly |
| `pattern` | no | yes | `copyScalarValidation` |
| `minimum`, `maximum`, `multipleOf` | no | yes | `copyScalarValidation` |
| `minLength`, `maxLength` | no | yes, only when `> 0` | `copyScalarValidation` |
| `format` | no | yes | `emitNot` call site |
| `required` | no | yes | `junctorMemberSchema` |
| `exclusiveMinimum`, `exclusiveMaximum` | no | **no** | emitted nowhere by this transpiler |

### The consequence, concretely

A document containing:

```yaml
not:
  type: string
  pattern: "^prod-"
```

generated **no constraint at all** at v1.33.0: there is no `const` or `enum`, so `len(values) == 0` and the
whole `not` was dropped. At v1.60.0 it generates:

```yaml
not:
  pattern: "^prod-"
```

which the apiserver enforces. **A field value of `prod-eu` that was accepted before is now rejected at
admission** — on the next write to the CR, not at generation time, so a CRD that generates cleanly can still
reject objects that already exist.

## 2. What did NOT change

- **Which nodes produce a `not` at all.** The gate is `isScalarOnly`, and its body is byte-identical at
  v1.33.0 (line 342) and v1.56.0 (line 355), re-checked at v1.60.0 — verified by diffing the two, with a control proving the diff
  detects an altered copy:

  ```go
  func isScalarOnly(n *schemas.Type) bool {
      return len(n.Properties) == 0 && n.Items == nil && len(n.AllOf) == 0 && len(n.AnyOf) == 0 &&
          len(n.OneOf) == 0 && n.Ref == "" && n.AdditionalProperties == nil && n.If == nil && n.Not == nil
  }
  ```

  A `not` on an object, an array or an untyped node is still dropped, and a `not` whose subschema is
  non-trivial is still dropped.
- **A `not` built only from `exclusiveMinimum` or `exclusiveMaximum`.** Those are emitted nowhere, so the
  subschema is still vacuous and still dropped — now via `isVacuousJunctorMember`, which exists to stop
  `not: {}` reaching a CRD, because an empty `not` matches everything and therefore rejects every value.
- **Any document with no `not`.** This is every document in the census.

## 3. A dropped constraint is visible, and that was worth checking

> **Correction (2026-11-13): this section is true of drops CRDGEN makes, and does not apply to a
> `not` written in an OpenAPI document.** Such a `not` never reaches crdgen — `oas2jsonschema`'s
> `Schema` type has no `Not` field, so the keyword is discarded in the OAS→schema step. There is no
> drop site, so nothing calls `t.warn`, so `status.schemaWarnings` stays empty. A reader who checked
> `schemaWarnings` after a `not` vanished would find nothing and conclude the constraint had
> survived. Measured on released binaries; open as **#396**.
>
> The warning plumbing itself works and is worth keeping in mind for every other constraint:
> `render.go:180` and `:221` do call `crdgen.GenerateWithWarnings`.

Every drop above calls `t.warn(...)`. Those warnings are not swallowed: `render.go` calls
`crdgen.GenerateWithWarnings`, adapts them onto `GenerationWarnings`, and the controller writes them to
`status.schemaWarnings` on the RestDefinition and raises a `SchemaWarnings` Warning Event
(`internal/controllers/restdefinition/restdefinition.go:1306-1308`).

So if the bump drops a `not` of yours, you can see it:

```bash
kubectl get restdefinition <name> -n <ns> -o jsonpath='{.status.schemaWarnings}'
```

## 4. The census, and why its negative is trustworthy

Six corpora, 96 documents, zero affected. The instrument matters more than the number, because a
clean-looking zero is this sweep's natural failure mode:

- The predicate was written **from plumbing's source** — `isScalarOnly` and `copyScalarValidation` — not from
  a description of the behaviour. An earlier version written from a description gave the wrong answer and had
  to be withdrawn after it had already been circulated.
- A document that cannot be parsed is **counted and reported**, and the run exits non-zero. An earlier version
  printed `TOTAL not nodes: 0` for unparseable input — the identical string it prints for a genuinely clean
  corpus, making a broken instrument and a clean result indistinguishable.
- A `not` whose enclosing schema declares no `type` is reported `UNKNOWN` plus `INCOMPLETE` with exit 2,
  rather than being silently counted either way.

## 5. What to do if you have a `not` carrying more than const/enum

1. Before upgrading, generate the CRD and read the `not` node. Anything beyond `enum` in it is newly
   enforced.
2. Check existing CRs against the new constraint. The rejection happens on the next write, so nothing fails
   at upgrade time.
3. Read `status.schemaWarnings` after the upgrade. A `not` that is still being dropped says so there.
