package deploy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTemplate writes a post-helm-render RDC configmap template: the chart escapes its own braces, so by
// the time the controller reads this file the Values range has already been expanded into literal lines
// and only the per-RestDefinition placeholders remain.
func writeTemplate(t *testing.T, extraData string) string {
	t.Helper()
	body := `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .resource }}-{{ .apiVersion }}-configmap
  namespace: {{ .namespace }}
data:
  REST_CONTROLLER_SERVICEACCOUNT_NAME: {{ .composition_controller_sa_name }}
  REST_CONTROLLER_SERVICEACCOUNT_NAMESPACE: {{ .composition_controller_sa_namespace }}
  HOME: /tmp
` + extraData

	p := filepath.Join(t.TempDir(), "configmap.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func TestRenderedSnowplowURLReadsTheKeyAConfiguredChartWrites(t *testing.T) {
	p := writeTemplate(t, "  URL_SNOWPLOW: \"http://snowplow.krateo-system.svc.cluster.local:8081\"\n")

	got, err := RenderedSnowplowURL(p)

	require.NoError(t, err)
	assert.Equal(t, "http://snowplow.krateo-system.svc.cluster.local:8081", got)
}

// rdc.env defaults to {}, so "absent" is the shape a default install actually produces, and "declared but
// empty" is what someone gets from `--set rdc.env.URL_SNOWPLOW=`. RDC treats both as no client at all, so
// the check has to as well -- reporting "configured" for an empty string would suppress the warning in
// precisely the case the warning is for.
func TestRenderedSnowplowURLTreatsAbsentAndEmptyAlike(t *testing.T) {
	for name, extra := range map[string]string{
		"absent":          "",
		"empty string":    "  URL_SNOWPLOW: \"\"\n",
		"other keys only": "  URL_AUTHN: \"http://authn\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := RenderedSnowplowURL(writeTemplate(t, extra))
			require.NoError(t, err)
			assert.Empty(t, got, "the caller must see this as unconfigured")
		})
	}
}

// Whitespace is returned verbatim, and that is not an oversight. RDC builds its snowplow client whenever
// URL_SNOWPLOW != "", so "   " yields a client that fails on a malformed request URL -- a real problem,
// but NOT "no snowplow client is configured". Trimming here would make the caller's event say something
// false about such an install, so the raw value crosses the boundary and the caller compares it the same
// way RDC does.
func TestRenderedSnowplowURLDoesNotTrim(t *testing.T) {
	got, err := RenderedSnowplowURL(writeTemplate(t, "  URL_SNOWPLOW: \"   \"\n"))

	require.NoError(t, err)
	assert.Equal(t, "   ", got, "RDC would build a client from this; the check must not call it unconfigured")
}

// An error, not an empty string. Empty means "snowplow is not configured" and would make the controller
// warn about a real, declared apiRef on the strength of a file it could not read -- a confident warning
// derived from no evidence. The caller fails open on the error instead.
func TestRenderedSnowplowURLErrorsRatherThanReportingUnconfigured(t *testing.T) {
	_, err := RenderedSnowplowURL(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	assert.Error(t, err)

	bad := filepath.Join(t.TempDir(), "notayaml.yaml")
	require.NoError(t, os.WriteFile(bad, []byte("this: [is not: a configmap"), 0o600))
	_, err = RenderedSnowplowURL(bad)
	assert.Error(t, err)
}

// A tripwire on the coupling this check depends on: URL_SNOWPLOW is not named anywhere in the chart, it
// arrives because the asset ranges over .Values.rdc.env into the ConfigMap's data. If that mechanism is
// ever replaced -- an explicit env block, a Secret, a downward-API ref -- RenderedSnowplowURL goes on
// returning "" and the warning starts firing on correctly configured installs, which is worse than not
// warning at all. Assert the mechanism, since the key itself cannot be asserted.
func TestChartStillDeliversRdcEnvIntoTheConfigmapData(t *testing.T) {
	b, err := os.ReadFile("../../../../../helm/oasgen-provider/assets/rdc/configmap.yaml")
	require.NoError(t, err, "the RDC configmap asset is the only path URL_SNOWPLOW travels")

	asset := string(b)
	assert.Contains(t, asset, "range $key, $value := .Values.rdc.env",
		"RenderedSnowplowURL reads data[URL_SNOWPLOW]; it is only ever there because of this range")
	assert.Contains(t, asset, "data:", "and the range must still land under data")
	assert.NotContains(t, asset, SnowplowURLKey,
		"the chart must not name the key itself -- if it starts to, this check needs to read it from there instead")
}
