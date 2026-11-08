package pathparsing

import (
	"testing"

	"github.com/krateo-platformops/plumbing/fieldpath"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTheLocalParserAndFieldpathAgreeExceptWhereStated is the safety net for #513's step 1.
//
// RULE: checks on the call sites I edit pass by construction. The damage in a 26-site refactor is in the
// inputs I did not think to change. So this asserts agreement between the parser being REPLACED and the one
// replacing it, over a corpus, and enumerates every input where they are allowed to differ.
//
// A row in `divergent` is a promise: the adoption PR must state that behaviour change. A row that drifts
// out of `agree` without being added to `divergent` fails here rather than in a consumer's cluster.
func TestTheLocalParserAndFieldpathAgreeExceptWhereStated(t *testing.T) {
	// Inputs where the two MUST produce the same segments. Keys only -- fieldpath's Keys() is documented
	// as the lossy flat form precisely so this comparison is possible.
	agree := []string{
		"a", "a.b.c", "spec.auth.secretRef.name", "a['b.c']", `a["b.c"]`, "a['b.c'].d",
		"['a..b']", "a[0]", "a['0']", "a.0", "credentials[0]", "spec.credentials[0].secretName",
		"spec.items[12].key", "a['x'].y[3].z", "[0]",
		"a[?key=value]", "credentials[?name=admin].value", "a[?k=]", "a.[?k=v]", "a['?k=v']",
	}
	for _, in := range agree {
		t.Run("agree/"+in, func(t *testing.T) {
			oldSegs, oldErr := ParsePath(in)
			newSegs, newErr := fieldpath.Parse(in)

			require.Equal(t, oldErr == nil, newErr == nil,
				"one parser accepted %q and the other refused it: old=%v new=%v", in, oldErr, newErr)
			if oldErr != nil {
				return
			}
			assert.Equal(t, oldSegs, fieldpath.Keys(newSegs),
				"%q must flatten identically, or the refactor is not a refactor at this input", in)
		})
	}

	// Inputs where they are ALLOWED to differ. Each is a stated behaviour change in the adoption PR.
	for _, tc := range []struct {
		in  string
		why string
	}{
		{in: "", why: "the local parser returns ([\"\"], nil) -- a plausible-but-wrong value that passes " +
			"every len(segs)==0 guard and walks to a property named \"\". fieldpath errors. Callers that " +
			"can receive an unset optional field must test path == \"\" BEFORE parsing"},
		{in: "a['my key']", why: "the local parser rejects ANY path containing a space, globally, before " +
			"parsing. fieldpath allows whitespace inside brackets, where it is delimited, and refuses it " +
			"in a bare name"},
		{in: `a[?name=say "hi"]`, why: "same global space ban; a predicate value is delimited"},
		{in: "a[?name=John Smith]", why: "same"},
		{in: ".leadingDot", why: "THE CONSEQUENTIAL ONE. The local parser KEEPS the leading dot as part " +
			"of the segment name -- \".leadingDot\" resolves to a property literally called \".leadingDot\" " +
			"-- while fieldpath strips it. So a path written \".id\" currently looks for a property named " +
			"\".id\", which no document has, and silently resolves to nothing. After adoption it looks for " +
			"\"id\". That is a FIX for the dialect confusion behind a consumer's issue, and it changes what " +
			"an existing path resolves to, so it is a behaviour change and not a refactor. It also means my " +
			"own fieldpath doc had the rationale BACKWARDS: it calls stripping the compatible behaviour, " +
			"when keeping the dot is what this module does today"},
		{in: "a[0][1]", why: "the local parser refuses adjacent brackets (\"must be separated by dot\"); " +
			"fieldpath accepts them as two segments. A widening"},
		{in: "a['']", why: "the local parser refuses an empty bracket; fieldpath accepts it as an " +
			"explicitly-quoted empty key. A widening"},
	} {
		t.Run("divergent/"+tc.in, func(t *testing.T) {
			oldSegs, oldErr := ParsePath(tc.in)
			newSegs, newErr := fieldpath.Parse(tc.in)
			same := (oldErr == nil) == (newErr == nil)
			if same && oldErr == nil {
				same = assert.ObjectsAreEqual(oldSegs, fieldpath.Keys(newSegs))
			}
			assert.False(t, same,
				"%q is listed as DIVERGENT but the two parsers now agree -- remove it from the list and "+
					"from the PR's stated changes, or the PR claims a change it does not make. %s",
				tc.in, tc.why)
		})
	}

	// And the space ban must still apply to BARE names in fieldpath, or the widening is wider than stated.
	for _, in := range []string{"my key.b", "a. b", "a.b "} {
		t.Run("bare-space-still-refused/"+in, func(t *testing.T) {
			_, err := fieldpath.Parse(in)
			assert.Error(t, err, "%q has whitespace in a BARE name; fieldpath must refuse it", in)
		})
	}
}
