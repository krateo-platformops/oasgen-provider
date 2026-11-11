package oas2jsonschema

import (
	"fmt"
	"sort"
	"strings"
)

// refuseSecretRefInURL rejects a request-direction fieldMapping that would interpolate a Secret's value
// into the request URL -- a `secretRef` resolver on an `inPath` or `inQuery` destination (#555).
//
// FATAL RATHER THAN A WARNING, by decision. The alternative considered was to render it and warn, on the
// grounds that some vendors accept a token only as a query parameter. Refused because of where the value
// then goes: a URL is reproduced in access logs, forward and reverse proxy logs, metrics labels, traces,
// and the text of downstream error messages. RedactValues is populated from secretRef and covers logged
// request BODIES; it does not and cannot cover a URL assembled from a path or query parameter, because by
// then the credential is part of the request line rather than a named field.
//
// A warning would also have been the weaker instrument in practice. It would arrive in
// status.schemaWarnings beside benign schema notes, on a definition that otherwise works, and the failure
// it predicts is invisible at the point of use -- the request succeeds, and the credential is already in
// three log systems. A refusal is legible at the one moment the author can still choose another
// destination.
//
// WHAT IS STILL ALLOWED, deliberately: a secretRef on `inBody`, and on a header, which is what the
// overwhelming majority of apiKey schemes want. Only the two URL-bearing destinations are refused, so this
// forecloses nothing a vendor legitimately needs except "credential in the URL".
//
// dependentLookup is NOT refused on any destination. It substitutes a value read back from the vendor's
// own API -- an id, a name -- which is not a credential and is the case #555 was reported for.
//
// ALL OFFENDING ENTRIES ARE REPORTED AT ONCE, sorted, rather than failing on the first. An author with
// three such mappings should learn that from one render instead of three, and a stable order means the
// message does not depend on verb iteration order.
func (g *OASSchemaGenerator) refuseSecretRefInURL() error {
	if g == nil || g.resourceConfig == nil {
		return nil
	}

	var offences []string
	seen := map[string]bool{}
	for _, verb := range g.resourceConfig.Verbs {
		for _, m := range verb.FieldMapping {
			if m.ResolverType != "secretRef" {
				continue
			}
			// requestDestination reports the single request-direction destination this entry addresses.
			// "body" is permitted and "" means the entry is response-direction, so neither is an offence.
			dest, name := requestDestination(m)
			if dest != "path" && dest != "query" {
				continue
			}
			key := verb.Action + "\x00" + dest + "\x00" + name
			if seen[key] {
				continue
			}
			seen[key] = true
			offences = append(offences, fmt.Sprintf("verb %q maps a secretRef resolver to %s parameter %q",
				verb.Action, dest, name))
		}
	}
	if len(offences) == 0 {
		return nil
	}
	sort.Strings(offences)

	return fmt.Errorf("a secretRef resolver cannot supply a path or query parameter, because the Secret's "+
		"value would appear in the request URL and therefore in access logs, proxy logs, metrics labels "+
		"and downstream error messages (redaction covers request bodies, not the request line): %s. "+
		"Move the credential to a header or to the request body, which are not refused",
		strings.Join(offences, "; "))
}
