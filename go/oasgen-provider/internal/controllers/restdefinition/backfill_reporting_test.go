package restdefinition

import (
	"context"
	"strings"
	"testing"

	"github.com/krateo-platformops/oasgen-provider/apis"
	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// #425: A FAILED BACKFILL WAS REPORTED NOWHERE ON A DEFAULT INSTALL.
//
// All three call sites logged the error at Debug and did nothing else, and `debug` defaults to false
// (main.go:44), so the line was not emitted at all unless someone had already decided to look.
//
// That silence mattered more than a missing log line usually does, because the failure it hid is
// unobservable by any other route: version-scoped watching filters SERVER-SIDE on both ListFunc and
// WatchFunc, so an unlabelled instance never enters the informer cache and cannot carry a condition or an
// event. On a default install this was the ONLY place the failure could ever have been reported.
//
// ASSERTED ON THE EVENT, NOT THE LOG, AND THAT IS THE POINT OF THE TEST.
//
// This package's recordingLogger funnels Debug, Info, Warn and Error into one slice, so a log assertion
// would pass against the Debug-only version this fixes -- the logger does not carry the distinction under
// test. The EVENT does: there was no event before, so its presence is the only channel that can tell the
// two versions apart. It is also what an operator sees without changing a flag first.
//
// THE FAILURE IS INJECTED AS A FORBIDDEN ON LIST, which is the most plausible real cause rather than a
// convenient one: this repo has shipped a chart RBAC gap on generated kinds before. An interceptor is used
// instead of an unregistered scheme because the fake client tolerates unstructured lists of kinds it does
// not know and returns an empty list -- so that route produces no failure at all, which an earlier version
// of this test discovered via its own premise assertion.
func TestAFailedBackfillIsReportedAsAnEvent(t *testing.T) {
	cr := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "pullrequest", Namespace: "demo-system"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			ResourceGroup: "github.krateo.io",
			Resource:      definitionv1alpha1.Resource{Kind: "PullRequest", Identifiers: []string{"number"}},
		},
	}

	s := runtime.NewScheme()
	require.NoError(t, apis.AddToScheme(s))
	// PRODUCTION PARITY, not a convenience. backfillVersionLabelsReporting provisions the ClusterRole that
	// authorises its own patch (#717), and the manager's scheme in production carries the built-in types --
	// which is why installRBACResources has been applying ClusterRoles and Roles here for releases. A fake
	// client without rbacv1 fails that Apply with "no kind is registered for the type v1.ClusterRole",
	// i.e. it tests a system this provider is not.
	require.NoError(t, rbacv1.AddToScheme(s))

	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Group: "github.krateo.io", Resource: "pullrequests"}, "",
		assertErr("pullrequests.github.krateo.io is forbidden: User \"system:serviceaccount:krateo-system:oasgen-provider\" cannot list resource"))

	var msgs []string
	rec := record.NewFakeRecorder(10)
	e := &external{
		kube: fake.NewClientBuilder().WithScheme(s).WithObjects(cr.DeepCopy()).
			WithInterceptorFuncs(interceptor.Funcs{
				List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
					return forbidden
				},
			}).Build(),
		log: recordingLogger{msgs: &msgs},
		rec: rec,
	}

	gvk := schema.GroupVersionKind{Group: "github.krateo.io", Version: "v1-0-0", Kind: "PullRequest"}
	e.backfillVersionLabelsReporting(context.Background(), cr, gvk, false)

	// THE PREMISE, asserted rather than assumed. An earlier version of this test used an unregistered
	// scheme to induce the failure; the fake client returned an empty list instead of an error, so the
	// backfill SUCCEEDED and the test was about to assert that a success path emits no event -- true, and
	// proof of nothing. This is why the premise is checked before the behaviour.
	require.NotEmpty(t, msgs,
		"premise: the backfill must actually have failed and logged something; with no failure there is "+
			"nothing for the event assertion below to be about")

	// FOUND BY REASON, NOT BY ARRIVAL ORDER. This used to read whichever event came first, which made it a
	// test of ordering as much as of reporting: #717 added a second, earlier event on the same code path
	// (BackfillGrantNotProvisioned, when the grant cannot be applied) and the first read stopped being this
	// one. Scanning loses nothing -- every assertion below is unchanged and still has to hold on a real
	// event -- and it stops the next event added anywhere upstream from failing this test for no reason.
	var found string
	var seen []string
	for {
		select {
		case ev := <-rec.Events:
			seen = append(seen, ev)
			if strings.Contains(ev, "VersionLabelNotBackfilled") {
				found = ev
			}
			continue
		default:
		}
		break
	}

	require.NotEmpty(t, found,
		"no VersionLabelNotBackfilled event was recorded: a failed backfill is reported nowhere an operator "+
			"would look, which is #425 -- and the instances it affects cannot carry a condition, because the "+
			"watch filters them out server-side before anything can annotate them.\nEvents seen: %v", seen)

	assert.Contains(t, found, "PullRequest", "the event must name the kind whose instances are affected")
	assert.True(t, strings.Contains(found, "never reconciled"),
		"the message must say what the consequence IS -- an operator who reads only 'could not backfill' "+
			"has no reason to treat it as urgent, and the affected instances cannot say so themselves")
}

// assertErr is a tiny helper so the Forbidden carries a realistic message without importing errors just
// for one call.
type assertErr string

func (e assertErr) Error() string { return string(e) }
