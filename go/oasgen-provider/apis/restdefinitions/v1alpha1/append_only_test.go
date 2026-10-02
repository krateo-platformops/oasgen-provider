package v1alpha1

import (
	"os"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/ext"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// additionalStatusFieldsRule reads the rule off the SHIPPED CRD, so this tests what the apiserver will
// enforce rather than the marker that was typed.
func additionalStatusFieldsRule(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../../crds/ogen.krateo.io_restdefinitions.yaml")
	require.NoError(t, err)
	var crd map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &crd))

	node := crd["spec"].(map[string]any)["versions"].([]any)[0].(map[string]any)["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)
	for _, step := range []string{"properties", "spec", "properties", "resource", "properties", "additionalStatusFields"} {
		next, ok := node[step].(map[string]any)
		require.True(t, ok, "walking the CRD stopped at %q", step)
		node = next
	}
	rules, _ := node["x-kubernetes-validations"].([]any)
	require.Len(t, rules, 1, "expected exactly one validation on additionalStatusFields")
	return rules[0].(map[string]any)["rule"].(string)
}

// TestAdditionalStatusFieldsIsAppendOnly runs the shipped rule through CEL with the two-variable
// comprehension the apiserver enables, so the behaviour checked here is the behaviour on a cluster.
//
// Fully immutable blocked an ordinary upgrade: github-provider-kog 0.3.2 adds `merged` and `merged_at` to
// PullRequest's additionalStatusFields, and on an existing install the apply was refused outright. The only
// way through was deleting the RestDefinition and its CRs — 24 publish records on the cluster that hit it.
func TestAdditionalStatusFieldsIsAppendOnly(t *testing.T) {
	rule := additionalStatusFieldsRule(t)

	env, err := cel.NewEnv(
		cel.Variable("self", cel.ListType(cel.StringType)),
		cel.Variable("oldSelf", cel.ListType(cel.StringType)),
		// Available in the apiserver's CEL from Kubernetes 1.32, below this chart's 1.33 floor.
		ext.TwoVarComprehensions(),
	)
	require.NoError(t, err)
	ast, iss := env.Compile(rule)
	require.NoError(t, iss.Err(), "the shipped rule must compile: %s", rule)
	prg, err := env.Program(ast)
	require.NoError(t, err)

	allowed := func(old, nw []any) bool {
		out, _, evalErr := prg.Eval(map[string]any{"oldSelf": old, "self": nw})
		require.NoError(t, evalErr)
		return out == types.True
	}

	base := []any{"id", "state", "number"}

	t.Run("the reported upgrade is allowed", func(t *testing.T) {
		require.True(t, allowed(base, []any{"id", "state", "number", "merged", "merged_at"}),
			"a KOG chart gaining a status field must be upgradable in place")
	})
	t.Run("unchanged is allowed", func(t *testing.T) {
		require.True(t, allowed(base, []any{"id", "state", "number"}))
	})
	t.Run("appending to an empty list is allowed", func(t *testing.T) {
		require.True(t, allowed([]any{}, []any{"id"}))
	})

	// The refusals. Each of these breaks stored objects or printer columns.
	for _, tc := range []struct {
		name string
		nw   []any
	}{
		{"removing an entry", []any{"id", "state"}},
		{"renaming an entry", []any{"id", "renamed", "number"}},
		{"reordering entries", []any{"number", "state", "id"}},
		{"prepending an entry", []any{"new", "id", "state", "number"}},
		{"replacing the list", []any{"other"}},
		{"appending but also changing an existing entry", []any{"id", "CHANGED", "number", "merged"}},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			require.False(t, allowed(base, tc.nw))
		})
	}
}

// The message has to tell an operator what is actually permitted, since the whole point of relaxing this
// was that the previous message ("are immutable") sent people to delete-and-recreate.
func TestRefusalMessageSaysWhatIsAllowed(t *testing.T) {
	raw, err := os.ReadFile("../../../crds/ogen.krateo.io_restdefinitions.yaml")
	require.NoError(t, err)
	require.Contains(t, string(raw), "append-only")
}
