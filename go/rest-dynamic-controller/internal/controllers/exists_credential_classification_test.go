//go:build unit

package restResources

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	ucontroller "github.com/krateo-platformops/unstructured-runtime/pkg/controller"
)

// WHAT THE MARKER DOES, which is why this split matters more than a message does.
//
// ExistenceUndetermined tells the runtime "ask again later". The runtime then sets Ready=False/Creating,
// and core-provider's rollup classifies a Creating child as CONVERGING: the parent reads
// Ready=False/Creating naming the child, WITH NO DEADLINE. It never goes green -- two tests in
// childhealth_test.go pin that -- but it also never fails, so a 403 that needs a human would be reported
// as progress for as long as nobody looked.
//
// Unmarked, the child reaches CreateOutcomeUnknown and the parent reads Unavailable. That is the honest
// shape for "needs a human", and it is the contract core-provider stated from their own code.
//
// So this test is about which errors carry the marker, not about their text.
func TestTheCredentialClassificationSplits401From403(t *testing.T) {
	vendor := func(code int) error {
		return &restclient.StatusError{StatusCode: code, Inner: fmt.Errorf("invalid status code: %d", code)}
	}

	t.Run("403 is NOT marked transient", func(t *testing.T) {
		err := vendor(http.StatusForbidden)
		require.True(t, restclient.IsAuthorizationDenied(err), "setup: the predicate must see this as a denial")

		// The arm the handler takes for a 403 produces a plain error.
		produced := fmt.Errorf("existence check: the vendor REFUSED the credential for get with 403: %w", err)
		assert.False(t, ucontroller.IsExistenceUndetermined(produced),
			"a 403 marked transient makes the parent composition read Creating with no deadline, which "+
				"reports an authorization decision as progress")
	})

	t.Run("401 IS marked transient", func(t *testing.T) {
		err := vendor(http.StatusUnauthorized)
		require.False(t, restclient.IsAuthorizationDenied(err),
			"401 is deliberately not an authorization denial: credential rotation is delegated to a "+
				"secret manager, so a 401 is how a credential being REPLACED looks")

		produced := ucontroller.ExistenceUndetermined(
			fmt.Errorf("existence check: the vendor REJECTED the credential for get with 401: %w", err))
		assert.True(t, ucontroller.IsExistenceUndetermined(produced),
			"marking a rotation-shaped 401 terminal would report Unavailable through every ordinary "+
				"rotation")
	})

	// THE CONTROL. Without it, an IsExistenceUndetermined that always returned false would make the 403
	// case above pass while proving nothing.
	t.Run("the marker predicate can tell the two apart", func(t *testing.T) {
		plain := errors.New("plain")
		marked := ucontroller.ExistenceUndetermined(plain)
		assert.False(t, ucontroller.IsExistenceUndetermined(plain), "an unmarked error must read unmarked")
		assert.True(t, ucontroller.IsExistenceUndetermined(marked), "a marked error must read marked")
		assert.True(t, errors.Is(marked, plain), "and the marker must preserve the cause")
	})
}

// The messages are read FROM THE SOURCE rather than restated here. A test holding its own copy of a
// sentence is the two-copies problem this session has spent the day fixing, and the first version of this
// test proved it: my pasted copy of the 401 message was TRUNCATED, so it failed on a clause the real
// message has. The copy was wrong, not the code.
//
// exists.go is inside this package, so the Go test cache tracks it -- unlike the chart YAML that
// hack/lease-name-agreement.py exists for, which lives outside the module and is NOT tracked.
func TestTheCredentialMessagesNameTheCredentialAndNotJustTheStatus(t *testing.T) {
	raw, err := os.ReadFile("exists.go")
	require.NoError(t, err, "reading the handler's own source")

	// JOIN Go's string concatenation before searching. A message long enough to be useful is wrapped by
	// gofmt across several lines as `"..." +` / `"..."`, so an anchor that reads contiguously in the
	// rendered message does NOT appear contiguously in the source. The same wrapping broke an earlier
	// source-scanning guard in this package, which is why this is normalised rather than worked around
	// with a shorter anchor.
	src := regexp.MustCompile(`"\s*\+\s*\n\s*"`).ReplaceAllString(string(raw), "")

	for _, tc := range []struct{ name, anchor string }{
		{"403", "REFUSED the credential"},
		{"401", "REJECTED the credential"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := strings.Index(src, tc.anchor)
			require.GreaterOrEqual(t, i, 0,
				"no %s arm found. The previous text read only \"unexpected status: %s\", which left an "+
					"operator to translate a number into \"a token is wrong\" and then to find WHICH token.",
				tc.name, tc.name)
			// The format string runs to the closing `))` of its fmt.Errorf; a generous window is enough
			// to see whether the clause that points at the source is inside it.
			window := src[i:min(i+900, len(src))]
			assert.Contains(t, window, "authenticationRefs",
				"naming the credential is half the job; the other half is saying where it comes from")
		})
	}
}
