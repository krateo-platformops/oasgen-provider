package oas2jsonschema

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateConfigurationSchema(t *testing.T) {
	// Common mock document with various parameters
	mockDoc := &mockOASDocument{
		Paths: map[string]*mockPathItem{
			"/items": {
				Ops: map[string]Operation{
					"get": &mockOperation{
						Parameters: []ParameterInfo{
							{Name: "api-version", In: "query", Schema: &Schema{Type: []string{"string"}}},
							{Name: "X-Request-ID", In: "header", Schema: &Schema{Type: []string{"string"}}},
						},
					},
					"post": &mockOperation{
						Parameters: []ParameterInfo{
							{Name: "api-version", In: "query", Schema: &Schema{Type: []string{"string"}}},
						},
					},
				},
			},
			"/items/{id}": {
				Ops: map[string]Operation{
					"put": &mockOperation{
						Parameters: []ParameterInfo{
							{Name: "id", In: "path", Schema: &Schema{Type: []string{"integer"}}},
							{Name: "api-version", In: "query", Schema: &Schema{Type: []string{"string"}}},
						},
					},
				},
			},
		},
		securitySchemes: []SecuritySchemeInfo{
			{Name: "BearerAuth", Type: SchemeTypeHTTP, Scheme: "bearer"},
			{Name: "BasicAuth", Type: SchemeTypeHTTP, Scheme: "basic"},
		},
	}

	testCases := []struct {
		name                string
		resourceConfig      *ResourceConfig
		doc                 OASDocument
		expectError         bool
		expectedSchemaPaths map[string]string // map of JSON path to expected type
	}{
		{
			name: "Parameters with Single Actions",
			doc:  mockDoc,
			resourceConfig: &ResourceConfig{
				Verbs: []Verb{
					{Action: "get", Path: "/items", Method: "get"},
					{Action: "put", Path: "/items/{id}", Method: "put"},
				},
				ConfigurationFields: []ConfigurationField{
					{
						FromOpenAPI:        FromOpenAPI{Name: "api-version", In: "query"},
						FromRestDefinition: FromRestDefinition{Actions: []string{"get"}},
					},
					{
						FromOpenAPI:        FromOpenAPI{Name: "X-Request-ID", In: "header"},
						FromRestDefinition: FromRestDefinition{Actions: []string{"get"}},
					},
					{
						FromOpenAPI:        FromOpenAPI{Name: "id", In: "path"},
						FromRestDefinition: FromRestDefinition{Actions: []string{"put"}},
					},
				},
			},
			expectedSchemaPaths: map[string]string{
				"properties.configuration.properties.query.properties.get.properties.api-version.type":   "string",
				"properties.configuration.properties.header.properties.get.properties.X-Request-ID.type": "string",
				"properties.configuration.properties.path.properties.put.properties.id.type":             "integer",
			},
		},
		{
			name: "Multiple Actions for a Single Config Field",
			doc:  mockDoc,
			resourceConfig: &ResourceConfig{
				Verbs: []Verb{
					{Action: "get", Path: "/items", Method: "get"},
					{Action: "put", Path: "/items/{id}", Method: "put"},
				},
				ConfigurationFields: []ConfigurationField{
					{
						FromOpenAPI:        FromOpenAPI{Name: "api-version", In: "query"},
						FromRestDefinition: FromRestDefinition{Actions: []string{"get", "put"}},
					},
				},
			},
			expectedSchemaPaths: map[string]string{
				"properties.configuration.properties.query.properties.get.properties.api-version.type": "string",
				"properties.configuration.properties.query.properties.put.properties.api-version.type": "string",
			},
		},
		{
			name: "Authentication Only",
			doc:  mockDoc,
			resourceConfig: &ResourceConfig{
				Verbs:               []Verb{},
				ConfigurationFields: []ConfigurationField{},
			},
			expectedSchemaPaths: map[string]string{
				"properties.authentication.properties.bearer.type": "object",
				"properties.authentication.properties.basic.type":  "object",
			},
		},
		{
			name: "Combined Parameters and Authentication",
			doc:  mockDoc,
			resourceConfig: &ResourceConfig{
				Verbs: []Verb{
					{Action: "get", Path: "/items", Method: "get"},
				},
				ConfigurationFields: []ConfigurationField{
					{
						FromOpenAPI:        FromOpenAPI{Name: "api-version", In: "query"},
						FromRestDefinition: FromRestDefinition{Actions: []string{"get"}},
					},
				},
			},
			expectedSchemaPaths: map[string]string{
				"properties.configuration.properties.query.properties.get.properties.api-version.type": "string",
				"properties.authentication.properties.bearer.type":                                     "object",
			},
		},
		{
			name: "Empty Case - No Fields and No Auth",
			doc:  &mockOASDocument{},
			resourceConfig: &ResourceConfig{
				Verbs:               []Verb{},
				ConfigurationFields: []ConfigurationField{},
			},
			expectedSchemaPaths: nil,
		},
		{
			name: "should gracefully skip configuration fields for non-existent parameters",
			doc:  mockDoc,
			resourceConfig: &ResourceConfig{
				Verbs: []Verb{
					{Action: "get", Path: "/items", Method: "get"},
				},
				ConfigurationFields: []ConfigurationField{
					{
						FromOpenAPI:        FromOpenAPI{Name: "non-existent-param", In: "query"},
						FromRestDefinition: FromRestDefinition{Actions: []string{"get"}},
					},
					{
						FromOpenAPI:        FromOpenAPI{Name: "api-version", In: "query"},
						FromRestDefinition: FromRestDefinition{Actions: []string{"get"}},
					},
				},
			},
			expectedSchemaPaths: map[string]string{
				"properties.configuration.properties.query.properties.get.properties.api-version.type": "string",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			generator := NewOASSchemaGenerator(tc.doc, DefaultGeneratorConfig(), tc.resourceConfig)

			// Act
			schemaBytes, err := generator.BuildConfigurationSchema()

			// Assert
			if tc.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			if tc.expectedSchemaPaths == nil {
				assert.Nil(t, schemaBytes, "Schema bytes should be nil for this test case")
				return
			}

			require.NotNil(t, schemaBytes, "Schema bytes should not be nil for this test case")

			var schemaMap map[string]interface{}
			err = json.Unmarshal(schemaBytes, &schemaMap)
			require.NoError(t, err, "Generated schema should be valid JSON")

			for path, expectedType := range tc.expectedSchemaPaths {
				keys := strings.Split(path, ".")
				val, ok := getNestedValue(schemaMap, keys...)
				assert.True(t, ok, "Expected path should exist in schema: %s", path)
				assert.Equal(t, expectedType, val, "Expected type mismatch at path: %s", path)
			}
		})
	}
}

// getNestedValue is a helper to traverse a nested map[string]interface{}
func getNestedValue(data map[string]interface{}, path ...string) (interface{}, bool) {
	var current interface{} = data
	for _, key := range path {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = m[key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// TestAPIKeySecurityScheme covers oasgen-provider#49: an OAS `type: apiKey, in: header` scheme must produce
// a usable credential field. Before this it produced nothing at all — the Configuration CRD had no
// `authentication` block, so there was no way to supply a credential and every request went out
// unauthenticated, with no warning.
func TestAPIKeySecurityScheme(t *testing.T) {
	apiKey := func(name, header string) SecuritySchemeInfo {
		return SecuritySchemeInfo{Name: name, Type: SchemeTypeAPIKey, In: "header", ParamName: header}
	}

	t.Run("keys by scheme KIND, not the empty Scheme field", func(t *testing.T) {
		// SecuritySchemeInfo.Scheme is populated only for type: http, so keying by it would name this
		// property "" — the bug this guards.
		k, ok := authMethodKey(apiKey("ApiKeyAuth", "Authorization"))
		require.True(t, ok)
		assert.Equal(t, "apiKey", k)
		k, ok = authMethodKey(SecuritySchemeInfo{Type: SchemeTypeHTTP, Scheme: "bearer"})
		require.True(t, ok)
		assert.Equal(t, "bearer", k)
	})

	t.Run("apiKey in query or cookie stays unsupported", func(t *testing.T) {
		for _, in := range []string{"query", "cookie"} {
			_, ok := authMethodKey(SecuritySchemeInfo{Name: "K", Type: SchemeTypeAPIKey, In: in})
			assert.False(t, ok, "in: %s must not be silently accepted", in)
		}
	})

	t.Run("single scheme defaults the header from the document", func(t *testing.T) {
		sch, err := createSchemaForSecurityScheme(apiKey("ApiKeyAuth", "Authorization"), "Authorization")
		require.NoError(t, err)
		var hdr *Schema
		for _, p := range sch.Properties {
			if p.Name == "header" {
				hdr = p.Schema
			}
		}
		require.NotNil(t, hdr, "the generated shape must expose a header field")
		assert.Equal(t, "Authorization", hdr.Default, "zero-config for an unambiguous document")
	})

	t.Run("several schemes leave header undefaulted so the author chooses", func(t *testing.T) {
		sch, err := createSchemaForSecurityScheme(apiKey("ApiKeyAuth", "Authorization"), "")
		require.NoError(t, err)
		for _, p := range sch.Properties {
			if p.Name == "header" {
				assert.Nil(t, p.Schema.Default, "no single correct default when the document is ambiguous")
				assert.Contains(t, p.Schema.Description, "more than one apiKey scheme")
			}
		}
	})

	t.Run("unsupported schemes are reported, not dropped", func(t *testing.T) {
		g := &OASSchemaGenerator{docs: NewDocumentSet(&stubSecDoc{schemes: []SecuritySchemeInfo{
			{Name: "OAuth", Type: SchemeTypeOAuth2},
			{Name: "ApiKeyAuth", Type: SchemeTypeAPIKey, In: "header", ParamName: "X-Api-Key"},
		}})}
		m, skipped, err := g.buildAuthMethodsSchemaMap()
		require.NoError(t, err)
		assert.Contains(t, m, "apiKey", "the supported one is still generated")
		require.Len(t, skipped, 1)
		assert.Contains(t, skipped[0], "OAuth", "the skipped one is named")
	})

	t.Run("two apiKey schemes collapse to one key with no default", func(t *testing.T) {
		g := &OASSchemaGenerator{docs: NewDocumentSet(&stubSecDoc{schemes: []SecuritySchemeInfo{
			apiKey("A", "Authorization"), apiKey("B", "X-Api-Key"),
		}})}
		m, skipped, err := g.buildAuthMethodsSchemaMap()
		require.NoError(t, err)
		assert.Empty(t, skipped)
		require.Len(t, m, 1, "one apiKey property, not two")
		for _, p := range m["apiKey"].Properties {
			if p.Name == "header" {
				assert.Nil(t, p.Schema.Default, "must not arbitrarily pick one vendor scheme's header")
			}
		}
	})
}

type stubSecDoc struct{ schemes []SecuritySchemeInfo }

func (s *stubSecDoc) FindPath(string) (PathItem, bool)      { return nil, false }
func (s *stubSecDoc) SecuritySchemes() []SecuritySchemeInfo { return s.schemes }
func (s *stubSecDoc) Version() string                       { return "1.0" }
