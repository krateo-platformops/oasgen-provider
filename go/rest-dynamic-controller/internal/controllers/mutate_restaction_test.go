package restResources

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	stringset "github.com/krateo-platformops/rest-dynamic-controller/internal/text"
	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestHasObserveVerb(t *testing.T) {
	assert.False(t, hasObserveVerb(nil))
	assert.False(t, hasObserveVerb([]getter.VerbsDescription{{Action: "create"}, {Action: "delete"}}),
		"create/delete alone cannot verify convergence")
	assert.True(t, hasObserveVerb([]getter.VerbsDescription{{Action: "create"}, {Action: "get"}}))
	assert.True(t, hasObserveVerb([]getter.VerbsDescription{{Action: "findby"}}))
	assert.True(t, hasObserveVerb([]getter.VerbsDescription{{Action: "GET"}}), "match is case-insensitive")
}

// mockGetClient implements restclient.UnstructuredClientInterface with a single canned GET answer, so the
// delete-verification probe can be driven end to end through builder.APICallBuilder rather than by calling
// evalNotFoundBody directly. Calling the helper directly is precisely what hid #218: both call sites are
// correct in isolation, and only one of them passed the body.
type mockGetClient struct {
	body    any   // the ResponseBody the GET answers with
	callErr error // when set, Call fails with this instead
	calls   int
}

func (m *mockGetClient) Call(ctx context.Context, cli *http.Client, path string, conf *restclient.RequestConfiguration) (restclient.Response, error) {
	m.calls++
	if m.callErr != nil {
		return restclient.Response{}, m.callErr
	}
	return restclient.Response{ResponseBody: m.body}, nil
}

func (m *mockGetClient) ValidateRequest(httpMethod, path string, parameters, query, headers, cookies map[string]string) error {
	return nil
}
func (m *mockGetClient) RequestedBody(httpMethod, path string) (stringset.StringSet, error) {
	return nil, nil
}
func (m *mockGetClient) RequestedParams(httpMethod, path string) (stringset.StringSet, stringset.StringSet, stringset.StringSet, stringset.StringSet, error) {
	// BOTH placeholders the tombstoneInfo path contains. Declaring only "id" left {kmipId} unpopulated, so
	// the URL these fixtures "called" would have carried the literal text "{kmipId}" -- a request
	// production could never have sent. It went unnoticed because the GET is mocked, so the unbuildable
	// URL never mattered; the delete-verification guard added for #261 is what surfaced it.
	return stringset.NewStringSet("id", "kmipId"), nil, nil, nil, nil
}
func (m *mockGetClient) FindBy(ctx context.Context, cli *http.Client, path string, conf *restclient.RequestConfiguration, findByAction *getter.VerbsDescription) (restclient.Response, error) {
	return restclient.Response{}, nil
}

// setFor wraps a canned client in a clientSet keyed by the definition's own document path, which is what
// every verb in these fixtures resolves to.
func setFor(info *getter.Info, cli restclient.UnstructuredClientInterface) *clientSet {
	return newClientSetWithClients(info, map[string]restclient.UnstructuredClientInterface{info.URL: cli})
}

// tombstoneInfo is a RestDefinition with a get verb carrying the notFoundBody predicate that Aruba
// security/Kmip needs: a 200 whose body says the record is deleted (#111).
func tombstoneInfo(predicate string) *getter.Info {
	v := getter.VerbsDescription{Action: "get", Method: "GET", Path: "/kms/{id}/kmip/{kmipId}"}
	if predicate != "" {
		v.NotFoundBody = &getter.JQProgram{Inline: predicate}
	}
	return &getter.Info{
		// A real document path: the getter refuses a definition without spec.oasPath, so a fixture with an
		// empty URL is not a shape production can produce.
		URL: "configmap://krateo-system/aruba-security/security.json",
		Resource: getter.Resource{
			Kind:             "Kmip",
			Identifiers:      []string{"id"},
			VerbsDescription: []getter.VerbsDescription{v},
		},
	}
}

func kmipCR() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"id": "b132fb62-998d-479f-9c9d-794c6f1ea2fd",
			// Required for the resource to be ADDRESSABLE: tombstoneInfo's path is
			// /kms/{id}/kmip/{kmipId}, so a CR carrying only `id` describes something no request can be
			// aimed at. These fixtures exist to exercise tombstone and liveness logic on a resource that
			// CAN be fetched, so they must supply every placeholder in the path (#261).
			"kmipId": "7c1f0b9e-3a44-4d1a-9f2b-55e0c3a81d47",
		},
		"status": map[string]interface{}{},
	}}
}

// unaddressableKmipCR is the #261 shape: a CR whose create failed, so no identifier was ever written and
// the GET path cannot be built. Deliberately missing kmipId rather than both, because one unresolved
// placeholder is enough and it keeps the contrast with kmipCR to a single field.
func unaddressableKmipCR() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"spec":   map[string]interface{}{"id": "b132fb62-998d-479f-9c9d-794c6f1ea2fd"},
		"status": map[string]interface{}{},
	}}
}

const tombstonePredicate = `if (.status | type) == "string" then (.status | ascii_downcase) == "deleted" else false end`

// TestExternalResourceStillExistsHonoursTombstoneBody is the test whose absence let #218 ship in the same
// commit as the feature: the predicate must be evaluated against the response BODY, not the
// restclient.Response wrapper around it. Before the fix the wrapper reached gojq, which cannot index a Go
// struct, so the predicate errored and the resource came back unverifiable — meaning deletion was never
// actually verified for exactly the resources notFoundBody exists to serve.
func TestExternalResourceStillExistsHonoursTombstoneBody(t *testing.T) {
	h := &handler{}
	cli := &mockGetClient{body: map[string]interface{}{
		"status":       "Deleted",
		"deletionDate": "2026-10-23T00:00:00Z",
	}}

	info := tombstoneInfo(tombstonePredicate)
	stillExists, verified, err := h.externalResourceStillExists(
		context.Background(), setFor(info, cli), info, kmipCR(), logging.NewNopLogger())

	require.NoError(t, err)
	assert.False(t, stillExists, "a 200 tombstone the predicate matches is absence, not presence")
	assert.True(t, verified, "the predicate answered, so the delete is VERIFIED gone -- not merely trusted")
	assert.Equal(t, 1, cli.calls, "the get verb is probed exactly once")
}

// TestExternalResourceStillExistsReportsLiveResource is the other half: the same predicate against a live
// record must report verifiably present, so the finalizer is held. It fails before the fix too -- the
// errored predicate reported (false, false), i.e. "could not check", for a resource plainly still there.
func TestExternalResourceStillExistsReportsLiveResource(t *testing.T) {
	h := &handler{}
	cli := &mockGetClient{body: map[string]interface{}{"status": "Active"}}

	info := tombstoneInfo(tombstonePredicate)
	stillExists, verified, err := h.externalResourceStillExists(
		context.Background(), setFor(info, cli), info, kmipCR(), logging.NewNopLogger())

	require.NoError(t, err)
	assert.True(t, stillExists, "the predicate did not match, so the 200 means the resource is still there")
	assert.True(t, verified, "and that answer is verified, not a fallback")
}

// TestExternalResourceStillExistsTolleratesBrokenPredicate pins the forgiving branch the fix must not
// change: a predicate that cannot produce a boolean is reported unverifiable (not an error, not "gone"),
// so the delete result governs and the CR neither wedges in Deleting nor orphans its resource.
func TestExternalResourceStillExistsTolleratesBrokenPredicate(t *testing.T) {
	h := &handler{}
	cli := &mockGetClient{body: map[string]interface{}{"status": "Deleted"}}

	info := tombstoneInfo(`.status`)
	stillExists, verified, err := h.externalResourceStillExists(
		context.Background(), setFor(info, cli), info, kmipCR(), logging.NewNopLogger())

	require.NoError(t, err, "a mistyped predicate must not surface as a reconcile error")
	assert.False(t, stillExists)
	assert.False(t, verified, "unverifiable: the delete result is the only signal there is")
}

// TestExternalResourceStillExistsWithoutPredicate proves the fix did not make notFoundBody mandatory: with
// no predicate declared, a 200 is presence and a 404 is verified absence, exactly as before.
func TestExternalResourceStillExistsWithoutPredicate(t *testing.T) {
	h := &handler{}

	info := tombstoneInfo("")
	stillExists, verified, err := h.externalResourceStillExists(
		context.Background(), setFor(info, &mockGetClient{body: map[string]interface{}{"status": "Active"}}),
		info, kmipCR(), logging.NewNopLogger())
	require.NoError(t, err)
	assert.True(t, stillExists)
	assert.True(t, verified)

	stillExists, verified, err = h.externalResourceStillExists(
		context.Background(), setFor(info, &mockGetClient{callErr: &restclient.StatusError{StatusCode: http.StatusNotFound}}),
		info, kmipCR(), logging.NewNopLogger())
	require.NoError(t, err)
	assert.False(t, stillExists)
	assert.True(t, verified, "a 404 is verifiably gone")
}

// TestResolveDeleteOutcomeReleasesOnTombstoneDespiteDeleteError pins the outcome #111 was filed for, which
// the broken predicate left unreachable: the resource is verifiably gone, so the finalizer is released even
// though the DELETE itself reported an error. Before the fix the tombstone read as unverifiable, deleteErr
// governed, and the CR held its finalizer forever -- the exact wedge the feature was meant to remove.
func TestResolveDeleteOutcomeReleasesOnTombstoneDespiteDeleteError(t *testing.T) {
	h := &handler{}
	cli := &mockGetClient{body: map[string]interface{}{"status": "Deleted"}}

	info := tombstoneInfo(tombstonePredicate)
	err := h.resolveDeleteOutcome(context.Background(), setFor(info, cli), info, kmipCR(),
		fmt.Errorf("delete returned 500"), logging.NewNopLogger())

	assert.NoError(t, err, "verifiably absent beats the delete error: release the finalizer")
}

// TestResolveDeleteOutcomeHoldsWhenResourceIsStillThere is the other outcome the fix changes, and the one
// worth knowing about: a 200 the predicate does NOT match is now verifiably present, so a successful delete
// is no longer trusted -- it holds and retries. That is the premise of #111 (a 2xx means the deletion was
// requested, not completed), but it does mean a predicate that fails to match a real tombstone now hangs
// the delete where it previously fell through to the delete result.
func TestResolveDeleteOutcomeHoldsWhenResourceIsStillThere(t *testing.T) {
	h := &handler{}
	cli := &mockGetClient{body: map[string]interface{}{"status": "Active"}}

	info := tombstoneInfo(tombstonePredicate)
	err := h.resolveDeleteOutcome(context.Background(), setFor(info, cli), info, kmipCR(),
		nil, logging.NewNopLogger())

	require.Error(t, err, "a successful delete is not proof; the get verb says it is still there")
	assert.Contains(t, err.Error(), "still present")
}

func (m *mockGetClient) UpdatableBodyPaths(httpMethod, path string) ([]string, error) {
	return nil, nil
}

// TestExternalResourceStillExistsReportsUnaddressableAsUnverifiable is the #261 fix.
//
// The delegated delete path verified deletion with a GET and returned that GET's error as its own. For a
// TRANSIENT failure that is right -- "could not check" means retry. For a PERMANENT one it is a wedge: a CR
// whose create failed has no identifier in its status, so the GET path can never be built, the error
// repeats on every reconcile, and the finalizer is held until a human strips it by hand. Measured at eight
// hours on a CloudServer whose create the vendor refused, while a Vpc in the same state deleted at once --
// because the NATIVE delete path has always had this guard and this path never consulted it.
//
// Asserting calls == 0 rather than only the return values is the point: the fix is that the request is
// never ATTEMPTED. A version that dialled anyway and swallowed the error would satisfy the return contract
// while still waiting out a connection timeout per reconcile, and would pass a test that only read
// (stillExists, verified, err).
func TestExternalResourceStillExistsReportsUnaddressableAsUnverifiable(t *testing.T) {
	h := &handler{}
	// Answers 200/live if dialled at all, so a regression that attempts the call reports the resource
	// PRESENT and holds the finalizer -- a loud failure here rather than an accidental pass.
	cli := &mockGetClient{body: map[string]interface{}{"status": "Active"}}

	info := tombstoneInfo(tombstonePredicate)
	stillExists, verified, err := h.externalResourceStillExists(
		context.Background(), setFor(info, cli), info, unaddressableKmipCR(), logging.NewNopLogger())

	require.NoError(t, err, "an unresolved path placeholder is a PERMANENT inability to verify, so it must "+
		"not surface as an error: the caller retries on error, and this condition can never clear")
	assert.False(t, stillExists)
	assert.False(t, verified, "nothing was checked, so nothing is verified")
	assert.Zero(t, cli.calls, "the verification GET must never be attempted: its URL would carry the "+
		"literal text {kmipId}, so the request cannot be aimed at anything")
}

// TestResolveDeleteOutcomeReleasesWhenUnaddressable is the same fix at the level that matters to a user: the
// CR actually leaves Deleting.
func TestResolveDeleteOutcomeReleasesWhenUnaddressable(t *testing.T) {
	h := &handler{}
	cli := &mockGetClient{body: map[string]interface{}{"status": "Active"}}

	info := tombstoneInfo(tombstonePredicate)
	err := h.resolveDeleteOutcome(context.Background(), setFor(info, cli), info, unaddressableKmipCR(),
		nil, logging.NewNopLogger())

	assert.NoError(t, err, "unverifiable means the delete result governs; the delete succeeded, so release")
	assert.Zero(t, cli.calls)
}

// TestResolveDeleteOutcomeStillHoldsOnTransientVerificationFailure is the CONTROL ARM, and it is the more
// important of the two.
//
// Without it the #261 fix could drift into "release the finalizer whenever verification is inconvenient",
// which would orphan real external resources -- the failure mode the hold-on-error rule exists to prevent,
// and a far worse one than the wedge being fixed. A 500 from the vendor, or a refused connection, is
// exactly the case where the resource may well still be there and we cannot tell. That must still hold.
//
// The distinction the fix draws is PERMANENT vs TRANSIENT, not "verification failed". This test pins the
// transient half so narrowing it is a test failure rather than a silent change of policy.
func TestResolveDeleteOutcomeStillHoldsOnTransientVerificationFailure(t *testing.T) {
	h := &handler{}
	cli := &mockGetClient{callErr: &restclient.StatusError{StatusCode: http.StatusInternalServerError}}

	info := tombstoneInfo(tombstonePredicate)
	// Addressable: every placeholder resolves, so the guard does not apply and the call is made.
	err := h.resolveDeleteOutcome(context.Background(), setFor(info, cli), info, kmipCR(),
		nil, logging.NewNopLogger())

	require.Error(t, err, "a 500 leaves the resource's state unknown, so the finalizer must be held and "+
		"the delete retried -- releasing here would orphan a resource that is still present")
	assert.Equal(t, 1, cli.calls, "an addressable resource must still be dialled")
}
