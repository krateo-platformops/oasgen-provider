package oas2jsonschema

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findByDoc builds a one-path document whose GET returns `response`. A nil response means an operation
// with no documented responses at all, which is the "cannot read the response" case.
func findByDoc(path string, response *Schema) *mockOASDocument {
	op := &mockOperation{}
	if response != nil {
		op = &mockOperation{Responses: map[int]ResponseInfo{
			200: {Content: map[string]*Schema{"application/json": response}},
		}}
	}
	return &mockOASDocument{Paths: map[string]*mockPathItem{path: {Ops: map[string]Operation{"get": op}}}}
}

func findByVerb(path, policy, itemsPath string) Verb {
	return Verb{
		Action:                 ActionFindBy,
		Method:                 "GET",
		Path:                   path,
		ItemsPath:              itemsPath,
		IdentifiersMatchPolicy: policy,
	}
}

func policyGen(identifiers []string, verbs []Verb, doc *mockOASDocument) *OASSchemaGenerator {
	return &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		resourceConfig:  &ResourceConfig{Identifiers: identifiers, Verbs: verbs},
		docs:            NewDocumentSet(doc),
	}
}

func requireWarning(t *testing.T, err error) SchemaGenerationError {
	t.Helper()
	require.Error(t, err, "a findby that can adopt the wrong object must be reported")
	var sge SchemaGenerationError
	require.True(t, errors.As(err, &sge), "the warning must be a structured SchemaGenerationError, got %T", err)
	assert.Equal(t, CodeIdentifiersMatchPolicyOmitted, sge.Code)
	return sge
}

// TestTheMeasuredCaseIsWarned is #444 reduced to its shape: several identifiers, no policy, and a findby
// over a real collection. This is the configuration under which a resource naming one child adopted its
// PARENT on a live fleet and the parent was then deleted.
func TestTheMeasuredCaseIsWarned(t *testing.T) {
	const p = "/admin/realms/{realm}/authentication/flows/{flowAlias}/executions"
	g := policyGen(
		[]string{"realm", "flowAlias", "provider"},
		[]Verb{findByVerb(p, "", "")},
		findByDoc(p, obj(
			prop("executions", arr(obj(prop("alias", scalar("string"))))),
		)),
	)

	sge := requireWarning(t, g.warnIdentifiersMatchPolicyOmitted())

	assert.Equal(t, "spec.resource.verbsDescription[0].identifiersMatchPolicy", sge.Path,
		"the warning must name the field to set, at the verb that owns it")
	assert.Contains(t, sge.Message, "realm, flowAlias, provider",
		"naming the identifiers is the point: the reader has to see WHICH ones any single match would be enough for")
	assert.Contains(t, sge.Message, "ANY ONE")
	assert.Contains(t, sge.Message, "identifiersMatchPolicy: AND")
}

// TestAFindByThatCanOnlyReturnOneObjectIsNotWarned is the arm that encodes the census correction, and the
// one that goes red if anyone "simplifies" the gate back to counting identifiers.
//
// Adopting the WRONG item requires there to be another item. This path fully identifies one object, so
// the response is a single object, which rest-dynamic-controller wraps as a list of exactly one
// (ExtractItemsFromResponse case 3) -- OR has nothing else to pick. Two of oasgen's own shipped samples
// are exactly this shape, and an ungated rule flags both of them: of the three policy-omitting
// definitions found across 2653 RestDefinitions, two cannot mis-adopt at all.
func TestAFindByThatCanOnlyReturnOneObjectIsNotWarned(t *testing.T) {
	const p = "/orgs/{org}/actions/runner-groups/{runner_group_id}"
	g := policyGen(
		[]string{"org", "runner_group_id"},
		[]Verb{findByVerb(p, "", "")},
		findByDoc(p, obj(
			prop("id", scalar("integer")),
			prop("name", scalar("string")),
		)),
	)

	assert.NoError(t, g.warnIdentifiersMatchPolicyOmitted(),
		"a single-object findby cannot adopt a different object; warning here is a false positive on provably safe input")
}

// A bare-array response and a declared itemsPath are both collections, by the same rule the generator
// already uses to find the item schema.
func TestEveryCollectionShapeIsWarned(t *testing.T) {
	const p = "/widgets"

	t.Run("bare array", func(t *testing.T) {
		g := policyGen([]string{"a", "b"}, []Verb{findByVerb(p, "", "")},
			findByDoc(p, arr(obj(prop("a", scalar("string"))))))
		requireWarning(t, g.warnIdentifiersMatchPolicyOmitted())
	})

	t.Run("declared itemsPath", func(t *testing.T) {
		g := policyGen([]string{"a", "b"}, []Verb{findByVerb(p, "", "data.items")},
			findByDoc(p, obj(prop("data", obj(prop("items", arr(obj(prop("a", scalar("string"))))))))))
		requireWarning(t, g.warnIdentifiersMatchPolicyOmitted())
	})

	t.Run("envelope with two arrays", func(t *testing.T) {
		// Ambiguous WHICH collection, but certainly a collection.
		g := policyGen([]string{"a", "b"}, []Verb{findByVerb(p, "", "")},
			findByDoc(p, obj(
				prop("data", arr(obj(prop("a", scalar("string"))))),
				prop("included", arr(obj(prop("b", scalar("string"))))),
			)))
		requireWarning(t, g.warnIdentifiersMatchPolicyOmitted())
	})
}

// TestAnUnreadableResponseIsWarned pins the asymmetry deliberately: a response the document does not
// describe is an inability to PROVE the response is a single object, not a proof that it is one. Staying
// quiet here would hide the hazard on exactly the documents that describe their responses least.
func TestAnUnreadableResponseIsWarned(t *testing.T) {
	const p = "/widgets"
	g := policyGen([]string{"a", "b"}, []Verb{findByVerb(p, "", "")}, findByDoc(p, nil))
	requireWarning(t, g.warnIdentifiersMatchPolicyOmitted())
}

// The controls. Each of these must stay silent, and each would be a distinct way to get the gate wrong.
func TestTheWarningStaysSilentWhenThereIsNothingToSay(t *testing.T) {
	const p = "/widgets"
	collection := func() *mockOASDocument {
		return findByDoc(p, obj(prop("items", arr(obj(prop("a", scalar("string")))))))
	}

	t.Run("policy declared AND", func(t *testing.T) {
		g := policyGen([]string{"a", "b"}, []Verb{findByVerb(p, "AND", "")}, collection())
		assert.NoError(t, g.warnIdentifiersMatchPolicyOmitted())
	})

	t.Run("policy declared OR", func(t *testing.T) {
		// OR stated explicitly is a decision, not an omission. The warning exists to make the author
		// choose, so it must not keep firing once they have.
		g := policyGen([]string{"a", "b"}, []Verb{findByVerb(p, "OR", "")}, collection())
		assert.NoError(t, g.warnIdentifiersMatchPolicyOmitted())
	})

	t.Run("one identifier", func(t *testing.T) {
		// Nothing can be "not consulted" with a single identifier: the policy cannot change which item
		// is chosen.
		g := policyGen([]string{"a"}, []Verb{findByVerb(p, "", "")}, collection())
		assert.NoError(t, g.warnIdentifiersMatchPolicyOmitted())
	})

	t.Run("observation delegated to a RESTAction", func(t *testing.T) {
		// This is the keycloak shape, and the arm that cost me a wrongly-filed issue.
		//
		// Five identifiers, the policy omitted, and a findby over a genuine collection -- every condition
		// the warning tests for -- yet the findby is NEVER EXECUTED, because the resource delegates its
		// observe. rest-dynamic-controller's Observe returns from observeViaRestAction before building any
		// client, and its Exists refuses outright rather than reading. So the policy cannot choose
		// anything, and warning here would have been the warning's only hit in the entire fleet: a 100%
		// false-positive rate on the measure the collection gate exists to protect.
		g := policyGen(
			[]string{"realm", "flowAlias", "provider", "alias", "subFlow"},
			[]Verb{findByVerb(p, "", "")}, collection())
		g.resourceConfig.ObserveDelegated = true
		assert.NoError(t, g.warnIdentifiersMatchPolicyOmitted())
	})

	t.Run("no findby verb", func(t *testing.T) {
		// Identifier matching is reached only through findby. A resource observed through 'get' binds a
		// path parameter, so the policy is inert for it.
		g := policyGen([]string{"a", "b"},
			[]Verb{{Action: ActionGet, Method: "GET", Path: p}}, collection())
		assert.NoError(t, g.warnIdentifiersMatchPolicyOmitted())
	})
}

// TestTheCollectionCountAgreesWithTheUnwrapRule is the anti-drift test named in
// findByCanReturnMoreThanOneItem's doc comment.
//
// unwrapFindByItems asks which schema ONE item has; findByCanReturnMoreThanOneItem asks how many items
// there can be. They read the same document and must answer consistently, because the two drifting apart
// would make the warning fire on exactly the definitions it must stay quiet about.
//
// The cross-check is computed from unwrapFindByItems' OBSERVABLE behaviour rather than restated from its
// source: it returns the envelope unchanged for a single object, and something else (or an error) for
// every collection shape. So "unwrap gave back the whole envelope" is an independent witness for "there
// is only one candidate".
func TestTheCollectionCountAgreesWithTheUnwrapRule(t *testing.T) {
	cases := []struct {
		name      string
		schema    *Schema
		itemsPath string
	}{
		{"bare array", arr(obj(prop("a", scalar("string")))), ""},
		{"envelope, one array", obj(prop("items", arr(obj(prop("a", scalar("string")))))), ""},
		{"envelope, two arrays", obj(
			prop("data", arr(scalar("string"))),
			prop("included", arr(scalar("string"))),
		), ""},
		{"single object", obj(prop("id", scalar("integer")), prop("name", scalar("string"))), ""},
		{"single object, nested object property", obj(prop("meta", obj(prop("id", scalar("integer"))))), ""},
		{"itemsPath resolving", obj(prop("data", obj(prop("items", arr(scalar("string")))))), "data.items"},
		{"itemsPath not resolving", obj(prop("id", scalar("integer"))), "nope.items"},
	}

	var sawTrue, sawFalse bool
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unwrapped, err := unwrapFindByItems(tc.schema, tc.itemsPath)

			// The independent witness: a single object is the one case where unwrapping yields the
			// envelope itself.
			returnedTheEnvelope := err == nil && unwrapped != nil &&
				strings.Join(propertyNames(unwrapped), ",") == strings.Join(propertyNames(tc.schema), ",")

			got := findByCanReturnMoreThanOneItem(tc.schema, tc.itemsPath)
			assert.Equal(t, !returnedTheEnvelope, got,
				"the two halves of the envelope rule disagree about %q", tc.name)

			if got {
				sawTrue = true
			} else {
				sawFalse = true
			}
		})
	}

	// Without this the table could drift to all-collections and the test above would pass vacuously --
	// every assertion comparing a constant to itself.
	require.True(t, sawTrue, "the table must contain at least one collection, or the agreement is untested")
	require.True(t, sawFalse, "the table must contain at least one single object, or the agreement is untested")
}

// TestTheWarningReachesGenerationWarnings is the wiring arm.
//
// Every assertion above calls warnIdentifiersMatchPolicyOmitted directly, so all of them would still pass
// if Generate never called it -- the predicate would be perfect and the warning would reach no one. This
// goes through Generate and asserts the warning arrives in the channel the controller actually reads.
func TestTheWarningReachesGenerationWarnings(t *testing.T) {
	resourceConfig := &ResourceConfig{
		Identifiers: []string{"name", "owner"},
		Verbs: []Verb{
			{Action: "create", Path: "/widgets", Method: "post"},
			findByVerb("/widgets", "", ""),
		},
	}
	doc := &mockOASDocument{Paths: map[string]*mockPathItem{
		"/widgets": {Ops: map[string]Operation{
			"post": &mockOperation{RequestBody: RequestBodyInfo{Content: map[string]*Schema{
				"application/json": obj(prop("name", scalar("string")), prop("owner", scalar("string"))),
			}}},
			"get": &mockOperation{Responses: map[int]ResponseInfo{200: {Content: map[string]*Schema{
				"application/json": obj(prop("items", arr(obj(
					prop("name", scalar("string")),
					prop("owner", scalar("string")),
				)))),
			}}}},
		}},
	}}

	result, err := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), resourceConfig).Generate()
	require.NoError(t, err)

	var found bool
	for _, w := range result.GenerationWarnings {
		var sge SchemaGenerationError
		if errors.As(w, &sge) && sge.Code == CodeIdentifiersMatchPolicyOmitted {
			found = true
		}
	}
	assert.True(t, found,
		"the warning must reach GenerationWarnings; got %v", result.GenerationWarnings)
}
