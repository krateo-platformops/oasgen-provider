package oas2jsonschema

import (
	"context"
	"fmt"
	"slices"
	"strings"

	pathparsing "github.com/krateo-platformops/oasgen-provider/internal/tools/pathparsing"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/safety"
)

// BuildStatusSchema generates the complete status schema for a given resource.
func (g *OASSchemaGenerator) BuildStatusSchema() ([]byte, []error, error) {
	var warnings []error

	allStatusFields := statusFieldsFor(g.resourceConfig)
	if len(allStatusFields) == 0 {
		return nil, []error{SchemaGenerationError{Code: CodeNoStatusSchema, Message: "no identifiers or additional status fields defined, skipping status schema generation"}}, nil
	}

	responseSchema, err := g.getBaseSchemaForStatus()
	if err != nil {
		warnings = append(warnings, SchemaGenerationError{Message: fmt.Sprintf("schema validation warning: %v", err)})
	}
	if responseSchema == nil {
		warnings = append(warnings, SchemaGenerationError{Code: CodeNoStatusSchema, Message: "could not find a GET or FINDBY response schema for status generation"})
	}

	if err := prepareSchemaForCRD(responseSchema, g.generatorConfig); err != nil {
		return nil, warnings, fmt.Errorf("could not prepare status schema for CRD: %w", err)
	}

	statusSchema, buildWarnings := g.composeStatusSchema(allStatusFields, responseSchema)
	warnings = append(warnings, buildWarnings...)

	byteSchema, err := GenerateJsonSchema(statusSchema, g.generatorConfig)
	if err != nil {
		return nil, warnings, fmt.Errorf("could not generate final JSON schema for status: %w", err)
	}

	return byteSchema, warnings, nil
}

// composeStatusSchema builds the status schema by finding nested fields in the response schema
// and constructing a corresponding nested structure in the new status schema.
//
// When a status field is produced by a response-direction fieldMapping (inResponse -> inCustomResource),
// the field's CR-domain name will not exist in the raw response schema; its type is instead resolved
// through the mapping's source path (inResponse). A 'jq' value transform yields a type that is not
// statically analyzable, so such fields fall back to string (documented, not a "not found" error).
func (g *OASSchemaGenerator) composeStatusSchema(allStatusFields []string, responseSchema *Schema) (*Schema, []error) {
	var warnings []error
	statusSchema := &Schema{Type: []string{"object"}, Properties: []Property{}}
	respMappings := g.responseFieldMappingsForStatus()

	for _, fieldName := range allStatusFields {
		pathSegments, err := pathparsing.ParsePath(fieldName)
		if err != nil {
			warnings = append(warnings, SchemaGenerationError{Path: g.statusFieldPath(fieldName), Code: CodeFieldNotFound, Message: fmt.Sprintf("invalid path format for status field '%s': %v", fieldName, err)})
			continue
		}
		leaf := pathSegments[len(pathSegments)-1]

		// If a response fieldMapping relocates this status field, resolve its type through the source path
		// instead of looking up the CR-domain name (which is absent from the raw response).
		if m, ok := respMappings[fieldName]; ok {
			if m.ValueMappingType == "jq" {
				// A jq transform's output type is not statically analyzable: default to string.
				warnings = append(warnings, SchemaGenerationError{Path: g.statusFieldPath(fieldName), Code: CodeStatusFieldNotFound, Message: fmt.Sprintf("status field '%s' is produced by a jq value transform (inResponse '%s'); type is not statically known, defaulting to string", fieldName, m.InResponse)})
				g.addPropertyByPath(statusSchema, pathSegments, Property{Name: leaf, Schema: &Schema{Type: []string{"string"}}})
				continue
			}

			srcSegments, srcErr := pathparsing.ParsePath(m.InResponse)
			if srcErr == nil {
				if srcProp, srcFound := g.findPropertyByPath(responseSchema, srcSegments); srcFound {
					schema := srcProp.Schema
					if m.ValueMappingType == "alias" {
						// Aliased values are CR-domain strings/enums; the source's own enum no longer applies.
						schema = &Schema{Type: []string{"string"}, Description: srcProp.Schema.Description}
					}
					g.addPropertyByPath(statusSchema, pathSegments, Property{Name: leaf, Schema: schema})
					continue
				}
			}
			// Mapping declared but its source path is unresolvable: warn and fall back to string.
			warnings = append(warnings, SchemaGenerationError{Path: g.statusFieldPath(fieldName), Code: CodeStatusFieldNotFound, Message: fmt.Sprintf("status field '%s' maps from response path '%s' which was not found, defaulting to string", fieldName, m.InResponse)})
			g.addPropertyByPath(statusSchema, pathSegments, Property{Name: leaf, Schema: &Schema{Type: []string{"string"}}})
			continue
		}

		// Find the property in the source response schema.
		foundProp, found := g.findPropertyByPath(responseSchema, pathSegments)
		if found {
			// `findPropertyByPath` returns a deep-copied property, so we can use it directly.
			g.addPropertyByPath(statusSchema, pathSegments, foundProp)
		} else {
			// Fallback for fields not found in the response schema.
			warnings = append(warnings, SchemaGenerationError{Path: g.statusFieldPath(fieldName), Code: CodeStatusFieldNotFound, Message: fmt.Sprintf("status field '%s' not found in response, defaulting to string", fieldName)})
			fallbackProp := Property{Name: leaf, Schema: &Schema{Type: []string{"string"}}} // Fallback to string type
			g.addPropertyByPath(statusSchema, pathSegments, fallbackProp)
		}
	}

	return statusSchema, warnings
}

// responseFieldMappingsForStatus collects the response-direction fieldMapping entries declared on the
// GET/FINDBY verbs (the verbs whose response feeds the status), keyed by their CR-domain destination.
// A leading "status." prefix on inCustomResource is stripped so the key matches the bare status field
// names in identifiers/additionalStatusFields. The GET verb takes precedence over FINDBY on conflict.
func (g *OASSchemaGenerator) responseFieldMappingsForStatus() map[string]FieldMappingEntry {
	out := map[string]FieldMappingEntry{}
	if g.resourceConfig == nil {
		return out
	}
	for _, action := range []string{ActionGet, ActionFindBy} {
		for _, verb := range g.resourceConfig.Verbs {
			if !strings.EqualFold(verb.Action, action) {
				continue
			}
			for _, m := range verb.FieldMapping {
				if m.InResponse == "" || m.InCustomResource == "" {
					continue
				}
				dest := strings.TrimPrefix(m.InCustomResource, "status.")
				if _, exists := out[dest]; !exists {
					out[dest] = m
				}
			}
		}
	}
	return out
}

// findPropertyByPath is the public entry point for finding a nested property.
// It sets up a recursion guard, inspired by the pattern in spec_builder.go.
func (g *OASSchemaGenerator) findPropertyByPath(schema *Schema, path []string) (Property, bool) {
	guard := safety.NewRecursionGuard(g.generatorConfig.MaxRecursionDepth, g.generatorConfig.MaxRecursionNodes, g.generatorConfig.RecursionTimeout)
	ctx, cancel := guard.WithContext()
	defer cancel()
	return g.findPropertyByPathRec(ctx, schema, path, guard, 0)
}

// findPropertyByPathRec recursively traverses a schema to find a nested property.
// Returns a deep copy of the found property and true, or an empty property and false if not found.
func (g *OASSchemaGenerator) findPropertyByPathRec(ctx context.Context, schema *Schema, path []string, guard *safety.RecursionGuard, depth int) (Property, bool) {
	if schema == nil || len(path) == 0 || guard.Check(ctx, depth) != nil {
		return Property{}, false
	}

	fieldName := path[0]
	//log.Printf("Processing field: %s", fieldName)
	remainingPath := path[1:]
	//log.Printf("Remaining fields to process: %v", remainingPath)

	for _, prop := range schema.Properties {
		if prop.Name == fieldName {
			if len(remainingPath) == 0 {
				// Return a deep copy
				return Property{
					Name:   prop.Name,
					Schema: prop.Schema.deepCopy(),
				}, true
			}
			if prop.Schema == nil || getPrimaryType(prop.Schema.Type) != "object" {
				// Can't traverse further if it's not an object.
				// E.g., the case of "metadata.nested.leaf" where "nested" is a string.
				continue
			}
			// Continue traversing into the sub-schema.
			return g.findPropertyByPathRec(ctx, prop.Schema, remainingPath, guard, depth+1)
		}
	}

	return Property{}, false // Not found at this level
}

// addPropertyByPath is the public entry point for adding a nested property.
// It sets up a recursion guard for safety.
func (g *OASSchemaGenerator) addPropertyByPath(schema *Schema, path []string, propToAdd Property) {
	guard := safety.NewRecursionGuard(g.generatorConfig.MaxRecursionDepth, g.generatorConfig.MaxRecursionNodes, g.generatorConfig.RecursionTimeout)
	ctx, cancel := guard.WithContext()
	defer cancel()
	g.addPropertyByPathRec(ctx, schema, path, propToAdd, guard, 0)
}

// addPropertyByPathRec recursively builds the nested object structure in a schema
// and adds the target property at the correct location.
func (g *OASSchemaGenerator) addPropertyByPathRec(ctx context.Context, schema *Schema, path []string, propToAdd Property, guard *safety.RecursionGuard, depth int) {
	if schema == nil || len(path) == 0 || guard.Check(ctx, depth) != nil {
		return
	}

	fieldName := path[0]
	remainingPath := path[1:]

	// If this is the last part of the path, add the property here.
	if len(remainingPath) == 0 {
		// Avoid adding duplicates.
		for _, p := range schema.Properties {
			if p.Name == fieldName {
				return
			}
		}
		schema.Properties = append(schema.Properties, propToAdd)
		return
	}

	//  Intermediate path segment. Find or create the next object schema.
	var nextSchema *Schema
	found := false
	for _, p := range schema.Properties {
		if p.Name == fieldName {
			nextSchema = p.Schema
			found = true
			break
		}
	}

	if nextSchema != nil && getPrimaryType(nextSchema.Type) != "object" {
		// Error: expected an object to traverse further, but found a different type.
		// Example: if the path is "metadata.nested.leaf" but "nested" is a string and not an object.
		// So we cannot reach "leaf".
		//log.Printf("Warning: expected object type at '%s' but found type '%v'.", fieldName, nextSchema.Type)
		return
	}

	if !found {
		nextSchema = &Schema{Type: []string{"object"}, Properties: []Property{}}
		schema.Properties = append(schema.Properties, Property{Name: fieldName, Schema: nextSchema})
	}

	// Recurse into the next level.
	g.addPropertyByPathRec(ctx, nextSchema, remainingPath, propToAdd, guard, depth+1)
}

// statusFieldsFor returns the field names the status schema is built from: identifiers first, then
// additionalStatusFields. The order matters -- statusFieldPath reports a name present in both stanzas
// against identifiers, matching the entry reached first.
//
// slices.Concat, NOT append(Identifiers, AdditionalStatusFields...): appending onto a slice the caller
// still owns writes into ITS backing array whenever that array has spare capacity, replacing whatever
// the owner had after the append point. Nothing depends on that today, because Identifiers arrives from
// JSON unmarshalling with cap == len so the append always allocates -- but that is a property of the
// caller, not of this function, and one appended-to Identifiers anywhere upstream would turn this into
// silent corruption of the resource config. Same class as the childPath fix in
// rest-dynamic-controller#216.
func statusFieldsFor(rc *ResourceConfig) []string {
	if rc == nil {
		return nil
	}
	return slices.Concat(rc.Identifiers, rc.AdditionalStatusFields)
}

// statusFieldPath names where a status-field warning came from, so the rendered message locates itself.
// These warnings are read by someone holding a RestDefinition, and the stanza plus the entry name is the
// coordinate they can act on.
//
// The status schema is built from identifiers AND additionalStatusFields combined, so a warning could
// always have come from either. This hardcoded "additionalStatusFields.", which sent anyone whose
// IDENTIFIER failed to resolve searching a stanza that does not contain the name. Observed while
// generating a dispatch resource: `workflowId` is an identifier and a path parameter, so it is absent
// from the correlated-run response and warned -- and removing it from additionalStatusFields, where it
// had never been, changed nothing and the warning persisted. The doc comment above already said the
// coordinate should name either stanza; only the implementation disagreed.
func (g *OASSchemaGenerator) statusFieldPath(fieldName string) string {
	// Nil-safe: composeStatusSchema takes the field list as an argument, so it is reachable from a
	// generator built without a resource config (several tests do exactly that). Falling back to the
	// additionalStatusFields label there keeps the previous behaviour rather than panicking.
	if g.resourceConfig != nil && slices.Contains(g.resourceConfig.Identifiers, fieldName) {
		return "identifiers." + fieldName
	}
	return "additionalStatusFields." + fieldName
}
