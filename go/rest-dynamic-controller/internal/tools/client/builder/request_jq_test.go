//go:build unit || integration

package builder

import (
	"context"
	"strings"
	"testing"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func crWithRef(ref string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{"ref": ref},
	}}
}

func pathEntry(jq string) *CallInfo {
	return &CallInfo{
		FieldMapping: []getter.FieldMappingItem{{
			InPath:           "ref",
			InCustomResource: "spec.ref",
			ValueMapping: &getter.ValueMapping{
				Type: "jq",
				JQ:   &getter.JQProgram{Inline: jq},
			},
		}},
	}
}

func emptyConfig() *restclient.RequestConfiguration {
	return &restclient.RequestConfiguration{
		Parameters: map[string]string{},
		Query:      map[string]string{},
	}
}

// TestRequestJQOnPathParameter is #117: the entire reason a Go plugin existed.
//
// GitHub's create ref endpoint returns and accepts "refs/heads/x"; its read endpoint wants "heads/x".
// The KOG proxied that read through a plugin whose handler did exactly one thing:
//
//	ref := strings.TrimPrefix(r.PathValue("ref"), "refs/")
//
// A deployment, a service, an image and a release pipeline, to run one line of string surgery — because
// path and query parameters were the one part of the request surface with no transformation hook at all.
func TestRequestJQOnPathParameter(t *testing.T) {
	cfg := emptyConfig()
	applyFieldMapping(context.Background(), pathEntry(`sub("^refs/"; "")`), crWithRef("refs/heads/feature-x"),
		cfg, map[string]interface{}{}, nil)

	require.NoError(t, cfg.BuildErr)
	assert.Equal(t, "heads/feature-x", cfg.Parameters["ref"],
		"the plugin's one line of Go, expressed declaratively")
}

// TestRequestJQFailureIsNotASilentlyMissingParameter is the forbidding test.
//
// The previous behaviour for a request-direction jq entry was `continue` — skip it. The intent was
// defensible ("do not send a value that should have been transformed but wasn't"), but the effect was
// that the path parameter was simply ABSENT. A URL missing a segment still parses; the API answers 404;
// and the reconciler acts on not-found by CREATING. So a declared-but-unrunnable transform did not fail,
// it created a duplicate.
//
// Every failure below must therefore surface, not vanish.
func TestRequestJQFailureIsNotASilentlyMissingParameter(t *testing.T) {
	cases := []struct {
		name string
		jq   string
		want string
	}{
		{name: "program does not compile", jq: `sub(`, want: "compiling jq"},
		{name: "program fails at runtime", jq: `.a.b.c`, want: "running jq"},
		{name: "program returns an object", jq: `{ref: .}`, want: "no meaningful form in a URL"},
		{name: "program returns an array", jq: `[.]`, want: "no meaningful form in a URL"},
		{name: "program returns null", jq: `null`, want: "cannot address anything"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := emptyConfig()
			applyFieldMapping(context.Background(), pathEntry(tc.jq), crWithRef("refs/heads/x"),
				cfg, map[string]interface{}{}, nil)

			require.Error(t, cfg.BuildErr,
				"a transform that cannot produce a URL value must fail the request, not drop the parameter: "+
					"a missing path segment still yields a parseable URL, a 404, and then a create")
			assert.Contains(t, cfg.BuildErr.Error(), tc.want)
			assert.Contains(t, cfg.BuildErr.Error(), `path parameter "ref"`,
				"the message must name the offending entry")
			assert.NotContains(t, cfg.Parameters, "ref",
				"and nothing half-transformed may be left behind")
		})
	}
}

// A non-string scalar is fine — it has an unambiguous URL form. Only shapes that don't are refused.
func TestRequestJQScalarsAreAccepted(t *testing.T) {
	for _, tc := range []struct{ name, jq, want string }{
		{"number", `123`, "123"},
		{"number from arithmetic", `(. | length)`, "20"},
		{"boolean", `true`, "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := emptyConfig()
			applyFieldMapping(context.Background(), pathEntry(tc.jq), crWithRef("refs/heads/feature-x"),
				cfg, map[string]interface{}{}, nil)
			require.NoError(t, cfg.BuildErr)
			assert.Equal(t, tc.want, cfg.Parameters["ref"])
		})
	}
}

// The transform must see and produce the LOGICAL value. Escaping is buildPath's single responsibility, and
// running the program after it would make the author reason about percent-encoding inside jq.
func TestRequestJQRunsBeforeEscaping(t *testing.T) {
	cfg := emptyConfig()
	applyFieldMapping(context.Background(), pathEntry(`sub("^refs/"; "")`), crWithRef("refs/heads/feature/x"),
		cfg, map[string]interface{}{}, nil)

	require.NoError(t, cfg.BuildErr)
	assert.Equal(t, "heads/feature/x", cfg.Parameters["ref"],
		"the slash is still a literal slash here; url.PathEscape happens later, in buildPath")
	assert.False(t, strings.Contains(cfg.Parameters["ref"], "%2F"),
		"the program must not be handed pre-escaped input")
}

// Query parameters get the identical treatment — the gap was never path-specific.
func TestRequestJQOnQueryParameter(t *testing.T) {
	ci := &CallInfo{FieldMapping: []getter.FieldMappingItem{{
		InQuery:          "branch",
		InCustomResource: "spec.ref",
		ValueMapping: &getter.ValueMapping{
			Type: "jq",
			JQ:   &getter.JQProgram{Inline: `sub("^refs/heads/"; "")`},
		},
	}}}

	cfg := emptyConfig()
	applyFieldMapping(context.Background(), ci, crWithRef("refs/heads/feature-x"), cfg, map[string]interface{}{}, nil)

	require.NoError(t, cfg.BuildErr)
	assert.Equal(t, "feature-x", cfg.Query["branch"])
}

// A body field may carry a jq transform too. Body has requestTransform, but that is whole-document; this
// is the per-field granularity the response direction has always had. Non-scalars are fine here: a body
// field legitimately holds objects and arrays, unlike a URL component.
func TestRequestJQOnBodyFieldAcceptsNonScalars(t *testing.T) {
	ci := &CallInfo{FieldMapping: []getter.FieldMappingItem{{
		InBody:           "ref",
		InCustomResource: "spec.ref",
		ValueMapping: &getter.ValueMapping{
			Type: "jq",
			JQ:   &getter.JQProgram{Inline: `{name: ., kind: "branch"}`},
		},
	}}}

	body := map[string]interface{}{}
	cfg := emptyConfig()
	applyFieldMapping(context.Background(), ci, crWithRef("refs/heads/x"), cfg, body, nil)

	require.NoError(t, cfg.BuildErr, "an object is a perfectly good body field, unlike a URL segment")
	assert.Equal(t, map[string]interface{}{"name": "refs/heads/x", "kind": "branch"}, body["ref"])
}
