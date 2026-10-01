package generation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/labels"
)

func matches(t *testing.T, sel string, l map[string]string) bool {
	t.Helper()
	if sel == "" {
		return true
	}
	p, err := labels.Parse(sel)
	assert.NoError(t, err, "selector %q must parse", sel)
	return p.Matches(labels.Set(l))
}

// TestExactlyOneControllerClaimsEachLabelledInstance is the property the per-version model needs.
//
// Every served version runs its own controller, and every served endpoint returns every object, so the
// apiVersion cannot separate them — the label does.
func TestExactlyOneControllerClaimsEachLabelledInstance(t *testing.T) {
	versions := []string{"v1-0-26", "v1-0-27", "v1-0-28"}

	for _, owner := range versions {
		t.Run("instance labelled "+owner, func(t *testing.T) {
			var claimedBy []string
			for _, v := range versions {
				if matches(t, VersionSelector(v), map[string]string{VersionLabel: owner}) {
					claimedBy = append(claimedBy, v)
				}
			}
			assert.Equal(t, []string{owner}, claimedBy,
				"none means it never reconciles; two means it is reconciled twice")
		})
	}
}

// TestSelectorCannotGoStale is why this is exact equality rather than anything cleverer.
//
// An exact selector is a constant function of the controller's OWN version, so appending a version to the
// CRD cannot change what an existing controller claims. An earlier attempt gave the newest version
// `notin (every other served version)` so it would also pick up unlabelled instances; that selector went
// wrong as soon as another version was appended, because the older Deployment kept the one it was created
// with and began claiming the newer version's instances.
func TestSelectorCannotGoStale(t *testing.T) {
	deployedWhenCurrent := VersionSelector("v1-0-27")

	// v1-0-28 is appended later. The v1-0-27 Deployment is NOT re-rendered.
	assert.False(t, matches(t, deployedWhenCurrent, map[string]string{VersionLabel: "v1-0-28"}),
		"an older controller must not start claiming a newer version's instances when the CRD grows")
	assert.True(t, matches(t, deployedWhenCurrent, map[string]string{VersionLabel: "v1-0-27"}),
		"and must keep claiming its own")
	assert.Equal(t, deployedWhenCurrent, VersionSelector("v1-0-27"),
		"the selector depends on nothing but the controller's own version")
}

// An unlabelled instance belongs to no controller. That is deliberate and symmetric with
// composition-dynamic-controller: labelling is the MANAGER's job (the admission policy, plus the
// provider's sweep for anything predating it), never the per-version controller's.
func TestUnlabelledInstancesAreNobodysUntilStamped(t *testing.T) {
	for _, v := range []string{"v1-0-27", "v1-0-28"} {
		assert.False(t, matches(t, VersionSelector(v), map[string]string{}),
			"%s must not claim an unlabelled instance", v)
	}
}

func TestEmptyVersionWatchesEverything(t *testing.T) {
	assert.Equal(t, "", VersionSelector(""))
}

func TestServedVersionsExcludesVacuum(t *testing.T) {
	assert.Nil(t, ServedVersions(nil))
}
