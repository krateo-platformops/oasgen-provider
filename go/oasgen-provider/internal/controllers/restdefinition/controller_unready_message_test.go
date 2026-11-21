//go:build unit

package restdefinition

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/deploy"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/objects"
)

// TestTheUnreadyControllerMessageNamesTheCountAndTheCause is the whole of issue 815's hole 2.
//
// The symptom filed -- a RestDefinition reporting Ready while its generated controller reconciles
// nothing -- is PREVENTED rather than reported: Observe gates Available on the rdc Deployment's
// readiness and returns, so Available is unreachable while the controller is unready.
//
// What made that gate meaningful is #843. Before it the chart set no probe, so ReadyReplicas counted
// containers that had STARTED -- a controller unable to read its Lease was Ready and the gate passed.
// Since 0.85.0 the chart defaults a readinessProbe at /readyz, which fails when the Lease cannot be
// read or written, so ReadyReplicas now counts pods that pass it.
//
// THE REMAINING GAP WAS THE WORDING. "not ready yet" reads as "still starting" for a controller that
// will NEVER be ready, and an operator waits instead of looking at RBAC. This asserts the message
// carries the count and the cause.
func TestTheUnreadyControllerMessageNamesTheCountAndTheCause(t *testing.T) {
	l := newSecretRefLoop(t)

	// One reconcile: Observe answers "does not exist" for the missing Deployment, Create deploys it.
	// The fake client sets no status, so ReadyReplicas stays 0 -- which is exactly the state under test.
	// rollOut() is DELIBERATELY NOT CALLED; it is what the other tests use to get past this gate.
	// One reconcile: Observe answers "does not exist" for the missing Deployment, Create deploys it.
	_, _ = l.reconcile()

	// THE STATE IS SET EXPLICITLY, as the INVERSE of the harness's rollOut(). Relying on the fake
	// client's defaults is what made the first version of this test pass over the gate: the assertion
	// then judged the drift branch by a message meant for this one, and the setup control is what
	// caught it. Located with the SAME constructor Observe uses, so this is the object Observe reads.
	dep := &appsv1.Deployment{}
	require.NoError(t, objects.CreateK8sObject(dep, targetGVRForTest(l.cr), types.NamespacedName{
		Namespace: l.cr.Namespace, Name: l.cr.Name + deploy.ControllerResourceSuffix}, RDCtemplateDeploymentPath))
	require.NoError(t, l.e.kube.Get(context.Background(), client.ObjectKeyFromObject(dep), dep),
		"Create must have deployed the controller")
	dep.Status = appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 0, AvailableReplicas: 0, UpdatedReplicas: 1}
	require.NoError(t, l.e.kube.Status().Update(context.Background(), dep))

	// SETUP CONTROL: the state really is 1 desired / 0 ready, so the gate below must fire. A fixture
	// with 0 desired would read 0 == 0 and pass the gate, which is how the first attempt went wrong.
	require.NotNil(t, dep.Spec.Replicas, "the fixture has no desired replica count")
	require.Equal(t, int32(1), *dep.Spec.Replicas)
	require.Equal(t, int32(0), dep.Status.ReadyReplicas)

	// OBSERVE IS CALLED DIRECTLY, not through l.reconcile(). The harness's reconcile() asserts that
	// Observe reached the END of its drift checks ("Managing finalizers"), because every other test
	// using it needs that -- so it REFUSES to let Observe stop at the readiness gate, which is the
	// only state this test is about. A harness built for one purpose asserting against another is a
	// failure that reads like a defect in the subject.
	fresh := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, l.e.kube.Get(context.Background(), client.ObjectKeyFromObject(l.cr), fresh))
	l.cr = fresh
	obs, err := l.e.Observe(context.Background(), l.cr)
	require.NoError(t, err)

	// The gate answers "exists and up to date" so the runtime waits rather than attempting an Update
	// on a controller that is still starting. Asserting it here pins that this is the branch reached.
	require.True(t, obs.ResourceUpToDate, "the readiness gate must answer up to date, not request work")

	conds := l.cr.Status.Conditions
	require.NotEmpty(t, conds, "no conditions were set, so this test asserts nothing about the message")

	var msg string
	for _, c := range conds {
		if string(c.Type) == "Ready" {
			msg = c.Message
		}
	}
	// SETUP CONTROL: we are at the unready gate and not somewhere else. Without this, a run that
	// stopped at "CRD not found" would be judged by assertions meant for this branch.
	require.Contains(t, msg, "Dynamic Controller",
		"Observe did not stop at the controller-readiness gate; the message was %q", msg)

	require.Contains(t, msg, "replicas pass the readiness check",
		"the message does not name the replica count. 0/1 tells an operator whether anything is "+
			"passing the probe at all, which is the difference between 'starting' and 'stuck'")
	require.Contains(t, msg, "ownership Lease",
		"the message does not name the cause. Since 0.85.0 this gate IS the signal for a controller "+
			"that cannot reach its Lease, and 'not ready yet' sends an operator away to wait")
	require.Contains(t, strings.ToLower(msg), "rbac",
		"the message does not say where to look. The measured cause is the ServiceAccount's Lease RBAC")

	// AND IT MUST NOT CLAIM CONTENTION, which is the wrong diagnosis and the one a reader reaches for.
	// Measured in unstructured-runtime readyz.go:43: a replica WAITING for a peer's Lease is READY, so
	// a persistent 0-of-N is never a handover.
	require.Contains(t, msg, "not contention",
		"the message leaves contention open as an explanation. A waiting replica is READY, so "+
			"contention cannot produce this state and an operator should not spend time on it")

	// The old wording must be gone: it is the defect, not a prefix of the fix.
	require.NotEqual(t, "Dynamic Controller '"+l.cr.Name+"-controller' not ready yet", msg,
		"the message is still the bare 'not ready yet' form")
}
