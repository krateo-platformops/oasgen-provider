package oas2jsonschema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSchemaDeepCopyCopiesEveryField is the guard for the whole class of bug #249 belongs to.
//
// deepCopyRec assigns fields ONE BY ONE, by hand. A field added to Schema and forgotten here is dropped
// silently, and the symptom appears nowhere near the mistake: the spec base schema is deep-copied, so the
// loss shows up as a constraint missing from a generated CRD, layers away, with nothing to point at. That
// is how #249 presented -- a `pattern` read correctly off the document, carried into the domain type, and
// then lost, which took a live-cluster investigation on someone else's machine to notice.
//
// Reflection rather than an enumerated list, deliberately: a hand-written list of fields to check is the
// same construct that failed, and would need the same discipline to keep current.
func TestSchemaDeepCopyCopiesEveryField(t *testing.T) {
	// Every field set to a distinctive non-zero value. A field left at its zero value here would make the
	// test vacuous for that field, so the count is asserted below.
	minLen, maxLen := int64(3), int64(255)
	minItems, maxItems := int64(1), int64(5)
	minimum, maximum, multipleOf := 1.5, 99.5, 0.5

	original := &Schema{
		Type:                 []string{"object"},
		Description:          "d",
		Properties:           []Property{{Name: "p", Schema: &Schema{Type: []string{"string"}, Pattern: "^x$"}}},
		Items:                &Schema{Type: []string{"string"}},
		AllOf:                []*Schema{{Type: []string{"object"}}},
		Required:             []string{"p"},
		Default:              "dflt",
		Enum:                 []interface{}{"a", "b"},
		AdditionalProperties: &AdditionalProperties{Bool: true},
		MaxProperties:        7,
		Format:               "int64",
		Extensions:           map[string]interface{}{"x-foo": "bar"},
		Pattern:              "^(heads|tags)/.+",
		MinLength:            &minLen,
		MaxLength:            &maxLen,
		Minimum:              &minimum,
		Maximum:              &maximum,
		ExclusiveMinimum:     true,
		ExclusiveMaximum:     true,
		MultipleOf:           &multipleOf,
		MinItems:             &minItems,
		MaxItems:             &maxItems,
		UniqueItems:          true,
	}

	// Fail loudly if Schema grows a field this fixture does not set — otherwise the test silently stops
	// covering it, which is precisely the failure mode being guarded.
	v := reflect.ValueOf(*original)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if v.Field(i).IsZero() {
			t.Errorf("field %q is zero in this fixture, so the deep-copy check does not cover it; set it", name)
		}
	}

	copied := original.deepCopy()
	require.NotNil(t, copied)

	// Compare field by field so a failure names the field rather than dumping two structs.
	oc, cc := reflect.ValueOf(*original), reflect.ValueOf(*copied)
	for i := 0; i < oc.NumField(); i++ {
		name := oc.Type().Field(i).Name
		if cc.Field(i).IsZero() && !oc.Field(i).IsZero() {
			t.Errorf("deepCopy dropped field %q: add it to deepCopyRec", name)
		}
	}

	// And the values must match, not merely be present.
	assert.Equal(t, original.Pattern, copied.Pattern)
	require.NotNil(t, copied.MinLength)
	assert.Equal(t, minLen, *copied.MinLength)
	require.NotNil(t, copied.Minimum)
	assert.Equal(t, minimum, *copied.Minimum)
	assert.True(t, copied.UniqueItems)
	assert.True(t, copied.ExclusiveMinimum)

	// Pointers must not be shared: the spec base schema is mutated after copying (validatePatterns clears
	// an uncompilable pattern, prepareSchemaForCRD rewrites types), and aliasing would let one
	// resource's generation alter the parsed document other resources read.
	assert.NotSame(t, original.MinLength, copied.MinLength, "MinLength must be a fresh pointer, not an alias")
	assert.NotSame(t, original.Minimum, copied.Minimum, "Minimum must be a fresh pointer, not an alias")
}

const keywordDoc = `
openapi: 3.0.0
info: {title: t, version: "1.0"}
paths:
  /refs:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [ref]
              properties:
                ref:   {type: string, pattern: '^(heads|tags)/.+', minLength: 3, maxLength: 255}
                count: {type: integer, minimum: 1, maximum: 100, multipleOf: 1}
                tags:  {type: array, minItems: 1, maxItems: 5, uniqueItems: true, items: {type: string, pattern: '^[a-z]+$'}}
                bad:   {type: string, pattern: '(a)\1'}
      responses:
        '201': {description: ok, content: {application/json: {schema: {type: object, properties: {ref: {type: string}}}}}}
  /refs/{ref}:
    get:
      parameters: [{name: ref, in: path, required: true, schema: {type: string}}]
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: object, properties: {ref: {type: string}}}}}}
`

func keywordSpec(t *testing.T) (map[string]map[string]any, []string) {
	t.Helper()
	doc, err := NewLibOASParser().Parse([]byte(keywordDoc))
	require.NoError(t, err)

	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Verbs: []Verb{
			{Action: ActionCreate, Method: "POST", Path: "/refs"},
			{Action: ActionGet, Method: "GET", Path: "/refs/{ref}"},
		},
		Identifiers: []string{"ref"},
	})
	raw, warnings, err := g.BuildSpecSchema()
	require.NoError(t, err)

	var parsed struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &parsed))

	var msgs []string
	for _, w := range warnings {
		msgs = append(msgs, w.Error())
	}
	return parsed.Properties, msgs
}

// TestValidationKeywordsReachTheSpecSchema covers #249: a constraint the document declares must be
// enforced, not merely read. Before this, every one of these was dropped at the adapter boundary, so the
// document looked as though it declared a constraint nothing applied.
func TestValidationKeywordsReachTheSpecSchema(t *testing.T) {
	props, _ := keywordSpec(t)

	assert.Equal(t, "^(heads|tags)/.+", props["ref"]["pattern"])
	assert.EqualValues(t, 3, props["ref"]["minLength"])
	assert.EqualValues(t, 255, props["ref"]["maxLength"])

	assert.EqualValues(t, 1, props["count"]["minimum"])
	assert.EqualValues(t, 100, props["count"]["maximum"])
	assert.EqualValues(t, 1, props["count"]["multipleOf"])

	assert.EqualValues(t, 1, props["tags"]["minItems"])
	assert.EqualValues(t, 5, props["tags"]["maxItems"])
	assert.Equal(t, true, props["tags"]["uniqueItems"],
		"emitted as uniqueItems here; plumbing's transpiler converts it to x-kubernetes-list-type: set, which is the apiserver-enforced form")

	// Nested through items, which is a separate recursion path.
	items, ok := props["tags"]["items"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "^[a-z]+$", items["pattern"])
}

// TestAnUncompilablePatternIsDroppedWithAWarning is the safety half.
//
// Kubernetes compiles CRD patterns as RE2 while OpenAPI specifies ECMA-262, so a backreference is valid in
// the document and rejected by the apiserver — which refuses the ENTIRE CustomResourceDefinition, not just
// that validation. Dropping the pattern costs one constraint; emitting it costs the whole resource.
func TestAnUncompilablePatternIsDroppedWithAWarning(t *testing.T) {
	props, msgs := keywordSpec(t)

	_, present := props["bad"]["pattern"]
	assert.False(t, present, "an RE2-invalid pattern must not be emitted, got %v", props["bad"]["pattern"])

	var hit string
	for _, m := range msgs {
		if strings.Contains(m, "RE2") {
			hit = m
		}
	}
	require.NotEmpty(t, hit, "expected a warning explaining the drop, got %v", msgs)
	assert.Contains(t, hit, "spec.bad", "the warning must name the path so the author can fix it")
	assert.Contains(t, hit, "refuse the entire", "the warning should say why it was dropped rather than emitted")
}
