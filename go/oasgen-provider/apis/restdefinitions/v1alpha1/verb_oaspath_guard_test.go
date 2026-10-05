package v1alpha1

import (
	"os"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// verbsDescriptionRule reads the rule off the SHIPPED CRD, so this tests what the apiserver enforces
// rather than the marker that was typed.
func verbsDescriptionRule(t *testing.T) (rule, message string) {
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
	rules, _ := node["x-kubernetes-validations"].([]any)
	require.Len(t, rules, 1, "expected exactly one validation on verbsDescription")
	r := rules[0].(map[string]any)
	return r["rule"].(string), r["message"].(string)
}

// TestPerVerbOASPathIsRefusedUntilImplemented guards the gap between a DECLARED field and a READ one.
//
// VerbsDescription.OASPath ships in the published CRD, pattern-validated and documented with its full
// contract, and nothing reads it. Without this rule a RestDefinition setting it is admitted and the verb
// is then resolved from spec.oasPath regardless — a CRD generated from the wrong document, with no error
// and no warning. That is the silent-wrong-answer shape, so the write is refused instead.
//
// This rule is TEMPORARY and is deleted by #108. The test is what makes removing it deliberate: whoever
// implements the feature has to delete this too, and will read why it existed on the way past.
func TestPerVerbOASPathIsRefusedUntilImplemented(t *testing.T) {
	rule, message := verbsDescriptionRule(t)

	env, err := cel.NewEnv(cel.Variable("self", cel.ListType(cel.MapType(cel.StringType, cel.AnyType))))
	require.NoError(t, err)
	ast, iss := env.Compile(rule)
	require.NoError(t, iss.Err(), "the shipped rule must compile: %s", rule)
	prg, err := env.Program(ast)
	require.NoError(t, err)

	allowed := func(verbs []any) bool {
		out, _, evalErr := prg.Eval(map[string]any{"self": verbs})
		require.NoError(t, evalErr)
		return out == types.True
	}

	get := map[string]any{"action": "get", "method": "GET", "path": "/cloudServers/{id}"}
	create := map[string]any{"action": "create", "method": "POST", "path": "/cloudServers"}
	createWithOverride := map[string]any{
		"action": "create", "method": "POST", "path": "/cloudServers",
		"oasPath": "configmap://ns/compute11/openapi-v1.1.json",
	}

	require.True(t, allowed([]any{get, create}),
		"the ordinary single-document case must be unaffected")
	require.False(t, allowed([]any{get, createWithOverride}),
		"a verb carrying oasPath must be refused while nothing reads it")
	require.False(t, allowed([]any{createWithOverride}),
		"including when it is the only verb")

	// The message has to tell someone what to do, not just that they cannot do this.
	require.Contains(t, message, "not implemented yet")
	require.Contains(t, message, "#108", "and where to track it")
}
