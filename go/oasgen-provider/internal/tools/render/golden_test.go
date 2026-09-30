package render_test

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/oas2jsonschema"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/render"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite the golden file from current output")

// TestCRDsMatchGolden pins the exact CRDs render.CRDs produces for a known RestDefinition.
//
// It exists because nothing else in the repo can catch a change to render.CRDs. The parity test
// (TestRenderMatchesControllerCreate) compares the /render HTTP surface against the controller's apply
// path — but since #157 BOTH of those call render.CRDs, so a change to it moves both sides identically
// and the comparison still passes. Verified: appending a bogus category inside render.CRDs leaves that
// test green.
//
// That matters more than it sounds. render.CRDs is the generation half of every RestDefinition reconcile
// in every install; a silent change to it changes the CRDs served to users. The refactor that created it
// WAS behaviour-preserving — checked by diffing the controller's applied CRDs on main against the branch,
// byte-identical — but that check was a one-off run by hand and left nothing behind that could fail.
// This is the thing that fails.
//
// When a change here is intentional, regenerate and review the diff as part of the change:
//
//	go test ./internal/tools/render/ -run TestCRDsMatchGolden -update-golden
//
// The diff is the point. A golden file whose updates are not read is just a slower way of changing
// behaviour silently.
func TestCRDsMatchGolden(t *testing.T) {
	const golden = "testdata/github-repo.crds.json"

	rdYAML, err := os.ReadFile("../../../../../examples/github-repo/restdefinition.yaml")
	require.NoError(t, err)
	oasText, err := os.ReadFile("../../../samples/usage_guide/assets/repo.yaml")
	require.NoError(t, err)

	rdJSON, err := yaml.YAMLToJSON(rdYAML)
	require.NoError(t, err)
	cr := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, json.Unmarshal(rdJSON, cr))

	doc, err := oas2jsonschema.NewLibOASParser().Parse(oasText)
	require.NoError(t, err)

	res, err := render.CRDs(context.Background(), cr, render.TargetGVK(cr, doc), doc, render.HasSecuritySchemes(doc))
	require.NoError(t, err)
	require.NotNil(t, res.CRD)
	require.NotNil(t, res.ConfigurationCRD, "the example's document declares a bearer scheme")

	got, err := json.MarshalIndent(map[string]any{
		"crd":                    res.CRD,
		"configurationCrd":       res.ConfigurationCRD,
		"skippedSecuritySchemes": res.SkippedSecuritySchemes,
	}, "", "  ")
	require.NoError(t, err)
	got = append(got, '\n')

	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o755))
		require.NoError(t, os.WriteFile(golden, got, 0o644))
		t.Logf("wrote %s (%d bytes) — READ THE DIFF before committing", golden, len(got))
		return
	}

	want, err := os.ReadFile(golden)
	require.NoError(t, err, "golden file missing; regenerate with -update-golden and review the diff")
	require.Equal(t, string(want), string(got),
		"render.CRDs output changed. If deliberate, regenerate with -update-golden and review the diff as "+
			"part of the change — this is the generation half of every RestDefinition reconcile, so a change "+
			"here changes the CRDs served to users.")
}

// Generation must be deterministic, or the golden above is a flake generator rather than a guard.
func TestCRDsAreDeterministic(t *testing.T) {
	rdYAML, err := os.ReadFile("../../../../../examples/github-repo/restdefinition.yaml")
	require.NoError(t, err)
	oasText, err := os.ReadFile("../../../samples/usage_guide/assets/repo.yaml")
	require.NoError(t, err)
	rdJSON, err := yaml.YAMLToJSON(rdYAML)
	require.NoError(t, err)

	render1 := func() string {
		cr := &definitionv1alpha1.RestDefinition{}
		require.NoError(t, json.Unmarshal(rdJSON, cr))
		doc, derr := oas2jsonschema.NewLibOASParser().Parse(oasText)
		require.NoError(t, derr)
		res, rerr := render.CRDs(context.Background(), cr, render.TargetGVK(cr, doc), doc, render.HasSecuritySchemes(doc))
		require.NoError(t, rerr)
		b, merr := json.Marshal(map[string]any{"crd": res.CRD, "cfg": res.ConfigurationCRD})
		require.NoError(t, merr)
		return string(b)
	}

	first := render1()
	for i := 0; i < 4; i++ {
		require.Equal(t, first, render1(), "run %d differs; map iteration order is leaking into generated output", i+2)
	}
}
