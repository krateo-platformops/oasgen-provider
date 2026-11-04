package restclient

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// findItemInList used to discard a comparison error and carry on, with the log its own comment described
// commented out — so the error reached nothing at all and the item silently counted as "not a match".
// "Nothing matched" is the one answer a caller may act on by CREATING, which for a findby-only kind
// duplicates a live vendor resource (#371's reasoning, #439's surviving half).
//
// THE PAIR OF ARMS IS THE POINT, and the second is why the larger reclassification in #439 was withdrawn:
// an identifier merely ABSENT from a response item must still produce a clean no-match, because a
// findby-only kind can legitimately rely on never matching to get its create. Only a structurally broken
// lookup changes behaviour. Asserting the first alone would pass against a change that also broke the
// second, which is the change I reverted.
func TestAComparisonErrorIsPropagatedNotSwallowed(t *testing.T) {
	// A nil Resource makes isInResource fail structurally ("resource is nil"). It is reached only because
	// the item CARRIES the identifier — an item lacking it never gets that far, which is the control below.
	u := &UnstructuredClient{
		IdentifierFields: []string{"id"},
		Resource:         nil,
	}

	item, found, _, err := u.findItemInList([]interface{}{
		map[string]interface{}{"id": "abc123"},
	})

	require.Error(t, err,
		"a structurally failed comparison must reach the caller: swallowing it reports 'nothing matched', "+
			"and a caller acting on that CREATES a duplicate of a resource it never managed to compare")
	assert.Nil(t, item)
	assert.False(t, found)
	assert.Contains(t, err.Error(), "identifiers",
		"the error should say what it was doing, since it now surfaces to an operator rather than to a "+
			"commented-out log line")
}

// THE CONTROL, and it is load-bearing rather than belt-and-braces. This is the path a findby-only kind
// depends on: its declared identifier is not a field on the listed items, so findby never matches, the
// first Observe reports not-exists, and the create fires once. A consumer has six live CRs of exactly this
// shape. Propagating errors must not touch it.
func TestAnIdentifierAbsentFromTheItemIsStillACleanNoMatch(t *testing.T) {
	res := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{"somethingElse": "x"},
	}}
	u := &UnstructuredClient{
		IdentifierFields: []string{"node_id"},
		Resource:         res,
	}

	item, found, _, err := u.findItemInList([]interface{}{
		// The vendor's collection simply has no node_id field, which is the measured real case.
		map[string]interface{}{"uuid": "n-1", "name": "blade01"},
		map[string]interface{}{"uuid": "n-2", "name": "blade02"},
	})

	require.NoError(t, err,
		"an identifier absent from the response item is NOT an error: it takes the !found path and "+
			"continues. A findby-only kind relies on this to never match and get its create, so turning it "+
			"into an error breaks working kinds — which is why #439's reclassification was reverted")
	assert.False(t, found)
	assert.Nil(t, item)
}

// And an empty list is unchanged: no match, no error. Guards against the propagation change accidentally
// making the trivial case noisy.
func TestAnEmptyListIsStillACleanNoMatch(t *testing.T) {
	u := &UnstructuredClient{IdentifierFields: []string{"id"}, Resource: &unstructured.Unstructured{}}

	item, found, _, err := u.findItemInList(nil)

	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, item)
}
