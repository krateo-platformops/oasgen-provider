//go:build integration

package deploy

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// TestTheShippedDeploymentIsACCEPTEDByARealApiserver is the gate the last two releases needed.
//
// PARSE, VERSION, CONTENT and ADMISSION are four separate facts:
//
//	0.85.0   the asset did not PARSE
//	0.85.1   it parsed, and was REFUSED at ADMISSION:
//	         spec.template.spec.containers[0].livenessProbe: Required value: must specify a handler type
//
// #856's decode gate PASSED on 0.85.1, because `{}` is valid YAML. Every generated controller failed
// to be created, on every reconcile. A server dry-run is the only thing that answers the fourth fact,
// and it runs here because it needs an apiserver -- not because it is slow.
func TestTheShippedDeploymentIsACCEPTEDByARealApiserver(t *testing.T) {
	f := features.New("admission").
		Assess("the shipped asset is accepted", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			cli, err := client.New(cfg.Client().RESTConfig(), client.Options{})
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
				return ctx
			}

			dep := renderShippedRDCDeployment(t, namespace)
			dep.Name = "admission-subject"

			if err := cli.Create(ctx, dep.DeepCopy(), client.DryRunAll); err != nil {
				t.Fatalf("THE SHIPPED rdc DEPLOYMENT IS REFUSED BY THE APISERVER: %v\n"+
					"Every generated controller would fail to be created, on every reconcile, with the "+
					"asset parsing perfectly -- which is what 0.85.1 did.", err)
			}

			// THE NEGATIVE CONTROL, and the arm is worth nothing without it: the same Deployment
			// carrying 0.85.1's empty probe must be REFUSED, and refused FOR THAT REASON.
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
