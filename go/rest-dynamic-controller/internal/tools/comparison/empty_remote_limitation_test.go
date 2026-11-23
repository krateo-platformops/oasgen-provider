//go:build unit || integration

package comparison

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEmptyRemoteGuardPreemptsDeclaredEmpty pins a KNOWN LIMITATION, measured, not a desired
// behaviour: the empty-remote guard returns drift before the absent-key branch can decide that a
// declared empty value is satisfied by absence.
//
// THIS ASSERTION IS EXPECTED TO INVERT. When the guard learns about declared-empty values, the first
// require below must become True and this comment must go. It is written as an assertion rather than
// left out so that the fix cannot land silently: a test that merely logged the current answer would
// pass either way and tell the next reader nothing.
//
// WHY IT IS A SEPARATE CHANGE. Line 399 is not gated on requirePresentFields, so moving it changes the
// default comparison path for every kind in every fleet -- unlike the declared-empty condition in the
// absent-key branch, which only affects kinds that opted in. The last change at this layer had a
// 169-path consumer census behind it.
//
// MEASURED with its own control, both in this test: with a sibling key present in the remote the
// guard cannot fire and the declared-empty condition does work, which is what isolates the guard as
// the cause rather than the condition being broken.
func TestEmptyRemoteGuardPreemptsDeclaredEmpty(t *testing.T) {
	cr := map[string]any{"config": map[string]any{"allowed-client-scopes": []any{}}}

	t.Run("remote sub-object entirely empty: the guard preempts, so still drift", func(t *testing.T) {
		res, err := NewComparer(nil).
			WithRequiredPaths([]string{"config.allowed-client-scopes"}).
			CompareExisting(cr, map[string]any{"config": map[string]any{}})
		require.NoError(t, err)
		require.False(t, res.IsEqual,
			"known limitation: when this starts returning equal, the guard has been fixed -- invert "+
				"this assertion and delete the limitation note above")
		require.NotNil(t, res.Reason)
		require.Contains(t, res.Reason.Reason, "empty on the remote",
			"and the reason must still be the GUARD's, not the absent-key branch's: if this changes "+
				"the preemption moved rather than went away")
	})

	// CONTROL: one sibling key in the remote means len(rm) != 0, so the guard cannot fire and the
	// declared-empty condition decides. Without this arm the one above would not show that the guard
	// is the cause.
	t.Run("CONTROL: a sibling key in the remote, so the guard cannot fire", func(t *testing.T) {
		res, err := NewComparer(nil).
			WithRequiredPaths([]string{"config.allowed-client-scopes"}).
			CompareExisting(cr, map[string]any{"config": map[string]any{"max-clients": "10"}})
		require.NoError(t, err)
		require.True(t, res.IsEqual,
			"declared-empty must be satisfied by absence wherever the guard is not in the way; if "+
				"this fails the condition itself is broken and the arm above means nothing")
	})
}
