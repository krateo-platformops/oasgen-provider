package server

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// THE MEMORY BUDGET, AND WHY IT IS DUPLICATED IN THE CHART.
//
// The /render body cap and this process's memory limit are one decision, not two (#628). The chart
// derives the limit from the cap so the broken pair cannot be expressed through its declared values;
// this file is the other half, closing the route no chart schema can: the generic `env` passthrough,
// and hand-written manifests that never go through the chart at all.
//
// The constants below are mirrored in helm/controller-render-service/templates/deployment.yaml, and
// a test asserts the two agree. They cannot share a definition -- Helm cannot import Go -- so the
// test is the enforcement. If they drift, the chart could derive a limit this check then rejects,
// and the service would refuse to start from its own chart's defaults.
//
// BASELINE PLUS A MARGINAL RATE, NOT A FLAT MULTIPLIER. Measured live heap at peak:
//
//	input      allocated   live heap    ratio
//	  35 KiB     9.6 MiB     8.3 MiB     243x
//	 349 KiB      37 MiB      15 MiB      44x
//	 1.3 MiB     124 MiB      46 MiB      35x
//	 3.1 MiB     300 MiB      90 MiB      29x
//
// The ratio is not constant, so `cap x 29` understates the fixed cost at small caps and would
// demand less than the Go runtime needs to start. 36.25 is 29x plus 25% headroom.
const (
	// budgetBaselineMiB is the fixed cost: runtime, server, and the parse machinery that does not
	// scale with the document.
	budgetBaselineMiB = 128
	// budgetMarginalNumerator / budgetMarginalDenominator is 36.25 -- the measured 29x live-heap
	// ratio at the large end plus 25% headroom. Expressed as a fraction because the chart computes
	// the same value with Helm's integer arithmetic.
	budgetMarginalNumerator   = 145
	budgetMarginalDenominator = 4
	// budgetFloorMiB is what the chart shipped before the knob existed. Keeping it as a floor means
	// the default cap renders a limit byte-identical to the previous release.
	budgetFloorMiB = 512
)

// RequiredMemoryMiB reports the memory limit a given body cap needs, matching the chart exactly.
func RequiredMemoryMiB(maxBodyBytes int64) int64 {
	capMiB := maxBodyBytes / (1 << 20)
	derived := int64(budgetBaselineMiB) + (capMiB*budgetMarginalNumerator)/budgetMarginalDenominator
	if derived < budgetFloorMiB {
		return budgetFloorMiB
	}
	return derived
}

// cgroupMemoryLimitBytes reports this process's container memory limit, and whether one was found.
//
// NOT FINDING ONE IS NOT A FAILURE. Running outside a container, on a host without cgroups, or under
// an unlimited cgroup are all ordinary -- a developer running `go run` must not be blocked by a
// check meant for a deployed pod. The caller treats "no limit found" as "cannot check", never as
// "limit is zero", because those differ by everything.
// Overridable so the two cgroup layouts and the no-limit case can be exercised without a container.
// Production never changes them.
var (
	cgroupV2MemoryMaxPath   = "/sys/fs/cgroup/memory.max"
	cgroupV1MemoryLimitPath = "/sys/fs/cgroup/memory/memory.limit_in_bytes"
)

func cgroupMemoryLimitBytes() (int64, bool) {
	// cgroup v2 first: a single file, "max" when unlimited.
	if b, err := os.ReadFile(cgroupV2MemoryMaxPath); err == nil {
		s := strings.TrimSpace(string(b))
		if s == "max" {
			return 0, false
		}
		if v, perr := strconv.ParseInt(s, 10, 64); perr == nil && v > 0 {
			return v, true
		}
	}
	// cgroup v1: an enormous sentinel rather than a word when unlimited.
	if b, err := os.ReadFile(cgroupV1MemoryLimitPath); err == nil {
		if v, perr := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); perr == nil && v > 0 {
			// v1 reports unlimited as a value near max int64; anything at or above a terabyte is
			// not a real container limit for this service.
			if v < (1 << 40) {
				return v, true
			}
		}
	}
	return 0, false
}

// CheckMemoryBudget reports an error when the configured body cap cannot be served within this
// container's memory limit.
//
// It returns nil when no limit can be read, deliberately: see cgroupMemoryLimitBytes. A check that
// cannot run must not be reported as a check that passed, so the caller logs which case it was.
func CheckMemoryBudget(maxBodyBytes int64) (checked bool, err error) {
	limit, ok := cgroupMemoryLimitBytes()
	if !ok {
		return false, nil
	}
	needMiB := RequiredMemoryMiB(maxBodyBytes)
	limitMiB := limit / (1 << 20)
	if needMiB <= limitMiB {
		return true, nil
	}
	return true, fmt.Errorf(
		"max-body-bytes is %d (%d MiB), which needs about %d MiB of live heap for a single request, "+
			"but this container's memory limit is %d MiB. Parsing a document holds roughly 29 times "+
			"its size live at peak, so this service would accept bodies it cannot render and be "+
			"OOM-killed under load. Either lower the cap or raise the memory limit -- in the chart, "+
			"set maxBodyMiB and the limit is derived for you",
		maxBodyBytes, maxBodyBytes/(1<<20), needMiB, limitMiB)
}
