package oas2jsonschema

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/pathparsing"
)

// warnDottedStatusFieldShadowsParameter reports a DOTTED additionalStatusFields or identifiers entry
// whose FIRST segment names a request parameter that no request-direction fieldMapping supplies on that
// verb (#463).
//
// WHY THIS IS A DEFECT AND NOT A STYLE NOTE. A dotted entry such as `owner.login` renders the status
// schema NESTED -- `owner` becomes an object with a `login` property -- so the top-level status key is
// `owner`. rest-dynamic-controller fills a request parameter by matching a TOP-LEVEL field name against
// the verb's parameters, and the value it hands over is therefore the whole map. The destination is
// map[string]string, so the map is rendered with fmt.Sprintf("%v") and the request carries the literal
// string `map[login:octocat]`.
//
// That is not a malformed URL. It is a syntactically valid one addressing something that does not exist,
// so the vendor answers 404 and the reconciler acts on not-found by CREATING -- the #117 shape.
//
// WHY THE CHECK IS PER-VERB. mappingSuppliedParams exists and would be the obvious thing to consult, but
// it is computed once across ALL verbs into one flat map (#457). Using it would exempt a verb that has no
// mapping because a DIFFERENT verb has one -- and that is precisely the reachable case: a kind whose
// `get` maps the parameter and whose `update` does not still sends the map on `update`. So each verb is
// asked about its own mappings here. This duplicates a little of mappingSuppliedParams' logic
// deliberately; the alternative is to make that function verb-aware, which is #457's job and a
// behaviour change for its existing callers.
//
// WHY A REQUIRED SPEC PROPERTY IS SKIPPED. Spec wins over status -- processFields writes only where the
// key is absent ("Avoid overwriting existing values") -- so a parameter the spec is GUARANTEED to carry
// can never take the status value. A required property of the generated spec is exactly that guarantee:
// the apiserver rejects a CR without it.
//
// AN EARLIER VERSION OF THIS COMMENT CLAIMED MORE THAN THAT AND WAS WRONG. It said an OAS path parameter
// must declare `required: true`, therefore always becomes a required spec property, therefore cannot
// reach this at all. The github-provider-kog session falsified it with a generated CRD: their
// UserRepository has `owner` as a required path parameter on get and update, and the spec has no `owner`
// key whatsoever. A parameter a request-direction fieldMapping supplies is omitted from the spec
// ENTIRELY ("rather than merely un-required"), so it is not in `required` and the guarantee does not
// apply.
//
// The behaviour is still right, by a different route, and it is worth naming both branches because only
// one of them is this check's own:
//
//   - an UNMAPPED parameter is added to the spec by addParametersToSpec, carrying its OAS requiredness,
//     so a required one lands in `required` and the guarantee above applies;
//   - a MAPPED parameter is absent from the spec, so this guard does not catch it -- the per-verb
//     mapping check below does.
//
// The distinction that makes both true at once, in the words of the session that measured it: the base
// body does come from create, but PARAMETERS MERGE FROM EVERY VERB. So a path parameter declared only on
// `get` is still a required spec property, which is why
// TestAnUnmappedPathParameterOnANonCreateVerbIsRequiredInTheGeneratedSpec passes.
//
// TWO SHAPES ARE THEREFORE REACHABLE, and the second is the reason this check is per-verb:
//
//  1. an OPTIONAL query, header or cookie parameter with a dotted status field and no mapping on that
//     verb -- the author simply leaves the field unset and status fills it;
//  2. a parameter mapped on SOME verb, hence absent from the spec entirely, but NOT on the verb being
//     built. A path parameter included. A kind that maps `owner` on `get` and omits it on `update` has
//     `owner` missing from the spec globally and unsupplied on `update`.
//
// Shape 2 is a per-verb asymmetry rather than a per-kind property, which is exactly what a per-kind
// screen reports as clean. No claim is made here beyond those two; an earlier version of this comment
// reasoned from requiredness to a boundary and got it wrong, so the arms in the test file carry the
// claim instead.
//
// The GENERATED spec JSON is the input rather than a re-derivation, and rather than the intermediate
// *Schema: it is the artifact that ships in the CRD and the thing the apiserver will actually enforce,
// after exclusions, mappings and identifier re-adding have all had their say. Reading the artifact is
// the only way this cannot drift from what is enforced.
func (g *OASSchemaGenerator) warnDottedStatusFieldShadowsParameter(specSchemaJSON []byte) []error {
	if g.resourceConfig == nil {
		return nil
	}

	// First segment -> the declared entry, so the message can quote what the author wrote. First
	// declaration wins, matching mappingSuppliedParams' own first-seen rule, so the message is stable
	// across renders rather than depending on map iteration order.
	shadowed := map[string]string{}
	for _, f := range statusFieldsFor(g.resourceConfig) {
		segments, err := pathparsing.ParsePath(f)
		if err != nil || len(segments) < 2 {
			// A parse failure is reported by composeStatusSchema; a single-segment entry renders a leaf
			// and cannot shadow anything.
			continue
		}
		if _, seen := shadowed[segments[0]]; !seen {
			shadowed[segments[0]] = f
		}
	}
	if len(shadowed) == 0 {
		return nil
	}

	// Properties the generated spec REQUIRES, which the apiserver then guarantees are present. Read out
	// of the generated JSON; an unreadable schema yields an empty set, which makes this warning MORE
	// talkative rather than silent -- the safe direction for an advisory check whose companion is a hard
	// refusal at request-build time.
	specRequired := map[string]bool{}
	if len(specSchemaJSON) > 0 {
		var parsed struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(specSchemaJSON, &parsed); err == nil {
			for _, name := range parsed.Required {
				specRequired[name] = true
			}
		}
	}

	var warnings []error
	for _, verb := range g.resourceConfig.Verbs {
		supplied := verbSuppliedParams(verb)

		verbDoc := g.docFor(verb.Action)
		path, ok := verbDoc.FindPath(verb.Path)
		if !ok {
			// The missing path is its own warning, from the spec builder. Do not pile on.
			continue
		}
		op, ok := path.GetOperations()[strings.ToLower(verb.Method)]
		if !ok {
			continue
		}

		for _, param := range op.GetParameters() {
			if isAuthorizationHeader(param) {
				continue
			}
			// Only the four string-valued destinations. A body property legitimately takes a composite,
			// which is why the request-build guard skips the body too.
			switch strings.ToLower(param.In) {
			case "path", "query", "header", "cookie":
			default:
				continue
			}
			entry, collides := shadowed[param.Name]
			if !collides || supplied[param.Name] || specRequired[param.Name] {
				continue
			}
			warnings = append(warnings, SchemaGenerationError{
				Path: g.statusFieldPath(entry),
				Code: CodeStatusFieldShadowsParameter,
				Message: fmt.Sprintf("status field '%s' renders status.%s as an OBJECT, and '%s' is also a %s "+
					"parameter of the '%s' verb that no request-direction fieldMapping supplies: the request "+
					"would carry the rendered object (like 'map[...]') instead of a value. Either add a "+
					"request-direction fieldMapping supplying '%s' from the leaf, or rename the status field "+
					"so its first segment does not collide with the parameter",
					entry, param.Name, param.Name, strings.ToLower(param.In), verb.Action, param.Name),
			})
		}
	}
	return warnings
}

// verbSuppliedParams returns the parameter names THIS verb's own request-direction fieldMapping entries
// supply. Deliberately per-verb; see the comment above for why mappingSuppliedParams cannot be used.
func verbSuppliedParams(verb Verb) map[string]bool {
	supplied := map[string]bool{}
	for _, m := range verb.FieldMapping {
		// An entry with no source in the CR is a response-direction mapping or malformed; neither
		// supplies a request parameter. Same test mappingSuppliedParams makes.
		if m.InCustomResource == "" {
			continue
		}
		// InPath and InQuery are the only request destinations a mapping can write -- the entry type
		// offers inPath, inQuery and inBody and nothing else. So a HEADER or COOKIE collision can never
		// be exempted by a mapping, and will always be reported here. That is correct rather than
		// unfortunate: nothing downstream can rescue it either.
		if m.InPath != "" {
			supplied[m.InPath] = true
		}
		if m.InQuery != "" {
			supplied[m.InQuery] = true
		}
	}
	return supplied
}
