package restdefinition

import (
	"reflect"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	rtv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fullyPopulatedStatus returns a RestDefinitionStatus with every data field set to a distinctive
// non-zero value, so a field the snapshot fails to carry shows up as a difference rather than as a
// coincidental match.
func fullyPopulatedStatus() definitionv1alpha1.RestDefinitionStatus {
	hasSchemes := true
	return definitionv1alpha1.RestDefinitionStatus{
		Resource:                 definitionv1alpha1.KindApiVersion{Kind: "ActionsVariableRepo", APIVersion: "github.krateo.io/v1-0"},
		Configuration:            definitionv1alpha1.KindApiVersion{Kind: "ActionsVariableRepoConfiguration", APIVersion: "github.krateo.io/v1-0"},
		OASPath:                  "configmap://krateo-system/gh/openapi.yaml",
		Digest:                   "sha256:digest",
		HasSecuritySchemes:       &hasSchemes,
		OASHash:                  "sha256:oas",
		ResourceHash:             "sha256:resource",
		AuthSecretDigest:         "sha256:authsecret",
		AuthSecretRBACNamespaces: []string{"ns-a", "ns-b"},
	}
}

// TestCreatedStatusSnapshotCarriesEveryField is the guard for the class #263 removed.
//
// It asserts EQUALITY against an expected status, not that each field is non-zero. The previous version
// of this guard checked only non-zero, which let a swapped pair of same-typed fields, or a wrong but
// non-zero value, pass unnoticed.
//
// More importantly, the previous guard checked only that the applier applied what it was GIVEN. It could
// not check that the collection site collected everything -- and the collection site was where the bug
// was: deleting any of seven captures in Create left the whole suite green. A snapshot has no collection
// site, so this test now covers the entire path from "the status Create established" to "the status
// written", which is what makes the class gone rather than merely tested for.
func TestCreatedStatusSnapshotCarriesEveryField(t *testing.T) {
	want := fullyPopulatedStatus()

	// Fail loudly if RestDefinitionStatus grows a data field this fixture leaves unset -- otherwise the
	// comparison below silently stops covering it. ConditionedStatus is deliberately not set here; it is
	// asserted separately, because applyTo merges it rather than carrying it.
	v := reflect.ValueOf(want)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if name == "ConditionedStatus" {
			continue
		}
		if v.Field(i).IsZero() {
			t.Errorf("RestDefinitionStatus.%s is zero in this fixture, so it is not covered; set it in fullyPopulatedStatus", name)
		}
	}

	cr := restDefinitionForStatusTest()
	cr.Status = want

	target := &definitionv1alpha1.RestDefinition{}
	snapshotCreatedStatus(cr).applyTo(target)

	// Compare the whole status, conditions excluded -- applyTo merges those, and their timestamps differ
	// per call.
	got := target.Status
	got.ConditionedStatus = rtv1.ConditionedStatus{}
	assert.Equal(t, want, got, "the snapshot must carry every data field of the status, unchanged")
}

// TestCreatedStatusSnapshotIsTakenByValue -- the snapshot must be a copy, not a view.
//
// persistStatusWithRetry applies it once per attempt against a re-read object, so if the snapshot
// aliased cr.Status, anything mutating cr between the snapshot and the final attempt would change what
// gets written. The slice field is the one that would alias without a deep copy.
func TestCreatedStatusSnapshotIsTakenByValue(t *testing.T) {
	cr := restDefinitionForStatusTest()
	cr.Status = fullyPopulatedStatus()

	st := snapshotCreatedStatus(cr)

	// Mutate the source after snapshotting, including through the slice.
	cr.Status.Digest = "sha256:changed-after-the-snapshot"
	cr.Status.AuthSecretRBACNamespaces[0] = "mutated"
	cr.Status.AuthSecretRBACNamespaces = append(cr.Status.AuthSecretRBACNamespaces, "ns-c")

	target := &definitionv1alpha1.RestDefinition{}
	st.applyTo(target)

	assert.Equal(t, "sha256:digest", target.Status.Digest,
		"the snapshot must not see a scalar changed after it was taken")
	assert.Equal(t, []string{"ns-a", "ns-b"}, target.Status.AuthSecretRBACNamespaces,
		"the snapshot must deep-copy the slice; otherwise a later mutation of cr rewrites what is persisted")
}

// TestCreatedStatusApplyToMergesConditionsRatherThanReplacing is the property that stopped this being a
// plain `target.Status = snapshot`.
//
// applyTo runs against a FRESHLY READ object. Replacing its condition list wholesale would discard a
// condition another writer set between our read and our write -- and the whole reason this code re-reads
// per attempt is to merge concurrent changes rather than clobber them. The data fields are this phase's
// to own; the condition list is shared.
func TestCreatedStatusApplyToMergesConditionsRatherThanReplacing(t *testing.T) {
	cr := restDefinitionForStatusTest()
	cr.Status = fullyPopulatedStatus()
	// A condition on the SNAPSHOT that must not be transplanted onto the target.
	cr.Status.ConditionedStatus = rtv1.ConditionedStatus{Conditions: []rtv1.Condition{{
		Type: "FromTheSnapshot", Status: "True", LastTransitionTime: metav1.Now(), Reason: "Snapshot",
	}}}

	target := &definitionv1alpha1.RestDefinition{}
	target.Status.ConditionedStatus = rtv1.ConditionedStatus{Conditions: []rtv1.Condition{{
		Type: "SetByAnotherWriter", Status: "True", LastTransitionTime: metav1.Now(), Reason: "Concurrent",
	}}}

	snapshotCreatedStatus(cr).applyTo(target)

	types := map[rtv1.ConditionType]bool{}
	for _, c := range target.Status.Conditions {
		types[c.Type] = true
	}

	assert.True(t, types["SetByAnotherWriter"],
		"a condition another writer set must survive; replacing the list would clobber it")
	assert.False(t, types["FromTheSnapshot"],
		"the snapshot's own conditions must not be transplanted onto a freshly read object")
	assert.NotEmpty(t, target.Status.Conditions, "applyTo must still merge its own Creating condition")

	// And the data fields still land.
	assert.Equal(t, "sha256:digest", target.Status.Digest)
}

// TestCreatedStatusCarriesTheAuthSecretFields is the named regression for the two fields the hand-filled
// version dropped, kept so a failure points at the cause rather than at a whole-struct diff.
//
// An empty AuthSecretDigest makes Observe's auth-secret drift check compare empty against a real hash,
// report drift that does not exist, and redeploy on every reconcile. An empty AuthSecretRBACNamespaces
// makes teardownAuthSecretRBAC return early on Delete, leaking RBAC in every namespace it provisioned.
func TestCreatedStatusCarriesTheAuthSecretFields(t *testing.T) {
	cr := restDefinitionForStatusTest()
	cr.Status.AuthSecretDigest = "sha256:authsecret"
	cr.Status.AuthSecretRBACNamespaces = []string{"krateo-system", "team-a"}

	target := &definitionv1alpha1.RestDefinition{}
	snapshotCreatedStatus(cr).applyTo(target)

	assert.Equal(t, "sha256:authsecret", target.Status.AuthSecretDigest,
		"an empty authSecretDigest makes Observe report drift on every reconcile, redeploying forever")
	assert.Equal(t, []string{"krateo-system", "team-a"}, target.Status.AuthSecretRBACNamespaces,
		"an empty authSecretRBACNamespaces makes teardownAuthSecretRBAC return early on Delete, leaking RBAC")
}

// TestCreatedStatusApplyToIsIdempotent -- persistStatusWithRetry runs applyTo once per write attempt plus
// once to mirror onto the caller's copy, so applying twice must be indistinguishable from once.
func TestCreatedStatusApplyToIsIdempotent(t *testing.T) {
	cr := restDefinitionForStatusTest()
	cr.Status = fullyPopulatedStatus()
	st := snapshotCreatedStatus(cr)

	once := &definitionv1alpha1.RestDefinition{}
	st.applyTo(once)

	twice := &definitionv1alpha1.RestDefinition{}
	st.applyTo(twice)
	st.applyTo(twice)

	require.NotNil(t, once.Status.HasSecuritySchemes)
	require.NotNil(t, twice.Status.HasSecuritySchemes)
	assert.Equal(t, *once.Status.HasSecuritySchemes, *twice.Status.HasSecuritySchemes)

	o, w := once.Status, twice.Status
	o.ConditionedStatus, w.ConditionedStatus = rtv1.ConditionedStatus{}, rtv1.ConditionedStatus{}
	assert.Equal(t, o, w, "applying twice must be indistinguishable from applying once")
}
