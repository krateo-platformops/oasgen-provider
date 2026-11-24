package oas2jsonschema

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAreTypesCompatible(t *testing.T) {
	testCases := []struct {
		name     string
		types1   []string
		types2   []string
		expected bool
	}{
		{
			name:     "Identical primary types",
			types1:   []string{"string"},
			types2:   []string{"string"},
			expected: true,
		},
		{
			name:     "Identical primary types with null",
			types1:   []string{"object", "null"},
			types2:   []string{"object", "null"},
			expected: true,
		},
		{
			name:     "Different primary types",
			types1:   []string{"string"},
			types2:   []string{"integer"},
			expected: false,
		},
		{
			name:     "One primary type, one null",
			types1:   []string{"string", "null"},
			types2:   []string{"null"},
			expected: false,
		},
		{
			name:     "One primary type (not nullable), one null",
			types1:   []string{"string"},
			types2:   []string{"null"},
			expected: false,
		},
		{
			name:     "One null, one primary type (not nullable)",
			types1:   []string{"null"},
			types2:   []string{"boolean"},
			expected: false,
		},
		{
			name:     "Both empty",
			types1:   []string{},
			types2:   []string{},
			expected: true,
		},
		{
			name:     "One empty, one with primary type",
			types1:   []string{},
			types2:   []string{"string"},
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := areTypesCompatible(tc.types1, tc.types2)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

func TestPrepareSchemaForCRD_WithRecursionGuard(t *testing.T) {
	// Get the default config to make the test robust against changes in default values.
	defaultConfig := DefaultGeneratorConfig()
	limit := defaultConfig.MaxRecursionDepth

	testCases := []struct {
		name                  string
		depth                 int
		expectError           bool
		expectedErrorContains string
	}{
		{
			name:                  "should return an error when max recursion depth is exceeded",
			depth:                 limit + 1,
			expectError:           true,
			expectedErrorContains: "recursion limit exceeded",
		},
		{
			name:        "should correctly prepare a deeply nested schema well within the recursion limit",
			depth:       limit / 2,
			expectError: false,
		},
		{
			name:        "should succeed at the exact recursion depth limit",
			depth:       limit,
			expectError: false,
		},
		{
			name:        "should succeed with a single level of nesting",
			depth:       1,
			expectError: false,
		},
		{
			name:        "should succeed with no nesting",
			depth:       0,
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange: Create a schema with the specified depth.
			root := &Schema{Type: []string{"object"}}
			current := root

			for i := 0; i < tc.depth; i++ {
				next := &Schema{Type: []string{"object"}}
				current.Properties = []Property{
					{Name: "nested", Schema: next},
				}
				current = next
			}

			// Act
			err := prepareSchemaForCRD(root, defaultConfig)

			// Assert
			if tc.expectError {
				require.Error(t, err, "Expected an error but got none")
				assert.True(t, strings.Contains(err.Error(), tc.expectedErrorContains),
					"Error message should contain '%s'", tc.expectedErrorContains)
			} else {
				require.NoError(t, err, "Expected no error but got one")

				// Also, verify that the schema structure is preserved and wasn't truncated.
				var finalDepth int
				current = root
				for len(current.Properties) > 0 && current.Properties[0].Schema != nil {
					finalDepth++
					current = current.Properties[0].Schema
				}
				assert.Equal(t, tc.depth, finalDepth, "The final schema depth should be preserved")
			}
		})
	}
}

func TestPrepareSchemaForCRDWithVisited_MergeAllOfProperties(t *testing.T) {
	schema := &Schema{
		Type: []string{"object"},
		AllOf: []*Schema{
			{
				Properties: []Property{
					{Name: "prop1", Schema: &Schema{Type: []string{"string"}}},
					{Name: "prop2", Schema: &Schema{Type: []string{"integer"}}},
				},
				Required: []string{"prop1"},
			},
			{
				Properties: []Property{
					{Name: "prop2", Schema: &Schema{Type: []string{"number"}}}, // Duplicate property
					{Name: "prop3", Schema: &Schema{Type: []string{"boolean"}}},
				},
				Required: []string{"prop3"},
			},
		},
	}

	err := prepareSchemaForCRD(schema, DefaultGeneratorConfig())
	require.NoError(t, err, "prepareSchemaForCRD should not return an error")

	// Verify that properties are merged correctly
	expectedProperties := map[string]string{
		"prop1": "string",
		"prop2": "integer", // First occurrence should be kept
		"prop3": "boolean",
	}

	require.Equal(t, len(expectedProperties), len(schema.Properties), "Number of properties should match")

	for _, prop := range schema.Properties {
		expectedType, exists := expectedProperties[prop.Name]
		require.True(t, exists, "Unexpected property: %s", prop.Name)
		require.Equal(t, expectedType, prop.Schema.Type[0], "Property %s should have type %s", prop.Name, expectedType)
	}

	// Verify that required fields are merged correctly
	expectedRequired := []string{"prop1", "prop3"}
	require.Equal(t, len(expectedRequired), len(schema.Required), "Number of required fields should match")
	for _, req := range expectedRequired {
		require.Contains(t, schema.Required, req, "Required fields should contain %s", req)
	}
}

// Example reference: SubnetType in ArubaCloud Subnet schema
func TestPrepareSchemaForCRDWithVisited_MergeAllOfEnumOnlySchemas(t *testing.T) {
	schema := &Schema{
		Type: []string{"string"},
		AllOf: []*Schema{
			{
				Enum: []interface{}{"value1", "value2"},
			},
			{
				Enum: []interface{}{"value2", "value3"},
			},
		},
	}

	err := prepareSchemaForCRD(schema, DefaultGeneratorConfig())
	require.NoError(t, err, "prepareSchemaForCRD should not return an error")

	// Verify that enum values are merged correctly
	expectedEnum := []interface{}{"value1", "value2", "value3"}
	require.Equal(t, len(expectedEnum), len(schema.Enum), "Number of enum values should match")

	enumMap := make(map[interface{}]bool)
	for _, val := range schema.Enum {
		enumMap[val] = true
	}

	for _, expectedVal := range expectedEnum {
		require.True(t, enumMap[expectedVal], "Enum values should contain %v", expectedVal)
	}
}

// TestAdditionalProperties_ObjectForm is the regression for #45: a typed free-form map
// (additionalProperties: {type: string}) must survive OAS -> JSON-schema conversion. Only the boolean form
// used to, so Kubernetes-idiomatic annotation/label maps reached the CRD untyped or not at all.
func TestAdditionalProperties_ObjectForm(t *testing.T) {
	cfg := DefaultGeneratorConfig()

	t.Run("object form is emitted as the value sub-schema", func(t *testing.T) {
		schema := &Schema{
			Type: []string{"object"},
			AdditionalProperties: &AdditionalProperties{
				Schema: &Schema{Type: []string{"string"}},
			},
		}
		m, err := schemaToMap(schema, cfg)
		require.NoError(t, err)
		ap, ok := m["additionalProperties"].(map[string]interface{})
		require.True(t, ok, "expected a value sub-schema, got %#v", m["additionalProperties"])
		assert.Equal(t, "string", ap["type"], "the value type must survive — this is what map[string]string means")
	})

	t.Run("boolean form still emits true", func(t *testing.T) {
		m, err := schemaToMap(&Schema{Type: []string{"object"}, AdditionalProperties: &AdditionalProperties{Bool: true}}, cfg)
		require.NoError(t, err)
		assert.Equal(t, true, m["additionalProperties"])
	})

	t.Run("false and absent both emit nothing", func(t *testing.T) {
		for _, ap := range []*AdditionalProperties{{Bool: false}, nil} {
			m, err := schemaToMap(&Schema{Type: []string{"object"}, AdditionalProperties: ap}, cfg)
			require.NoError(t, err)
			_, present := m["additionalProperties"]
			assert.False(t, present, "additionalProperties: false is the default and need not be emitted")
		}
	})

	t.Run("nested value schema survives, not just scalars", func(t *testing.T) {
		schema := &Schema{
			Type: []string{"object"},
			AdditionalProperties: &AdditionalProperties{Schema: &Schema{
				Type:       []string{"object"},
				Properties: []Property{{Name: "port", Schema: &Schema{Type: []string{"integer"}, Format: "int64"}}},
			}},
		}
		m, err := schemaToMap(schema, cfg)
		require.NoError(t, err)
		ap := m["additionalProperties"].(map[string]interface{})
		props := ap["properties"].(map[string]interface{})
		port := props["port"].(map[string]interface{})
		assert.Equal(t, "integer", port["type"])
		assert.Equal(t, "int64", port["format"], "int64 must not be downgraded inside a map value either (cfbe7fc)")
	})

	t.Run("deepCopy copies the value schema rather than aliasing it", func(t *testing.T) {
		orig := &Schema{Type: []string{"object"}, AdditionalProperties: &AdditionalProperties{Schema: &Schema{Type: []string{"string"}}}}
		cp := orig.deepCopy()
		require.NotNil(t, cp.AdditionalProperties)
		require.NotNil(t, cp.AdditionalProperties.Schema)
		assert.NotSame(t, orig.AdditionalProperties, cp.AdditionalProperties, "struct must be copied")
		assert.NotSame(t, orig.AdditionalProperties.Schema, cp.AdditionalProperties.Schema, "value schema must be copied")
		cp.AdditionalProperties.Schema.Type = []string{"integer"}
		assert.Equal(t, []string{"string"}, orig.AdditionalProperties.Schema.Type, "mutating the copy must not touch the original")
	})
}

// TestNumberSurvivesAsNumber asserts the thing the removed conversion prevented: an OpenAPI `number`
// reaches the CRD as `number`, so a fractional bound a vendor documents can actually be declared.
//
// WHY THE CONVERSION WENT. Its only stated reason was a comment -- "for K8s CRD compatibility" -- and
// the commit that introduced it (cce5252, 2025-07-28, "fix: added handling of number type in oas")
// records none; it carried fmt.Printf debug lines, so it was written to make something work. Both
// halves that reason could have meant are now measured false, by consumers:
//
//   - crdgen accepts it: GenerateWithDiagnostics on the 0.91.1 tree (plumbing v1.93.0 -- the version
//     this module ships, not a later one) returns type: number with no error and no warning
//   - the apiserver accepts it: the generated CRD Established on GKE 1.36.4, stored 0.75, refused 0.1
//     below a 0.5 minimum, and refused a string
//
// MEASURED CONSUMER IMPACT: a GitHub ruleset's minimum_coverage and max_coverage_drop are `number`,
// GitHub accepts and stores 80.5, and the integer CRD could not carry it -- documented as a product
// constraint in that consumer's own chart rather than filed, which is how it survived 32 kinds.
//
// NOT A REGRESSION RISK FOR STORED DATA, because it WIDENS: 80 is a valid JSON Schema number, so every
// value admissible before stays admissible, and no CR can carry a fraction today because the integer
// refused it at admission. The comparer is unaffected: whole numbers normalise to int on both sides.
func TestNumberSurvivesAsNumber(t *testing.T) {
	schema := &Schema{
		Type: []string{"object"},
		Properties: []Property{
			{Name: "minimum_coverage", Schema: &Schema{Type: []string{"number"}}},
			{Name: "retries", Schema: &Schema{Type: []string{"integer"}}},
			{Name: "name", Schema: &Schema{Type: []string{"string"}}},
		},
	}

	require.NoError(t, prepareSchemaForCRD(schema, DefaultGeneratorConfig()))

	got := map[string]string{}
	for _, p := range schema.Properties {
		got[p.Name] = p.Schema.Type[0]
	}

	assert.Equal(t, "number", got["minimum_coverage"],
		"an OpenAPI number must reach the CRD as number; converting it to integer refuses the "+
			"fractional values the vendor documents, accepts and stores")

	// CONTROLS: the neighbours must be untouched, or this arm would pass on a function that stopped
	// transforming anything at all.
	assert.Equal(t, "integer", got["retries"], "control: a declared integer stays an integer")
	assert.Equal(t, "string", got["name"], "control: a declared string is unaffected")
}
