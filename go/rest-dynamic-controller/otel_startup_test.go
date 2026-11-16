package main

import (
	"os"
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
