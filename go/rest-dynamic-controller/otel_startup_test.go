package main

import (
	"os"
	"strings"
	"testing"
)

// TestOtelEndpointForLog covers the only thing the helper must get right: it reports PRESENCE and never
// the value. A node-local collector's endpoint is derived from the pod's status.hostIP, so printing it
// would put a node address into every generated controller's startup line on every restart.
func TestOtelEndpointForLog(t *testing.T) {
	const key = "OTEL_EXPORTER_OTLP_ENDPOINT"
	secret := "http://10.244.3.17:4318"

	for _, tc := range []struct {
		name, set, want string
		unset           bool
	}{
		{name: "unset", unset: true, want: "unset"},
		{name: "empty string is unset", set: "", want: "unset"},
		{name: "a real endpoint", set: secret, want: "set"},
		{name: "whitespace is a value", set: " ", want: "set"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.unset {
				t.Setenv(key, "x")
				if err := os.Unsetenv(key); err != nil {
					t.Fatalf("unsetenv: %v", err)
				}
			} else {
				t.Setenv(key, tc.set)
			}
			got := otelEndpointForLog()
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			// THE ASSERTION THAT MATTERS, and it can fail: the return value must never CONTAIN the
			// endpoint. A helper changed to return the value itself would pass every check above if
			// `want` were relaxed, so this is asserted separately from the equality.
			if tc.set == secret && (got == secret || len(got) > 5) {
				t.Errorf("the return value leaks the endpoint: %q. On a node-local collector this is a "+
					"node address, logged by 34 controllers on every restart", got)
			}
		})
	}
}

// TestOtelStartupDiagnosis_TheFourStates pins which combination produces which conclusion. The two Warn
// cases are the defect in issue 729 and they fail DIFFERENTLY, which is why both exist.
func TestOtelStartupDiagnosis_TheFourStates(t *testing.T) {
	// CALLS THE REAL FUNCTION. The first version of this test re-implemented the same switch inline,
	// which asserts a copy and proves nothing about the code that runs -- the exact failure I have been
	// catching in other people's checks all day. otelExportState is now extracted for this reason.
	diagnose := otelExportState

	for _, tc := range []struct {
		name                            string
		endpoint, metrics, traces, logs bool
		want                            string
	}{
		// 057's measured state before #729: the endpoint set on 34 controllers, every channel default false.
		{"057 as measured: endpoint, nothing on", true, false, false, false, "endpoint-without-channel"},
		// 057's measured state TODAY: logs on via rdc.env, metrics and traces absent entirely.
		{"057 today: logs only, endpoint set", true, false, false, true, "consistent"},
		// The cdc's measured state, for contrast: logs off, the other two on.
		{"cdc as measured: traces+metrics, no logs", true, true, true, false, "consistent"},
		// The worse direction: telemetry produced and discarded.
		{"logs on, NO endpoint", false, false, false, true, "channel-without-endpoint"},
		{"metrics on, NO endpoint", false, true, false, false, "channel-without-endpoint"},
		// Nothing configured at all is not a misconfiguration.
		{"nothing at all", false, false, false, false, "consistent"},
	} {
		if got := diagnose(tc.endpoint, tc.metrics, tc.traces, tc.logs); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestAuditScopeNameIsTheModulePath pins the one half of issue 744 that was a real defect.
//
// Every other audit writer in the platform passes `github.com/krateo-platformops/<component>/audit`;
// this controller passed a bare `rest-dynamic-controller/audit`, so its rows looked like they came from
// somewhere else. The observability session found it in a dz-audit sweep and put it plainly: "a writer's
// scope is how a reader tells writers apart."
//
// Routing does not depend on it -- that keys on event.name=audit -- which is exactly why the mismatch
// survived several releases unnoticed. So this asserts legibility, and it asserts it by SHAPE rather than
// by string equality, because the thing that matters is the convention and not this one spelling.
func TestAuditScopeNameIsTheModulePath(t *testing.T) {
	const want = "github.com/krateo-platformops/rest-dynamic-controller/audit"
	if auditScopeName != want {
		t.Errorf("auditScopeName = %q, want %q", auditScopeName, want)
	}
	// The convention, asserted separately: a reader joining the platform matches on the prefix and the
	// suffix, not on the component in between.
	if !strings.HasPrefix(auditScopeName, "github.com/krateo-platformops/") {
		t.Error("the scope must start with the org's module prefix, or a consumer grouping writers by " +
			"module cannot see this one at all")
	}
	if !strings.HasSuffix(auditScopeName, "/audit") {
		t.Error("the scope must end in /audit: that is what distinguishes the audit emitter's scope " +
			"from the controller's other instrumentation scopes within the same module")
	}
	// NEGATIVE CONTROL: the bare service name must NOT satisfy the convention, or the two assertions
	// above would pass for the value this change replaced.
	if strings.HasPrefix(serviceName+"/audit", "github.com/krateo-platformops/") {
		t.Fatal("the old value satisfies the prefix check, so these assertions cannot tell the new " +
			"value from the old one")
	}
}
