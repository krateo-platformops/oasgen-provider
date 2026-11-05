package restResources

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/hostpolicy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A create refused by the host policy used to leave Ready=False/Creating, which every consumer reads as
// progress: the composition rollup buckets it as converging, the portal shows a "Creating" pill, and an
// operator sees an install that never finishes. The request was never sent and will not be until someone
// edits the allowlist or the document, so there is nothing to converge towards (#42).
//
// THE REFUSAL IS BUILT BY THE REAL POLICY, not by wrapping the sentinel directly. A test that constructed
// fmt.Errorf("...: %w", ErrEndpointNotPermitted) would pass even if production stopped wrapping it -- the
// thing most likely to break, since there are six return sites in PermitsURL and a seventh could be added
// without one. Asking the policy for its own refusal is the only version that fails when that happens.
func realRefusal(t *testing.T) error {
	t.Helper()
	p, _, err := hostpolicy.Parse([]string{"allowed.example.com"}, nil, false)
	require.NoError(t, err)
	u, err := url.Parse("https://refused.example.com/widgets")
	require.NoError(t, err)
	refusal := p.PermitsURL(u)
	require.Error(t, refusal, "premise: the policy must refuse a host outside its allowlist, or this test "+
		"is asserting against a nil error")
	require.ErrorIs(t, refusal, hostpolicy.ErrEndpointNotPermitted,
		"premise: production must wrap its refusals with the sentinel, or the guard under test cannot "+
			"classify anything and every arm below is vacuous")
	return refusal
}

func TestARefusedCreateIsRecordedAsCreateFailedAndPersisted(t *testing.T) {
	mg := driftMG(1, nil)
	h, dyn := observeAuthzHandler(t, mg)

	stopped, cerr := h.stopOnEndpointRefusal(context.Background(), mg, "create", realRefusal(t), h.logger)

	require.NoError(t, cerr, "the write succeeded here, so the deferral channel must be empty (#468)")
	require.True(t, stopped,
		"the caller must be told to STOP: retrying a configuration decision cannot change it, and #313 "+
			"settled that for the 403 case")
	assert.Equal(t, condition.ReasonCreateFailed, persistedReadyReason(t, dyn, mg.GetName()),
		"CreateFailed must be PERSISTED, not merely set: a condition left in memory is absent from the "+
			"object the next reconcile reads, so the resource goes back to reading as Creating a minute later")
}

func TestARefusedUpdateIsRecordedAsUpdateFailed(t *testing.T) {
	mg := driftMG(1, nil)
	h, dyn := observeAuthzHandler(t, mg)

	stopped, cerr := h.stopOnEndpointRefusal(context.Background(), mg, "update", realRefusal(t), h.logger)

	require.NoError(t, cerr, "the write succeeded here, so the deferral channel must be empty (#468)")
	require.True(t, stopped)
	assert.Equal(t, condition.ReasonUpdateFailed, persistedReadyReason(t, dyn, mg.GetName()),
		"an update must not be reported as a failed create: the verb is the one distinction a consumer "+
			"can key on, which is the whole reason the reason carries it rather than the message")
}

// THE CONTROL. Without it the arms above pass against a guard that classifies everything -- which would
// stop retrying transient vendor failures forever, converting a self-correcting case into a permanent one.
func TestAVendorFailureIsNotAnEndpointRefusal(t *testing.T) {
	mg := driftMG(1, nil)
	h, dyn := observeAuthzHandler(t, mg)

	boom := &restclient.StatusError{StatusCode: http.StatusInternalServerError}
	stopped, cerr := h.stopOnEndpointRefusal(context.Background(), mg, "create", boom, h.logger)

	assert.NoError(t, cerr,
		"a 500 is not this helper's business, so it must not be reported as a deferred write either: "+
			"(false, nil) is 'not mine' and (false, conflict) is 'mine but unwritten' (#468)")
	assert.False(t, stopped,
		"a 500 is the vendor having a bad minute, not a configuration decision: stopping here would "+
			"abandon a create that the next reconcile would have completed")
	assert.NotEqual(t, condition.ReasonCreateFailed, persistedReadyReason(t, dyn, mg.GetName()),
		"nothing may record a permanent failure for a transient one")
}

// The two guards must stay distinguishable: a permanent 403 is an AUTHORISATION decision and a refused
// endpoint is a POLICY decision, and an operator fixes them in different places -- a credential versus an
// allowlist. Collapsing them into one reason would send people to the wrong file.
func TestTheTwoPermanentFailuresKeepSeparateReasons(t *testing.T) {
	denied := &restclient.StatusError{StatusCode: http.StatusForbidden}
	refusal := realRefusal(t)

	assert.False(t, errors.Is(denied, hostpolicy.ErrEndpointNotPermitted),
		"a vendor 403 must not be classified as an endpoint refusal: the credential is wrong, the "+
			"allowlist is not")
	assert.False(t, restclient.IsAuthorizationDenied(refusal),
		"an endpoint refusal must not be classified as an authorisation denial: the request never reached "+
			"a server, so no server judged the credential")
}
