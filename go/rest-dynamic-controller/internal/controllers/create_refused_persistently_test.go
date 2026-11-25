//go:build unit

package restResources

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	unstructuredtools "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured"
	runtimecondition "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"

	customcondition "github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
)

// crWithReady builds a CR carrying one Ready condition with the given status and stamp.
func crWithReady(t *testing.T, status metav1.ConditionStatus, stamp time.Time) *unstructured.Unstructured {
	t.Helper()
	mg := &unstructured.Unstructured{Object: map[string]interface{}{}}
	c := metav1.Condition{
		Type:    runtimecondition.TypeReady,
		Status:  status,
		Reason:  "Creating",
		Message: "creating",
	}
	if !stamp.IsZero() {
		c.LastTransitionTime = metav1.Time{Time: stamp}
	}
	require.NoError(t, unstructuredtools.SetConditions(mg, c))

	// SETUP CONTROL: the condition really landed, with the stamp we meant. Without this, a SetConditions
	// that silently dropped the timestamp would make every "not over the deadline" assertion below pass
	// for the wrong reason -- the same shape as reading an absence as a fact.
	got := unstructuredtools.GetConditions(mg)
	require.Len(t, got, 1, "the setup did not store exactly one condition")
	if !stamp.IsZero() {
		require.WithinDuration(t, stamp, got[0].LastTransitionTime.Time, time.Second,
			"the setup's lastTransitionTime was not preserved, so the age below is not the age under test")
	}
	return mg
}

// crWithRefusedSince builds the input the deadline is now measured from: the annotation, not a
// condition. crWithReady is KEPT, because the arms that used it now assert the opposite property --
// that the Ready condition no longer decides the interval -- which is exactly what issue 929 changed.
func crWithRefusedSince(t *testing.T, stamp time.Time) *unstructured.Unstructured {
	t.Helper()
	mg := &unstructured.Unstructured{Object: map[string]interface{}{}}
	mg.SetAnnotations(map[string]string{annotationCreateRefusedSince: stamp.UTC().Format(time.RFC3339)})
	require.Equal(t, stamp.UTC().Format(time.RFC3339), mg.GetAnnotations()[annotationCreateRefusedSince],
		"the setup did not store the stamp, so the age below is not the age under test")
	return mg
}

func TestTheDeadlineIsMeasuredFromTheRefusalStamp(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	t.Run("over the deadline reports, with the measured age", func(t *testing.T) {
		mg := crWithRefusedSince(t, now.Add(-90*time.Minute))
		age, over := createRefusedLongerThan(mg, time.Hour, now)
		require.True(t, over, "90 minutes not-Ready is past a 1h deadline and must report")
		require.Equal(t, 90*time.Minute, age, "the age returned is what the message quotes, so it must be the real one")
	})

	t.Run("under the deadline stays silent", func(t *testing.T) {
		mg := crWithRefusedSince(t, now.Add(-59*time.Minute))
		_, over := createRefusedLongerThan(mg, time.Hour, now)
		require.False(t, over, "59 minutes must not report, or a transient refusal is announced as persistent")
	})

	t.Run("exactly at the deadline reports", func(t *testing.T) {
		mg := crWithRefusedSince(t, now.Add(-time.Hour))
		_, over := createRefusedLongerThan(mg, time.Hour, now)
		require.True(t, over, "the boundary is inclusive; a >= that became > would delay by a whole resync")
	})

	t.Run("a Ready resource never reports however old", func(t *testing.T) {
		// STRONGER THAN BEFORE: the resource now carries an overdue stamp AND Ready=True, so this
		// arm fails unless the veto is actually consulted. Previously Ready was the anchor, so a
		// Ready=True resource had no interval at all and the arm passed for the wrong reason.
		mg := crWithRefusedSince(t, now.Add(-100*time.Hour))
		require.NoError(t, unstructuredtools.SetConditions(mg, metav1.Condition{
			Type: runtimecondition.TypeReady, Status: metav1.ConditionTrue, Reason: "Available", Message: "ok",
		}))
		_, over := createRefusedLongerThan(mg, time.Hour, now)
		require.False(t, over, "a Ready=True resource is working; reporting a refusal on it would be a false alarm")
	})

	t.Run("an unparsable stamp is missing data, not year 0001", func(t *testing.T) {
		// The annotation is operator-visible, so a hand-edited or truncated value is reachable. An
		// unreadable stamp must mean "not yet", for the reason drift-repeats reads a corrupt count as
		// 0: this is a brake, and a corrupt brake must fail open rather than wedge the reconcile.
		mg := &unstructured.Unstructured{Object: map[string]interface{}{}}
		mg.SetAnnotations(map[string]string{annotationCreateRefusedSince: "not-a-timestamp"})
		_, over := createRefusedLongerThan(mg, time.Hour, now)
		require.False(t, over,
			"an unusable stamp produced a report. A value that cannot be parsed is missing data, and "+
				"treating it as an instant would report every such resource as overdue")
	})

	t.Run("an EMPTY stamp is absent, not the zero instant", func(t *testing.T) {
		// Replaces the serialized-zero-lastTransitionTime arm, which has no analogue now: an RFC3339
		// annotation has no zero value that parses. The reachable equivalent is an empty string, which
		// an operator can produce by clearing the annotation's value rather than removing the key.
		mg := &unstructured.Unstructured{Object: map[string]interface{}{}}
		mg.SetAnnotations(map[string]string{annotationCreateRefusedSince: ""})
		_, over := createRefusedLongerThan(mg, time.Hour, now)
		require.False(t, over,
			"an empty stamp was treated as a real instant, so every resource carrying one would be "+
				"reported as two thousand years overdue the moment it is observed")
	})

	t.Run("no stamp at all", func(t *testing.T) {
		mg := &unstructured.Unstructured{Object: map[string]interface{}{}}
		_, over := createRefusedLongerThan(mg, time.Hour, now)
		require.False(t, over, "nothing has been refused yet, so there is nothing to report")
	})

	// THE REGRESSION ARM FOR ISSUE 929 ITSELF. An old Ready=False with no stamp must NOT report: that
	// is precisely the substitution the old anchor made, and the one a transient observe error
	// exploited by resetting the condition's lastTransitionTime. If this ever passes a report, the
	// fallback has come back.
	t.Run("an ANCIENT Ready=False with no stamp does not report", func(t *testing.T) {
		mg := crWithReady(t, metav1.ConditionFalse, now.Add(-100*time.Hour))
		_, over := createRefusedLongerThan(mg, time.Hour, now)
		require.False(t, over,
			"the Ready condition's stamp was used as a fallback anchor. That is issue 929: the runtime "+
				"rewrites that timestamp on any observe error, so an interval measured from it is not "+
				"the interval the create has been refused for")
	})

	t.Run("a zero deadline disables the report", func(t *testing.T) {
		mg := crWithRefusedSince(t, now.Add(-100*time.Hour))
		_, over := createRefusedLongerThan(mg, 0, now)
		require.False(t, over,
			"zero must DISABLE rather than mean 'immediately': the field defaults to zero in any handler "+
				"built without the setter, and 'report instantly' would then be the accidental default")
	})

	t.Run("nil resource", func(t *testing.T) {
		_, over := createRefusedLongerThan(nil, time.Hour, now)
		require.False(t, over)
	})
}

// TestSettingTheConditionDoesNotMoveItsOwnAnchor is the property that makes this design work rather than
// merely compile.
//
// The condition is Ready=False and the anchor is Ready's lastTransitionTime. The runtime's Upsert keeps
// the existing stamp when the STATUS is unchanged -- so writing this condition must NOT reset the clock
// it just read. If it did, the deadline would re-arm every cycle: the report would fire once and then
// never again, and the age in every later message would be wrong.
// TestSettingTheConditionDoesNotMoveItsOwnAnchor is KEPT, and is now stronger rather than moot.
//
// Under the old anchor this guarded a real hazard: the gated condition was itself on Ready, so writing
// it could have moved the lastTransitionTime it had just read, re-arming the deadline every cycle. The
// annotation cannot be moved by a condition write at all, so the property is now true by construction
// -- which is exactly why it is worth asserting. If anyone reintroduces a condition-derived anchor,
// this is the test that notices, and it also now asserts the condition write leaves the ANNOTATION
// alone, which is a second thing nothing else checks.
func TestSettingTheConditionDoesNotMoveItsOwnAnchor(t *testing.T) {
	now := time.Now()
	original := now.Add(-2 * time.Hour)
	mg := crWithRefusedSince(t, original)
	stampBefore := mg.GetAnnotations()[annotationCreateRefusedSince]

	age, over := createRefusedLongerThan(mg, time.Hour, now)
	require.True(t, over, "setup: two hours must be over a one-hour deadline")
	require.NoError(t, unstructuredtools.SetConditions(mg, customcondition.CreateRefusedPersistently(age, 400, "bad request")))

	conds := unstructuredtools.GetConditions(mg)
	require.Len(t, conds, 1, "the condition must REPLACE the Ready condition, not add a second one")
	require.Equal(t, customcondition.ReasonCreateRefusedPersistently, conds[0].Reason,
		"the reason did not change, so the report is invisible to anyone reading the reason")
	require.Equal(t, stampBefore, mg.GetAnnotations()[annotationCreateRefusedSince],
		"writing the condition MOVED the anchor it reads. The deadline would re-arm every cycle, so the "+
			"report fires once and never again, and every later age is measured from the wrong instant")

	// And the age is still computed the same on the next pass, which is the consequence spelled out.
	age2, over2 := createRefusedLongerThan(mg, time.Hour, now)
	require.True(t, over2, "the second pass stopped reporting, so the condition disarmed itself")
	require.Equal(t, age.Round(time.Second), age2.Round(time.Second), "the age moved between passes")
}

func TestTheMessageNamesWhatWasMeasuredAndNotWhatWasInferred(t *testing.T) {
	c := customcondition.CreateRefusedPersistently(90*time.Minute, 422, "unprocessable")

	require.Contains(t, c.Message, "not been Ready for 1h30m0s",
		"the message must quote the measured quantity: the anchor is Ready's transition, which is not "+
			"the same as the time spent retrying")
	require.NotContains(t, strings.ToLower(c.Message), "retried for",
		"the message claims a retry duration the anchor cannot support -- Upsert compares status, not "+
			"reason, so a resource already not-Ready for another cause carries an earlier anchor")
	require.Contains(t, c.Message, "422", "the status code must be in the message or the operator has to go to the logs")
	require.Contains(t, c.Message, "unprocessable", "the vendor's own detail is the most useful part")
	require.Equal(t, metav1.ConditionFalse, c.Status, "the condition must stay Ready=False or it moves its own anchor")
}

// TestBothCreatePathsReportTheRefusal asserts the CALL SITES, because every other check in this file
// passes on an uncalled function.
//
// createRefusedWithoutCreating is consulted on two paths -- the dispatch path and the ordinary create
// path -- and those two have already disagreed about the same status code once: a 401 was classified on
// one and fell through on the other, decided only by which path sent the request. A reporter wired into
// one of them would be a feature that works for half the installs.
func TestBothCreatePathsReportTheRefusal(t *testing.T) {
	b, err := os.ReadFile("restResources.go")
	require.NoError(t, err)
	src := string(b)

	// PARSE CONTROL: the file read is the one with the create paths in it. Without this a wrong path
	// would make the counts below 0 and the assertions would fail for the wrong reason.
	require.Contains(t, src, "func NewHandler(", "the file read does not look like restResources.go")
	classifications := strings.Count(src, "createRefusedWithoutCreating(code)")
	require.Equal(t, 2, classifications,
		"the number of create paths that classify a refusal changed. Every one of them needs to RETURN "+
			"the condition, or it appears on some installs and not others depending on which path sent "+
			"the request")

	// IT COUNTS THE RETURN, NOT THE CALL, and that distinction is the whole correction. The previous
	// version of this arm counted `h.reportPersistentCreateRefusal(mg,` -- it asserted the helper was
	// CALLED and passed for a year while the condition reached nothing, because the helper wrote onto
	// the object the handler was handed and the runtime discards that by design.
	// ONE STRING NOW PROVES BOTH HALVES, which is why the recorder exists (issue 929).
	// recordAndReportCreateRefusal stamps the first refusal AND returns the condition, so counting it
	// asserts that every classifying path does both. Counting them separately would allow a path that
	// reported without stamping -- measuring an interval that never started -- or stamped without
	// reporting, which is silent forever.
	reports := strings.Count(src, "Conditions: h.recordAndReportCreateRefusal(ctx, mg,")
	require.Equal(t, classifications, reports,
		"a create path classifies a refusal and does NOT go through recordAndReportCreateRefusal. "+
			"Conditions written onto `mg` are discarded -- the runtime re-fetches so that it owns the "+
			"single status write -- and a path that skips the recorder also never stamps, so its "+
			"deadline is measured from an interval that never started")

	// AND THE BARE REPORTER MUST NOT BE CALLED FROM A CREATE PATH. It is still used BY the recorder,
	// so its existence is fine; a create path calling it directly would report without stamping.
	require.NotContains(t, src, "Conditions: h.persistentCreateRefusalConditions(mg,",
		"a create path calls the reporter directly, bypassing the stamp. It would report the first "+
			"refusal it sees and then measure every later one from an interval that never started")

	// AND THE OLD SHAPE MUST NOT COME BACK. An in-handler SetConditions on this path is the defect,
	// not a style choice, so its absence is asserted rather than left to review.
	require.NotContains(t, src, "h.reportPersistentCreateRefusal(mg,",
		"a create path went back to WRITING the condition instead of returning it; that write is "+
			"discarded by the runtime and the report silently stops arriving")
}

// TestTheRefusalConditionIsRETURNEDNotWritten is the behavioural half, and the half that did not
// exist.
//
// MEASURED BY A CONSUMER on 0.85.2: a create refused with a 400 sat 198 MINUTES against a 1-HOUR
// deadline and the reason NEVER CHANGED. The anchor held exactly as designed; the report had never
// reached an apiserver in any release since 0.84.0 -- and every arm in this file passed throughout,
// because they asserted that a helper writes, which was true, and never that the write survives.
func TestTheRefusalConditionIsRETURNEDNotWritten(t *testing.T) {
	now := time.Now()
	h := &handler{createRefusalReportAfter: time.Hour}

	// Over the deadline: the condition must be RETURNED.
	mg := crWithRefusedSince(t, now.Add(-2*time.Hour))
	got := h.persistentCreateRefusalConditions(mg, 400, "bad request")
	require.Len(t, got, 1, "nothing was returned, so the runtime has nothing to apply and the report "+
		"does not arrive -- which is exactly the measured 0.85.2 behaviour")
	require.Equal(t, customcondition.ReasonCreateRefusedPersistently, got[0].Reason)
	require.Equal(t, runtimecondition.TypeReady, got[0].Type,
		"the condition must be Ready: the runtime replaces its own Ready on that path and ADDS other "+
			"types, so a different type would not displace Ready=False/Creating")

	// AND IT MUST NOT ALSO WRITE. The object handed in must be untouched, or the handler is doing
	// both and the stale-resourceVersion hazard the runtime's contract warns about is back.
	before := len(unstructuredtools.GetConditions(mg))
	_ = h.persistentCreateRefusalConditions(mg, 400, "bad request")
	require.Equal(t, before, len(unstructuredtools.GetConditions(mg)),
		"calling the reporter MUTATED the object it was handed. Conditions must be returned, not "+
			"written: a write from inside the call leaves the runtime's copy at a stale resourceVersion")

	// SETUP CONTROL: under the deadline it returns nothing, so the assertion above is about the
	// deadline and not about the function always returning one condition.
	fresh := crWithRefusedSince(t, now.Add(-1*time.Minute))
	require.Empty(t, h.persistentCreateRefusalConditions(fresh, 400, "bad request"),
		"a resource under the deadline returned a condition, so this arm cannot tell the deadline is "+
			"doing anything")
}

// TestTheFlagReachesTheHandler closes the last gap: a defaulted field and a configured one are
// indistinguishable from everything except the flag's effect, so the setter call is the feature.
func TestTheFlagReachesTheHandler(t *testing.T) {
	b, err := os.ReadFile("../../main.go")
	require.NoError(t, err)
	src := string(b)

	// PARSE CONTROL: a known sibling proves the read landed on main.go.
	require.Contains(t, src, "SetEventRecorder", "the file read does not look like main.go")

	require.Contains(t, src, "REST_CONTROLLER_CREATE_REFUSAL_REPORT_AFTER",
		"the deadline is not configurable from the environment, so an operator cannot change it")
	require.Contains(t, src, "SetCreateRefusalReportAfter(*createRefusalReportAfter)",
		"the flag is parsed and never applied: the handler keeps its default and the flag is dead, which "+
			"no build and no other test would report")
}

// TestTheDefaultIsAWallClockDuration pins the unit decision.
func TestTheDefaultIsAWallClockDuration(t *testing.T) {
	require.Equal(t, time.Hour, DefaultCreateRefusalReportAfter)
	require.NotZero(t, DefaultCreateRefusalReportAfter,
		"a zero default would disable the report in every handler built without the setter")
}
