package restResources

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/krateo-platformops/audit"
	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/embedded"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// recordingLogger captures what the audit emitter writes, so a test can assert on the EVENT rather than
// on the fact that a function was called. The OTel Logs API is the only seam available: audit.Emitter
// holds a log.Logger and exposes nothing about what it emitted.
type recordingLogger struct {
	embedded.Logger
	records []otellog.Record
}

func (l *recordingLogger) Emit(ctx context.Context, r otellog.Record) {
	l.records = append(l.records, r)
}
func (l *recordingLogger) Enabled(context.Context, otellog.EnabledParameters) bool {
	return true
}

func (l *recordingLogger) attrs(i int) map[string]string {
	out := map[string]string{}
	// attribute.KeyValue, not otellog.KeyValue: otel/log >= v0.21 moved KeyValue/Value/String to
	// otel/attribute, which is the same move that made audit v0.1.0 fail to compile against the version
	// this module links.
	l.records[i].WalkAttributes(func(kv attribute.KeyValue) bool {
		out[string(kv.Key)] = kv.Value.Emit()
		return true
	})
	return out
}

func auditTestCR() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("vcloud.aruba.krateo.io/v1alpha1")
	u.SetKind("CloudServer")
	u.SetName("srv-1")
	u.SetNamespace("demo-system")
	u.SetUID("b132fb62-998d-479f-9c9d-794c6f1ea2fd")
	u.SetGeneration(7)
	u.SetResourceVersion("90210")
	return u
}

// TestEmitAuditCarriesTheOptionBJoinKey is the assertion C8 exists for. snowplow audits the apiserver
// write that created the CR; this event records the EXTERNAL effect. Without uid and the generation the
// reconcile acted on, the two records cannot be matched and the audit trail answers no question worth
// asking.
func TestEmitAuditCarriesTheOptionBJoinKey(t *testing.T) {
	lg := &recordingLogger{}
	h := &handler{auditEmitter: audit.New(lg)}

	h.emitAudit(context.Background(), auditTestCR(), "https://api.arubacloud.com/v1", "POST", 201, nil)

	require.Len(t, lg.records, 1, "one external write must produce exactly one audit event")
	a := lg.attrs(0)

	assert.Equal(t, "b132fb62-998d-479f-9c9d-794c6f1ea2fd", a["k8s.object.uid"],
		"the join key back to snowplow's record of the user's action")
	assert.Equal(t, "7", a["k8s.object.generation"],
		"the generation the reconcile ACTED ON, not the CR's current one")
	assert.Equal(t, "CloudServer", a["k8s.resource.kind"])
	assert.Equal(t, "demo-system", a["k8s.namespace.name"])
	assert.Equal(t, "success", a["outcome"])
	assert.Equal(t, "201", a["http.response.status_code"])
	assert.Equal(t, "POST", a["http.request.method"])
	assert.Equal(t, "rest.write", a["krateo.action"])
}

// TestEmitAuditReducesServerAddressToHost is the one field where getting it wrong writes a credential to
// a log. Vendor paths routinely carry identifiers and at least one carries a token-shaped segment, so
// server.address must be host[:port] and nothing else.
//
// The reduction happens in audit v0.3.0 rather than here, which is the point: asserting it from this side
// pins the behaviour we DEPEND on, so a dependency bump that regressed it fails our suite rather than
// silently widening what we log.
func TestEmitAuditReducesServerAddressToHost(t *testing.T) {
	lg := &recordingLogger{}
	h := &handler{auditEmitter: audit.New(lg)}

	h.emitAudit(context.Background(), auditTestCR(),
		"https://user:secret@api.arubacloud.com:8443/v1/cloudServers/tok_abc123?key=sensitive",
		"DELETE", 204, nil)

	require.Len(t, lg.records, 1)
	got := lg.attrs(0)["server.address"]

	assert.Equal(t, "api.arubacloud.com:8443", got)
	assert.NotContains(t, got, "secret", "userinfo must not survive")
	assert.NotContains(t, got, "tok_abc123", "a path segment must not survive")
	assert.NotContains(t, got, "sensitive", "a query value must not survive")
}

// TestEmitAuditOmitsStatusCodeForDelegatedWrites pins the deliberate asymmetry. A delegated write goes
// through snowplow's /call, so the status RDC can see is snowplow's, not the vendor's. Reporting it would
// assert the external API answered when nothing of the sort was established, so the field is omitted.
//
// This is the control arm for the honesty of the emitter: if someone later "fixes" the missing field by
// passing snowplow's 200, this fails.
func TestEmitAuditOmitsStatusCodeForDelegatedWrites(t *testing.T) {
	lg := &recordingLogger{}
	h := &handler{auditEmitter: audit.New(lg)}

	h.emitAudit(context.Background(), auditTestCR(), "https://api.arubacloud.com/v1", "POST", 0, nil)

	require.Len(t, lg.records, 1)
	_, present := lg.attrs(0)["http.response.status_code"]
	assert.False(t, present,
		"a status code we did not observe must be ABSENT, not reported as 0 or as snowplow's 200")
}

// TestEmitAuditIsANoOpWithoutAnEmitter -- the emitter is nil whenever -otel-logs-enabled is off, and every
// call site is unguarded by design. If this panics, the whole controller does.
func TestEmitAuditIsANoOpWithoutAnEmitter(t *testing.T) {
	h := &handler{}
	assert.NotPanics(t, func() {
		h.emitAudit(context.Background(), auditTestCR(), "https://x.example.com", "POST", 201, nil)
	})
}

// TestExternalStatusCodeReadsBothSources -- the status lives on *StatusError for a failure and on the
// Response for a success, and 0 means "never reached a status" rather than "zero".
func TestExternalStatusCodeReadsBothSources(t *testing.T) {
	assert.Equal(t, 409,
		externalStatusCode(&restclient.Response{}, &restclient.StatusError{StatusCode: 409}),
		"a failure carries the vendor status on StatusError")

	assert.Equal(t, 0,
		externalStatusCode(&restclient.Response{}, assert.AnError),
		"a transport error never reached a status, so 0 -- which audit.Emit omits")
}

// --- #318: a delegated write must not assert an HTTP method RDC never issued ---

// TestAuditOmitsMethodWhenNotIssuedByUs asserts the EMITTED ATTRIBUTES, not the arguments we passed.
//
// That distinction is the point: the defect was a value we supplied confidently, so a test checking what we
// supply would have agreed with the bug. The assertion is on what a consumer of the trail would read.
func TestAuditOmitsMethodWhenNotIssuedByUs(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		statusCode int
		// wantMethodPresent is whether http.request.method should appear at all.
		wantMethodPresent bool
		wantStatusPresent bool
	}{
		{
			// A delegated write: snowplow's RESTAction stages issue the request, so RDC knows neither the
			// method nor the status code. Both must be ABSENT, not guessed.
			name:              "delegated write omits both method and status",
			method:            "",
			statusCode:        0,
			wantMethodPresent: false,
			wantStatusPresent: false,
		},
		{
			// A native write: both are known from the OAS operation and the response, and both must appear.
			// This is the control -- if it ever fails, the change has omitted the method everywhere and the
			// audit trail has lost a field it was correctly recording.
			name:              "native write records both",
			method:            "PATCH",
			statusCode:        200,
			wantMethodPresent: true,
			wantStatusPresent: true,
		},
		{
			// A native write whose call failed before a response: the method is known, the code is not.
			// Asymmetry is legitimate here and must not be collapsed either way.
			name:              "native write with no response records the method only",
			method:            "DELETE",
			statusCode:        0,
			wantMethodPresent: true,
			wantStatusPresent: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lg := &recordingLogger{}
			h := &handler{auditEmitter: audit.New(lg)}

			h.emitAudit(context.Background(), auditTestCR(), "https://api.example.com/v1/things/abc", tc.method, tc.statusCode, nil)

			require.Len(t, lg.records, 1, "one external write must produce exactly one audit event")
			attrs := lg.attrs(0)

			_, methodPresent := attrs["http.request.method"]
			if methodPresent != tc.wantMethodPresent {
				t.Errorf("http.request.method present = %v, want %v (attrs: %v)", methodPresent, tc.wantMethodPresent, attrs)
			}
			_, statusPresent := attrs["http.response.status_code"]
			if statusPresent != tc.wantStatusPresent {
				t.Errorf("http.response.status_code present = %v, want %v", statusPresent, tc.wantStatusPresent)
			}
			if tc.wantMethodPresent && attrs["http.request.method"] != tc.method {
				t.Errorf("http.request.method = %q, want %q", attrs["http.request.method"], tc.method)
			}
		})
	}
}

// TestNoAuditCallSiteInventsAnHTTPMethod asserts the CALL SITES, not the helper.
//
// This is a source-level test, which is unusual and deliberate. The test above verifies what emitAudit does
// when given "" -- the helper's contract. It says nothing about whether the delegated call sites actually
// pass "", and I confirmed that gap by mutation: putting http.MethodPut back at one delegated site compiles
// and fails NOTHING. The contract was verified; conformance to it was not. Two good halves, never compared.
//
// A behaviour test would need a full delegated write driven through a snowplow stub for each of create,
// update and delete. That is worth having and is not this; in the meantime the invariant is genuinely
// source-level -- "no call site may pass a guessed method constant" -- so asserting it against the source is
// the honest instrument rather than a proxy for one.
//
// The rule: every emitAudit call passes EITHER callInfo.Method (native -- the method is read from the OAS
// operation that is about to be issued) OR "" (delegated -- snowplow issues the request and RDC never sees
// its method). An http.Method* constant at a call site is always a guess, because a site that KNOWS the
// method has callInfo to read it from.
func TestNoAuditCallSiteInventsAnHTTPMethod(t *testing.T) {
	src, err := os.ReadFile("restResources.go")
	require.NoError(t, err, "the file under test must be readable; a skipped check here is not a pass")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "restResources.go", src, 0)
	require.NoError(t, err)

	var sites int
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "emitAudit" {
			return true
		}
		sites++

		// emitAudit(ctx, mg, serverURL, httpMethod, statusCode, callErr) -- the method is argument 4.
		require.GreaterOrEqual(t, len(call.Args), 4, "emitAudit signature changed; this test must be updated rather than deleted")
		methodArg := call.Args[3]

		pos := fset.Position(call.Pos())
		switch a := methodArg.(type) {
		case *ast.BasicLit:
			assert.Equal(t, `""`, a.Value,
				"%s: a literal method at an emitAudit site is a guess; pass \"\" for a delegated write", pos)
		case *ast.SelectorExpr:
			// callInfo.Method is the only acceptable selector: it comes from the OAS operation being issued.
			if ident, ok := a.X.(*ast.Ident); ok && ident.Name == "http" {
				t.Errorf("%s: emitAudit is passed http.%s, which is a GUESS -- a delegated write must pass \"\" so the field is omitted (#318)",
					pos, a.Sel.Name)
			}
		}
		return true
	})

	// Guard against the check silently covering nothing: if emitAudit is renamed or the calls move, zero
	// sites would pass vacuously. The count is deliberately a floor, not an equality, so adding a legitimate
	// new emit site does not fail the build.
	assert.GreaterOrEqual(t, sites, 10,
		"expected to inspect at least the 10 known emitAudit sites; found %d, so this check is not reaching them", sites)
}
