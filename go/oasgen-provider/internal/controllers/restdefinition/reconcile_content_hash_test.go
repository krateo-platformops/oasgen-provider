//go:build unit

package restdefinition

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WHAT THESE PIN (#627)
//
// `status.oasHash` and `status.resourceHash` are what Observe compares against the live document and the
// live spec to decide whether the generated CRD is current. Create's CRD-already-exists path compared
// only ONE of the three drift inputs -- the generator build -- and then advanced all three anyway.
//
// So an OAS content change that first arrived via Create had its hash advanced with nothing rendered,
// and THAT LATCHES PERMANENTLY: every later Observe compares the new document against a stored hash of
// the new document, finds no drift, and the CRD keeps its old schema forever while the RestDefinition
// reports Ready. Measured by the installer session on a clean GKE cluster -- CRD byte-identical,
// `status.oasHash` holding the digest of the NEW document, unchanged ten minutes later.
//
// THE ASSERTION THAT MATTERS IS THAT THE HASH DID **NOT** MOVE. A test that only checks "the CRD was
// regenerated" catches a missed detection, which self-heals on the next pass, and is blind to the latch,
// which does not. The two failures look identical for one reconcile and then diverge forever.
//
// Create is reached here the way it is reached in production: Observe returns early while the generated
// controller's Deployment is absent (`!deployOk`, restdefinition.go), so it never gets as far as its own
// drift checks. That is the precondition installer's cluster supplied with an oasgen version move, and
// the one keycloak-provider-kog's clean upgrade did not -- which is why their run regenerated correctly
// and is the control for the whole issue.

// editTheOASDocument changes the document the RestDefinition points at, through the CLUSTER, exactly as a
// chart upgrade that bumps only its OAS asset does. A comment line is enough: it moves the composite
// digest without changing the generated schema, so the marker below stays a clean signal for "did a
// render happen" rather than being confounded by a schema that would differ anyway.
func (l *secretRefLoop) editTheOASDocument() {
	l.t.Helper()
	ctx := context.Background()
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: "demo-system", Name: "pr"}
	require.NoError(l.t, l.e.kube.Get(ctx, key, cm),
		"the fixture's OAS ConfigMap must exist, or this arm edits nothing")

	before := cm.Data["openapi.yaml"]
	require.NotEmpty(l.t, before, "the fixture's OAS key must carry a document")
	cm.Data["openapi.yaml"] = before + "\n# edited by the test to move the composite digest\n"
	require.NoError(l.t, l.e.kube.Update(ctx, cm))

	// CONTROL: the edit really landed in the cluster the controller reads, not just in our copy.
	reread := &corev1.ConfigMap{}
	require.NoError(l.t, l.e.kube.Get(ctx, key, reread))
	require.NotEqual(l.t, before, reread.Data["openapi.yaml"],
		"the OAS edit did not persist, so the arm would be measuring an unchanged document")
}

// THE LATCH ARM. Create must leave the hashes alone when it renders nothing.
func TestCreateDoesNotAdvanceTheContentHashesItDidNotRenderFrom(t *testing.T) {
	l := settleAt(t, "0.56.0-aaaaaaa", true)

	beforeOAS := l.persisted().OASHash
	beforeRes := l.persisted().ResourceHash
	require.NotEmpty(t, beforeOAS, "the fixture must have stamped an OAS hash before the document is edited")
	require.NotEmpty(t, beforeRes, "the fixture must have stamped a resource hash too")

	marker := l.markTheGeneratedCRD()
	l.editTheOASDocument()
	// The generator does NOT move, so Create's only render branch is not taken.
	l.removeTheControllerDeployment()

	drift, observed := l.reconcile()
	require.True(t, drift)
	require.True(t, containsSubstring(observed, "Dynamic Controller not deployed yet"),
		"this arm must take the CREATE path, or it says nothing about Create's stamps: %v", observed)

	// Nothing rendered -- the tamper survives.
	require.True(t, l.generatedCRDHasSpecProperty(marker),
		"the generator did not move, so Create must not re-render; marker %q was removed", marker)

	// AND THE HASH MUST NOT HAVE MOVED. This is the whole issue.
	require.Equal(t, beforeOAS, l.persisted().OASHash,
		"Create advanced status.oasHash without rendering from the new document. The next Observe then "+
			"compares the new document against a stored hash OF the new document, finds no drift, and the "+
			"CRD keeps its old schema permanently -- no later reconcile revisits it (#627)")
	require.Equal(t, beforeRes, l.persisted().ResourceHash,
		"Create advanced status.resourceHash without rendering; same latch, second input")
}

// THE CONTROL, and without it the arm above passes against a provider that never advances the hash at
// all -- which would be a different bug with the same green test.
//
// Same document edit, but the controller Deployment is left in place, so Observe reaches its drift checks
// and routes to Update, which renders. The hash must move AND the tamper must be gone.
func TestUpdateAdvancesTheContentHashWhenItDoesRender(t *testing.T) {
	l := settleAt(t, "0.56.0-aaaaaaa", true)

	beforeOAS := l.persisted().OASHash
	require.NotEmpty(t, beforeOAS)

	marker := l.markTheGeneratedCRD()
	l.editTheOASDocument()
	// NOTE: no removeTheControllerDeployment() here. That single line is the difference between this
	// control and the arm above, which is what makes the pair a measurement rather than two assertions.

	drift, observed := l.reconcile()
	require.True(t, drift, "an OAS content change must be reported as drift: %v", observed)

	require.False(t, l.generatedCRDHasSpecProperty(marker),
		"the document changed and the Deployment was present, so Update must have re-rendered; the "+
			"tamper %q survived, which means nothing regenerated", marker)
	require.NotEqual(t, beforeOAS, l.persisted().OASHash,
		"the CRD was re-rendered from the new document, so status.oasHash must now be its digest")
}

// TestTheContentHashStampsAreWrittenInExactlyOnePlace is the structural half, and it is here for the same
// reason TestTheGeneratorStampIsWrittenInExactlyOnePlace is: no fixture can catch a FOURTH call site
// added later that stamps without rendering, which is exactly how both #541 and #627 were introduced.
//
// #541 established "stamp only where you rendered" and applied it to one input of three. The other two
// kept their unconditional writes, under a heading stating the general rule. This asserts the rule for
// all three.
func TestTheContentHashStampsAreWrittenInExactlyOnePlace(t *testing.T) {
	src := readControllerSource(t)

	const oasStamp = "cr.Status.OASHash = oasHash"
	const resStamp = "cr.Status.ResourceHash = resourceHash(cr)"

	require.Equal(t, 1, countOccurrences(src, oasStamp),
		"status.oasHash must be assigned in exactly one place -- renderGeneratedCRDs, which renders "+
			"first. A second assignment is #627.")
	require.Equal(t, 1, countOccurrences(src, resStamp),
		"status.resourceHash must be assigned in exactly one place, for the same reason")
	require.Equal(t, 0, countOccurrences(src, "cr.Status.ResourceHash = resHash"),
		"the old Update-side resourceHash assignment is gone; renderGeneratedCRDs writes it")

	// Both assignments must sit inside the helper, AFTER the render.
	helper := helperBody(t, src, "func (e *external) renderGeneratedCRDs(")
	require.Contains(t, helper, "e.generateAndApplyCRDs(",
		"renderGeneratedCRDs must be the function that renders")
	for _, stamp := range []string{oasStamp, resStamp} {
		require.Contains(t, helper, stamp,
			"the single assignment must live inside renderGeneratedCRDs, after the render: %s", stamp)
		require.Less(t, indexOf(helper, "e.generateAndApplyCRDs("), indexOf(helper, stamp),
			"the render must come BEFORE the stamp, or the stamp can be written for a render that failed: %s", stamp)
	}

	// CONTROL: the reader can see a string that is definitely there, so a zero above means absence rather
	// than a file that was not read.
	require.Equal(t, 1, countOccurrences(src, "func (e *external) renderGeneratedCRDs("),
		"control: the source really was read")
}
