//go:build unit || integration

package restResources

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	ucontroller "github.com/krateo-platformops/unstructured-runtime/pkg/controller"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

// TestTheSentinelIsAttachedOnlyWhereTheCheckCanNEVERAnswer asserts the DECISION Exists returns, not
// the predicate behind it.
//
// The predicate is pinned against the shared cross-module table in shared_unanswerable_table_test.go.
// This arm answers the different question: does the error that leaves Exists actually CARRY the
// sentinel where the predicate says permanent, and leave it off where it does not? A correct
// predicate behind a mis-wired branch is invisible to every arm that calls the predicate directly,
// and the consequence is not cosmetic -- the runtime STAYS STOPPED on a permanent error and WAITS on
// a transient one.
//
// IT ASSERTS ON THE RETURNED VALUE because reaching the branch through Exists needs a fetchable OAS
// document: Exists refuses first with `no OAS document is declared for action "get"`, measured. An
// end-to-end arm therefore belongs in the integration suite, and this one does not pretend to be it.
func TestTheSentinelIsAttachedOnlyWhereTheCheckCanNEVERAnswer(t *testing.T) {
	base := errors.New(`existence check: this kind declares no findby verb, so existence cannot be read`)

	// The one shape that can never become addressable: the get needs a status-sourced id and no
	// findby is declared, so the only thing that would fill status.pushId is the create response
	// that was lost.
	unanswerable := getter.Resource{
		Kind: "GitPush",
		VerbsDescription: []getter.VerbsDescription{
			{Action: "create", Method: "POST", Path: "/pushes"},
			{Action: "get", Method: "GET", Path: "/pushes/{pushId}",
				FieldMapping: []getter.FieldMappingItem{
					{InPath: "pushId", InCustomResource: "status.pushId"},
				}},
		},
	}

	// SETUP CONTROL: the predicate agrees. Without it, a decision arm that never reaches the branch
	// would pass for the wrong reason, and a later predicate change could make this vacuous while it
	// still reported green.
	if _, _, permanent := existenceIsPermanentlyUnanswerable(unanswerable); !permanent {
		t.Fatalf("setup: the predicate must call this shape permanently unanswerable, or this arm is " +
			"not testing the branch it names")
	}

	got := markIfPermanentlyUnanswerable(unanswerable, base)
	require.True(t, ucontroller.IsExistenceUnanswerable(got),
		"the sentinel was NOT attached, so the runtime will WAIT on a condition no later pass can "+
			"resolve -- the CR strands exactly as it does today. err = %v", got)

	// The original cause must survive, or an operator loses the only sentence naming the verb.
	require.ErrorIs(t, got, base, "the underlying error was replaced rather than wrapped")

	// And the message must name the parameter and its source, in those positions -- a swap would
	// leave both strings present and only a positional assertion sees it.
	require.Contains(t, got.Error(), "needs {pushId}")
	require.Contains(t, got.Error(), `mapped from "status.pushId"`)
	require.NotContains(t, got.Error(), "needs {status.pushId}", "param and source are SWAPPED")

	// THE OTHER DIRECTION, one thing changed: a declared findby makes it answerable, so the same
	// decision must NOT mark it.
	answerable := getter.Resource{
		Kind: "Issue",
		VerbsDescription: []getter.VerbsDescription{
			{Action: "get", Method: "GET", Path: "/issues/{number}",
				FieldMapping: []getter.FieldMappingItem{
					{InPath: "number", InCustomResource: "status.number"},
				}},
			{Action: "findby", Method: "GET", Path: "/issues"},
		},
	}
	if _, _, permanent := existenceIsPermanentlyUnanswerable(answerable); permanent {
		t.Fatalf("setup: a declared findby must make this shape answerable")
	}
	plain := markIfPermanentlyUnanswerable(answerable, base)
	require.False(t, ucontroller.IsExistenceUnanswerable(plain),
		"the sentinel was attached to an ANSWERABLE resource: the runtime would stay stopped and "+
			"report on something a later pass could resolve by itself. err = %v", plain)
	require.Equal(t, base, plain, "an answerable resource's error must pass through UNCHANGED")
}

// TestASentinelAndATransientMarkAreNeverBothReturned pins the precedence hazard directly.
//
// An error carrying both marks is treated as TRANSIENT by the runtime, so the stronger claim is
// silently discarded -- which makes a double mark worse than no mark: it looks like the fix is in
// while the behaviour is unchanged.
func TestASentinelAndATransientMarkAreNeverBothReturned(t *testing.T) {
	unanswerable := getter.Resource{
		Kind: "GitPush",
		VerbsDescription: []getter.VerbsDescription{
			{Action: "get", Method: "GET", Path: "/pushes/{pushId}",
				FieldMapping: []getter.FieldMappingItem{
					{InPath: "pushId", InCustomResource: "status.pushId"},
				}},
		},
	}

	got := markIfPermanentlyUnanswerable(unanswerable, errors.New("existence cannot be read"))

	require.True(t, ucontroller.IsExistenceUnanswerable(got),
		"setup: this path must carry the unanswerable mark, or the check below is vacuous")
	require.False(t, ucontroller.IsExistenceUndetermined(got),
		"the error carries BOTH ExistenceUnanswerable and ExistenceUndetermined. The runtime's "+
			"precedence rule treats that as TRANSIENT, so the resource WAITS instead of reporting -- "+
			"the fix would look present and change nothing. err = %v", got)

	// AND THE CONVERSE, which is the realistic mistake: a transient error that acquires the
	// permanent mark because the predicate was consulted on the wrong path.
	transient := ucontroller.ExistenceUndetermined(errors.New("transport failure"))
	stillTransient := markIfPermanentlyUnanswerable(getter.Resource{
		Kind: "Answerable",
		VerbsDescription: []getter.VerbsDescription{
			{Action: "get", Method: "GET", Path: "/things/{id}",
				FieldMapping: []getter.FieldMappingItem{{InPath: "id", InCustomResource: "spec.id"}}},
		},
	}, transient)
	require.True(t, ucontroller.IsExistenceUndetermined(stillTransient),
		"a transient error lost its mark passing through the decision")
	require.False(t, ucontroller.IsExistenceUnanswerable(stillTransient),
		"a TRANSIENT error acquired the PERMANENT mark, so a recoverable resource stops forever")
}
