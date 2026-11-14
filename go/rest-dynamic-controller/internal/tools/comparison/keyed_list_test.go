//go:build unit

package comparison

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func obj(kv ...interface{}) map[string]interface{} {
	m := map[string]interface{}{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func keyed(path string, keys ...string) *Comparer {
	return NewComparer(nil).WithMapListKeys(map[string][]string{path: keys})
}

// THE HEADLINE, and its control in the same test. A vendor that stores an array of objects as a set
// returns it in an arbitrary order; with the keys declared that is not drift, and WITHOUT them the
// identical data still is. The second half is what makes the first half mean anything -- otherwise a
// comparison that returned equal for everything would pass.
func TestReorderedObjectsAreEqualOnlyWhenTheKeysAreDeclared(t *testing.T) {
	mg := map[string]interface{}{"rules": []interface{}{
		obj("name", "alpha", "action", "allow"),
		obj("name", "beta", "action", "deny"),
	}}
	rm := map[string]interface{}{"rules": []interface{}{
		obj("name", "beta", "action", "deny"),
		obj("name", "alpha", "action", "allow"),
	}}

	res, err := keyed("rules", "name").CompareExisting(mg, rm)
	require.NoError(t, err)
	assert.True(t, res.IsEqual, "declared map-list: a reorder is not drift; got %s", res)

	// CONTROL: the declaration is the only thing that changed the verdict.
	plain, err := NewComparer(nil).CompareExisting(mg, rm)
	require.NoError(t, err)
	assert.False(t, plain.IsEqual,
		"without the declaration the same reorder must still be positional drift, or this feature is "+
			"not what makes the difference")
}

// A real difference inside a matched element is still a difference, and the message names the element
// by its KEYS -- which is the point of matching by key, since an index is meaningless in an array
// whose order is arbitrary.
func TestADifferenceInsideAMatchedElementIsReportedWithItsKeys(t *testing.T) {
	mg := map[string]interface{}{"rules": []interface{}{
		obj("name", "alpha", "action", "allow"),
		obj("name", "beta", "action", "deny"),
	}}
	rm := map[string]interface{}{"rules": []interface{}{
		obj("name", "beta", "action", "LOG"),
		obj("name", "alpha", "action", "allow"),
	}}

	res, err := keyed("rules", "name").CompareExisting(mg, rm)
	require.NoError(t, err)
	require.False(t, res.IsEqual)
	require.NotNil(t, res.Reason)
	assert.Contains(t, res.Reason.Reason, "name=beta",
		"the reason must name the element by key; got %q", res.Reason.Reason)
	// Bracket notation, which is how FieldPath renders an index segment -- the path itself is
	// ["rules","1","action"]. Asserting the RENDERED form on purpose: this string is what reaches the
	// Ready condition and what driftSignature keys on, so a change to it changes the non-convergence
	// budget's behaviour and should fail a test rather than pass quietly.
	assert.Equal(t, "rules[1].action", res.Reason.FieldPath(),
		"the PATH keeps the CR-side index so nested declaration lookups still resolve")
}

// An element the remote does not have at all, at equal length, is drift named by the missing key.
func TestAnElementMissingFromTheRemoteIsNamedByItsKey(t *testing.T) {
	mg := map[string]interface{}{"rules": []interface{}{
		obj("name", "alpha"), obj("name", "beta"),
	}}
	rm := map[string]interface{}{"rules": []interface{}{
		obj("name", "alpha"), obj("name", "gamma"),
	}}

	res, err := keyed("rules", "name").CompareExisting(mg, rm)
	require.NoError(t, err)
	require.False(t, res.IsEqual)
	require.NotNil(t, res.Reason)
	assert.Contains(t, res.Reason.Reason, "no element with name=beta on the remote")
}

// A COMPOSITE KEY MUST BE INJECTIVE. These two elements are different, and a separator-joined tuple
// would make them the same string -- a silent collision that matches one element against the wrong
// counterpart and reports equal.
func TestCompositeKeyTupleDoesNotCollideOnASeparator(t *testing.T) {
	a := obj("ns", "x|y", "name", "z")
	b := obj("ns", "x", "name", "y|z")
	keys := []string{"ns", "name"}

	ta, missA := keyTupleOf(a, keys)
	tb, missB := keyTupleOf(b, keys)
	require.Empty(t, missA)
	require.Empty(t, missB)
	assert.NotEqual(t, ta, tb, "two distinct elements produced the same key tuple")

	// And end to end: the two must not be reported equal to each other.
	mg := map[string]interface{}{"rules": []interface{}{a}}
	rm := map[string]interface{}{"rules": []interface{}{b}}
	res, err := keyed("rules", "ns", "name").CompareExisting(mg, rm)
	require.NoError(t, err)
	assert.False(t, res.IsEqual, "a collided tuple reported two different elements as a match")
}

// A duplicate tuple is a DECLARATION problem and is reported as one, on either side. Picking one of
// two elements with the same identity would make the verdict depend on map iteration order.
func TestDuplicateKeyTuplesAreReportedRatherThanResolved(t *testing.T) {
	for _, tc := range []struct {
		name, wantSide string
		mg, rm         map[string]interface{}
	}{
		{
			name: "remote", wantSide: "the remote has more than one element",
			mg: map[string]interface{}{"rules": []interface{}{obj("name", "a"), obj("name", "b")}},
			rm: map[string]interface{}{"rules": []interface{}{obj("name", "a"), obj("name", "a")}},
		},
		{
			name: "custom resource", wantSide: "the custom resource has more than one element",
			mg: map[string]interface{}{"rules": []interface{}{obj("name", "a"), obj("name", "a")}},
			rm: map[string]interface{}{"rules": []interface{}{obj("name", "a"), obj("name", "b")}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := keyed("rules", "name").CompareExisting(tc.mg, tc.rm)
			require.NoError(t, err, "a declaration problem is a reported difference, not a Go error")
			require.False(t, res.IsEqual)
			require.NotNil(t, res.Reason)
			assert.Contains(t, res.Reason.Reason, tc.wantSide)
			assert.Contains(t, res.Reason.Reason, "do not identify elements")
		})
	}
}

// An element with no usable value for a declared key is reported distinctly from a difference, so the
// author is told the declaration cannot identify the element rather than hunting a field change.
func TestAnUnusableKeyValueIsReportedAsSuch(t *testing.T) {
	for _, tc := range []struct {
		name string
		elem map[string]interface{}
	}{
		{"absent", obj("action", "allow")},
		{"nil", obj("name", nil, "action", "allow")},
		{"object", obj("name", obj("nested", "x"), "action", "allow")},
		{"array", obj("name", []interface{}{"x"}, "action", "allow")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mg := map[string]interface{}{"rules": []interface{}{tc.elem}}
			rm := map[string]interface{}{"rules": []interface{}{obj("name", "alpha", "action", "allow")}}
			res, err := keyed("rules", "name").CompareExisting(mg, rm)
			require.NoError(t, err)
			require.False(t, res.IsEqual)
			require.NotNil(t, res.Reason)
			assert.Contains(t, res.Reason.Reason, `no usable value for declared list key "name"`)
		})
	}
}

// THE REGRESSION TEST FOR THE RECEIVER. A set declared INSIDE a keyed element must keep its set
// semantics. The first version of the arm recursed through the package-level CompareExisting, which
// builds a zero-value Comparer -- so every nested declaration was silently dropped and a reordered
// nested set became permanent drift, which is the bug this whole feature exists to remove.
func TestANestedSetInsideAKeyedElementKeepsItsSemantics(t *testing.T) {
	c := NewComparer([]string{"rules.types"}).
		WithMapListKeys(map[string][]string{"rules": {"name"}})

	mg := map[string]interface{}{"rules": []interface{}{
		obj("name", "alpha", "types", []interface{}{"x", "y"}),
		obj("name", "beta", "types", []interface{}{"p", "q"}),
	}}
	rm := map[string]interface{}{"rules": []interface{}{
		obj("name", "beta", "types", []interface{}{"q", "p"}),
		obj("name", "alpha", "types", []interface{}{"y", "x"}),
	}}

	res, err := c.CompareExisting(mg, rm)
	require.NoError(t, err)
	assert.True(t, res.IsEqual,
		"a declared set nested inside a keyed element lost its semantics; got %s", res)

	// CONTROL: the nested set declaration is load-bearing. Drop it and the reordered inner arrays are
	// positional drift again, which proves the assertion above is not passing for some other reason.
	withoutNested := NewComparer(nil).WithMapListKeys(map[string][]string{"rules": {"name"}})
	plain, err := withoutNested.CompareExisting(mg, rm)
	require.NoError(t, err)
	assert.False(t, plain.IsEqual,
		"without the nested set declaration the inner reorder must still be drift")
}

// When the data disagrees with the declaration the arm does not apply, and the comparison falls back
// to the positional walk rather than guessing -- the same choice the set arm makes for objects where
// scalars were declared.
func TestScalarDataUnderAMapListDeclarationFallsBackToPositional(t *testing.T) {
	mg := map[string]interface{}{"rules": []interface{}{"a", "b"}}
	rm := map[string]interface{}{"rules": []interface{}{"b", "a"}}

	res, err := keyed("rules", "name").CompareExisting(mg, rm)
	require.NoError(t, err)
	assert.False(t, res.IsEqual, "scalars cannot be matched by key, so this must stay positional")
}

// Lengths are still checked first, and that rule is untouched: a map-list that gained an element
// upstream is a real difference, consistent with the set arm and with compareSlices before it.
func TestLengthsStillDifferBeforeAnyKeyMatching(t *testing.T) {
	mg := map[string]interface{}{"rules": []interface{}{obj("name", "a")}}
	rm := map[string]interface{}{"rules": []interface{}{obj("name", "a"), obj("name", "b")}}

	res, err := keyed("rules", "name").CompareExisting(mg, rm)
	require.NoError(t, err)
	require.False(t, res.IsEqual)
	require.NotNil(t, res.Reason)
	assert.Equal(t, "slice lengths differ", res.Reason.Reason)
}

// A map-list nested inside an array of objects resolves, because the declaration path carries no
// index and the runtime path's indices are stripped before lookup.
func TestAKeyedListNestedInsideAnArrayOfObjectsResolves(t *testing.T) {
	c := NewComparer(nil).WithMapListKeys(map[string][]string{"groups.rules": {"name"}})

	mg := map[string]interface{}{"groups": []interface{}{
		obj("id", "g1", "rules", []interface{}{obj("name", "a"), obj("name", "b")}),
	}}
	rm := map[string]interface{}{"groups": []interface{}{
		obj("id", "g1", "rules", []interface{}{obj("name", "b"), obj("name", "a")}),
	}}

	res, err := c.CompareExisting(mg, rm)
	require.NoError(t, err)
	assert.True(t, res.IsEqual, "nested keyed list did not resolve; got %s", res)
}

// WithMapListKeys must be a no-op for empty input, so a Comparer built without declarations behaves
// exactly as it did before #650.
func TestWithMapListKeysIsANoOpWhenEmpty(t *testing.T) {
	assert.Nil(t, NewComparer(nil).WithMapListKeys(nil).mapListKeys)
	assert.Nil(t, NewComparer(nil).WithMapListKeys(map[string][]string{}).mapListKeys)
	assert.Nil(t, NewComparer(nil).WithMapListKeys(map[string][]string{"": {"name"}}).mapListKeys,
		"an empty path is not a declaration")
	assert.Nil(t, NewComparer(nil).WithMapListKeys(map[string][]string{"rules": {}}).mapListKeys,
		"a path with no key names is not a declaration")
}
