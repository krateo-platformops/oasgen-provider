package oas2jsonschema

import (
	"fmt"
	"strings"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/pathparsing"
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
func (g *OASSchemaGenerator) warnIdentifiersMissingFromFindByResponse() []error {
	identifiers := g.resourceConfig.Identifiers
	if len(identifiers) == 0 {
		return nil
	}

	var findBy *Verb
	for i := range g.resourceConfig.Verbs {
		if strings.EqualFold(g.resourceConfig.Verbs[i].Action, ActionFindBy) {
			findBy = &g.resourceConfig.Verbs[i]
			break
		}
	}
	if findBy == nil {
		// No findby verb, so nothing ever compares identifiers against a response.
		return nil
	}
	if g.resourceConfig.ObserveDelegated {
		// The findby is declared but never executed; see ResourceConfig.ObserveDelegated.
		return nil
	}

	envelope, _, err := extractRawResponseSchemaForAction(g.docs, g.resourceConfig.Verbs, ActionFindBy, g.generatorConfig)
	if err != nil || envelope == nil {
		return nil // cannot read the response, so cannot assert a field is missing from it
	}
	item, uerr := unwrapFindByItems(envelope, findBy.ItemsPath)
	if uerr != nil || item == nil {
		// An ambiguous envelope is its own, louder diagnostic (ErrAmbiguousCollection); do not pile a
		// speculative absence claim on top of it.
		return nil
	}

	policy := strings.ToUpper(findBy.IdentifiersMatchPolicy)
	var missing []string
	for _, identifier := range identifiers {
		segments, perr := pathparsing.ParsePath(identifier)
		if perr != nil || len(segments) == 0 {
			continue // an unparseable identifier path is a different defect, already reported elsewhere
		}
		if found, _ := lookupTypeByPath(item, segments); !found {
			missing = append(missing, identifier)
		}
	}
	if len(missing) == 0 {
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
