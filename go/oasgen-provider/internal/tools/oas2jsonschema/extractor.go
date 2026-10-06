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

// getBaseSchemaForSpec returns the base schema for the spec, which is the request body of the 'create' action.
// TODO: what about no create action but only update? (maybe this could be configured in the GeneratorConfig)
//
// Resolved against the CREATE verb's own document, which is the whole of #108's motivating case: Aruba's
// CloudServer has findby/get/delete in compute-provider.json (1.0.0) and create alone in
// compute-provider_v1.1.json (1.1.0), where the 1.1 document holds exactly that one path. Reading the
// default document here meant the create body -- the CRD's entire spec -- came from the document that
// does not contain it.
func (g *OASSchemaGenerator) getBaseSchemaForSpec() (*Schema, error) {
	for _, verb := range g.resourceConfig.Verbs {
		if verb.Action != ActionCreate { // Right now we hardcode the action to 'create'
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
					//log.Printf("Warning: 'create' action schema is of type 'array', wrapping it into an object with an 'items' property")
					schema.Properties = append(schema.Properties, Property{Name: "items", Schema: &Schema{Type: []string{"array"}, Items: schema.Items}})
					schema.Type = []string{"object"}
				}
				return schema.deepCopy(), nil
			}
		}
	}
	return &Schema{}, nil
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
