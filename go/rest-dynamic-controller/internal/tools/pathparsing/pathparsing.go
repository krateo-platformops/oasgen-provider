package pathparsing

import (
	"github.com/krateo-platformops/plumbing/fieldpath"
)

// ParsePath parses a field path into flat segments.
//
// It delegates to plumbing/fieldpath, which is the one implementation of this dialect (#513). Three
// modules used to carry three parsers for it, with three different accepted grammars and no test
// comparing them: this module's, oasgen-provider's (dot notation ONLY, while its doc comment claimed
// bracket support it refused), and unstructured-runtime's `strings.Split(path, ".")`, which silently
// mis-splits a quoted key containing a dot.
//
// # Why this stayed a one-function adapter instead of 26 call-site edits
//
// Every caller here wants the flat form, and `fieldpath.Keys` is documented as exactly that -- lossy on
// purpose, because `a[0]` (a list index) and `a['0']` (a map key) flatten to the same thing. Rewriting 26
// sites to carry typed segments would be a large diff whose reviewers could not separate "moved to the
// shared parser" from "changed what this site does with the result". The behaviour change lives in one
// function, where stated_changes_test.go guards it.
//
// (This comment named differential_test.go, which at the time existed nowhere: the differential that
// found these five changes was deleted once the adoption made its rows tautologies, and
// stated_changes_test.go says so in its own doc. differential_test.go now exists again and guards a
// different pair -- this flat walker against the typed one in typed.go -- so the two names are no longer
// interchangeable.)
//
// The typed form is available directly from `fieldpath.Parse` for any site that later needs to tell an
// index from a key -- a writer, a validator, or a diagnostic. `GetNestedField` does not: it dispatches on
// the runtime type of the DATA, so a value is a map or a list and the segment's text is read accordingly.
//
// # Five behaviour changes, all stated in #513's step-1 PR
//
//	""              was ([""], nil) -- a plausible-but-wrong value that passed every len(segs)==0 guard
//	                and walked to a property named "". Now an error. Callers that can receive an unset
//	                optional field must test path == "" BEFORE parsing; 12 of the 26 already do.
//	a['my key']     a space anywhere used to reject the whole path. Whitespace is now allowed where it is
//	                delimited (quoted keys, predicate values) and still refused in a bare name.
//	".id"           the old parser KEPT the leading dot, so ".id" named a property called ".id" and found
//	                nothing. It is now stripped. This is a FIX for a dialect confusion, not compatibility.
//	a[0][1]         adjacent brackets used to be refused; now two segments.
//	a['']           an empty bracket used to be refused; an explicitly-quoted empty key is now a key.
func ParsePath(path string) ([]string, error) {
	segs, err := fieldpath.Parse(path)
	if err != nil {
		return nil, err
	}
	return fieldpath.Keys(segs), nil
}
