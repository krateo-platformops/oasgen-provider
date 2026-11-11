//go:build unit || integration

package deploy

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WHAT THIS PINS
//
// The generated controller used to be restarted on EVERY `Deploy`. The comment above that block said
// "if the hash changes" and no hash was ever consulted, so once #510/#527 made a secretRef Secret-name
// change a reason to redeploy, a consumer measured 8 Deployment revisions in 15 seconds where the
// previous release had 2 in a day — with the ConfigMap's resourceVersion and data hash provably
// identical across the restarts (#588).
//
// The decision is now `restartNeeded`, and these rows are the contract. The table deliberately contains
// both outcomes, and the suite asserts that it does: a table of all-false rows passes against a function
// that never restarts, and a table of all-true rows passes against the unconditional behaviour this
// change exists to remove.
func TestRestartNeeded(t *testing.T) {
	cases := []struct {
		name      string
		live      map[string]string
		rendered  map[string]string
		cmExisted bool
		want      bool
		why       string
	}{
		{
			// THE #588 CASE. A secretRef Secret-set change rewrites a Role's resourceNames and nothing
			// else; the ConfigMap renders byte-identical. The consumer's measurement is exactly this row.
			name:      "identical data is not a reason to restart",
			live:      map[string]string{"REST_CONTROLLER_SERVICEACCOUNT_NAME": "sa", "X": "1"},
			rendered:  map[string]string{"REST_CONTROLLER_SERVICEACCOUNT_NAME": "sa", "X": "1"},
			cmExisted: true,
			want:      false,
			why:       "the pod's environment is unchanged, so rolling it achieves nothing and loses its logs",
		},
		{
			name:      "a changed value requires a restart",
			live:      map[string]string{"REST_CONTROLLER_DEBUG": "false"},
			rendered:  map[string]string{"REST_CONTROLLER_DEBUG": "true"},
			cmExisted: true,
			want:      true,
			why:       "envFrom values are fixed at container start, so a running pod keeps the old one forever",
		},
		{
			name:      "an added key requires a restart",
			live:      map[string]string{"A": "1"},
			rendered:  map[string]string{"A": "1", "B": "2"},
			cmExisted: true,
			want:      true,
			why:       "the new variable would never reach the running container",
		},
		{
			name:      "a removed key requires a restart",
			live:      map[string]string{"A": "1", "B": "2"},
			rendered:  map[string]string{"A": "1"},
			cmExisted: true,
			want:      true,
			why:       "the container would keep a variable the definition no longer declares",
		},
		{
			// A pod that does not exist yet cannot hold stale environment, and the Deployment created
			// alongside starts with the new ConfigMap already in place.
			name:      "the first deploy does not restart anything",
			live:      nil,
			rendered:  map[string]string{"A": "1"},
			cmExisted: false,
			want:      false,
			why:       "there is no running pod to roll",
		},
		{
			// nil and empty are the same absence of data, and must not read as a change — otherwise the
			// first reconcile after an empty-data ConfigMap would roll the controller for nothing.
			name:      "nil and empty data are the same",
			live:      nil,
			rendered:  map[string]string{},
			cmExisted: true,
			want:      false,
			why:       "both mean the container receives no variables from this ConfigMap",
		},
	}

	// The suite must be able to fail in BOTH directions, asserted rather than assumed.
	var wantTrue, wantFalse int
	for _, tc := range cases {
		if tc.want {
			wantTrue++
		} else {
			wantFalse++
		}
	}
	require.NotZero(t, wantTrue, "no row expects a restart: this table passes against a function that never restarts")
	require.NotZero(t, wantFalse, "no row forbids a restart: this table passes against the unconditional behaviour #588 removed")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, restartNeeded(tc.live, tc.rendered, tc.cmExisted), tc.why)
		})
	}
}

// RBAC CANNOT TRIGGER A RESTART, and that is structural rather than a behaviour to test.
//
// Authorization is evaluated by the apiserver on every request and the controller caches no decision, so
// a Role whose `resourceNames` gained or lost a Secret takes effect on the controller's next request with
// no restart at all. The decision function takes no RBAC input, so this cannot regress by a logic change
// — only by someone passing one in.
//
// Asserted on the source, following the precedent in the rest-dynamic-controller client package: a
// signature check, so a future author who adds a Role or a Rules argument has to come here and say why.
func TestTheRestartDecisionTakesNoRBACInput(t *testing.T) {
	b, err := os.ReadFile("deploy.go")
	require.NoError(t, err)
	src := string(b)

	const sig = "func restartNeeded(liveData, renderedData map[string]string, cmExisted bool) bool {"
	require.Contains(t, src, sig,
		"restartNeeded's signature changed. If an argument was added, check it is not an RBAC input: a "+
			"Role change must never roll the controller, because authorization is evaluated per request "+
			"and the controller caches no decision (#588).")

	// And the call site must pass the ConfigMap's data, not a digest that also covers RBAC. The
	// accumulated `hsh` in this file sums the ClusterRole rules, the Role rules and the bindings' subjects
	// among other things; gating the restart on that is how #588 would come back.
	callIdx := strings.Index(src, "mustRestart := restartNeeded(")
	require.NotEqual(t, -1, callIdx, "the restart decision is no longer made from restartNeeded")
	call := src[callIdx : callIdx+120]
	assert.NotContains(t, call, "hsh",
		"the restart is gated on the accumulated hash, which also covers RBAC rules and binding "+
			"subjects. That reintroduces #588: a Role change would roll the controller. Gate it on the "+
			"ConfigMap's data only. Call site: %s", call)
}
