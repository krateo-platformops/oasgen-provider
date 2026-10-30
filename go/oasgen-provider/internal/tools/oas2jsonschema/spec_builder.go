package oas2jsonschema

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	pathparsing "github.com/krateo-platformops/oasgen-provider/internal/tools/pathparsing"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/safety"
)

// BuildSpecSchema generates the complete spec schema for a given resource.
// It returns the schema as a byte slice with a list of warnings (non-fatal errors as a slice of errors).
// It returns a fatal error if the schema generation fails.
func (g *OASSchemaGenerator) BuildSpecSchema() ([]byte, []error, error) {
	var warnings []error

	// Create the base schema for the spec
	// which is the request body of the 'create' action.
	baseSchema, err := g.getBaseSchemaForSpec()
	if err != nil {
		return nil, nil, fmt.Errorf("could not determine base schema for spec: %w", err)
	}

	// A body field a RESOLVER supplies must not be required of the user (#246).
	//
	// The controller substitutes it -- from a Secret, or from a dependent lookup -- so requiring it forces
	// the author to write a value that is then overwritten. For secretRef that is worse than pointless:
	// the obvious thing to put in a required field called `token` is the credential, in cleartext, in the
	// CR spec, which is the exact outcome secretRef exists to avoid. For a dependentLookup it makes the
	// feature inert, because a `when` guard keyed on the field's absence can never fire.
	//
	// Done HERE, before parameters are added, and that ordering is load-bearing. The names come off the
	// request body's own `required`; a required PARAMETER that happens to share a name with a
	// resolver-supplied body field is a different thing, and addParametersToSpec re-adds it below. Pruning
	// afterwards would silently unrequire that parameter instead.
	dropResolverSuppliedRequired(baseSchema, g.resourceConfig.Verbs)

	// Add parameters to the spec schema. A cross-document parameter conflict is fatal, not a warning:
	// the merged field would otherwise be chosen by verb order (#108).
	requiredParameters, paramWarnings, err := g.addParametersToSpec(baseSchema)
	warnings = append(warnings, paramWarnings...)
	if err != nil {
		return nil, warnings, err
	}

	// Add identifiers to the spec schema, if configured.
	// Kept for legacy reasons, disabled by default.
	if g.generatorConfig.IncludeIdentifiersInSpec {
		addIdentifiersToSpec(baseSchema, g.resourceConfig.Identifiers)
	}

	// A resource with no create verb still needs its identifiers to be expressible.
	//
	// Since #236 the spec of such a resource is NOT necessarily empty: when an update verb is declared,
	// getBaseSchemaForSpec derives the base from the update body, so the resource is adopt-then-update
	// rather than read-only. The identifiers may therefore already be present, which
	// addIdentifierSelectorsToSpec skips ("already expressible") -- this pass adds only the ones the
	// body does not define.
	//
	// The original reasoning still holds for a genuinely read-only resource (no body-carrying write
	// verb), which is the case the next paragraph describes:
	//
	// A READ-ONLY resource has no request body to derive its spec from, so the spec
	// would otherwise contain nothing but path/query parameters and configurationRef. Its declared
	// identifiers would then name fields that exist nowhere: findby compares each listed item against the
	// CR and never matches, while get binds its {id} from a status field that only that findby could have
	// populated. The two block each other, and the resource is generated, statically valid, admitted by
	// the API server, and non-functional (#75).
	//
	// The generator's implicit assumption is that an identifier is always a spec field, which holds only
	// because a create body normally defines it. Where there is no create body, materialise the
	// identifiers into the spec explicitly: they are SELECTORS, values the user supplies to choose which
	// existing object this CR refers to.
	identifierSelectors := map[string]bool{}
	if !g.hasCreateVerb() {
		warnings = append(warnings, g.addIdentifierSelectorsToSpec(baseSchema)...)
		for _, id := range g.resourceConfig.Identifiers {
			identifierSelectors[id] = true
		}
	}

	// Schema preparation for CRD compatibility.
	if err := prepareSchemaForCRD(baseSchema, g.generatorConfig); err != nil {
		return nil, warnings, fmt.Errorf("could not prepare spec schema for CRD: %w", err)
	}

	// If the resource has configuration fields, add a reference to the configuration schema.
	// This is done only if there are configuration fields or security schemes defined.
	hasConfigurationRef := len(g.resourceConfig.ConfigurationFields) > 0 || len(g.doc().SecuritySchemes()) > 0
	if hasConfigurationRef {
		addConfigurationRefToSpec(baseSchema)
	}

	// Remove configured fields from the spec schema.
	warnings = append(warnings, g.removeConfiguredFields(baseSchema)...)

	// Remove excluded fields from the spec schema.
	warnings = append(warnings, g.removeExcludedSpecFields(baseSchema)...)

	// Narrow the spec to the allowlist, if one is declared. Mutually exclusive with excludedSpecFields at
	// admission, so the two never both apply; it runs here, last, because an allowlist has to see the
	// finished spec -- parameters merged, configurationRef added, configuration fields already pulled out --
	// or it would be deciding about fields that are not yet there. FATAL on an entry that matches nothing.
	if ierr := g.retainIncludedSpecFields(baseSchema, alwaysKeptFields{
		requiredParameters:  requiredParameters,
		identifierSelectors: identifierSelectors,
		configurationRef:    hasConfigurationRef,
	}); ierr != nil {
		return nil, warnings, ierr
	}

	// A pattern the API server cannot compile makes it refuse the whole CustomResourceDefinition, so
	// every pattern is compiled here and dropped with a warning if it fails (#249).
	warnings = append(warnings, validatePatterns(baseSchema, "spec")...)

	// Convert the schema to JSON schema format.
	byteSchema, err := GenerateJsonSchema(baseSchema, g.generatorConfig)
	if err != nil {
		return nil, warnings, fmt.Errorf("could not generate final JSON schema: %w", err)
	}

	//log.Printf("Generated spec schema: %s", string(byteSchema))

	return byteSchema, warnings, nil
}

// paramOrigin records which verb contributed a merged parameter, and from which document, so a later
// verb declaring the same name can be checked against it (#108).
type paramOrigin struct {
	action string
	doc    OASDocument
	schema *Schema
}

// addParametersToSpec adds the parameters from all verbs to the schema.
// Assumption: it adds parameters at the root level of the spec schema and does not support nested parameters.
// Nested parameters do not make sense in the context of path/query/header/cookie parameters.
//
// Returns a FATAL error when two verbs resolving to different OAS documents declare the same parameter
// name with different schemas. This is the one place in generation where two documents demonstrably feed
// the same CRD field, and the merge below resolves it by VERB ORDER -- so the alternative to refusing is
// a CRD whose shape depends on how the author happened to order verbsDescription, with the losing verb
// sending a value shaped for the other document's contract. Nothing downstream could report it.
// Returns the names of spec fields derived from REQUIRED parameters, which includedSpecFields may never
// drop: a kind that cannot bind its own URL is the breakage #197 fixed, and an allowlist could otherwise
// reintroduce it by configuration. Returned rather than stashed on the generator because a second field
// that must agree with the schema is the shape generator.go already regrets once.
func (g *OASSchemaGenerator) addParametersToSpec(schema *Schema) (map[string]bool, []error, error) {
	var warnings []error

	// Track unique parameter names to avoid duplicates (E.g. path parameters may be repeated across verbs),
	// along with where each came from so a cross-document disagreement can be caught.
	// Here we track path, query, header, and cookie parameters.
	uniqueParams := make(map[string]paramOrigin)

	// Internal helper function to check if property already exists in schema
	propertyExists := func(name string) bool {
		for _, prop := range schema.Properties {
			if prop.Name == name {
				return true
			}
		}
		return false
	}

	// Internal helper function to check if property is already required in schema
	requiredParameters := map[string]bool{}
	// Parameters a request-direction fieldMapping supplies. See mappingSuppliedParams: these are omitted
	// from the spec entirely rather than merely un-required, because an optional field that nothing reads
	// is its own trap -- an author who sets it is silently ignored, since the request takes the mapped
	// value. They are also kept OUT of requiredParameters, which exists to protect a field from
	// excludedSpecFields pruning on the grounds that "dropping one leaves a kind that cannot build its own
	// URL" -- false once a mapping supplies it, so the protection would be guarding nothing.
	//
	// Safe to omit the property: addIdentifiersToSpec runs AFTER this and re-adds any identifier it does
	// not find, so a parameter that is also a declared identifier still gets its spec field (as an
	// optional string) when includeIdentifiersInSpec is on.
	mappingSupplied := mappingSuppliedParams(g.resourceConfig.Verbs)

	propertyAlreadyRequired := func(name string) bool {
		for _, req := range schema.Required {
			if req == name {
				return true
			}
		}
		return false
	}

	for _, verb := range g.resourceConfig.Verbs {
		// 1. Path lookup, in the verb's OWN document -- parameters are merged across every verb, so a verb
		// that overrode spec.oasPath contributes its parameters from the document it named.
		verbDoc := g.docFor(verb.Action)
		path, ok := verbDoc.FindPath(verb.Path)
		if !ok {
			warnings = append(warnings, SchemaGenerationError{Code: CodePathNotFound, Message: fmt.Sprintf("path '%s' set in RestDefinition not found in OAS", verb.Path)})
			continue
		}

		// 2. Operation lookup
		ops := path.GetOperations()
		op, ok := ops[strings.ToLower(verb.Method)]
		if !ok {
			continue
		}

		// 3. Parameters lookup
		for _, param := range op.GetParameters() {
			// if is authorization header, skip it as it is managed by the Configuration CR within the authentication section.
			if isAuthorizationHeader(param) {
				continue
			}

			// A name already merged from a DIFFERENT document must agree with what is there (#108).
			//
			// Scoped to cross-document deliberately: two verbs sharing one document that declare the same
			// parameter differently is pre-existing, long-standing, and resolved by first-wins, so checking
			// it here would refuse RestDefinitions that work today. A disagreement is only reachable at all
			// once a verb overrides spec.oasPath, which is exactly the new surface this guards.
			if prior, exists := uniqueParams[param.Name]; exists && prior.doc != verbDoc {
				if diffs := compareSchemas("spec."+param.Name, prior.schema, param.Schema, prior.action, verb.Action, g.generatorConfig); len(diffs) > 0 {
					return nil, warnings, SchemaGenerationError{
						Path: "spec." + param.Name,
						Code: CodeCrossDocumentParameterConflict,
						Message: fmt.Sprintf(
							"parameter %q is declared by verb %q and verb %q, which resolve to different OAS documents, "+
								"with schemas that disagree (%v); the two definitions are merged into one spec field and the "+
								"winner would be decided by the order of verbsDescription, so set the verbs' oasPath to "+
								"documents that agree on %q, or exclude it via spec.resource.excludedSpecFields",
							param.Name, prior.action, verb.Action, errors.Join(diffs...), param.Name),
						Got:      param.Schema,
						Expected: prior.schema,
					}
				}
			}

			if mappingSupplied[param.Name] {
				// Supplied by a fieldMapping: not the author's to provide, so neither required, nor
				// present in spec, nor protected from excludedSpecFields pruning.
				//
				// uniqueParams IS still recorded. The cross-document conflict check above reads it, and a
				// mapped parameter is still SENT -- only its source changes -- so two verbs declaring it
				// from documents whose schemas disagree is just as much an authoring error as before.
				// Skipping the bookkeeping would have silently disabled that check for exactly the
				// parameters this change touches.
				if _, exists := uniqueParams[param.Name]; !exists {
					uniqueParams[param.Name] = paramOrigin{action: verb.Action, doc: verbDoc, schema: param.Schema}
				}
				continue
			}

			if param.Required {
				requiredParameters[param.Name] = true
			}

			// Add parameter to spec only if not already present in uniqueParams AND not in existing base schema
			// Therefore we give precedence to base schema properties over parameters.
			if _, exists := uniqueParams[param.Name]; !exists && !propertyExists(param.Name) {
				// Deep copy the schema to avoid modifying the original object, which might be shared.
				schemaCopy := param.Schema.deepCopy()

				if param.Description == "" {
					schemaCopy.Description = fmt.Sprintf("PARAMETER: %s", param.In)
				} else {
					schemaCopy.Description = fmt.Sprintf("PARAMETER: %s - %s", param.In, param.Description)
				}
				schema.Properties = append(schema.Properties, Property{Name: param.Name, Schema: schemaCopy})
				if param.Required {
					schema.Required = append(schema.Required, param.Name)
				}

				uniqueParams[param.Name] = paramOrigin{action: verb.Action, doc: verbDoc, schema: param.Schema}
			}

			// If the parameter is already present but optional in the base schema (e.g., in the request body schema) and it is required in a parameter definition (e.g., path parameter),
			// ensure it is marked as required in the final schema.
			if propertyExists(param.Name) && param.Required && !propertyAlreadyRequired(param.Name) {
				//log.Printf("Marking existing property '%s' as required based on parameter definition", param.Name)
				schema.Required = append(schema.Required, param.Name)
			}
		}
	}

	return requiredParameters, warnings, nil
}

// addConfigurationRefToSpec adds the `configurationRef` property to the schema (at the root level of spec).
func addConfigurationRefToSpec(schema *Schema) {
	configRefSchema := &Schema{
		Type:        []string{"object"},
		Description: "A reference to the Configuration CR that holds all the needed configuration for this resource. OASGen Provider added this field automatically.",
		Properties: []Property{
			{Name: "name", Schema: &Schema{Type: []string{"string"}}},
			{Name: "namespace", Schema: &Schema{Type: []string{"string"}, Description: "Namespace of the referenced Configuration CR. If not provided, the same namespace will be used."}},
		},
		Required: []string{"name"}, // Namespace not required, if namespace is not provided, it is Rest Dynamic Controller's duty to use the same namespace as the resource.
	}
	schema.Properties = append(schema.Properties, Property{Name: "configurationRef", Schema: configRefSchema})
	schema.Required = append(schema.Required, "configurationRef")
}

// addIdentifiersToSpec adds the identifiers to the schema.
// Kept for legacy reasons, disabled by default.
// TODO: this will be removed in future versions.
func addIdentifiersToSpec(schema *Schema, identifiers []string) {
	for _, identifier := range identifiers {
		found := false
		for i, p := range schema.Properties {
			if p.Name == identifier {
				// Field already exists, append to its description
				if p.Schema.Description != "" {
					schema.Properties[i].Schema.Description += " "
				}
				schema.Properties[i].Schema.Description += fmt.Sprintf("(IDENTIFIER: %s)", identifier)
				found = true
				break
			}
		}
		// If the identifier is not found, add it to the schema.
		if !found {
			schema.Properties = append(schema.Properties, Property{
				Name: identifier,
				Schema: &Schema{
					Description: fmt.Sprintf("IDENTIFIER: %s", identifier),
					Type:        []string{"string"},
				},
			})
		}
	}
}

// hasCreateVerb reports whether the resource declares a create action. Its request body is what the spec
// schema is normally derived from, so its absence is what makes a resource read-only.
func (g *OASSchemaGenerator) hasCreateVerb() bool {
	for _, verb := range g.resourceConfig.Verbs {
		if strings.EqualFold(verb.Action, ActionCreate) {
			return true
		}
	}
	return false
}

// addIdentifierSelectorsToSpec materialises the declared identifiers of a read-only resource into the spec
// as user-supplied selectors, so the user can express WHICH existing object the CR refers to.
//
// The type is taken from the get/findby response schema when the identifier appears there, so a numeric id
// stays numeric rather than being forced to string; identifiers absent from that schema fall back to
// string. An identifier that is present in neither the spec nor the response schema names a field the
// controller can never read, so it is reported as a warning rather than silently added — the resource would
// otherwise reach Ready while being unable to resolve.
func (g *OASSchemaGenerator) addIdentifierSelectorsToSpec(schema *Schema) []error {
	var warnings []error

	statusSchema, err := g.getBaseSchemaForStatus()
	if err != nil {
		// Not fatal: fall back to untyped selectors rather than failing generation outright.
		statusSchema = nil
	}

	for _, identifier := range g.resourceConfig.Identifiers {
		if slices.ContainsFunc(schema.Properties, func(p Property) bool { return p.Name == identifier }) {
			continue // already expressible (e.g. it is also a path/query parameter)
		}

		// A DOTTED identifier is a nested path, not a key containing dots. RDC reads it that way --
		// isInResource splits on "." and walks spec -> metadata -> name via NestedFieldNoCopy -- so
		// emitting a flat property literally named "metadata.name" produces a spec the controller can
		// never read: findby compares against a field that does not exist and matches nothing, silently,
		// on a resource that reports Ready (#106).
		//
		// composeStatusSchema already handles this class of value correctly with the same two helpers.
		// Diverging from it here is what created the bug, so the selector path now uses them too.
		pathSegments, perr := pathparsing.ParsePath(identifier)
		if perr != nil {
			warnings = append(warnings, SchemaGenerationError{
				Path:    fmt.Sprintf("identifiers.%s", identifier),
				Code:    CodeFieldNotFound,
				Message: fmt.Sprintf("invalid path format for identifier '%s': %v", identifier, perr),
				Got:     identifier,
			})
			continue
		}
		leaf := pathSegments[len(pathSegments)-1]

		// The wording distinguishes the two reasons a resource has no create verb, because they mean
		// different things to whoever reads the generated CRD. With no body-carrying write verb at all
		// the resource really is read-only. With an update verb, the spec is derived from ITS body
		// (#236) and the resource is adopt-then-update: the identifier still selects which object is
		// meant, but calling the resource read-only would be wrong.
		description := "SELECTOR: identifies which existing object this resource refers to. This resource declares no create verb, so this field selects an object rather than defining one."
		if g.declaresAnyAction(ActionUpdate) {
			description = "SELECTOR: identifies which existing object this resource refers to. This resource has no create verb, so it adopts an existing object and updates it; this field selects which one."
		}

		selector := &Schema{
			Type:        []string{"string"},
			Description: description,
		}

		// Resolve the declared type by walking the SAME path through the observe response, so a nested
		// identifier keeps its type instead of silently becoming a string.
		if found, typ := lookupTypeByPath(statusSchema, pathSegments); found {
			if len(typ) > 0 {
				selector.Type = typ
			}
		} else {
			warnings = append(warnings, SchemaGenerationError{
				Path: fmt.Sprintf("identifiers.%s", identifier),
				Code: CodeIdentifierNotResolvable,
				Message: fmt.Sprintf("identifier %q is not present in the get/findby response schema, so findby can never match on it; "+
					"it is exposed in the spec as a string selector, but verify it names a real field of the listed items", identifier),
				Got: identifier,
			})
		}

		g.addPropertyByPath(schema, pathSegments, Property{Name: leaf, Schema: selector})
	}

	return warnings
}

// lookupTypeByPath walks a parsed path through a schema and reports the leaf's declared type.
//
// It exists because an identifier may be nested ("metadata.name"), so checking only the top-level
// properties -- as this file did before #106 -- both misses the type and misreports the field as
// absent, firing CodeIdentifierNotResolvable for an identifier that is perfectly valid.
func lookupTypeByPath(schema *Schema, path []string) (bool, []string) {
	cur := schema
	for i, seg := range path {
		if cur == nil {
			return false, nil
		}
		var next *Schema
		for _, p := range cur.Properties {
			if p.Name == seg {
				next = p.Schema
				break
			}
		}
		if next == nil {
			return false, nil
		}
		if i == len(path)-1 {
			return true, next.Type
		}
		cur = next
	}
	return false, nil
}

// isAuthorizationHeader checks if the given parameter is an authorization header (case-insensitive).
func isAuthorizationHeader(param ParameterInfo) bool {
	return strings.EqualFold(param.In, "header") && strings.Contains(strings.ToLower(param.Name), "authorization")
}

// removeConfiguredFields removes the fields from the schema that are defined in the configurationFields list.
func (g *OASSchemaGenerator) removeConfiguredFields(schema *Schema) []error {
	if len(g.resourceConfig.ConfigurationFields) == 0 {
		return nil
	}

	var warnings []error
	for _, field := range g.resourceConfig.ConfigurationFields {
		pathSegments, err := pathparsing.ParsePath(field.FromOpenAPI.Name)
		//log.Printf("Removing configured field '%s' with path segments: %v", field.FromOpenAPI.Name, pathSegments)
		if err != nil {
			warnings = append(warnings, SchemaGenerationError{Code: CodeFieldNotFound, Message: fmt.Sprintf("invalid path format for configured field '%s': %v", field.FromOpenAPI.Name, err)})
			continue
		}

		if !g.removeFieldAtPath(schema, pathSegments) {
			warnings = append(warnings, SchemaGenerationError{Code: CodeFieldNotFound, Message: fmt.Sprintf("field '%s' set in configurationFields not found in schema", field.FromOpenAPI.Name)})
		}
	}
	return warnings
}

// removeExcludedSpecFields removes the fields from the schema that are defined in the `excludedSpecFields` list.
func (g *OASSchemaGenerator) removeExcludedSpecFields(schema *Schema) []error {
	if len(g.resourceConfig.ExcludedSpecFields) == 0 {
		return nil
	}

	var warnings []error
	for _, excludedField := range g.resourceConfig.ExcludedSpecFields {
		pathSegments, err := pathparsing.ParsePath(excludedField)
		//log.Printf("Removing excluded field '%s' with path segments: %v", excludedField, pathSegments)
		if err != nil {
			warnings = append(warnings, SchemaGenerationError{Code: CodeFieldNotFound, Message: fmt.Sprintf("invalid path format for excluded field '%s': %v", excludedField, err)})
			continue
		}

		if !g.removeFieldAtPath(schema, pathSegments) {
			warnings = append(warnings, SchemaGenerationError{Code: CodeFieldNotFound, Message: fmt.Sprintf("field '%s' set in excludedSpecFields not found in schema", excludedField)})
		}
	}
	return warnings
}

// removeFieldAtPath is the entry point for removing a nested field from a schema.
// It sets up a recursion guard and calls the recursive implementation.
func (g *OASSchemaGenerator) removeFieldAtPath(schema *Schema, fields []string) bool {
	guard := safety.NewRecursionGuard(g.generatorConfig.MaxRecursionDepth, g.generatorConfig.MaxRecursionNodes, g.generatorConfig.RecursionTimeout)
	ctx, cancel := guard.WithContext()
	defer cancel()

	return g.removeFieldAtPathRec(ctx, schema, fields, guard, 0)
}

// removeFieldAtPathRec recursively traverses the schema and removes the specified nested field represented by the fields slice.
func (g *OASSchemaGenerator) removeFieldAtPathRec(ctx context.Context, schema *Schema, fields []string, guard *safety.RecursionGuard, depth int) bool {
	if schema == nil || len(fields) == 0 {
		return false
	}

	if err := guard.Check(ctx, depth); err != nil {
		return false
	}

	fieldName := fields[0]
	//log.Printf("At depth %d, looking for field '%s' in schema", depth, fieldName)
	remainingFields := fields[1:]
	//log.Printf("Remaining fields to process: %v", remainingFields)

	propIndex := -1
	for i, prop := range schema.Properties {
		if prop.Name == fieldName {
			propIndex = i
			break
		}
	}

	if propIndex == -1 {
		return false // Property not found at this level
	}

	// If there are more segments, recurse into the child schema.
	if len(remainingFields) > 0 {
		return g.removeFieldAtPathRec(ctx, schema.Properties[propIndex].Schema, remainingFields, guard, depth+1)
	}

	// --- Field found at the current level, and it's the target to remove ---

	// Remove the property from the Properties slice.
	//log.Printf("Removing field '%s' from schema at depth %d", fieldName, depth)
	schema.Properties = append(schema.Properties[:propIndex], schema.Properties[propIndex+1:]...)
	if len(schema.Properties) == 0 {
		schema.Properties = []Property{} // Ensure it's an empty slice, not nil
	}

	// Remove the field name from the Required slice, if it exists.
	reqIndex := -1
	for i, req := range schema.Required {
		if req == fieldName {
			reqIndex = i
			break
		}
	}
	if reqIndex != -1 {
		//log.Printf("Removing field '%s' from required fields at depth %d", fieldName, depth)
		schema.Required = append(schema.Required[:reqIndex], schema.Required[reqIndex+1:]...)
		if len(schema.Required) == 0 {
			schema.Required = []string{} // Ensure it's an empty slice, not nil
		}
	}

	//log.Printf("Field '%s' successfully removed", fieldName)

	return true // Field was found and removed.
}

// dropResolverSuppliedRequired removes, from the schema's required list, every name that a
// request-direction fieldMapping resolver supplies.
//
// Only `inBody` entries are considered. A resolver on inPath/inQuery addresses a URL rather than a spec
// property, so there is nothing in `required` that legitimately corresponds to it, and matching such an
// entry by name could unrequire an unrelated field that merely shares it.
//
// The property itself stays in the schema: the API still accepts the field, a CR may still set it (an
// explicitly supplied value is what a dependentLookup's `when` guard exists to honour), and removing the
// property would also remove the thing the mapping is anchored on.
// mappingSuppliedParams returns the PARAMETER names whose value a request-direction fieldMapping supplies,
// so they are not demanded of the author.
//
// This is #246's rule for parameters. That fix established the principle for resolver-supplied BODY fields:
//
//	"A body field a RESOLVER supplies must not be required of the user [...] requiring it forces the author
//	 to write a value that is then overwritten."
//
// It was never extended to parameters, so every path parameter stayed a required top-level spec field named
// after the PARAMETER, regardless of what the mapping said. For a parameter mapped from status.* that is not
// merely redundant, it is IMPOSSIBLE to satisfy: the vendor assigns the id at create and the controller
// reads it back, so the author cannot supply a value that does not exist yet, and the kind is uncreatable.
// Measured on five generated CRDs, five for five (#325).
//
// A mapping from spec.* -- e.g. inCustomResource: spec.keypair.name for path parameter {name} -- is the
// milder case: the value IS author-supplied, just at another path, and the flat duplicate is redundant
// rather than unsatisfiable. Both are suppressed; only one was fatal.
//
// InPath and InQuery are the request-direction destinations that address a parameter. InBody is #246's
// territory and is left to dropResolverSuppliedRequired, which runs before this and prunes the request
// body's own `required` -- the two must not overlap, because that function's doc comment is explicit that
// pruning a PARAMETER afterwards would silently unrequire one that merely shares a name with a
// resolver-supplied body field.
func mappingSuppliedParams(verbs []Verb) map[string]bool {
	supplied := map[string]bool{}
	for _, verb := range verbs {
		for _, m := range verb.FieldMapping {
			// A request-direction entry names its source in the CR. An entry with no source is a
			// response-direction mapping (inResponse -> inCustomResource) or malformed; neither supplies a
			// request parameter.
			if m.InCustomResource == "" {
				continue
			}
			if m.InPath != "" {
				supplied[m.InPath] = true
			}
			if m.InQuery != "" {
				supplied[m.InQuery] = true
			}
		}
	}
	return supplied
}

func dropResolverSuppliedRequired(schema *Schema, verbs []Verb) {
	if schema == nil || len(schema.Required) == 0 {
		return
	}

	supplied := map[string]bool{}
	for _, verb := range verbs {
		for _, m := range verb.FieldMapping {
			if m.ResolverType == "" || m.InBody == "" {
				continue
			}
			supplied[m.InBody] = true
		}
	}
	if len(supplied) == 0 {
		return
	}

	kept := make([]string, 0, len(schema.Required))
	for _, name := range schema.Required {
		if supplied[name] {
			continue
		}
		kept = append(kept, name)
	}
	schema.Required = kept
}

// validatePatterns compiles every `pattern` in the schema with Go's regexp and REMOVES the ones that do
// not compile, reporting each as a warning.
//
// Kubernetes compiles a CRD's patterns as RE2; OpenAPI specifies ECMA-262. Most patterns are identical
// under both, but not all: backreferences and lookahead are valid ECMA and rejected by RE2. Emitting one
// of those produces a CustomResourceDefinition the API server refuses outright -- so the resource does
// not merely lose a constraint, the whole CRD fails to install, exactly as an over-budget CEL rule does.
//
// Dropping the pattern and warning is therefore the conservative choice in the real sense: the resource
// loses one constraint it was never going to get, instead of the install losing the resource. The warning
// names the path so the author can rewrite it RE2-compatibly.
func validatePatterns(schema *Schema, path string) []error {
	if schema == nil {
		return nil
	}

	var warnings []error

	if schema.Pattern != "" {
		if _, err := regexp.Compile(schema.Pattern); err != nil {
			warnings = append(warnings, SchemaGenerationError{
				Path:    path,
				Code:    CodeFieldNotFound,
				Message: fmt.Sprintf("pattern %q at %s is not a valid RE2 expression and was dropped: %v. Kubernetes compiles CRD patterns as RE2 while OpenAPI specifies ECMA-262, so constructs such as backreferences and lookahead are rejected; emitting it would make the API server refuse the entire CustomResourceDefinition", schema.Pattern, path, err),
				Got:     schema.Pattern,
			})
			schema.Pattern = ""
		}
	}

	for _, p := range schema.Properties {
		warnings = append(warnings, validatePatterns(p.Schema, path+"."+p.Name)...)
	}
	warnings = append(warnings, validatePatterns(schema.Items, path+"[]")...)
	for i, a := range schema.AllOf {
		warnings = append(warnings, validatePatterns(a, fmt.Sprintf("%s.allOf[%d]", path, i))...)
	}
	if schema.AdditionalProperties != nil {
		warnings = append(warnings, validatePatterns(schema.AdditionalProperties.Schema, path+".additionalProperties")...)
	}

	return warnings
}
