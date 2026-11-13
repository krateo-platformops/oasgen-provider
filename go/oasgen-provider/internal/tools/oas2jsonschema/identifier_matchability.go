package oas2jsonschema

import (
	"fmt"
	"strings"
)

// warnIdentifiersMissingFromFindByResponse reports a declared identifier that findby can never match on,
// because the items findby returns do not carry it.
//
// # Why this is a second warning rather than a widening of CodeIdentifierNotResolvable
//
// CodeIdentifierNotResolvable answers a different question: can the user EXPRESS this identifier, and what
// TYPE should the selector have. That question only arises for a resource with no create verb, which is why
// its emitter sits under `if !g.hasCreateVerb()`, and it resolves types through getBaseSchemaForStatus,
// which prefers the GET response because status is populated from whichever observe verb exists.
//
// Both of those are right for expressibility and wrong for MATCHABILITY:
//
//   - matching happens for every kind that declares findby, create verb or not. The guard made the check
//     unreachable for most kinds, and the comment above it stated the assumption that led there -- "an
//     identifier is always a spec field, which holds only because a create body normally defines it". A
//     create body says what we SEND. Matchability depends only on what we RECEIVE.
//   - matching reads the FINDBY response, never the get response. A vendor whose item endpoint is complete
//     and whose collection is abridged satisfies a get-resolved check and still cannot match: Ironic's
//     /v1/nodes/{uuid} carries ~40 fields while /v1/nodes abridges items to 7.
//
// Widening the existing warning would have made one function serve two questions whose correct guards and
// correct schemas differ, which is how it acquired three separate blind spots. So this asks only its own
// question, against its own schema, for every kind that can reach the matcher (#445).
//
// # Silence when the response cannot be read, which is the OPPOSITE of #444's choice
//
// #444's warning says "this configuration is risky", so an unreadable response -- an inability to prove it
// is a single object -- warrants warning. This one asserts "this identifier is ABSENT", a positive claim
// about the response, and absence cannot be established from a schema that could not be read. A warning
// naming a specific field as missing, emitted because the document was unreadable, would be wrong rather
// than cautious. So the two branch opposite ways on the same input, deliberately.
func (g *OASSchemaGenerator) warnIdentifiersMissingFromFindByResponse(mr matchabilityResult) []error {
	// The verdict is computed ONCE by the caller and handed to both directions, so the suppression
	// below and issue 629's warning cannot be driven by two computations that disagree.
	if mr.Verdict == MatchabilityIndeterminate {
		return nil
	}
	identifiers := g.resourceConfig.Identifiers
	missing := mr.Missing
	policy := mr.Policy
	if len(missing) == 0 {
		return nil
	}

	// ISSUE 625: SUPPRESSED FOR AN ACTION-ONLY KIND, and the suppression is the fix rather than a
	// softening of it.
	//
	// For that shape an unmatchable findby is not a defect -- it is the only thing that makes the
	// resource work, because unstructured-runtime queues a Create only on !ResourceExists. The
	// message below tells the author to "remove %s from identifiers", and a consumer who followed it
	// quoted this warning in a commit as the justification for making findby match, which disabled
	// every provision transition for 24 hours. The same change had been reverted in their repo four
	// days earlier under the subject "the responseTransform I added today would have silently
	// stopped every transition" -- so this warning out-argued the author's own correct revert note.
	//
	// A diagnostic that overrides a correct local decision is worse than no diagnostic. For this
	// shape the accurate warning is close to the inverse, and that is what
	// warnCreateUnreachableForActionOnlyKind emits instead.
	if g.isActionOnly() {
		return nil
	}

	// The consequence differs by policy, and so does the severity, so the message says which applies
	// rather than leaving the reader to work it out. Under AND the resource is simply non-functional:
	// every item fails the comparison, findby reports not-found, and the reconciler acts on not-found by
	// CREATING -- a duplicate external resource per reconcile.
	var consequence string
	switch {
	case policy == "AND":
		consequence = fmt.Sprintf(
			"identifiersMatchPolicy is AND, so EVERY declared identifier must match and findby can therefore "+
				"never match any item: it will report not-found and the reconciler acts on not-found by creating. "+
				"Remove %s from identifiers, or declare an itemsPath/findby whose items carry it",
			quoteList(missing))
	case len(missing) == len(identifiers):
		consequence = "no declared identifier is present in the findby response, so findby can never match any item " +
			"regardless of policy: it will report not-found and the reconciler acts on not-found by creating"
	default:
		remaining := len(identifiers) - len(missing)
		consequence = fmt.Sprintf(
			"identifiersMatchPolicy is OR (the default), so matching silently narrows to the %d identifier(s) "+
				"that ARE present; %s contribute nothing. Verify the remaining one(s) identify the object uniquely",
			remaining, quoteList(missing))
	}

	return []error{SchemaGenerationError{
		Path: "spec.resource.identifiers",
		Code: CodeIdentifierNotInFindByResponse,
		Message: fmt.Sprintf(
			"%s absent from the items the findby response carries, so findby cannot compare against %s. %s",
			quoteList(missing), pluralise(len(missing), "it", "them"), consequence),
		Got: missing,
	}}
}

func quoteList(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, i := range items {
		quoted = append(quoted, fmt.Sprintf("%q", i))
	}
	if len(quoted) == 1 {
		return "identifier " + quoted[0] + " is"
	}
	return "identifiers " + strings.Join(quoted, ", ") + " are"
}

func pluralise(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// warnCreateUnreachableForActionOnlyKind reports the inverse defect (issue 629): a kind whose create
// is its only write verb, whose findby CAN match, and whose only effect therefore never fires.
//
// # WHY A MISSING DIAGNOSTIC HERE IS WORSE THAN AN ORDINARY ONE
//
// The measured symptom is an INVERTED health signal. The broken resource reports
// Observed=True, Ready=True/Available, Synced=True/ReconcileSuccess with zero creates and the
// external object still in its initial state, while the WORKING one reports Ready=False/Pending.
// Triaging by CR health you would pick the broken one. unstructured-runtime
// (pkg/controller/worker.go) queues a Create only on !observation.ResourceExists -- nothing about
// up-to-dateness can produce one -- so the moment findby matches, Observe returns
// {ResourceExists: true, ResourceUpToDate: true}, logs "External resource up-to-date", and the
// definition's only effect never fires again.
//
// This is generation-time only. It tells the AUTHOR at apply time; it does not fix the inverted CR
// condition an operator triages later, which is the runtime half and lands separately.
//
// RDC's latched_absence.go already covers the UNMATCHED case at runtime -- a synthesised not-found
// plus Pending takes the latch and reportLatchedInferredAbsence warns after a repeat threshold. The
// matched case is the symmetric gap, and nothing covered it on any path.
func (g *OASSchemaGenerator) warnCreateUnreachableForActionOnlyKind(mr matchabilityResult) []error {
	if !g.isActionOnly() {
		return nil
	}
	// Only a POSITIVE matchability finding justifies this claim. Indeterminate must stay silent in
	// BOTH directions: a document whose findby envelope could not be read tells us nothing about
	// whether the create is reachable, and a confident "your only verb can never run" emitted
	// because a schema was unreadable would be wrong rather than cautious.
	if mr.Verdict != MatchabilityCanMatch {
		return nil
	}

	detail := "all declared identifiers are present in the items the findby response carries"
	if len(mr.Missing) > 0 {
		// The OR-with-some-present case: matching narrows to the ones that ARE present and still
		// succeeds, so the create is just as unreachable as if none were missing.
		detail = "identifiersMatchPolicy is OR (the default) and at least one declared identifier is " +
			"present in the findby response, so matching narrows to it and still succeeds"
	}

	return []error{SchemaGenerationError{
		Path: "spec.resource.verbsDescription",
		Code: CodeCreateUnreachableForActionOnlyKind,
		Message: "this resource declares create as its only write verb (no get, no update, no dispatch, " +
			"no createApiRef), so its create is reachable only while findby does NOT match: the runtime " +
			"queues a create exclusively on an absent resource. " + detail + ", so findby can match -- and " +
			"once it does, this resource's only effect never fires again while every condition reports " +
			"healthy. If create is an action rather than a creation, declare identifiers the findby " +
			"response does not carry, or express the action as a dispatch verb",
	}}
}
