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
//	422 Unprocessable        the request was understood and refused on its content
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
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}
