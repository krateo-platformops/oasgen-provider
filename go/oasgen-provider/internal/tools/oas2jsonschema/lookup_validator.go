package oas2jsonschema

import (
	"fmt"
	"slices"
)

// ValidateDependentLookups refuses a resource whose dependentLookup resolvers do not line up with its
// declared lookups.
//
// These two checks would naturally be CRD validation -- they reject a declaration, not a document -- and
// they are here only because they cannot be. Both have to traverse verbsDescription x fieldMapping, and
// neither list carries a maxItems bound; verbsDescription's was removed on purpose (#169). Written as CEL
// they measure over 100x the apiserver's per-rule cost budget AND push the whole schema past its total,
// which makes the apiserver refuse the CustomResourceDefinition outright: the chart fails to install and
// the provider never starts. apis/restdefinitions/v1alpha1/crd_cost_budget_test.go measures that with the
// apiserver's own estimator so the tradeoff cannot be silently undone.
//
// The cost of enforcing here instead is that a bad declaration is ADMITTED and then fails to generate,
// rather than being rejected at write time. That is the same bargain the per-verb oasPath disagreement
// check makes, and it is the right way round: a resolver naming a lookup that does not exist would
// otherwise send the request without the value it was supposed to carry.
//
// Returns a non-nil error for the FIRST problem found, with the verb's action and the offending name in
// the message, because the caller maps it onto a field path for the user.
func (g *OASSchemaGenerator) ValidateDependentLookups() error {
	declared := make([]string, 0, len(g.resourceConfig.DependentLookups))
	for _, l := range g.resourceConfig.DependentLookups {
		declared = append(declared, l.Name)
	}

	for _, verb := range g.resourceConfig.Verbs {
		for _, m := range verb.FieldMapping {
			if m.ResolverLookupName == "" {
				continue
			}
			if len(declared) == 0 {
				return SchemaGenerationError{
					Path:    fmt.Sprintf("verbsDescription[%s].fieldMapping", verb.Action),
					Code:    CodeLookupNotFound,
					Message: fmt.Sprintf("verb %q has a dependentLookup resolver naming %q, but the resource declares no dependentLookups", verb.Action, m.ResolverLookupName),
					Got:     m.ResolverLookupName,
				}
			}
			if !slices.Contains(declared, m.ResolverLookupName) {
				return SchemaGenerationError{
					Path:    fmt.Sprintf("verbsDescription[%s].fieldMapping", verb.Action),
					Code:    CodeLookupNotFound,
					Message: fmt.Sprintf("verb %q has a dependentLookup resolver naming %q, which is not a declared dependentLookups entry (declared: %v)", verb.Action, m.ResolverLookupName, declared),
					Got:     m.ResolverLookupName,
				}
			}
		}
	}
	return nil
}
