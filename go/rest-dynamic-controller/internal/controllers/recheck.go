package restResources

import (
	"strings"
	"time"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

// RecheckWhenWaiting is the RecheckAfter hint this controller hands the runtime from a path that is waiting.
//
// THE RULE: HINT ONLY WHERE THE OUTCOME WILL CHANGE WITHOUT ANYONE ACTING.
//
// A hint answers "what am I waiting on, and when could it plausibly have happened". If the answer is the
// vendor finishing, a parent's PUT landing or a referent appearing, polling is the right tool. If the
// answer is a person (declare a verb, fix a credential, change the spec), polling is wrong: the condition
// already says what is needed, and a hint would only burn requests until the resync ceiling.
//
// ONE interval per code PATH, never per resource: no CRD field, no configuration. It is a short constant
// with NO backoff. Every recheck that still finds the path waiting returns it again, so the runtime polls
// at this interval until the outcome settles, and the runtime clamps it to its resync period.
//
// WHY 15s. Measured on a consumer fleet, a parent appeared +12s after its child in two of four from-zero
// runs and +67-74s in the other two, against a 180s resync. Without a hint the child waits one to three
// resyncs; at 15s it is seen within one interval of the parent appearing, and a path that never settles
// costs 12 observes per resync, not a hot loop.
//
// Every return site is classified in recheckPolicy below, and recheck_test.go fails when a site is added
// without a decision. Hint a new site only after answering the rule's question for it.
const RecheckWhenWaiting = 15 * time.Second

// recheckPath names a place Observe or CreateWithResult can return from while the answer is "not yet" or
// "nobody can say". It is the unit the rule is applied to.
type recheckPath int

const (
	// --- HINT: the outcome settles with nobody acting ---

	// pathNoCreateVerbAdopt: no create verb, a findby declared. The kind adopts what something else
	// produces (a parent's PUT, an operator), so the findby will match on its own. Also the PUT-only adopt.
	// Gated by definitionCanSettle: with no findby either, nothing can ever adopt.
	pathNoCreateVerbAdopt recheckPath = iota
	// pathReferentMissingDispatch and pathReferentMissingFields: a dependent lookup found no referent.
	//
	// Why this is safe to hint ALWAYS: errLookupReferentMissing is built at exactly one site
	// (lookup_runner.go) and only when the lookup's onMissing is NOT "error". onMissing "requeue" (the
	// default) is the author asserting "the referent is expected, wait for it". An onMissing "error" lookup
	// returns a plain error instead, where the runtime ignores the hint anyway. So a referent whose absence
	// is the steady state cannot reach this path.
	pathReferentMissingDispatch
	pathReferentMissingFields
	// pathRESTActionAbsent: a kind observed through a RESTAction (no get verb) whose notFoundExpr is true.
	// The thing it observes appears on its own. Gated by definitionCanSettle like pathNoCreateVerbAdopt, so
	// the observe and create sides never disagree about a kind nothing can settle.
	pathRESTActionAbsent
	// pathAsyncCreatePendingGet and pathAsyncCreatePendingFindBy: not found while the Pending condition is
	// set, which means a create is in flight at the vendor.
	pathAsyncCreatePendingGet
	pathAsyncCreatePendingFindBy
	// pathAsyncPollPending: a requeue-based async operation is still pending. Nothing else schedules the
	// next poll, which is the same gap as the others in a second place.
	pathAsyncPollPending
	// pathCreateRequested: a create was issued this pass and returned nil. This is issue 648's own
	// headline: handleCreate returns nil and enqueues no observe, so a healthy resource reads
	// Ready=False/Creating until the next watch event or resync (a consumer measured 153.1s to the first
	// GET). The vendor finishing, and the observe that reports it, need nobody to act.
	//
	// THE BROADEST ROW: it fires on EVERY successful create. The cost is one extra cheap Observe per
	// created resource, against a healthy resource otherwise reporting Ready=False/Creating for up to a
	// full resync. Ungated: a create just happened, so definitionCanSettle is true by construction.
	pathCreateRequested

	// --- NO HINT ---

	// pathDispatchNotYetVisible: the dispatched correlation token has not appeared yet. THE RULE WOULD FIT.
	// It is held back because the DispatchedButNotObserved verdict is COUNT-based (maxDispatchObserveMisses
	// observes), so a 15s hint would latch that failure at about 75s instead of about 15 minutes. Issue 947.
	// This row changes once 947 makes the deadline time-based; it is not excluded by the rule.
	pathDispatchNotYetVisible
	// pathActing: the pass reports absence or drift and the runtime acts on it (create, update). Acting is
	// what the rule excludes.
	pathActing
	// pathSettled: exists and up to date, or assumed so. Nothing is waiting.
	pathSettled
	// pathNoCreateNoFindBy: no create verb AND no findby. Nothing can ever adopt; an author must declare a
	// verb. Reached through the gate on pathNoCreateVerbAdopt and pathRESTActionAbsent.
	pathNoCreateNoFindBy
	// pathAuthorizationDenied: the credential cannot do the verb. A person grants it.
	pathAuthorizationDenied
	// pathCreateRefusedPersistently: the vendor refuses the create deterministically. A person changes the spec.
	pathCreateRefusedPersistently
	// pathDriftNotConverging: the update issued for a difference is not resolving it. A person decides.
	pathDriftNotConverging
	// pathCorrelationTokenMissing: spec carries no correlation token. A person sets it.
	pathCorrelationTokenMissing
	// pathBeingDeleted: the object is being deleted; waiting for it to settle is moot.
	pathBeingDeleted
	// pathCount is the number of paths; the test requires a policy row for every one below it.
	pathCount
)

type recheckRule struct {
	name string
	hint bool
	// gated: hint only if definitionCanSettle says the definition can ever settle.
	gated bool
}

// recheckPolicy is the rule applied to every path. A new recheckPath without a row here fails the test.
var recheckPolicy = map[recheckPath]recheckRule{
	pathNoCreateVerbAdopt:         {name: "no create verb, findby declared (adopt / PUT-only adopt)", hint: true, gated: true},
	pathReferentMissingDispatch:   {name: "dependent lookup referent missing (dispatch create)", hint: true},
	pathReferentMissingFields:     {name: "dependent lookup referent missing (create fields)", hint: true},
	pathRESTActionAbsent:          {name: "observe via RESTAction reports absent", hint: true, gated: true},
	pathAsyncCreatePendingGet:     {name: "get not found while create Pending", hint: true},
	pathAsyncCreatePendingFindBy:  {name: "findby not found while create Pending", hint: true},
	pathAsyncPollPending:          {name: "async requeue poll still pending", hint: true},
	pathDispatchNotYetVisible:     {name: "dispatch token not yet visible (issue 947)"},
	pathActing:                    {name: "absence or drift reported, runtime acts"},
	pathSettled:                   {name: "exists and up to date"},
	pathNoCreateNoFindBy:          {name: "no create verb and no findby"},
	pathAuthorizationDenied:       {name: "authorization denied"},
	pathCreateRefusedPersistently: {name: "create refused persistently"},
	pathDriftNotConverging:        {name: "drift not converging"},
	pathCorrelationTokenMissing:   {name: "correlation token missing"},
	pathBeingDeleted:              {name: "being deleted"},
	pathCreateRequested:           {name: "create requested this pass (issue 648)", hint: true},
}

// recheckAfter is the single place a hint is decided. Production returns call it; nothing sets
// RecheckAfter by hand.
func recheckAfter(p recheckPath, r *getter.Resource) time.Duration {
	rule, ok := recheckPolicy[p]
	if !ok || !rule.hint {
		return 0
	}
	if rule.gated && !definitionCanSettle(r) {
		return 0
	}
	return RecheckWhenWaiting
}

// definitionCanSettle reports whether anything could ever settle a kind that is waiting: it declares a way
// to create (a create verb, a createApiRef or a dispatch verb) or a findby to adopt by.
//
// createApiRef and dispatch COUNT as ways to create, and must not be narrowed back to "a create verb": a
// createApiRef kind delegates its creation to a RESTAction and a dispatch kind creates by dispatching, so
// both can settle on their own and neither is waiting on an author. A kind with
// neither has nothing that can ever make it exist, so it is waiting on an author, not on time.
//
// ONE predicate, called by both the create side and the observe side, because they must agree about one
// resource.
func definitionCanSettle(r *getter.Resource) bool {
	if r == nil {
		return false
	}
	if r.CreateApiRef != nil {
		return true
	}
	for _, v := range r.VerbsDescription {
		switch strings.ToLower(v.Action) {
		case "create", "findby", "dispatch":
			return true
		}
	}
	return false
}
