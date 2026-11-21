package restResources

import (
	"regexp"
	"strings"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

// pathParamPattern matches an OAS path template parameter: {id}, {issue_number}, {pushId}.
var pathParamPattern = regexp.MustCompile(`\{([^{}/]+)\}`)

// statusSourcePrefix is the convention for a mapping that reads from status. Measured across this
// repo's examples and fixtures: inCustomResource values carry an explicit `spec.` or `status.`
// prefix (`spec.name`, `spec.projectId`, `spec.node_id`, `status.id`), never a bare field name.
const statusSourcePrefix = "status."

// existenceIsPermanentlyUnanswerable reports whether this resource's existence can NEVER be read --
// not whether it cannot be read on THIS pass.
//
// THE DISTINCTION IS THE WHOLE POINT, and getting it wrong is worse than not having it. The runtime
// treats a permanently-unanswerable error by STAYING STOPPED and reporting; a transient one waits
// and retries. So marking a merely-not-yet-addressable resource as permanent means it stops forever
// on a condition that would have cleared by itself. unstructured-runtime's precedence rule makes
// that asymmetry explicit: an error marked BOTH ExistenceUndetermined and unanswerable is treated as
// TRANSIENT, so the two marks must never both be returned.
//
// WHY THIS PREDICATE AND NOT "the findby verb is missing". The existence check picks its read verb by
// ADDRESSABILITY: it dry-runs the GET and only falls back to findby when that does not validate. A
// get path parameter sourced from `status.<id>` is empty at reconcile 1, so the GET never validates,
// the action becomes findby, and with none declared the check can never answer -- because the only
// thing that would fill `status.<id>` is the create response that was lost. A parameter sourced from
// `spec.*` validates at reconcile 1 and the GET is used, so findby is never consulted: measured on
// one consumer's chart, ten findby-less kinds split SIX exposed and FOUR not.
//
// IT IS THE SAME PREDICATE oasgen-provider APPLIES TO THE DOCUMENT at generation time, where it
// WARNS so somebody can add a findby while the document is still being written. The two cannot share
// code -- separate Go modules, no dependency either way, both under internal/ -- so they share
// testdata/unanswerable-existence-cases.json, and each module's test asserts its own predicate
// against every row. If they drift, the other module's suite goes red and names the row.
//
// DELIBERATELY NARROW: only an EXPLICIT `status.*` mapping counts. An unmapped parameter may still
// resolve by name from the generated spec, and deciding that here would mean re-deriving the
// rendered schema -- an approximation of the real resolver, which is how a vacuous gate got written
// in this repo. A false negative leaves today's behaviour; a false positive stops a resource
// forever. The asymmetry decides the direction.
func existenceIsPermanentlyUnanswerable(res getter.Resource) (param, source string, unanswerable bool) {
	var get *getter.VerbsDescription
	for i := range res.VerbsDescription {
		switch strings.ToLower(res.VerbsDescription[i].Action) {
		case "findby":
			// A findby is the fallback the runtime reaches for; with one declared, nothing here is
			// permanent.
			return "", "", false
		case "get":
			get = &res.VerbsDescription[i]
		}
	}
	if get == nil {
		// No get verb is a DIFFERENT defect, reported elsewhere. Saying "existence unanswerable"
		// about it would be true for the wrong reason, and would stop the resource on that reason.
		return "", "", false
	}

	for _, m := range pathParamPattern.FindAllStringSubmatch(get.Path, -1) {
		name := m[1]
		if src, ok := statusMappedGetParam(get, name); ok {
			return name, src, true
		}
	}
	return "", "", false
}

// statusMappedGetParam reports the `status.*` source a get path parameter is mapped from, if any.
//
// BOTH MAPPING FIELDS ARE READ. requestFieldMapping is deprecated and fieldMapping is current, and a
// definition may carry either -- a check reading only the current one would miss every definition
// written before the rename, which is most of those in the wild.
func statusMappedGetParam(get *getter.VerbsDescription, param string) (string, bool) {
	for _, m := range get.FieldMapping {
		if m.InPath == param && strings.HasPrefix(m.InCustomResource, statusSourcePrefix) {
			return m.InCustomResource, true
		}
	}
	for _, m := range get.RequestFieldMapping {
		if m.InPath == param && strings.HasPrefix(m.InCustomResource, statusSourcePrefix) {
			return m.InCustomResource, true
		}
	}
	return "", false
}
