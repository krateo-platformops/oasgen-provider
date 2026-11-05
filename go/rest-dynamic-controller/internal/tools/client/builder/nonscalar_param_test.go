//go:build unit

package builder

import (
	"context"
	"testing"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/pb33f/libopenapi"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// #463. A dotted additionalStatusFields entry such as `owner.login` renders the status schema NESTED, so
// the top-level status key is `owner`. processFields fills a request parameter by matching a TOP-LEVEL
// field name, so the value handed over is the whole map -- and the four string-valued destinations
// (Parameters, Query, Headers, Cookies) rendered it with fmt.Sprintf("%v"), putting the literal
// `map[login:octocat]` on the wire. That is a syntactically valid URL addressing something that does not
// exist, so the vendor 404s and the reconciler acts on not-found by CREATING (#117).
//
// These drive the real BuildCallConfig over a real document, for the reason header_population_test.go
// states: a hand-built ReqParams would assert that a branch copies a value, not that the value reaches
// ReqParams at all.

const nonScalarDoc = `
openapi: 3.0.0
info: {title: t, version: "1"}
paths:
  /repos/{repo}:
    get:
      parameters:
        - name: repo
          in: path
          required: true
          schema: {type: string}
        - name: owner
          in: query
          schema: {type: string}
        - name: X-Owner
          in: header
          schema: {type: string}
        - name: sess
          in: cookie
          schema: {type: string}
      responses:
        "200": {description: ok}
`

func nonScalarCallInfo(t *testing.T) *CallInfo {
	t.Helper()
	d, err := libopenapi.NewDocument([]byte(nonScalarDoc))
	if err != nil {
		t.Fatalf("fixture is not a document: %v", err)
	}
	v3Doc, errs := d.BuildV3Model()
	if len(errs) > 0 {
		t.Fatalf("fixture did not build: %v", errs)
	}
	cli := &restclient.UnstructuredClient{DocScheme: v3Doc}
	info := &getter.Info{
		Resource: getter.Resource{
			Kind:        "Repo",
			Identifiers: []string{"repo"},
			VerbsDescription: []getter.VerbsDescription{
				{Action: string(apiaction.Get), Method: "GET", Path: "/repos/{repo}"},
			},
		},
	}
	_, callInfo, err := APICallBuilder(cli, info, apiaction.Get)
	if err != nil {
		t.Fatalf("APICallBuilder: %v", err)
	}
	if callInfo == nil {
		t.Fatal("no CallInfo")
	}
	// THE PREMISE. If these names are not in ReqParams, every case below passes trivially because the
	// branch never fires. Assert the input before the behaviour.
	for name, present := range map[string]bool{
		"repo":    callInfo.ReqParams.Parameters.Contains("repo"),
		"owner":   callInfo.ReqParams.Query.Contains("owner"),
		"X-Owner": callInfo.ReqParams.Headers.Contains("X-Owner"),
		"sess":    callInfo.ReqParams.Cookies.Contains("sess"),
	} {
		if !present {
			t.Fatalf("ReqParams does not contain %q, so these cases cannot exercise the population branch", name)
		}
	}
	return callInfo
}

func crWithStatus(status map[string]interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec":   map[string]interface{}{},
		"status": status,
	}}
	u.SetKind("Repo")
	u.SetName("repo-1")
	u.SetNamespace("default")
	return u
}

// TestScalarParamValue pins the renderer. Each accepted type is one unstructured data can actually hold:
// k8s decodes JSON numbers as int64 or float64, never int.
func TestScalarParamValue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  string
		ok    bool
	}{
		{"string", "octocat", "octocat", true},
		{"bool", true, "true", true},
		{"int64", int64(42), "42", true},
		{"float64", 2.5, "2.5", true},
		{"nil renders empty, NOT <nil>", nil, "", true},
		{"map is refused", map[string]interface{}{"login": "octocat"}, "", false},
		{"slice is refused", []interface{}{"a", "b"}, "", false},
		{"an unconsidered type fails closed", struct{ A int }{1}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := scalarParamValue(tc.value)
			if ok != tc.ok || got != tc.want {
				t.Errorf("scalarParamValue(%#v) = (%q, %v), want (%q, %v)", tc.value, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestANestedStatusValueRefusesTheRequest covers all FOUR string-valued destinations. The first pass of
// this fix guarded only Parameters and Query; Headers and Cookies were populated by #305 and have the
// identical ending, so each gets its own arm rather than one arm standing for the set.
func TestANestedStatusValueRefusesTheRequest(t *testing.T) {
	nested := map[string]interface{}{"login": "octocat"}

	for _, tc := range []struct{ name, field string }{
		{"path parameter", "repo"},
		{"query parameter", "owner"},
		{"header", "X-Owner"},
		{"cookie", "sess"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ci := nonScalarCallInfo(t)
			cfg := BuildCallConfig(context.Background(), ci, crWithStatus(map[string]interface{}{tc.field: nested}), nil, nil)
			if cfg == nil {
				t.Fatal("no RequestConfiguration")
			}
			if cfg.BuildErr == nil {
				t.Fatalf("BuildErr is nil for a composite %s value: the request would carry %q",
					tc.name, "map[login:octocat]")
			}
			// The rendered map must not have been written anywhere.
			for dest, m := range map[string]map[string]string{
				"Parameters": cfg.Parameters, "Query": cfg.Query, "Headers": cfg.Headers, "Cookies": cfg.Cookies,
			} {
				if v, ok := m[tc.field]; ok {
					t.Errorf("%s[%s] was written as %q despite the refusal", dest, tc.field, v)
				}
			}
		})
	}
}

// TestAScalarStatusValueStillPopulates is the control, and without it the arms above pass for a
// provider that refuses everything.
func TestAScalarStatusValueStillPopulates(t *testing.T) {
	ci := nonScalarCallInfo(t)
	cfg := BuildCallConfig(context.Background(), ci, crWithStatus(map[string]interface{}{
		"repo": "hello-world", "owner": "octocat", "X-Owner": "octocat", "sess": "abc",
	}), nil, nil)
	if cfg == nil {
		t.Fatal("no RequestConfiguration")
	}
	if cfg.BuildErr != nil {
		t.Fatalf("BuildErr set for all-scalar values: %v", cfg.BuildErr)
	}
	for _, tc := range []struct {
		dest, field, want string
		got               map[string]string
	}{
		{"Parameters", "repo", "hello-world", cfg.Parameters},
		{"Query", "owner", "octocat", cfg.Query},
		{"Headers", "X-Owner", "octocat", cfg.Headers},
		{"Cookies", "sess", "abc", cfg.Cookies},
	} {
		if got := tc.got[tc.field]; got != tc.want {
			t.Errorf("%s[%s] = %q, want %q", tc.dest, tc.field, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------------------------------
// THE REGRESSION ARMS. The first version of this fix decided at the point of discovery and shipped a
// refusal of every `get` on a working kind.
//
// processFields runs BEFORE the mapping writers, by design and for stated reasons. A request-direction
// mapping then writes the parameter unconditionally -- so a kind whose mapped parameter reads from a
// NESTED status field ends up with exactly the right value, and an immediate BuildErr condemned it from
// a value that no longer existed. The message even told the author to add the mapping they already had.
//
// Measured on a real definition by the github-provider-kog session, whose UserRepository shape is
// reproduced below: Parameters=map[name:my-repo owner:octocat] with BuildErr non-nil, against a clean
// parent commit. These arms are theirs in substance.
//
// The suite was green because it had no mapping arm at all: it asserted that the branch fires and never
// asked what ran next.

const userRepositoryShapeDoc = `
openapi: 3.0.0
info: {title: t, version: "1"}
paths:
  /repos/{owner}/{name}:
    get:
      parameters:
        - name: owner
          in: path
          required: true
          schema: {type: string}
        - name: name
          in: path
          required: true
          schema: {type: string}
        - name: scope
          in: query
          schema: {type: string}
        - name: X-Owner
          in: header
          schema: {type: string}
      responses:
        "200": {description: ok}
`

func userRepoCallInfo(t *testing.T, mappings []getter.RequestFieldMappingItem) *CallInfo {
	t.Helper()
	d, err := libopenapi.NewDocument([]byte(userRepositoryShapeDoc))
	if err != nil {
		t.Fatalf("fixture is not a document: %v", err)
	}
	v3Doc, errs := d.BuildV3Model()
	if len(errs) > 0 {
		t.Fatalf("fixture did not build: %v", errs)
	}
	cli := &restclient.UnstructuredClient{DocScheme: v3Doc}
	info := &getter.Info{
		Resource: getter.Resource{
			Kind:        "UserRepository",
			Identifiers: []string{"name"},
			VerbsDescription: []getter.VerbsDescription{{
				Action: string(apiaction.Get), Method: "GET", Path: "/repos/{owner}/{name}",
				RequestFieldMapping: mappings,
			}},
		},
	}
	_, callInfo, err := APICallBuilder(cli, info, apiaction.Get)
	if err != nil {
		t.Fatalf("APICallBuilder: %v", err)
	}
	if callInfo == nil {
		t.Fatal("no CallInfo")
	}
	if !callInfo.ReqParams.Parameters.Contains("owner") {
		t.Fatal("ReqParams.Parameters does not contain owner, so these arms cannot exercise the branch")
	}
	return callInfo
}

// The UserRepository shape, after create has populated status. This FAILED on the first version of the
// fix and passes on its parent, which is what makes it a regression arm rather than a feature arm.
func TestAMappedPathParameterSurvivesACompositeStatusValue(t *testing.T) {
	ci := userRepoCallInfo(t, []getter.RequestFieldMappingItem{
		{InPath: "owner", InCustomResource: "status.owner.login"},
		{InPath: "name", InCustomResource: "status.name"},
	})
	cfg := BuildCallConfig(context.Background(), ci, crWithStatus(map[string]interface{}{
		"owner": map[string]interface{}{"login": "octocat"},
		"name":  "my-repo",
	}), nil, nil)
	if cfg == nil {
		t.Fatal("no RequestConfiguration")
	}
	if cfg.BuildErr != nil {
		t.Fatalf("BuildErr = %v.\nThe mapping supplied owner=%q, so the request is correct and must not be "+
			"refused. Deciding at the point of discovery refuses every get on this kind.",
			cfg.BuildErr, cfg.Parameters["owner"])
	}
	if got := cfg.Parameters["owner"]; got != "octocat" {
		t.Errorf("Parameters[owner] = %q, want %q", got, "octocat")
	}
	if got := cfg.Parameters["name"]; got != "my-repo" {
		t.Errorf("Parameters[name] = %q, want %q", got, "my-repo")
	}
}

// Pre-create, with an empty status: nothing to decline, and it passed on both versions. Kept so the arm
// above cannot be "fixed" by making the whole path unreachable.
func TestTheMappedShapePassesBeforeCreateToo(t *testing.T) {
	ci := userRepoCallInfo(t, []getter.RequestFieldMappingItem{
		{InPath: "owner", InCustomResource: "status.owner.login"},
	})
	cfg := BuildCallConfig(context.Background(), ci, crWithStatus(map[string]interface{}{}), nil, nil)
	if cfg == nil {
		t.Fatal("no RequestConfiguration")
	}
	if cfg.BuildErr != nil {
		t.Fatalf("BuildErr = %v on an empty status", cfg.BuildErr)
	}
}

// A mapping can target inPath, inQuery or inBody -- there is no inHeader. So a composite header value
// can never be rescued, and the refusal must still arrive even with mappings present.
func TestACompositeHeaderIsRefusedEvenWithMappingsPresent(t *testing.T) {
	ci := userRepoCallInfo(t, []getter.RequestFieldMappingItem{
		{InPath: "owner", InCustomResource: "status.owner.login"},
		{InPath: "name", InCustomResource: "status.name"},
	})
	cfg := BuildCallConfig(context.Background(), ci, crWithStatus(map[string]interface{}{
		"owner":   map[string]interface{}{"login": "octocat"},
		"name":    "my-repo",
		"X-Owner": map[string]interface{}{"login": "octocat"},
	}), nil, nil)
	if cfg == nil {
		t.Fatal("no RequestConfiguration")
	}
	if cfg.BuildErr == nil {
		t.Fatal("BuildErr is nil: no mapping can supply a header, so a composite there is unrescuable " +
			"and must be refused")
	}
	if _, ok := cfg.Headers["X-Owner"]; ok {
		t.Errorf("Headers[X-Owner] = %q was written despite the refusal", cfg.Headers["X-Owner"])
	}
}

// And the other direction: a mapping for a DIFFERENT parameter must not clear the verdict on this one.
// Without this, "some writer supplied something" could be mistaken for "this field was supplied".
func TestAMappingForAnotherParameterDoesNotClearTheVerdict(t *testing.T) {
	ci := userRepoCallInfo(t, []getter.RequestFieldMappingItem{
		{InPath: "name", InCustomResource: "status.name"},
	})
	cfg := BuildCallConfig(context.Background(), ci, crWithStatus(map[string]interface{}{
		"owner": map[string]interface{}{"login": "octocat"},
		"name":  "my-repo",
	}), nil, nil)
	if cfg == nil {
		t.Fatal("no RequestConfiguration")
	}
	if cfg.BuildErr == nil {
		t.Fatalf("BuildErr is nil, but nothing supplied owner: Parameters=%v", cfg.Parameters)
	}
}
