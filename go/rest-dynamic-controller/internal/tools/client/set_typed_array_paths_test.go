//go:build unit || integration

package restclient

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WHAT THIS PINS
//
// SetTypedArrayPaths decides which arrays the drift comparison may treat as unordered. Emit a path that
// is not a set and reordering stops being detected for a field where order is meaningful; fail to emit
// one that is and the consumer's resource re-PUTs forever (#617). Both directions are rows below.
//
// The scalar-items restriction is the load-bearing part and it is the same rule plumbing's crdgen uses to
// decide between `x-kubernetes-list-type: set` and a CEL uniqueness rule -- so a document that produces a
// native Kubernetes set in the CRD produces a multiset comparison here, and the two cannot disagree about
// which arrays are sets.

const setTypedSpec = `
openapi: 3.0.0
info: {title: realms, version: "1.0"}
paths:
  /realms:
    post:
      operationId: createRealm
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                realm:              {type: string}
                enabledEventTypes:  {type: array, uniqueItems: true, items: {type: string}}
                eventsListeners:    {type: array, uniqueItems: true, items: {type: string}}
                ports:              {type: array, uniqueItems: true, items: {type: integer}}
                flags:              {type: array, uniqueItems: true, items: {type: boolean}}
                ratios:             {type: array, uniqueItems: true, items: {type: number}}
                orderedSteps:       {type: array, items: {type: string}}
                objectSet:
                  type: array
                  uniqueItems: true
                  items:
                    type: object
                    properties:
                      id: {type: string}
                rules:
                  type: array
                  items:
                    type: object
                    properties:
                      name:  {type: string}
                      types: {type: array, uniqueItems: true, items: {type: string}}
                nested:
                  type: object
                  properties:
                    deepSet: {type: array, uniqueItems: true, items: {type: string}}
      responses:
        "200": {description: ok}
`

func TestSetTypedArrayPaths(t *testing.T) {
	client := clientFromSpec(t, setTypedSpec)

	got, err := client.SetTypedArrayPaths("POST", "/realms")
	require.NoError(t, err)
	sort.Strings(got)

	want := []string{
		"enabledEventTypes", // THE CONSUMER CASE
		"eventsListeners",
		"flags", // boolean items
		"nested.deepSet",
		"ports",       // integer items
		"ratios",      // number items
		"rules.types", // a set inside an array of objects, with NO index segment
	}
	sort.Strings(want)
	assert.Equal(t, want, got)
}

// Each exclusion gets its own statement, because a single equality assertion above would be satisfied by
// a walker that is wrong for several different reasons at once, and the failure would not say which.
func TestSetTypedArrayPathsExclusions(t *testing.T) {
	client := clientFromSpec(t, setTypedSpec)
	got, err := client.SetTypedArrayPaths("POST", "/realms")
	require.NoError(t, err)

	in := func(p string) bool {
		for _, g := range got {
			if g == p {
				return true
			}
		}
		return false
	}

	assert.False(t, in("orderedSteps"),
		"an array WITHOUT uniqueItems is not a set. Emitting it would stop reordering being detected "+
			"in a field whose order is meaningful -- a JSON Patch sequence, a priority list")
	assert.False(t, in("objectSet"),
		"uniqueItems with OBJECT items must not be emitted: matching objects needs a key, which is "+
			"identifiersMatchPolicy's question. plumbing's crdgen draws the same line, giving non-scalar "+
			"arrays a CEL rule rather than list-type: set")
	assert.False(t, in("realm"),
		"a scalar string is not an array")
	assert.False(t, in("rules"),
		"the OUTER array of objects is not a set; only the scalar set nested inside it is")
	assert.False(t, in("nested"),
		"an object is not an array")

	// And the positive control for this test: if `in` were broken, every assertion above would pass.
	assert.True(t, in("enabledEventTypes"),
		"the membership helper itself is broken, so none of the exclusions above proved anything")
}

func TestSetTypedArrayPathsEdgeCases(t *testing.T) {
	t.Run("a document with no sets returns nothing, not an error", func(t *testing.T) {
		client := clientFromSpec(t, `
openapi: 3.0.0
info: {title: t, version: "1.0"}
paths:
  /things:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                name:  {type: string}
                steps: {type: array, items: {type: string}}
      responses:
        "200": {description: ok}
`)
		got, err := client.SetTypedArrayPaths("POST", "/things")
		require.NoError(t, err)
		assert.Empty(t, got, "no uniqueItems anywhere means no sets, which is an answer rather than a failure")
	})

	t.Run("an operation with no request body returns nothing, not an error", func(t *testing.T) {
		client := clientFromSpec(t, `
openapi: 3.0.0
info: {title: t, version: "1.0"}
paths:
  /things:
    get:
      responses:
        "200": {description: ok}
`)
		got, err := client.SetTypedArrayPaths("GET", "/things")
		require.NoError(t, err)
		assert.Empty(t, got,
			"a verb with no body declares no sets. This must not be an error: the caller falls back to "+
				"positional comparison, and an error here would be logged on every reconcile of every "+
				"resource whose update verb has no body")
	})

	t.Run("the boolean items form is not a set", func(t *testing.T) {
		// `items: true` declares no element schema, so nothing can be called scalar. Reaching
		// Items.A on that shape is a nil dereference, which in this package takes the pod down.
		client := clientFromSpec(t, `
openapi: 3.1.0
info: {title: t, version: "1.0"}
paths:
  /things:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                anything: {type: array, uniqueItems: true, items: true}
      responses:
        "200": {description: ok}
`)
		got, err := client.SetTypedArrayPaths("POST", "/things")
		require.NoError(t, err, "must not panic or error on the boolean items form")
		assert.Empty(t, got)
	})

	t.Run("a missing path and a missing operation are errors, not silent empties", func(t *testing.T) {
		client := clientFromSpec(t, setTypedSpec)

		_, err := client.SetTypedArrayPaths("POST", "/nope")
		assert.Error(t, err, "a path that is not in the document is a configuration error, not zero sets")

		_, err = client.SetTypedArrayPaths("DELETE", "/realms")
		assert.Error(t, err, "an operation that is not in the document is a configuration error")
	})

	t.Run("a nil document is an error", func(t *testing.T) {
		_, err := (&UnstructuredClient{}).SetTypedArrayPaths("POST", "/realms")
		assert.Error(t, err)
	})
}

// THE TWO WALKERS MUST AGREE ABOUT WHERE AN OPERATION'S BODY IS.
//
// SetTypedArrayPaths and UpdatableBodyPaths resolve the same document, the same path item, the same
// operation and the same media type, by design -- so a change to how one finds a body cannot silently
// leave the other looking somewhere else. Asserted on the source, following the precedent in this
// package's other tests: a count, so adding a third introspection method makes someone decide.
func TestBothBodyIntrospectorsResolveTheBodyIdentically(t *testing.T) {
	client := clientFromSpec(t, setTypedSpec)

	// Both must find the same operation's body: the leaf paths must CONTAIN every set path, since a set
	// is a leaf as far as collectLeafPaths is concerned.
	leaves, err := client.UpdatableBodyPaths("POST", "/realms")
	require.NoError(t, err)
	sets, err := client.SetTypedArrayPaths("POST", "/realms")
	require.NoError(t, err)
	require.NotEmpty(t, sets, "premise: the fixture must declare at least one set")

	leafSet := map[string]struct{}{}
	for _, l := range leaves {
		leafSet[l] = struct{}{}
	}
	for _, s := range sets {
		if s == "rules.types" {
			// The one documented divergence: collectLeafPaths stops at the outer array and never
			// descends into its items, so it emits `rules` and not `rules.types`. SetTypedArrayPaths
			// descends deliberately, because that shape is real.
			assert.Contains(t, leafSet, "rules",
				"the leaf walker should still see the outer array it stopped at")
			continue
		}
		assert.Contains(t, leafSet, s,
			"%q is a set but the leaf walker did not see it, so the two are resolving different "+
				"bodies or disagree about what a leaf is", s)
	}
}
