package restclient

import "testing"

// #391. The exemption that keeps ValidateRequest from demanding a credential header was a SUBSTRING test
// for "authorization" and nothing else, so it covered 1 of the 9 names this package already enumerates in
// credentialHeaders.
//
// The exemption exists because SetAuth attaches the credential to the *http.Request late in Call and in
// CallForPagination, while ValidateRequest runs earlier in each of those same two functions against
// opts.Headers -- a map the credential is never written into, since
// SetAuth operates on the *http.Request. The validator structurally cannot see it. That reasoning is not
// specific to `Authorization`; it applies to every header whose value arrives after validation.
//
// So a provider on X-Auth-Token, X-Api-Key or PRIVATE-TOKEN whose document ALSO declares that header as a
// required parameter had every request refused with "missing header: <name>" before the credential was
// attached.
func TestSuppliedAfterValidationCoversEveryCredentialHeader(t *testing.T) {
	for _, name := range []string{
		"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key",
		"Api-Key", "X-Auth-Token", "X-Amz-Security-Token", "Private-Token",
	} {
		if !suppliedAfterValidation(name) {
			t.Errorf("suppliedAfterValidation(%q) = false. ValidateRequest will then demand a header "+
				"SetAuth supplies 114 lines later, refusing a correctly formed request", name)
		}
	}
	// Case-insensitively, because header names are on the wire: a document spelling it `x-api-key` must
	// not walk past the exemption.
	for _, name := range []string{"x-api-key", "X-AUTH-TOKEN", "private-token"} {
		if !suppliedAfterValidation(name) {
			t.Errorf("suppliedAfterValidation(%q) = false; header names are case-insensitive", name)
		}
	}
}

// THE UNION MUST NOT NARROW. The old predicate was a SUBSTRING test, so a name merely CONTAINING
// "authorization" was exempt -- and `X-Custom-Authorization` is not in credentialHeaders. Swapping the
// substring test for the exact list would have been a silent regression for exactly those names, which is
// why both are kept.
//
// This case is the one that would catch a later "simplification" that drops isAuthorizationHeader as
// redundant.
func TestSuppliedAfterValidationStillCoversSubstringAuthorizationNames(t *testing.T) {
	for _, name := range []string{
		"X-Custom-Authorization", "authorization-token", "My-Authorization-Thing",
	} {
		if IsCredentialHeader(name) {
			t.Fatalf("test premise broken: %q is in credentialHeaders, so this case no longer "+
				"distinguishes the substring test from the exact list", name)
		}
		if !suppliedAfterValidation(name) {
			t.Errorf("suppliedAfterValidation(%q) = false: the old substring behaviour was lost, which "+
				"narrows the exemption instead of widening it", name)
		}
	}
}

// CONTROL. An ordinary required header must STILL be demanded, or the exemption has swallowed the check
// it is an exception to -- and #305's whole point is that a required header the CR carries should be
// populated and validated, not waved through.
func TestSuppliedAfterValidationDoesNotExemptOrdinaryHeaders(t *testing.T) {
	for _, name := range []string{
		"X-Tenant", "Accept", "Content-Type", "X-Request-Id", "X-OpenStack-Nova-API-Version",
	} {
		if suppliedAfterValidation(name) {
			t.Errorf("suppliedAfterValidation(%q) = true, but an ordinary required header must still be "+
				"demanded: nothing supplies it after validation, so exempting it means the request goes "+
				"out without a header the document says is mandatory", name)
		}
	}
}
