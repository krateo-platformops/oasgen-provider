package pathparsing

import (
	"github.com/krateo-platformops/plumbing/fieldpath"
)

// ParseTyped and GetTyped are the dialect's OWN parser and walker — plumbing's fieldpath.Parse and
// fieldpath.Get, which dispatches on each parsed segment's Kind — as against ParsePath and
// GetNestedField, which flatten to []string and re-derive each segment's intent from its text.
//
// They are kept as two steps, mirroring the pair they replace, so a caller can still tell an invalid
// PATH from a path that cannot be READ here. Collapsing them into one call would merge those two error
// classes, which is the confusion #528 exists to undo.
//
// # Why a second reader exists at all, and why it is this one that CR reads use
//
// The grammar has been shared since #513: ParsePath IS fieldpath.Keys(fieldpath.Parse(x)), and
// oasgen-provider's secretRef collector parses the same strings with the same code. What was NOT shared
// is the SELECTION rule, and for a `[?key=value]` selector the two are not separable: the parser decides
// what a path means, the walker decides which element it picks.
//
// That mattered because of where the two ends meet. oasgen-provider's #510 collector resolves a Secret
// NAME out of a custom resource and writes a Role naming it; this controller later READS that Secret. If
// the two walkers select different elements, the provider grants for one Secret and the controller reads
// another. While this controller also granted itself a per-CR Role that was computed by its own walker,
// the disagreement was invisible — each side was internally consistent. Removing that self-granting
// (#540, because it breaks adoption and races RBAC escalation on create) makes the provider's Role the
// only grant, and the two walkers agreeing stops being a nicety.
//
// # The three shapes where they DID disagree, and which way
//
// Measured, not assumed: the differential test in this package drives both walkers over one document and
// a corpus of paths. Nineteen rows agree, including every shape a consumer actually writes — plain
// dotted paths, `[?type=password]` selectors, numeric coercion, out-of-range indices, absent entries, a
// duplicate match. Three differ, all deliberately:
//
//	a['0'] against an ARRAY   flat: "0" against a slice is an index, so a QUOTED key silently indexed it.
//	                          typed: an error. The author asked for a key; an array element is not one.
//	a.0 against an ARRAY      the same collapse, and A DOCUMENTED FORM REMOVED ON PURPOSE. `a.0` and
//	                          `a['0']` parse to byte-identical segments, so no walker taking []Segment
//	                          can accept one and refuse the other. readStringPath's comment advertised
//	                          "credentials.0.value" for as long as that read was flat.
//	a[0] against a MAP        flat: "0" against a map is a key, so an INDEX silently became one.
//	                          typed: an error. The author wrote an index; a map has none.
//
// In all three, the typed walker refuses what the flat one resolved — so the provider's collector
// (typed) could not name a Secret this controller (flat) would go on to read. That is the gap, and
// taking the typed walker for every custom-resource read closes it by construction rather than by
// watching for it.
//
// The third row is the one worth remembering: the corpus did not find it. An existing test did, by
// failing. A differential is only as wide as the shapes someone thought to list, and the branch this
// one came from listed two.
//
// # What still uses the flat walker, and why that is not the same gap
//
// Writes into a request body (SetNestedField, RemoveNestedField) and the read of a RESPONSE body stay on
// the flat form, because plumbing ships no typed Set or Remove beside Get. A body has no RBAC
// consequence: nothing grants access to a field of a JSON document. Mixing the walkers WITHIN the body
// path would be the real inconsistency — a path that reads as an error and writes successfully — so the
// body path stays entirely flat until there is a typed writer to move it to, as a whole.
//
// ParseTyped parses path into the typed segment form. It is fieldpath.Parse under this package's name so
// that every use of the dialect in this module goes through one door, and so a caller needs no direct
// import of plumbing to hold the result.
func ParseTyped(path string) ([]fieldpath.Segment, error) {
	return fieldpath.Parse(path)
}

// GetTyped reads the value at segs from obj.
//
// It keeps fieldpath.Get's three-way contract unchanged, so callers keep their own wording:
//
//	found=false, err=nil   the path does not resolve against this data. ABSENT.
//	err != nil             the path cannot APPLY here — a kind mismatch, or a selector matching more
//	                       than one element. Callers must not collapse this into "absent".
func GetTyped(obj any, segs []fieldpath.Segment) (any, bool, error) {
	return fieldpath.Get(obj, segs)
}
