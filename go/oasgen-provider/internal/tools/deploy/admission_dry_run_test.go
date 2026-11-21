//go:build integration

package deploy

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/objects"
)

// shippedRDCDeployment renders the SHIPPED chart asset the way the provider receives it and decodes it
// into a typed Deployment.
//
// Helm unescapes `{{ "{{" }}` so the ConfigMap carries the provider's OWN template; the provider then
// renders that. This reproduces both steps far enough to get a decodable object, which is all the
// apiserver needs to judge.
func shippedRDCDeployment(t *testing.T, ns string) *appsv1.Deployment {
	t.Helper()
	p := filepath.Join("..", "..", "..", "..", "..", "helm", "oasgen-provider", "assets", "rdc", "deployment.yaml")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("could not read the SHIPPED asset at %s: %v -- a path error here would make this arm "+
			"pass vacuously, which is the failure mode it exists to prevent", p, err)
	}
	src := strings.ReplaceAll(string(b), `{{ "{{" }}`, "{{")
	src = strings.ReplaceAll(src, `{{ "}}" }}`, "}}")

	// PARSE CONTROL: the right file, and it declares the probes this arm is about.
	if !strings.Contains(src, "kind: Deployment") {
		t.Fatalf("the file read is not a Deployment")
	}
	if !strings.Contains(src, "readinessProbe") {
		t.Fatalf("the asset declares no readinessProbe; this arm would measure nothing")
	}

	// The chart's own defaults, then every remaining chart-level line dropped -- helm resolves those
	// before the provider sees the ConfigMap, so they are not part of the object under test. Dropping
	// rather than substituting: a bare scalar where a directive was is how three earlier versions of a
	// similar helper reported defects that were really the helper's.
	for _, kv := range [][2]string{
		{"{{ .Values.rdc.service.port }}", "80"},
		{"{{ .Values.rdc.healthProbePort }}", "8081"},
		{"{{ .Values.rdc.metrics.port }}", "9090"},
	} {
		src = strings.ReplaceAll(src, kv[0], kv[1])
	}
	var kept []string
	for _, line := range strings.Split(src, "\n") {
		if regexp.MustCompile(`^\s*\{\{`).MatchString(line) ||
			strings.Contains(line, "toYaml") || strings.Contains(line, "nindent") ||
			strings.Contains(line, "include ") || strings.Contains(line, ".Values") ||
			strings.Contains(line, ".Chart") || strings.Contains(line, ".Release") {
			continue
		}
		kept = append(kept, line)
	}
	src = strings.Join(kept, "\n")

	// CONTROL: dropping those lines did not drop the ports block, which is what this arm is about.
	if !strings.Contains(src, "- name: http") {
		t.Fatalf("the stripper removed the ports block, so this arm can no longer see its subject")
	}

	tmp := filepath.Join(t.TempDir(), "deployment.yaml")
	if err := os.WriteFile(tmp, []byte(src), 0o600); err != nil {
		t.Fatalf("writing the rendered asset: %v", err)
	}

	// THE PRODUCTION DECODE PATH. objects.CreateK8sObject is the function that produced the
	// consumer's error, so the object the apiserver judges below is the one the provider would send.
	var dep appsv1.Deployment
	if err := objects.CreateK8sObject(&dep, schema.GroupVersionResource{
		Group: "example.com", Version: "v1alpha1", Resource: "widgets",
	}, types.NamespacedName{Namespace: ns, Name: "widget-controller"}, tmp); err != nil {
		t.Fatalf("the SHIPPED asset does not decode: %v", err)
	}
	return &dep
}

// TestTheShippedDeploymentIsACCEPTEDByARealApiserver is the gate the last two releases needed.
//
// 0.85.0 shipped an asset that did not PARSE. 0.85.1 shipped one that parsed and was REFUSED at
// ADMISSION -- `rdc.livenessProbe: {}` renders a probe with no handler:
//
//	spec.template.spec.containers[0].livenessProbe: Required value: must specify a handler type
//
// Every generated controller failed to be created, on every reconcile, and the existing decode gate
// PASSED because `{}` is valid YAML. PARSE, VERSION, CONTENT and ADMISSION are four separate facts;
// three had checks and the fourth shipped twice.
//
// A server dry-run is the only thing that answers the fourth. It runs here rather than in the unit
// suite because it needs an apiserver, and this package already provisions a kind cluster.
func TestTheShippedDeploymentIsACCEPTEDByARealApiserver(t *testing.T) {
	f := features.New("admission").
		Assess("the shipped asset is accepted", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			cli, err := client.New(cfg.Client().RESTConfig(), client.Options{})
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
				return ctx
			}

			dep := shippedRDCDeployment(t, namespace)
			dep.Name = "admission-subject"

			if err := cli.Create(ctx, dep.DeepCopy(), client.DryRunAll); err != nil {
				t.Fatalf("THE SHIPPED rdc DEPLOYMENT IS REFUSED BY THE APISERVER: %v\n"+
					"Every generated controller would fail to be created, on every reconcile, with the "+
					"asset parsing perfectly -- which is what 0.85.1 did. A decode gate cannot see this.", err)
			}

			// THE NEGATIVE CONTROL, and the arm is worthless without it: the same Deployment carrying
			// 0.85.1's empty probe must be REFUSED. If this is accepted, the apiserver in this cluster
			// does not enforce the rule and the assertion above proves nothing about anybody's cluster.
			broken := dep.DeepCopy()
			broken.Name = "admission-negative-control"
			broken.Spec.Template.Spec.Containers[0].LivenessProbe = &corev1.Probe{}
			err = cli.Create(ctx, broken, client.DryRunAll)
			if err == nil {
				t.Fatalf("NEGATIVE CONTROL FAILED: a Deployment with livenessProbe:{} was ACCEPTED. "+
					"This apiserver (%s) does not enforce the handler requirement, so the subject "+
					"assertion above says nothing about a cluster that does.",
					cfg.Client().RESTConfig().Host)
			}
			if !strings.Contains(err.Error(), "must specify a handler type") {
				t.Fatalf("NEGATIVE CONTROL was refused for the WRONG REASON: %v\n"+
					"Expected \"must specify a handler type\". A refusal on some other ground means this "+
					"arm is not measuring the defect it was written for.", err)
			}
			t.Logf("negative control refused correctly: %v", err)
			return ctx
		}).Feature()

	testenv.Test(t, f)
}
