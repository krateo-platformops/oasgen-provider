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

	// CONTROL: the setup must be able to fail. If the seed did not land, every assertion below is vacuous.
	require.Equal(t, "sha256:deadbeef", recordedResolverDigest(mg), "seed: the digest must be recorded")
	require.NotEmpty(t, conditionTypes(t, mg), "seed: conditions must exist")
	assert.Contains(t, conditionTypes(t, mg), customcondition.TypeResolverDigestRecorded,
		"seed: the digest condition must be present before the clear, or the test proves nothing")
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

// HALF ONE: every condition type except the one Update re-sets is destroyed, and the digest condition
// never comes back -- Observe writes it only on the bootstrap pass, which cannot recur once a digest
// is recorded.
func TestUpdateWithABodyDestroysTheResolverDigestCondition(t *testing.T) {
	mg := seedStatus(t)

	clearStatusFields(mg)
	require.NoError(t, unstructuredtools.SetConditions(mg, customcondition.Updating()))

	types := conditionTypes(t, mg)
	assert.NotContains(t, types, customcondition.TypeResolverDigestRecorded,
		"if this now SURVIVES, the bug is fixed and this test should be inverted")
	assert.Equal(t, []string{condition.TypeReady}, types,
		"only the condition Update re-sets survives the clear; got %v", types)
}

// HALF TWO, and the serious one: on the PENDING branch the digest is wiped and deliberately NOT
// re-recorded, so the next Observe sees recorded=="" -> digestNeedsBootstrap -> adopts the CURRENT
// fingerprints. The rotation is retired without ever having been pushed.
func TestPendingUpdateWipesTheRecordedDigestSoTheRotationIsLost(t *testing.T) {
	mg := seedStatus(t)

	// Update, response body present, pending branch: clear, populate, SetConditions(Pending()), and
	// NO digest write -- by design, because a pending update has not landed.
	clearStatusFields(mg)
	require.NoError(t, unstructuredtools.SetConditions(mg, customcondition.Pending()))

	assert.Empty(t, recordedResolverDigest(mg),
		"the recorded digest survived the clear; if so this half is fixed")

	// The consequence, through the real decision function: a rotated Secret now reads as a bootstrap.
	verdict := resolverDigestDecision(true, recordedResolverDigest(mg), "sha256:ROTATEDVALUE")
	assert.Equal(t, digestNeedsBootstrap, verdict,
		"with the digest wiped, a ROTATED secret is adopted as a bootstrap instead of reported as drift")

	// And the contrast that makes it a defect rather than a quirk: had the digest survived, the same
	// rotation would have been reported as drift.
	assert.Equal(t, digestRotated, resolverDigestDecision(true, "sha256:deadbeef", "sha256:ROTATEDVALUE"),
		"control: with the digest intact the rotation IS drift, so the loss is what changes the verdict")
}
