//go:build unit || integration

package restclient

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WHAT THIS PINS
//
// ListMapKeyPaths decides which arrays of OBJECTS the drift comparison may match by key instead of by
// position. Emit a path that is not a declared map-list and reordering stops being detected where
// order is meaningful; fail to emit one that is, and the consumer's resource re-PUTs forever because
// the element it rewrites comes back in a different slot (#650).
//
// BOTH EXTENSIONS ARE REQUIRED, and the rows below are mostly about that: a document that declares
// only the keys, or only the type, or declares the type as `set`, is NOT a map-list. Honouring a
// half-declaration would give such a document different comparison semantics with nothing saying so,
// and the generator side already refuses to emit a malformed value rather than passing it through.
const listMapSpec = `
openapi: 3.0.0
info: {title: policies, version: "1.0"}
paths:
  /policies:
    put:
      operationId: replacePolicy
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                name: {type: string}
                rules:
                  type: array
                  x-kubernetes-list-type: map
                  x-kubernetes-list-map-keys: [name]
                  items:
                    type: object
                    properties:
                      name:   {type: string}
                      action: {type: string}
                composite:
                  type: array
                  x-kubernetes-list-type: map
                  x-kubernetes-list-map-keys: [ns, name]
                  items:
                    type: object
                    properties:
                      ns:   {type: string}
                      name: {type: string}
                scalarKey:
                  type: array
                  x-kubernetes-list-type: map
                  x-kubernetes-list-map-keys: name
                  items:
                    type: object
                    properties:
                      name: {type: string}
                keysWithoutType:
                  type: array
                  x-kubernetes-list-map-keys: [name]
                  items:
                    type: object
                    properties:
                      name: {type: string}
                typeWithoutKeys:
                  type: array
                  x-kubernetes-list-type: map
                  items:
                    type: object
                    properties:
                      name: {type: string}
                declaredAsSet:
                  type: array
                  x-kubernetes-list-type: set
                  x-kubernetes-list-map-keys: [name]
                  items:
                    type: object
                    properties:
                      name: {type: string}
                scalarItems:
                  type: array
                  x-kubernetes-list-type: map
                  x-kubernetes-list-map-keys: [name]
                  items: {type: string}
                plainObjects:
                  type: array
                  items:
                    type: object
                    properties:
                      name: {type: string}
                groups:
                  type: array
                  items:
                    type: object
                    properties:
                      id: {type: string}
                      rules:
                        type: array
                        x-kubernetes-list-type: map
                        x-kubernetes-list-map-keys: [name]
                        items:
                          type: object
                          properties:
                            name: {type: string}
                nested:
                  type: object
                  properties:
                    deep:
                      type: array
                      x-kubernetes-list-type: map
                      x-kubernetes-list-map-keys: [key]
                      items:
                        type: object
                        properties:
                          key: {type: string}
      responses:
        "200": {description: ok}
  /nobody:
    put:
      operationId: noBody
      responses:
        "200": {description: ok}
`

func TestListMapKeyPaths(t *testing.T) {
	client := clientFromSpec(t, listMapSpec)

	got, err := client.ListMapKeyPaths("PUT", "/policies")
	require.NoError(t, err)

	want := map[string][]string{
		"rules":        {"name"},
		"composite":    {"ns", "name"},
		"scalarKey":    {"name"}, // a bare scalar is LIFTED, matching the generator
		"groups.rules": {"name"}, // nested in an array of objects, with NO index segment
		"nested.deep":  {"key"},
	}
	assert.Equal(t, want, got)
}

// Each exclusion gets its own statement. A single equality assertion above is satisfied by a walker
// that is wrong for several reasons at once, and the failure would not say which.
func TestListMapKeyPathsExclusions(t *testing.T) {
	client := clientFromSpec(t, listMapSpec)
	got, err := client.ListMapKeyPaths("PUT", "/policies")
	require.NoError(t, err)

	_, ok := got["keysWithoutType"]
	assert.False(t, ok, "keys without x-kubernetes-list-type: map is not a declared map-list")

	_, ok = got["typeWithoutKeys"]
	assert.False(t, ok, "x-kubernetes-list-type: map with no keys names nothing to match on")

	_, ok = got["declaredAsSet"]
	assert.False(t, ok, "x-kubernetes-list-type: set is the other arm; keys beside it do not make it a map")

	_, ok = got["scalarItems"]
	assert.False(t, ok, "scalar items cannot be matched by key -- that is SetTypedArrayPaths' arm")

	_, ok = got["plainObjects"]
	assert.False(t, ok, "an undeclared array of objects must stay positional")

	_, ok = got["name"]
	assert.False(t, ok, "control: a scalar property is not an array at all")
}

// An operation with no request body is a true empty answer, not an error -- the same contract
// SetTypedArrayPaths states, so the two cannot drift apart in how they treat a bodyless operation.
func TestListMapKeyPathsNoRequestBody(t *testing.T) {
	client := clientFromSpec(t, listMapSpec)

	got, err := client.ListMapKeyPaths("PUT", "/nobody")
	require.NoError(t, err, "a bodyless operation is not a failure")
	assert.Nil(t, got)
}

// A missing path or operation IS an error, because the caller asked about something that is not there
// and silently returning no declarations would degrade every array to positional with nothing said.
func TestListMapKeyPathsUnknownPathOrOperation(t *testing.T) {
	client := clientFromSpec(t, listMapSpec)

	_, err := client.ListMapKeyPaths("PUT", "/does-not-exist")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path not found")

	_, err = client.ListMapKeyPaths("DELETE", "/policies")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "operation not found")
}

// THE TWO ARMS MUST NOT CLAIM THE SAME PATH. A path emitted by both would get whichever branch
// compareSlices checks first, making the arm a property of the code's order rather than of the
// declaration. Asserted against the same document both functions read.
func TestTheTwoListArmsAreDisjointOnOneDocument(t *testing.T) {
	client := clientFromSpec(t, listMapSpec)

	sets, err := client.SetTypedArrayPaths("PUT", "/policies")
	require.NoError(t, err)
	maps, err := client.ListMapKeyPaths("PUT", "/policies")
	require.NoError(t, err)
	require.NotEmpty(t, maps, "control: the document must declare map-lists or this proves nothing")

	for _, s := range sets {
		_, clash := maps[s]
		assert.False(t, clash, "path %q is claimed by both the set arm and the map-list arm", s)
	}
}
