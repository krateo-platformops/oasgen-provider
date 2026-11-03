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

// #305. An OAS header parameter declaring `required: true` AND a `default` generates a CORRECT CRD -- the
// generator honours the default and the apiserver materialises it onto the CR -- and then nothing ever
// populated it into the request. ValidateRequest then demanded a header the CR was carrying, and the call
// failed with "missing header: <name>".
//
// The issue's own stated mechanism ("default is not consulted at request construction") is misleading:
// `default` IS consulted, by the generator. The drop was population, and population only -- opts.Headers
// reaches the wire at restclient.go:252 and opts.Cookies via AddCookie at :297. ReqParams.Headers and
// .Cookies had ZERO reads anywhere in either module, against a control of 3 for the sibling fields.
//
// These cases drive the real BuildCallConfig with ReqParams derived from a real document, rather than a
// hand-built CallInfo -- because a hand-built ReqParams would assert that the branch copies a value, not
// that the value ever gets into ReqParams in the first place.

const headerDoc = `
openapi: 3.0.0
info: {title: t, version: "1"}
paths:
  /things:
    post:
      parameters:
        - name: X-Tenant
          in: header
          required: true
          schema: {type: string, default: "acme"}
        - name: X-Api-Key
          in: header
          schema: {type: string}
        - name: locale
          in: cookie
          schema: {type: string}
      responses:
        "201": {description: created}
`

// callInfoFromDoc builds a CallInfo the way production does: APICallBuilder over a real client whose
// ReqParams come from RequestedParams on a real libopenapi document.
func callInfoFromDoc(t *testing.T, action apiaction.APIAction) *CallInfo {
	t.Helper()
	d, err := libopenapi.NewDocument([]byte(headerDoc))
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
			Kind:        "Thing",
			Identifiers: []string{"name"},
			VerbsDescription: []getter.VerbsDescription{
				{Action: string(apiaction.Create), Method: "POST", Path: "/things"},
			},
		},
	}
	_, callInfo, err := APICallBuilder(cli, info, action)
	if err != nil {
		t.Fatalf("APICallBuilder: %v", err)
	}
	if callInfo == nil {
		t.Fatal("no CallInfo")
	}
	// THE PREMISE. If ReqParams.Headers did not contain the name, every case below would pass trivially
	// for the wrong reason -- the branch would simply never fire. Assert the input before the behaviour.
	if !callInfo.ReqParams.Headers.Contains("X-Tenant") {
		t.Fatalf("ReqParams.Headers does not contain X-Tenant, so these cases cannot exercise the "+
			"population branch at all. Got headers=%v", callInfo.ReqParams.Headers)
	}
	if !callInfo.ReqParams.Cookies.Contains("locale") {
		t.Fatalf("ReqParams.Cookies does not contain locale. Got cookies=%v", callInfo.ReqParams.Cookies)
	}
	return callInfo
}

func crWithSpec(spec map[string]interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{"spec": spec}}
	u.SetKind("Thing")
	u.SetName("thing-1")
	u.SetNamespace("default")
	return u
}

// A header parameter whose value the CR carries -- whether the author wrote it or the apiserver
// materialised the OAS default -- must reach the request.
func TestAHeaderParameterIsPopulatedFromTheCRSpec(t *testing.T) {
	ci := callInfoFromDoc(t, apiaction.Create)
	cfg := BuildCallConfig(context.Background(), ci, crWithSpec(map[string]interface{}{"X-Tenant": "acme"}), nil, nil)
	if cfg == nil {
		t.Fatal("no RequestConfiguration")
	}
	if got := cfg.Headers["X-Tenant"]; got != "acme" {
		t.Errorf("Headers[X-Tenant] = %q, want %q. Unpopulated, ValidateRequest then demands a header the "+
			"CR is carrying and the call fails with \"missing header: X-Tenant\" (#305)", got, "acme")
	}
}

func TestACookieParameterIsPopulatedFromTheCRSpec(t *testing.T) {
	ci := callInfoFromDoc(t, apiaction.Create)
	cfg := BuildCallConfig(context.Background(), ci, crWithSpec(map[string]interface{}{"locale": "it-IT"}), nil, nil)
	if cfg == nil {
		t.Fatal("no RequestConfiguration")
	}
	if got := cfg.Cookies["locale"]; got != "it-IT" {
		t.Errorf("Cookies[locale] = %q, want %q. ValidateRequest demands a required cookie with no "+
			"exemption of any kind, and oasgen's own skip tests In == \"header\" so it never applies to "+
			"a cookie", got, "it-IT")
	}
}

// THE SECURITY CASE, and the one worth most. Measured with the refusal removed, ground truth from a test
// server: `server saw X-Api-Key="s3cret" from the CR SPEC`.
//
// A credential in the main CR spec is WIDER read access than a Secret -- anyone with get on the CR -- and
// RedactValues does not cover it, being populated solely from secretRef. So only the by-name
// credentialHeaders list catches it, and it has to be checked in RDC rather than only in the generator:
// the generator's skip matches *authorization* alone, 1 of the 9 names, and RDC must not trust the CRD it
// is handed.
func TestACredentialHeaderIsRefusedFromTheCRSpec(t *testing.T) {
	ci := callInfoFromDoc(t, apiaction.Create)
	cfg := BuildCallConfig(context.Background(), ci, crWithSpec(map[string]interface{}{
		"X-Api-Key": "s3cret",
		"X-Tenant":  "acme", // a non-credential header beside it, so a blanket refusal would fail this case
	}), nil, nil)
	if cfg == nil {
		t.Fatal("no RequestConfiguration")
	}
	if got, ok := cfg.Headers["X-Api-Key"]; ok {
		t.Errorf("Headers[X-Api-Key] = %q, but a credential header must not be populated from the CR "+
			"spec: that puts a credential in cleartext where anyone with get on the CR can read it, and "+
			"RedactValues will not redact it because that is populated solely from secretRef", got)
	}
	// The control: the refusal must be by NAME, not a blanket refusal of every header.
	if got := cfg.Headers["X-Tenant"]; got != "acme" {
		t.Errorf("Headers[X-Tenant] = %q, want \"acme\": the refusal must be per-name, or this case would "+
			"pass with a branch that populates nothing at all", got)
	}
}

// Every credential header name, not just the one the document happens to declare. A list used for log
// redaction and a list used for refusal that are the same list cannot drift apart -- this asserts they
// are in fact the same list.
func TestEveryCredentialHeaderNameIsRecognised(t *testing.T) {
	for _, name := range []string{
		"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key",
		"Api-Key", "X-Auth-Token", "X-Amz-Security-Token", "Private-Token",
	} {
		if !restclient.IsCredentialHeader(name) {
			t.Errorf("IsCredentialHeader(%q) = false, want true", name)
		}
		// Header names are case-insensitive on the wire, so the refusal must be too -- otherwise
		// `x-api-key` in a document walks straight past it.
		if !restclient.IsCredentialHeader("x-api-KEY") {
			t.Error("IsCredentialHeader must be case-insensitive: header names are, so a document " +
				"spelling it `x-api-key` would otherwise bypass the refusal")
		}
	}
	// CONTROL: a non-credential header must NOT be refused, or the predicate is useless.
	for _, name := range []string{"X-Tenant", "Accept", "Content-Type", "X-Request-Id"} {
		if restclient.IsCredentialHeader(name) {
			t.Errorf("IsCredentialHeader(%q) = true, want false", name)
		}
	}
}

// PRECEDENCE: the Configuration CR's header beats the CR spec's value.
//
// `applyConfigSpec` runs at builder.go:138, BEFORE processFields, so the Configuration CR's header is
// already in the map when the spec is walked -- and the `!ok` guard is what preserves it. That ordering is
// tier-for-tier identical to the Query branch's, so it needed no new decision; this case pins it so a
// later change to either has to notice.
//
// THIS CASE IS VACUOUS WITHOUT ITS OWN MUTATION, and that is the point of recording it here. It PASSES at
// HEAD: with no population branch at all, nothing overwrites the Configuration CR's value, so the right
// answer comes out for the wrong reason. Removing the population branch does NOT kill it. Only removing
// the `!ok` guard does. A test that one plausible mutation cannot kill is a test whose value has to be
// demonstrated rather than assumed.
func TestTheConfigurationCRHeaderBeatsTheCRSpec(t *testing.T) {
	ci := callInfoFromDoc(t, apiaction.Create)
	configSpec := map[string]interface{}{
		"header": map[string]interface{}{
			"create": map[string]interface{}{
				"X-Tenant": "from-configuration-cr",
			},
		},
	}
	cfg := BuildCallConfig(context.Background(), ci,
		crWithSpec(map[string]interface{}{"X-Tenant": "from-spec-default"}), configSpec, nil)
	if cfg == nil {
		t.Fatal("no RequestConfiguration")
	}
	if got := cfg.Headers["X-Tenant"]; got != "from-configuration-cr" {
		t.Errorf("Headers[X-Tenant] = %q, want %q. The Configuration CR is the operator's channel and the "+
			"CR spec is the author's; an apiserver-materialised OAS default must not silently beat an "+
			"operator's explicit setting", got, "from-configuration-cr")
	}
}
