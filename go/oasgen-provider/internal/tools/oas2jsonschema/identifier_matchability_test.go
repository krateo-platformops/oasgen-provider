package oas2jsonschema

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ironicShapedDoc is the measured Ironic shape, reduced: the ITEM endpoint is complete and the COLLECTION
// abridges its items. Production carries ~40 fields on /v1/nodes/{uuid} and 7 on /v1/nodes.
func ironicShapedDoc() *mockOASDocument {
	return &mockOASDocument{Paths: map[string]*mockPathItem{
		"/v1/nodes": {Ops: map[string]Operation{
			"get": &mockOperation{Responses: map[int]ResponseInfo{200: {Content: map[string]*Schema{
				"application/json": obj(prop("nodes", arr(obj(
					prop("uuid", scalar("string")),
					prop("power_state", scalar("string")),
				)))),
			}}}},
			"post": &mockOperation{RequestBody: RequestBodyInfo{Content: map[string]*Schema{
				"application/json": obj(prop("uuid", scalar("string")), prop("name", scalar("string"))),
			}}},
		}},
		"/v1/nodes/{uuid}": {Ops: map[string]Operation{
			"get": &mockOperation{Responses: map[int]ResponseInfo{200: {Content: map[string]*Schema{
				// COMPLETE: carries `name`, which the collection item above does not.
				"application/json": obj(
					prop("uuid", scalar("string")),
					prop("name", scalar("string")),
					prop("power_state", scalar("string")),
				),
			}}}},
		}},
	}}
}

func matchGen(identifiers []string, verbs []Verb, doc *mockOASDocument) *OASSchemaGenerator {
	return &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		resourceConfig:  &ResourceConfig{Identifiers: identifiers, Verbs: verbs},
		docs:            NewDocumentSet(doc),
	}
}

func requireOneWarning(t *testing.T, errs []error) SchemaGenerationError {
	t.Helper()
	require.Len(t, errs, 1, "expected exactly one matchability warning, got %v", errs)
	var sge SchemaGenerationError
	require.True(t, errors.As(errs[0], &sge), "want SchemaGenerationError, got %T", errs[0])
	assert.Equal(t, CodeIdentifierNotInFindByResponse, sge.Code)
	return sge
}

// TestAnIdentifierPresentInGetButAbsentFromTheFindByItemIsWarned is the whole reason this warning exists
// separately, and it is the arm that fails if anyone resolves it against the get response.
//
// The existing CodeIdentifierNotResolvable resolves types through getBaseSchemaForStatus, which prefers
// ActionGet. For this document that check sees `name` and is satisfied -- while findby, which is what
// actually compares identifiers, reads items that do not carry it. This is measured, not contrived: the
// Ironic maintainers reported `AND` per-parent uniqueness as impossible for exactly this reason.
func TestAnIdentifierPresentInGetButAbsentFromTheFindByItemIsWarned(t *testing.T) {
	g := matchGen(
		[]string{"uuid", "name"},
		[]Verb{
			{Action: ActionCreate, Method: "POST", Path: "/v1/nodes"},
			findByVerb("/v1/nodes", "", ""),
			{Action: ActionGet, Method: "GET", Path: "/v1/nodes/{uuid}"},
		},
		ironicShapedDoc(),
	)

	sge := requireOneWarning(t, g.warnIdentifiersMissingFromFindByResponse(g.matchability()))

	assert.Contains(t, sge.Message, `"name"`, "the absent identifier must be named")
	assert.NotContains(t, sge.Message, `"uuid"`, "an identifier the findby items DO carry must not be reported")
	assert.Contains(t, sge.Message, "OR", "the default policy's consequence is the narrowing, and the message must say so")
	assert.Equal(t, "spec.resource.identifiers", sge.Path)
}

// TestTheWarningReachesAKindWithACreateVerb pins the other half of #445.
//
// CodeIdentifierNotResolvable's only emitter sits under `if !g.hasCreateVerb()`, so it has never run for a
// kind declaring create -- which is most of them. If this warning ever acquires the same guard, this arm
// goes red. The document above declares `post` on /v1/nodes precisely so the create verb is real.
func TestTheWarningReachesAKindWithACreateVerb(t *testing.T) {
	verbs := []Verb{
		{Action: ActionCreate, Method: "POST", Path: "/v1/nodes"},
		findByVerb("/v1/nodes", "", ""),
		// A get verb is REQUIRED here now, and not incidental to this test's point. Without it the
		// fixture is the action-only shape (create as the only write verb), for which issue 625
		// deliberately SUPPRESSES this warning -- so the arm would fail for a reason that has nothing
		// to do with #445, which is what it exists to pin. Keeping create-with-a-get preserves
		// exactly the property under test: the warning is not gated on `!hasCreateVerb()`.
		{Action: ActionGet, Method: "GET", Path: "/v1/nodes/{uuid}"},
	}
	g := matchGen([]string{"uuid", "name"}, verbs, ironicShapedDoc())

	require.True(t, g.hasCreateVerb(), "the fixture must actually declare a create verb or this arm proves nothing")
	requireOneWarning(t, g.warnIdentifiersMissingFromFindByResponse(g.matchability()))
}

// Under AND the resource is not degraded, it is non-functional: every item fails, findby reports
// not-found, and the reconciler acts on not-found by CREATING.
func TestTheANDConsequenceIsStatedAsNonFunctional(t *testing.T) {
	g := matchGen(
		[]string{"uuid", "name"},
		[]Verb{findByVerb("/v1/nodes", "AND", "")},
		ironicShapedDoc(),
	)

	sge := requireOneWarning(t, g.warnIdentifiersMissingFromFindByResponse(g.matchability()))
	assert.Contains(t, sge.Message, "AND")
	assert.Contains(t, sge.Message, "never match")
	assert.Contains(t, sge.Message, "creating", "the create-loop consequence is the reason AND is the severe case")
}

func TestEveryIdentifierAbsentIsReportedAsPolicyIndependent(t *testing.T) {
	g := matchGen(
		[]string{"name", "owner"}, // neither is in the abridged item
		[]Verb{findByVerb("/v1/nodes", "", "")},
		ironicShapedDoc(),
	)

	sge := requireOneWarning(t, g.warnIdentifiersMissingFromFindByResponse(g.matchability()))
	assert.Contains(t, sge.Message, "regardless of policy")
}

func TestTheMatchabilityWarningStaysSilentWhenItShould(t *testing.T) {
	t.Run("every identifier present in the item", func(t *testing.T) {
		g := matchGen([]string{"uuid", "power_state"}, []Verb{findByVerb("/v1/nodes", "", "")}, ironicShapedDoc())
		assert.Empty(t, g.warnIdentifiersMissingFromFindByResponse(g.matchability()))
	})

	t.Run("no findby verb", func(t *testing.T) {
		g := matchGen([]string{"uuid", "name"},
			[]Verb{{Action: ActionGet, Method: "GET", Path: "/v1/nodes/{uuid}"}}, ironicShapedDoc())
		assert.Empty(t, g.warnIdentifiersMissingFromFindByResponse(g.matchability()))
	})

	t.Run("observation delegated", func(t *testing.T) {
		g := matchGen([]string{"uuid", "name"}, []Verb{findByVerb("/v1/nodes", "", "")}, ironicShapedDoc())
		g.resourceConfig.ObserveDelegated = true
		assert.Empty(t, g.warnIdentifiersMissingFromFindByResponse(g.matchability()))
	})

	// THE DESIGN ARM: this warning asserts a field is ABSENT, which an unreadable schema cannot
	// establish. #444's warning branches the OPPOSITE way on the same input, because it asserts risk
	// rather than absence. Collapsing the two would make one of them wrong.
	t.Run("findby response cannot be read", func(t *testing.T) {
		doc := &mockOASDocument{Paths: map[string]*mockPathItem{
			"/v1/nodes": {Ops: map[string]Operation{"get": &mockOperation{}}}, // no documented responses
		}}
		g := matchGen([]string{"uuid", "name"}, []Verb{findByVerb("/v1/nodes", "", "")}, doc)
		assert.Empty(t, g.warnIdentifiersMissingFromFindByResponse(g.matchability()),
			"absence cannot be asserted from a response that could not be read; naming a specific field as "+
				"missing here would be wrong rather than cautious")
	})

	t.Run("ambiguous envelope", func(t *testing.T) {
		// ErrAmbiguousCollection is its own, louder diagnostic; do not stack a speculative absence on it.
		doc := &mockOASDocument{Paths: map[string]*mockPathItem{
			"/v1/nodes": {Ops: map[string]Operation{"get": &mockOperation{
				Responses: map[int]ResponseInfo{200: {Content: map[string]*Schema{
					"application/json": obj(
						prop("nodes", arr(obj(prop("uuid", scalar("string"))))),
						prop("links", arr(obj(prop("href", scalar("string"))))),
					),
				}}},
			}}},
		}}
		g := matchGen([]string{"uuid", "name"}, []Verb{findByVerb("/v1/nodes", "", "")}, doc)
		assert.Empty(t, g.warnIdentifiersMissingFromFindByResponse(g.matchability()))
	})

	t.Run("no identifiers declared", func(t *testing.T) {
		g := matchGen(nil, []Verb{findByVerb("/v1/nodes", "", "")}, ironicShapedDoc())
		assert.Empty(t, g.warnIdentifiersMissingFromFindByResponse(g.matchability()))
	})
}

// A dotted identifier is a nested path, and RDC reads it that way. Checking only top-level properties
// would report a perfectly valid nested identifier as absent -- the mistake #106 fixed on the other path.
func TestANestedIdentifierIsResolvedThroughItsPath(t *testing.T) {
	doc := &mockOASDocument{Paths: map[string]*mockPathItem{
		"/widgets": {Ops: map[string]Operation{"get": &mockOperation{
			Responses: map[int]ResponseInfo{200: {Content: map[string]*Schema{
				"application/json": arr(obj(prop("metadata", obj(prop("name", scalar("string")))))),
			}}},
		}}},
	}}

	t.Run("present nested identifier is not reported", func(t *testing.T) {
		g := matchGen([]string{"metadata.name"}, []Verb{findByVerb("/widgets", "", "")}, doc)
		assert.Empty(t, g.warnIdentifiersMissingFromFindByResponse(g.matchability()),
			"a nested identifier that IS present must not be reported; a top-level-only check would")
	})

	t.Run("absent nested identifier is reported", func(t *testing.T) {
		g := matchGen([]string{"metadata.serial"}, []Verb{findByVerb("/widgets", "", "")}, doc)
		sge := requireOneWarning(t, g.warnIdentifiersMissingFromFindByResponse(g.matchability()))
		assert.Contains(t, sge.Message, "metadata.serial")
	})
}

// The wiring arm: every assertion above calls the method directly, so all of them would pass if Generate
// never called it.
func TestTheMatchabilityWarningReachesGenerationWarnings(t *testing.T) {
	resourceConfig := &ResourceConfig{
		Identifiers: []string{"uuid", "name"},
		Verbs: []Verb{
			{Action: ActionCreate, Method: "POST", Path: "/v1/nodes"},
			findByVerb("/v1/nodes", "", ""),
			// See the note in TestTheWarningReachesAKindWithACreateVerb: without a get verb this is
			// the action-only shape and the warning is suppressed by design (issue 625). This test
			// pins the PLUMBING -- that the warning reaches GenerationWarnings -- so it needs a shape
			// the warning legitimately fires for. The action-only shape's own plumbing is pinned by
			// TestTheUnreachableCreateWarningReachesGenerationWarnings.
			{Action: ActionGet, Method: "GET", Path: "/v1/nodes/{uuid}"},
		},
	}
	result, err := NewOASSchemaGenerator(ironicShapedDoc(), DefaultGeneratorConfig(), resourceConfig).Generate()
	require.NoError(t, err)

	var found bool
	for _, w := range result.GenerationWarnings {
		var sge SchemaGenerationError
		if errors.As(w, &sge) && sge.Code == CodeIdentifierNotInFindByResponse {
			found = true
		}
	}
	assert.True(t, found, "the warning must reach GenerationWarnings; got %v", result.GenerationWarnings)
}
