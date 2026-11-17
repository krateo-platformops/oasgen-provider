//go:build unit

package crd

import (
	"context"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientsetscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Two RestDefinitions, same group and kind. OwnerOf is what the delete path consults before it uninstalls,
// so these pin the three answers it can give -- and the UID is the assertion, not existence: a recovery
// path that recreated the CRD would satisfy "a CRD is present" while having destroyed every CR of the kind
// (issue 763).
func TestOwnerOfAnswersTheDeletePathsQuestion(t *testing.T) {
	gr := schema.GroupResource{Group: "keycloak.ogen.krateo.io", Resource: "keycloakrealms"}

	const owner = "krateo-system/keycloak-config-kog-realm"
	live := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:        gr.String(),
			UID:         types.UID("uid-of-the-owners-crd"),
			Annotations: map[string]string{OwnerAnnotation: owner},
		},
	}

	if err := registerEventually(); err != nil {
		t.Fatalf("registering the apiextensions scheme: %v", err)
	}
	kube := fake.NewClientBuilder().WithScheme(clientsetscheme.Scheme).WithObjects(live).Build()

	got, err := OwnerOf(context.Background(), kube, gr)
	if err != nil {
		t.Fatalf("OwnerOf: %v", err)
	}
	if got != owner {
		t.Fatalf("OwnerOf = %q, want %q. The delete path compares this against its own namespace/name; a "+
			"wrong or empty answer makes a non-owner uninstall the owner's CRD", got, owner)
	}

	// The non-owner's comparison, which is the decision the controller makes.
	const nonOwner = "default/keycloak-config-kog-realm"
	if got == nonOwner {
		t.Fatalf("a CRD owned by %q compared equal to %q; the two namespaces are the only difference and "+
			"they are what distinguishes a duplicate from the real thing", owner, nonOwner)
	}

	// THE UID IS UNCHANGED, because OwnerOf must be a read. A GET that mutated or recreated the object
	// would be invisible to an existence check.
	after := &apiextensionsv1.CustomResourceDefinition{}
	if err := kube.Get(context.Background(), client.ObjectKey{Name: gr.String()}, after); err != nil {
		t.Fatalf("re-reading the CRD: %v", err)
	}
	if after.UID != types.UID("uid-of-the-owners-crd") {
		t.Errorf("the CRD's UID changed from uid-of-the-owners-crd to %q across an OwnerOf call. A "+
			"recreated CRD cascade-deletes every CR of the kind, and an existence check cannot see it",
			after.UID)
	}
}

// An absent CRD is "" with NO error, so a RestDefinition whose CRD was already gone still completes its
// delete. An unowned one is also "": the annotation postdates some installs, and treating a missing
// annotation as "someone else owns it" would strand every CRD created before it existed.
func TestOwnerOfSeparatesAbsentFromUnowned(t *testing.T) {
	gr := schema.GroupResource{Group: "keycloak.ogen.krateo.io", Resource: "keycloakrealms"}

	if err := registerEventually(); err != nil {
		t.Fatalf("registering the apiextensions scheme: %v", err)
	}

	empty := fake.NewClientBuilder().WithScheme(clientsetscheme.Scheme).Build()
	got, err := OwnerOf(context.Background(), empty, gr)
	if err != nil {
		t.Fatalf("OwnerOf on an absent CRD returned an error: %v. The delete path would then skip the "+
			"uninstall forever for a CRD that is simply not there", err)
	}
	if got != "" {
		t.Errorf("OwnerOf on an absent CRD = %q, want \"\"", got)
	}

	unowned := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: gr.String(), UID: types.UID("u")},
	}
	kube := fake.NewClientBuilder().WithScheme(clientsetscheme.Scheme).WithObjects(unowned).Build()
	got, err = OwnerOf(context.Background(), kube, gr)
	if err != nil {
		t.Fatalf("OwnerOf on an unowned CRD: %v", err)
	}
	if got != "" {
		t.Errorf("OwnerOf on a CRD with no owner annotation = %q, want \"\". A CRD created before the "+
			"annotation existed would otherwise never be uninstallable by anyone", got)
	}
}
