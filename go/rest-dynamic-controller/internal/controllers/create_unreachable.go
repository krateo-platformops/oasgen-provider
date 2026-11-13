package restResources

import (
	"strings"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/unstructured-runtime/pkg/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// isActionOnlyResource reports whether create is this resource's only write verb -- the shape where
// create is an ACTION rather than a creation.
//
// THIS PREDICATE EXISTS TWICE, DELIBERATELY, AND IS PINNED BY A SHARED TABLE.
//
// oasgen-provider decides the same shape at generation time (oas2jsonschema.isActionOnly, issues 625
// and 629). The two cannot share code: separate Go modules, and both live under internal/. So they
// share `testdata/action-only-shape.json` at the repo root instead, and BOTH modules' tests assert
// their own predicate against every row in it.
//
// That is the enforceable version of "one predicate". A "keep these in sync" comment is not: it fails
// silently, and the entire argument for one predicate is that a generation-time suppression and a
// runtime diagnostic must not disagree about the shape they describe.
//
// WHY EACH ESCAPE IS HERE. unstructured-runtime queues a Create only on !observation.ResourceExists
// (pkg/controller/worker.go) -- nothing about up-to-dateness can produce one. So for a resource with
// no other write verb the create is reachable exactly one way: a findby that does not match. Each
// clause below names a way the create is reachable WITHOUT that, and a kind matching any of them must
// not be reported.
//
// `delete` is deliberately NOT an escape: it only runs on deletion and can never realise spec drift,
// so a kind with findby+create+delete is still action-only.
func isActionOnlyResource(r *getter.Resource) bool {
	if r == nil {
		return false
	}
	has := func(action string) bool {
		for _, v := range r.VerbsDescription {
			if strings.EqualFold(v.Action, action) {
				return true
			}
		}
		return false
	}
	if !has("create") {
		return false // nothing to be unreachable
	}
	if has("get") || has("update") || has("dispatch") {
		return false
	}
	// Delegation makes the create reachable without a create verb at all. createApiRef is handled
	// before any create call is built, and observeApiRef means the findby never executes.
	if r.CreateApiRef != nil || r.ObserveApiRef != nil {
		return false
	}
	return true
}

// createIsUnreachable reports whether this resource's only effect can never fire again.
//
// THE THIRD CLAUSE IS THE WHOLE DIFFICULTY, and leaving it out would reproduce the defect issue 625
// is about.
//
// For an action-only kind the create fires when findby reports the resource absent. **After the
// create succeeds, findby matches -- that is the NORMAL TERMINAL STATE.** A node that has been
// provisioned is still in /v1/nodes, so its findby matches forever, correctly. Reporting on a matched
// findby alone would therefore warn about every successfully provisioned resource: a confident false
// positive on the healthy end state of exactly the kind this is meant to help.
//
// What issue 629 measured is narrower -- "node still at `enroll`, **0 creates**". The defect is a
// findby that matched from the start, so the create was never queued and never will be.
//
// unstructured-runtime records a successful create as an annotation on the managed resource
// (krateo.io/external-create-succeeded), so "has a create ever succeeded for this CR" is answerable
// without inventing state of our own.
func createIsUnreachable(r *getter.Resource, mg *unstructured.Unstructured) bool {
	if !isActionOnlyResource(r) {
		return false
	}
	// A zero time means no create has ever succeeded for this resource.
	return meta.GetExternalCreateSucceeded(mg).IsZero()
}
