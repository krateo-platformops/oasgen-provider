package restResources

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	unstructuredtools "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured"

	customcondition "github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
)

// reportPersistentCreateRefusal sets the announce-only condition when a refused create has gone on long
// enough, and is called from BOTH create paths.
//
// TWO CALL SITES, DELIBERATELY SHARED. createRefusedWithoutCreating is consulted on the dispatch path and
// on the ordinary create path, and those two paths have already disagreed about the same status code once
// -- a 401 was classified on one and fell through on the other, decided only by which path sent the
// request. One reporter means they cannot diverge again on this.
//
// IT NEVER CHANGES THE RETURN. The caller goes on to return controller.CreateNotCommitted exactly as
// before; this only writes a condition. See customcondition.CreateRefusedPersistently for why
// suppressing the retry here would reintroduce issue 818's undeletable CR.
func (h *handler) reportPersistentCreateRefusal(mg *unstructured.Unstructured, code int, detail string, log logging.Logger) {
	age, over := createRefusedLongerThan(mg, h.createRefusalReportAfter, time.Now())
	if !over {
		return
	}
	if cerr := unstructuredtools.SetConditions(mg, customcondition.CreateRefusedPersistently(age, code, detail)); cerr != nil {
		// A failure to annotate must not change the create outcome: the retry classification above is
		// the load-bearing part and is already decided.
		log.Debug("Could not record the persistent-create-refusal condition", "error", cerr)
	}
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
