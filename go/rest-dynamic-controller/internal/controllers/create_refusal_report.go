package restResources

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	customcondition "github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
)

// persistentCreateRefusalConditions RETURNS the announce-only condition when a refused create has gone
// on long enough, for the caller to put in CreateResult.Conditions. It is used by BOTH create paths.
//
// TWO CALL SITES, DELIBERATELY SHARED. createRefusedWithoutCreating is consulted on the dispatch path and
// on the ordinary create path, and those two paths have already disagreed about the same status code once
// -- a 401 was classified on one and fell through on the other, decided only by which path sent the
// request. One reporter means they cannot diverge again on this.
//
// IT NEVER CHANGES THE CREATE OUTCOME. The caller goes on to return controller.CreateNotCommitted exactly as
// before; this only writes a condition. See customcondition.CreateRefusedPersistently for why
// suppressing the retry here would reintroduce issue 818's undeletable CR.
func (h *handler) persistentCreateRefusalConditions(mg *unstructured.Unstructured, code int, detail string) []metav1.Condition {
	age, over := createRefusedLongerThan(mg, h.createRefusalReportAfter, time.Now())
	if !over {
		return nil
	}
	// RETURNED, NOT WRITTEN -- and NOT because an in-handler write never works. It usually does: this
	// controller pairs SetConditions with tools.UpdateStatus at 24 non-test sites and those conditions
	// arrive. THIS function was the one site of 25 that called SetConditions and no UpdateStatus, so
	// its write never left the process.
	//
	// AND PERSISTING WOULD NOT HAVE BEEN ENOUGH HERE, which is why the fix is the returned channel and
	// not an added UpdateStatus. handleCreate RE-FETCHES after CreateWithResult returns
	// (worker.go:1565) and writes its own conditions (1595); Upsert REPLACES a condition of the same
	// TYPE, and both our CreateRefusedPersistently and the runtime's Creating are on Ready. A
	// persisted Ready would have been replaced too. v1.32.1 applies the RETURNED conditions AFTER its
	// own, so only that channel is durable on this path.
	//
	// MEASURED, which is why this is a correction rather than a refactor. This function used to call
	// SetConditions(mg, ...), and a consumer measured the consequence on 0.85.2: a create refused
	// with a 400 sat 198 MINUTES against a 1-HOUR deadline and the reason NEVER CHANGED. The anchor
	// held exactly as designed; the report never reached the apiserver, in any release since 0.84.0.
	//
	// AND BUMPING THE RUNTIME ALONE DID NOT FIX IT. unstructured-runtime v1.32.1 applies a handler's
	// CreateResult.Conditions on the failed-create path -- but this handler returned a CreateResult
	// with no Conditions at all, so the runtime dutifully applied an empty list. I shipped the half
	// that was not mine and described the whole as done; a consumer asking "is there another
	// precondition" is what caught it.
	return []metav1.Condition{customcondition.CreateRefusedPersistently(age, code, detail)}
}

// SetCreateRefusalReportAfter overrides how long a refused create may go on before the condition is set.
// Zero disables the report.
//
// A SETTER RATHER THAN A CONSTRUCTOR PARAMETER, following SetEventRecorder: NewHandler's signature is
// called from main.go and from tests, and threading one more argument through every call site to carry a
// value that has a sensible default is the change most likely to be made wrong somewhere.
func (h *handler) SetCreateRefusalReportAfter(d time.Duration) {
	h.createRefusalReportAfter = d
}
