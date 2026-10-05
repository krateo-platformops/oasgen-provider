package v1alpha1

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// verbsDescriptionNode walks the SHIPPED CRD to the verbsDescription schema, so this tests what the
// apiserver enforces rather than the marker that was typed.
func verbsDescriptionNode(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../../crds/ogen.krateo.io_restdefinitions.yaml")
	require.NoError(t, err)
	var crd map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &crd))

	node := crd["spec"].(map[string]any)["versions"].([]any)[0].(map[string]any)["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)
	for _, step := range []string{"properties", "spec", "properties", "resource", "properties", "verbsDescription"} {
		next, ok := node[step].(map[string]any)
		require.True(t, ok, "walking the CRD stopped at %q", step)
		node = next
	}
	return node
}

// TestPerVerbOASPathIsAdmitted is the mirror of the rule #108 deleted, and exists for the same reason that
// one did: to make a change here deliberate.
//
// Until #108 the CRD carried `self.all(v, !has(v.oasPath))`, refusing the write because the field shipped
// and nothing read it. Now every per-verb lookup resolves against the verb's own document, so the write
// must be ACCEPTED -- and reinstating that rule, or any rule on this list, would make the resource #108
// exists for undescribable again while every Go test still passed, because nothing else in the repo reads
// the shipped CRD's validations.
//
// The MaxItems=32 bound is checked for the same reason. It was never a limit on verbs: it existed only to
// keep that rule's comprehension inside the apiserver's CEL cost budget (#169), and it went with the rule.
func TestPerVerbOASPathIsAdmitted(t *testing.T) {
	node := verbsDescriptionNode(t)

	assert.Nil(t, node["x-kubernetes-validations"],
		"verbsDescription carries no list-level validation; the #108 guard was deleted when the feature landed")
	assert.Nil(t, node["maxItems"],
		"the 32-verb bound existed only to bound the deleted rule's CEL cost, and is not a real limit on verbs")

	items, ok := node["items"].(map[string]any)
	require.True(t, ok)
	props, ok := items["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, props, "oasPath",
		"the per-verb override must still ship: it is the feature, not a leftover")
}
