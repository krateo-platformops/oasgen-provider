//go:build unit

package restResources

import (
	"os"
	"strings"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolverSourcePaths tells the comparison which CR paths the request never carried (#663). It is a
// MIRROR of stripResolverSources, and the two disagreeing is silent in both directions:
//
//	a path this misses   -> compared against a vendor that never received it  (the #663 defect, back)
//	a path this invents  -> stops being compared at all                       (drift goes unreported)
func TestResolverSourcePaths(t *testing.T) {
	ref := &getter.FieldResolver{}

	verb := &getter.VerbsDescription{
		Action: "update",
		FieldMapping: []getter.FieldMappingItem{
			{InCustomResource: "spec.extra.bmc_secret", Resolver: ref},
			{InCustomResource: "status.observed.tokenRef", Resolver: ref},
			{InCustomResource: "clientSecretRef", Resolver: ref}, // already unprefixed
			{InCustomResource: "spec.plain.relocation"},          // NO resolver: ordinary API data
			{InCustomResource: "", Resolver: ref},                // resolver with no CR source
			{InCustomResource: "spec.", Resolver: ref},           // trims to empty
		},
	}

	got := resolverSourcePaths(verb)
	assert.Equal(t, []string{"extra.bmc_secret", "observed.tokenRef", "clientSecretRef"}, got)
}

func TestResolverSourcePathsOnNothing(t *testing.T) {
	assert.Nil(t, resolverSourcePaths(nil))
	assert.Nil(t, resolverSourcePaths(&getter.VerbsDescription{Action: "update"}))
}

// A SOURCE-LEVEL TRIPWIRE ON THE OTHER HALF OF THE MIRROR.
//
// The two sites live in different packages and stripResolverSources is unexported, so there is no way
// to call it from here and compare. What can be pinned is that it still uses the SAME condition and the
// SAME chained trim -- and a behavioural test of resolverSourcePaths alone would pass happily while
// the strip drifted away from it.
func TestStripResolverSourcesStillUsesTheSameRuleAsThisMirror(t *testing.T) {
	b, err := os.ReadFile("../tools/client/builder/builder.go")
	require.NoError(t, err)
	src := string(b)
	require.NotEmpty(t, src)

	fn := strings.Index(src, "func stripResolverSources(")
	require.NotEqual(t, -1, fn,
		"stripResolverSources was renamed or removed; re-point this test rather than deleting it, and "+
			"check whether resolverSourcePaths still mirrors anything")
	body := src[fn:]
	if next := strings.Index(body[1:], "\nfunc "); next != -1 {
		body = body[:next+1]
	}

	// CONTROL: the slice must actually be the function, or both assertions below are vacuous.
	require.Contains(t, body, "RemoveNestedField",
		"control: this does not look like stripResolverSources' body")

	assert.Contains(t, body, `mapping.Resolver == nil || mapping.InCustomResource == ""`,
		"the strip's condition changed. resolverSourcePaths selects entries by exactly this test, so the "+
			"comparison would now discount a different set of paths than the request actually omits")

	assert.Contains(t, body, `strings.TrimPrefix(strings.TrimPrefix(mapping.InCustomResource, "spec."), "status.")`,
		"the strip's path normalisation changed. The chained trim is load-bearing: a single TrimPrefix "+
			"would leave `status.` on a status-sourced path, and the comparison looks paths up by the "+
			"trimmed form")
}
