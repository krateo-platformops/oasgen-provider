// Package pathparsing parses a field path into the property names of a JSON schema.
//
// It delegates to plumbing/fieldpath, the one implementation of this dialect (#513), and then REFUSES the
// parts of that grammar that cannot mean anything here.
package pathparsing

import (
	"fmt"
	"strconv"

	"github.com/krateo-platformops/plumbing/fieldpath"
)

// ParsePath parses a field path into flat segments, accepting only segments that name a property.
//
// # What changed, and why it is a widening and a narrowing at once
//
// The previous local parser accepted quoted keys, but ONLY with a separating dot before the bracket group,
// or with the group at the start of the path. Measured:
//
//	['b.c']            -> ["b.c"]              OK
//	a.['b.c']          -> ["a" "b.c"]          OK
//	credentials.['0']  -> ["credentials" "0"]  OK   (as a KEY "0", never an index)
//	a['b.c']           -> ERROR "invalid characters in segment"
//	credentials[0]     -> ERROR
//
// So the one refused spelling was `name[bracket]` with no dot -- and that is precisely the form the old doc
// comment gave in BOTH of its examples, while the spelling that worked was undocumented. An author who
// read the documentation and copied it got a parse error; an author who guessed the dot form succeeded.
//
// The no-dot spelling now works too, so both are accepted and the documentation is no longer wrong about
// its own examples. EXISTING RestDefinitions using the dot spelling are unaffected, which is the part that
// matters, since those are the ones in the wild.
//
// (I first described this parser as "dot notation only, rejecting all bracket notation", from a single
// probe of the doc's own spelling. This module's test file had eight passing rows contradicting that --
// green on every CI run. Before claiming a capability is absent, grep the tests for it: a passing
// assertion of not-X refutes X for the cost of one search.)
//
// Array indices and [?key=value] predicates are part of the shared grammar and are REFUSED here, at the
// path, rather than being passed on. Every caller in this module addresses the property names of a JSON
// SCHEMA:
//
//	7 sites walk a *Schema          arrayAtPath, lookupTypeByPath, fieldExistsAtPath, removeFieldAtPath,
//	                                findPropertyByPath, buildIncludeTree
//	4 sites read segments by index  as field NAMES -- two of them WRITERS that emit a property from the
//	                                last segment into a generated CRD
//
// A schema describes ONE element shape under `items`; it has no indexed elements and it is not a document,
// so neither an index nor a selector can address anything in it.
//
// # Why the refusal is here and not at the walkers
//
// Passing an index through would make it a property NAME. `lookupTypeByPath` matches `cur.Properties` by
// name, so `credentials[0]` would be looked for as a property literally called "0", fail, and be reported
// as "identifiers are absent from the items the findby response carries" -- a message that names the wrong
// cause and whose certainty is already the subject of #483.
//
// Worse at the two writer sites: `spec_builder.go` and `status_builder.go` take the LAST segment as a
// property name and emit it. An unrefused index would put a property literally named "0" into a generated
// CRD, which is the defect `spec_builder.go`'s own comment describes:
//
//	emitting a flat property literally named "metadata.name" produces a spec the controller can never
//	read: findby compares against a field that does not exist and matches nothing, silently, on a
//	resource that reports Ready (#106)
//
// # One message, not two
//
// An earlier version of this plan had two messages, one for the schema walkers and one for the positional
// readers. That distinction is real but it does not change what the author must do, and both halves are
// covered by the same true sentence: this path addresses a schema's property names. Two near-identical
// messages would be a difference the reader has to resolve for no benefit.
func ParsePath(path string) ([]string, error) {
	segs, err := fieldpath.Parse(path)
	if err != nil {
		return nil, err
	}
	for _, s := range segs {
		if s.Kind == fieldpath.KindKey {
			continue
		}
		// The escape hatch has to be SPELLED CORRECTLY or the message is advice that does not work.
		// s.String() renders an index as "[0]" and a predicate as "[?k=v]", so interpolating it into a
		// quote suggestion produced ['[0]'] -- a key literally named "[0]", which is not what any author
		// meant. The literal-key equivalent of a[0] is a.['0'], quoting the KEY and not the brackets.
		what, literal := "an array index", strconv.Itoa(s.Index)
		if s.Kind == fieldpath.KindPredicate {
			what, literal = "a [?key=value] selector", "?"+s.Key+"="+s.Value
		}
		return nil, fmt.Errorf(
			"path %q contains %s (%q), which cannot address anything here: this path names the properties "+
				"of a schema, and a schema describes one element shape rather than indexed elements. "+
				"If %q is a literal property name, quote it: ['%s']",
			path, what, s.String(), literal, literal)
	}
	return fieldpath.Keys(segs), nil
}
