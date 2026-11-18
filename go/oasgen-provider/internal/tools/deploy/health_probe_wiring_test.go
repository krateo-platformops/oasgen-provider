//go:build unit

package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// chartValues reads the shipped chart's values.yaml. The path is relative because this asserts about
// the artifact that ships, not about a fixture.
func chartValues(t *testing.T) map[string]any {
	t.Helper()
	// internal/tools/deploy -> go/oasgen-provider -> go -> repo root
	p := filepath.Join("..", "..", "..", "..", "..", "helm", "oasgen-provider", "values.yaml")
	b, err := os.ReadFile(p)
	require.NoError(t, err, "could not read the shipped values.yaml at %s", p)

	var v map[string]any
	require.NoError(t, yaml.Unmarshal(b, &v))
	// PARSE CONTROL: the file really was read and really is the chart. Without it, an empty map would
	// make every lookup below return nil and the assertions would fail for the wrong reason -- or, if
	// written as absence checks, pass for the wrong reason.
	require.Contains(t, v, "rdc", "values.yaml parsed but has no rdc block -- wrong file or wrong path")
	return v
}

// TestTheReadinessProbePortMatchesTheListenerPort is the assertion that stops a diagnostic from
// becoming an outage.
//
// The chart turns the readiness probe ON by default (#815), because the defect it reports is invisible:
// a controller that cannot acquire its ownership lease reports `avail=1 ready=1 restarts=0` while
// reconciling nothing. But a probe pointed at a port NOTHING LISTENS ON marks every pod NotReady --
// which is worse than the defect, and arrives through a one-character edit in values.yaml that no other
// test would notice.
//
// Two values have to agree and nothing structural makes them agree:
//
//	rdc.healthProbePort                  -> REST_CONTROLLER_HEALTH_PROBE_PORT, what the binary binds
//	rdc.readinessProbe.httpGet.port      -> what the kubelet probes
func TestTheReadinessProbePortMatchesTheListenerPort(t *testing.T) {
	v := chartValues(t)
	rdc, ok := v["rdc"].(map[string]any)
	require.True(t, ok, "rdc is not a mapping")

	listen, ok := rdc["healthProbePort"]
	require.True(t, ok, "rdc.healthProbePort is absent: the chart cannot tell the binary which port to bind")

	probe, ok := rdc["readinessProbe"].(map[string]any)
	require.True(t, ok, "rdc.readinessProbe is absent or not a mapping")

	httpGet, ok := probe["httpGet"].(map[string]any)
	require.True(t, ok, "rdc.readinessProbe.httpGet is absent: the probe has no port to compare")

	require.Equal(t, listen, httpGet["port"],
		"the readiness probe points at a different port from the one the controller binds. Every pod "+
			"would be marked NotReady -- an outage produced by a diagnostic, which is the one outcome "+
			"worse than the defect it reports")

	require.Equal(t, "/readyz", httpGet["path"],
		"the probe path must be /readyz, which is the only route the health listener serves -- any "+
			"other path answers 404 and marks every pod NotReady")
}

// TestNoLivenessProbeIsWiredToTheReadinessCheck pins the distinction that makes this safe at all.
//
// Waiting for a lease is HEALTHY: exactly one replica holds it and the others are correct to wait. A
// liveness probe pointed at /readyz would restart every waiting replica in a loop, turning a working
// design into a crash loop. The value is deliberately an empty mapping rather than absent, so the
// intent is visible in a values diff.
func TestNoLivenessProbeIsWiredToTheReadinessCheck(t *testing.T) {
	v := chartValues(t)
	rdc := v["rdc"].(map[string]any)

	live, present := rdc["livenessProbe"]
	require.True(t, present,
		"rdc.livenessProbe is absent rather than empty: an absent key renders the same null, but an "+
			"empty mapping says the emptiness was a decision")

	switch l := live.(type) {
	case nil:
		// An explicit null is acceptable: nothing is wired.
	case map[string]any:
		require.Empty(t, l,
			"a liveness probe is configured. If it points at /readyz it restarts every replica that is "+
				"correctly waiting for the ownership lease; if it points elsewhere, say where and why")
	default:
		t.Fatalf("rdc.livenessProbe has an unexpected shape %T", live)
	}
}

// TestTheConfigMapCarriesTheHealthPortToTheBinary closes the loop: the values are only useful if the
// rendered ConfigMap actually passes the port to the controller.
//
// A probe configured in values.yaml and a port never delivered to the binary look identical in a values
// diff, and produce every pod NotReady.
func TestTheConfigMapCarriesTheHealthPortToTheBinary(t *testing.T) {
	p := filepath.Join("..", "..", "..", "..", "..", "helm", "oasgen-provider", "assets", "rdc", "configmap.yaml")
	b, err := os.ReadFile(p)
	require.NoError(t, err, "could not read the rdc ConfigMap asset at %s", p)
	text := string(b)

	// PARSE CONTROL: this is the right file. The asset is a Go template so it cannot be unmarshalled;
	// a known sibling key proves the read landed on the ConfigMap rather than on something else.
	require.Contains(t, text, "REST_CONTROLLER_METRICS_SERVER_PORT",
		"the file read does not look like the rdc ConfigMap -- wrong path")

	require.Contains(t, text, "REST_CONTROLLER_HEALTH_PROBE_PORT",
		"the ConfigMap does not pass the health port to the binary, so the readiness probe points at a "+
			"port nothing binds and every pod is marked NotReady")
	require.Contains(t, strings.ReplaceAll(text, " ", ""), ".Values.rdc.healthProbePort",
		"the env key exists but is not fed from rdc.healthProbePort, so the chart value and the bound "+
			"port can drift apart silently")
}
