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

// schemaAt walks the shipped CRD to a node under spec.resource, so these tests run the CEL that is
// actually generated rather than the markers that produced it. See pagination_cel_test.go for why that
// distinction is the whole point: a marker with a typo'd field name still emits a rule, and that rule
// evaluates to true and admits everything.
func schemaAt(t *testing.T, steps ...string) map[string]any {
	t.Helper()

	raw, err := os.ReadFile(generatedCRDPath)
	require.NoError(t, err, "the generated CRD must be readable; run `make generate`")

	var crd map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &crd))

	versions, _ := crd["spec"].(map[string]any)["versions"].([]any)
	require.NotEmpty(t, versions)

	for _, v := range versions {
		vm := v.(map[string]any)
		if vm["name"] != Version {
			continue
		}
		node := vm["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)
		for _, step := range append([]string{"properties", "spec", "properties", "resource"}, steps...) {
			next, ok := node[step].(map[string]any)
			require.True(t, ok, "walking the CRD stopped at %q", step)
			node = next
		}
		return node
	}

	t.Fatalf("version %q not found in the generated CRD", Version)
	return nil
}

func lookup(name string, params ...map[string]any) map[string]any {
	l := map[string]any{"name": name, "method": "GET", "path": "/x", "select": ".id"}
	if len(params) > 0 {
		l["parameters"] = toAny(params)
	}
	return l
}

func toAny[T any](in []T) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	return out
}

// evalRulesList is evalRules for a rule whose `self` is the LIST itself rather than an object -- the
// uniqueness rule is declared on the dependentLookups field, so the apiserver binds self to the array.
func evalRulesList(t *testing.T, rules []celRule, items []any) string {
	t.Helper()
	require.NotEmpty(t, rules, "no CEL rules found: a schema with no rules admits everything")

	env, err := cel.NewEnv(
		cel.Variable("self", cel.ListType(cel.DynType)),
		ext.TwoVarComprehensions(),
	)
	require.NoError(t, err)

	for _, r := range rules {
		ast, iss := env.Compile(r.Rule)
		require.NoError(t, iss.Err(), "rule does not compile: %s", r.Rule)

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]any{"self": items})
		require.NoError(t, err, "evaluating %s", r.Rule)

		if out != types.True {
			return r.Message
		}
	}
	return ""
}

func verbWithLookupResolver(lookupName string) map[string]any {
	return map[string]any{
		"action": "create", "method": "POST", "path": "/x",
		"fieldMapping": []any{map[string]any{
			"inBody": "sha",
			"resolver": map[string]any{
				"type":            "dependentLookup",
				"dependentLookup": map[string]any{"name": lookupName},
			},
		}},
	}
}

// TestDependentLookupResourceCELAdmission pins the resource-level rules that make a dependent lookup
// safe to declare.
//
// The rejections are the point. A forward or self reference is what a cycle looks like in this schema,
// and the design's answer to cycles is that they are UNREPRESENTABLE -- references may only point at an
// earlier entry -- rather than detected by a runtime check that can be wrong. If this rule admits a
// forward reference, that guarantee is gone and nothing else restores it.
//
// The acceptances matter too: a rule that over-rejects is as broken as one that admits everything. The
// secretRef case is here because the resolver rules must not fire on a resolver kind they do not govern.
func TestDependentLookupResourceCELAdmission(t *testing.T) {
	rules := rulesOf(t, schemaAt(t))

	cases := []struct {
		name       string
		doc        map[string]any
		wantReject bool
	}{
		{
			name: "second lookup references the first, resolver names the second",
			doc: map[string]any{
				"kind": "GitRef",
				"dependentLookups": []any{
					lookup("defaultBranch"),
					lookup("baseSha", map[string]any{"inPath": "ref", "fromLookup": "defaultBranch"}),
				},
				"verbsDescription": []any{verbWithLookupResolver("baseSha")},
			},
		},
		{
			name: "no lookups and no lookup resolver",
			doc: map[string]any{
				"kind":             "Repo",
				"verbsDescription": []any{map[string]any{"action": "create", "method": "POST", "path": "/x"}},
			},
		},
		{
			name: "secretRef resolver with no dependentLookups is untouched by these rules",
			doc: map[string]any{
				"kind": "Repo",
				"verbsDescription": []any{map[string]any{
					"action": "create", "method": "POST", "path": "/x",
					"fieldMapping": []any{map[string]any{
						"inBody": "token",
						"resolver": map[string]any{
							"type": "secretRef",
							"secretRef": map[string]any{
								"nameFromCustomResource": "spec.secretName",
								"keyFromCustomResource":  "spec.secretKey",
							},
						},
					}},
				}},
			},
		},
		{
			name: "FORWARD reference: the first lookup reads the second",
			doc: map[string]any{
				"kind": "GitRef",
				"dependentLookups": []any{
					lookup("defaultBranch", map[string]any{"inPath": "ref", "fromLookup": "baseSha"}),
					lookup("baseSha"),
				},
				"verbsDescription": []any{verbWithLookupResolver("baseSha")},
			},
			wantReject: true,
		},
		{
			name: "SELF reference",
			doc: map[string]any{
				"kind": "GitRef",
				"dependentLookups": []any{
					lookup("baseSha", map[string]any{"inPath": "ref", "fromLookup": "baseSha"}),
				},
				"verbsDescription": []any{verbWithLookupResolver("baseSha")},
			},
			wantReject: true,
		},
		// A resolver naming an UNDECLARED lookup is not checked here: that check has to traverse
		// verbsDescription x fieldMapping, neither of which carries a maxItems bound, and as CEL it
		// measured >100x the apiserver's per-rule cost budget while also pushing the whole schema over
		// its total -- which refuses the CRD at install. See TestGeneratedCRDIsWithinCELCostBudget. It is
		// enforced at generation instead; see TestValidateDependentLookups.
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := evalRules(t, rules, tc.doc)
			if tc.wantReject {
				require.NotEmpty(t, msg, "expected this declaration to be refused")
			} else {
				require.Empty(t, msg, "expected this declaration to be admitted")
			}
		})
	}
}

// TestDependentLookupNamesMustBeUnique covers the rule on the list itself: a resolver names a lookup, so
// two lookups sharing a name leave which one runs decided by list order.
func TestDependentLookupNamesMustBeUnique(t *testing.T) {
	rules := rulesOf(t, schemaAt(t, "properties", "dependentLookups"))

	t.Run("distinct names", func(t *testing.T) {
		require.Empty(t, evalRulesList(t, rules, []any{lookup("a"), lookup("b")}))
	})
	t.Run("duplicate names", func(t *testing.T) {
		require.NotEmpty(t, evalRulesList(t, rules, []any{lookup("a"), lookup("a")}))
	})
}

// TestDependentLookupSelectXorMatch pins that a lookup declares exactly one way of extracting its value.
// Neither leaves nothing to substitute; both leaves which one wins to the implementation.
func TestDependentLookupSelectXorMatch(t *testing.T) {
	rules := rulesOf(t, schemaAt(t, "properties", "dependentLookups", "items"))

	match := map[string]any{
		"whereField":               "alias",
		"equalsFromCustomResource": "spec.flow",
		"selectField":              "id",
	}

	cases := []struct {
		name       string
		doc        map[string]any
		wantReject bool
	}{
		{name: "select only", doc: map[string]any{"name": "a", "method": "GET", "path": "/x", "select": ".id"}},
		{name: "match only", doc: map[string]any{"name": "a", "method": "GET", "path": "/x", "match": match}},
		{
			name:       "both select and match",
			doc:        map[string]any{"name": "a", "method": "GET", "path": "/x", "select": ".id", "match": match},
			wantReject: true,
		},
		{
			name:       "neither select nor match",
			doc:        map[string]any{"name": "a", "method": "GET", "path": "/x"},
			wantReject: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := evalRules(t, rules, tc.doc)
			if tc.wantReject {
				require.NotEmpty(t, msg, "expected refusal")
			} else {
				require.Empty(t, msg, "expected admission")
			}
		})
	}
}

// TestLookupParameterSourceXor pins that a parameter binds exactly one location from exactly one source.
func TestLookupParameterSourceXor(t *testing.T) {
	rules := rulesOf(t, schemaAt(t, "properties", "dependentLookups", "items", "properties", "parameters", "items"))

	cases := []struct {
		name       string
		doc        map[string]any
		wantReject bool
	}{
		{name: "path from CR", doc: map[string]any{"inPath": "owner", "inCustomResource": "spec.owner"}},
		{name: "path from an earlier lookup", doc: map[string]any{"inPath": "ref", "fromLookup": "defaultBranch"}},
		{name: "query from CR", doc: map[string]any{"inQuery": "filter", "inCustomResource": "spec.filter"}},
		{
			name:       "both sources",
			doc:        map[string]any{"inPath": "ref", "inCustomResource": "spec.ref", "fromLookup": "defaultBranch"},
			wantReject: true,
		},
		{
			name:       "both locations",
			doc:        map[string]any{"inPath": "ref", "inQuery": "ref", "inCustomResource": "spec.ref"},
			wantReject: true,
		},
		{
			name:       "no location",
			doc:        map[string]any{"inCustomResource": "spec.ref"},
			wantReject: true,
		},
		{
			name:       "no source",
			doc:        map[string]any{"inPath": "ref"},
			wantReject: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := evalRules(t, rules, tc.doc)
			if tc.wantReject {
				require.NotEmpty(t, msg, "expected refusal")
			} else {
				require.Empty(t, msg, "expected admission")
			}
		})
	}
}
