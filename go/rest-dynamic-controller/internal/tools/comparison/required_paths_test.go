//go:build unit || integration

package comparison

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keycloakShape reproduces the measured case: the CR declares two config keys, the vendor holds one.
//
// This is not a synthetic example. keycloak-provider-kog measured it on Keycloak 26.6.3: a
// KeycloakClientRegistrationPolicy whose spec.config carried allow-default-scopes and
// allowed-client-scopes, against a vendor object holding only allow-default-scopes. The CR read "up
// to date" and Ready=True, no update was ever sent, and the policy that should have restricted agents
// to the `mcp` scope was not in force.
func keycloakShape() (cr, vendor map[string]any) {
	cr = map[string]any{
		"config": map[string]any{
			"allow-default-scopes":  []any{"true"},
			"allowed-client-scopes": []any{"mcp"},
		},
	}
	vendor = map[string]any{
		"config": map[string]any{
			"allow-default-scopes": []any{"true"},
		},
	}
	return cr, vendor
}

// TestAbsentKeyIsSkippedUnlessDeclared is the pair that defines the feature: the default is unchanged,
// and the declaration is what turns an absence into drift.
func TestAbsentKeyIsSkippedUnlessDeclared(t *testing.T) {
	cr, vendor := keycloakShape()

	t.Run("undeclared: absence is NOT drift, which is the unchanged default", func(t *testing.T) {
		res, err := NewComparer(nil).CompareExisting(cr, vendor)
		require.NoError(t, err)
		assert.True(t, res.IsEqual,
			"the absent-key default must be untouched for kinds that declare nothing, or every vendor "+
				"that omits a write-only field would update on every resync forever")
	})

	t.Run("declared: absence IS drift, and names the path", func(t *testing.T) {
		res, err := NewComparer(nil).
			WithRequiredPaths([]string{"config.allowed-client-scopes"}).
			CompareExisting(cr, vendor)
		require.NoError(t, err)
		require.False(t, res.IsEqual, "a declared-but-absent path must be reported as drift")
		require.NotNil(t, res.Reason, "drift with no Reason gives an operator nothing to act on")
		assert.Equal(t, "config.allowed-client-scopes", res.Reason.FieldPath(),
			"the reason must name the declared path, not its parent")
		assert.Contains(t, res.Reason.Reason, "required-present")
	})

	// SETUP CONTROL: declaring a DIFFERENT path must not make this pair unequal. Without it, the arm
	// above could pass because the comparer had become indiscriminate rather than because the
	// declaration was consulted.
	t.Run("declaring an unrelated path changes nothing", func(t *testing.T) {
		res, err := NewComparer(nil).
			WithRequiredPaths([]string{"config.some-other-key"}).
			CompareExisting(cr, vendor)
		require.NoError(t, err)
		assert.True(t, res.IsEqual,
			"a declaration for a path the CR does not carry must not report drift")
	})

	// AND THE PRESENT CASE MUST STILL BE EQUAL. A declaration is about absence, not about forcing a
	// comparison to fail whenever the path appears.
	t.Run("declared and PRESENT with an equal value is still equal", func(t *testing.T) {
		present := map[string]any{
			"config": map[string]any{
				"allow-default-scopes":  []any{"true"},
				"allowed-client-scopes": []any{"mcp"},
			},
		}
		res, err := NewComparer(nil).
			WithRequiredPaths([]string{"config.allowed-client-scopes"}).
			CompareExisting(cr, present)
		require.NoError(t, err)
		assert.True(t, res.IsEqual, "a declared path that is present and equal must not be drift")
	})
}

// TestRequiredPathsMatchingRules pins the matching convention, which is inherited from
// isStrippedPath rather than invented: normalisePath drops numeric indices, so a declaration is
// index-insensitive.
func TestRequiredPathsMatchingRules(t *testing.T) {
	// THE CONTAINING OBJECT MUST BE NON-EMPTY, and the first version of this arm was vacuous for
	// missing that. With the remote element as `{}`, a pre-existing rule already reports drift --
	// "object is set in the custom resource but empty on the remote", at path items[0] -- so the arm
	// passed identically with and without the declaration. MEASURED both ways before rewriting it.
	//
	// That narrows the feature usefully: a declaration only decides anything when the containing
	// object EXISTS with other keys and just this one is missing. Which is exactly the measured
	// keycloak shape, where config carries allow-default-scopes and lacks allowed-client-scopes.
	t.Run("an index in the live path still matches an index-free declaration", func(t *testing.T) {
		cr := map[string]any{"items": []any{map[string]any{"sibling": "s", "wanted": "x"}}}
		vendor := map[string]any{"items": []any{map[string]any{"sibling": "s"}}}

		// CONTROL FIRST: without the declaration this pair is EQUAL, so the arm below is about the
		// declaration and not about some other rule finding the difference.
		undeclared, err := NewComparer(nil).CompareExisting(cr, vendor)
		require.NoError(t, err)
		require.True(t, undeclared.IsEqual,
			"the control failed: something other than the declaration reports this, so the arm would "+
				"prove nothing (got %q at %q)", reasonText(undeclared), reasonPath(undeclared))

		res, err := NewComparer(nil).WithRequiredPaths([]string{"items.wanted"}).CompareExisting(cr, vendor)
		require.NoError(t, err)
		require.False(t, res.IsEqual, "a declared nested absence under an array element must be drift")
		assert.Equal(t, "items[0].wanted", res.Reason.FieldPath(),
			"the REASON keeps the index even though the DECLARATION does not: the operator needs the "+
				"element, the author should not have to enumerate them")
	})

	t.Run("an empty declaration list leaves the comparer unchanged", func(t *testing.T) {
		cr, vendor := keycloakShape()
		res, err := NewComparer(nil).WithRequiredPaths([]string{}).CompareExisting(cr, vendor)
		require.NoError(t, err)
		assert.True(t, res.IsEqual)
	})

	t.Run("empty strings in the list are ignored, not treated as a path", func(t *testing.T) {
		cr, vendor := keycloakShape()
		res, err := NewComparer(nil).WithRequiredPaths([]string{"", ""}).CompareExisting(cr, vendor)
		require.NoError(t, err)
		assert.True(t, res.IsEqual,
			"a list of empty strings must behave as no declaration, or a mis-rendered chart value "+
				"would make every absence drift")
	})

	// POSITIVE CONTROL for the helper itself: a declaration that SHOULD match does, through the same
	// normalisePath route, so the negative arms above are about the path and not about the map being
	// unreachable.
	t.Run("the declaration really is consulted", func(t *testing.T) {
		c := NewComparer(nil).WithRequiredPaths([]string{"config.allowed-client-scopes"})
		assert.True(t, c.isRequiredPath([]string{"config", "allowed-client-scopes"}))
		assert.False(t, c.isRequiredPath([]string{"config", "allow-default-scopes"}))
		assert.True(t, c.isRequiredPath([]string{"config", "0", "allowed-client-scopes"}),
			"normalisePath drops numeric segments, so an indexed live path still matches")
	})
}

// reasonText and reasonPath keep the control's failure message readable without a nil check at each
// use. A control whose own message panics is worse than no control.
func reasonText(r ComparisonResult) string {
	if r.Reason == nil {
		return ""
	}
	return r.Reason.Reason
}

func reasonPath(r ComparisonResult) string {
	if r.Reason == nil {
		return ""
	}
	return r.Reason.FieldPath()
}

// TestDeclaredEmptyValueIsSatisfiedByAbsence covers the half of issue 886 that the first cut got
// wrong: a declared EMPTY value and an absent observed key are the same state, so listing the path
// must not report drift.
//
// MEASURED per key on Keycloak 26.6.3 by keycloak-provider-kog, after I asked whether any of the eight
// registration-policy config keys can legitimately be empty. Five can: an empty list is accepted with
// 204 and the GET afterwards does not carry the key at all. Three reject it with 400 "is required".
//
// AND THE REMEDIES I HAD PROPOSED WERE BOTH WRONG, which is why this is a code condition and not a
// paragraph of guidance. "Refuse the empty value at admission" would have made a restriction
// impossible to UNDO, because omitting a key from that vendor's component PUT keeps the previous
// value, so a declared empty list is the only way to clear one. "Do not list the path" fails because
// the same key needs both behaviours: non-empty declared plus absent is drift, empty declared plus
// absent is agreement.
func TestDeclaredEmptyValueIsSatisfiedByAbsence(t *testing.T) {
	const path = "config.allowed-client-scopes"

	// absentVendor holds the sibling key only, so every arm below differs from its CR by exactly the
	// declared path and nothing else.
	absentVendor := func() map[string]any {
		return map[string]any{"config": map[string]any{"allow-default-scopes": []any{"true"}}}
	}
	crWith := func(v any) map[string]any {
		return map[string]any{"config": map[string]any{
			"allow-default-scopes":  []any{"true"},
			"allowed-client-scopes": v,
		}}
	}
	compare := func(t *testing.T, cr, vendor map[string]any) ComparisonResult {
		t.Helper()
		res, err := NewComparer(nil).WithRequiredPaths([]string{path}).CompareExisting(cr, vendor)
		require.NoError(t, err)
		return res
	}

	t.Run("declared empty list, absent from the vendor: AGREEMENT", func(t *testing.T) {
		res := compare(t, crWith([]any{}), absentVendor())
		assert.True(t, res.IsEqual,
			"a vendor that normalises an empty collection away is behaving correctly, and reporting "+
				"that as drift would update on every resync forever -- the failure the absent-key "+
				"default exists to prevent, arriving through the opt-in")
	})

	// SUBJECT CONTROL: the same declaration, non-empty. Without this arm the one above could pass
	// because the condition had disabled the feature outright rather than narrowed it.
	t.Run("declared NON-empty, absent from the vendor: still drift", func(t *testing.T) {
		res := compare(t, crWith([]any{"mcp"}), absentVendor())
		require.False(t, res.IsEqual, "narrowing the exception must not switch it off")
		require.NotNil(t, res.Reason)
		assert.Equal(t, path, res.Reason.FieldPath())
	})

	t.Run("declared empty, vendor NON-empty: drift, by ordinary comparison", func(t *testing.T) {
		vendor := map[string]any{"config": map[string]any{
			"allow-default-scopes":  []any{"true"},
			"allowed-client-scopes": []any{"mcp"},
		}}
		res := compare(t, crWith([]any{}), vendor)
		assert.False(t, res.IsEqual,
			"clearing a restriction the vendor still holds is a real difference; the key is PRESENT, "+
				"so this never reaches the absent-key branch at all")
	})

	t.Run("empty string and empty map are empty too", func(t *testing.T) {
		for name, v := range map[string]any{"empty string": "", "empty map": map[string]any{}} {
			res := compare(t, crWith(v), absentVendor())
			assert.True(t, res.IsEqual, "%s declared, key absent: same state", name)
		}
	})

	// ZERO AND FALSE ARE NOT EMPTY. A vendor silently dropping these is exactly what issue 886 exists
	// to catch, so they must keep reporting drift. This is the arm that a reflect-based or
	// truthiness-based emptiness test would fail.
	t.Run("false and zero are NOT empty: absence is still drift", func(t *testing.T) {
		for name, v := range map[string]any{"false": false, "zero": int64(0), "zero float": 0.0} {
			res := compare(t, crWith(v), absentVendor())
			assert.False(t, res.IsEqual,
				"%s is a meaningful declared value; a vendor dropping it is the silent-drop bug", name)
		}
	})
}
