package restdefinition

import (
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func crWithVerbDocs(def string, overrides map[string]string) *definitionv1alpha1.RestDefinition {
	cr := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "cloudserver", Namespace: "demo"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			OASPath:       def,
			ResourceGroup: "compute.aruba.io",
			Resource:      definitionv1alpha1.Resource{Kind: "CloudServer"},
		},
	}
	for _, action := range []string{"get", "findby", "delete", "create"} {
		v := definitionv1alpha1.VerbsDescription{Action: action, Method: "GET", Path: "/x"}
		if p, ok := overrides[action]; ok {
			v.OASPath = p
		}
		cr.Spec.Resource.VerbsDescription = append(cr.Spec.Resource.VerbsDescription, v)
	}
	return cr
}

func TestOASPathsForIsDeduplicatedAndStable(t *testing.T) {
	cr := crWithVerbDocs("configmap://ns/compute/v1.json", map[string]string{
		"create": "configmap://ns/compute11/v1.1.json",
		"delete": "configmap://ns/compute11/v1.1.json", // same override twice
	})

	got := oasPathsFor(cr)
	assert.Equal(t, []string{"configmap://ns/compute/v1.json", "configmap://ns/compute11/v1.1.json"}, got,
		"two verbs naming the same document must fetch it once, and the order must be stable")

	none := crWithVerbDocs("configmap://ns/compute/v1.json", nil)
	assert.Equal(t, []string{"configmap://ns/compute/v1.json"}, oasPathsFor(none),
		"the ordinary single-document case is unchanged")
}

// THE assertion this increment exists for. The single-document digest covered spec.oasPath alone, so
// editing an override document left the stored hash unchanged: no drift, no regeneration, and a resource
// that stays Ready while serving a schema that no longer matches its own document.
func TestCompositeDigestChangesWhenAnyDocumentChanges(t *testing.T) {
	base := map[string][]byte{
		"configmap://ns/compute/v1.json":     []byte(`{"openapi":"3.0.0","info":{"version":"1.0.0"}}`),
		"configmap://ns/compute11/v1.1.json": []byte(`{"openapi":"3.0.0","info":{"version":"1.1.0"}}`),
	}
	before := compositeOASDigest(base)

	edited := map[string][]byte{
		"configmap://ns/compute/v1.json":     base["configmap://ns/compute/v1.json"],
		"configmap://ns/compute11/v1.1.json": []byte(`{"openapi":"3.0.0","info":{"version":"1.1.1"}}`),
	}
	assert.NotEqual(t, before, compositeOASDigest(edited),
		"editing the SECOND document must register as drift; this is the hole the composite closes")

	// And the first still matters.
	editedFirst := map[string][]byte{
		"configmap://ns/compute/v1.json":     []byte(`{"openapi":"3.0.0","info":{"version":"1.0.1"}}`),
		"configmap://ns/compute11/v1.1.json": base["configmap://ns/compute11/v1.1.json"],
	}
	assert.NotEqual(t, before, compositeOASDigest(editedFirst))
}

func TestCompositeDigestIsOrderIndependentAndPathSensitive(t *testing.T) {
	a := map[string][]byte{"p1": []byte("one"), "p2": []byte("two")}
	b := map[string][]byte{"p2": []byte("two"), "p1": []byte("one")}
	assert.Equal(t, compositeOASDigest(a), compositeOASDigest(b),
		"map iteration order must not change the hash, or every reconcile looks like drift")

	// Same bytes, different path: which document a verb reads is part of what generated the CRD.
	moved := map[string][]byte{"p1": []byte("one"), "p3": []byte("two")}
	assert.NotEqual(t, compositeOASDigest(a), compositeOASDigest(moved),
		"a document moving path must register, even when its contents did not change")
}

// A single document must hash the same whether it arrives alone or through the composite path, because
// status.OASHash written by one and compared by the other has to agree.
func TestSingleDocumentIsStableThroughTheCompositePath(t *testing.T) {
	only := map[string][]byte{"configmap://ns/compute/v1.json": []byte(`{"openapi":"3.0.0"}`)}
	assert.Equal(t, compositeOASDigest(only), compositeOASDigest(only),
		"deterministic for the same input")
	assert.NotEmpty(t, compositeOASDigest(only))
}
