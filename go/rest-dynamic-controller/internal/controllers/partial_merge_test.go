package restResources

import (
	"reflect"
	"testing"
)

// TestMergePartialFields_TheMeasuredIronicCase is the live measurement turned into an assertion,
// including the arm that made it attributable.
func TestMergePartialFields_TheMeasuredIronicCase(t *testing.T) {
	remote := map[string]interface{}{
		"extra": map[string]interface{}{
			"kubeadm_join": "j", "cert_key": "c",
			"etcd_ip": "10.0.0.5", "cp_endpoint": "https://10.0.0.9:6443",
			"deployed_configdrive_hash": "abc123",
		},
	}

	t.Run("blade01: the CR's 2 keys win, the vendor's other 3 survive", func(t *testing.T) {
		body := map[string]interface{}{
			"extra": map[string]interface{}{"kubeadm_join": "NEW", "cert_key": "c"},
		}
		got := mergePartialFields(body, remote, bodySet("extra"))
		if !reflect.DeepEqual(got, []string{"extra"}) {
			t.Fatalf("merged %v, want [extra]", got)
		}
		want := map[string]interface{}{
			"kubeadm_join": "NEW", // the CR wins
			"cert_key":     "c",
			"etcd_ip":      "10.0.0.5", "cp_endpoint": "https://10.0.0.9:6443",
			"deployed_configdrive_hash": "abc123",
		}
		if !reflect.DeepEqual(body["extra"], want) {
			t.Errorf("merged body:\n got %v\nwant %v", body["extra"], want)
		}
	})

	t.Run("blades 02/03: a field the CR does not express is NOT invented", func(t *testing.T) {
		// The live control: these blades were sent no `extra` at all and kept their keys. A merge that
		// materialised the vendor's value into the body would start SENDING a field the CR never
		// mentioned -- turning a resource that touches nothing into one that writes the whole object.
		body := map[string]interface{}{"name": "blade02"}
		if got := mergePartialFields(body, remote, bodySet("extra")); got != nil {
			t.Fatalf("merged %v, want nil", got)
		}
		if _, present := body["extra"]; present {
			t.Error("extra was ADDED to a body that did not carry it: the request would now replace a " +
				"field this CR has no opinion about")
		}
	})

	t.Run("a COMPLETE spec.extra is not reported as merged", func(t *testing.T) {
		body := map[string]interface{}{
			"extra": map[string]interface{}{
				"kubeadm_join": "j", "cert_key": "c", "etcd_ip": "x",
				"cp_endpoint": "y", "deployed_configdrive_hash": "z",
			},
		}
		if got := mergePartialFields(body, remote, bodySet("extra")); got != nil {
			t.Fatalf("merged %v, want nil: the CR already expressed every key, so reporting a merge "+
				"would claim a rescue that did not happen", got)
		}
	})
}

// TestMergePartialFields_OnlyDeclaredFields is the scope assertion, and it can fail: the two fields are
// otherwise IDENTICAL, so only the declared set can distinguish them.
func TestMergePartialFields_OnlyDeclaredFields(t *testing.T) {
	body := map[string]interface{}{
		"declared":   map[string]interface{}{"a": 1},
		"undeclared": map[string]interface{}{"a": 1},
	}
	remote := map[string]interface{}{
		"declared":   map[string]interface{}{"a": 1, "vendorOnly": 2},
		"undeclared": map[string]interface{}{"a": 1, "vendorOnly": 2},
	}
	got := mergePartialFields(body, remote, bodySet("declared"))
	if !reflect.DeepEqual(got, []string{"declared"}) {
		t.Fatalf("merged %v, want [declared]", got)
	}
	if _, kept := body["undeclared"].(map[string]interface{})["vendorOnly"]; kept {
		t.Error("an UNDECLARED field was merged: declaring partialFields is what opts a field in, and " +
			"merging without it would change every kind's update silently")
	}
}

// TestMergePartialFields_NonMapsAndEmptyInputs covers the cases with nothing to preserve, including the
// array case, which is OUT OF SCOPE rather than overlooked.
func TestMergePartialFields_NonMapsAndEmptyInputs(t *testing.T) {
	body := map[string]interface{}{
		"scalar":      "a",
		"list":        []interface{}{"a"},
		"mapVsScalar": map[string]interface{}{"a": 1},
	}
	remote := map[string]interface{}{
		"scalar":      "b",
		"list":        []interface{}{"a", "b"},
		"mapVsScalar": "b",
	}
	if got := mergePartialFields(body, remote, bodySet("scalar", "list", "mapVsScalar")); got != nil {
		t.Fatalf("merged %v, want nil.\n"+
			"A scalar has no keys. An ARRAY is replayed whole too and can lose elements, but merging one "+
			"needs element identity, and this repo has three notions in flight -- choosing one here would "+
			"establish it for the warning as well. A type disagreement is the drift comparison's business.", got)
	}

	full := map[string]interface{}{"m": map[string]interface{}{"a": 1}}
	for name, tc := range map[string]struct{ body, remote map[string]interface{} }{
		"no body":   {nil, full},
		"no remote": {full, nil},
		"neither":   {nil, nil},
	} {
		if got := mergePartialFields(tc.body, tc.remote, bodySet("m")); got != nil {
			t.Errorf("%s: merged %v, want nil", name, got)
		}
	}
	if got := mergePartialFields(full, full, bodySet()); got != nil {
		t.Errorf("empty declared set: merged %v, want nil -- a kind that declares nothing must be unchanged", got)
	}
}
