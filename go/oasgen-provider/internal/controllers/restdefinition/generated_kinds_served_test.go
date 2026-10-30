package restdefinition

import (
	"context"
	"errors"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// servingMapper resolves exactly the GVKs it is given and returns a NoMatchError for anything else -- which
// is what the apiserver's RESTMapper does for a kind, or a VERSION, it is not serving.
type servingMapper struct {
	apimeta.RESTMapper
	serving []schema.GroupVersionKind
	// failWith, when set, is returned for every lookup instead. It stands for discovery being
	// unreachable rather than for a kind being absent.
	failWith error
}

func (m servingMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*apimeta.RESTMapping, error) {
	if m.failWith != nil {
		return nil, m.failWith
	}
	for _, gvk := range m.serving {
		if gvk.GroupKind() != gk {
			continue
		}
		// The VERSION must match too. This is the whole point: the two generated halves are served at
		// different versions, so a mapper that matched on GroupKind alone would make the test pass for
		// the exact bug it exists to catch.
		for _, v := range versions {
			if gvk.Version == v {
				return &apimeta.RESTMapping{GroupVersionKind: gvk}, nil
			}
		}
	}
	return nil, &apimeta.NoKindMatchError{GroupKind: gk, SearchedVersions: versions}
}

func externalWithMapper(t *testing.T, m apimeta.RESTMapper) *external {
	t.Helper()
	return &external{
		kube: fake.NewClientBuilder().WithRESTMapper(m).Build(),
		log:  logging.NewNopLogger(),
	}
}

func gvk(group, version, kind string) schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: group, Version: version, Kind: kind}
}

func rdWithStatus(resourceAPIVersion, resourceKind, cfgAPIVersion, cfgKind string) *definitionv1alpha1.RestDefinition {
	cr := &definitionv1alpha1.RestDefinition{}
	cr.Name = "nova-flavor"
	cr.Status.Resource = definitionv1alpha1.KindApiVersion{APIVersion: resourceAPIVersion, Kind: resourceKind}
	cr.Status.Configuration = definitionv1alpha1.KindApiVersion{APIVersion: cfgAPIVersion, Kind: cfgKind}
	return cr
}

const (
	grp    = "nova.openstack.krateo.io"
	resAPI = grp + "/v2-1"
	cfgAPI = grp + "/v1alpha1"
)

func TestGeneratedKindsServed(t *testing.T) {
	cases := []struct {
		name        string
		mapper      apimeta.RESTMapper
		cr          *definitionv1alpha1.RestDefinition
		wantServed  bool
		wantPending string
		wantErr     bool
	}{
		{
			name: "both halves served at their own versions",
			mapper: servingMapper{serving: []schema.GroupVersionKind{
				gvk(grp, "v2-1", "ComputeFlavor"),
				gvk(grp, "v1alpha1", "ComputeFlavorConfiguration"),
			}},
			cr:         rdWithStatus(resAPI, "ComputeFlavor", cfgAPI, "ComputeFlavorConfiguration"),
			wantServed: true,
		},
		{
			// The motivating measurement: Ready went true while the CRD had not appeared.
			name:        "resource half not served yet",
			mapper:      servingMapper{serving: []schema.GroupVersionKind{gvk(grp, "v1alpha1", "ComputeFlavorConfiguration")}},
			cr:          rdWithStatus(resAPI, "ComputeFlavor", cfgAPI, "ComputeFlavorConfiguration"),
			wantServed:  false,
			wantPending: gvk(grp, "v2-1", "ComputeFlavor").String(),
		},
		{
			// The case that defeated a weaker gate: the CRD exists and is Established, but at the WRONG
			// version. A GroupKind-only check would call this served.
			name:        "resource kind served, but at a different version",
			mapper:      servingMapper{serving: []schema.GroupVersionKind{gvk(grp, "v1alpha1", "ComputeFlavor")}},
			cr:          rdWithStatus(resAPI, "ComputeFlavor", "", ""),
			wantServed:  false,
			wantPending: gvk(grp, "v2-1", "ComputeFlavor").String(),
		},
		{
			name:        "configuration half not served yet",
			mapper:      servingMapper{serving: []schema.GroupVersionKind{gvk(grp, "v2-1", "ComputeFlavor")}},
			cr:          rdWithStatus(resAPI, "ComputeFlavor", cfgAPI, "ComputeFlavorConfiguration"),
			wantServed:  false,
			wantPending: gvk(grp, "v1alpha1", "ComputeFlavorConfiguration").String(),
		},
		{
			// An empty Status.Configuration means NO Configuration CRD was generated -- only
			// configurationFields or security schemes produce one. Treating it as unknown would wedge
			// every definition without configuration in Creating forever.
			name:       "empty configuration status is skipped, not treated as unserved",
			mapper:     servingMapper{serving: []schema.GroupVersionKind{gvk(grp, "v2-1", "ComputeFlavor")}},
			cr:         rdWithStatus(resAPI, "ComputeFlavor", "", ""),
			wantServed: true,
		},
		{
			// "Could not look" must not read as "not served": reporting Creating on every discovery
			// hiccup would make a healthy install flap.
			name:    "discovery failure is an error, not a negative answer",
			mapper:  servingMapper{failWith: errors.New("etcdserver: request timed out")},
			cr:      rdWithStatus(resAPI, "ComputeFlavor", "", ""),
			wantErr: true,
		},
		{
			name:       "status not yet recorded leaves nothing to check",
			mapper:     servingMapper{failWith: errors.New("must not be called")},
			cr:         rdWithStatus("", "", "", ""),
			wantServed: true,
		},
		{
			name:    "an unparseable apiVersion we wrote ourselves is reported",
			mapper:  servingMapper{},
			cr:      rdWithStatus("a/b/c", "ComputeFlavor", "", ""),
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := externalWithMapper(t, tc.mapper)
			served, pending, err := e.generatedKindsServed(context.Background(), tc.cr)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got served=%v pending=%q", served, pending)
				}
				if served {
					t.Error("served must be false alongside an error: an undetermined answer is not an affirmative one")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if served != tc.wantServed {
				t.Errorf("served = %v, want %v", served, tc.wantServed)
			}
			if pending != tc.wantPending {
				t.Errorf("pending = %q, want %q", pending, tc.wantPending)
			}
		})
	}
}
