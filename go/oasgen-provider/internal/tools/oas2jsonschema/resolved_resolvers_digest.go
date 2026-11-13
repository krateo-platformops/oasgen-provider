package oas2jsonschema

import "strings"

// StatusResolvedResolversDigest is the status field carrying the salted digest of the values this
// resource's secretRef resolvers last resolved to (#598).
//
// It is a CONSTANT shared by the generator that declares it and the tests that assert it, so the
// name cannot drift between the declaration and anything checking for it. rest-dynamic-controller
// writes the same key; the two repositories cannot share a definition, so a test on each side reads
// this name from its own module and they are compared by the shared fixture in testdata.
const StatusResolvedResolversDigest = "resolvedResolversDigest"

// hasSecretRefResolver reports whether any verb's field mapping carries a secretRef resolver.
//
// This is the gate on declaring the digest field, and it is deliberately narrow: `dependentLookup`
// resolvers are NOT secretRef and contribute nothing to it. A resource with no secretRef resolver
// has no Secret content to track, so declaring the field for it would change its generated CRD's
// shape to hold a permanently empty string.
func (g *OASSchemaGenerator) hasSecretRefResolver() bool {
	if g.resourceConfig == nil {
		return false
	}
	for _, verb := range g.resourceConfig.Verbs {
		for _, m := range verb.FieldMapping {
			if strings.EqualFold(m.ResolverType, "secretRef") {
				return true
			}
		}
	}
	return false
}
