package restdefinition

import (
	"context"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func rdScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := definitionv1alpha1.SchemeBuilder.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return scheme
}

func newRestDefinition(ns, name, group, kind string) *definitionv1alpha1.RestDefinition {
	return &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			ResourceGroup: group,
			Resource:      definitionv1alpha1.Resource{Kind: kind},
		},
	}
}

func configurationEvent(group, kind string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(getConfigurationGVK(newRestDefinition("", "", group, kind)))
	u.SetNamespace("some-ns")
	u.SetName("some-cfg")
	return u
}

// TestEnqueueRestDefinitionForConfiguration_MatchesOwningRestDefinition proves the mapping function finds
// exactly the RestDefinition whose generated Configuration Kind matches the event object's GVK, across
// namespaces (Configuration instances are not assumed same-namespace as their owning RestDefinition).
func TestEnqueueRestDefinitionForConfiguration_MatchesOwningRestDefinition(t *testing.T) {
	owner := newRestDefinition("ns1", "widgets", "widgets.example.io", "Widget")
	unrelated := newRestDefinition("ns2", "gadgets", "gadgets.example.io", "Gadget")
	kube := fake.NewClientBuilder().WithScheme(rdScheme(t)).WithObjects(owner, unrelated).Build()

	reqs := enqueueRestDefinitionForConfiguration(kube, logging.NewNopLogger())(context.Background(), configurationEvent("widgets.example.io", "Widget"))

	if len(reqs) != 1 {
		t.Fatalf("expected exactly one match, got %d: %v", len(reqs), reqs)
	}
	if reqs[0].Namespace != "ns1" || reqs[0].Name != "widgets" {
		t.Fatalf("expected ns1/widgets, got %s/%s", reqs[0].Namespace, reqs[0].Name)
	}
}

func TestEnqueueRestDefinitionForConfiguration_NoMatch(t *testing.T) {
	unrelated := newRestDefinition("ns2", "gadgets", "gadgets.example.io", "Gadget")
	kube := fake.NewClientBuilder().WithScheme(rdScheme(t)).WithObjects(unrelated).Build()

	reqs := enqueueRestDefinitionForConfiguration(kube, logging.NewNopLogger())(context.Background(), configurationEvent("widgets.example.io", "Widget"))

	if len(reqs) != 0 {
		t.Fatalf("expected no matches, got %v", reqs)
	}
}

func TestEnsureConfigurationWatch_NilRegistryIsNoop(t *testing.T) {
	e := &external{} // ctrl and cfgWatch both nil, as under most unit tests
	cr := newRestDefinition("ns1", "widgets", "widgets.example.io", "Widget")

	// Must not panic.
	e.ensureConfigurationWatch(cr)
}
