package crd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	restdefinitionsv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/crd/generation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(s))
	// RestDefinition too: the ownership check now asks whether the annotated owner still EXISTS, so a
	// scheme that cannot represent one would make every owner look dangling (#137).
	require.NoError(t, restdefinitionsv1alpha1.SchemeBuilder.AddToScheme(s))
	return s
}

// liveRD builds a RestDefinition for "namespace/name", so a test can distinguish an owner that exists from
// a husk that does not. Before #137 that distinction did not exist and no test needed it.
func liveRD(t *testing.T, nsName string) *restdefinitionsv1alpha1.RestDefinition {
	t.Helper()
	ns, name, ok := strings.Cut(nsName, "/")
	require.True(t, ok, "owner must be namespace/name")
	return &restdefinitionsv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
	}
}

// genCRD builds a single-version generated CRD, as crdgen would emit (served+storage on the one version).
func genCRD(group, kind, plural, version, desc string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: plural + "." + group},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: kind, Plural: plural},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name:    version,
				Served:  true,
				Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"spec":   {Type: "object", Description: desc},
							"status": {Type: "object"},
						},
					},
				},
			}},
		},
	}
}

func findVer(crd *apiextensionsv1.CustomResourceDefinition, name string) *apiextensionsv1.CustomResourceDefinitionVersion {
	for i := range crd.Spec.Versions {
		if crd.Spec.Versions[i].Name == name {
			return &crd.Spec.Versions[i]
		}
	}
	return nil
}

func specDesc(v *apiextensionsv1.CustomResourceDefinitionVersion) string {
	return v.Schema.OpenAPIV3Schema.Properties["spec"].Description
}

func vacuum(name string) apiextensionsv1.CustomResourceDefinitionVersion {
	return apiextensionsv1.CustomResourceDefinitionVersion{
		Name: name, Served: false, Storage: true,
		Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
			Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{"status": {Type: "object"}},
		}},
	}
}

func TestApplyOrUpdateCRD_Create(t *testing.T) {
	cli := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	newcrd := genCRD("github.krateo.io", "PullRequest", "pullrequests", "v1-0-0", "A")

	out, err := ApplyOrUpdateCRD(context.Background(), cli, newcrd, "demo/rd")
	require.NoError(t, err)
	assert.Equal(t, schema.GroupVersionResource{Group: "github.krateo.io", Version: "v1-0-0", Resource: "pullrequests"}, out.GVR)
	assert.Empty(t, out.AdoptedFrom, "nothing was displaced by a plain create")

	got, err := Get(context.Background(), cli, out.GVR.GroupResource())
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Len(t, got.Spec.Versions, 1)
	require.NotEmpty(t, got.Spec.Versions[0].AdditionalPrinterColumns, "VERSION column added on create")
	assert.Equal(t, "VERSION", got.Spec.Versions[0].AdditionalPrinterColumns[0].Name)
}

func TestApplyOrUpdateCRD_InPlaceBreaking(t *testing.T) {
	// live: v1-0-0 (schema A, served, non-storage) + vacuum (storage).
	live := genCRD("g", "K", "widgets", "v1-0-0", "A")
	live.Spec.Versions[0].Storage = false
	live.Spec.Versions = append(live.Spec.Versions, vacuum(generation.VacuumVersionName))
	cli := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(live).Build()

	// re-apply v1-0-0 with a BREAKING new spec schema B
	_, err := ApplyOrUpdateCRD(context.Background(), cli, genCRD("g", "K", "widgets", "v1-0-0", "B"), "demo/rd")
	require.NoError(t, err)

	got, err := Get(context.Background(), cli, schema.GroupResource{Group: "g", Resource: "widgets"})
	require.NoError(t, err)
	v := findVer(got, "v1-0-0")
	require.NotNil(t, v)
	assert.Equal(t, "B", specDesc(v), "matching version's spec schema replaced in place (breaking allowed)")
	assert.True(t, v.Served, "served flag preserved")
	assert.False(t, v.Storage, "vacuum still holds storage; the served version is not flipped to storage")
	require.NotNil(t, findVer(got, generation.VacuumVersionName), "vacuum preserved")
	require.NotNil(t, got.Spec.Conversion)
	assert.Equal(t, apiextensionsv1.NoneConverter, got.Spec.Conversion.Strategy)
}

func TestApplyOrUpdateCRD_AppendNewVersion(t *testing.T) {
	// live: single version v1-0-0 (served+storage), no vacuum yet.
	live := genCRD("g", "K", "widgets", "v1-0-0", "A")
	cli := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(live).Build()

	_, err := ApplyOrUpdateCRD(context.Background(), cli, genCRD("g", "K", "widgets", "v1-1-0", "B"), "demo/rd")
	require.NoError(t, err)

	got, err := Get(context.Background(), cli, schema.GroupResource{Group: "g", Resource: "widgets"})
	require.NoError(t, err)
	require.Len(t, got.Spec.Versions, 3, "v1-0-0, v1-1-0, vacuum")
	require.NotNil(t, findVer(got, "v1-0-0"))
	require.NotNil(t, findVer(got, "v1-1-0"))
	vac := findVer(got, generation.VacuumVersionName)
	require.NotNil(t, vac)
	assert.True(t, vac.Storage)
	assert.False(t, vac.Served)
	for _, n := range []string{"v1-0-0", "v1-1-0"} {
		v := findVer(got, n)
		assert.True(t, v.Served, "%s served", n)
		assert.False(t, v.Storage, "%s not storage (vacuum is)", n)
	}
	require.NotNil(t, got.Spec.Conversion)
	assert.Equal(t, apiextensionsv1.NoneConverter, got.Spec.Conversion.Strategy)
}

// The merge path uses optimistic concurrency: a conflicting Update must be retried (re-read + re-merge),
// not surfaced as an error. Inject a 409 on the first Update and assert ApplyOrUpdateCRD still succeeds and
// the change lands, with the vacuum preserved across the retry.
func TestApplyOrUpdateCRD_RetriesOnConflict(t *testing.T) {
	live := genCRD("g", "K", "widgets", "v1-0-0", "A")
	live.Spec.Versions[0].Storage = false
	live.Spec.Versions = append(live.Spec.Versions, vacuum(generation.VacuumVersionName))

	updateCalls := 0
	cli := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(live).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				updateCalls++
				if updateCalls == 1 {
					return apierrors.NewConflict(
						schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"},
						obj.GetName(), fmt.Errorf("simulated conflict"))
				}
				return c.Update(ctx, obj, opts...)
			},
		}).Build()

	_, err := ApplyOrUpdateCRD(context.Background(), cli, genCRD("g", "K", "widgets", "v1-0-0", "B"), "demo/rd")
	require.NoError(t, err, "conflict must be retried, not returned")
	assert.GreaterOrEqual(t, updateCalls, 2, "first Update conflicted and the retry re-ran")

	got, err := Get(context.Background(), cli, schema.GroupResource{Group: "g", Resource: "widgets"})
	require.NoError(t, err)
	assert.Equal(t, "B", specDesc(findVer(got, "v1-0-0")), "in-place change applied after the retry")
	require.NotNil(t, findVer(got, generation.VacuumVersionName), "vacuum preserved through the retry")
}

// Regression guard for the original clobber bug: a full-PUT apply must not drop a sibling served version.
func TestApplyOrUpdateCRD_InPlaceKeepsSiblingVersion(t *testing.T) {
	live := genCRD("g", "K", "widgets", "v1-0-0", "A")
	live.Spec.Versions[0].Storage = false
	live.Spec.Versions = append(live.Spec.Versions,
		apiextensionsv1.CustomResourceDefinitionVersion{
			Name: "v1-1-0", Served: true, Storage: false,
			Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
				Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{"spec": {Type: "object", Description: "B"}, "status": {Type: "object"}},
			}},
		},
		vacuum(generation.VacuumVersionName),
	)
	cli := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(live).Build()

	// in-place update of v1-0-0 only
	_, err := ApplyOrUpdateCRD(context.Background(), cli, genCRD("g", "K", "widgets", "v1-0-0", "A2"), "demo/rd")
	require.NoError(t, err)

	got, err := Get(context.Background(), cli, schema.GroupResource{Group: "g", Resource: "widgets"})
	require.NoError(t, err)
	assert.Equal(t, "A2", specDesc(findVer(got, "v1-0-0")), "v1-0-0 updated")
	sib := findVer(got, "v1-1-0")
	require.NotNil(t, sib, "sibling served version must survive the full-PUT apply")
	assert.Equal(t, "B", specDesc(sib), "sibling schema untouched")
	require.NotNil(t, findVer(got, generation.VacuumVersionName), "vacuum survives")
}

// The ownership guard: a CRD owned by one LIVE RestDefinition must not be modified by another (which would put
// two RDCs on the same resource). A live foreign owner is rejected with *ErrOwnershipConflict, CRD untouched.
//
// The RestDefinition object below is not decoration. Before #137 this test passed without it, because the
// check compared strings and never looked the owner up -- which is exactly why a husk that had been deleted
// blocked regeneration forever. The guard is only meaningful when the owner it names actually exists.
func TestApplyOrUpdateCRD_RejectsForeignOwner(t *testing.T) {
	live := genCRD("g", "K", "widgets", "v1-0-0", "A")
	live.Annotations = map[string]string{OwnerAnnotation: "demo/rd-a"}
	cli := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(live, liveRD(t, "demo/rd-a")).Build()

	_, err := ApplyOrUpdateCRD(context.Background(), cli, genCRD("g", "K", "widgets", "v1-0-0", "B"), "demo/rd-b")
	require.Error(t, err)
	var conflict *ErrOwnershipConflict
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, "demo/rd-a", conflict.Owner)
	assert.Equal(t, "demo/rd-b", conflict.Requester)

	got, gerr := Get(context.Background(), cli, schema.GroupResource{Group: "g", Resource: "widgets"})
	require.NoError(t, gerr)
	assert.Equal(t, "A", specDesc(findVer(got, "v1-0-0")), "a foreign RestDefinition must not modify the CRD")
	assert.Equal(t, "demo/rd-a", got.Annotations[OwnerAnnotation], "owner unchanged")
}

// Create stamps the owner; a pre-existing unowned CRD is adopted (owner stamped) and updated.
func TestApplyOrUpdateCRD_CreateStampsOwnerAndAdoptsUnowned(t *testing.T) {
	cli := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	_, err := ApplyOrUpdateCRD(context.Background(), cli, genCRD("g", "K", "widgets", "v1-0-0", "A"), "demo/rd-a")
	require.NoError(t, err)
	got, _ := Get(context.Background(), cli, schema.GroupResource{Group: "g", Resource: "widgets"})
	assert.Equal(t, "demo/rd-a", got.Annotations[OwnerAnnotation], "create stamps the owner")

	unowned := genCRD("g2", "K2", "gadgets", "v1-0-0", "A") // no owner annotation
	cli2 := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(unowned).Build()
	_, err = ApplyOrUpdateCRD(context.Background(), cli2, genCRD("g2", "K2", "gadgets", "v1-0-0", "B"), "demo/rd-b")
	require.NoError(t, err, "an unowned CRD is adopted")
	got2, _ := Get(context.Background(), cli2, schema.GroupResource{Group: "g2", Resource: "gadgets"})
	assert.Equal(t, "demo/rd-b", got2.Annotations[OwnerAnnotation], "unowned CRD adopted + stamped")
	assert.Equal(t, "B", specDesc(findVer(got2, "v1-0-0")), "in-place update applied on adopt")
}

// TestApplyOrUpdateCRD_AdoptsDanglingOwner is the fix for #137.
//
// A RestDefinition that no longer exists cannot double-reconcile anything, so its leftover annotation is not
// a conflict — it is litter. Before this, the two were indistinguishable, and the consequence was not a
// cosmetic error: regeneration was refused permanently, with no path back that did not involve a human
// editing annotations by hand. On a live cluster that was a 1h40m outage triggered by an ordinary chart bump.
func TestApplyOrUpdateCRD_AdoptsDanglingOwner(t *testing.T) {
	live := genCRD("g", "K", "widgets", "v1-0-0", "A")
	live.Annotations = map[string]string{OwnerAnnotation: "demo/rd-husk"}
	// Note what is NOT in the fake client: any RestDefinition called demo/rd-husk.
	cli := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(live).Build()

	out, err := ApplyOrUpdateCRD(context.Background(), cli, genCRD("g", "K", "widgets", "v1-0-0", "B"), "demo/rd-live")
	require.NoError(t, err, "a deleted owner must not block regeneration")
	assert.Equal(t, "demo/rd-husk", out.AdoptedFrom,
		"the displaced owner must be reported so the caller can raise an Event: a self-healing fix that heals "+
			"silently teaches nobody that the husk was there")

	got, gerr := Get(context.Background(), cli, schema.GroupResource{Group: "g", Resource: "widgets"})
	require.NoError(t, gerr)
	assert.Equal(t, "demo/rd-live", got.Annotations[OwnerAnnotation], "ownership moves to the live RestDefinition")
	assert.Equal(t, "B", specDesc(findVer(got, "v1-0-0")), "and the regeneration actually applied")
}

// TestApplyOrUpdateCRD_UndeterminedOwnerNeitherAdoptsNorConflicts is the forbidding test.
//
// A failed lookup is not evidence of absence. Adopting on it would hand a CRD to a second controller on the
// strength of an RBAC denial or a dropped connection — turning a loud, recoverable refusal into a silent,
// corrupting double-reconcile. It is equally not evidence of presence. Both answers must stay unavailable.
//
// This is the same discipline as pagination's Indeterminate (#119), and it is the reason that one is a type.
func TestApplyOrUpdateCRD_UndeterminedOwnerNeitherAdoptsNorConflicts(t *testing.T) {
	live := genCRD("g", "K", "widgets", "v1-0-0", "A")
	live.Annotations = map[string]string{OwnerAnnotation: "demo/rd-a"}

	// Fail ONLY the RestDefinition lookup; CRD reads must still work, or the test would be proving that a
	// broken client fails rather than that an unverifiable owner is refused.
	cli := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(live).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, isRD := obj.(*unstructured.Unstructured); isRD && key.Namespace != "" {
					return apierrors.NewForbidden(
						schema.GroupResource{Group: "ogen.krateo.io", Resource: "restdefinitions"}, key.Name,
						fmt.Errorf("no RBAC for restdefinitions"))
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).Build()

	out, err := ApplyOrUpdateCRD(context.Background(), cli, genCRD("g", "K", "widgets", "v1-0-0", "B"), "demo/rd-b")
	require.Error(t, err)

	var undetermined *ErrOwnershipUndetermined
	require.ErrorAs(t, err, &undetermined,
		"a lookup failure must be its own verdict, not a conflict and above all not an adoption")
	assert.Equal(t, "demo/rd-a", undetermined.Owner)

	var conflict *ErrOwnershipConflict
	assert.NotErrorAs(t, err, &conflict, "an unverifiable owner is not a proven conflict either")
	assert.Empty(t, out.AdoptedFrom, "nothing may be adopted on a failed lookup")

	got, gerr := Get(context.Background(), cli, schema.GroupResource{Group: "g", Resource: "widgets"})
	require.NoError(t, gerr)
	assert.Equal(t, "A", specDesc(findVer(got, "v1-0-0")), "the CRD must be untouched")
	assert.Equal(t, "demo/rd-a", got.Annotations[OwnerAnnotation], "and the owner annotation must be untouched")
}

// A malformed annotation names nobody, so it cannot be a live owner. Treating it as undetermined would wedge
// forever on a value no future reconcile can improve — the #137 failure mode reintroduced by another route.
func TestApplyOrUpdateCRD_MalformedOwnerAnnotationIsAdopted(t *testing.T) {
	for _, bad := range []string{"no-slash", "/only-name", "only-ns/"} {
		t.Run(bad, func(t *testing.T) {
			live := genCRD("g", "K", "widgets", "v1-0-0", "A")
			live.Annotations = map[string]string{OwnerAnnotation: bad}
			cli := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(live).Build()

			out, err := ApplyOrUpdateCRD(context.Background(), cli, genCRD("g", "K", "widgets", "v1-0-0", "B"), "demo/rd-live")
			require.NoError(t, err)
			assert.Equal(t, bad, out.AdoptedFrom)

			got, _ := Get(context.Background(), cli, schema.GroupResource{Group: "g", Resource: "widgets"})
			assert.Equal(t, "demo/rd-live", got.Annotations[OwnerAnnotation])
		})
	}
}
