//go:build unit

package restdefinition

import (
	"context"
	"testing"

	"github.com/krateo-platformops/oasgen-provider/apis"
	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/provider-runtime/pkg/meta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// ISSUE 576: a failed instance list was read as zero instances, so the finalizer that exists to stop a
// RestDefinition being deleted while its generated Kind still has instances was removed by the very
// failure it is most likely to meet.
//
// Two distinct silent zeros, both from matching error TEXT:
//
//	"the server could not find the requested resource"  -> items nilled, error CLEARED, count 0
//	"no matches for"                                     -> the whole block skipped, so the error stayed
//	                                                        non-nil and was then OVERWRITTEN by the
//	                                                        Update calls below, which reused `err`
//
// And the obvious fix -- classify with IsNoMatchError and treat a no-match as "no instances" -- would
// reintroduce it through a race this repo had already measured: `no matches for kind` is indistinguishable
// from a missing CRD (#314), and the RESTMapper lags CRD creation (five RestDefinitions Ready at t+148s,
// five of their CRDs appearing at t+149s). An undiscoverable kind whose CRD exists may hold instances.
//
// So the question these tests ask is never "did it count correctly" but **"may the finalizer be removed
// on this evidence"**, and the only answer that permits removal is a CRD that is genuinely absent.

func fzScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(s))
	require.NoError(t, apis.AddToScheme(s))
	return s
}

func fzDefinition(withFinalizer bool) *definitionv1alpha1.RestDefinition {
	cr := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "petstore", Namespace: "krateo-system"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			ResourceGroup: "petstore.krateo.io",
			Resource:      definitionv1alpha1.Resource{Kind: "Pet"},
		},
		Status: definitionv1alpha1.RestDefinitionStatus{
			Resource: definitionv1alpha1.KindApiVersion{Kind: "Pet", APIVersion: "petstore.krateo.io/v1alpha1"},
		},
	}
	if withFinalizer {
		meta.AddFinalizer(cr, restresourcesStillExistFinalizer)
	}
	return cr
}

// fzLive seeds cr (plus extras) into a fake client and returns a copy READ BACK FROM IT, so the object
// carries a server-assigned resourceVersion. manageFinalizers persists its decision with an Update, and
// an in-memory object with an empty resourceVersion is rejected as a conflict -- which is how the second
// run of this test failed. The real caller passes an object it read from the apiserver, so this is also
// the faithful shape.
func fzLive(t *testing.T, cr *definitionv1alpha1.RestDefinition, funcs interceptor.Funcs, extras ...client.Object) (client.Client, *definitionv1alpha1.RestDefinition) {
	t.Helper()
	objs := append([]client.Object{cr.DeepCopy()}, extras...)
	kube := fake.NewClientBuilder().WithScheme(fzScheme(t)).WithObjects(objs...).
		WithInterceptorFuncs(funcs).Build()

	live := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, kube.Get(context.Background(), client.ObjectKeyFromObject(cr), live))
	require.NotEmpty(t, live.GetResourceVersion(),
		"premise: the fake client must assign a resourceVersion, or every Update below conflicts")
	return kube, live
}

// the CRD the generated Kind would have: <plural>.<group>
func fzCRD() *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "pets.petstore.krateo.io"},
	}
}

func noMatchErr() error {
	return &apimeta.NoKindMatchError{
		GroupKind:        schema.GroupKind{Group: "petstore.krateo.io", Kind: "Pet"},
		SearchedVersions: []string{"v1alpha1"},
	}
}

func notFoundErr() error {
	return apierrors.NewNotFound(schema.GroupResource{Group: "petstore.krateo.io", Resource: "pets"}, "")
}

func TestAFailedInstanceListNeverRemovesTheFinalizer(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name          string
		listErr       error
		seedCRD       bool
		crdGetFails   bool
		wantErr       bool
		wantFinalizer bool
		why           string
	}{
		{
			// THE #314 RACE, and the reason the obvious fix is wrong. The mapper does not know the kind
			// yet, but the CRD is right there and may hold instances this client cannot list.
			name: "unknown kind but the CRD EXISTS", listErr: noMatchErr(), seedCRD: true,
			wantErr: true, wantFinalizer: true,
			why: "instances may exist and cannot be counted; removing the finalizer here deletes a " +
				"RestDefinition whose resources are live",
		},
		{
			name: "NotFound but the CRD EXISTS", listErr: notFoundErr(), seedCRD: true,
			wantErr: true, wantFinalizer: true,
			why: "same ambiguity reached through the other error type",
		},
		{
			// THE ONE PATH THAT MAY REMOVE IT. No CRD means no custom resources, because deleting a CRD
			// deletes its instances.
			name: "unknown kind and the CRD is genuinely ABSENT", listErr: noMatchErr(), seedCRD: false,
			wantErr: false, wantFinalizer: false,
			why: "no instances CAN exist, so holding the finalizer would block deletion forever",
		},
		{
			name: "NotFound and the CRD is genuinely ABSENT", listErr: notFoundErr(), seedCRD: false,
			wantErr: false, wantFinalizer: false,
			why: "same, through the other error type",
		},
		{
			// A transient failure must not be classified at all.
			name: "any other list failure", listErr: apierrors.NewInternalError(assert.AnError), seedCRD: true,
			wantErr: true, wantFinalizer: true,
			why: "a timeout, an RBAC refusal or an unhealthy apiserver says nothing about the count",
		},
		{
			// THE ARM THAT SEPARATES THE AXES. The row above seeds the CRD, so removing the error-type
			// guard entirely still produces an error there (via the CRD-exists branch) and the arm passes
			// -- which is exactly what happened: that mutation SURVIVED the first version of this table.
			// With the CRD absent, a fall-through would reach "no instances can exist" and remove the
			// finalizer on a transient failure, which is issue 576 restored through a third door.
			name: "any other list failure AND the CRD is absent", listErr: apierrors.NewInternalError(assert.AnError), seedCRD: false,
			wantErr: true, wantFinalizer: true,
			why: "an InternalError is not evidence about the CRD or the count; only a no-match or a " +
				"NotFound may be followed by the CRD question at all",
		},
		{
			// Ignorance about the CRD is still ignorance. Refusing cannot delete anything.
			name: "unknown kind and the CRD check ITSELF fails", listErr: noMatchErr(), crdGetFails: true,
			wantErr: true, wantFinalizer: true,
			why: "we could not determine whether the CRD exists, so we cannot determine whether instances can",
		},
	}

	// The table must contain both outcomes, asserted rather than assumed: all-hold rows pass against a
	// function that never removes the finalizer, and all-remove rows pass against the bug itself.
	var holds, removes int
	for _, tc := range cases {
		if tc.wantFinalizer {
			holds++
		} else {
			removes++
		}
	}
	require.NotZero(t, holds, "no row expects the finalizer HELD: this table passes against issue 576")
	require.NotZero(t, removes, "no row expects the finalizer REMOVED: this table passes against a "+
		"function that never removes it, which would block every deletion forever")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var extras []client.Object
			if tc.seedCRD {
				extras = append(extras, fzCRD())
			}
			funcs := interceptor.Funcs{
				List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
					return tc.listErr
				},
			}
			if tc.crdGetFails {
				// Fails ONLY the CRD read, and DELEGATES everything else. Returning nil for other reads
				// would be a silent fake success: the RestDefinition would come back unpopulated with an
				// empty resourceVersion, and the premise assertion in fzLive is what caught that.
				funcs.Get = func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*apiextensionsv1.CustomResourceDefinition); ok {
						return apierrors.NewInternalError(assert.AnError)
					}
					return c.Get(ctx, key, obj, opts...)
				}
			}
			kube, cr := fzLive(t, fzDefinition(true), funcs, extras...)

			err := manageFinalizers(ctx, kube, cr, func(string, ...any) {})

			if tc.wantErr {
				require.Error(t, err,
					"the list could not be performed, so this must surface and the reconcile retry. %s", tc.why)
			} else {
				require.NoError(t, err, "%s", tc.why)
			}
			assert.Equal(t, tc.wantFinalizer, meta.FinalizerExists(cr, restresourcesStillExistFinalizer),
				"FINALIZER DECISION WRONG. %s", tc.why)
		})
	}
}

// The happy paths, so the fix cannot have broken counting.
func TestTheFinalizerStillTracksAListThatWorked(t *testing.T) {
	ctx := context.Background()

	t.Run("instances exist: the finalizer is added", func(t *testing.T) {
		kube, cr := fzLive(t, fzDefinition(false), interceptor.Funcs{
			List: func(_ context.Context, _ client.WithWatch, l client.ObjectList, _ ...client.ListOption) error {
				ul, ok := l.(*unstructured.UnstructuredList)
				require.True(t, ok)
				ul.Items = []unstructured.Unstructured{{Object: map[string]any{"metadata": map[string]any{"name": "p1"}}}}
				return nil
			},
		})
		require.NoError(t, manageFinalizers(ctx, kube, cr, func(string, ...any) {}))
		assert.True(t, meta.FinalizerExists(cr, restresourcesStillExistFinalizer),
			"an instance exists, so deletion must be blocked")
	})

	t.Run("a SUCCESSFUL empty list removes the finalizer", func(t *testing.T) {
		// The control for the whole fix: a list that genuinely succeeded and genuinely found nothing must
		// still remove the finalizer. If this fails, the fix has turned a working zero into a hold and
		// every RestDefinition becomes undeletable.
		kube, cr := fzLive(t, fzDefinition(true), interceptor.Funcs{
			List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
				return nil
			},
		})
		require.NoError(t, manageFinalizers(ctx, kube, cr, func(string, ...any) {}))
		assert.False(t, meta.FinalizerExists(cr, restresourcesStillExistFinalizer),
			"a successful list finding nothing must still release the finalizer, or nothing can ever be deleted")
	})
}
