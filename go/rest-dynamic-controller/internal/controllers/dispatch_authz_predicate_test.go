//go:build unit

package restResources

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
)

// #783's existing guards assert the ORDER of the dispatch caller's branches, by source offset. They cannot
// fail when the PREDICATE does not match the error the dispatch path actually produces -- the branch is
// simply never taken, and an ordering assertion is still true.
//
// That gap is why a consumer measured "#783 does not take effect" on 0.81.0. Their case was a 401, which
// IsAuthorizationDenied returns false for DELIBERATELY (credential rotation is delegated; a 401 may be a
// rotation in flight, so it must stay retryable). The behaviour was correct and the expectation was mine:
// I had described their 401 case to them as "the before-#783 measurement", which it never was.
//
// So this test does what the offset guards cannot: it builds the error the dispatch path produces, wrapped
// exactly as dispatch.go wraps it, and asserts what each predicate says about it.
func TestTheAuthzPredicateMatchesThroughTheDispatchWrapping(t *testing.T) {
	// dispatch.go's two post-send returns wrap with: fmt.Errorf("%w: %w", errDispatchReachedTheVendor, err)
	wrapAsDispatchDoes := func(inner error) error {
		return fmt.Errorf("%w: %w", errDispatchReachedTheVendor, inner)
	}
	// What the request path returns for a rejected status: a *StatusError around statusCodeError's text.
	vendorRejected := func(code int) error {
		return &restclient.StatusError{
			StatusCode: code,
			Inner:      fmt.Errorf("invalid status code: %d", code),
		}
	}

	t.Run("403 reaches the predicate through the sentinel wrap", func(t *testing.T) {
		err := wrapAsDispatchDoes(vendorRejected(http.StatusForbidden))

		require.True(t, errors.Is(err, errDispatchReachedTheVendor),
			"the sentinel must survive, or #778 stops classifying the send")
		assert.True(t, restclient.IsAuthorizationDenied(err),
			"THIS IS THE ASSERTION THE OFFSET GUARDS CANNOT MAKE: fmt.Errorf with two %w verbs must keep "+
				"the *StatusError reachable by errors.As, or #783's branch is dead code that no ordering "+
				"test would notice")
	})

	t.Run("401 is NOT authorization denied, by design", func(t *testing.T) {
		err := wrapAsDispatchDoes(vendorRejected(http.StatusUnauthorized))

		require.True(t, errors.Is(err, errDispatchReachedTheVendor))
		assert.False(t, restclient.IsAuthorizationDenied(err),
			"401 must stay retryable: credential rotation is delegated to a secret manager, so a 401 is "+
				"how a rotation IN FLIGHT looks. Treating it as a permanent denial would make this "+
				"architecture intolerant of the rotation it is built to absorb")
	})

	// THE NEGATIVE CONTROL FOR THE WRAP ITSELF. If fmt.Errorf's two-verb form silently dropped the second
	// error, the 403 case above would fail -- but so would a case that never had a StatusError, and the two
	// are indistinguishable from the 403 assertion alone.
	t.Run("a wrapped error with no status carries no authz verdict", func(t *testing.T) {
		err := wrapAsDispatchDoes(errors.New("connection reset by peer"))

		require.True(t, errors.Is(err, errDispatchReachedTheVendor))
		assert.False(t, restclient.IsAuthorizationDenied(err),
			"no status in the chain, so no verdict -- this is the dropped-socket case, which must reach "+
				"the unknown-outcome path and not a denial")
	})

	// AND THE CODES #778 CLASSIFIES AS "REFUSED WITHOUT CREATING", through the same wrap, because that
	// predicate reads a code off the same chain and has the same failure mode.
	for _, tc := range []struct {
		code         int
		refusedClean bool
		why          string
	}{
		{http.StatusBadRequest, true, "400 is a malformed request: the vendor stored nothing"},
		{http.StatusUnprocessableEntity, true, "422 is a semantic refusal: nothing stored"},
		// 401 WAS false IN THIS TABLE WHEN IT WAS WRITTEN, AND TWO GREEN PRs MADE IT WRONG. This test
		// (#792) and the 401 classification change (#793) were open at the same time; each passed CI
		// against a main that did not contain the other, and merging both left main red on exactly this
		// row. Neither review could have seen it, because neither diff mentions the other's file.
		//
		// The current behaviour is #793's and it is the right one: a 401 is refused at AUTHENTICATION,
		// before any handler that could create, so nothing was stored and CreateNotCommitted lets the
		// runtime retry directly. Leaving it unclassified sent it to an existence check that, for a kind
		// which cannot answer existence, never answers -- stranding the resource and blocking its
		// deletion.
		{http.StatusUnauthorized, true, "401 is refused at authentication: nothing was created, so the " +
			"runtime may retry the create directly rather than confirming a negative it cannot confirm"},
		{http.StatusForbidden, false, "403 is handled earlier as a denial, so it must not be listed here"},
		{http.StatusInternalServerError, false, "a 500 may have stored the resource before failing"},
	} {
		t.Run(fmt.Sprintf("refusedWithoutCreating/%d", tc.code), func(t *testing.T) {
			err := wrapAsDispatchDoes(vendorRejected(tc.code))
			got := createRefusedWithoutCreating(externalStatusCode(nil, err))
			assert.Equal(t, tc.refusedClean, got, tc.why)
		})
	}
}
