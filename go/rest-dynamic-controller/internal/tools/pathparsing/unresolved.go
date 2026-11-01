package pathparsing

import "strings"

// UnresolvedPathParams returns, in order of appearance, the names of the {placeholder} segments in a path
// template that params does not populate — treating an empty value as unpopulated, since substituting it
// would silently address a different URL (".../users/" rather than ".../users/{id}").
//
// A non-empty result means the external resource is NOT ADDRESSABLE: the request cannot be aimed at
// anything, so sending it is pointless. Callers use it to distinguish "the call would fail" from "the call
// failed", which matters most on delete — a CR whose create never succeeded has no identifier in its
// status, and blocking its teardown on a request that can never be built strands the CR forever.
//
// IT LIVES HERE, NOT IN builder, BECAUSE THE PACKAGE THAT BUILDS THE URL CANNOT IMPORT builder. builder
// imports the restclient package, so restclient importing builder back is a cycle — which is how the one
// place that actually substitutes placeholders ended up with no check at all while four callers that merely
// reason ABOUT a request each had one (#361). pathparsing imports only the standard library, so both sides
// can use this single implementation rather than keeping two that drift.
func UnresolvedPathParams(path string, params map[string]string) []string {
	var missing []string
	for i := 0; i < len(path); i++ {
		if path[i] != '{' {
			continue
		}
		end := strings.IndexByte(path[i:], '}')
		if end < 0 {
			break // unterminated placeholder: nothing addressable to report beyond this point
		}
		name := path[i+1 : i+end]
		if name != "" && params[name] == "" {
			missing = append(missing, name)
		}
		i += end
	}
	return missing
}
