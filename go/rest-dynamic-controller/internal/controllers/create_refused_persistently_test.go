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

func TestTheDeadlineIsMeasuredFromReadysTransition(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	t.Run("over the deadline reports, with the measured age", func(t *testing.T) {
		mg := crWithReady(t, metav1.ConditionFalse, now.Add(-90*time.Minute))
		age, over := createRefusedLongerThan(mg, time.Hour, now)
		require.True(t, over, "90 minutes not-Ready is past a 1h deadline and must report")
		require.Equal(t, 90*time.Minute, age, "the age returned is what the message quotes, so it must be the real one")
	})

	t.Run("under the deadline stays silent", func(t *testing.T) {
		mg := crWithReady(t, metav1.ConditionFalse, now.Add(-59*time.Minute))
		_, over := createRefusedLongerThan(mg, time.Hour, now)
		require.False(t, over, "59 minutes must not report, or a transient refusal is announced as persistent")
	})

	t.Run("exactly at the deadline reports", func(t *testing.T) {
		mg := crWithReady(t, metav1.ConditionFalse, now.Add(-time.Hour))
		_, over := createRefusedLongerThan(mg, time.Hour, now)
		require.True(t, over, "the boundary is inclusive; a >= that became > would delay by a whole resync")
	})

	t.Run("a Ready resource never reports however old", func(t *testing.T) {
		mg := crWithReady(t, metav1.ConditionTrue, now.Add(-100*time.Hour))
		_, over := createRefusedLongerThan(mg, time.Hour, now)
		require.False(t, over, "a Ready=True resource is working; reporting a refusal on it would be a false alarm")
	})

	t.Run("an unparsable stamp is missing data, not year 0001", func(t *testing.T) {
		// BUILT AS A RAW MAP, not via SetConditions with a zero time: that path marshals to
		// `lastTransitionTime: null` and PANICS the runtime's GetConditions, which does an unchecked
		// m["lastTransitionTime"].(string). Filed separately; this arm is about our own behaviour on a
		// stamp that reaches us unusable, which is the shape a hand-edited or older CR takes.
		mg := &unstructured.Unstructured{Object: map[string]interface{}{
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{
						"type":               runtimecondition.TypeReady,
						"status":             string(metav1.ConditionFalse),
						"reason":             "Creating",
						"message":            "creating",
						"lastTransitionTime": "not-a-timestamp",
					},
				},
			},
		}}
		_, over := createRefusedLongerThan(mg, time.Hour, now)
		require.False(t, over,
			"an unusable lastTransitionTime produced a report. A stamp that cannot be read is missing "+
				"data, and treating it as an instant would report every such resource as overdue")
	})

	t.Run("a SERIALIZED zero stamp reaches the guard and must not report", func(t *testing.T) {
		// THE TWO SHAPES WERE NOT INTERCHANGEABLE AT unstructured-runtime v1.30.0, and the version is
		// the part this comment used to omit. Measured at v1.30.0:
		//
		//	"not-a-timestamp"        time.Parse errors -> GetConditions returned NIL for the WHOLE
		//	                         list, so the condition never reached createRefusedLongerThan
		//	"0001-01-01T00:00:00Z"   parses cleanly AND IsZero() is true -> it DID reach the guard
		//
		// CORRECTED AT v1.32.0, and measured by removing the guard rather than inferred from the
		// release note: BOTH arms now fail without it. #115 made GetConditions parse entry by entry,
		// so a stamp that is absent, null, not a string or not RFC3339 reads as the ZERO TIME on a
		// KEPT condition instead of discarding the whole list. The guard therefore protects two shapes
		// here, not one, and the distinction this comment drew has dissolved.
		//
		// Both arms are kept: they are now two routes to the same guard, and if a later version
		// separates them again the pair still covers both.
		mg := &unstructured.Unstructured{Object: map[string]interface{}{
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{
						"type":               runtimecondition.TypeReady,
						"status":             string(metav1.ConditionFalse),
						"reason":             "Creating",
						"message":            "creating",
						"lastTransitionTime": "0001-01-01T00:00:00Z",
					},
				},
			},
		}}
		// SETUP CONTROL: it really did reach us, rather than being dropped like the unparsable one.
		require.Len(t, unstructuredtools.GetConditions(mg), 1,
			"the serialized zero was dropped before the guard, so this arm tests nothing")

		_, over := createRefusedLongerThan(mg, time.Hour, now)
		require.False(t, over,
			"a zero lastTransitionTime was treated as a real instant, so every resource carrying one "+
				"would be reported as two thousand years overdue the moment it is observed")
	})

	t.Run("no Ready condition at all", func(t *testing.T) {
		mg := &unstructured.Unstructured{Object: map[string]interface{}{}}
		_, over := createRefusedLongerThan(mg, time.Hour, now)
		require.False(t, over, "nothing has been observed yet, so there is nothing to report")
	})

	t.Run("a zero deadline disables the report", func(t *testing.T) {
		mg := crWithReady(t, metav1.ConditionFalse, now.Add(-100*time.Hour))
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
func TestSettingTheConditionDoesNotMoveItsOwnAnchor(t *testing.T) {
	now := time.Now()
	original := now.Add(-2 * time.Hour)
	mg := crWithReady(t, metav1.ConditionFalse, original)

	age, over := createRefusedLongerThan(mg, time.Hour, now)
	require.True(t, over, "setup: two hours must be over a one-hour deadline")
	require.NoError(t, unstructuredtools.SetConditions(mg, customcondition.CreateRefusedPersistently(age, 400, "bad request")))

	conds := unstructuredtools.GetConditions(mg)
	require.Len(t, conds, 1, "the condition must REPLACE the Ready condition, not add a second one")
	require.Equal(t, customcondition.ReasonCreateRefusedPersistently, conds[0].Reason,
		"the reason did not change, so the report is invisible to anyone reading the reason")
	require.WithinDuration(t, original, conds[0].LastTransitionTime.Time, time.Second,
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
	reports := strings.Count(src, "Conditions: h.persistentCreateRefusalConditions(mg,")
	require.Equal(t, classifications, reports,
		"a create path classifies a refusal and does NOT return the condition in CreateResult. "+
			"Conditions written onto `mg` are discarded: the runtime re-fetches so that it owns the "+
			"single status write, so only a RETURNED condition reaches the cluster")

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
	mg := crWithReady(t, metav1.ConditionFalse, now.Add(-2*time.Hour))
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
	fresh := crWithReady(t, metav1.ConditionFalse, now.Add(-1*time.Minute))
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
