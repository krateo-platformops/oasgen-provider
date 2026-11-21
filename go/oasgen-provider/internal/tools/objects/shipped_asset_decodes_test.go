//go:build unit

package objects_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/objects"
)

// helmRender does the ONE transformation helm performs on this asset before the provider sees it:
// `{{ "{{" }} x {{ "}}" }}` becomes `{{ x }}`. The asset is escaped that way precisely so helm emits
// the provider's OWN template into the ConfigMap rather than evaluating it.
func helmRender(t *testing.T, s string) string {
	t.Helper()
	out := strings.ReplaceAll(s, `{{ "{{" }}`, "{{")
	out = strings.ReplaceAll(out, `{{ "}}" }}`, "}}")

	// CONTROL: the escaping really was present and really was removed. Without this, an asset that
	// stopped using the escaped form would make this helper a no-op and the test would still pass --
	// on text that is no longer what ships.
	require.NotContains(t, out, `{{ "{{" }}`, "the escaped form survived the render")
	require.NotEqual(t, s, out, "nothing was unescaped, so this is not the shipped asset's form")
	return out
}

// TestTheShippedRDCDeploymentAssetDecodes is the gate that was missing when 0.85.0 shipped unusable.
//
// WHAT HAPPENED. #843 added a `health` container port to helm/oasgen-provider/assets/rdc/deployment.yaml
// indented 14 spaces where `- name: http` sits at 12, making it a nested list inside the http item's
// mapping -- invalid YAML. Every generated controller Deployment then failed to decode:
//
//	"Error creating deployment object ... failed to decode object: yaml: line 86: did not find expected key"
//	454 times on one consumer's cluster, 0 of 25 RestDefinitions regenerated.
//
// WHY NOTHING CAUGHT IT, and this is the part worth keeping. There are TWO deployment.yaml files:
//
//	helm/oasgen-provider/assets/rdc/deployment.yaml                    <- SHIPS. Got the change.
//	internal/controllers/restdefinition/testdata/setup/rdc/deployment.yaml  <- TESTS. Did not.
//
// Measured: the fixture has 0 occurrences of `healthProbePort` and 0 of `name: health`. So every test
// in this repo exercised text that never contained the defect. And #843's own wiring test asserted the
// rendered ConfigMap CONTAINS "REST_CONTROLLER_HEALTH_PROBE_PORT" and ".Values.rdc.healthProbePort" --
// strings.Contains cannot see invalid YAML, so it passed on a file that would not parse.
//
// This test decodes the SHIPPED file through the SAME function the provider uses, so the channel it
// reads is the one that broke.
func TestTheShippedRDCDeploymentAssetDecodes(t *testing.T) {
	// internal/tools/objects -> oasgen-provider -> go -> repo root
	asset := filepath.Join("..", "..", "..", "..", "..", "helm", "oasgen-provider", "assets", "rdc", "deployment.yaml")
	raw, err := os.ReadFile(asset)
	require.NoError(t, err, "could not read the SHIPPED asset at %s -- a path error here would make "+
		"this gate pass vacuously, which is the failure mode it exists to prevent", asset)

	// PARSE CONTROL: it is the rdc Deployment and not some other asset.
	require.Contains(t, string(raw), "kind: Deployment", "the file read is not a Deployment")
	require.Contains(t, string(raw), "healthProbePort",
		"the shipped asset no longer mentions healthProbePort. If the probe was removed deliberately, "+
			"delete this control; if not, the asset is not the one #843 changed")

	// Helm's own transformation, then the chart's values, which the provider never sees as templates.
	rendered := helmRender(t, string(raw))
	// The three port values are substituted BEFORE the drop below, because they are the structure
	// under test -- dropping their lines would remove the very ports this asserts on.
	for _, sub := range [][2]string{
		{"{{ .Values.rdc.service.port }}", "80"},
		{"{{ .Values.rdc.healthProbePort }}", "8081"},
		{"{{ .Values.rdc.metrics.port }}", "9090"},
	} {
		rendered = strings.ReplaceAll(rendered, sub[0], sub[1])
	}
	// Any remaining chart-level directive is stripped the way helm resolves it: this test is about the
	// STRUCTURE the provider receives, and an unresolved {{- if }} is not structure.
	// EVERY REMAINING CHART-LEVEL LINE IS DROPPED, not substituted. helm resolves these before the
	// provider ever reads the ConfigMap, so they are not part of the structure under test -- and a
	// substitution that leaves a bare scalar where a directive was is how the first three versions of
	// this helper reported a defect in the asset that was really a defect in the helper.
	var kept []string
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, "toYaml") || strings.Contains(line, "nindent") ||
			strings.Contains(line, "include ") || strings.Contains(line, ".Values") ||
			strings.Contains(line, ".Chart") || strings.Contains(line, ".Release") ||
			regexp.MustCompile(`^\s*\{\{`).MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}
	rendered = strings.Join(kept, "\n")

	// CONTROL: dropping those lines did NOT drop the ports block, which is the thing under test.
	require.Contains(t, rendered, "- name: http", "the http port was dropped by the stripper")
	require.Contains(t, rendered, "- name: health", "the health port was dropped by the stripper, so "+
		"this test can no longer see the defect it exists for")

	tmp := filepath.Join(t.TempDir(), "deployment.yaml")
	require.NoError(t, os.WriteFile(tmp, []byte(rendered), 0o600))

	// THE PRODUCTION PATH. CreateK8sObject reads the file, renders the provider's own template, and
	// decodes with the same YAML serializer that produced the consumer's error.
	var dep appsv1.Deployment
	err = objects.CreateK8sObject(&dep, schema.GroupVersionResource{
		Group: "example.com", Version: "v1alpha1", Resource: "widgets",
	}, types.NamespacedName{Namespace: "ns", Name: "widget-controller"}, tmp)
	require.NoError(t, err, "the SHIPPED rdc deployment asset does not decode. Every generated "+
		"controller would fail to be created, with no RestDefinition regenerated -- which is what "+
		"0.85.0 did on a real cluster")

	// AND THE PORTS MUST BE SIBLINGS, which is the specific thing that broke. A decode that succeeded
	// with health nested inside http would be a different bug with the same cause.
	require.NotEmpty(t, dep.Spec.Template.Spec.Containers, "no containers decoded")
	ports := dep.Spec.Template.Spec.Containers[0].Ports
	names := make([]string, 0, len(ports))
	for _, p := range ports {
		names = append(names, p.Name)
	}
	require.Contains(t, names, "http", "the http port is missing, so the ports list is not what ships")
	require.Contains(t, names, "health",
		"the health port did not decode as a sibling of http. It is declared in the asset, so if it is "+
			"absent here it was nested inside another port's mapping -- the 0.85.0 defect exactly")
}
