package restdefinition

import (
	"context"
	"reflect"
	"testing"

	"github.com/krateo-platformops/oasgen-provider/apis"
	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// conflictOnFirstStatusUpdate makes the first status write fail with a resource-version conflict and every
// later one succeed — which is what a fresh install actually does to this write when many RestDefinitions
// reconcile at once.
func conflictOnFirstStatusUpdate(attempts *int) interceptor.Funcs {
	return interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			*attempts++
			if *attempts == 1 {
				return apierrors.NewConflict(
					schema.GroupResource{Group: "ogen.krateo.io", Resource: "restdefinitions"},
					obj.GetName(),
					assert.AnError,
				)
			}
			return c.Status().Update(ctx, obj, opts...)
		},
	}
}

func externalForStatusTest(t *testing.T, cr *definitionv1alpha1.RestDefinition, funcs interceptor.Funcs) *external {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, apis.AddToScheme(s))

	return &external{
		kube: fake.NewClientBuilder().
			WithScheme(s).
			WithObjects(cr.DeepCopy()).
			WithStatusSubresource(&definitionv1alpha1.RestDefinition{}).
			WithInterceptorFuncs(funcs).
			Build(),
		log: logging.NewNopLogger(),
		rec: record.NewFakeRecorder(100),
	}
}

func restDefinitionForStatusTest() *definitionv1alpha1.RestDefinition {
	return &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "actionsvariablerepo", Namespace: "krateo-system"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			OASPath:       "configmap://krateo-system/gh/openapi.yaml",
			ResourceGroup: "github.krateo.io",
			Resource: definitionv1alpha1.Resource{
				Kind:        "ActionsVariableRepo",
				Identifiers: []string{"name"},
			},
		},
	}
}

var statusTestGVK = schema.GroupVersionKind{
	Group:   "github.krateo.io",
	Version: "v1-0",
	Kind:    "ActionsVariableRepo",
}

// TestCreatePhaseStatusSurvivesAConflict is the Path A regression from #252.
//
// A plain Status().Update returns a resource-version conflict as an error, and Create returned it — so
// provider-runtime, unable to know how much of an errored Create took effect, left
// krateo.io/external-create-pending set and every later reconcile answered "cannot determine creation
// result". The resource stayed permanently not-Ready with its CRDs applied and no Deployment, and nothing
// repaired it.
//
// A conflict here is ordinary rather than exceptional: it was measured 70ms after the CRDs were applied on
// a fresh GKE cluster, which is precisely when many RestDefinitions reconcile together.
func TestCreatePhaseStatusSurvivesAConflict(t *testing.T) {
	cr := restDefinitionForStatusTest()
	attempts := 0
	e := externalForStatusTest(t, cr, conflictOnFirstStatusUpdate(&attempts))

	err := e.persistCreatePhaseStatus(context.Background(), cr, statusTestGVK, true)
	require.NoError(t, err, "a resource-version conflict must be retried, not returned: returning it strands the resource forever")
	assert.Equal(t, 2, attempts, "expected exactly one retry after the injected conflict")
}

// TestCreatePhaseStatusRecordsTheAppliedVersion is the Path B regression from #252.
//
// The first phase applied the CRD at a version and recorded only HasSecuritySchemes, so
// status.resource.apiVersion stayed empty, observedVersion fell back to resourceVersion, and the next
// Observe looked the CRD up at the WRONG version — reporting not-exists for a CRD that existed and sending
// the runtime back into the create handshake. Measured: external-create-pending re-set 97 seconds AFTER
// external-create-succeeded.
//
// The version is not a guess; it is the gvk whose CRD was just applied.
func TestCreatePhaseStatusRecordsTheAppliedVersion(t *testing.T) {
	cr := restDefinitionForStatusTest()
	attempts := 0
	e := externalForStatusTest(t, cr, conflictOnFirstStatusUpdate(&attempts))

	require.NoError(t, e.persistCreatePhaseStatus(context.Background(), cr, statusTestGVK, true))

	// Persisted, not merely set in memory.
	stored := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, e.kube.Get(context.Background(), client.ObjectKeyFromObject(cr), stored))

	assert.Equal(t, "github.krateo.io/v1-0", stored.Status.Resource.APIVersion,
		"the version the CRD was applied at must be recorded, or the next Observe looks in the wrong place")
	assert.Equal(t, "ActionsVariableRepo", stored.Status.Resource.Kind)
	require.NotNil(t, stored.Status.HasSecuritySchemes)
	assert.True(t, *stored.Status.HasSecuritySchemes)

	// And observedVersion must now resolve to it rather than the fallback, which is the whole point.
	assert.Equal(t, "v1-0", observedVersion(stored),
		"observedVersion must return the applied version so the primary CRD lookup uses the right GVR")

	// The caller's copy is mirrored too, so the rest of the reconcile sees what was written.
	assert.Equal(t, "github.krateo.io/v1-0", cr.Status.Resource.APIVersion)
}

// TestCreatePhaseStatusGivesUpOnPersistentConflict pins that the retry is BOUNDED and that a genuine,
// persistent failure still surfaces.
//
// This is the other half of the conflict behaviour, and it is the one that keeps the fix honest: retrying
// forever would turn a real problem into a hang, and swallowing the error would tell provider-runtime the
// create succeeded when nothing was written. The attempt count is what distinguishes "retried" from "tried
// once" — if the retry were removed, TestCreatePhaseStatusSurvivesAConflict would see attempts == 1 and
// its require.NoError would fail.
func TestCreatePhaseStatusGivesUpOnPersistentConflict(t *testing.T) {
	cr := restDefinitionForStatusTest()
	attempts := 0
	alwaysConflict := interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			attempts++
			return apierrors.NewConflict(
				schema.GroupResource{Group: "ogen.krateo.io", Resource: "restdefinitions"},
				obj.GetName(),
				assert.AnError,
			)
		},
	}
	e := externalForStatusTest(t, cr, alwaysConflict)

	err := e.persistCreatePhaseStatus(context.Background(), cr, statusTestGVK, true)
	require.Error(t, err, "a conflict that never clears must surface, not be swallowed as success")
	assert.True(t, apierrors.IsConflict(err), "the original conflict should not be masked, got %v", err)

	assert.Greater(t, attempts, 1, "it must have retried rather than failing on the first attempt")
	assert.Less(t, attempts, 20, "the retry must be bounded, not unbounded")
}

// TestCreatePhaseOnePersistsEveryFieldSetBeforeIt is the #491 regression. It is the THIRD version of
// this test; the first two were vacuous and a mutation caught both. That history is the useful part.
//
// V1 asserted snapshotStatus(cr).applyTo(target). The snapshot path was ALWAYS complete -- that is what
// TestCreatedStatusSnapshotCarriesEveryField proves -- and the bug was that persistCreatePhaseStatus did
// not USE it. Passed before the fix and after.
//
// V2 called the real function but seeded the fake apiserver from a cr that ALREADY carried the warnings,
// because externalForStatusTest stores cr.DeepCopy(). The re-read object therefore had them whatever the
// write did, so reverting to the shipped code still passed.
//
// V3 REPRODUCES THE FRESH-INSTALL SEQUENCE, which is the only shape that can fail: the STORED object has
// an empty status, and the warnings exist ONLY on the in-memory cr -- exactly as generateAndApplyCRDs
// leaves things, having set cr.Status.SchemaWarnings in place a few lines before the call. A write that
// hand-lists fields onto a freshly read object then loses them, and nothing fails where the mistake is.
//
// Verified able to fail: reverting persistCreatePhaseStatus to the exact 0.54.0 closure makes this fail
// and name SchemaWarnings.
//
// What it cost not to have this: status.schemaWarnings shipped in 0.54.0 and was never persisted on a
// fresh install. Measured by github-provider-kog -- 32 definitions, 5 warnings in the oasgen log, 0 on
// status -- with a comment-only ConfigMap edit as the control, which took the Update path and persisted
// correctly 64 seconds later. FIVE existing tests covered this feature or this function and none could
// fail on it.
func TestCreatePhaseOnePersistsEveryFieldSetBeforeIt(t *testing.T) {
	// The STORED object as a fresh install has it: no status yet.
	stored := restDefinitionForStatusTest()
	stored.Status = definitionv1alpha1.RestDefinitionStatus{}
	e := externalForStatusTest(t, stored, interceptor.Funcs{})

	// The IN-MEMORY cr, as generateAndApplyCRDs leaves it: warnings set in place, nothing persisted.
	cr := restDefinitionForStatusTest()
	cr.Status = fullyPopulatedStatus()

	require.NoError(t, e.persistCreatePhaseStatus(context.Background(), cr, statusTestGVK, true))

	got := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, e.kube.Get(context.Background(), client.ObjectKeyFromObject(cr), got))

	want := fullyPopulatedStatus()
	v := reflect.ValueOf(want)
	gv := reflect.ValueOf(got.Status)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if name == "ConditionedStatus" {
			continue // the write sets Creating; conditions are asserted by the tests above
		}
		require.False(t, gv.Field(i).IsZero(),
			"RestDefinitionStatus.%s was set on cr before Create's first phase and is ZERO ON THE "+
				"PERSISTED OBJECT. The write targets a freshly READ object, so a field set in place and "+
				"not carried by the write is never persisted -- that is #491, and before it #263. Do not "+
				"fix this by adding the field to a list: the write must carry the whole status.", name)
	}

	require.Equal(t, want.SchemaWarnings, got.Status.SchemaWarnings,
		"schemaWarnings specifically: the field #491 lost on every fresh install")
}
