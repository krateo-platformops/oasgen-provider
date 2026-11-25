//go:build unit || integration

package restdefinition

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/krateo-platformops/provider-runtime/pkg/meta"
)

// annotated builds the smallest object meta.ShouldDelete reads: the two policy annotations.
func orphanAnnotated(kv map[string]string) metav1.Object {
	return &metav1.ObjectMeta{Annotations: kv}
}

// TestOrphanDecidesTheCRDUninstall pins the DECISION the delete path now makes, at the level the
// decision is made: SkipCRD = notOwner || !meta.ShouldDelete(cr).
//
// WHY A TABLE OVER meta.ShouldDelete RATHER THAN A RECONCILE TEST. The gate is one boolean built from
// two annotations, and a reconcile test would exercise the apiserver fake, the CRD lookup, the owner
// read and Undeploy to assert that same boolean -- any of which failing would fail this for an
// unrelated reason. The wiring is asserted separately below, on the source, so both halves are
// covered without one hiding the other.
//
// FOUND BY A CONSUMER, and the annotation was ACCEPTED AND SILENTLY INERT: the delete gate consults
// meta.IsActionAllowed, which deliberately ignores deletion-policy, so `orphan` on a RestDefinition
// did nothing and nothing said so. The generated CRD was dropped -- though #125's live-instance guard
// means a kind with CRs was refused rather than destroyed.
func TestOrphanDecidesTheCRDUninstall(t *testing.T) {
	cases := []struct {
		name            string
		annotations     map[string]string
		wantSkipCRDOnly bool // true => the CRD must be KEPT
	}{
		{
			name:            "no annotations at all: the CRD is uninstalled, as before",
			annotations:     nil,
			wantSkipCRDOnly: false,
		},
		{
			name:            "deletion-policy: delete is the explicit form of the default",
			annotations:     map[string]string{meta.AnnotationKeyDeletionPolicy: meta.DeletionPolicyDelete},
			wantSkipCRDOnly: false,
		},
		{
			name:            "deletion-policy: orphan KEEPS the generated CRD -- the whole point",
			annotations:     map[string]string{meta.AnnotationKeyDeletionPolicy: meta.DeletionPolicyOrphan},
			wantSkipCRDOnly: true,
		},
		{
			// FLIPPED BY THE v1.21.1 BUMP, and the library changed rather than this test being wrong.
			// It previously said: "ShouldDelete returns true for observe-delete regardless of
			// deletion-policy; that is the library's semantics" -- asserted so a future reader would
			// not 'fix' it. provider-runtime then fixed it upstream. Its own doc at v1.21.1:
			//
			//	ShouldDelete reports whether the external resource is deleted with o: the
			//	policy allows delete, and the deletion policy keeps nothing.
			//	krateo.io/deletion-policy: orphan always wins (D5), and a deletion policy
			//	that is neither delete nor orphan keeps the resource too.
			//
			// At v1.8.2 ShouldDelete read both annotations and let observe-delete override orphan, so
			// an operator who asked for orphan got the resource deleted anyway. At v1.21.1 it is
			// ManagementDecision(o).May(Delete), where May folds in the deletion policy -- note that
			// Policy.Allows does NOT, which is why IsActionAllowed is deprecated for exactly this
			// discrepancy.
			//
			// SO THE CRD NOW SURVIVES where it was previously removed, which is what the annotation
			// asked for. This case is kept rather than deleted because it is the one that detected
			// the change, and it is still not OUR precedence -- it is the library's, now correct.
			name: "management-policy: orphan beats observe-delete, by the library's rule since v1.21.1",
			annotations: map[string]string{
				meta.AnnotationKeyDeletionPolicy:   meta.DeletionPolicyOrphan,
				meta.AnnotationKeyManagementPolicy: meta.ManagementPolicyObserveDelete,
			},
			wantSkipCRDOnly: true,
		},
		{
			name: "an unrecognised deletion-policy is NOT treated as delete",
			annotations: map[string]string{
				meta.AnnotationKeyDeletionPolicy: "orphean", // a typo an operator could plausibly make
			},
			wantSkipCRDOnly: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keep := !meta.ShouldDelete(orphanAnnotated(tc.annotations))
			assert.Equal(t, tc.wantSkipCRDOnly, keep,
				"meta.ShouldDelete decided the opposite of what this case needs; SkipCRD is "+
					"notOwner || !ShouldDelete, so this boolean IS whether the CRD survives")
		})
	}

	// SETUP CONTROL: the two verdicts must be reachable from this helper at all. Without it, a
	// ShouldDelete that always returned one value would pass every same-direction row above.
	t.Run("both verdicts are reachable", func(t *testing.T) {
		assert.True(t, meta.ShouldDelete(orphanAnnotated(nil)))
		assert.False(t, meta.ShouldDelete(orphanAnnotated(map[string]string{
			meta.AnnotationKeyDeletionPolicy: meta.DeletionPolicyOrphan,
		})))
	})
}

// TestTheOrphanGateIsWiredIntoSkipCRD asserts the wiring, because the table above would pass
// unchanged if the Delete path never consulted ShouldDelete at all.
//
// A DECISION NOBODY READS IS THE FAILURE MODE HERE: meta.ShouldDelete existed in the pinned
// provider-runtime the whole time and had ZERO call sites in this package, which is exactly how the
// annotation came to be accepted and inert.
func TestTheOrphanGateIsWiredIntoSkipCRD(t *testing.T) {
	src := readRestDefinitionSource(t)

	assert.Contains(t, src, "SkipCRD:        notOwner || !meta.ShouldDelete(cr),",
		"the CRD uninstall must be skipped when ShouldDelete says the resource is orphaned; without "+
			"this line the table above tests a decision the Delete path never makes")

	// AND IT MUST NOT GATE THE WHOLE DELETE. Orphan is about the generated CRD, not about leaving
	// this RestDefinition's own Deployment, RBAC and Configuration behind.
	assert.NotContains(t, src, "if !meta.ShouldDelete(cr) {\n\t\treturn nil",
		"ShouldDelete must not short-circuit Delete: the RestDefinition's own objects are still removed")
}

// readRestDefinitionSource reads the file under test with a PARSE CONTROL, because a wrong path
// would make every Contains assertion above fail for the wrong reason and every NotContains pass
// vacuously.
func readRestDefinitionSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("restdefinition.go")
	if err != nil {
		t.Fatalf("cannot read restdefinition.go; if it moved, this test must move with it: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "func (e *external) Delete(") {
		t.Fatalf("the file read does not contain Delete, so the assertions below prove nothing")
	}
	return src
}
