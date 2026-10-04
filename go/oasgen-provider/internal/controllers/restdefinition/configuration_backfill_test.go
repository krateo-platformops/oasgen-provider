package restdefinition

import (
	"context"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/crd/generation"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func cfgTestCR(withConfigFields bool) *definitionv1alpha1.RestDefinition {
	cr := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "pullrequest", Namespace: "demo"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			ResourceGroup: "github.krateo.io",
			Resource:      definitionv1alpha1.Resource{Kind: "PullRequest"},
		},
	}
	if withConfigFields {
		cr.Spec.Resource.ConfigurationFields = []definitionv1alpha1.ConfigurationField{{}}
	}
	return cr
}

func cfgTestExternal(t *testing.T, gvks []schema.GroupVersionKind, objs ...client.Object) *external {
	t.Helper()
	s := runtime.NewScheme()
	for _, g := range gvks {
		s.AddKnownTypeWithName(g, &unstructured.Unstructured{})
		lg := g
		lg.Kind += "List"
		s.AddKnownTypeWithName(lg, &unstructured.UnstructuredList{})
	}
	return &external{
		kube: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build(),
		log:  logging.NewNopLogger(),
		rec:  record.NewFakeRecorder(100),
	}
}

func obj(gvk schema.GroupVersionKind, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	u.SetName(name)
	u.SetNamespace("demo")
	return u
}

func labelOfKind(t *testing.T, e *external, gvk schema.GroupVersionKind, name string) string {
	t.Helper()
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(gvk)
	require.NoError(t, e.kube.Get(context.Background(), client.ObjectKey{Namespace: "demo", Name: name}, got))
	return got.GetLabels()[generation.VersionLabel]
}

// #180: the policy matches resources: ["*"] and stamps BOTH kinds; the backfill stamped only the
// resource kind. Instances predating the policy therefore split into a labelled half and a half that
// never would be, and the unlabelled half shrinks over time as objects happen to be touched -- so a
// stage-3 pruning decision reading "versions that have labelled instances" gets a different answer
// depending on when it runs.
func TestBackfillCoversTheConfigurationKindToo(t *testing.T) {
	cr := cfgTestCR(true)
	resGVK := schema.GroupVersionKind{Group: "github.krateo.io", Version: "v1-0-28", Kind: "PullRequest"}
	cfgGVK := getConfigurationGVK(cr)

	e := cfgTestExternal(t, []schema.GroupVersionKind{resGVK, cfgGVK},
		obj(resGVK, "pr-1"), obj(cfgGVK, "pr-1-config"))

	require.NoError(t, e.backfillVersionLabels(context.Background(), cr, resGVK, false))

	assert.Equal(t, "v1-0-28", labelOfKind(t, e, resGVK, "pr-1"),
		"the resource instance carries the resource CRD's version")
	assert.NotEmpty(t, labelOfKind(t, e, cfgGVK, "pr-1-config"),
		"the Configuration instance must be stamped too; the policy stamps it, so the backfill must agree")
}

// The version each kind is stamped with is the part most easily got wrong. The policy writes
// request.requestKind.version -- the version of the endpoint the write came through -- so a
// Configuration is labelled with the CONFIGURATION CRD's version, which differs from the resource's.
func TestConfigurationIsStampedWithItsOwnVersionNotTheResources(t *testing.T) {
	cr := cfgTestCR(true)
	resGVK := schema.GroupVersionKind{Group: "github.krateo.io", Version: "v1-0-28", Kind: "PullRequest"}
	cfgGVK := getConfigurationGVK(cr)
	require.NotEqual(t, resGVK.Version, cfgGVK.Version,
		"precondition: the two CRDs sit at different versions, which is why this distinction exists")

	e := cfgTestExternal(t, []schema.GroupVersionKind{resGVK, cfgGVK},
		obj(resGVK, "pr-1"), obj(cfgGVK, "pr-1-config"))

	require.NoError(t, e.backfillVersionLabels(context.Background(), cr, resGVK, false))

	assert.Equal(t, cfgGVK.Version, labelOfKind(t, e, cfgGVK, "pr-1-config"),
		"stamping the resource's version here would write a different value than the policy writes for "+
			"the same object, and only on objects old enough to have been backfilled")
}

// A RestDefinition with neither configuration fields nor security schemes generates no Configuration
// CRD, so there is no such kind to list and attempting it would error on every reconcile.
func TestNoConfigurationKindIsNotAnError(t *testing.T) {
	cr := cfgTestCR(false)
	resGVK := schema.GroupVersionKind{Group: "github.krateo.io", Version: "v1-0-28", Kind: "PullRequest"}

	e := cfgTestExternal(t, []schema.GroupVersionKind{resGVK}, obj(resGVK, "pr-1"))

	require.NoError(t, e.backfillVersionLabels(context.Background(), cr, resGVK, false),
		"no Configuration CRD exists for this definition, so there is nothing to backfill")
	assert.Equal(t, "v1-0-28", labelOfKind(t, e, resGVK, "pr-1"))
}
