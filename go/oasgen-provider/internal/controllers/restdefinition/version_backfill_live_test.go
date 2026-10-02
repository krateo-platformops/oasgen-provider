//go:build integration
// +build integration

package restdefinition

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/crd/generation"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

var liveBackfillGVK = schema.GroupVersionKind{
	Group:   "backfill-check.krateo.io",
	Version: "v1-0-28",
	Kind:    "Widget",
}

func liveBackfillCRD() *apiextensionsv1.CustomResourceDefinition {
	preserve := true
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets." + liveBackfillGVK.Group},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: liveBackfillGVK.Group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind: "Widget", ListKind: "WidgetList", Plural: "widgets", Singular: "widget",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: liveBackfillGVK.Version, Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type:                   "object",
						XPreserveUnknownFields: &preserve,
					},
				},
			}},
		},
	}
}

// TestBackfillStampsRealInstancesOnACluster exercises the migration path against a real apiserver.
//
// The unit tests use a fake client, which is enough to pin the RULES -- fill an absence, never move an
// existing value, skip the empty and vacuum versions -- but not enough to claim the path works. A fake
// client does not enforce a schema, does not apply merge-patch semantics the way the apiserver does, and
// cannot show that a label selector on ABSENCE actually selects the objects we think it does.
//
// That distinction stopped being academic when the estate turned out to contain both cases:
//
//   - one cluster where every resource instance is already labelled, so the backfill is a no-op and the
//     path never runs;
//   - another on a much older provider with 36 unlabelled RESOURCE instances and no oas-version policy
//     that has ever existed -- exactly the population this code is for.
//
// The second cluster cannot be used as a test bed (it is production, and the version gap would confound
// any failure), so this is the closest honest substitute: real apiserver, real objects, real selector.
func TestBackfillStampsRealInstancesOnACluster(t *testing.T) {
	const ns = "default"
	const unlabelledCount = 7

	widget := func(name string, labels map[string]string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(liveBackfillGVK)
		u.SetName(name)
		u.SetNamespace(ns)
		if labels != nil {
			u.SetLabels(labels)
		}
		return u
	}

	f := features.New("backfill stamps real instances").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			kube, err := client.New(cfg.Client().RESTConfig(), client.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if err := apiextensionsv1.AddToScheme(kube.Scheme()); err != nil {
				t.Fatal(err)
			}
			if err := kube.Create(ctx, liveBackfillCRD()); err != nil {
				t.Fatalf("creating the test CRD: %v", err)
			}

			// Wait for the endpoint to be servable before writing instances through it.
			deadline := time.Now().Add(60 * time.Second)
			for {
				list := &unstructured.UnstructuredList{}
				list.SetGroupVersionKind(schema.GroupVersionKind{
					Group: liveBackfillGVK.Group, Version: liveBackfillGVK.Version, Kind: "WidgetList",
				})
				if err := kube.List(ctx, list); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the test CRD never became servable")
				}
				time.Sleep(time.Second)
			}

			// The population this exists for: instances written before any policy stamped them.
			for i := 0; i < unlabelledCount; i++ {
				if err := kube.Create(ctx, widget(fmt.Sprintf("pre-policy-%d", i), nil)); err != nil {
					t.Fatalf("creating unlabelled instance %d: %v", i, err)
				}
			}
			// One already owned by an OLDER version, plus an unrelated label that must survive.
			if err := kube.Create(ctx, widget("already-owned", map[string]string{
				generation.VersionLabel: "v1-0-27",
				"unrelated":             "keep-me",
			})); err != nil {
				t.Fatal(err)
			}
			return ctx
		}).
		Assess("every unlabelled instance is stamped, and the owned one is untouched", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			kube, err := client.New(cfg.Client().RESTConfig(), client.Options{})
			if err != nil {
				t.Fatal(err)
			}
			e := &external{kube: kube, log: logging.NewNopLogger(), rec: record.NewFakeRecorder(100)}

			if err := e.backfillVersionLabel(ctx, liveBackfillGVK, liveBackfillGVK.Version); err != nil {
				t.Fatalf("backfill returned an error: %v", err)
			}

			// The assertion that matters on a real cluster: NOTHING is left matching the absence
			// selector. This is the same query an operator runs to decide whether a roll orphaned
			// anything, so it is the claim worth making directly rather than counting patched objects.
			remaining := &unstructured.UnstructuredList{}
			remaining.SetGroupVersionKind(schema.GroupVersionKind{
				Group: liveBackfillGVK.Group, Version: liveBackfillGVK.Version, Kind: "WidgetList",
			})
			sel, err := labelAbsence()
			if err != nil {
				t.Fatal(err)
			}
			if err := kube.List(ctx, remaining, &client.ListOptions{LabelSelector: sel}); err != nil {
				t.Fatalf("listing still-unlabelled instances: %v", err)
			}
			if n := len(remaining.Items); n != 0 {
				names := []string{}
				for i := range remaining.Items {
					names = append(names, remaining.Items[i].GetName())
				}
				t.Fatalf("%d instance(s) still carry no version label after the backfill: %v", n, names)
			}

			// And the one that already named a version was not migrated onto this one.
			owned := &unstructured.Unstructured{}
			owned.SetGroupVersionKind(liveBackfillGVK)
			if err := kube.Get(ctx, client.ObjectKey{Namespace: ns, Name: "already-owned"}, owned); err != nil {
				t.Fatal(err)
			}
			if got := owned.GetLabels()[generation.VersionLabel]; got != "v1-0-27" {
				t.Fatalf("an instance already owned by v1-0-27 was moved to %q; the backfill fills an absence and must never migrate", got)
			}
			if got := owned.GetLabels()["unrelated"]; got != "keep-me" {
				t.Fatalf("the merge patch dropped an unrelated label: unrelated=%q", got)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			kube, err := client.New(cfg.Client().RESTConfig(), client.Options{})
			if err != nil {
				return ctx
			}
			_ = apiextensionsv1.AddToScheme(kube.Scheme())
			_ = kube.Delete(ctx, liveBackfillCRD())
			return ctx
		}).Feature()

	testenv.Test(t, f)
}
