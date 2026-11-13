package oas2jsonschema

import "strings"

// StatusResolvedResolversDigest is the status field carrying the salted digest of the values this
// resource's secretRef resolvers last resolved to (#598).
//
// It is a CONSTANT shared by the generator that declares it and the tests that assert it, so the
// name cannot drift between the declaration and anything checking for it.
//
// rest-dynamic-controller writes the same key. It is a separate Go MODULE in this same repository
// and this name lives under internal/, so the two cannot share a definition: they share
// `testdata/resolver-digest-contract.json` at the repo root instead, and each module's suite asserts
// its own side against it. See shared_resolver_digest_test.go for why a drift in either direction is
// silent.
const StatusResolvedResolversDigest = "resolvedResolversDigest"

// hasUpdatableSecretRefResolver reports whether the UPDATE verb's field mapping carries a secretRef
// resolver -- which is exactly the condition under which the digest field is ever written.
//
// TWO NARROWINGS, both load-bearing, and the second is the one that is easy to get wrong.
//
// NOT EVERY RESOLVER. `dependentLookup` is a resolver but not a secretRef: its value comes from
// another resource via an API call, not from a Secret's content, so it has no rotation story. It
// must not pull the field in -- and on the controller side, resolving one costs a vendor round-trip
// per CR per poll.
//
// NOT EVERY VERB. Only the update verb's resolvers count. A digest difference's ONLY remedy is an
// update, so a resolver the update does not send is one whose rotation an update cannot push. The
// generated controller already applies this exact principle to ordinary drift -- "no update verb, or
// one without a JSON body, means nothing about this resource is fixable by an update, so nothing
// should be reported as drift" -- and a digest is drift by another channel.
//
// Widening either side of that is a distinct failure:
//
//	declaring wider than we write  -> a permanently empty field on kinds that never use it, which is
//	                                  a generated-CRD shape change that buys nothing.
//	writing wider than we declare  -> a create-only secretRef reports a difference no update can
//	                                  retire: one update per poll, per CR, forever.
//
// So this predicate IS the scope rule, and `testdata/resolver-digest-contract.json` pins it against
// the controller's.
//
// A resolver's `when` guard is deliberately not consulted here. The guard decides whether a given CR
// INSTANCE resolves the value; this decides whether the KIND can ever need the field. A guarded
// resolver is in scope even on an instance where it contributes nothing.
func (g *OASSchemaGenerator) hasUpdatableSecretRefResolver() bool {
	if g.resourceConfig == nil {
		return false
	}
	for _, verb := range g.resourceConfig.Verbs {
		if !strings.EqualFold(verb.Action, ActionUpdate) {
			continue
		}
		for _, m := range verb.FieldMapping {
			if strings.EqualFold(m.ResolverType, "secretRef") {
				return true
			}
		}
	}
	return false
}
