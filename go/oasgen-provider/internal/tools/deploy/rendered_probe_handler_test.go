//go:build unit

package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEveryProbeInTheAssetIsGuardedAgainstAnEmptyHandler is the gate 0.85.1 needed and did not have.
//
// WHAT 0.85.1 SHIPPED. rdc.livenessProbe is `{}` on purpose -- the emptiness is a decision, visible
// in a values diff -- and the asset rendered it unconditionally:
//
//	livenessProbe:
//	  {{- toYaml .Values.rdc.livenessProbe | nindent 12 }}
//
// `toYaml {}` renders `{}`, and the apiserver REFUSES a probe with no handler:
//
//	Deployment.apps "x-controller" is invalid: spec.template.spec.containers[0].livenessProbe:
//	Required value: must specify a handler type
//
// Every generated controller was rejected, repeatedly, on every reconcile. At 0.84.0 the key was
// ABSENT, so `toYaml nil` rendered `null` and the field did not exist -- accepted.
//
// WHY NO EXISTING CHECK SAW IT, and this is the part worth keeping. The asset PARSES: `{}` is
// perfectly valid YAML. #856's gate decodes the asset through the provider's own CreateK8sObject and
// passed. The rejection happens at ADMISSION, which is a different question from "does this parse" --
// and the whole of 0.85.1 is the distance between those two facts.
//
// AND MY OWN TEST PINNED THE DEFECT. TestNoLivenessProbeIsWiredToTheReadinessCheck asserted that
// rdc.livenessProbe is an empty mapping "so the emptiness was a decision" -- a true statement about
// the VALUES that said nothing about what they render to.
//
// This asserts the STRUCTURE OF THE TEMPLATE: a probe field may only be emitted inside a guard, so an
// empty value renders nothing instead of an empty handler. A server dry-run against a live apiserver
// is the authoritative check and belongs in the integration suite; this is the offline half, and it
// is the half that can run on every PR.
func TestEveryProbeInTheAssetIsGuardedAgainstAnEmptyHandler(t *testing.T) {
	// internal/tools/deploy -> oasgen-provider -> go -> repo root
	p := filepath.Join("..", "..", "..", "..", "..", "helm", "oasgen-provider", "assets", "rdc", "deployment.yaml")
	b, err := os.ReadFile(p)
	require.NoError(t, err, "could not read the SHIPPED asset at %s -- a path error here would make "+
		"this gate pass vacuously, which is the failure mode it exists to prevent", p)
	src := string(b)

	// PARSE CONTROL: the right file, and it really does declare probes. Without this a wrong path or a
	// renamed field makes "no unguarded probe found" true and meaningless.
	require.Contains(t, src, "kind: Deployment", "the file read is not a Deployment")
	probes := regexp.MustCompile(`(?m)^\s*(livenessProbe|readinessProbe|startupProbe):`).FindAllString(src, -1)
	require.NotEmpty(t, probes, "the asset declares no probe fields at all, so this gate measures nothing")

	lines := strings.Split(src, "\n")
	for i, line := range lines {
		m := regexp.MustCompile(`^\s*(livenessProbe|readinessProbe|startupProbe):\s*$`).FindStringSubmatch(line)
		if m == nil {
			continue
		}
		// The guard must be the line immediately above. `with` is the only form that emits nothing for
		// an empty value; `if` on the same value would also work, so both are accepted.
		require.Greater(t, i, 0, "%s is the first line of the file", m[1])
		prev := strings.TrimSpace(lines[i-1])
		guarded := strings.HasPrefix(prev, "{{- with .Values.rdc.") || strings.HasPrefix(prev, "{{- if .Values.rdc.")
		require.True(t, guarded,
			"line %d emits %s UNGUARDED. If its value is an empty map, toYaml renders {} and the "+
				"apiserver REFUSES the Deployment -- \"must specify a handler type\" -- so every "+
				"generated controller fails to be created. That is what 0.85.1 did. The asset parses "+
				"either way, so no YAML-level check can see it. Wrap it in {{- with .Values.rdc.<probe> }}.\n"+
				"  line %d: %s\n  line %d: %s",
			i+1, m[1], i, prev, i+1, strings.TrimSpace(line))
	}
}

// TestTheReadinessEscapeHatchSaysNullNotEmpty pins a documentation claim that was wrong in the
// direction of the outage it warned about.
//
// The asset's own comment told an operator that disabling the probe means setting
// rdc.readinessProbe "empty". MEASURED: helm MERGES maps, so `readinessProbe: {}` in a values file
// leaves this chart's httpGet in place -- and with healthProbePort: 0 that is a probe pointing at a
// port nothing binds, which marks every pod NotReady. `readinessProbe: null` is what removes it.
//
//	readinessProbe: {}    -> rendered httpGet {path: /readyz, port: 8081}   STILL THERE
//	readinessProbe: null  -> readinessProbe absent                          REMOVED
func TestTheReadinessEscapeHatchSaysNullNotEmpty(t *testing.T) {
	p := filepath.Join("..", "..", "..", "..", "..", "helm", "oasgen-provider", "assets", "rdc", "deployment.yaml")
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	src := string(b)

	require.Contains(t, src, "must be set to NULL too",
		"the escape-hatch comment does not say NULL. Saying \"empty\" sends an operator to {}, which "+
			"helm MERGES -- the chart's own httpGet survives and every pod is marked NotReady, which "+
			"is the outcome that comment exists to prevent")
	require.NotContains(t, src, "rdc.readinessProbe must be empty too",
		"the superseded wording is still present; it is wrong in the direction of the outage")
}
