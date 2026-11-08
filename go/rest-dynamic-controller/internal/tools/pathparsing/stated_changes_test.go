package pathparsing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTheFiveStatedBehaviourChanges pins what adopting plumbing/fieldpath changed (#513 step 1).
//
// # Why this REPLACED a differential test rather than joining it
//
// The divergences below were found by a differential test asserting the old local parser and fieldpath
// agreed over a corpus, with every permitted difference enumerated as a row that MUST still differ. That
// instrument found three changes this module's own audit had missed, and then the adoption destroyed it:
// ParsePath now IS fieldpath.Keys(fieldpath.Parse(x)), so its "agree" rows became tautologies and its
// "must differ" rows became guaranteed failures.
//
// A test that cannot fail is worth less than no test, because it reports success. So the net was deleted
// once it had done its job, and what it found is pinned here against the OLD behaviour, recorded in each
// row -- which is a test that can still fail, by someone changing the grammar back.
func TestTheFiveStatedBehaviourChanges(t *testing.T) {
	t.Run("an empty path is an error, not one empty segment", func(t *testing.T) {
		// WAS: ([""], nil). A plausible-but-wrong value: it passed every `len(segs) == 0` guard, so the
		// caller believed it had a path and walked to a property named "". 12 of the 26 call sites already
		// test path == "" before parsing; the other 14 reach this, and 6 of those treat any parse error as
		// "skip this entry", so they are unaffected in practice.
		segs, err := ParsePath("")
		require.Error(t, err, "an empty path must not resolve to a usable-looking value")
		assert.Nil(t, segs)
	})

	t.Run("whitespace is allowed where it is delimited", func(t *testing.T) {
		// WAS: `strings.Contains(path, " ")` rejected the WHOLE path, before parsing, so a quoted key
		// containing a space had no spelling at all.
		for in, want := range map[string][]string{
			"a['my key']":           {"a", "my key"},
			`a[?name=say "hi"]`:     {"a", "?name=say \"hi\""},
			"a[?name=John Smith]":   {"a", "?name=John Smith"},
			"spec['my key'].nested": {"spec", "my key", "nested"},
		} {
			got, err := ParsePath(in)
			require.NoError(t, err, "%q: whitespace inside brackets is delimited and legitimate", in)
			assert.Equal(t, want, got, in)
		}
	})

	t.Run("whitespace in a BARE name is still refused", func(t *testing.T) {
		// The widening must not be wider than stated: a space outside brackets is almost always trailing
		// YAML whitespace or a missing quote, and the rule is "a key with whitespace must be quoted".
		for _, in := range []string{"my key.b", "a. b", "a.b ", "a b"} {
			_, err := ParsePath(in)
			assert.Error(t, err, "%q has whitespace in a bare name and must be refused", in)
		}
	})

	t.Run("a leading dot is stripped, which is a FIX", func(t *testing.T) {
		// WAS: [".id"] -- the dot was KEPT as part of the segment name, so the path named a property
		// literally called ".id". No document has one, so it resolved to nothing, silently, on every
		// reconcile. Paths of this shape exist in the wild because two of our own doc examples disagreed
		// about the dialect.
		got, err := ParsePath(".id")
		require.NoError(t, err)
		assert.Equal(t, []string{"id"}, got,
			"a leading dot must be stripped; keeping it addressed a property that cannot exist")

		got, err = ParsePath(".inputs.correlationId")
		require.NoError(t, err)
		assert.Equal(t, []string{"inputs", "correlationId"}, got)
	})

	t.Run("adjacent brackets are two segments", func(t *testing.T) {
		// WAS: refused -- "adjacent brackets must be separated by dot".
		got, err := ParsePath("a[0][1]")
		require.NoError(t, err)
		assert.Equal(t, []string{"a", "0", "1"}, got)
	})

	t.Run("an explicitly quoted empty key is a key", func(t *testing.T) {
		// WAS: refused -- "empty bracket content". a[] is still refused; a[''] is not, because quoting is
		// how a user says "this is the key I mean".
		got, err := ParsePath("a['']")
		require.NoError(t, err)
		assert.Equal(t, []string{"a", ""}, got)

		_, err = ParsePath("a[]")
		assert.Error(t, err, "an UNquoted empty bracket is still malformed")
	})
}

// TestTheGrammarEveryCallSiteRelliesOnIsUnchanged is the other half of the refactor claim: the inputs that
// must behave exactly as before. These are the shapes RestDefinitions actually contain, so a regression
// here breaks live definitions rather than edge cases.
func TestTheGrammarEveryCallSiteReliesOnIsUnchanged(t *testing.T) {
	for in, want := range map[string][]string{
		"id":                             {"id"},
		"spec.auth.secretRef.name":       {"spec", "auth", "secretRef", "name"},
		"status.metadata.id":             {"status", "metadata", "id"},
		"a['b.c']":                       {"a", "b.c"},
		`a["b.c"]`:                       {"a", "b.c"},
		"spec.credentials[0].secretName": {"spec", "credentials", "0", "secretName"},
		"credentials[?name=admin].value": {"credentials", "?name=admin", "value"},
		"searchCriteria.sourceRefName":   {"searchCriteria", "sourceRefName"},
	} {
		got, err := ParsePath(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, "%q is a shape live RestDefinitions contain", in)
	}
	for _, in := range []string{"a..b", "trailing.", "a['unterminated", "a[0"} {
		_, err := ParsePath(in)
		assert.Error(t, err, "%q was malformed before and must stay malformed", in)
	}
}
