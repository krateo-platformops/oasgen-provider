package restResources

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/unstructured-runtime/pkg/meta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func crWithCreateMarkers(pending, succeeded, failed time.Time) *unstructured.Unstructured {
	u := crWithKind("Thing")
	if !pending.IsZero() {
		meta.SetExternalCreatePending(u, pending)
	}
	if !succeeded.IsZero() {
		meta.SetExternalCreateSucceeded(u, succeeded)
	}
	if !failed.IsZero() {
		meta.SetExternalCreateFailed(u, failed)
	}
	return u
}

// PRESENCE IS NOT THE TEST; ORDER IS. All three markers are second-resolution timestamps and all three can
// coexist on one object, so a classifier keyed on "is the failed annotation set" answers the wrong
// question. Every row below has at least two markers present for exactly that reason.
func TestClassifyCreateOutcomeReadsORDERNotPresence(t *testing.T) {
	// Spaced by whole seconds: the annotations are written at second resolution, so sub-second deltas
	// would be a property of the test rather than of anything the controller can observe.
	t0 := time.Now().Add(-10 * time.Minute)
	t1 := t0.Add(1 * time.Second)
	t2 := t0.Add(2 * time.Second)

	cases := []struct {
		name    string
		cr      *unstructured.Unstructured
		want    createOutcome
		because string
	}{
		{
			"no markers at all", crWithCreateMarkers(time.Time{}, time.Time{}, time.Time{}),
			createNeverAttempted,
			"no pending marker was ever written, so no external call was made under this CR",
		},
		{
			"pending then succeeded", crWithCreateMarkers(t0, t1, time.Time{}),
			createSucceeded,
			"a create landed; something plausibly exists at the vendor",
		},
		{
			"pending then failed", crWithCreateMarkers(t0, time.Time{}, t1),
			createFailed,
			"the create did not land, so it left nothing behind",
		},
		{
			"succeeded THEN failed -- a later retry failed", crWithCreateMarkers(t0, t1, t2),
			createFailed,
			"both are set and the FAILED one is newer, so the latest attempt did not land",
		},
		{
			"failed THEN succeeded -- a retry landed", crWithCreateMarkers(t0, t2, t1),
			createSucceeded,
			"both are set and the SUCCEEDED one is newer: a presence test would have called this failed",
		},
		{
			"pending is newest -- in flight", crWithCreateMarkers(t2, t1, time.Time{}),
			createIncomplete,
			"pending after the latest outcome is an attempt whose result was never recorded",
		},
		{
			"pending newest with BOTH outcomes behind it", crWithCreateMarkers(t2, t0, t1),
			createIncomplete,
			"a fresh attempt after an earlier failure is still in flight",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyCreateOutcome(tc.cr), tc.because)
		})
	}
}

// The decision that says whether anything is worth warning about. The two `succeeded` rows are the pairing
// that matters: a synthesised 404 arrives as an error and must warn; the vendor's own 404 arrives as a nil
// error and must not.
func TestShouldReportPossibleOrphan(t *testing.T) {
	someErr := errors.New("findby listed successfully but nothing matched")

	cases := []struct {
		name    string
		outcome createOutcome
		err     error
		want    bool
		because string
	}{
		{"succeeded, could not re-derive", createSucceeded, someErr, true,
			"a create landed and the object could not be addressed: it may survive the deletion"},
		{"succeeded, the VENDOR said not-found", createSucceeded, nil, false,
			"proven absent. Warning here would cry wolf and train operators to ignore the real case"},
		{"never attempted", createNeverAttempted, someErr, false,
			"no create was made under this CR, so nothing was left behind to orphan"},
		{"never attempted, no error", createNeverAttempted, nil, false, ""},
		{"failed", createFailed, someErr, false, "the last create did not land"},
		{"failed, no error", createFailed, nil, false, ""},
		{"incomplete", createIncomplete, someErr, false, "unreachable from the delete path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, shouldReportPossibleOrphan(tc.outcome, tc.err), tc.because)
		})
	}
}

// The Event message is the only durable artifact -- the CR is deleted in the same pass -- so what it names
// has to be right. Identifiers come from the CR and NEVER from the vendor's response.
func TestIdentifierSummaryNamesEveryIdentifierIncludingUnsetOnes(t *testing.T) {
	info := &getter.Info{}
	info.Resource.Identifiers = []string{"username", "id", "realm"}

	cr := crWithKind("KeycloakUser")
	require.NoError(t, unstructured.SetNestedField(cr.Object, "jdoe", "spec", "username"))
	require.NoError(t, unstructured.SetNestedField(cr.Object, "abc-123", "status", "id"))
	// `realm` is deliberately set NOWHERE.

	got := identifierSummary(info, cr)

	assert.Contains(t, got, "username=jdoe", "a spec-supplied identifier is what makes recovery possible")
	assert.Contains(t, got, "id=abc-123", "a server-assigned identifier lands in status")
	assert.Contains(t, got, "realm=<unset>",
		"an identifier with no value must still be NAMED: which field was missing is itself the diagnosis, "+
			"and silently dropping it leaves an alert that cannot say what to look for")

	// Stable order, so the same state produces the same message and an alert can be deduplicated.
	assert.Equal(t, "id=abc-123, realm=<unset>, username=jdoe", got)
}

// STATUS WINS OVER SPEC, asserted rather than assumed: for a server-assigned identifier the status value is
// the authoritative one, and a stale spec copy must not shadow it.
func TestIdentifierSummaryPrefersStatusOverSpec(t *testing.T) {
	info := &getter.Info{}
	info.Resource.Identifiers = []string{"id"}

	cr := crWithKind("Thing")
	require.NoError(t, unstructured.SetNestedField(cr.Object, "stale-from-spec", "spec", "id"))
	require.NoError(t, unstructured.SetNestedField(cr.Object, "authoritative", "status", "id"))

	assert.Equal(t, "id=authoritative", identifierSummary(info, cr))
}

func TestIdentifierSummaryWithNoIdentifiersDeclared(t *testing.T) {
	assert.Equal(t, "no identifiers declared", identifierSummary(&getter.Info{}, crWithKind("Thing")))
	assert.Equal(t, "no identifiers declared", identifierSummary(nil, crWithKind("Thing")))
}

// Every outcome must render as a word, because the log field that explains WHY the finalizer was released
// is otherwise an integer nobody can read without the source.
func TestCreateOutcomeStringIsExhaustive(t *testing.T) {
	for _, o := range []createOutcome{createNeverAttempted, createSucceeded, createFailed, createIncomplete} {
		s := o.String()
		assert.NotEmpty(t, s)
		assert.NotEqual(t, "unknown", s, "outcome %d renders as \"unknown\"; String() is missing a case", o)
	}
	assert.Equal(t, "unknown", createOutcome(99).String(), "the fallback must still be reachable")
}

// THE DEPENDENCY-ORDERING ASSERTION, which is the only thing standing behind createIncomplete being
// documented as unreachable.
//
// classifyCreateOutcome names the case and says the runtime resolves it before Delete is dispatched. That
// is a claim about unstructured-runtime's worker, not about this repository, so it is exactly the sort of
// premise that stops being true on a bump with nothing to notice. This reads the module in the build cache
// and asserts the ordering, so a reordering upstream fails here instead of silently making a documented
// unreachable branch reachable.
func TestTheRuntimeResolvesAnIncompleteCreateBeforeDispatchingDelete(t *testing.T) {
	dir := runtimeModuleDir(t)
	b, err := os.ReadFile(dir + "/pkg/controller/worker.go")
	require.NoError(t, err, "could not read the runtime's worker.go; if its layout changed, this guard needs updating")
	src := string(b)

	recovery := strings.Index(src, "meta.ExternalCreateIncomplete(el)")
	require.NotEqual(t, -1, recovery, "the incomplete-create recovery block is gone from worker.go")

	// The dispatch is the point after which our Delete can run. WasDeleted gates the finalizer ADD above
	// it, so the marker used here is the dispatch's own guard.
	dispatch := strings.Index(src, "if !meta.WasDeleted(el)")
	require.NotEqual(t, -1, dispatch, "the dispatch guard is gone from worker.go; this guard needs updating")

	assert.Less(t, recovery, dispatch,
		"the runtime now dispatches BEFORE resolving an incomplete create, so classifyCreateOutcome's "+
			"createIncomplete case is reachable from the delete path and must be given a real behaviour "+
			"instead of being documented as unreachable")
}
