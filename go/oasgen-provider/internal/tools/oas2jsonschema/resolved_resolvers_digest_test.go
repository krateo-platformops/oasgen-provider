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

// digestGen builds a create/get/update resource and puts `mapping` on the verb named by `onAction`.
//
// WHICH VERB CARRIES THE MAPPING IS THE WHOLE EXPERIMENT, so it is a parameter rather than a
// constant. An earlier version of this helper always attached the mapping to `create`; against a
// gate that only consults `update` every row would have come back false, and against the previous
// any-verb gate every secretRef row came back true. Neither arrangement can tell the two gates
// apart -- the same mapping has to be placeable on either verb for the table to discriminate.
func digestGen(t *testing.T, onAction string, mapping []FieldMappingEntry) *OASSchemaGenerator {
	t.Helper()
	verbs := []Verb{
		{Action: ActionCreate, Method: "POST", Path: "/v1/nodes"},
		{Action: ActionGet, Method: "GET", Path: "/v1/nodes/{uuid}"},
		{Action: ActionUpdate, Method: "PATCH", Path: "/v1/nodes/{uuid}"},
	}
	var placed bool
	for i := range verbs {
		if verbs[i].Action == onAction {
			verbs[i].FieldMapping = mapping
			placed = true
		}
	}
	require.True(t, placed || mapping == nil,
		"onAction %q matches none of this fixture's verbs, so the mapping was silently dropped and "+
			"the row would pass for the wrong reason", onAction)
	return matchGen([]string{"uuid"}, verbs, ironicShapedDoc())
}

// THE ABSENT CASE IS THE LOAD-BEARING ONE.
//
// Declaring this field unconditionally would change the generated CRD's shape for every consumer,
// including one who fingerprints theirs and treats an unattributable diff as a spoiled instrument. A
// resource whose UPDATE verb has no secretRef resolver has nothing an update could push, so the
// field would be a permanently empty string bought at the cost of moving everyone's CRD.
//
// So this table's `false` rows are not completeness -- they are the assertion that most consumers see
// no change at all.
func TestTheDigestFieldIsDeclaredOnlyForAnUpdatableSecretRefResolver(t *testing.T) {
	for _, tc := range []struct {
		name     string
		onAction string
		mapping  []FieldMappingEntry
		want     bool
		why      string
	}{
		{
			name:     "a secretRef resolver on update",
			onAction: ActionUpdate,
			mapping:  []FieldMappingEntry{{InBody: "password", ResolverType: "secretRef"}},
			want:     true,
			why:      "the only shape whose Secret content can rotate AND be pushed back by an update",
		},
		{
			name:     "a secretRef resolver on update with a when guard",
			onAction: ActionUpdate,
			mapping:  []FieldMappingEntry{{InBody: "password", ResolverType: "secretRef", ResolverHasWhen: true}},
			want:     true,
			why:      "a guard decides whether an INSTANCE resolves it, not whether the kind is in scope",
		},
		{
			// THE DISCRIMINATING ROW: identical mapping to the first, on a different verb.
			name:     "a secretRef resolver on create only",
			onAction: ActionCreate,
			mapping:  []FieldMappingEntry{{InBody: "password", ResolverType: "secretRef"}},
			want:     false,
			why: "an update cannot push this value, so a digest difference in it would be drift no " +
				"update can retire -- one update per poll, per CR, forever",
		},
		{
			name:     "no mapping at all",
			onAction: ActionUpdate,
			mapping:  nil,
			want:     false,
			why:      "nothing to digest; the field would be permanently empty",
		},
		{
			name:     "a plain mapping on update with no resolver",
			onAction: ActionUpdate,
			mapping:  []FieldMappingEntry{{InBody: "token", InCustomResource: "spec.token"}},
			want:     false,
			why:      "a CR-sourced value is already in spec, so ordinary drift detection covers it",
		},
		{
			// THE NARROW TYPE GATE. dependentLookup is a resolver but not a secretRef: its value comes
			// from another resource via an API call, not from a Secret's content, so it has no
			// rotation story and must not pull the field in.
			name:     "a dependentLookup resolver on update",
			onAction: ActionUpdate,
			mapping:  []FieldMappingEntry{{InPath: "userId", ResolverType: "dependentLookup", ResolverLookupName: "l"}},
			want:     false,
			why:      "dependentLookup is a resolver but not a secretRef; it reads no Secret content",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := digestGen(t, tc.onAction, tc.mapping)
			require.Equal(t, tc.want, g.hasUpdatableSecretRefResolver(), "the predicate itself: %s", tc.why)

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

// THE SAME MAPPING, MOVED BETWEEN TWO VERBS, MUST CHANGE THE ANSWER.
//
// Stated as its own test because it is the one property that separates this gate from the any-verb
// gate it replaced, and a reader scanning the table above can miss that two of its rows differ in
// nothing but `onAction`. If a future edit widens the gate back to "any verb", every row of that
// table still passes except one -- and this fails with the reason.
func TestMovingTheSecretRefOffUpdateWithdrawsTheField(t *testing.T) {
	mapping := []FieldMappingEntry{{InBody: "password", ResolverType: "secretRef"}}

	onUpdate := digestGen(t, ActionUpdate, mapping)
	onCreate := digestGen(t, ActionCreate, mapping)

	require.True(t, onUpdate.hasUpdatableSecretRefResolver(),
		"the positive half: a secretRef on update must be in scope, or the negative half below proves nothing")
	require.False(t, onCreate.hasUpdatableSecretRefResolver(),
		"the same mapping on create must NOT be in scope; an update cannot push it")

	_, onUpdateDeclared := statusPropsFor(t, onUpdate)[StatusResolvedResolversDigest]
	_, onCreateDeclared := statusPropsFor(t, onCreate)[StatusResolvedResolversDigest]
	assert.True(t, onUpdateDeclared, "declared for the update-verb resolver")
	assert.False(t, onCreateDeclared, "withdrawn for the create-verb resolver")
}

// The declared field must be a STRING and carry the reason it exists, because a bare property name
// in a CRD tells an operator reading `kubectl explain` nothing about why their status grew a field.
func TestTheDigestFieldIsAStringAndExplainsItself(t *testing.T) {
	g := digestGen(t, ActionUpdate, []FieldMappingEntry{{InBody: "password", ResolverType: "secretRef"}})
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
