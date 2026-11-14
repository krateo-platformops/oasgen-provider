//go:build unit

package restResources

import (
	"testing"

	customcondition "github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
	unstructuredtools "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured"
	"github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A faithful model of Update's status sequence when the response HAS a body, which is the common case:
//
//	clearStatusFields(mg)            restResources.go:1736
//	populateStatusFields(...)        identifiers + additionalStatusFields only
//	SetConditions(mg, Updating())    or Pending(), on the pending branch
//	<digest recorded on the SUCCESS branch only>
//	UpdateStatus(...)                persists whatever is left
//
// No HTTP machinery: the question is purely what survives the clear, and these are the real functions.
func seedStatus(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	mg := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "example.krateo.io/v1alpha1",
		"kind":       "Thing",
		"metadata":   map[string]interface{}{"name": "t", "namespace": "n"},
	}}
	require.NoError(t, unstructuredtools.SetConditions(mg, condition.Available()))
	require.NoError(t, unstructuredtools.SetConditions(mg, customcondition.ResolverDigestRecorded()))
	require.NoError(t, setResolverDigest(mg, "sha256:deadbeef"))
	// A response-derived identifier, so a test can tell "preserves the right keys" from "preserves
	// everything". This is the field whose staleness clearStatusFields exists to prevent.
	require.NoError(t, unstructured.SetNestedField(mg.Object, "stale-id-123", "status", "id"))

	// CONTROL: the setup must be able to fail. If the seed did not land, every assertion below is vacuous.
	require.Equal(t, "sha256:deadbeef", recordedResolverDigest(mg), "seed: the digest must be recorded")
	require.NotEmpty(t, conditionTypes(t, mg), "seed: conditions must exist")
	assert.Contains(t, conditionTypes(t, mg), customcondition.TypeResolverDigestRecorded,
		"seed: the digest condition must be present before the clear, or the test proves nothing")
	id, found, err := unstructured.NestedString(mg.Object, "status", "id")
	require.NoError(t, err)
	require.True(t, found, "seed: the response-derived identifier must be present before the clear")
	require.Equal(t, "stale-id-123", id)
	return mg
}

func conditionTypes(t *testing.T, mg *unstructured.Unstructured) []string {
	t.Helper()
	conds, found, err := unstructured.NestedSlice(mg.Object, "status", "conditions")
	require.NoError(t, err)
	if !found {
		return nil
	}
	var out []string
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		require.True(t, ok)
		out = append(out, m["type"].(string))
	}
	return out
}

// HALF ONE: a condition type the caller does not re-add must SURVIVE the clear. Before issue 667 only
// the re-added one did, so ResolverDigestRecorded was lost permanently -- Observe writes it under
// `if bootstrappedDigest`, which cannot recur once a digest is recorded.
//
// Asserted on ANY extra condition type, not on the digest condition by name, because the defect was
// never specific to it: it was the first condition whose write happens to be gated on a
// non-recurring branch, and a fix scoped to that name would hold only until the next one.
func TestUpdateWithABodyPreservesConditionsItDoesNotReAdd(t *testing.T) {
	mg := seedStatus(t)

	clearStatusFields(mg)
	require.NoError(t, unstructuredtools.SetConditions(mg, customcondition.Updating()))

	types := conditionTypes(t, mg)
	assert.Contains(t, types, customcondition.TypeResolverDigestRecorded,
		"the digest condition was destroyed by the clear; it is never re-written, so the loss is permanent")
	assert.Contains(t, types, condition.TypeReady,
		"control: the condition the caller DID re-add must be there, or the model is wrong")
	assert.Len(t, types, 2, "expected both condition types to coexist; got %v", types)

	// The response-derived fields it is SUPPOSED to drop must still be dropped, or this test would
	// pass just as well against a function that does nothing.
	_, found, err := unstructured.NestedString(mg.Object, "status", "id")
	require.NoError(t, err)
	assert.False(t, found, "a stale response-derived identifier survived the clear, which is the "+
		"deadlock clearStatusFields exists to prevent")
}

// HALF TWO, and the serious one: on the PENDING branch the digest must survive untouched, so a
// rotation is still reported as drift on the next Observe rather than adopted as a bootstrap.
//
// The pending branch deliberately does not re-record the digest -- a pending update has not landed,
// and re-recording would retire a difference the API has not accepted. That is correct, and it is
// only safe if the PRIOR digest survives. Before issue 667 the clear removed it first, so the
// rotation was retired by the act of the update being SLOW.
func TestPendingUpdateKeepsTheRecordedDigestSoTheRotationIsStillDrift(t *testing.T) {
	mg := seedStatus(t)

	// Update, response body present, pending branch: clear, populate, SetConditions(Pending()), and
	// NO digest write.
	clearStatusFields(mg)
	require.NoError(t, unstructuredtools.SetConditions(mg, customcondition.Pending()))

	assert.Equal(t, "sha256:deadbeef", recordedResolverDigest(mg),
		"the recorded digest did not survive the clear, so the next Observe will adopt the rotated "+
			"fingerprints and the rotation will never reach the vendor")

	// The consequence, through the real decision function rather than a restatement of it.
	assert.Equal(t, digestRotated, resolverDigestDecision(true, recordedResolverDigest(mg), "sha256:ROTATEDVALUE"),
		"a rotated Secret must still be DRIFT after a pending update")

	// CONTRAST CONTROL, kept from the characterisation version: an EMPTY recorded digest really does
	// turn the same rotated value into a bootstrap. Without this, the assertion above could pass on a
	// decision function that returned digestRotated regardless of what was recorded.
	assert.Equal(t, digestNeedsBootstrap, resolverDigestDecision(true, "", "sha256:ROTATEDVALUE"),
		"control: the verdict must actually depend on the recorded digest")
}
