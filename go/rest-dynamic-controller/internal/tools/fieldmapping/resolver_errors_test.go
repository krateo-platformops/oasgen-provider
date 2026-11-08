package fieldmapping

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A DUPLICATE SELECTOR MATCH IS NOT AN ABSENCE, and saying so cost a consumer real debugging time.
//
// The walker refuses a [?key=value] selector that matches more than one element, naming both indices. Both
// readers here collapsed `err != nil || !found` into "path %q not found on the custom resource", so a path
// present TWICE was reported as missing. A consumer hit it on a real cluster and had to read the walker's
// source to find out what the message meant.
//
// The control is the row below it: a genuinely absent path must still say "not found", or this test would
// pass against a reader that had simply stopped distinguishing anything at all.
func TestADuplicateSelectorIsNotReportedAsNotFound(t *testing.T) {
	twoOtp := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"credentials": []any{
			map[string]any{"type": "otp", "secretDataSecretRef": map[string]any{"name": "a"}},
			map[string]any{"type": "otp", "secretDataSecretRef": map[string]any{"name": "b"}},
		}},
	}}
	path := "spec.credentials[?type=otp].secretDataSecretRef.name"

	_, err := readStringPath(twoOtp, path)
	if err == nil {
		t.Fatal("two matching elements must be an error, not a silent pick")
	}
	if strings.Contains(err.Error(), "not found on the custom resource") {
		t.Fatalf("a path present TWICE must not be reported as absent; got: %v", err)
	}
	if !strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("the message must say the path cannot be read, so the reason is legible; got: %v", err)
	}
}

// THE CONTROL. A genuinely missing path must still report "not found" -- otherwise the assertion above
// would pass against a reader that reported every failure the same new way.
func TestAGenuinelyAbsentPathStillSaysNotFound(t *testing.T) {
	passwordOnly := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"credentials": []any{map[string]any{"type": "password"}}},
	}}

	_, err := readStringPath(passwordOnly, "spec.credentials[?type=otp].secretDataSecretRef.name")
	if err == nil {
		t.Fatal("an absent path must be an error here")
	}
	if !strings.Contains(err.Error(), "not found on the custom resource") {
		t.Fatalf("a truly absent path must still say not found; got: %v", err)
	}
}

// A parse failure must carry its own reason too. It previously collapsed to a bare `invalid path %q`,
// which told an operator nothing about WHY -- an unbalanced bracket and an unknown selector form read
// identically.
func TestAnUnparseablePathCarriesTheParserReason(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{}}

	_, err := readStringPath(obj, "spec.credentials[?type=otp")
	if err == nil {
		t.Fatal("an unbalanced bracket must be an error")
	}
	if !strings.Contains(err.Error(), "invalid path") {
		t.Fatalf("got: %v", err)
	}
	if len(err.Error()) <= len(`invalid path "spec.credentials[?type=otp"`) {
		t.Fatalf("the parser's reason must be wrapped, not discarded; got: %v", err)
	}
}
