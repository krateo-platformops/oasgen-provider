package oas2jsonschema

import (
	"fmt"
	"sort"
	"strings"
)

// warnRequiredParameterWithUnreachableDefault reports a parameter declared `required: true` whose only
// value source is an OAS `default` -- a combination that is accepted at generation today and then fails
// EVERY call.
//
// WHY IT CANNOT WORK. rest-dynamic-controller enforces `required` at request construction and never
// consults `default`:
//
//	for _, param := range mergeParameters(pathItem.Parameters, getDoc.Parameters) {
//	    if param.Required != nil && *param.Required {
//	        if param.In == "header" {
//	            if _, ok := headers[param.Name]; !ok && !suppliedAfterValidation(param.Name) {
//	                return fmt.Errorf("missing header: %s", param.Name)
//
// There is no `default` branch in any of the four locations. So an author who pins a constant the way
// OpenAPI invites -- `required` because the API demands the parameter, `default` because the value is
// fixed -- gets a RestDefinition that generates cleanly, reaches Ready, and fails every verb.
//
// AND THE DEFECT IS WIDER THAN IT WAS FILED AS (#305 describes it for header): the validator has the same
// shape for path, query, header and cookie, so a required QUERY parameter with a default fails identically.
//
// WHY NOT HONOUR THE DEFAULT INSTEAD, which is the other way to fix this. In a vendor's OpenAPI document a
// `default` DESCRIBES WHAT THE SERVER ASSUMES; it is not an instruction to the client. Transmitting it
// makes this controller assert a value the author never wrote. That is the 0.53.0 defect one type down --
// crdgen stamped `default: {}` on optional object parents, the apiserver materialised them, and the
// controller sent values nobody had asked for; 0.54.0 fixed it by setting NoParentDefaults. Honouring a
// scalar default at request construction would reinstate exactly that reasoning for scalars.
//
// NON-FATAL, and deliberately. A refusal would be defensible -- the kind cannot work -- but it would
// refuse a document that is legal OpenAPI, and this diagnostic has to prove it fires on real corpora
// before it is allowed to block an install.
func (g *OASSchemaGenerator) warnRequiredParameterWithUnreachableDefault() []error {
	type finding struct{ name, in, action string }
	var found []finding
	seen := map[string]bool{}

	for _, verb := range g.resourceConfig.Verbs {
		doc := g.docFor(verb.Action)
		if doc == nil {
			continue
		}
		path, ok := doc.FindPath(verb.Path)
		if !ok {
			continue
		}
		op, ok := path.GetOperations()[strings.ToLower(verb.Method)]
		if !ok {
			continue
		}
		// PER VERB, and that is the whole point: StaticHeaders and StaticQueries are per-verb fields, so
		// a header declared on `create` does nothing for `get`. A map built across all verbs suppressed
		// the `get` finding because `create` happened to declare it -- caught by a control that predicted
		// one finding would remain and measured zero.
		supplied := g.valueSourcesDeclaredFor(verb)

		for _, param := range op.GetParameters() {
			if !param.Required || param.Schema == nil || param.Schema.Default == nil {
				continue
			}
			// A SCALAR DEFAULT IS NOW HONOURED at request construction (#835), so the kind works and
			// there is nothing to report. Only a default this controller CANNOT place in a path, query,
			// header or cookie still fails every call -- an object or an array, which would need a
			// serialisation the vendor never specified.
			if isScalarDefault(param.Schema.Default) {
				continue
			}
			if supplied[parameterKey(param.Name, param.In)] {
				continue
			}
			k := parameterKey(param.Name, param.In) + "|" + verb.Action
			if seen[k] {
				continue
			}
			seen[k] = true
			found = append(found, finding{param.Name, param.In, verb.Action})
		}
	}
	if len(found) == 0 {
		return nil
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].name != found[j].name {
			return found[i].name < found[j].name
		}
		return found[i].action < found[j].action
	})

	out := make([]error, 0, len(found))
	for _, f := range found {
		out = append(out, fmt.Errorf(
			"parameter %q (in: %q) on action %q is declared required with an OAS default and nothing else "+
				"supplies it, and that default is not a scalar: it cannot be placed in a %s without inventing "+
				"a serialisation the vendor never specified, so every %q call will fail with "+
				"\"missing %s: %s\". A scalar default IS sent automatically; this one cannot be. Declare "+
				"the value on the verb instead -- verbsDescription[].headers for a header, or a "+
				"requestFieldMapping entry",
			f.name, f.in, f.action, f.in, f.action, f.in, f.name))
	}
	return out
}

// valueSourcesDeclaredFor is every parameter THIS VERB supplies a value for, so a parameter that is wired
// up is not reported.
//
// SCOPED TO ONE VERB because StaticHeaders, StaticQueries and FieldMapping are per-verb declarations. The
// first version of this built one map across every verb, and a control caught it: declaring the header on
// `create` silently suppressed the finding for `get`, which is the opposite of the truth -- a per-verb
// header does nothing for another verb.
//
// configurationFields is resource-level and so applies to every verb, which is why it is read from the
// resource config rather than the verb.
func (g *OASSchemaGenerator) valueSourcesDeclaredFor(verb Verb) map[string]bool {
	supplied := map[string]bool{}
	{
		for _, h := range verb.StaticHeaders {
			supplied[parameterKey(h.Name, "header")] = true
		}
		for _, q := range verb.StaticQueries {
			supplied[parameterKey(q.Name, "query")] = true
		}
		// FieldMapping names its destination EXPLICITLY, so each entry suppresses exactly one location.
		// A blanket loop over all four would suppress a genuinely unreachable header because the author
		// mapped a path parameter of the same name -- an over-broad suppression hides real findings,
		// which is worse here than a false one, because a missed finding fails every call silently.
		for _, m := range verb.FieldMapping {
			if m.InPath != "" {
				supplied[parameterKey(m.InPath, "path")] = true
			}
			if m.InQuery != "" {
				supplied[parameterKey(m.InQuery, "query")] = true
			}
		}
	}
	for _, f := range g.resourceConfig.ConfigurationFields {
		supplied[parameterKey(f.FromOpenAPI.Name, f.FromOpenAPI.In)] = true
	}
	return supplied
}

// isScalarDefault reports whether a default is a single value this controller can place in a path, query,
// header or cookie -- which is exactly the set #835 sends automatically.
//
// Anything composite is not reportable as "sent"; it is reportable as a kind that cannot work.
func isScalarDefault(def interface{}) bool {
	switch def.(type) {
	case nil:
		return false
	case map[string]interface{}, []interface{}, map[interface{}]interface{}:
		return false
	default:
		return true
	}
}
