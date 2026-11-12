//go:build unit || integration

package comparison

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WHAT THIS PINS
//
// Keycloak stores a realm's `enabledEventTypes` as a Java Set and returns its members in hash order. The
// comparison walked both slices by index, so 23 unchanged names were reported as drift on every resync,
// the controller PUT the realm, and the resource never converged -- Ready=False/Updating forever, one
// PUT every three minutes. Measured by keycloak-provider-kog on Keycloak 26.6.3 (#617).
//
// With TWO elements the order happened to match, which is why nobody found this earlier. A table of
// small arrays cannot see it; the 23-element row below is the real shape.
//
// THE SCOPE IS THE POINT. Only an array the OAS declares `uniqueItems: true` with scalar items becomes
// order-insensitive. Every other array stays positional, and a comparer built with no paths is
// byte-for-byte the historical behaviour -- which the rows below assert directly rather than leaving to
// the 26 pre-existing call sites to imply.

// the 23 names from the consumer's realm, in two orders that differ
var realmEventTypes = []interface{}{
	"SEND_RESET_PASSWORD", "UPDATE_CONSENT_ERROR", "GRANT_CONSENT", "REMOVE_TOTP", "REVOKE_GRANT",
	"LOGIN_ERROR", "CLIENT_LOGIN", "RESET_PASSWORD_ERROR", "IMPERSONATE_ERROR", "CODE_TO_TOKEN_ERROR",
	"CUSTOM_REQUIRED_ACTION", "RESTART_AUTHENTICATION", "IMPERSONATE", "UPDATE_PROFILE_ERROR",
	"LOGIN", "UPDATE_PASSWORD_ERROR", "CLIENT_INITIATED_ACCOUNT_LINKING", "TOKEN_EXCHANGE",
	"AUTHREQID_TO_TOKEN", "LOGOUT", "REGISTER", "DELETE_ACCOUNT", "CLIENT_REGISTER",
}

func reversed(in []interface{}) []interface{} {
	out := make([]interface{}, len(in))
	for i := range in {
		out[i] = in[len(in)-1-i]
	}
	return out
}

func TestSetTypedArraysAreComparedAsMultisets(t *testing.T) {
	cases := []struct {
		name     string
		setPaths []string
		mg       map[string]interface{}
		rm       map[string]interface{}
		wantEq   bool
		why      string
	}{
		{
			// THE CONSUMER CASE, at the size that exposed it.
			name:     "a 23-element set, reordered, is NOT drift",
			setPaths: []string{"enabledEventTypes"},
			mg:       map[string]interface{}{"enabledEventTypes": realmEventTypes},
			rm:       map[string]interface{}{"enabledEventTypes": reversed(realmEventTypes)},
			wantEq:   true,
			why: "Keycloak returns a Set in hash order; reporting that as drift re-PUTs the realm every " +
				"three minutes and the resource never goes Ready",
		},
		{
			// THE CONTROL FOR THE WHOLE CHANGE. If this ever passes as equal, the fix has become global
			// and every ordered array in every consumer silently stopped detecting reordering.
			name:     "a POSITIONAL array, reordered, IS drift",
			setPaths: nil,
			mg:       map[string]interface{}{"steps": []interface{}{"a", "b", "c"}},
			rm:       map[string]interface{}{"steps": []interface{}{"c", "b", "a"}},
			wantEq:   false,
			why: "order is meaningful in an ordinary array -- a JSON Patch sequence, a priority list -- " +
				"and nothing in the document said this one was a set",
		},
		{
			name:     "a path that is a set for ANOTHER field does not make this one a set",
			setPaths: []string{"enabledEventTypes"},
			mg:       map[string]interface{}{"steps": []interface{}{"a", "b"}},
			rm:       map[string]interface{}{"steps": []interface{}{"b", "a"}},
			wantEq:   false,
			why:      "the lookup must be per-path, not a global flag once any set exists",
		},
		{
			name:     "a set with a member missing is drift",
			setPaths: []string{"types"},
			mg:       map[string]interface{}{"types": []interface{}{"a", "b", "c"}},
			rm:       map[string]interface{}{"types": []interface{}{"a", "b"}},
			wantEq:   false,
			why:      "lengths differ, so this is caught before the multiset walk -- asserted so it stays caught",
		},
		{
			name:     "a set with an extra member is drift",
			setPaths: []string{"types"},
			mg:       map[string]interface{}{"types": []interface{}{"a", "b"}},
			rm:       map[string]interface{}{"types": []interface{}{"a", "b", "c"}},
			wantEq:   false,
			why:      "the remote has something the CR does not declare",
		},
		{
			// THE ROW THAT KILLS A LENGTH-PLUS-CONTAINS IMPLEMENTATION. Same length, same member set,
			// different multiplicities. A containment check calls these equal.
			name:     "same length and same members but different multiplicities is drift",
			setPaths: []string{"types"},
			mg:       map[string]interface{}{"types": []interface{}{"a", "a", "b"}},
			rm:       map[string]interface{}{"types": []interface{}{"a", "b", "b"}},
			wantEq:   false,
			why: "each remote element may be consumed by at most one match; a containment check would " +
				"report these as equal and would be the obvious wrong implementation",
		},
		{
			name:     "a set with a genuinely different member is drift",
			setPaths: []string{"types"},
			mg:       map[string]interface{}{"types": []interface{}{"a", "b", "c"}},
			rm:       map[string]interface{}{"types": []interface{}{"a", "b", "d"}},
			wantEq:   false,
			why:      "order-insensitive is not value-insensitive",
		},
		{
			// A SET NESTED INSIDE AN ARRAY OF OBJECTS. The runtime path carries an element index
			// (`rules.0.types`) that the schema-derived path never has (`rules.types`). If the index is
			// not stripped before the lookup, this row reports drift and the fix silently does nothing.
			name:     "a set nested inside an array of objects is still a set",
			setPaths: []string{"rules.types"},
			mg: map[string]interface{}{"rules": []interface{}{
				map[string]interface{}{"types": []interface{}{"x", "y", "z"}},
			}},
			rm: map[string]interface{}{"rules": []interface{}{
				map[string]interface{}{"types": []interface{}{"z", "x", "y"}},
			}},
			wantEq: true,
			why:    "the element index must be stripped from the runtime path before the schema lookup",
		},
		{
			// THE SCALAR-ONLY SCOPE, made testable. A declared set whose elements turn out to be objects
			// falls back to positional rather than guessing how to match two objects.
			name:     "a declared set whose elements are OBJECTS falls back to positional",
			setPaths: []string{"rules"},
			mg: map[string]interface{}{"rules": []interface{}{
				map[string]interface{}{"id": "1"}, map[string]interface{}{"id": "2"},
			}},
			rm: map[string]interface{}{"rules": []interface{}{
				map[string]interface{}{"id": "2"}, map[string]interface{}{"id": "1"},
			}},
			wantEq: false,
			why: "matching objects needs a key, which is identifiersMatchPolicy's question; a second " +
				"answer invented here would be worse than the ordering bug",
		},
		{
			name:     "no set paths at all is the historical behaviour",
			setPaths: nil,
			mg:       map[string]interface{}{"types": []interface{}{"a", "b"}},
			rm:       map[string]interface{}{"types": []interface{}{"b", "a"}},
			wantEq:   false,
			why: "a document that could not be read, or has no sets, must leave the comparison exactly " +
				"as it was -- the fix must not change semantics when its input is missing",
		},
		{
			// Numbers arrive from two different decoders: a JSON number from the vendor and an int64
			// from the unstructured CR. CompareAny normalises that everywhere else in this package and
			// the multiset walk has to use it too, or a reordered set of integers reports drift.
			name:     "a reordered set of numbers across decoder types is NOT drift",
			setPaths: []string{"ports"},
			mg:       map[string]interface{}{"ports": []interface{}{int64(80), int64(443), int64(8080)}},
			rm:       map[string]interface{}{"ports": []interface{}{float64(8080), float64(80), float64(443)}},
			wantEq:   true,
			why:      "== would make these differ; the rest of this package compares values, not dynamic types",
		},
	}

	// The table must contain both outcomes, asserted rather than assumed: all-equal rows pass against a
	// comparer that always reports equality, which is a far worse bug than the one being fixed.
	var eq, neq int
	for _, tc := range cases {
		if tc.wantEq {
			eq++
		} else {
			neq++
		}
	}
	require.NotZero(t, eq, "no row expects equality: this table passes against a comparer that never converges")
	require.NotZero(t, neq, "no row expects drift: this table passes against a comparer that reports everything equal")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := NewComparer(tc.setPaths).CompareExisting(tc.mg, tc.rm)
			require.NoError(t, err)
			assert.Equal(t, tc.wantEq, res.IsEqual, "%s\n  mg=%v\n  rm=%v\n  reason=%v",
				tc.why, tc.mg, tc.rm, res.Reason)
		})
	}
}

// THE PACKAGE-LEVEL ENTRY POINT MUST NOT HAVE CHANGED.
//
// 26 call sites across this repo use CompareExisting, and every one of them must keep comparing
// positionally. Asserted here directly: the same inputs through the old function and through a comparer
// built with the relevant path must disagree, which is the only way to show the opt-in is really an
// opt-in rather than a new default that happens to be off in the cases the other tests cover.
func TestTheOptInIsReallyAnOptIn(t *testing.T) {
	mg := map[string]interface{}{"types": []interface{}{"a", "b", "c"}}
	rm := map[string]interface{}{"types": []interface{}{"c", "a", "b"}}

	old, err := CompareExisting(mg, rm)
	require.NoError(t, err)
	optedIn, err := NewComparer([]string{"types"}).CompareExisting(mg, rm)
	require.NoError(t, err)

	assert.False(t, old.IsEqual,
		"the package-level CompareExisting became order-insensitive. Every existing caller would stop "+
			"detecting a reordered array, and none of them asked for that")
	assert.True(t, optedIn.IsEqual, "the opt-in comparer must treat the declared set as a set")
	assert.NotEqual(t, old.IsEqual, optedIn.IsEqual,
		"the two comparers agree on an input designed to separate them, so either the opt-in does "+
			"nothing or it has become the default")
}

// isSetPath's index stripping, asserted on its own.
//
// The nested row in the table above covers it end to end, but a single failing row there could be read as
// a problem with nesting rather than with path normalisation. This says which it is.
func TestIsSetPathStripsArrayIndices(t *testing.T) {
	c := NewComparer([]string{"rules.types", "enabledEventTypes"})

	for _, tc := range []struct {
		path []string
		want bool
	}{
		{[]string{"enabledEventTypes"}, true},
		{[]string{"rules", "types"}, true},
		{[]string{"rules", "0", "types"}, true},
		{[]string{"rules", "17", "types"}, true},
		{[]string{"rules", "types", "0"}, true}, // the array itself, reached through an index
		{[]string{"rules"}, false},
		{[]string{"types"}, false},
		{[]string{"rules", "notanindex", "types"}, false},
		{nil, false},
	} {
		t.Run(fmt.Sprintf("%v", tc.path), func(t *testing.T) {
			assert.Equal(t, tc.want, c.isSetPath(tc.path),
				"a numeric segment is an array index and is not part of the schema path; a non-numeric "+
					"one is a real field name and must not be dropped")
		})
	}

	assert.False(t, (&Comparer{}).isSetPath([]string{"enabledEventTypes"}),
		"the zero value must declare nothing a set")
}
