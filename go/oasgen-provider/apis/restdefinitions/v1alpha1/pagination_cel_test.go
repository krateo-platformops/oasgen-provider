package v1alpha1

import (
	"os"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// This file tests the GENERATED CRD, not the Go markers above it.
//
// The markers are the input to controller-gen; what admits or rejects a RestDefinition is the CEL that
// comes out the other side. A marker with a typo'd field name still generates a rule -- one that quietly
// evaluates to true and admits everything. Reading the shipped file and running its rules is the only
// way that failure shows up as a red test rather than as a cluster accepting a declaration it should
// have refused.
const generatedCRDPath = "../../../crds/ogen.krateo.io_restdefinitions.yaml"

type celRule struct {
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

// paginationSchema returns the pagination subschema of the shipped CRD, as a generic map.
func paginationSchema(t *testing.T) map[string]any {
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
		for _, step := range []string{"properties", "spec", "properties", "resource", "properties", "verbsDescription"} {
			next, ok := node[step].(map[string]any)
			require.True(t, ok, "walking the CRD stopped at %q", step)
			node = next
		}
		pag, ok := node["items"].(map[string]any)["properties"].(map[string]any)["pagination"].(map[string]any)
		require.True(t, ok, "verbsDescription has no pagination schema")
		return pag
	}

	t.Fatalf("version %q not found in the generated CRD", Version)
	return nil
}

func rulesOf(t *testing.T, schema map[string]any) []celRule {
	t.Helper()
	raw, err := yaml.Marshal(schema["x-kubernetes-validations"])
	require.NoError(t, err)
	var out []celRule
	require.NoError(t, yaml.Unmarshal(raw, &out))
	return out
}

// evalRules compiles every rule against `self` = doc and returns the message of the first that fails,
// or "" when all pass. This mirrors what the API server does at admission.
func evalRules(t *testing.T, rules []celRule, doc map[string]any) string {
	t.Helper()
	require.NotEmpty(t, rules, "no CEL rules found: a schema with no rules admits everything")

	env, err := cel.NewEnv(cel.Variable("self", cel.MapType(cel.StringType, cel.DynType)))
	require.NoError(t, err)

	for _, r := range rules {
		ast, iss := env.Compile(r.Rule)
		require.NoError(t, iss.Err(), "rule does not compile: %s", r.Rule)

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]any{"self": doc})
		require.NoError(t, err, "evaluating %s", r.Rule)

		if out != types.True {
			return r.Message
		}
	}
	return ""
}

// TestPaginationCELAdmission pins which declarations the shipped CRD accepts.
//
// The rejections matter more than the acceptances. A pagination block declaring two strategies, or
// naming a strategy whose configuration is absent, has no single defensible reading -- and the failure
// mode of guessing is not a crash but a findby that stops early, reports not-found, and has the
// reconciler create a duplicate of the resource it failed to find (#119). Refusing at admission is the
// only place that costs nobody an external resource.
func TestPaginationCELAdmission(t *testing.T) {
	rules := rulesOf(t, paginationSchema(t))

	pageNumber := map[string]any{
		"request":  map[string]any{"pageIn": "query", "pagePath": "page", "startPage": 1},
		"maxPages": 20,
	}
	continuationToken := map[string]any{
		"request":  map[string]any{"tokenIn": "query", "tokenPath": "cursor"},
		"response": map[string]any{"tokenIn": "header", "tokenPath": "X-Next"},
	}

	cases := []struct {
		name       string
		doc        map[string]any
		wantReject bool
	}{
		{
			name: "pageNumber with its config",
			doc:  map[string]any{"type": "pageNumber", "pageNumber": pageNumber},
		},
		{
			name: "continuationToken with its config",
			doc:  map[string]any{"type": "continuationToken", "continuationToken": continuationToken},
		},
		{
			name:       "pageNumber with no pageNumber block",
			doc:        map[string]any{"type": "pageNumber"},
			wantReject: true,
		},
		{
			name:       "continuationToken with no continuationToken block",
			doc:        map[string]any{"type": "continuationToken"},
			wantReject: true,
		},
		{
			name: "pageNumber carrying a stray continuationToken block",
			doc: map[string]any{
				"type": "pageNumber", "pageNumber": pageNumber, "continuationToken": continuationToken,
			},
			wantReject: true,
		},
		{
			name: "continuationToken carrying a stray pageNumber block",
			doc: map[string]any{
				"type": "continuationToken", "continuationToken": continuationToken, "pageNumber": pageNumber,
			},
			wantReject: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := evalRules(t, rules, tc.doc)
			if tc.wantReject {
				require.NotEmpty(t, msg, "the CRD admitted a declaration with no single defensible reading")
				return
			}
			require.Empty(t, msg, "the CRD rejected a valid declaration: %s", msg)
		})
	}
}

// TestPageNumberBodySignalCELAdmission covers the nested rules: exactly one total path, and at most one
// end-of-collection mechanism. Two mechanisms cannot both be authoritative, and picking one silently is
// how an author's actual intent gets discarded.
func TestPageNumberBodySignalCELAdmission(t *testing.T) {
	pag := paginationSchema(t)
	pnResp := pag["properties"].(map[string]any)["pageNumber"].(map[string]any)["properties"].(map[string]any)["response"].(map[string]any)

	header := map[string]any{"name": "Link", "matches": `rel="next"`}

	t.Run("response block", func(t *testing.T) {
		rules := rulesOf(t, pnResp)
		require.Empty(t, evalRules(t, rules, map[string]any{"header": header}))
		require.Empty(t, evalRules(t, rules, map[string]any{"body": map[string]any{"totalPagesPath": "total_pages"}}))
		require.NotEmpty(t,
			evalRules(t, rules, map[string]any{"header": header, "body": map[string]any{"totalPagesPath": "p"}}),
			"both a header and a body signal were admitted; only one can be authoritative")
	})

	t.Run("body signal", func(t *testing.T) {
		body := pnResp["properties"].(map[string]any)["body"].(map[string]any)
		rules := rulesOf(t, body)
		require.Empty(t, evalRules(t, rules, map[string]any{"totalPagesPath": "total_pages"}))
		require.Empty(t, evalRules(t, rules, map[string]any{"totalItemsPath": "total_count"}))
		require.NotEmpty(t, evalRules(t, rules, map[string]any{}),
			"a body signal declaring no total path was admitted, leaving nothing to compare against")
		require.NotEmpty(t,
			evalRules(t, rules, map[string]any{"totalPagesPath": "p", "totalItemsPath": "i"}),
			"two totals were admitted; they can disagree and there is no rule for which wins")
	})
}

// TestMaxPagesIsRequiredWithNoDefault pins the deliberate absence of a default.
//
// A default here would be a bound nobody chose, silently ending searches at a number the author never
// considered -- and since a truncated search that reported absence would create a duplicate, the number
// would be doing real damage while looking like a convenience.
func TestMaxPagesIsRequiredWithNoDefault(t *testing.T) {
	pag := paginationSchema(t)
	pn := pag["properties"].(map[string]any)["pageNumber"].(map[string]any)

	required, _ := pn["required"].([]any)
	require.Contains(t, required, "maxPages", "maxPages must be required")

	maxPages := pn["properties"].(map[string]any)["maxPages"].(map[string]any)
	require.NotContains(t, maxPages, "default", "maxPages must have NO default")
	require.EqualValues(t, 1, maxPages["minimum"])

	// The ceiling must match rest-dynamic-controller's pagination.MaxFindByPages, the hard backstop in
	// the findby loop. A CRD admitting more than the loop will walk would let an author configure a
	// bound that is then cut short by a different one -- two limits, one of them invisible. The RDC side
	// asserts the same number against its own copy of this file.
	require.EqualValues(t, 1000, maxPages["maximum"],
		"maxPages ceiling must equal rest-dynamic-controller's pagination.MaxFindByPages")

	// startPage carries no default either: 0- and 1-based APIs are both common, and guessing wrong
	// skips or repeats a page without any symptom other than a resource that is never found.
	req := pn["properties"].(map[string]any)["request"].(map[string]any)
	reqRequired, _ := req["required"].([]any)
	require.Contains(t, reqRequired, "startPage")
	require.NotContains(t, req["properties"].(map[string]any)["startPage"], "default")
}

// TestContinuationTokenResponseAdmitsBody pins that the enum caught up with the implementation.
//
// The field's own documentation said "header or body" while the enum admitted only header and the body
// branch of the extractor was a `// Not implemented yet` comment that fell through to "no token" -- so a
// body token would have ended the walk after page one, silently. Both halves are real now.
func TestContinuationTokenResponseAdmitsBody(t *testing.T) {
	pag := paginationSchema(t)
	ct := pag["properties"].(map[string]any)["continuationToken"].(map[string]any)
	resp := ct["properties"].(map[string]any)["response"].(map[string]any)
	tokenIn := resp["properties"].(map[string]any)["tokenIn"].(map[string]any)

	enum, _ := tokenIn["enum"].([]any)
	require.ElementsMatch(t, []any{"header", "body"}, enum,
		"continuationToken response tokenIn must admit both locations now that body is implemented")
}
