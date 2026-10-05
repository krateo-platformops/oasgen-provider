package restdefinition

import (
	"fmt"
	"strings"
)

// maxWarningsInEvent bounds how many warnings are named in the aggregated event.
//
// A Kubernetes event message is not an unbounded field, and a document with many unresolvable fields
// would otherwise produce one enormous unreadable line. Three is enough to recognise the problem and
// decide whether to go and read the log, which is where the full list is.
const maxWarningsInEvent = 3

// summariseWarnings renders warnings for an event message: the first few in full, then a count.
//
// "+N more" rather than silent truncation. A truncated list that does not say it was truncated is the
// kind of thing that has someone fix three problems and conclude they are done.
func summariseWarnings(warnings []error) string {
	if len(warnings) == 0 {
		return ""
	}

	shown := warnings
	if len(shown) > maxWarningsInEvent {
		shown = shown[:maxWarningsInEvent]
	}

	parts := make([]string, 0, len(shown))
	for _, w := range shown {
		if w == nil {
			continue
		}
		parts = append(parts, w.Error())
	}

	out := strings.Join(parts, "; ")
	if remaining := len(warnings) - len(shown); remaining > 0 {
		out += fmt.Sprintf(" (+%d more, see the controller log)", remaining)
	}
	return out
}

// warningEventMessage is the operator-facing text for the aggregated schema-warning event.
//
// Built here rather than inline at the Eventf call so a test asserts against the SAME string the
// controller emits. A test that rebuilds the format string itself proves only that the test's copy is
// what the test expects, and keeps passing after someone changes the real one.
//
// It states the CONSEQUENCE, not just the count. The failure these warnings exist to catch is silent --
// the RestDefinition reports Ready, findby never matches, nothing errors -- so a message saying only
// "2 warnings occurred" gives an operator no reason to act. "a field named here may never resolve at
// runtime" is the part that sends someone to look.
func warningEventMessage(warnings []error) string {
	return fmt.Sprintf(
		"%d schema warning(s) while generating this CRD; the CRD was still generated, but a field "+
			"named here may never resolve at runtime: %s",
		len(warnings), summariseWarnings(warnings))
}
