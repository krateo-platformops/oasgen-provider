package restResources

import (
	"reflect"
	"testing"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/text"
)

func bodySet(names ...string) text.StringSet {
	s := text.StringSet{}
	for _, n := range names {
		s[n] = struct{}{}
	}
	return s
}

// TestRemoteOnlyBodyKeys_TheMeasuredIroniCase reproduces the shape that was measured on a live cluster,
// including the CONTROL that made it attributable: blade01 had a non-empty spec.extra and lost keys,
// blades 02 and 03 had no spec.extra at all and kept theirs.
//
// Both arms are in one test on purpose. A test with only the positive arm would pass under an
// implementation that reports every field unconditionally, which is the failure mode that makes a
// data-loss warning worthless.
func TestRemoteOnlyBodyKeys_TheMeasuredIronicCase(t *testing.T) {
	remote := map[string]interface{}{
		"extra": map[string]interface{}{
			"kubeadm_join":              "...",
			"cert_key":                  "...",
			"etcd_ip":                   "10.0.0.5",
			"cp_endpoint":               "https://10.0.0.9:6443",
			"deployed_configdrive_hash": "abc123",
		},
	}
	body := bodySet("extra")

	t.Run("blade01: a partial spec.extra reports exactly the unmodelled keys", func(t *testing.T) {
		spec := map[string]interface{}{
			"extra": map[string]interface{}{"kubeadm_join": "...", "cert_key": "..."},
		}
		got := remoteOnlyBodyKeys(spec, remote, body)
		want := map[string][]string{"extra": {"cp_endpoint", "deployed_configdrive_hash", "etcd_ip"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("blade02/03: NO spec.extra reports nothing", func(t *testing.T) {
		// The negative arm of the live measurement. These blades were sent no `extra` at all and kept
		// their keys, so there is nothing to warn about -- and a warning here would fire on every CR of
		// every kind that does not model some field the vendor holds, which is most of them.
		spec := map[string]interface{}{"name": "blade02"}
		if got := remoteOnlyBodyKeys(spec, remote, body); got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})

	t.Run("a COMPLETE spec.extra reports nothing", func(t *testing.T) {
		spec := map[string]interface{}{
			"extra": map[string]interface{}{
				"kubeadm_join": "...", "cert_key": "...", "etcd_ip": "...",
				"cp_endpoint": "...", "deployed_configdrive_hash": "...",
			},
		}
		if got := remoteOnlyBodyKeys(spec, remote, body); got != nil {
			t.Fatalf("got %v, want nil: a field the CR fully expresses loses nothing and must stay silent", got)
		}
	})
}

// TestRemoteOnlyBodyKeys_OnlyWhatTheUpdateSends is the scope assertion, and it can fail.
//
// A field the update verb does not put in its body is never sent, so it cannot be deleted by one. A
// warning about it would be a false positive -- and a false positive in a warning about DATA LOSS is
// worse than silence, because it trains the reader to ignore the real one.
func TestRemoteOnlyBodyKeys_OnlyWhatTheUpdateSends(t *testing.T) {
	spec := map[string]interface{}{
		"sent":    map[string]interface{}{"a": 1},
		"notSent": map[string]interface{}{"a": 1},
	}
	remote := map[string]interface{}{
		"sent":    map[string]interface{}{"a": 1, "vendorOnly": 2},
		"notSent": map[string]interface{}{"a": 1, "vendorOnly": 2},
	}

	got := remoteOnlyBodyKeys(spec, remote, bodySet("sent"))
	want := map[string][]string{"sent": {"vendorOnly"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v: only the field the update body carries may be reported", got, want)
	}

	// CONTROL: the two fields are otherwise IDENTICAL, so the only thing that can distinguish them is the
	// body set. If the implementation ignored the set, the assertion above would have failed with both
	// present rather than passing for the wrong reason.
	if _, reported := got["notSent"]; reported {
		t.Error("a field outside the update body was reported")
	}
}

// TestRemoteOnlyBodyKeys_NonMapValuesAreNotReported covers the cases where there is nothing to lose, and
// the array case specifically, which is OUT OF SCOPE rather than overlooked.
func TestRemoteOnlyBodyKeys_NonMapValuesAreNotReported(t *testing.T) {
	body := bodySet("scalar", "list", "specMapRemoteScalar", "specScalarRemoteMap")
	spec := map[string]interface{}{
		"scalar":              "a",
		"list":                []interface{}{"a"},
		"specMapRemoteScalar": map[string]interface{}{"a": 1},
		"specScalarRemoteMap": "a",
	}
	remote := map[string]interface{}{
		"scalar":              "b",
		"list":                []interface{}{"a", "b"},
		"specMapRemoteScalar": "b",
		"specScalarRemoteMap": map[string]interface{}{"a": 1, "vendorOnly": 2},
	}

	if got := remoteOnlyBodyKeys(spec, remote, body); got != nil {
		t.Fatalf("got %v, want nil.\n"+
			"A scalar has no keys. An ARRAY is replayed whole too and CAN lose elements, but naming which "+
			"one needs a notion of element identity, and this repo has three in flight (positional, "+
			"set-typed arrays, map-list keys) -- picking one inside a warning would quietly establish it "+
			"for the merge as well, so arrays get their own decision. A type disagreement between spec "+
			"and remote is a different defect that the drift comparison already reports.", got)
	}
}

// TestRemoteOnlyBodyKeys_SortedForAStableMessage pins the ordering, which is load-bearing rather than
// cosmetic: the report fires once per generation keyed on its own text, so a message whose key order
// churns between reconciles would defeat the key and re-warn forever.
func TestRemoteOnlyBodyKeys_SortedForAStableMessage(t *testing.T) {
	spec := map[string]interface{}{"m": map[string]interface{}{}}
	remote := map[string]interface{}{
		"m": map[string]interface{}{"zulu": 1, "alpha": 2, "mike": 3},
	}
	got := remoteOnlyBodyKeys(spec, remote, bodySet("m"))
	want := []string{"alpha", "mike", "zulu"}
	if !reflect.DeepEqual(got["m"], want) {
		t.Fatalf("got %v, want %v", got["m"], want)
	}
}

// TestRemoteOnlyBodyKeys_EmptyInputsAreSilent keeps the caller's zero-write path zero-write.
func TestRemoteOnlyBodyKeys_EmptyInputsAreSilent(t *testing.T) {
	full := map[string]interface{}{"m": map[string]interface{}{"a": 1}}
	for name, tc := range map[string]struct{ spec, remote map[string]interface{} }{
		"no spec":   {nil, full},
		"no remote": {full, nil},
		"neither":   {nil, nil},
	} {
		if got := remoteOnlyBodyKeys(tc.spec, tc.remote, bodySet("m")); got != nil {
			t.Errorf("%s: got %v, want nil", name, got)
		}
	}
}
