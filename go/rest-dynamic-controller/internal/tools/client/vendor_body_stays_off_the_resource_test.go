package restclient

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAVendorBodyNeverReachesTheErrorString pins the one property that keeps a vendor's response text off
// the custom resource, and which until now held only by construction.
//
// THE CHAIN, traced end to end on both construction sites rather than assumed:
//
//	restclient.go:407 / :988   BodySnippet: redactSnippet(errorBodySnippet(resp.Body), ...)
//	                           Inner:       statusCodeError(resp.StatusCode)   <- status ONLY
//	StatusError.Error()        renders StatusCode and Inner. Never BodySnippet.
//	the controller             returns that error; unstructured-runtime's ReconcileError puts the
//	                           error STRING into a condition message, where anyone with `get` reads it
//
// So the body is excluded because Error() happens not to mention a field it could mention. ONE field added
// to that Sprintf -- by someone making the message more helpful, which is a reasonable thing to want --
// publishes attacker-influenced vendor text to every reader of the resource. Nothing failed if that
// happened. No test, no lint, no review checklist: the log line asserting the property is a COMMENT.
//
// A consumer quoted that comment back to me as the reason their vendor's explanation was unreachable. The
// comment is TRUE, which is why this test exists: a true safety claim that nothing enforces is one commit
// from being false, and the commit that breaks it will look like an improvement.
func TestAVendorBodyNeverReachesTheErrorString(t *testing.T) {
	// A sentinel that cannot appear in a legitimate rendering of status code or inner error. "403" or
	// "forbidden" would be matched by Error()'s own output and the assertion would be vacuous.
	const sentinel = "ZZQX-vendor-explanation-must-not-appear-ZZQX"

	se := &StatusError{
		StatusCode:  403,
		Inner:       errors.New("forbidden"),
		BodySnippet: sentinel,
	}

	// SETUP CONTROL: the sentinel really is on the error, so a pass below means "excluded from the
	// string" and not "never set in the first place". These two must fail differently.
	require.Equal(t, sentinel, VendorBodySnippet(se),
		"the fixture did not carry the snippet, so the exclusion assertion proves nothing")

	require.NotContains(t, se.Error(), sentinel,
		"the vendor's response body is in the error string, and unstructured-runtime's ReconcileError "+
			"puts that string into a condition message readable by anyone with `get` on the resource")

	// AND THROUGH A WRAP, because the controller returns wrapped errors and %v on a wrapped error calls
	// the inner Error(). A rendering that is safe bare and unsafe wrapped would pass the check above.
	wrapped := fmt.Errorf("creating widget: %w", se)
	require.NotContains(t, wrapped.Error(), sentinel,
		"the body escapes through a wrap, which is how every caller actually returns this error")

	// The snippet must still be REACHABLE by the layer that logs it, or excluding it from the string
	// would have discarded the explanation rather than relocated it -- the defect #334 was filed for.
	require.Equal(t, sentinel, VendorBodySnippet(wrapped),
		"the snippet is unreachable through a wrap, so the explanation is lost rather than relocated")
}

// TestASynthesisedStatusAlsoWithholdsTheBody covers the other branch of Error(), which has its own
// Sprintf pair and so its own chance to include the field.
//
// Two renderings of one rule is the shape where a guard gets applied to one and not the other.
func TestASynthesisedStatusAlsoWithholdsTheBody(t *testing.T) {
	const sentinel = "ZZQX-synthesised-path-body-ZZQX"

	for _, inner := range []error{nil, errors.New("item not found")} {
		se := &StatusError{
			StatusCode:  404,
			Synthesised: true,
			Inner:       inner,
			BodySnippet: sentinel,
		}
		require.Equal(t, sentinel, VendorBodySnippet(se), "fixture did not carry the snippet")
		require.NotContains(t, se.Error(), sentinel,
			"the synthesised rendering leaks the body (inner=%v)", inner)
		// The synthesised branch must also keep saying it was synthesised, or a caller deciding whether
		// to clear krateo.io/external-create-pending reads an invented 404 as a vendor one.
		require.True(t, strings.Contains(se.Error(), "synthesised"),
			"the synthesised marker is gone from the message (inner=%v)", inner)
	}
}
