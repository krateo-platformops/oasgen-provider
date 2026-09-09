//go:build unit

package restdefinition

import "testing"

// TestResourceExistsForController pins the rule behind #122: "exists but not ready" is EXISTS.
//
// Reporting a not-yet-Ready controller Deployment as non-existent told provider-runtime the external
// resource was gone, so it re-entered the create handshake on a resource that had already been
// created — re-setting krateo.io/external-create-pending and wedging on errCreateIncomplete once the
// creation grace period lapsed. The RestDefinition then sat Ready=False forever with a CRD that was
// in fact served, taking its owning composition down with it.
//
// The asymmetry in the first two rows is the part most at risk of being "tidied" later: a missing
// Deployment MUST stay non-existent, because Create is what deploys it. Making both rows true would
// mean the controller is never created at all.
func TestResourceExistsForController(t *testing.T) {
	cases := []struct {
		name      string
		deployOk  bool
		deployRdy bool
		want      bool
		why       string
	}{
		{
			name: "deployment absent", deployOk: false, deployRdy: false, want: false,
			why: "genuinely does not exist; Create must run to deploy it",
		},
		{
			name: "deployment present but not ready", deployOk: true, deployRdy: false, want: true,
			why: "#122: it exists and is rolling out. false here re-triggers create and wedges the CR",
		},
		{
			name: "deployment present and ready", deployOk: true, deployRdy: true, want: true,
			why: "the ordinary case",
		},
		{
			name: "ready reported without the deployment existing", deployOk: false, deployRdy: true, want: false,
			why: "incoherent input, but existence must still follow deployOk alone",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resourceExistsForController(tc.deployOk, tc.deployRdy); got != tc.want {
				t.Errorf("resourceExistsForController(%v, %v) = %v, want %v — %s",
					tc.deployOk, tc.deployRdy, got, tc.want, tc.why)
			}
		})
	}
}
