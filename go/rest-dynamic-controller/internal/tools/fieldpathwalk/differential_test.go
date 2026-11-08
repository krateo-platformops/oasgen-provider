package fieldpathwalk

import (
	"fmt"
	"testing"

	"github.com/krateo-platformops/plumbing/fieldpath"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/pathparsing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTheNewWalkerAgreesWithTheOldExceptWhereStated is the transition proof, and the reason the port can
// be trusted at all.
//
// It lives HERE rather than in oasgen-provider because this is the module where both walkers exist:
// pathparsing.GetNestedField (flat form, in production) and Get (typed, staged for plumbing). oasgen has
// neither today.
//
// For every path, Get(Parse(p)) must agree with GetNestedField(Keys(Parse(p))) -- the same path, through
// the same parser, into the same document -- except for the three rows listed as corrections. A row that
// stops diverging fails here, so the port cannot claim a correction it does not make, and a row that
// starts diverging fails too.
//
// WHAT THIS BUYS. oasgen-provider #510 has the provider resolve a Secret name out of a CR, and
// rest-dynamic-controller later reads that Secret. If the two select different elements, the provider
// grants a Role for one Secret and the controller reads another -- #510's own failure mode, one level
// down in the half of the semantics the parser does not cover. This test is what makes "they select the
// same element" measured rather than assumed.
func TestTheNewWalkerAgreesWithTheOldExceptWhereStated(t *testing.T) {
	// The three paths where the typed walker DELIBERATELY differs, each with the reason.
	corrections := map[string]string{
		"spec.credentials['0'].valueSecretRef.name": "a quoted key against an ARRAY silently indexed it in the flat form; " +
			"the author asked for a key and got element 0",
		"spec.zeroKey[0]": "an unquoted index against a MAP resolved as the key \"0\"; the author wrote an index and a map has none",
	}

	// NOT a correction, and the differential caught me listing it as one. Both walkers refuse
	// "spec.name.deeper" -- only the MESSAGE differs, and a message is not an outcome. My own `why` for
	// the row said so while the row claimed otherwise, which is precisely what a "must still differ"
	// assertion is for: it fails when a listed change was never a change.

	agree := []string{
		"spec.name",
		"spec.credentials[0].valueSecretRef.name",
		"spec.credentials[1].secretDataSecretRef.key",
		"spec.credentials[?type=password].valueSecretRef.name",
		"spec.credentials[?type=otp].secretDataSecretRef.name",
		"spec.credentials[?type=nosuch].valueSecretRef.name",
		"spec.credentials[?type=dup].n",
		"spec.numeric[?id=2].hit",
		"spec.floaty[?id=2].hit",
		"spec.stringy[?id=2].hit",
		"spec.emptyVal[?type=].hit",
		"spec.credentials[?type=a-scalar-element].x",
		"spec.credentials[99].name",
		"spec.missing.deeper",
		"spec.zeroKey['0']",
		"spec.name.deeper", // both refuse it; the agreement check returns early once both errored
	}

	for _, p := range agree {
		t.Run("agree/"+p, func(t *testing.T) {
			segs, perr := fieldpath.Parse(p)
			require.NoError(t, perr)

			newVal, newFound, newErr := Get(doc(), segs)
			oldVal, oldFound, oldErr := pathparsing.GetNestedField(doc(), fieldpath.Keys(segs))

			assert.Equal(t, oldErr == nil, newErr == nil,
				"one walker errored and the other did not: old=%v new=%v", oldErr, newErr)
			if oldErr != nil {
				return
			}
			assert.Equal(t, oldFound, newFound, "found must agree")
			if oldFound {
				assert.Equal(t, fmt.Sprintf("%v", oldVal), fmt.Sprintf("%v", newVal),
					"the two walkers selected DIFFERENT values for %q -- which is exactly the #510 failure "+
						"mode this port exists to prevent", p)
			}
		})
	}

	for p, why := range corrections {
		t.Run("corrected/"+p, func(t *testing.T) {
			segs, perr := fieldpath.Parse(p)
			require.NoError(t, perr)

			_, newFound, newErr := Get(doc(), segs)
			_, oldFound, oldErr := pathparsing.GetNestedField(doc(), fieldpath.Keys(segs))

			same := (oldErr == nil) == (newErr == nil) && oldFound == newFound
			assert.False(t, same,
				"%q is listed as a CORRECTION but the two walkers now agree (old: found=%v err=%v / new: "+
					"found=%v err=%v). Remove it from the list, or the port claims a change it does not make. %s",
				p, oldFound, oldErr, newFound, newErr, why)
		})
	}
}
