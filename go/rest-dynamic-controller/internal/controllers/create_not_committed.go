package restResources

import "net/http"

// createRefusedWithoutCreating reports whether an external status code means the vendor REFUSED the
// request and therefore created nothing.
//
// Its only caller wraps such an error in controller.CreateNotCommitted, which tells the runtime it may
// retry the create directly instead of confirming the negative with an existence check first.
//
// THE ASYMMETRY DECIDES THE LIST, in core-provider's statement of it rather than mine: an UNCLASSIFIED
// error costs one existence check, plus the two-minute recovery grace if the resource is absent. A
// WRONGLY classified one costs a DUPLICATE external resource. So this is deliberately a short list of
// codes the vendor answers having rejected the request, and every other outcome is left alone.
//
//	400 Bad Request          the request was malformed; nothing was created
//	401 Unauthorized         the credential was rejected at AUTHENTICATION, before any handler that
//	                         could create. See the block below: this was an omission, not a decision.
//	422 Unprocessable        the request was understood and refused on its content
//
// 401 WAS MISSING AND THE OMISSION HAD A COST THIS COMMENT'S OWN MODEL COULD NOT SEE. The asymmetry above
// prices an unclassified error at "one existence check, plus the two-minute recovery grace". FOR A KIND
// THAT CANNOT ANSWER EXISTENCE -- no findby, or a get that needs an identifier the create never recorded
// -- that check never answers, so the price is not one check: the CR sits at CreateOutcomeUnknown
// indefinitely AND CANNOT BE DELETED, because unstructured-runtime's create-recovery gate runs before the
// deletion dispatch and is deliberately unconditional. Measured by two consumer sessions: a 58-minute
// hung `kubectl delete`, and on a real service desk every create during an end-user token expiry becoming
// a stuck resource.
//
// AND THE TWO SEND PATHS DISAGREED ABOUT THE SAME STATUS. restclient.IsAuthorizationDenied is 403-only --
// correctly, since a 401 is how credential rotation in flight looks and must stay retryable -- so on the
// ordinary create path a 401 fell through to the generic post-send return, and on the dispatch path it
// landed on Requested: true with no classification. Same status code, same vendor, opposite outcomes,
// decided only by which code path sent the request.
//
// #783's own comment names this: it says the dispatch block's missing branch meant "a 401 or 403 landed
// there and held the CR at CreateOutcomeUnknown -- waiting for a human on what is an ordinary operational
// event, a rotated or expired credential". The branch it added keys on IsAuthorizationDenied, which is
// 403. SO THE COMMENT CLAIMED BOTH HALVES AND THE CODE DELIVERED ONE, and the 401 half stayed broken
// behind a sentence saying it was fixed. That is why the omission survived review: the reasoning had
// already been written down.
//
// WHY 401 BELONGS HERE RATHER THAN IN THE EARLIER AUTHZ BRANCH: createAuthorizationDeniedResult returns
// Requested: false, no error, and a condition that tells an operator to fix the credential. That is right
// for a 403, which retrying will not change. A 401 is the opposite -- it clears by itself when the secret
// manager rotates -- so it needs a RETRY, which is what CreateNotCommitted gives: the runtime re-sends
// directly, and the pass after the rotation files the request. No duplicate is possible because
// authentication rejected the request before anything could be written.
//
// THE RESIDUAL RISK, stated rather than dismissed: a gateway that authenticates AFTER forwarding could
// answer 401 having already written. That is the same risk class the list already accepts for 400 and 422
// -- a vendor that writes and then rejects -- and it is not the shape any measured case takes.
//
// NOT 409 Conflict, and this is the exclusion that matters: some vendors answer 409 BECAUSE the resource
// now exists, so "not committed" is false for exactly the case that would duplicate.
//
// NOT 404. On a create it usually means a parent is missing, so nothing was created -- but a vendor that
// answers 404 from a route which nevertheless wrote something would be unrecoverable, and the price of
// leaving it out is one existence check.
//
// NOT 5xx or a transport error, which are the whole reason the recovery exists: the vendor may have
// committed and lost the acknowledgement. That is the case a create must never blindly retry.
//
// NOT 403 EITHER, and not because of a judgement: it never reaches the caller.
// restclient.IsAuthorizationDenied is handled earlier in the create path and returns a NIL error, so the
// runtime sees no failure and retries nothing. 403 was in the list proposed to core-provider before this
// path was read; including it here would be dead code that implied the opposite.
//
// A zero code -- externalStatusCode's answer when there is no HTTP status to read -- is not classified,
// which is correct: no status means the request may never have completed.
func createRefusedWithoutCreating(code int) bool {
	switch code {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}
