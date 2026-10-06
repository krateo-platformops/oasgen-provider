package oas2jsonschema

import (
	"fmt"
	"reflect"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/text"
)

// Note: currently the Configuration CRD has no status subresource.

// BuildConfigurationSchema builds the spec schema for the Configuration CRD.
func (g *OASSchemaGenerator) BuildConfigurationSchema() ([]byte, error) {
	if len(g.resourceConfig.ConfigurationFields) == 0 && len(g.doc().SecuritySchemes()) == 0 {
		return nil, nil
	}

	// Root schema for the entire configuration.
	rootSchema := &Schema{
		Type:       []string{"object"},
		Properties: []Property{},
	}

	paramTypeSchemas := make(map[string]*Schema)

	for _, field := range g.resourceConfig.ConfigurationFields {
		fieldParam, err := g.findParameterInOAS(field)
		if err != nil {
			// TODO: Consider logging a warning here.
			continue
		}

		// Ensure the top-level schema for the parameter's location (e.g., "query") already exists.
		paramIn := fieldParam.In
		if _, ok := paramTypeSchemas[paramIn]; !ok {
			paramTypeSchemas[paramIn] = &Schema{Type: []string{"object"}, Properties: []Property{}}
		}
		paramTypeSchema := paramTypeSchemas[paramIn]

		// Iterate over all actions this configuration field applies to.
		for _, action := range field.FromRestDefinition.Actions {
			// Each action takes the parameter as ITS OWN document declares it (#108). A field scoped to
			// several actions can span two documents, and resolving once for the whole field stamped
			// whichever document answered first onto every action -- so Aruba's create, served by the 1.1
			// document, was offered api-version as the 1.0 document describes it: optional, defaulting to
			// "1", against an endpoint that requires "1.1". Admission accepted the omission and the wrong
			// version went on the wire with nothing to report it.
			//
			// Falling back to the field-level resolution keeps this a correction and not a removal: an
			// action whose own document does not declare the parameter still gets an entry, exactly as
			// before, rather than silently losing a field some Configuration may already set.
			param := fieldParam
			if actionParam, aerr := g.findParameterForAction(field, action); aerr == nil {
				param = actionParam
			}

			var actionSchema *Schema
			found := false
			// Check if a schema for this action (e.g., "get") already exists
			for i := range paramTypeSchema.Properties {
				if paramTypeSchema.Properties[i].Name == action {
					actionSchema = paramTypeSchema.Properties[i].Schema
					found = true
					break
				}
			}
			// If not found, create a new schema for the action (e.g., "get").
			if !found {
				actionSchema = &Schema{Type: []string{"object"}, Properties: []Property{}}
				paramTypeSchema.Properties = append(paramTypeSchema.Properties, Property{
					Name:   action,
					Schema: actionSchema,
				})
			}

			// Add a deep copy of the parameter's schema to the action's schema
			// to prevent issues with shared schema references.
			actionSchema.Properties = append(actionSchema.Properties, Property{Name: param.Name, Schema: param.Schema.deepCopy()})
			if param.Required {
				actionSchema.Required = append(actionSchema.Required, param.Name)
			}
		}
	}

	if len(paramTypeSchemas) > 0 {
		configurationSchema := &Schema{
			Type:       []string{"object"},
			Properties: []Property{},
		}
		for paramType, schema := range paramTypeSchemas {
			configurationSchema.Properties = append(configurationSchema.Properties, Property{Name: paramType, Schema: schema})
		}
		rootSchema.Properties = append(rootSchema.Properties, Property{
			Name:   "configuration",
			Schema: configurationSchema,
		})
	}

	authMethodsSchemas, skippedSchemes, err := g.buildAuthMethodsSchemaMap()
	if err != nil {
		return nil, fmt.Errorf("could not generate auth schemas for configuration: %w", err)
	}
	g.skippedSecuritySchemes = skippedSchemes
	if len(authMethodsSchemas) > 0 {
		addAuthMethods(rootSchema, authMethodsSchemas)
	}
	//log.Printf("Len of auth methods schemas: %d", len(authMethodsSchemas))

	return GenerateJsonSchema(rootSchema, g.generatorConfig)
}

// buildAuthMethodsSchemaMap generates the JSON schemas for the authentication methods, and returns the
// names of any schemes it could not generate.
//
// Those names are RETURNED rather than dropped because the failure was invisible: a document whose only
// scheme is unsupported still yields a Configuration CRD — hasSecuritySchemes is len(SecuritySchemes()) > 0,
// i.e. presence, not supportability — and addAuthMethods is then skipped because this map is empty. The
// user gets a <Kind>Configuration that looks like where credentials go, with no authentication field in it,
// and finds out via 401s. A CRD that lies is worse than no CRD.
//
// It is a warning rather than an error on purpose. Unlike a rejected requestTransform or poll path, the
// user declared nothing here — the VENDOR's document mentions a scheme we cannot generate — and we cannot
// know whether the endpoint actually enforces the scheme it advertises. Failing generation would break
// anyone running an oauth2-declaring document against an endpoint that does not enforce it, whose only
// recourse would be editing the vendor spec.
func (g *OASSchemaGenerator) buildAuthMethodsSchemaMap() (map[string]*Schema, []string, error) {
	schemes := g.doc().SecuritySchemes()

	// A header default is only meaningful when exactly one apiKey scheme exists; with several there is no
	// single correct answer, so the generated field stays required and the author chooses.
	apiKeyCount := 0
	for _, s := range schemes {
		if s.Type == SchemeTypeAPIKey && s.In == "header" {
			apiKeyCount++
		}
	}

	schemaMap := make(map[string]*Schema)
	var skipped []string
	for _, secScheme := range schemes {
		key, ok := authMethodKey(secScheme)
		if !ok {
			skipped = append(skipped, fmt.Sprintf("%s (type: %s, in: %s)", secScheme.Name, secScheme.Type, secScheme.In))
			continue
		}
		defaultHeader := ""
		if apiKeyCount == 1 {
			defaultHeader = secScheme.ParamName
		}
		authSchema, err := createSchemaForSecurityScheme(secScheme, defaultHeader)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%v)", secScheme.Name, err))
			continue
		}
		schemaMap[key] = authSchema
	}
	return schemaMap, skipped, nil
}

// addAuthMethods adds the `authentication` property to the configuration schema.
func addAuthMethods(schema *Schema, authSchemas map[string]*Schema) {
	authMethodsProps := []Property{}
	for key, authSchema := range authSchemas {
		authMethodsProps = append(authMethodsProps, Property{Name: text.FirstToLower(key), Schema: authSchema})
	}

	authMethodsSchema := &Schema{
		Type:        []string{"object"},
		Description: "The authentication methods available for this API.",
		Properties:  authMethodsProps,
	}
	schema.Properties = append(schema.Properties, Property{Name: "authentication", Schema: authMethodsSchema})
}

// authMethodKey is the property name a scheme is exposed under within `authentication`, and the string
// rest-dynamic-controller switches on (auth.ToType). It is derived from the scheme KIND, not from
// SecuritySchemeInfo.Scheme: that sub-field is populated only for `type: http`, so keying by it would name
// an apiKey scheme's property "" (the empty string).
//
// The vocabulary is deliberately closed — basic, bearer, apiKey. Deriving it from the document's own scheme
// names (ApiKeyAuth, TokenAuth, ...) would make RDC's switch impossible without a second lookup table, and
// would bake vendor naming into the CR schema, so a vendor rename would become a breaking CRD change here.
func authMethodKey(info SecuritySchemeInfo) (string, bool) {
	switch {
	case info.Type == SchemeTypeHTTP && info.Scheme == "basic":
		return "basic", true
	case info.Type == SchemeTypeHTTP && info.Scheme == "bearer":
		return "bearer", true
	case info.Type == SchemeTypeAPIKey && info.In == "header":
		return "apiKey", true
	}
	return "", false
}

// createSchemaForSecurityScheme generates the JSON schema for a given security scheme.
//
// Supported: http/basic, http/bearer, and apiKey in a header. apiKey in query or cookie is deliberately not
// supported — query in particular puts credentials in URLs and access logs — and neither is oauth2 or
// openIdConnect; all of them are reported by the caller rather than dropped.
//
// defaultHeader is applied to the apiKey shape's `header` field when non-empty. The caller leaves it empty
// when the document declares SEVERAL apiKey schemes, so the field stays required and the author says which
// one they mean, instead of the generator picking one arbitrarily.
func createSchemaForSecurityScheme(info SecuritySchemeInfo, defaultHeader string) (*Schema, error) {
	if info.Type == SchemeTypeHTTP && info.Scheme == "basic" {
		return reflectSchema(reflect.TypeOf(BasicAuth{}))
	}

	if info.Type == SchemeTypeHTTP && info.Scheme == "bearer" {
		return reflectSchema(reflect.TypeOf(BearerAuth{}))
	}

	if info.Type == SchemeTypeAPIKey && info.In == "header" {
		sch, err := reflectSchema(reflect.TypeOf(APIKeyAuth{}))
		if err != nil {
			return nil, err
		}
		applyAPIKeyHeaderDefault(sch, defaultHeader, info)
		return sch, nil
	}

	return nil, fmt.Errorf("unsupported security scheme %q (type: %s, in: %s)", info.Name, info.Type, info.In)
}

// applyAPIKeyHeaderDefault sets the generated `header` property's default and description. With a single
// apiKey scheme the default makes the field zero-config; with several the caller passes an empty
// defaultHeader, leaving the author to choose.
func applyAPIKeyHeaderDefault(sch *Schema, defaultHeader string, info SecuritySchemeInfo) {
	for i := range sch.Properties {
		if sch.Properties[i].Name != "header" || sch.Properties[i].Schema == nil {
			continue
		}
		if defaultHeader != "" {
			sch.Properties[i].Schema.Default = defaultHeader
			sch.Properties[i].Schema.Description = fmt.Sprintf(
				"Header the credential is sent in. Defaulted from security scheme %q, which declares %q.",
				info.Name, defaultHeader)
		} else {
			sch.Properties[i].Schema.Description = "Header the credential is sent in. This document declares " +
				"more than one apiKey scheme, so there is no single correct default: set this to the header of " +
				"the scheme this configuration should use."
		}
		return
	}
}
