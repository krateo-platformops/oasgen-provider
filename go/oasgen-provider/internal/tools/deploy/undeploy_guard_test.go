package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var repoGVR = schema.GroupVersionResource{Group: "github.krateo.io", Version: "v1alpha1", Resource: "repositories"}
var repoGVK = schema.GroupVersionKind{Group: "github.krateo.io", Version: "v1alpha1", Kind: "Repository"}

func newRepoScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(repoGVK, &unstructured.Unstructured{})
	lg := repoGVK
	lg.Kind += "List"
	s.AddKnownTypeWithName(lg, &unstructured.UnstructuredList{})
	return s
}

func newRepo(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(repoGVK)
	u.SetName(name)
	u.SetNamespace("default")
	return u
}

// TestUndeploy_RefusesToDropCRDWithLiveInstances covers #125, which is the only defect in this repo's
// backlog capable of destroying production assets irreversibly.
//
// Uninstalling a CRD cascade-deletes every CR of that kind, and those CRs carry finalizers that delete
// the EXTERNAL resource. So deleting a RestDefinition could delete real GitHub repositories — no per-CR
// opt-in, nothing to undo it, and triggered by a routine lifecycle operation.
//
// A guard already existed (the restresources-still-exist finalizer) but was consulted only for
// SkipDeploy, never for SkipCRD, and reflected a PREVIOUS reconcile's observation rather than the
// cluster's state at the moment of deletion.
func TestUndeploy_RefusesToDropCRDWithLiveInstances(t *testing.T) {
	logf := func(msg string, kv ...any) {}

	t.Run("live instances: refuses, and says why", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(newRepoScheme(t)).
			WithObjects(newRepo("publish-pet-repo"), newRepo("another")).Build()

		err := Undeploy(context.Background(), kube, UndeployOptions{
			KubeClient: kube, GVR: repoGVR, GVK: repoGVK, Log: logf, SkipCRD: false, SkipDeploy: true,
		})

		if err == nil {
			t.Fatal("Undeploy must REFUSE to uninstall a CRD with live instances: doing so cascade-deletes " +
				"the CRs and their finalizers delete the real external resources (#125)")
		}
		if !errors.Is(err, ErrLiveInstances) {
			t.Errorf("refusal must be identifiable as ErrLiveInstances, got: %v", err)
		}
		if !strings.Contains(err.Error(), "2 live instance") {
			t.Errorf("the error should name how many instances blocked it, got: %v", err)
		}
	})

	t.Run("no instances: proceeds", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(newRepoScheme(t)).Build()

		err := Undeploy(context.Background(), kube, UndeployOptions{
			KubeClient: kube, GVR: repoGVR, GVK: repoGVK, Log: logf, SkipCRD: false, SkipDeploy: true,
		})

		// It gets past the guard. crd.Uninstall against a fake client may still fail for unrelated
		// reasons, so the assertion is narrow: whatever happens, it must NOT be the live-instance refusal.
		if errors.Is(err, ErrLiveInstances) {
			t.Errorf("must not refuse when no instances exist, got: %v", err)
		}
	})

	t.Run("SkipCRD short-circuits the guard entirely", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(newRepoScheme(t)).
			WithObjects(newRepo("publish-pet-repo")).Build()

		err := Undeploy(context.Background(), kube, UndeployOptions{
			KubeClient: kube, GVR: repoGVR, GVK: repoGVK, Log: logf, SkipCRD: true, SkipDeploy: true,
		})

		if errors.Is(err, ErrLiveInstances) {
			t.Errorf("SkipCRD means the caller is not touching the CRD at all, so the guard is irrelevant: %v", err)
		}
	})
}

// TestLiveInstanceCount_NoKindRefuses pins that an unlistable request refuses rather than reporting
// zero. A zero here would authorise dropping the CRD, which is the destructive act this guard exists to
// prevent — so "I could not look" must never be spelled the same way as "there was nothing there".
func TestLiveInstanceCount_NoKindRefuses(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(newRepoScheme(t)).WithObjects(newRepo("r")).Build()

	if _, err := liveInstanceCount(context.Background(), kube, schema.GroupVersionKind{}, repoGVR.GroupResource()); err == nil {
		t.Error("a missing Kind must be an error, not a count of zero: zero would authorise the delete")
	}
}

// THE CRD NAME, AS THE APISERVER REQUIRES IT: `<plural>.<group>`.
//
// Deliberately a literal and not `repoGVR.GroupResource().String()`, which is the expression the code
// under test uses to build the name it asks about. Deriving the fixture from that expression would make
// the two halves agree by construction and leave the test blind to the naming convention being wrong --
// the authority here is the apiserver, which rejects any other name, so the literal is the ground truth.
const repoCRDName = "repositories.github.krateo.io"

// listableScheme knows the generated kind AND CustomResourceDefinition.
//
// Both halves are needed. The kind, so that a list can SUCCEED and return a real count -- the table below
// has a row for that, and without it every row would be a refusal and the suite would pass against a
// function that never counts anything. And apiextensions, because `crd.Get` registers it into client-go's
// GLOBAL clientset scheme, which is not the fake client's: without it the CRD question would fail with
// "no kind is registered" and every refusal below would be the right outcome for the wrong reason.
func listableScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := newRepoScheme(t)
	if err := apiextensionsv1.AddToScheme(s); err != nil {
		t.Fatalf("registering apiextensions in the test scheme: %v", err)
	}
	return s
}

func newRepoCRDObject() *apiextensionsv1.CustomResourceDefinition {
	obj := &apiextensionsv1.CustomResourceDefinition{}
	obj.SetName(repoCRDName)
	return obj
}

// WHY THE LISTING FAILURES ARE INJECTED RATHER THAN PROVOKED.
//
// The test this replaced built a client with an EMPTY scheme and its comment said that "reproduces 'no
// matches for kind'". It does not: controller-runtime's fake client returns a nil error and an empty list
// for an unregistered unstructured kind. So that test asserted "an empty list counts zero" while claiming
// to cover the no-match path, and the no-match arm of this function -- including the two `strings.Contains`
// arms #609 removed -- had NO coverage at all. Measured: with the fix in place and the CRD seeded, that
// client still returned (0, nil), because the branch was never entered.
//
// A real `*apimeta.NoKindMatchError` is what `apimeta.IsNoMatchError` recognises, so it is what gets
// injected, following the same pattern as the #576 discovery-guard tests.
func noMatchErr() error {
	return &apimeta.NoKindMatchError{
		GroupKind:        repoGVK.GroupKind(),
		SearchedVersions: []string{repoGVK.Version},
	}
}

func notFoundErr() error {
	return apierrors.NewNotFound(repoGVR.GroupResource(), "")
}

func forbiddenErr() error {
	return apierrors.NewForbidden(repoGVR.GroupResource(), "", errors.New("no permission"))
}

// TestLiveInstanceCount_AnUnlistableKindAsksWhetherItsCRDExists is the #609 fix.
//
// A list that says "no such kind" used to return a bare zero, and a zero authorises uninstalling the CRD
// -- which cascade-deletes every CR of that kind and runs each one's external-resource finalizer. But that
// listing failure has two causes with OPPOSITE correct outcomes, and the only thing separating them is
// whether the CustomResourceDefinition is still there:
//
//   - CRD gone      -> the kind cannot have instances, so zero is right;
//   - CRD present   -> instances may exist and be invisible to this client (a discovery cache older than
//     the CRD, an apiserver not yet serving it, discovery erroring under load), so zero would delete them.
//
// Every row below pairs an injected listing failure with the presence or absence of the CRD, so the two
// causes are varied independently and each outcome has a row that produces it.
func TestLiveInstanceCount_AnUnlistableKindAsksWhetherItsCRDExists(t *testing.T) {
	cases := []struct {
		name     string
		listErr  error
		seedCRD  bool
		seedRepo int
		wantN    int
		wantErr  bool
		wantMsg  string
		why      string
	}{
		{
			name: "the list SUCCEEDS: it counts", seedRepo: 2, wantN: 2,
			why: "without this row every row refuses, and the suite would pass against a function that " +
				"never returns a count at all",
		},
		{
			name:    "no-match and the CRD is genuinely ABSENT: zero is correct",
			listErr: noMatchErr(), seedCRD: false, wantN: 0,
			why: "the CRD is gone, so the kind cannot have instances and there is nothing to protect",
		},
		{
			name:    "no-match but the CRD EXISTS: refuses",
			listErr: noMatchErr(), seedCRD: true, wantErr: true, wantMsg: "unlistable",
			why: "THE #609 CASE. Instances may exist and be unlistable by this client; zero would " +
				"authorise uninstalling the CRD and cascade-deleting them",
		},
		{
			name:    "NotFound and the CRD is genuinely ABSENT: zero is correct",
			listErr: notFoundErr(), seedCRD: false, wantN: 0,
			why: "the same reasoning as no-match: the apiserver has no such resource because the CRD is gone",
		},
		{
			name:    "NotFound but the CRD EXISTS: refuses",
			listErr: notFoundErr(), seedCRD: true, wantErr: true, wantMsg: "unlistable",
			why: "a 404 on the resource endpoint while the CRD object is present is precisely the " +
				"not-yet-served window, where instances of an older served version can exist",
		},
		{
			name:    "Forbidden with the CRD absent: propagates, and does NOT become a zero",
			listErr: forbiddenErr(), seedCRD: false, wantErr: true, wantMsg: "forbidden",
			why: "an RBAC failure is not an answer about instances. The CRD question must not even be " +
				"asked -- an absent CRD must not convert a permission error into an authorised delete",
		},
		{
			name:    "Forbidden with the CRD present: propagates unchanged",
			listErr: forbiddenErr(), seedCRD: true, wantErr: true, wantMsg: "forbidden",
			why: "the operator must see the RBAC cause, not a message about the CRD",
		},
	}

	// The table must be able to fail in BOTH directions, asserted rather than assumed: a table of
	// all-refusal rows passes against a function that refuses unconditionally, which is just as broken.
	var wantErrRows, wantOKRows int
	for _, tc := range cases {
		if tc.wantErr {
			wantErrRows++
		} else {
			wantOKRows++
		}
	}
	if wantErrRows == 0 {
		t.Fatal("no row expects a refusal: this table passes against a function that always counts zero")
	}
	if wantOKRows == 0 {
		t.Fatal("no row expects an answer: this table passes against a function that always refuses")
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := fake.NewClientBuilder().WithScheme(listableScheme(t))
			objs := []client.Object{}
			if tc.seedCRD {
				objs = append(objs, newRepoCRDObject())
			}
			for i := 0; i < tc.seedRepo; i++ {
				objs = append(objs, newRepo(fmt.Sprintf("repo-%d", i)))
			}
			if len(objs) > 0 {
				b = b.WithObjects(objs...)
			}
			if tc.listErr != nil {
				b = b.WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						return tc.listErr
					},
				})
			}
			kube := b.Build()

			n, err := liveInstanceCount(context.Background(), kube, repoGVK, repoGVR.GroupResource())

			if !tc.wantErr {
				if err != nil {
					t.Fatalf("expected an answer, got refusal %q -- %s", err, tc.why)
				}
				if n != tc.wantN {
					t.Errorf("expected %d instances, got %d -- %s", tc.wantN, n, tc.why)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected a refusal, got %d instances -- %s", n, tc.why)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.wantMsg) {
				t.Errorf("the refusal must say %q so whoever debugs it knows which situation they are in; "+
					"got: %v -- %s", tc.wantMsg, err, tc.why)
			}
			if n != 0 {
				t.Errorf("a refusal must not also report a count, got %d", n)
			}
		})
	}

	t.Run("a Forbidden list never reaches the CRD question", func(t *testing.T) {
		// Asserted on the ground truth -- whether a Get for the CRD was issued -- rather than inferred
		// from the error message. The fix must not have widened which failures consult the CRD.
		var crdGets int
		kube := fake.NewClientBuilder().WithScheme(listableScheme(t)).
			WithObjects(newRepoCRDObject()).
			WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					return forbiddenErr()
				},
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, isCRD := obj.(*apiextensionsv1.CustomResourceDefinition); isCRD {
						crdGets++
					}
					return c.Get(ctx, key, obj, opts...)
				},
			}).Build()

		if _, err := liveInstanceCount(context.Background(), kube, repoGVK, repoGVR.GroupResource()); err == nil {
			t.Fatal("a Forbidden list must refuse")
		}
		if crdGets != 0 {
			t.Errorf("the CRD was consulted %d time(s) for a Forbidden list. Only a no-match/NotFound may "+
				"reach that question; widening it means a permission error with an absent CRD would read "+
				"as an authorised zero", crdGets)
		}
	})

	t.Run("the CRD question itself fails: refuses, and differently", func(t *testing.T) {
		// NOT a NotFound, which is a real answer ("absent"). This is the third outcome -- the question
		// could not be asked -- and it must be spelled differently from the other two, or the two
		// refusals read as the same situation to whoever debugs them.
		kube := fake.NewClientBuilder().WithScheme(listableScheme(t)).
			WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					return noMatchErr()
				},
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, isCRD := obj.(*apiextensionsv1.CustomResourceDefinition); isCRD {
						return apierrors.NewInternalError(errors.New("etcd is unavailable"))
					}
					return c.Get(ctx, key, obj, opts...)
				},
			}).Build()

		_, err := liveInstanceCount(context.Background(), kube, repoGVK, repoGVR.GroupResource())
		if err == nil {
			t.Fatal("whether the CRD exists could not be determined, so neither zero nor a count is " +
				"knowable: this must refuse")
		}
		if !strings.Contains(err.Error(), "could not be determined") {
			t.Errorf("an unanswerable CRD question must be distinguishable from a CRD that is present, "+
				"got: %v", err)
		}
	})

	t.Run("an incomplete group-resource refuses instead of asking a question that can only say absent", func(t *testing.T) {
		// `schema.GroupResource.String()` returns the BARE RESOURCE when the group is empty, and no CRD is
		// ever named that way -- so the Get would be a guaranteed NotFound, read as "the CRD is absent",
		// and would authorise the delete. The dangerous answer must not be reachable from malformed input.
		for _, gr := range []schema.GroupResource{
			{Resource: "repositories"},  // no group
			{Group: "github.krateo.io"}, // no resource
			{},                          // neither
		} {
			kube := fake.NewClientBuilder().WithScheme(listableScheme(t)).
				WithObjects(newRepoCRDObject()).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						return noMatchErr()
					},
				}).Build()

			if _, err := liveInstanceCount(context.Background(), kube, repoGVK, gr); err == nil {
				t.Errorf("group-resource %#v cannot name a CRD, so the check is impossible and must refuse "+
					"rather than report a zero that authorises the delete", gr)
			}
		}
	})
}

// TestUndeploy_DoesNotUninstallWhenTheKindIsUnlistable asserts the CONSEQUENCE, not the return value.
//
// The question that matters is whether a DELETE reached the CustomResourceDefinition. Asserting only that
// Undeploy returned an error would pass against a version that refused AFTER uninstalling, and the
// uninstall is the irreversible half: for github-provider-kog the cascade issues
// `DELETE /repos/{owner}/{repo}`, losing code and history; for openstack-ironic-operator-kog it
// deprovisions bare-metal blades whose provisioned state is not in git.
//
// The second arm is the control that MUST delete. Without it this test also passes against an Undeploy
// that uninstalls nothing under any circumstances -- the same green for the opposite reason.
func TestUndeploy_DoesNotUninstallWhenTheKindIsUnlistable(t *testing.T) {
	logf := func(msg string, kv ...any) {}

	build := func(t *testing.T, withCRD bool, deletes *[]string) client.WithWatch {
		t.Helper()
		b := fake.NewClientBuilder().WithScheme(listableScheme(t)).
			WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					return noMatchErr()
				},
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					if _, isCRD := obj.(*apiextensionsv1.CustomResourceDefinition); isCRD {
						*deletes = append(*deletes, obj.GetName())
					}
					return c.Delete(ctx, obj, opts...)
				},
			})
		if withCRD {
			b = b.WithObjects(newRepoCRDObject())
		}
		return b.Build()
	}

	t.Run("CRD present and the kind unlistable: no DELETE reaches the CRD", func(t *testing.T) {
		var deletes []string
		kube := build(t, true, &deletes)

		err := Undeploy(context.Background(), kube, UndeployOptions{
			KubeClient: kube, GVR: repoGVR, GVK: repoGVK, Log: logf, SkipCRD: false, SkipDeploy: true,
		})

		if err == nil {
			t.Error("Undeploy must refuse when it cannot tell whether instances exist")
		}
		if len(deletes) != 0 {
			t.Fatalf("a DELETE reached the CustomResourceDefinition %v. Uninstalling it cascade-deletes "+
				"every CR of the kind and runs each one's external-resource finalizer (#609)", deletes)
		}

		// And the CRD must still be there, asked of the cluster rather than of the harness: a count of
		// zero DELETEs is this test's own bookkeeping, while the object's presence is the fact.
		live := &apiextensionsv1.CustomResourceDefinition{}
		if gerr := kube.Get(context.Background(), client.ObjectKey{Name: repoCRDName}, live); gerr != nil {
			t.Errorf("the CRD is gone after a refusal: %v", gerr)
		}
	})

	t.Run("CONTROL -- CRD absent: the guard does not stop the uninstall", func(t *testing.T) {
		var deletes []string
		kube := build(t, false, &deletes)

		err := Undeploy(context.Background(), kube, UndeployOptions{
			KubeClient: kube, GVR: repoGVR, GVK: repoGVK, Log: logf, SkipCRD: false, SkipDeploy: true,
		})

		// crd.Uninstall against an already-absent CRD may itself error; what must NOT happen is the guard
		// refusing, because a gone CRD cannot have instances.
		if err != nil && strings.Contains(err.Error(), "unlistable") {
			t.Errorf("the guard refused although the CRD is absent, so the kind cannot have instances: %v", err)
		}
		if err != nil && strings.Contains(err.Error(), "could not be determined") {
			t.Errorf("the guard treated an absent CRD as an unanswerable question: %v", err)
		}
	})
}
