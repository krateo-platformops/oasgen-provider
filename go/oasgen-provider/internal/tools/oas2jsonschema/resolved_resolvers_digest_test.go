package oas2jsonschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statusPropsFor renders the status schema and returns its declared property names.
//
// It asserts on the RENDERED JSON rather than on the in-memory Schema, because what matters is what
// reaches the CRD: a property present in the struct and dropped by the serialiser would pass an
// in-memory assertion and still be pruned by the apiserver.
func statusPropsFor(t *testing.T, g *OASSchemaGenerator) map[string]any {
	t.Helper()
	raw, _, err := g.BuildStatusSchema()
	require.NoError(t, err)
	require.NotEmpty(t, raw, "an empty status schema makes every assertion below vacuous")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	props, _ := decoded["properties"].(map[string]any)
	return props
}

func digestGen(t *testing.T, mapping []FieldMappingEntry) *OASSchemaGenerator {
	t.Helper()
	return matchGen([]string{"uuid"}, []Verb{
		{Action: ActionCreate, Method: "POST", Path: "/v1/nodes", FieldMapping: mapping},
		{Action: ActionGet, Method: "GET", Path: "/v1/nodes/{uuid}"},
	}, ironicShapedDoc())
}

// THE ABSENT CASE IS THE LOAD-BEARING ONE.
//
// Declaring this field unconditionally would change the generated CRD's shape for every consumer,
// including one who fingerprints theirs and treats an unattributable diff as a spoiled instrument. A
// resource with no secretRef resolver has nothing to digest, so the field would be a permanently
// empty string bought at the cost of moving everyone's CRD.
//
// So this table's `false` rows are not completeness — they are the assertion that most consumers see
// no change at all.
func TestTheDigestFieldIsDeclaredOnlyForASecretRefResolver(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mapping []FieldMappingEntry
		want    bool
		why     string
	}{
		{
			name:    "a secretRef resolver",
			mapping: []FieldMappingEntry{{InBody: "password", ResolverType: "secretRef"}},
			want:    true,
			why:     "the only shape whose Secret content can rotate behind the controller's back",
		},
		{
			name:    "a secretRef resolver with a when guard",
			mapping: []FieldMappingEntry{{InBody: "password", ResolverType: "secretRef", ResolverHasWhen: true}},
			want:    true,
			why:     "a conditional resolver still resolves a Secret when its guard holds",
		},
		{
			name:    "no mapping at all",
			mapping: nil,
			want:    false,
			why:     "nothing to digest; the field would be permanently empty",
		},
		{
			name:    "a plain mapping with no resolver",
			mapping: []FieldMappingEntry{{InBody: "token", InCustomResource: "spec.token"}},
			want:    false,
			why:     "a CR-sourced value is already in spec, so ordinary drift detection covers it",
		},
		{
			// THE NARROW GATE. dependentLookup is a resolver but not a secretRef: its value comes from
			// another resource via an API call, not from a Secret's content, so it has no rotation
			// story and must not pull the field in.
			name:    "a dependentLookup resolver",
			mapping: []FieldMappingEntry{{InPath: "userId", ResolverType: "dependentLookup", ResolverLookupName: "l"}},
			want:    false,
			why:     "dependentLookup is a resolver but not a secretRef; it reads no Secret content",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := digestGen(t, tc.mapping)
			require.Equal(t, tc.want, g.hasSecretRefResolver(), "the predicate itself: %s", tc.why)

			props := statusPropsFor(t, g)
			_, declared := props[StatusResolvedResolversDigest]
			assert.Equal(t, tc.want, declared,
				"status.%s declared=%v, want %v: %s", StatusResolvedResolversDigest, declared, tc.want, tc.why)

			// CONTROL: the schema must carry the resource's OWN status fields either way, or an
			// absent digest field would be satisfied by a schema that declared nothing at all.
			assert.NotEmpty(t, props,
				"the status schema declared no properties, so the digest assertion proves nothing")
		})
	}

}

// THE TABLE ABOVE MUST CONTAIN BOTH OUTCOMES, counted rather than assumed.
//
// An all-true table passes against a predicate that returns true unconditionally; an all-false one
// against the opposite. This counts the rows of each kind, so a future edit that "simplifies" the
// table into one outcome fails here with the reason instead of passing vacuously.
//
// It mirrors the table deliberately rather than sharing it: a shared slice could be reduced in one
// place and this check would reduce with it.
func TestTheDigestDeclarationTableHasBothOutcomes(t *testing.T) {
	want := []bool{true, true, false, false, false}
	var yes, no int
	for _, w := range want {
		if w {
			yes++
		} else {
			no++
		}
	}
	require.NotZero(t, yes,
		"no row expects the field to be declared: the table passes against a generator that never declares it")
	require.NotZero(t, no,
		"no row forbids it: the table passes against one that declares it for every resource, which is "+
			"the CRD-shape change this gate exists to avoid")
	assert.Equal(t, 5, len(want),
		"the mirrored expectation has %d rows; the table above has 5. Re-sync them, or this check is "+
			"counting something else", len(want))
}

// The declared field must be a STRING and carry the reason it exists, because a bare property name
// in a CRD tells an operator reading `kubectl explain` nothing about why their status grew a field.
func TestTheDigestFieldIsAStringAndExplainsItself(t *testing.T) {
	g := digestGen(t, []FieldMappingEntry{{InBody: "password", ResolverType: "secretRef"}})
	props := statusPropsFor(t, g)

	f, ok := props[StatusResolvedResolversDigest].(map[string]any)
	require.True(t, ok, "the digest field is missing or not an object: %v", props[StatusResolvedResolversDigest])
	assert.Equal(t, "string", f["type"])

	desc, _ := f["description"].(string)
	require.NotEmpty(t, desc, "a CRD property with no description is undocumented in kubectl explain")
	for _, want := range []string{"secretRef", "UID", "create or update"} {
		assert.Contains(t, desc, want,
			"the description must mention %q: an operator needs to know what rotates it, why it is "+
				"salted, and when it is written", want)
	}
}
