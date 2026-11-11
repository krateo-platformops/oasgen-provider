//go:build unit

package restdefinition

import (
	"context"
	"testing"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
)

// theSeededController returns the one controller Deployment the shared fixture seeds.
//
// It is found by LISTING rather than by name on purpose. The fixture renders it through
// objects.CreateK8sObject from the same template Observe uses, and that template carries literal
// placeholder values for metadata.name and metadata.namespace -- so the live object is
// "placeholder/placeholder", not "<cr>-controller". Observe resolves it the same way, so the pair is
// self-consistent and a hand-written key would simply miss, exercising the not-deployed-yet branch and
// proving nothing. The existing fixture comment warns about exactly this.
func theSeededController(t *testing.T, e *external) *appsv1.Deployment {
	t.Helper()
	var l appsv1.DeploymentList
	require.NoError(t, e.kube.List(context.Background(), &l))
	require.Len(t, l.Items, 1,
		"the fixture must seed exactly one controller Deployment; found %d. If it now seeds more, this "+
			"helper has to pick the right one rather than the only one", len(l.Items))
	return &l.Items[0]
}

// THE CALL SITE, not the function. Found by the call-site wiring audit that followed issue 599.
//
// resourceExistsForController's own truth table is covered in observation_test.go over four rows,
// including the load-bearing "present but not ready -> EXISTS". That test passes `tc.deployOk,
// tc.deployRdy` straight into the function, so it says nothing about what Observe passes.
//
// Mutation testing against the package as it stood showed two call-site mutations that disable the rule
// entirely, and BOTH SURVIVED:
//
//	resourceExistsForController(deployReady, deployOk)    swap        SURVIVED
//	resourceExistsForController(deployReady, deployReady) wrong var   SURVIVED
//
// Only a third mutation -- hardcoding `true` -- was caught, which proves the package's tests reach this
// path and simply cannot tell the two booleans apart. Both arguments are `bool`, so a swap compiles
// silently; see #122 for what it costs.
//
// #122, live: reporting a controller Deployment that exists but is still rolling out as NON-EXISTENT told
// provider-runtime the external resource was gone. It re-entered the create handshake on an
// already-created resource, re-set krateo.io/external-create-pending 75 seconds AFTER create had
// succeeded, and wedged on errCreateIncomplete once the creation grace period lapsed. The RestDefinition
// then sat Ready=False forever with a CRD that was in fact served, taking its owning composition down.
//
// So this test drives the real Observe with a Deployment that EXISTS and is NOT READY -- the one input
// combination that distinguishes the two arguments -- and asserts on what Observe reports.
func TestObserveReportsAnUnreadyControllerAsExisting(t *testing.T) {
	ctx := context.Background()
	var msgs, lines []string
	cr := restDefinitionForHashTest()
	e := externalForHashTest(t, cr, &msgs, &lines)

	// The shared fixture seeds a READY Deployment. Make it exist-but-not-ready here rather than
	// parameterising the fixture, which three other test files depend on.
	// Make it EXIST but not be READY by widening the desired replica count, not by writing status: the
	// fake client treats status as a subresource, so a plain Update to .Status is silently dropped and
	// the premise below would read as ready. Spec is unambiguously writable, and a Deployment asked for
	// two replicas with one ready IS exactly a controller mid-rollout.
	//
	// LookupDeployment's rule: ready = Spec.Replicas != nil && *Spec.Replicas == Status.ReadyReplicas.
	dep := theSeededController(t, e)
	dep.Spec.Replicas = ptr(int32(2))
	require.NoError(t, e.kube.Update(ctx, dep))

	// PREMISE, asserted through the very helper Observe uses. Without this the test could pass because
	// the Deployment went missing, or because it stayed ready -- either of which would exercise a
	// different branch and prove nothing about the two arguments.
	probe := &appsv1.Deployment{ObjectMeta: dep.ObjectMeta}
	exists, ready, lerr := deployment.LookupDeployment(ctx, e.kube, probe)
	require.NoError(t, lerr)
	require.True(t, exists, "premise: the controller Deployment must EXIST for this test to be about readiness")
	require.False(t, ready, "premise: it must NOT be ready, or the two arguments are indistinguishable")

	obs, err := e.Observe(ctx, cr)
	require.NoError(t, err)

	assert.True(t, obs.ResourceExists,
		"Observe reported a controller Deployment that EXISTS but is not yet ready as non-existent. That "+
			"tells provider-runtime the external resource is gone, so it re-enters the create handshake on "+
			"an already-created resource, re-sets krateo.io/external-create-pending, and wedges on "+
			"errCreateIncomplete (#122). Check the argument ORDER at the resourceExistsForController call "+
			"sites: both parameters are bool, so a swap compiles silently.")
}

// The complementary row, so the pair can distinguish "existence follows deployOk" from "existence is
// always true". Hardcoding true was the one mutation the package already killed; keeping the arm here
// means the two live in one place with the same reasoning.
func TestObserveReportsAMissingControllerAsNotExisting(t *testing.T) {
	ctx := context.Background()
	var msgs, lines []string
	cr := restDefinitionForHashTest()
	e := externalForHashTest(t, cr, &msgs, &lines)

	dep := theSeededController(t, e)
	require.NoError(t, e.kube.Delete(ctx, dep))

	exists, _, lerr := deployment.LookupDeployment(ctx, e.kube, &appsv1.Deployment{ObjectMeta: dep.ObjectMeta})
	require.NoError(t, lerr)
	require.False(t, exists, "premise: the Deployment must be gone")

	obs, err := e.Observe(ctx, cr)
	require.NoError(t, err)

	assert.False(t, obs.ResourceExists,
		"a MISSING controller Deployment was reported as existing, so Create never runs and the controller "+
			"is never deployed at all. The asymmetry with the not-ready case above is deliberate (#122): "+
			"absent means absent, not-ready means present.")
}
