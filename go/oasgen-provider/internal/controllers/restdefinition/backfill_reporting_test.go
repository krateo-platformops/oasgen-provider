package restdefinition

import (
	"context"
	"strings"
	"testing"

	"github.com/krateo-platformops/oasgen-provider/apis"
	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	select {
	case ev := <-rec.Events:
		assert.Contains(t, ev, "VersionLabelNotBackfilled", "the event reason is what an operator greps for")
		assert.Contains(t, ev, "PullRequest", "the event must name the kind whose instances are affected")
		assert.True(t, strings.Contains(ev, "never reconciled"),
			"the message must say what the consequence IS -- an operator who reads only 'could not backfill' "+
				"has no reason to treat it as urgent, and the affected instances cannot say so themselves")
	default:
		t.Fatal("no event was recorded: a failed backfill is reported nowhere an operator would look, " +
			"which is #425 -- and the instances it affects cannot carry a condition, because the watch " +
			"filters them out server-side before anything can annotate them")
	}
}

// assertErr is a tiny helper so the Forbidden carries a realistic message without importing errors just
// for one call.
type assertErr string

func (e assertErr) Error() string { return string(e) }
