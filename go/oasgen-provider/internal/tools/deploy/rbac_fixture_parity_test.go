//go:build unit

package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The RBAC templates exist in THREE places: the asset the chart ships, and two test fixture sets. Before
// #473 all three had drifted:
//
//	helm/oasgen-provider/assets/rdc/rbac/clusterrole.yaml            35 lines, name carries the apiGroup
//	internal/tools/deploy/testdata/clusterrole.yaml                  11 lines, no apiGroup in the name
//	internal/controllers/.../testdata/setup/rdc/rbac/clusterrole.yaml 29 lines, no apiGroup in the name
//
// So every test of this machinery rendered a template resembling nothing deployed, and the divergence that
// mattered -- a cluster-wide `secrets` grant present only in the shipped copy -- made the narrow
// auth-secret Role a strict subset of a grant the ServiceAccount already held. The template and the
// fixtures were each verified alone and never compared.
//
// This test is the comparison. It is deliberately a PARITY assertion rather than a golden file: a golden
// would be a fourth copy to drift.
//
// THE ONLY PERMITTED DIFFERENCE is helm escaping. The shipped asset is itself rendered by helm, so its
// template actions are wrapped as {{`{{ .x }}`}} to survive that pass; the fixtures are consumed directly.
// Stripping the wrapper is the whole transformation, and nothing else may differ.
func TestRBACFixturesMatchTheShippedAssets(t *testing.T) {
	const shippedDir = "../../../../../helm/oasgen-provider/assets/rdc/rbac"
	fixtureDirs := []string{
		"testdata",
		"../../controllers/restdefinition/testdata/setup/rdc/rbac",
	}

	entries, err := os.ReadDir(shippedDir)
	require.NoError(t, err, "the shipped asset directory must exist -- a wrong path here would make this test vacuous")
	require.NotEmpty(t, entries, "the shipped asset directory must not be empty")

	var compared int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		shipped, rerr := os.ReadFile(filepath.Join(shippedDir, e.Name()))
		require.NoError(t, rerr)
		want := normaliseRBAC(unescapeHelm(string(shipped)))

		for _, dir := range fixtureDirs {
			path := filepath.Join(dir, e.Name())
			raw, ferr := os.ReadFile(path)
			if os.IsNotExist(ferr) {
				// A fixture set need not carry every shipped file; it must not carry a DIFFERENT one.
				continue
			}
			require.NoError(t, ferr)
			require.Equal(t, want, normaliseRBAC(string(raw)),
				"%s has drifted from the shipped asset %s/%s -- a test rendering this fixture is not "+
					"testing what the chart deploys", path, shippedDir, e.Name())
			compared++
		}
	}

	// Without this the test passes when every path is wrong and nothing is compared at all.
	require.GreaterOrEqual(t, compared, 6,
		"expected at least 6 fixture/asset pairs to be compared; got %d, which means the paths are wrong "+
			"and this test is asserting nothing", compared)
	t.Logf("compared %d fixture/asset pairs", compared)
}

func unescapeHelm(s string) string {
	return strings.NewReplacer("{{`", "", "`}}", "").Replace(s)
}

// normaliseRBAC drops comments and blank lines and trims each line: the shipped assets carry explanatory
// comments that a fixture has no reason to repeat, and indentation is not what this test is about.
func normaliseRBAC(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, t)
	}
	return out
}
