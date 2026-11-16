package restResources

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

// TestPartialFieldsFor_PerVerb asserts the declaration is scoped to ONE verb. The three verbs below
// declare DIFFERENT fields, so a lookup that ignored the action would return a union and this fails.
func TestPartialFieldsFor_PerVerb(t *testing.T) {
	verbs := []getter.VerbsDescription{
		{Action: "create", PartialFields: []string{"createOnly"}},
		{Action: "update", PartialFields: []string{"extra", "config"}},
		{Action: "get"},
	}
	for _, tc := range []struct {
		action string
		want   []string
	}{
		{"update", []string{"config", "extra"}},
		{"create", []string{"createOnly"}},
		{"get", nil},
		{"delete", nil},
		{"UPDATE", []string{"config", "extra"}}, // the action is matched case-insensitively, as elsewhere
	} {
		got := keys(partialFieldsFor(verbs, tc.action))
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("action %q: got %v, want %v", tc.action, got, tc.want)
		}
	}
}

// TestPartialFieldsFor_EmptyNamesAreDropped: an empty string would match a body field named "" and is
// not a declaration of anything.
func TestPartialFieldsFor_EmptyNamesAreDropped(t *testing.T) {
	verbs := []getter.VerbsDescription{{Action: "update", PartialFields: []string{"", "extra", ""}}}
	if got := keys(partialFieldsFor(verbs, "update")); !reflect.DeepEqual(got, []string{"extra"}) {
		t.Errorf("got %v, want [extra]", got)
	}
}

// TestDropPartialFields is the omit-on-read-failure arm: the declared field must LEAVE the body, and
// nothing else may.
func TestDropPartialFields(t *testing.T) {
	body := map[string]interface{}{
		"name":  "blade01",
		"extra": map[string]interface{}{"kubeadm_join": "j"},
		"other": map[string]interface{}{"k": "v"},
	}
	dropped := dropPartialFields(body, bodySet("extra", "neverPresent"))
	if !reflect.DeepEqual(dropped, []string{"extra"}) {
		t.Fatalf("dropped %v, want [extra] -- a field the body does not carry was not dropped from it", dropped)
	}
	if _, present := body["extra"]; present {
		t.Error("extra is STILL in the body: the update would replace the vendor's object with the CR's " +
			"partial view, which is the defect this path exists to avoid")
	}
	for _, keep := range []string{"name", "other"} {
		if _, present := body[keep]; !present {
			t.Errorf("%q was dropped: only DECLARED fields may be omitted, or an unrelated field stops "+
				"being sent and the update silently stops doing what the CR asks", keep)
		}
	}
}

// TestDropPartialFields_NothingDeclared: a kind with no declaration must be byte-for-byte unchanged.
func TestDropPartialFields_NothingDeclared(t *testing.T) {
	body := map[string]interface{}{"name": "x", "extra": map[string]interface{}{"a": 1}}
	before := len(body)
	if got := dropPartialFields(body, bodySet()); got != nil {
		t.Fatalf("dropped %v from a kind that declared nothing", got)
	}
	if len(body) != before {
		t.Errorf("body changed from %d keys to %d", before, len(body))
	}
}

func keys(s map[string]struct{}) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestPartialFieldsSurvivesTheMirror is the test the getter.go comment asks for: oasgen's CRD type and
// RDC's VerbsDescription are TWO SEPARATE STRUCTS, and a field present in only one is silently dropped.
//
// The failure that makes this worth a test is not "the feature does not work". It is that the CRD would
// ACCEPT a partialFields declaration, admission would store it, `kubectl get` would echo it back, and
// every update would still delete the author's keys. An author told their data is protected while it is
// not is strictly worse off than one told the field does not exist.
//
// So this asserts the JSON tag round-trips through the type the controller actually reads, using the
// exact wire shape the apiserver serves.
func TestPartialFieldsSurvivesTheMirror(t *testing.T) {
	const wire = `{"action":"update","method":"PATCH","path":"/nodes/{id}","partialFields":["extra","config"]}`

	var v getter.VerbsDescription
	if err := json.Unmarshal([]byte(wire), &v); err != nil {
		t.Fatalf("unmarshalling the verb: %v", err)
	}
	if !reflect.DeepEqual(v.PartialFields, []string{"extra", "config"}) {
		t.Fatalf("PartialFields = %v, want [extra config].\n"+
			"The JSON tag on RDC's VerbsDescription does not match the CRD's `partialFields`, so the "+
			"declaration is dropped on the way in and every update replays the field whole.", v.PartialFields)
	}

	// And it must reach the lookup the Update path uses, not merely the struct.
	if got := keys(partialFieldsFor([]getter.VerbsDescription{v}, "update")); !reflect.DeepEqual(got, []string{"config", "extra"}) {
		t.Errorf("partialFieldsFor = %v, want [config extra]", got)
	}

	// THE NEGATIVE HALF, and the first version of it was inside the matcher's own equivalence class.
	//
	// I first used `partialfields` -- a CASE variant -- and it populated the field, because Go's
	// encoding/json matches names case-insensitively. That is correct behaviour, so the control proved
	// nothing about the tag and only restated the standard library. Asserted here as the observed fact:
	var lower getter.VerbsDescription
	if err := json.Unmarshal([]byte(`{"action":"update","partialfields":["extra"]}`), &lower); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}
	if len(lower.PartialFields) != 1 {
		t.Errorf("encoding/json stopped matching field names case-insensitively: `partialfields` gave %v. "+
			"Not a defect in this feature, but the apiserver validates only the CRD's own spelling, so an "+
			"author's case variant is pruned before it ever reaches here.", lower.PartialFields)
	}

	// A key OUTSIDE that equivalence class must not arrive. Singular, and underscored -- two spellings an
	// author could plausibly write, neither of which encoding/json will accept for this field.
	for _, wrong := range []string{
		`{"action":"update","partialField":["extra"]}`,
		`{"action":"update","partial_fields":["extra"]}`,
	} {
		var w getter.VerbsDescription
		if err := json.Unmarshal([]byte(wrong), &w); err != nil {
			t.Fatalf("unmarshalling %s: %v", wrong, err)
		}
		if len(w.PartialFields) != 0 {
			t.Errorf("%s populated PartialFields = %v: the struct is accepting a name the CRD does not "+
				"declare, so this test cannot tell a correct tag from a wrong one", wrong, w.PartialFields)
		}
	}
}
