package oas2jsonschema

import (
	"strings"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/pathparsing"
)

// Matchability is the three-valued answer to "can this resource's findby match its declared
// identifiers?".
//
// # WHY THREE VALUES AND NOT A BOOL
//
// Two diagnostics depend on this question and they point in OPPOSITE directions:
//
//   - CodeIdentifierNotInFindByResponse (issue 625) must NOT fire for an action-only kind, because
//     for that shape an unmatchable findby is the only thing that makes the resource work.
//   - CodeCreateUnreachableForActionOnlyKind (issue 629) MUST fire for an action-only kind whose
//     findby CAN match, because then the kind's only verb can never run.
//
// A bool forces "cannot tell" to be reported as one of the two real answers, and whichever one it is
// folded into, that diagnostic becomes wrong on a document nobody could read. The matchability
// computation has five distinct ways of not knowing, and before this type all five returned the same
// nil as "the identifiers are all present" -- so "can match" and "cannot tell" were indistinguishable
// to every caller.
type Matchability int

const (
	// MatchabilityIndeterminate is the ZERO VALUE on purpose. A caller that forgets to assign a
	// verdict gets "cannot tell", which fires NEITHER diagnostic; the failure mode of a missing
	// assignment is silence rather than a false positive in one direction or the other.
	MatchabilityIndeterminate Matchability = iota
	// MatchabilityCanMatch: findby can match at least one item on its declared identifiers.
	MatchabilityCanMatch
	// MatchabilityCannotMatch: findby can never match, so it always reports not-found.
	MatchabilityCannotMatch
)

func (m Matchability) String() string {
	switch m {
	case MatchabilityCanMatch:
		return "CanMatch"
	case MatchabilityCannotMatch:
		return "CannotMatch"
	default:
		return "Indeterminate"
	}
}

// Reasons a verdict is Indeterminate. These are a diagnostic aid for tests and for anyone reading a
// trace: without them, a test asserting "neither diagnostic fired" cannot tell WHICH of the five
// causes it exercised, and would pass for a cause it never meant to cover.
const (
	IndeterminateNoIdentifiers      = "no identifiers are declared, so there is nothing to match on"
	IndeterminateNoFindBy           = "no findby verb is declared, so nothing ever compares identifiers"
	IndeterminateObserveDelegated   = "observation is delegated via observeApiRef, so the findby never executes"
	IndeterminateResponseUnreadable = "the findby response schema could not be read"
	IndeterminateEnvelopeAmbiguous  = "the findby envelope is ambiguous (its own diagnostic reports this)"
)

// matchabilityResult carries the verdict plus what is needed to describe it.
type matchabilityResult struct {
	Verdict Matchability
	// Missing lists the declared identifiers absent from the findby response items. Meaningful only
	// when the response could be read; empty for every Indeterminate verdict.
	Missing []string
	// Policy is the upper-cased identifiersMatchPolicy actually in force.
	Policy string
	// Reason is non-empty exactly when Verdict is MatchabilityIndeterminate.
	Reason string
}

func indeterminate(reason string) matchabilityResult {
	return matchabilityResult{Verdict: MatchabilityIndeterminate, Reason: reason}
}

// matchability answers the question once, for every caller.
//
// THE POLICY DEPENDENCE IS LOAD-BEARING. "Some identifiers are missing" is not the same verdict under
// the two policies, and getting it wrong in the lenient direction is what would make issue 629's
// diagnostic miss its own case:
//
//	AND, any missing  -> CannotMatch   every item fails the comparison
//	AND, none missing -> CanMatch
//	OR,  all missing  -> CannotMatch
//	OR,  some missing -> CanMatch      matching NARROWS to the present ones; it does not stop
//
// That last row is the one a naive "any missing means it cannot match" gets wrong, and it is exactly
// the row where a create becomes unreachable for an action-only kind.
func (g *OASSchemaGenerator) matchability() matchabilityResult {
	identifiers := g.resourceConfig.Identifiers
	if len(identifiers) == 0 {
		return indeterminate(IndeterminateNoIdentifiers)
	}

	var findBy *Verb
	for i := range g.resourceConfig.Verbs {
		if strings.EqualFold(g.resourceConfig.Verbs[i].Action, ActionFindBy) {
			findBy = &g.resourceConfig.Verbs[i]
			break
		}
	}
	if findBy == nil {
		return indeterminate(IndeterminateNoFindBy)
	}
	if g.resourceConfig.ObserveDelegated {
		return indeterminate(IndeterminateObserveDelegated)
	}

	envelope, _, err := extractRawResponseSchemaForAction(g.docs, g.resourceConfig.Verbs, ActionFindBy, g.generatorConfig)
	if err != nil || envelope == nil {
		// Absence cannot be established from a schema that could not be read. Note this is the
		// OPPOSITE of issue 444's choice, deliberately: that warning says "this configuration is
		// risky", so an unreadable response warrants warning; this one makes a positive claim about
		// what the response contains, and a positive claim cannot rest on an unread document.
		return indeterminate(IndeterminateResponseUnreadable)
	}
	item, uerr := unwrapFindByItems(envelope, findBy.ItemsPath)
	if uerr != nil || item == nil {
		// An ambiguous envelope is its own, louder diagnostic (ErrAmbiguousCollection); do not pile a
		// speculative claim in either direction on top of it.
		return indeterminate(IndeterminateEnvelopeAmbiguous)
	}

	policy := strings.ToUpper(findBy.IdentifiersMatchPolicy)
	var missing []string
	for _, identifier := range identifiers {
		segments, perr := pathparsing.ParsePath(identifier)
		if perr != nil || len(segments) == 0 {
			continue // an unparseable identifier path is a different defect, reported elsewhere
		}
		if found, _ := lookupTypeByPath(item, segments); !found {
			missing = append(missing, identifier)
		}
	}

	res := matchabilityResult{Missing: missing, Policy: policy}
	switch {
	case len(missing) == 0:
		res.Verdict = MatchabilityCanMatch
	case policy == "AND":
		// Every declared identifier must match, so one absent identifier is enough to make every
		// comparison fail.
		res.Verdict = MatchabilityCannotMatch
	case len(missing) == len(identifiers):
		res.Verdict = MatchabilityCannotMatch
	default:
		// OR with some present: matching narrows to those, and can still succeed.
		res.Verdict = MatchabilityCanMatch
	}
	return res
}

// hasVerb reports whether the resource declares the given action.
func (g *OASSchemaGenerator) hasVerb(action string) bool {
	for _, verb := range g.resourceConfig.Verbs {
		if strings.EqualFold(verb.Action, action) {
			return true
		}
	}
	return false
}

// isActionOnly reports whether create is this resource's only write verb -- the shape where create is
// an ACTION rather than a creation, and where an unmatchable findby is a feature.
//
// openstack-ironic-operator-kog's ironic-node-provision is the canonical instance: findby + create,
// where create is `PUT /v1/nodes/{node_id}/states/provision`. No get, so Observe decides existence
// from findby alone. No update, so drift cannot drive the endpoint. unstructured-runtime queues a
// Create only on !ResourceExists, so the create is reachable exactly one way -- a findby that does
// not match.
//
// THE ESCAPES ARE THE POINT, AND ONE OF THEM WAS INVISIBLE UNTIL NOW. Each clause below names a way
// the create can be reached without a non-matching findby. createApiRef is the one the generator
// could not see before CreateDelegated was plumbed: the runtime handles it before it builds any
// create call, so such a kind creates perfectly well with no create verb at all, and concluding
// "this create is unreachable" for it would be a confident false positive on a legitimate shape --
// which is the failure issue 625 exists to stop repeating.
//
// Shared with issue 624's predicate deliberately: a sixth escape added later must not be handled in
// one place and missed in the other.
func (g *OASSchemaGenerator) isActionOnly() bool {
	if !g.hasVerb(ActionCreate) {
		return false // nothing to be unreachable
	}
	if g.hasVerb(ActionGet) || g.hasVerb(ActionUpdate) || g.hasVerb(ActionDispatch) {
		return false
	}
	if g.resourceConfig.CreateDelegated || g.resourceConfig.ObserveDelegated {
		return false
	}
	return true
}
