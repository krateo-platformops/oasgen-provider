//go:build unit || integration

package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequiredMemoryTracksTheCapAboveTheFloor(t *testing.T) {
	const MiB = 1 << 20
	for _, tc := range []struct {
		capMiB int64
		want   int64
		why    string
	}{
		{1, 512, "below the floor: the Go runtime needs more than a derivation from a tiny cap would give"},
		{4, 512, "THE DEFAULT. Must equal what the chart shipped before the knob existed, or upgrading changes a limit nobody asked to change"},
		{8, 512, "still under the floor"},
		{16, 708, "above the floor, so the derivation governs"},
		{32, 1288, "and it scales linearly from there"},
	} {
		got := RequiredMemoryMiB(tc.capMiB * MiB)
		assert.Equal(t, tc.want, got, "cap %d MiB: %s", tc.capMiB, tc.why)
	}

	// The table must contain rows on BOTH sides of the floor, or it passes against a constant.
	assert.NotEqual(t, RequiredMemoryMiB(4*MiB), RequiredMemoryMiB(32*MiB),
		"every row returned the floor, so this table cannot show the derivation does anything")
}

// THE CROSS-ARTIFACT CONTRACT. These constants are mirrored in the chart template, and Helm cannot
// import Go, so this test is the only thing keeping them together.
//
// If they drift the failure is nasty and non-obvious: the chart derives a limit, this check rejects
// it, and the service refuses to start from its OWN chart's defaults. That is a worse failure than
// the one the whole feature exists to prevent.
func TestTheChartDerivationMatchesThisPackage(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "..", "..",
		"helm", "controller-render-service", "templates", "deployment.yaml")
	b, err := os.ReadFile(path)
	require.NoError(t, err,
		"cannot read the chart template, so the constants below are unverified against it. Looked in %s", path)
	src := string(b)

	re := regexp.MustCompile(`max (\d+) \(add (\d+) \(div \(mul \.Values\.maxBodyMiB (\d+)\) (\d+)\)\)`)
	m := re.FindStringSubmatch(src)
	require.NotNil(t, m,
		"the derivation expression in the chart template no longer matches the shape this test reads. "+
			"If it was rewritten, re-point this test -- do not delete it: it is the only check that the "+
			"chart and this package agree, and disagreement makes the service refuse its own defaults")

	atoi := func(s string) int64 { v, _ := strconv.ParseInt(s, 10, 64); return v }
	assert.Equal(t, int64(budgetFloorMiB), atoi(m[1]), "the chart's floor differs from budgetFloorMiB")
	assert.Equal(t, int64(budgetBaselineMiB), atoi(m[2]), "the chart's baseline differs from budgetBaselineMiB")
	assert.Equal(t, int64(budgetMarginalNumerator), atoi(m[3]), "the chart's marginal numerator differs")
	assert.Equal(t, int64(budgetMarginalDenominator), atoi(m[4]), "the chart's marginal denominator differs")
}

func TestCheckMemoryBudget(t *testing.T) {
	const MiB = 1 << 20
	write := func(t *testing.T, name, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
		return p
	}
	// Point both layouts at nothing by default; each case sets what it needs.
	reset := func(t *testing.T) {
		t.Helper()
		v2, v1 := cgroupV2MemoryMaxPath, cgroupV1MemoryLimitPath
		missing := filepath.Join(t.TempDir(), "absent")
		cgroupV2MemoryMaxPath, cgroupV1MemoryLimitPath = missing, missing
		t.Cleanup(func() { cgroupV2MemoryMaxPath, cgroupV1MemoryLimitPath = v2, v1 })
	}

	t.Run("no cgroup limit means NOT CHECKED, not passed", func(t *testing.T) {
		reset(t)
		checked, err := CheckMemoryBudget(32 * MiB)
		assert.NoError(t, err, "a developer running outside a container must not be blocked")
		assert.False(t, checked,
			"reporting checked=true here would make 'no limit found' indistinguishable from 'the "+
				"limit is fine', which is the failure this whole feature is about")
	})

	t.Run("cgroup v2 unlimited is also NOT CHECKED", func(t *testing.T) {
		reset(t)
		cgroupV2MemoryMaxPath = write(t, "memory.max", "max\n")
		checked, err := CheckMemoryBudget(32 * MiB)
		assert.NoError(t, err)
		assert.False(t, checked, `"max" means unlimited, not zero`)
	})

	t.Run("a sufficient v2 limit passes", func(t *testing.T) {
		reset(t)
		cgroupV2MemoryMaxPath = write(t, "memory.max", strconv.FormatInt(512*MiB, 10))
		checked, err := CheckMemoryBudget(4 * MiB)
		assert.True(t, checked)
		assert.NoError(t, err, "the chart's own default pairing must start")
	})

	t.Run("an insufficient v2 limit refuses, and says both numbers", func(t *testing.T) {
		reset(t)
		cgroupV2MemoryMaxPath = write(t, "memory.max", strconv.FormatInt(512*MiB, 10))
		checked, err := CheckMemoryBudget(32 * MiB)
		assert.True(t, checked)
		require.Error(t, err, "32 MiB needs 1288 MiB and the limit is 512 MiB")
		assert.Contains(t, err.Error(), "1288", "the message must say what is needed")
		assert.Contains(t, err.Error(), "512", "and what is available")
		assert.Contains(t, err.Error(), "maxBodyMiB", "and how to fix it through the chart")
	})

	t.Run("cgroup v1 is read when v2 is absent", func(t *testing.T) {
		reset(t)
		cgroupV1MemoryLimitPath = write(t, "memory.limit_in_bytes", strconv.FormatInt(512*MiB, 10))
		checked, err := CheckMemoryBudget(32 * MiB)
		assert.True(t, checked, "the v1 layout must be read when v2 is not present")
		assert.Error(t, err)
	})

	t.Run("cgroup v1 unlimited sentinel is NOT CHECKED", func(t *testing.T) {
		reset(t)
		cgroupV1MemoryLimitPath = write(t, "memory.limit_in_bytes", "9223372036854771712")
		checked, err := CheckMemoryBudget(32 * MiB)
		assert.NoError(t, err)
		assert.False(t, checked,
			"v1 reports unlimited as a near-max int64; treating it as a real limit would refuse every "+
				"start on a host without a memory cgroup")
	})
}
