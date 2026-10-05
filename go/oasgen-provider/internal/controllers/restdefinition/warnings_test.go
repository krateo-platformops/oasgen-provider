package restdefinition

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func errs(n int) []error {
	out := make([]error, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Errorf("warning number %d", i))
	}
	return out
}

// The reachability fix is only worth anything if the event message is readable, so these pin the two
// ways a summary normally goes wrong: unbounded length, and truncation that does not admit itself.
func TestSummariseWarningsNamesTheFirstFewAndCountsTheRest(t *testing.T) {
	got := summariseWarnings(errs(7))

	assert.Contains(t, got, "warning number 0")
	assert.Contains(t, got, "warning number 2")
	assert.NotContains(t, got, "warning number 3", "only the first few are named in full")
	assert.Contains(t, got, "+4 more",
		"silent truncation would have someone fix three problems and conclude they were done")
	assert.Contains(t, got, "see the controller log", "and it must say where the rest are")
}

func TestSummariseWarningsDoesNotTruncateWhenItNeedNot(t *testing.T) {
	got := summariseWarnings(errs(maxWarningsInEvent))

	assert.Contains(t, got, fmt.Sprintf("warning number %d", maxWarningsInEvent-1))
	assert.NotContains(t, got, "more", "nothing was omitted, so the message must not claim it was")
}

func TestSummariseWarningsHandlesEmptyAndNil(t *testing.T) {
	assert.Equal(t, "", summariseWarnings(nil))
	assert.Equal(t, "", summariseWarnings([]error{}))

	// A nil entry must not render as "%!s(<nil>)" in an operator-facing message.
	got := summariseWarnings([]error{nil, errors.New("real one")})
	assert.NotContains(t, got, "nil")
	assert.Contains(t, got, "real one")
}

// The whole point of #151: the message has to tell an operator what the warning MEANS for them, not
// merely that one occurred. A resource whose identifier never resolves reports Ready and then never
// converges, so "the CRD was still generated, but a field named here may never resolve" is the part
// that sends someone to look.
func TestTheEventMessageExplainsTheConsequenceNotJustTheCount(t *testing.T) {
	// The function the controller actually calls -- not a copy of its format string, which would keep
	// passing after someone changed the real message.
	msg := warningEventMessage(errs(2))

	assert.Contains(t, msg, "2 schema warning(s)")
	assert.Contains(t, msg, "may never resolve at runtime",
		"the consequence is what sends an operator to look; a bare count does not")
	assert.Contains(t, msg, "the CRD was still generated",
		"and it must say the resource is not broken, or this reads as a failure it is not")
	assert.True(t, strings.Contains(msg, "warning number 0"), "and it names them")
}
