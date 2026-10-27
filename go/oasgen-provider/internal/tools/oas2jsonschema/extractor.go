package oas2jsonschema

import (
	"fmt"
	"strings"
)

// findParameterForAction resolves field's parameter against ONE action, in the document THAT action reads.
//
// The per-action form exists because a configuration field scoped to several actions can now span two
// documents (#108), and those documents can describe the same parameter differently. Aruba's CloudServer
// is the live case: `api-version` is declared by both compute documents, required with default "1.1" in
// the 1.1 document that serves create and optional with default "1" in the 1.0 document that serves
// everything else. One resolution cannot be right for both.
func (g *OASSchemaGenerator) findParameterForAction(field ConfigurationField, action string) (*ParameterInfo, error) {
	for _, verb := range g.resourceConfig.Verbs {
		if !strings.EqualFold(verb.Action, action) {
			continue
		}
		path, ok := g.docFor(verb.Action).FindPath(verb.Path)
		if !ok {
			continue
		}
		ops := path.GetOperations()
		op, ok := ops[strings.ToLower(verb.Method)]
		if !ok {
			continue
		}
		for _, param := range op.GetParameters() {
			if param.Name == field.FromOpenAPI.Name && param.In == field.FromOpenAPI.In {
				return &param, nil
			}
		}
	}
	return nil, fmt.Errorf("parameter '%s' in '%s' not found for action '%s'", field.FromOpenAPI.Name, field.FromOpenAPI.In, action)
}

// findParameterInOAS resolves field's parameter against the FIRST of its actions that declares it.
//
// Implemented on top of findParameterForAction so there is one definition of "resolve this field against
// this action" rather than two that can drift. It stays because the configuration builder still needs a
// field-level answer: whether the field resolves anywhere at all, and which parameter location (query,
// header, ...) its bucket belongs under.
func (g *OASSchemaGenerator) findParameterInOAS(field ConfigurationField) (*ParameterInfo, error) {
	for _, action := range field.FromRestDefinition.Actions {
		if param, err := g.findParameterForAction(field, action); err == nil {
			return param, nil
		}
	}
	return nil, fmt.Errorf("parameter '%s' in '%s' not found for any of the specified actions", field.FromOpenAPI.Name, field.FromOpenAPI.In)
}

// getBaseSchemaForSpec returns the base schema for the spec: the request body of the verb that DEFINES
// the resource.
//
// Precedence is explicit -- create, then dispatch, then update -- rather than whichever happens to be
// declared first. That distinction is the whole reason this is not a single loop with a widened
// condition: a resource declaring update before create in its verbsDescription would otherwise derive
// its spec from the update body, silently, on the strength of YAML ordering.
//
// The update fallback exists because a document with no create endpoint is not necessarily a read-only
// resource (#236). Aruba's CMP profile declares PUT/GET/DELETE for aiKeys and no POST at all; treating
// that as read-only gave it a spec of nothing but parameters and identifiers, so the declared update
// PUT a body missing every vendor field it was supposed to carry -- and generation reported nothing,
// because each half was behaving as written.
//
// The fallback is taken only when NO create or dispatch verb is DECLARED. A create verb that is
// declared but carries no body in an accepted MIME type keeps its existing behaviour (an empty base),
// because borrowing the update body there would inject fields into the spec of a resource that defines
// itself differently.
func (g *OASSchemaGenerator) getBaseSchemaForSpec() (*Schema, error) {
	schema, err := g.requestBodySchemaFor(ActionCreate, ActionDispatch)
	if err != nil {
		return nil, err
	}
	if schema != nil {
		return schema, nil
	}

	// Declared but bodyless: not the #236 case, so do not fall back.
	if g.declaresAnyAction(ActionCreate, ActionDispatch) {
		return &Schema{}, nil
	}

	schema, err = g.requestBodySchemaFor(ActionUpdate)
	if err != nil {
		return nil, err
	}
	if schema != nil {
		return schema, nil
	}
	return &Schema{}, nil
}

// declaresAnyAction reports whether the resource declares a verb for any of the given actions.
func (g *OASSchemaGenerator) declaresAnyAction(actions ...string) bool {
	for _, verb := range g.resourceConfig.Verbs {
		for _, action := range actions {
			if strings.EqualFold(verb.Action, action) {
				return true
			}
		}
	}
	return false
}

// requestBodySchemaFor returns the request body schema of the first of the given actions that declares
// one, trying the actions IN THE ORDER GIVEN rather than in verb-declaration order. A nil schema with a
// nil error means none of them contributed a body.
func (g *OASSchemaGenerator) requestBodySchemaFor(actions ...string) (*Schema, error) {
	for _, action := range actions {
		for _, verb := range g.resourceConfig.Verbs {
			if !strings.EqualFold(verb.Action, action) {
				continue
			}
			path, ok := g.docFor(verb.Action).FindPath(verb.Path)
			if !ok {
				return nil, fmt.Errorf("path '%s' not found in OpenAPI spec", verb.Path)
			}
			ops := path.GetOperations()
			op, ok := ops[strings.ToLower(verb.Method)]
			if !ok {
				return nil, fmt.Errorf("operation '%s' not found for path '%s'", verb.Method, verb.Path)
			}

			rb := op.GetRequestBody()
			for _, mimeType := range g.generatorConfig.AcceptedMIMETypes {
				if schema, ok := rb.Content[mimeType]; ok {
					if getPrimaryType(schema.Type) == "array" {
						schema.Properties = append(schema.Properties, Property{Name: "items", Schema: &Schema{Type: []string{"array"}, Items: schema.Items}})
						schema.Type = []string{"object"}
					}
					return schema.deepCopy(), nil
				}
			}
		}
	}
	return nil, nil
}

// getBaseSchemaForStatus returns the base schema for the status, which is the response body of the 'get' or 'findby' action.
// TODO: what about no get/findby action but only update? (maybe this could be configured in the GeneratorConfig)
func (g *OASSchemaGenerator) getBaseSchemaForStatus() (*Schema, error) {
	// Try each observe action in turn and only fail if NONE yields a schema. Returning the first action's
	// error aborted before findby was ever consulted, so a resource declaring findby but no get -- the
	// normal shape for a read-only resource -- lost its status schema entirely to
	// "action 'get' not defined in resource verbs", even though findby could have supplied it (#75).
	var firstErr error
	actions := []string{ActionGet, ActionFindBy}
	for _, action := range actions {
		schema, err := ExtractSchemaForAction(g.docs, g.resourceConfig.Verbs, action, g.generatorConfig)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if schema != nil {
			return schema, nil
		}
	}

	// A dispatch resource observes through its correlation collection and has no get or findby verb to
	// consult, so it is tried LAST and only once the ordinary sources have produced nothing. Last rather
	// than first because a dispatch verb may legally coexist with a get or findby (CEL forbids only
	// create and update), and where one exists it is the better status source: it describes the resource,
	// while the collection item describes an invocation of it.
	//
	// Its error REPLACES firstErr. A dispatch resource's get/findby failure is "no such verb", which is
	// not a defect for this kind and would bury the real problem -- a mistyped collection path.
	if schema, err := g.correlationItemSchema(); err != nil {
		return nil, err
	} else if schema != nil {
		return schema, nil
	}

	if firstErr != nil {
		return nil, firstErr
	}
	return nil, nil
}

// ExtractSchemaForAction resolves targetAction's path against the document that action belongs to.
//
// docs.For(targetAction) is the ONLY place in the codebase where a verb meets a document, which is why
// per-verb oasPath (#108) lands here and nowhere else. While that feature is unimplemented the set holds
// one document and For always returns it, so this is the same lookup it has always been.
func ExtractSchemaForAction(docs *DocumentSet, verbs []Verb, targetAction string, config *GeneratorConfig) (*Schema, error) {
	doc := docs.For(targetAction)
	var verbFound bool
	for _, verb := range verbs {
		if !strings.EqualFold(verb.Action, targetAction) {
			continue
		}
		verbFound = true

		//log.Printf("Verb matched for action '%s': %s %s", targetAction, verb.Method, verb.Path)

		path, ok := doc.FindPath(verb.Path)
		if !ok {
			return nil, fmt.Errorf("path '%s' not found in OAS document", verb.Path)
		}

		ops := path.GetOperations()
		op, ok := ops[strings.ToLower(verb.Method)]
		if !ok {
			return nil, fmt.Errorf("method '%s' not found for path '%s'", verb.Method, verb.Path)
		}

		responses := op.GetResponses()
		if responses == nil {
			// log.Printf("No responses defined for action '%s' in verb %s %s", targetAction, verb.Method, verb.Path)
			continue // Or return an error if responses are expected
		}

		for _, code := range config.SuccessCodes {
			resp, ok := responses[code]
			if !ok {
				continue
			}

			for _, mimeType := range config.AcceptedMIMETypes {
				schema, ok := resp.Content[mimeType]
				if !ok || schema == nil {
					continue
				}

				// For 'findby' we want ONE ITEM of the collection, not the response envelope.
				//
				// This used to unwrap only when the 200 schema was itself an array, so an envelope --
				// {total, values:[...]}, which is what most real APIs return -- left the envelope as the
				// base schema. Everything downstream then looked for identifiers that were a level deeper
				// than it was searching: their types degraded to string, and the not-resolvable warning
				// fired identically whether the identifier was valid or genuinely absent, which made it
				// useless for the one job it exists to do (#110).
				if strings.EqualFold(targetAction, ActionFindBy) {
					return unwrapFindByItems(schema, verb.ItemsPath)
				}
				// For other actions, we return the schema as is
				return schema.deepCopy(), nil
			}
		}
	}

	if !verbFound {
		return nil, fmt.Errorf("action '%s' not defined in resource verbs", targetAction)
	}

	return nil, fmt.Errorf("no suitable response schema found for action '%s'", targetAction)
}
