//go:build unit || integration

package comparison

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEmptyRemoteRespectsListedLeaves is the table for issue 912, and it replaces a file that pinned
// the defect as a known limitation -- that test's assertion said "when this starts returning equal, the
// guard has been fixed", and it has.
//
// THE RULE IS THE CONSUMER'S. I proposed a new `emptyObjectSatisfiesFields` list;
// keycloak-provider-kog refuted the need for it, because requirePresentFields plus issue 911 already
// say, leaf by leaf, both that absence is meaningful and that a declared empty value is satisfied by
// it. The guard answered for the whole object before the leaves were asked.
//
// Every arm below was named by a fleet that measured the vendor behind it.
func TestEmptyRemoteRespectsListedLeaves(t *testing.T) {
	const leaf = "config.allowed-client-scopes"
	cmp := func(t *testing.T, listed []string, cr, remote map[string]any) ComparisonResult {
		t.Helper()
		res, err := NewComparer(nil).WithRequiredPaths(listed).CompareExisting(cr, remote)
		require.NoError(t, err)
		return res
	}
	empty := map[string]any{"config": map[string]any{"allowed-client-scopes": []any{}}}

	t.Run("every comparable leaf listed and declared empty, remote {}: AGREEMENT", func(t *testing.T) {
		res := cmp(t, []string{leaf}, empty, map[string]any{"config": map[string]any{}})
		require.True(t, res.IsEqual,
			"keycloak measured {} as how their vendor returns 'no keys set', and a single-key policy "+
				"declaring [] against it sent 3 updates per 420s and never settled")
	})

	// IRONIC, measured: a vendor answered instance_info: {} while the CR carried real values. The guard
	// exists for this and must keep firing.
	t.Run("listed but a REAL declared value, remote {}: DRIFT", func(t *testing.T) {
		cr := map[string]any{"config": map[string]any{"allowed-client-scopes": []any{"mcp"}}}
		res := cmp(t, []string{leaf}, cr, map[string]any{"config": map[string]any{}})
		require.False(t, res.IsEqual, "the guard must still fire when a declared value is not empty")
	})

	// EVERY, not any: one unlisted leaf is enough to leave the guard in force, because the author has
	// not said that absence is meaningful there.
	t.Run("one listed+empty leaf, one UNLISTED+empty leaf, remote {}: DRIFT", func(t *testing.T) {
		cr := map[string]any{"config": map[string]any{
			"allowed-client-scopes": []any{},
			"web-origins":           []any{},
		}}
		res := cmp(t, []string{leaf}, cr, map[string]any{"config": map[string]any{}})
		require.False(t, res.IsEqual,
			"the rule is EVERY comparable leaf; one unlisted leaf leaves the guard in force")
	})

	// GITHUB'S ARM, and it is safety-critical rather than tidiness. Their vendor KEEPS
	// dismissal_restrictions set to empty lists and turns the restriction OFF with {} in the PUT, so
	// "nobody may dismiss" (empty lists) and "no restriction" (null or absent) are OPPOSITE settings.
	// Treating a null parent as satisfied here would read a branch where anybody may dismiss as
	// converged. keycloak had asked for absence to be treated like {} -- hedging it themselves as
	// unmeasured -- and this is why that clause was declined.
	t.Run("remote parent is NULL, leaves listed and declared empty: DRIFT", func(t *testing.T) {
		res := cmp(t, []string{leaf}, empty, map[string]any{"config": nil})
		require.False(t, res.IsEqual,
			"null is not {}: for at least one measured vendor they are opposite settings, so the "+
				"relaxation must never reach a null parent")
	})

	t.Run("remote parent ABSENT, leaves listed and declared empty: DRIFT", func(t *testing.T) {
		res := cmp(t, []string{leaf}, empty, map[string]any{"other": "x"})
		require.False(t, res.IsEqual, "an absent parent is not an empty one either")
	})

	// THE UNCHANGED DEFAULT, which is what keeps issue 660 closed: a kind that lists nothing behaves
	// exactly as before, so no existing fleet's verdict moves.
	t.Run("nothing listed, remote {}: DRIFT, exactly as before", func(t *testing.T) {
		res := cmp(t, nil, empty, map[string]any{"config": map[string]any{}})
		require.False(t, res.IsEqual, "a kind that lists nothing must be byte-identical to today")
		require.NotNil(t, res.Reason)
		require.Contains(t, res.Reason.Reason, "empty on the remote",
			"and for the right reason: this must still be the GUARD, not some other branch")
	})

	// THE ORIGINAL CONTROL, kept: with a sibling present the guard cannot fire at all, so the leaf rule
	// decides. It agreed before this change and must agree after -- the point of the fix is that these
	// two arms now give the SAME answer, where the vendor's key count used to decide.
	t.Run("CONTROL: a sibling in the remote, so the guard cannot fire: AGREEMENT", func(t *testing.T) {
		res := cmp(t, []string{leaf}, empty, map[string]any{"config": map[string]any{"max-clients": "10"}})
		require.True(t, res.IsEqual,
			"this agreed before the fix; if it now disagrees, the condition broke the leaf rule")
	})
}
