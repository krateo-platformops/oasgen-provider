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

// TestMergePartialFields_MapValuesMayBeAnything distinguishes two things a reader will conflate: a FIELD
// that is an array (out of scope, a no-op) from a field that is a MAP whose VALUES are arrays (merged by
// key, each value taken whole).
//
// Asked by the keycloak-provider-kog session before declaring partialFields on kog-group, whose
// `clientRoles` is {clientId: [roles]}. It was unasserted until they asked.
func TestMergePartialFields_MapValuesMayBeAnything(t *testing.T) {
	// kog-group.clientRoles, as Keycloak shapes it.
	body := map[string]interface{}{
		"clientRoles": map[string]interface{}{
			"krateo-portal": []interface{}{"view", "edit"},
		},
	}
	remote := map[string]interface{}{
		"clientRoles": map[string]interface{}{
			"krateo-portal":    []interface{}{"view"},          // the CR's value must WIN whole
			"realm-management": []interface{}{"manage-users"},  // the vendor's key must SURVIVE
			"account":          map[string]interface{}{"n": 1}, // a non-array value, also survives
		},
	}
	if got := mergePartialFields(body, remote, bodySet("clientRoles")); !reflect.DeepEqual(got, []string{"clientRoles"}) {
		t.Fatalf("merged %v, want [clientRoles]", got)
	}
	out := body["clientRoles"].(map[string]interface{})

	// The CR's array REPLACES the vendor's for the key it owns -- it is not unioned, appended or
	// element-merged. That is the whole reason "an array field is a no-op" and "a map VALUE that is an
	// array is taken whole" are different rules: no element identity is ever needed here.
	if !reflect.DeepEqual(out["krateo-portal"], []interface{}{"view", "edit"}) {
		t.Errorf("krateo-portal = %v, want [view edit]: the CR's array must be taken whole, not merged "+
			"element-wise with the vendor's [view]", out["krateo-portal"])
	}
	if !reflect.DeepEqual(out["realm-management"], []interface{}{"manage-users"}) {
		t.Errorf("realm-management = %v, want [manage-users]: a vendor key the CR does not express must "+
			"survive regardless of what TYPE its value has", out["realm-management"])
	}
	if !reflect.DeepEqual(out["account"], map[string]interface{}{"n": 1}) {
		t.Errorf("account = %v: a nested MAP value is also taken whole -- the merge is one level deep, so "+
			"it does not recurse into it", out["account"])
	}

	// And the contrast, in the same test so the two rules cannot drift apart: the FIELD being an array
	// is still a no-op.
	arrField := map[string]interface{}{"roles": []interface{}{"a"}}
	arrRemote := map[string]interface{}{"roles": []interface{}{"a", "b"}}
	if got := mergePartialFields(arrField, arrRemote, bodySet("roles")); got != nil {
		t.Errorf("a field that IS an array merged %v, want nil", got)
	}
	if !reflect.DeepEqual(arrField["roles"], []interface{}{"a"}) {
		t.Errorf("roles = %v, want [a] unchanged", arrField["roles"])
	}
}
