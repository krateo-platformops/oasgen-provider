//go:build unit || integration

package restResources

import (
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// THE HAZARD IS CONSTRUCTED, NOT HOPED FOR.
//
// `append(Resource.Identifiers, Resource.AdditionalStatusFields...)` writes into the identifier
// slice's backing array whenever it has spare capacity. After a JSON unmarshal it often does --
// measured at 3, 5 and 6 identifiers -- but a test that relied on unmarshal happening to produce
// spare capacity would be testing the unmarshaller, and would pass or fail for reasons unrelated to
// this code.
//
// So the capacity is set explicitly and a SENTINEL is planted in the spare slot. The old code
// overwrites it; the fix cannot, because it allocates.
func TestPopulateStatusFieldsDoesNotWriteIntoTheIdentifierSlicesArray(t *testing.T) {
	const sentinel = "SENTINEL_MUST_SURVIVE"

	// len 3, cap 4 -- exactly the shape nova measured for a three-identifier kind.
	ids := make([]string, 4)
	copy(ids, []string{"id_a", "id_b", "id_c"})
	ids[3] = sentinel
	identifiers := ids[:3]
	require.Equal(t, 3, len(identifiers))
	require.Equal(t, 4, cap(identifiers),
		"the fixture must have spare capacity or the aliasing it exists to detect cannot occur")

	clientInfo := &getter.Info{Resource: getter.Resource{
		Identifiers:            identifiers,
		AdditionalStatusFields: []string{"extra_field"},
	}}
	mg := &unstructured.Unstructured{Object: map[string]any{}}
	body := map[string]any{
		"id_a": "A", "id_b": "B", "id_c": "C", "extra_field": "E",
	}

	require.NoError(t, populateStatusFields(clientInfo, mg, body))

	// THE LOAD-BEARING ASSERTION. Reaching past len into the backing array is the only way to see it.
	assert.Equal(t, sentinel, ids[3],
		"populateStatusFields wrote %q into the identifier slice's backing array, past its length. "+
			"Nothing reading Resource.Identifiers normally sees it, which is why this is latent rather "+
			"than visible -- but restResources.go guards the identical pattern with a comment saying "+
			"why, and one site documenting a hazard while another does the unguarded form is worse "+
			"than either alone", ids[3])

	// CONTROL: the function must actually have done its work, or an untouched sentinel proves nothing.
	// A populateStatusFields that returned early would pass the assertion above for the wrong reason.
	for _, f := range []string{"id_a", "id_b", "id_c", "extra_field"} {
		v, found, err := unstructured.NestedFieldNoCopy(mg.Object, "status", f)
		require.NoError(t, err)
		assert.True(t, found, "status.%s was not populated, so this test did not exercise the append at all", f)
		assert.NotEmpty(t, v)
	}

	// And the identifiers themselves must be untouched within their length.
	assert.Equal(t, []string{"id_a", "id_b", "id_c"}, identifiers)
}
