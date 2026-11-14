//go:build unit || integration

package restResources

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/krateo-platformops/unstructured-runtime/pkg/controller"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// THE WHOLE POINT IS `Requested: true`, because that is what CLOSES the marker (issue 676).
//
// The runtime sets krateo.io/external-create-pending before calling this handler and closes it on the
// success path. An error return leaves early, so with pending set and neither succeeded nor failed
// present, meta.ExternalCreateIncomplete returns true, the object enters the incomplete-create state,
// and its recovery re-enters Create. Measured on a real Keycloak: six creates answering 409 over three
// minutes.
//
// So this asserts the FIELD, not merely "no error": `CreateResult{}` also returns no error in some
// shapes, and `Requested: false` is exactly what fails to close the marker.
func TestACreateThatReachedTheVendorIsReportedAsRequested(t *testing.T) {
	h, _ := handlerForCreateGuard()
	mg := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "example.krateo.io/v1alpha1",
		"kind":       "Thing",
		"metadata":   map[string]interface{}{"name": "t", "namespace": "n"},
	}}
	werr := errors.New("status write conflicted with a newer write: the object has been modified")

	res, err := h.createReachedTheVendor(mg, werr, logging.NewNopLogger())

	require.NoError(t, err,
		"an error return is what makes the runtime leave early without closing the pending marker")
	assert.True(t, res.Requested,
		"Requested:true is what stamps external-create-succeeded and closes external-create-pending; "+
			"Requested:false leaves the object in the incomplete-create state and its recovery re-enters Create")

	// CONTRAST: the zero value is what the code returned before, and it is NOT equivalent.
	assert.False(t, controlZeroResultRequested(),
		"control: CreateResult{}.Requested is false, which is precisely the value that does not close "+
			"the marker -- so returning the zero value with a nil error would not have fixed this")
}

// THE FAILURE MUST NOT BE SILENT. The create is reported as successful to the runtime, so the only
// record that status was not written is this event and the log line beside it.
func TestACreateThatReachedTheVendorStillRecordsTheStatusFailure(t *testing.T) {
	h, evs := handlerForCreateGuard()
	mg := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "example.krateo.io/v1alpha1",
		"kind":       "Thing",
		"metadata":   map[string]interface{}{"name": "t", "namespace": "n"},
	}}
	werr := errors.New("boom")

	_, err := h.createReachedTheVendor(mg, werr, logging.NewNopLogger())
	require.NoError(t, err)

	all := evs.all()
	require.Len(t, all, 1, "exactly one event: the status-write failure")
	assert.Equal(t, reasonCreated, all[0].Reason)
	assert.Contains(t, all[0].Message, "status could not be written",
		"the message must say what failed")
	assert.Contains(t, all[0].Message, "will not be retried",
		"and that the create is deliberately not retried, or a reader will expect one")
	assert.Contains(t, all[0].Message, "boom",
		"the underlying error must survive, wrapped")
}

// ALL THREE VENDOR-CALL SITES MUST ROUTE THROUGH THE HELPER, and a behavioural test cannot reach them
// without the HTTP machinery of a delegated RESTAction, a dispatch and an ordinary create. So this is a
// source-level assertion with a control.
//
// Three, because every UpdateStatus in CreateWithResult that sits AFTER a vendor call was enumerated
// rather than assumed -- which mattered: the issue originally guessed the async/pending ones were safe
// to keep as errors, and they are not. The delegated RESTAction is idempotent BY CONTRACT, but a
// dispatch is not, so a retry there fires the action a second time.
func TestEveryVendorCallSiteRoutesThroughTheHelper(t *testing.T) {
	b, err := os.ReadFile("restResources.go")
	require.NoError(t, err)
	src := string(b)

	fn := strings.Index(src, "func (h *handler) CreateWithResult(")
	require.NotEqual(t, -1, fn, "CreateWithResult's signature changed; re-point this test")
	body := src[fn:]
	if next := strings.Index(body[1:], "\nfunc (h *handler) "); next != -1 {
		body = body[:next+1]
	}

	// CONTROL: the slice must really be CreateWithResult, or the count below is meaningless.
	require.Contains(t, body, "ResourceBeingDeleted",
		"control: this does not look like CreateWithResult's body")

	assert.Equal(t, 3, strings.Count(body, "h.createReachedTheVendor("),
		"all three sites that have already reached the vendor must report the create as requested: the "+
			"delegated RESTAction, the dispatch, and the ordinary create. Found a different number, so "+
			"either a site was added without this treatment or one was removed")
}

// controlZeroResultRequested reads Requested off the REAL controller.CreateResult zero value -- the
// value the three sites returned before this fix. A lookalike local struct would make the control pass
// for a reason unrelated to the type under test.
func controlZeroResultRequested() bool {
	return controller.CreateResult{}.Requested
}
