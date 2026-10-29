package restdefinition

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// TestObserveAnswersOnPermanentRenderFailure is the regression test for #266.
//
// Observe returned an error for failures that are permanent and LOCAL -- a template that will never
// render -- and provider-runtime resolves an unresolved create handshake only after Observe ANSWERS:
//
//	observation, err := external.Observe(externalCtx, managed)
//	if err != nil { return reconcile.Result{}, werr }   // returns HERE
//	if unresolvedCreate { ... }                         // never reached
//
// So a half-created RestDefinition whose template cannot render was wedged on EVERY runtime version, and
// no bump could fix it: the guard was never given the chance to run.
//
// The template is swapped AFTER the harness builds, because the harness needs a valid one to construct
// the Deployment fixture with the same constructor Observe uses.
func TestObserveAnswersOnPermanentRenderFailure(t *testing.T) {
	cr := restDefinitionForHashTest()
	var msgs, lines []string
	e := externalForHashTest(t, cr, &msgs, &lines)

	// A template that parses as a template and cannot produce a Deployment. Not a missing file: a missing
	// file is also permanent, but this version also proves the branch is reached for a RENDER failure
	// rather than only for a stat failure.
	broken := filepath.Join(t.TempDir(), "broken.yaml")
	require.NoError(t, os.WriteFile(broken, []byte("this: [is not: a deployment\n"), 0o600))
	prev := RDCtemplateDeploymentPath
	RDCtemplateDeploymentPath = broken
	t.Cleanup(func() { RDCtemplateDeploymentPath = prev })

	obs, err := e.Observe(context.Background(), cr)

	// THE discriminating assertion: an answer, not an error.
	require.NoError(t, err,
		"a template that will never render is a PERMANENT failure: erroring here skips provider-runtime's "+
			"create-recovery guard, which only runs once Observe answers, wedging the object forever")
	assert.False(t, obs.ResourceExists)
	assert.True(t, obs.ResourceUpToDate,
		"settled, not drifting -- reporting drift would make the runtime call Update, which renders the "+
			"same template and fails identically")

	// The condition is half the deliverable. Without it the object shows only that it is not ready, which
	// is indistinguishable from the expected two-phase intermediate state.
	conds := cr.Status.ConditionedStatus.Conditions
	require.NotEmpty(t, conds, "the object must carry its own diagnosis")
	var msg string
	for _, c := range conds {
		if c.Message != "" {
			msg = c.Message
		}
	}
	assert.Contains(t, msg, "cannot render",
		"the condition must name the failure, not merely report unavailability")
	assert.Contains(t, msg, "retrying will not help",
		"and must say it is permanent, so an operator does not wait for it to clear")
}

// TestObserveStillErrorsOnTransientClusterFailure is the CONTROL ARM, and the one that matters.
//
// #266's fix could drift into "answer whenever Observe hits any error", which would report a resource
// ABSENT during an apiserver blip. The runtime would then call Create against a resource that already
// exists. For this provider Create applies manifests rather than allocating, so that is recoverable --
// but the same mistake in a provider whose Create allocates would duplicate real infrastructure, and the
// distinction the fix draws is PERMANENT vs TRANSIENT, not "an error happened".
//
// A failed cluster read must still error.
func TestObserveStillErrorsOnTransientClusterFailure(t *testing.T) {
	cr := restDefinitionForHashTest()
	var msgs, lines []string
	e := externalForHashTest(t, cr, &msgs, &lines)

	// Wrap the harness's client so reads of the generated Deployment fail the way a briefly unavailable
	// apiserver fails. The template stays VALID, so this cannot be reaching the permanent branch.
	boom := errors.New("etcdserver: request timed out")
	// The harness's client is a fake, which implements WithWatch; interceptor.NewClient requires that
	// rather than the plain client.Client the external field is typed as.
	base, ok := e.kube.(client.WithWatch)
	require.True(t, ok, "harness client must be a fake (WithWatch) for interception")
	e.kube = interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			// Match on the TYPE, not the name. Keying on a "-controller" suffix silently matched nothing
			// (the template supplies its own metadata.name), so no error was injected and the control arm
			// passed by failing to test anything.
			if _, isDeployment := obj.(*appsv1.Deployment); isDeployment {
				return boom
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	_, err := e.Observe(context.Background(), cr)

	require.Error(t, err,
		"a cluster read that failed is a TRANSIENT cannot-tell: it must retry, not report the resource "+
			"absent -- answering here would call Create on a resource that may well exist")
	assert.Contains(t, err.Error(), "timed out",
		"and the cause must survive, so the object's message names the real failure")
}
