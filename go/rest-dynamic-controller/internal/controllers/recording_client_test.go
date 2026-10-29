package restResources

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/krateo-platformops/plumbing/kubeutil/event"
	stringset "github.com/krateo-platformops/rest-dynamic-controller/internal/text"
	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/builder"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"testing"
)

// recordingClient is the shared answer to "was an external call made?" — a question no delete-path test
// in this package could previously ask (#284).
//
// Before this, every test that needed an UnstructuredClientInterface built its own partial mock for the
// one method it cared about. That is why the delete-path tests assert on the LOCAL object (finalizer
// removed, condition set, error returned) and never on the side effect: the assertion was not available
// to write. It is also the gap that let two defects ship in unstructured-runtime's worker, where the
// shared fakeExternalClient returns canned errors and structurally cannot answer it.
//
// The ordering below is the load-bearing part. See Call.
type recordingClient struct {
	mu sync.Mutex

	// dialled counts requests that PASSED validation and went out, keyed by HTTP method. The real client
	// validates before dialling (restclient.go:93) and rejects an unresolved required path parameter
	// (clienttools.go:200), so a request can fail without ever being attempted -- and a test that cannot
	// tell those apart will pass for the wrong reason, as #261's did.
	dialled map[string]int
	// rejected counts requests refused by validation, before any HTTP.
	rejected int

	// body is the canned ResponseBody. status is reported via Response.StatusCode().
	body   any
	status int
	// callErr, when set, is returned INSTEAD of a response -- but only AFTER the call is recorded.
	callErr error

	// serverURL is what the C8 audit emitter reads for server.address (#253).
	serverURL string
	// pathParams are the parameter names this client's operations declare as required path parameters.
	pathParams []string
}

func newRecordingClient() *recordingClient {
	return &recordingClient{
		dialled:    map[string]int{},
		status:     http.StatusOK,
		serverURL:  "https://api.example.com/v1",
		pathParams: []string{"id"},
	}
}

// Call records the dial and THEN returns the canned failure.
//
// That order is deliberate and is the one thing to preserve if this type is extended: a fake that returns
// its error before incrementing makes "was it called?" unanswerable in exactly the failure cases worth
// testing -- which are most of them on a delete path.
//
// Validation runs first, mirroring the real client, so `dialled` means DIALLED rather than "reached the
// client".
func (r *recordingClient) Call(ctx context.Context, cli *http.Client, path string, conf *restclient.RequestConfiguration) (restclient.Response, error) {
	if err := r.ValidateRequest("", path, conf.Parameters, conf.Query, conf.Headers, conf.Cookies); err != nil {
		r.mu.Lock()
		r.rejected++
		r.mu.Unlock()
		return restclient.Response{}, err
	}

	r.mu.Lock()
	method := "ANY"
	if conf != nil && conf.Method != "" {
		method = conf.Method
	}
	r.dialled[method]++
	r.dialled["*"]++
	r.mu.Unlock()

	if r.callErr != nil {
		return restclient.Response{}, r.callErr
	}
	return restclient.Response{ResponseBody: r.body}, nil
}

// Dialled reports how many requests went out, for a method or "*" for any.
func (r *recordingClient) Dialled(method string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dialled[method]
}

// Rejected reports how many requests validation refused before any HTTP.
func (r *recordingClient) Rejected() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rejected
}

// ValidateRequest reproduces the one real validation these tests depend on: a required path parameter the
// configuration does not supply is refused before any HTTP (clienttools.go:200).
func (r *recordingClient) ValidateRequest(httpMethod, path string, parameters, query, headers, cookies map[string]string) error {
	for _, name := range builder.UnresolvedPathParams(path, parameters) {
		return fmt.Errorf("missing path parameter: %s", name)
	}
	return nil
}

func (r *recordingClient) RequestedBody(httpMethod, path string) (stringset.StringSet, error) {
	return nil, nil
}

func (r *recordingClient) RequestedParams(httpMethod, path string) (stringset.StringSet, stringset.StringSet, stringset.StringSet, stringset.StringSet, error) {
	return stringset.NewStringSet(r.pathParams...), nil, nil, nil, nil
}

func (r *recordingClient) UpdatableBodyPaths(httpMethod, path string) ([]string, error) {
	return nil, nil
}

func (r *recordingClient) FindBy(ctx context.Context, cli *http.Client, path string, conf *restclient.RequestConfiguration, findByAction *getter.VerbsDescription) (restclient.Response, error) {
	return r.Call(ctx, cli, path, conf)
}

// ServerURL is the external endpoint, read by the audit emitter for server.address.
func (r *recordingClient) ServerURL() string { return r.serverURL }

// --- #279: Create must not call out for an object that is being deleted ---

// recordingEvents captures what the handler puts on the CR. event.Recorder is a one-method interface, so
// this is cheaper and more direct than adapting client-go's FakeRecorder, whose Eventf signature does not
// match.
type recordingEvents struct {
	mu     sync.Mutex
	events []event.Event
}

func (r *recordingEvents) Event(obj runtime.Object, e event.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recordingEvents) all() []event.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]event.Event(nil), r.events...)
}

// handlerForCreateGuard builds a handler whose only wired dependencies are the recorder and the logger.
// Leaving swaggerInfoGetter NIL is deliberate: if the guard ever stops returning early, Create reaches
// `swaggerInfoGetter == nil` and fails loudly rather than quietly taking some other path. The control arm
// below asserts exactly that error, which is what proves the guard did not stop it.
func handlerForCreateGuard() (*handler, *recordingEvents) {
	evs := &recordingEvents{}
	return &handler{
		logger:        logging.NewNopLogger(),
		eventRecorder: evs,
	}, evs
}

func deletingCR() *unstructured.Unstructured {
	u := auditTestCR()
	now := metav1.Now()
	u.SetDeletionTimestamp(&now)
	u.SetFinalizers([]string{"composition.krateo.io/finalizer"})
	return u
}

// TestCreateRefusesWhileDeleting is the #279 guard.
//
// A Create event queued before deletion is still dispatched after it, because the runtime's worker
// gates only the finalizer-add block on WasDeleted and not the dispatch switch. Where the create
// succeeds, the external resource is orphaned once the delete completes — one per reconcile for the
// duration of the delete, measured at three on a live cluster.
func TestCreateRefusesWhileDeleting(t *testing.T) {
	h, evs := handlerForCreateGuard()

	err := h.Create(context.Background(), deletingCR())

	// nil, not an error: the object is going away, so there is no later moment at which creating becomes
	// correct and requeueing serves nothing.
	require.NoError(t, err, "a deleting object must not be requeued for creation")
	// The refusal must be visible on the object. A silent return is indistinguishable from a create that
	// happened to find nothing to do.
	got := evs.all()
	require.Len(t, got, 1, "the refusal must leave exactly one event on the CR")
	assert.Contains(t, got[0].Message, "orphaned",
		"the event must say WHY it refused, not merely that it did")
}

// TestCreateProceedsWhenNotDeleting is the CONTROL ARM, and without it the test above passes against a
// handler that refuses every Create for any reason.
//
// It asserts the guard is not what stops this call: with no deletionTimestamp, Create must get PAST the
// guard and fail on the unwired swaggerInfoGetter instead. That specific error is the proof it proceeded.
func TestCreateProceedsWhenNotDeleting(t *testing.T) {
	h, evs := handlerForCreateGuard()

	err := h.Create(context.Background(), auditTestCR()) // no deletionTimestamp

	require.Error(t, err, "a live object must proceed past the guard")
	assert.Contains(t, err.Error(), "swagger info getter",
		"it must fail on the NEXT step, which proves the guard did not stop it")
	assert.Empty(t, evs.all(), "no refusal event: this call was not refused, it got further")
}
