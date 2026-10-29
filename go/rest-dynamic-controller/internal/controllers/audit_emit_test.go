package restResources

import (
	"context"
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
