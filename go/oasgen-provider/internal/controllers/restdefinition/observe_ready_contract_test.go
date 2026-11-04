package restdefinition

import (
	"context"
	"strings"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	prv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
	"github.com/krateo-platformops/provider-runtime/pkg/reconciler/reconcilertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// THE READY CONTRACT (provider-runtime v1.8.0+): every successful Observe must set Ready.
//
// The runtime sets Ready=Unknown/NotObserved when Connect or Observe fails, and does NOT clear it on the
// next success -- so a path that answers without setting Ready leaves an old failure's marker in place and
// is reported as a provider defect (a ReadyNotSetAfterObserve Warning).
//
// SEVEN paths in this Observe set no Ready at all before the bump. Six were drift returns: they reported
// "not up to date" and left whatever Ready was there, which on a healthy resource is Available. So a
// RestDefinition went on reporting Ready=True for the entire window in which its CRD was being regenerated
// after an OAS edit -- and the documented consumer recipe is `kubectl wait --for condition=Ready=True`
// followed by applying a CR. That wait returned immediately, the apply met the OLD schema, the new field
// was pruned, and nothing said why. It is #314 wearing a different coat.
//
// Asserted with the library's own helper rather than a local reimplementation. AssertObserveSetsReady
// seeds Ready=NotObserved on the managed resource itself -- exactly the marker a real failure leaves --
// calls Observe, and fails if the reason is still NotObserved. It also fails on an unwrapped error return,
// which makes it a check that Observe OWNS Ready on this path, not merely that some condition exists.
func TestObserveSetsReadyOnEverySuccessPath(t *testing.T) {
	cases := []struct {
		name string
		// mutate reaches one specific success path.
		mutate func(*definitionv1alpha1.RestDefinition)
		// wantReason is the Ready reason that path must report.
		wantReason prv1.ConditionReason
		// wantMessageContains is the part of the drift the condition has to NAME. A bare
		// Ready=False/Creating satisfies the runtime's contract while telling an operator nothing about
		// which of six drifts is in progress, so the message is part of the deliverable, not decoration.
		wantMessageContains string
	}{
		{
			name: "spec.resource drift (#224): an append-only field was appended, so the CRD must regenerate",
			mutate: func(cr *definitionv1alpha1.RestDefinition) {
				cr.Spec.Resource.AdditionalStatusFields = append(cr.Spec.Resource.AdditionalStatusFields, "status.failureReason")
			},
			wantReason:          prv1.ReasonCreating,
			wantMessageContains: "spec.resource changed",
		},
		{
			name: "spec.resource hash never stored: the upgrade path from before 0.27.0 regenerates once",
			mutate: func(cr *definitionv1alpha1.RestDefinition) {
				cr.Status.ResourceHash = ""
			},
			wantReason:          prv1.ReasonCreating,
			wantMessageContains: "spec.resource changed",
		},
		{
			// Reached by the UNMUTATED fixture: the fake cluster has no Configuration instances, so the
			// collected auth-secret digest differs from the stored one. That makes this the cheapest
			// reachable drift path rather than a contrived one.
			name:                "auth secret reference drift: a Configuration's tokenRef moved",
			mutate:              func(cr *definitionv1alpha1.RestDefinition) {},
			wantReason:          prv1.ReasonCreating,
			wantMessageContains: "authentication secret references changed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := restDefinitionForHashTest()
			tc.mutate(cr)

			// Seed the marker BEFORE building the fake client, so the object in the fake store carries it
			// too. Without this the test could pass vacuously: if Observe ever re-read the RestDefinition
			// from the cluster into the struct under assertion, a fixture whose stored copy had no
			// condition -- or a healthy one -- would overwrite the helper's marker and the check would be
			// measuring the fixture instead of the code.
			cr.SetConditions(prv1.NotObserved())

			var msgs, lines []string
			e := externalForHashTest(t, cr, &msgs, &lines)

			obs, err := reconcilertest.AssertObserveSetsReady(t, context.Background(), e, cr)
			require.NoError(t, err)
			t.Logf("observe trace:\n  %s", strings.Join(lines, "\n  "))

			ready := cr.GetCondition(prv1.TypeReady)
			assert.Equal(t, tc.wantReason, ready.Reason,
				"the Ready reason must say which state this is; Creating means work is pending and Update is what clears it")
			assert.Equal(t, metav1.ConditionFalse, ready.Status,
				"a RestDefinition whose CRD does not yet match its own spec is not Ready: the kind being served is not the kind it describes")
			assert.Contains(t, ready.Message, tc.wantMessageContains,
				"the condition must name WHICH drift is in progress -- six paths share the Creating reason, and an operator reading only the reason cannot tell a CRD regeneration from a controller redeploy")

			assert.False(t, obs.ResourceUpToDate, "premise: this must be a drift path, or the Ready it sets is not the one under test")
			assert.True(t, obs.ResourceExists, "premise: drift, not absence")
		})
	}
}

// The deletion path is NOT covered by the success contract, and asserting it with
// AssertObserveSetsReady would be wrong: Observe returns an unwrapped error here (uninstalling the
// controller can fail), and the helper rejects that by design.
//
// It still needs Ready, for a different reason. markNotObserved exempts exactly one reason from being
// overwritten after a failed Observe -- Deleting -- and it decides by reading the condition ALREADY on the
// object. So Ready=Deleting has to be set at the TOP of the branch, before the finalizer and Delete error
// returns, or those errors answer "we could not tell" about an object whose fate is not in doubt.
//
// What this pins is OBSERVE's own answer, which is why it survives the runtime change underneath it.
// Read from each version's markNotObserved: on v1.8.0 and v1.8.1 the exemption keyed on the condition
// already present, so the write below was the only thing keeping a failed uninstall from reporting
// NotObserved; v1.8.2 keys on meta.WasDeleted as well and so answers Deleting on its own. The write is
// therefore no longer required at the pinned version, and this test is no longer a test of the exemption.
// It is a test that Observe reports a deletion as a deletion without the runtime having to infer it --
// which is what makes the provider's answer readable on its own, and what would still be true if the pin
// moved back.
func TestObserveSetsDeletingBeforeItCanFail(t *testing.T) {
	cr := restDefinitionForHashTest()
	now := metav1.Now()
	cr.DeletionTimestamp = &now
	cr.Finalizers = []string{"finalizer.restdefinition.krateo.io"}
	cr.SetConditions(prv1.NotObserved())

	var msgs, lines []string
	e := externalForHashTest(t, cr, &msgs, &lines)

	_, err := e.Observe(context.Background(), cr)
	t.Logf("observe trace:\n  %s", strings.Join(lines, "\n  "))

	// THE PREMISE, asserted rather than assumed: this path must actually have FAILED. If the uninstall
	// ever starts succeeding in this fixture, the test below still passes but stops testing anything --
	// it would be asserting that a success path sets Deleting, which the success contract already covers.
	require.Error(t, err,
		"premise: this fixture must reach an error return on the deletion path, otherwise the exemption "+
			"this test exists to exercise is never consulted")

	ready := cr.GetCondition(prv1.TypeReady)
	assert.Equal(t, prv1.ReasonDeleting, ready.Reason,
		"Ready must already be Deleting when the error is returned: that reason is what stops the runtime "+
			"replacing it with NotObserved, and a resource on its way out stays on its way out whatever "+
			"the external system answers")
	assert.NotEqual(t, prv1.ReasonNotObserved, ready.Reason,
		"NotObserved here would claim we could not assess a resource that is simply being deleted")
}
