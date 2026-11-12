//go:build unit || integration

package restclient

import (
	"os"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A REAL THIRD-PARTY DOCUMENT, not one written alongside the walker.
//
// The other fixture in this package and the walker it exercises were written by the same hand in the
// same hour, so a green result there shows only that they agree with each other. This document was
// written to describe Keycloak's Admin API by the consumer whose realm the #617 defect was found on, and
// it carries the two `uniqueItems` declarations that make the fix reachable at all.
//
// Source and provenance are in the fixture's own header. The only modification is servers[0].url, which
// was a Helm placeholder.
//
// WHAT THIS DOCUMENT CAN AND CANNOT TEST, stated because the gap is easy to assume away:
//
//   - It tests the POSITIVE case and four negatives: RealmRepresentation declares seven arrays, two with
//     `uniqueItems` and five without, so "emits the sets" and "does not emit an ordinary array" are both
//     exercised on text nobody wrote for this test.
//   - It does NOT test the object-items exclusion. The document's one array of objects
//     (`items: $ref RealmRepresentation`, in listRealm's RESPONSE) is not in any request body, so the
//     walker never reaches it. That cell stays covered by the synthetic fixture.
//     The consumer stated there were no arrays of objects at all; that is true of RealmRepresentation
//     and not of the document, and the distinction does not change the conclusion.
//   - It does NOT exercise allOf flattening, which the document does not use.
func TestSetTypedArrayPathsOnARealKeycloakDocument(t *testing.T) {
	b, err := os.ReadFile("testdata/keycloak-realm.yaml")
	require.NoError(t, err)
	client := clientFromSpec(t, string(b))

	// The UPDATE verb, which is the operation production code asks (restResources.go, same reason as
	// compareScope=updatable in #220).
	got, err := client.SetTypedArrayPaths("PUT", "/admin/realms/{realm}")
	require.NoError(t, err)
	sort.Strings(got)

	assert.Equal(t, []string{"enabledEventTypes", "eventsListeners"}, got,
		"these two are the only uniqueItems declarations in the document, and they are the fields whose "+
			"reordering by Keycloak re-PUT a consumer's realm every three minutes (#617)")

	// The same answer from the create verb, since set-ness is a property of the schema rather than of
	// the operation. If these ever diverge, the walker is reading something operation-specific.
	fromCreate, err := client.SetTypedArrayPaths("POST", "/admin/realms")
	require.NoError(t, err)
	sort.Strings(fromCreate)
	assert.Equal(t, got, fromCreate,
		"createRealm and updateRealm share RealmRepresentation, so the set paths must be identical")
}

// The five ordinary arrays must NOT be emitted, asserted by name rather than by the count above.
//
// The equality assertion already excludes them, but it would fail identically if the walker emitted
// nothing at all. This says which arrays were correctly left out, so a regression names the field.
func TestTheRealDocumentsOrdinaryArraysAreNotSets(t *testing.T) {
	b, err := os.ReadFile("testdata/keycloak-realm.yaml")
	require.NoError(t, err)
	client := clientFromSpec(t, string(b))

	got, err := client.SetTypedArrayPaths("PUT", "/admin/realms/{realm}")
	require.NoError(t, err)
	in := func(p string) bool {
		for _, g := range got {
			if g == p {
				return true
			}
		}
		return false
	}

	// Arrays of strings with no uniqueItems. Order is meaningful in some of these (signature algorithm
	// preference lists), which is exactly why the walker must not widen.
	for _, ordinary := range []string{
		"webAuthnPolicySignatureAlgorithms",
		"webAuthnPolicyAcceptableAaguids",
		"webAuthnPolicyPasswordlessSignatureAlgorithms",
		"webAuthnPolicyPasswordlessAcceptableAaguids",
	} {
		// FIRST: the field must actually be declared in the document. `assert.False(in(x))` passes just
		// as happily for a field that does not exist, so without this a renamed or removed field would
		// turn each assertion below into a vacuous pass -- and a vendored third-party document is exactly
		// the kind of file that gets updated without anyone re-reading the tests that depend on it.
		require.Contains(t, string(b), "        "+ordinary+":",
			"%s is not declared in the fixture any more, so the assertion below proves nothing. Re-read "+
				"the document and pick arrays that are still there", ordinary)

		assert.False(t, in(ordinary),
			"%s is an array of strings WITHOUT uniqueItems, so reordering it is real drift and it must "+
				"not be compared as a set", ordinary)
	}

	// And the control: the helper works, so the four negatives above are findings rather than a broken
	// membership test.
	assert.True(t, in("enabledEventTypes"),
		"the membership helper is broken, so nothing above proved anything")
}

// A verb with no request body returns nothing and no error, on a real document.
//
// The synthetic fixture covers this too, but on a hand-made one-operation spec. Here it is the same
// document and the same path as the PUT above, differing only in method -- so it cannot pass for the
// wrong reason.
func TestARealDocumentsBodylessVerbsDeclareNoSets(t *testing.T) {
	b, err := os.ReadFile("testdata/keycloak-realm.yaml")
	require.NoError(t, err)
	client := clientFromSpec(t, string(b))

	for _, method := range []string{"GET", "DELETE"} {
		got, err := client.SetTypedArrayPaths(method, "/admin/realms/{realm}")
		require.NoError(t, err,
			"%s has no request body; that is an answer, not a failure, and erroring here would log on "+
				"every reconcile of every resource whose update verb has no body", method)
		assert.Empty(t, got, "%s declares no request body and therefore no sets", method)
	}
}
