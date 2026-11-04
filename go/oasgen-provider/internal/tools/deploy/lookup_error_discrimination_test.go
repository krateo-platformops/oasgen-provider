package deploy

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// #427: Lookup USED TO REPORT A FAILED LOOKUP AS A SUCCESSFUL EMPTY ONE.
//
// Three sites returned ("", nil) for every failure, so Observe compared status.Digest against "",
// concluded drift, and called Update -- forever, for as long as the underlying error persisted,
// re-applying identical manifests.
//
// That contradicted the rule the other three drift inputs state explicitly in their own comments: a
// transient failure is NOT drift (restdefinition.go:449, :496, :516). This input is the fourth, and it did
// the opposite INVISIBLY, because its caller cannot distinguish an empty digest from a failed one.
//
// And since the drift branches began reporting Ready=False/Creating with a message naming the drift, the
// consequence stopped being a wasteful loop and became a published WRONG DIAGNOSIS -- "the deployed
// resources drifted from the render; redeploying" -- propagated to the owning composition when nothing
// drifted and nothing needs redeploying.
//
// THE TWO ARMS ARE THE WHOLE TEST, and either alone would be misleading:
//   - a genuine absence must STILL return ("", nil), because that is how a first install reports "nothing
//     deployed yet" and Observe depends on it. A fix that turned every failure into an error would break
//     every first reconcile, and an error-only test would not notice.
//   - a non-NotFound failure must return an error, so the caller says "I could not tell" rather than
//     claiming drift.

func lookupOpts(kube client.Client, log func(string, ...any)) DeployOptions {
	return DeployOptions{
		GVR:                    schema.GroupVersionResource{Group: "github.krateo.io", Version: "v1alpha1", Resource: "repositories"},
		ConfigurationGVR:       schema.GroupVersionResource{Group: "github.krateo.io", Version: "v1alpha1", Resource: "repositoryconfigurations"},
		KubeClient:             kube,
		NamespacedName:         types.NamespacedName{Namespace: "demo-system", Name: "repo"},
		RBACFolderPath:         "testdata",
		DeploymentTemplatePath: "testdata/deploy.yaml",
		ConfigmapTemplatePath:  "testdata/cm.yaml",
		Log:                    log,
	}
}

func lookupScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("building scheme: %v", err)
	}
	return s
}

// ARM 1 — the control, and it is not optional. Nothing deployed means every Get returns NotFound, which
// is a TRUE answer. Lookup must report it as an empty digest with no error.
func TestLookupTreatsAGenuineAbsenceAsAnEmptyDigest(t *testing.T) {
	var logged []string
	kube := fake.NewClientBuilder().WithScheme(lookupScheme(t)).Build()

	digest, err := Lookup(context.Background(), kube, lookupOpts(kube, func(m string, kv ...any) {
		logged = append(logged, m)
	}))

	if err != nil {
		t.Fatalf("a genuine absence must not be an error -- this is how a FIRST INSTALL reports "+
			"\"nothing deployed yet\", and Observe depends on the empty digest: got err=%v", err)
	}
	if digest != "" {
		t.Errorf("expected an empty digest for an absent deployment, got %q", digest)
	}
}

// ARM 2 — the defect. A non-NotFound failure must reach the caller, so Observe can say "I could not tell"
// instead of fabricating drift.
//
// Injected with an interceptor rather than by misnaming objects. A probe that induced the failure by
// guessing object names had all four of its cases silently trip the SAME branch while its control also
// returned "" -- four confident verdicts sharing one broken premise, indistinguishable from the real
// result. An interceptor makes the injected failure the only variable.
func TestLookupPropagatesANonNotFoundFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"Forbidden (an RBAC gap on the generated kinds)", apierrors.NewForbidden(
			schema.GroupResource{Group: "", Resource: "configmaps"}, "repo-controller", errNoAccess)},
		{"Internal (the apiserver is unwell)", apierrors.NewInternalError(errNoAccess)},
		{"Timeout (the apiserver is slow)", apierrors.NewTimeoutError("too slow", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logged []string
			kube := fake.NewClientBuilder().WithScheme(lookupScheme(t)).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						return tc.err
					},
				}).Build()

			digest, err := Lookup(context.Background(), kube, lookupOpts(kube, func(m string, kv ...any) {
				logged = append(logged, m)
			}))

			if err == nil {
				t.Fatalf("a %T must reach the caller: returning (%q, nil) tells Observe the lookup "+
					"SUCCEEDED and found nothing, so it reports drift and calls Update forever -- and "+
					"since the drift branch now sets Ready=False/Creating with \"the deployed resources "+
					"drifted from the render; redeploying\", the misdiagnosis is published to the owning "+
					"composition (#427)", tc.err, digest)
			}
			if digest != "" {
				t.Errorf("a failed lookup must not also return a digest, got %q", digest)
			}

			// The failure must be LOGGED, not merely returned. The first of the three sites logged nothing
			// of its own; the only reason a failure there was visible at all was that a helper logged on
			// its way out.
			var sawLog bool
			for _, m := range logged {
				if strings.Contains(m, "Error") {
					sawLog = true
				}
			}
			if !sawLog {
				t.Errorf("the failure must be logged at the site as well as returned; logged=%v", logged)
			}
		})
	}
}

type constErr string

func (e constErr) Error() string { return string(e) }

const errNoAccess = constErr("repositories.github.krateo.io is forbidden: " +
	"User \"system:serviceaccount:krateo-system:oasgen-provider\" cannot get resource")
