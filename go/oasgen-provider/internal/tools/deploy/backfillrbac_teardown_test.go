package deploy

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestEveryBackfillRBACFunctionHasACallSite is the test that would have caught issue 742 the day #721
// shipped, and it is the reason this file exists rather than only the behavioural test below.
//
// DeleteBackfillRBAC was written, documented with a doc comment describing its IsNotFound tolerance, and
// NEVER CALLED. Nothing failed: the provider built, every test passed, the feature worked, and every
// RestDefinition ever deleted leaked its labelled ClusterRole into the aggregated role permanently.
//
// A unit test of the function itself would NOT have caught it -- the function is correct. Mutation testing
// would not either: mutating dead code kills nothing. The only signal is "is it reachable from the
// controller that owns the lifecycle", which is a question about CALL SITES, not about behaviour.
//
// Same shape as rest-dynamic-controller's TestNoBodySendingCallSiteBypassesTheHelper, which caught a real
// omission the same day it was written.
func TestEveryBackfillRBACFunctionHasACallSite(t *testing.T) {
	// The lifecycle owner. Both halves of the grant must be reachable from here.
	const owner = "../../controllers/restdefinition"

	want := map[string]string{
		"SyncBackfillRBAC":   "provisions the grant on the pass that uses it",
		"DeleteBackfillRBAC": "removes it when the RestDefinition goes away (issue 742)",
	}

	src := map[string]bool{}
	entries, err := os.ReadDir(owner)
	if err != nil {
		t.Fatalf("reading %s: %v", owner, err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(owner, e.Name()))
		if rerr != nil {
			t.Fatalf("reading %s: %v", e.Name(), rerr)
		}
		if _, perr := parser.ParseFile(fset, e.Name(), b, parser.SkipObjectResolution); perr != nil {
			t.Fatalf("parsing %s: %v", e.Name(), perr)
		}
		for fn := range want {
			if strings.Contains(string(b), "deploy."+fn+"(") {
				src[fn] = true
			}
		}
	}

	for fn, why := range want {
		if !src[fn] {
			t.Errorf("deploy.%s has NO call site in %s.\n"+
				"  It %s.\n"+
				"  A provisioning function with no teardown call site leaks its object for every resource "+
				"ever deleted, and nothing fails: the build succeeds, the tests pass, and the grant "+
				"outlives the thing it was scoped to.", fn, owner, why)
		}
	}

	// NEGATIVE CONTROL: a name that cannot be present, so a grep that matches everything is visible.
	if strings.Contains("x", "deploy.NoSuchBackfillFunction(") {
		t.Fatal("control: impossible")
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(owner, e.Name()))
		if strings.Contains(string(b), "deploy.NoSuchBackfillFunction(") {
			t.Fatal("NEGATIVE CONTROL FAILED: matched a function that does not exist, so a pass above " +
				"proves nothing about the real ones")
		}
	}
}

// TestDeleteBackfillRBAC_RemovesTheRoleAndToleratesAbsence is the behavioural half, which did not exist
// either: the function shipped untested as well as uncalled.
func TestDeleteBackfillRBAC_RemovesTheRoleAndToleratesAbsence(t *testing.T) {
	sch := runtime.NewScheme()
	if err := rbacv1.AddToScheme(sch); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	gvr := schema.GroupVersionResource{Group: "github.krateo.io", Resource: "repositories"}
	name := BackfillClusterRoleName(gvr)

	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{BackfillAggregationLabel: "true"}},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{gvr.Group}, Resources: []string{gvr.Resource}, Verbs: backfillVerbs}},
	}
	// A second role that must SURVIVE, so a teardown deleting everything labelled is caught.
	other := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   BackfillClusterRoleName(schema.GroupVersionResource{Group: "zendesk.ogen.krateo.io", Resource: "zendeskrequests"}),
			Labels: map[string]string{BackfillAggregationLabel: "true"},
		},
	}
	kube := fake.NewClientBuilder().WithScheme(sch).WithObjects(role, other).Build()

	if err := DeleteBackfillRBAC(context.Background(), kube, gvr.Group, gvr.Resource); err != nil {
		t.Fatalf("DeleteBackfillRBAC: %v", err)
	}
	var got rbacv1.ClusterRole
	if err := kube.Get(context.Background(), types.NamespacedName{Name: name}, &got); !errors.IsNotFound(err) {
		t.Errorf("the role is still present (err=%v): it stays aggregated and keeps granting patch on a "+
			"resource whose CRD has been dropped", err)
	}
	if err := kube.Get(context.Background(), types.NamespacedName{Name: other.Name}, &rbacv1.ClusterRole{}); err != nil {
		t.Errorf("an UNRELATED backfill role was removed (%v): teardown must delete one grant, not every "+
			"role carrying the aggregation label", err)
	}

	// IsNotFound tolerance -- the property the doc comment claimed and nothing had ever exercised.
	if err := DeleteBackfillRBAC(context.Background(), kube, gvr.Group, gvr.Resource); err != nil {
		t.Errorf("second delete returned %v, want nil: a RestDefinition whose grant was never provisioned, "+
			"or already removed, must not fail the teardown", err)
	}
	var _ client.Client = kube
}
