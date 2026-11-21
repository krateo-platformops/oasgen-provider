//go:build unit || integration

package deploy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/objects"
)

// renderShippedRDCDeployment reproduces BOTH render passes the asset really goes through, because
// only the second one is the provider's:
//
//	pass 1  HELM renders the chart with values.yaml. `{{ "{{" }}` survives as `{{`, and every
//	        .Values reference is resolved -- including `{{- with .Values.rdc.readinessProbe }}`,
//	        which is what decides whether a probe reaches the object at all.
//	pass 2  THE PROVIDER renders what helm left, with its own parameters.
//
// Both passes use text/template with sprig, which is exactly what helm and
// internal/tools/objects/templates both use, so toYaml/nindent/with behave as they do in production.
//
// AN EARLIER VERSION OF THIS HELPER STRIPPED the chart-level lines instead of rendering them. It
// produced a Deployment with NO PROBES AT ALL, so the admission assertion below passed over a
// probe-less object -- vacuous for precisely the defect this arm exists to catch. That is why the
// probe control at the end of this function is not optional.
func renderShippedRDCDeployment(t *testing.T, ns string) *appsv1.Deployment {
	t.Helper()

	chart := filepath.Join("..", "..", "..", "..", "..", "helm", "oasgen-provider")
	assetPath := filepath.Join(chart, "assets", "rdc", "deployment.yaml")
	valuesPath := filepath.Join(chart, "values.yaml")

	assetSrc, err := os.ReadFile(assetPath)
	if err != nil {
		t.Fatalf("could not read the SHIPPED asset at %s: %v -- a path error here would make this arm "+
			"pass vacuously, which is the failure mode it exists to prevent", assetPath, err)
	}
	valuesSrc, err := os.ReadFile(valuesPath)
	if err != nil {
		t.Fatalf("could not read the chart's values at %s: %v", valuesPath, err)
	}

	var values map[string]any
	if err := yaml.Unmarshal(valuesSrc, &values); err != nil {
		t.Fatalf("the chart's values.yaml does not parse: %v", err)
	}

	// CONTROL ON THE INPUT: values.yaml really does define a readiness probe with a handler. If this
	// ever becomes empty the arm below measures nothing, and it should say so here rather than pass.
	rdc, _ := values["rdc"].(map[string]any)
	rp, _ := rdc["readinessProbe"].(map[string]any)
	if _, ok := rp["httpGet"]; !ok {
		t.Fatalf("values.yaml's rdc.readinessProbe has no httpGet (%v) -- with no probe in the input, "+
			"an admission check on the output proves nothing about probes", rp)
	}

	// HELM'S OWN FUNCTIONS, not sprig's. `toYaml` is defined by helm's engine, not by sprig, and its
	// TrimSuffix of the trailing newline is what makes `nindent` line up -- without it every
	// toYaml block gains a blank line and the YAML shape changes. Reproduced here rather than
	// approximated, because an approximation of the renderer is the thing that made the previous
	// version of this helper report a probe-less object.
	helmFuncs := template.FuncMap{
		"toYaml": func(v any) string {
			b, err := yaml.Marshal(v)
			if err != nil {
				return ""
			}
			return strings.TrimSuffix(string(b), "\n")
		},
	}

	// THE CHART'S REAL NAMED TEMPLATES. The asset calls `include "krateo.image"`, which is defined in
	// templates/_helpers.tpl -- so the helpers are PARSED IN rather than stubbed. A stub would decide
	// the image reference itself, and "an approximation of the renderer" is exactly what made the
	// previous version of this helper report a probe-less object.
	helpersSrc, err := os.ReadFile(filepath.Join(chart, "templates", "_helpers.tpl"))
	if err != nil {
		t.Fatalf("could not read the chart's _helpers.tpl: %v", err)
	}
	if !strings.Contains(string(helpersSrc), `define "krateo.image"`) {
		t.Fatalf("_helpers.tpl does not define krateo.image, which the asset includes -- the helper " +
			"file moved and this arm would render a stub")
	}

	render := func(name, src string, data any) string {
		root := template.New(name).Funcs(sprig.FuncMap()).Funcs(helmFuncs)
		// `include` is helm's, not text/template's: it RETURNS the rendered text so it can be piped.
		root = root.Funcs(template.FuncMap{
			"include": func(tplName string, ctx any) (string, error) {
				var b bytes.Buffer
				if err := root.ExecuteTemplate(&b, tplName, ctx); err != nil {
					return "", err
				}
				return b.String(), nil
			},
		})
		if _, err := root.Parse(string(helpersSrc)); err != nil {
			t.Fatalf("the chart's _helpers.tpl does not parse: %v", err)
		}
		tpl, err := root.Parse(src)
		if err != nil {
			t.Fatalf("pass %q does not parse: %v", name, err)
		}
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, data); err != nil {
			t.Fatalf("pass %q does not execute: %v", name, err)
		}
		return buf.String()
	}

	// PASS 1 -- helm's. Chart, Release and Values are the three roots the asset touches.
	helmData := map[string]any{
		"Values":  values,
		"Chart":   map[string]any{"Name": "oasgen-provider", "Version": "0.0.0-test", "AppVersion": "0.0.0-test"},
		"Release": map[string]any{"Name": "oasgen-provider", "Namespace": ns, "Service": "Helm"},
	}
	afterHelm := render("helm", string(assetSrc), helmData)

	// PASS 2 -- the provider's. Only `{{ "{{" }}`-escaped directives survive pass 1, so whatever
	// remains here is what the provider itself resolves at reconcile time.
	rendered := afterHelm
	if strings.Contains(afterHelm, "{{") {
		// ALL EIGHT parameters the asset references, enumerated FROM the asset rather than guessed:
		//   grep -oE '\{\{ "\{\{" \}\} *\.[A-Za-z_][A-Za-z0-9_.]*' assets/rdc/deployment.yaml
		// A missing one does not render empty -- text/template fails with "invalid value; expected
		// string", so an omission is loud rather than silent. That is why they are listed in full.
		providerParams := map[string]any{
			"resource":           "widgets",
			"apiGroup":           "example.com",
			"apiVersion":         "v1alpha1",
			"namespace":          ns,
			"name":               "widget-controller",
			"leaseName":          "widget-controller-lease",
			"labelSelector":      "widgets-example-com-v1alpha1",
			"serviceAccountName": "widget-controller-sa",
		}
		rendered = render("provider", afterHelm, providerParams)
	}

	tmp := filepath.Join(t.TempDir(), "deployment.yaml")
	if err := os.WriteFile(tmp, []byte(rendered), 0o600); err != nil {
		t.Fatalf("writing the rendered asset: %v", err)
	}

	// THE PRODUCTION DECODE PATH. objects.CreateK8sObject is the function that produced the
	// consumer's error on 0.85.0, so the object judged below is the one the provider would send.
	var dep appsv1.Deployment
	if err := objects.CreateK8sObject(&dep, schema.GroupVersionResource{
		Group: "example.com", Version: "v1alpha1", Resource: "widgets",
	}, types.NamespacedName{Namespace: ns, Name: "widget-controller"}, tmp); err != nil {
		t.Fatalf("THE SHIPPED ASSET DOES NOT DECODE: %v\n--- what the two passes produced ---\n%s", err, rendered)
	}

	if len(dep.Spec.Template.Spec.Containers) == 0 {
		t.Fatalf("decoded to ZERO containers:\n%s", rendered)
	}

	// THE PROBE CONTROL. Without it this arm silently becomes a check on a probe-less object, which
	// is exactly what the stripper version of this helper did.
	c := dep.Spec.Template.Spec.Containers[0]
	if c.ReadinessProbe == nil {
		t.Fatalf("the rendered Deployment carries NO readinessProbe although values.yaml defines one "+
			"with an httpGet -- the render dropped it, so an admission check here would be vacuous:\n%s",
			rendered)
	}
	if c.ReadinessProbe.HTTPGet == nil && c.ReadinessProbe.TCPSocket == nil && c.ReadinessProbe.Exec == nil {
		t.Fatalf("the rendered readinessProbe has NO HANDLER -- this is 0.85.1's defect and the " +
			"apiserver must refuse it below; a decode gate cannot see it")
	}
	t.Logf("rendered: ports=%d readiness.handler=httpGet:%v liveness=%v",
		len(c.Ports), c.ReadinessProbe.HTTPGet != nil, c.LivenessProbe != nil)

	return &dep
}

// TestTheRenderedDeploymentCarriesARealReadinessProbe is the OFFLINE half, and it exists because
// the first version of the helper above STRIPPED the chart-level lines instead of rendering them.
// That produced a Deployment with no probes at all, so the admission arm below would have passed
// over a probe-less object -- VACUOUS for precisely the defect it was written to catch.
//
// It also pins #860's fix from the other side: values.yaml carries `rdc.livenessProbe: {}`, and the
// `{{- with }}` guard must drop the whole block rather than emit a handler-less probe, which is what
// 0.85.1 shipped. `readinessProbe` carries a real httpGet and must survive.
//
// This one needs no cluster, so it runs in the unit suite on every PR -- the admission arm cannot.
func TestTheRenderedDeploymentCarriesARealReadinessProbe(t *testing.T) {
	dep := renderShippedRDCDeployment(t, "demo-system")
	c := dep.Spec.Template.Spec.Containers[0]

	if c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet == nil {
		t.Fatalf("the rendered Deployment has no readinessProbe with an httpGet; values.yaml defines " +
			"one, so the render dropped it and the admission arm would measure a probe-less object")
	}
	if got, want := c.ReadinessProbe.HTTPGet.Path, "/readyz"; got != want {
		t.Errorf("readiness path = %q, want %q", got, want)
	}
	if c.LivenessProbe != nil {
		t.Errorf("a livenessProbe reached the object although values.yaml sets it to {}: %+v\n"+
			"An empty handler is what 0.85.1 shipped and the apiserver REFUSES it; the `with` guard "+
			"added in #860 must drop the block entirely", c.LivenessProbe)
	}

	// The ports, which is where 0.85.0's defect was: http and health as SIBLINGS, metrics absent at
	// its default of 0. A nested list here is what made the asset unparseable.
	names := map[string]int32{}
	for _, p := range c.Ports {
		names[p.Name] = p.ContainerPort
	}
	if names["http"] == 0 || names["health"] == 0 {
		t.Errorf("expected sibling http and health ports, got %v -- 0.85.0 nested one inside the other", names)
	}
	if _, ok := names["metrics"]; ok {
		t.Errorf("a metrics port reached the object although rdc.metrics.port defaults to 0: %v", names)
	}
}
