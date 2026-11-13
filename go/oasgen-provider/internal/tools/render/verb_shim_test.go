package render

import (
	"reflect"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/oas2jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// everyFieldSet is a VerbsDescription with a value for every field oas2jsonschema.Verb has a counterpart
// for, so a dropped field shows up as a zero value on the far side.
func everyFieldSet() definitionv1alpha1.VerbsDescription {
	return definitionv1alpha1.VerbsDescription{
		Action:                 "findby",
		Method:                 "GET",
		Path:                   "/widgets",
		ItemsPath:              "items",
		IdentifiersMatchPolicy: "AND",
		FieldMapping: []definitionv1alpha1.FieldMappingItem{
			{InBody: "name", InCustomResource: "spec.name"},
		},
		Correlation: &definitionv1alpha1.Correlation{
			FromSpec: "spec.token",
			Observe:  definitionv1alpha1.CorrelationObserve{Method: "GET", Path: "/runs", ItemsPath: "runs"},
			EchoedAt: ".inputs.correlationId",
		},
		Async: &definitionv1alpha1.AsyncConfig{Mode: "poll"},
		// The static injections. They SUPPLY a parameter, which makes them indistinguishable from a
		// fieldMapping for any check asking whether a parameter will have a value -- and they beat both
		// spec and status, because rest-dynamic-controller sets them before auto-population and
		// auto-population does not overwrite. The shim dropped them until #463 needed them.
		Queries: []definitionv1alpha1.QueryParam{{Name: "api-version", Value: "2024-01-01"}},
		Headers: []definitionv1alpha1.HeaderItem{{Name: "Accept", Value: "application/json"}},
	}
}

// TestEveryVerbFieldIsCarriedByTheShim is an anti-drift test, not a behaviour test.
//
// The #444 warning reads IdentifiersMatchPolicy off the findby verb. If the shim stopped copying it, a
// RestDefinition that declares `AND` would arrive at the generator declaring nothing, and the warning
// would fire on a resource whose author had already made the choice -- a false positive produced by a
// missing assignment rather than by a wrong rule, and invisible to every test that constructs a Verb
// directly.
//
// Rather than assert that one field, this asserts that NO exported field of Verb is left zero. The next
// field added to the domain type is then wired or the test fails, which is the only version of this check
// that survives the change it exists to catch.
func TestEveryVerbFieldIsCarriedByTheShim(t *testing.T) {
	got := toDomainVerbs([]definitionv1alpha1.VerbsDescription{everyFieldSet()})
	require.Len(t, got, 1)

	v := reflect.ValueOf(got[0])
	typ := v.Type()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		assert.False(t, v.Field(i).IsZero(),
			"oas2jsonschema.Verb.%s was left zero by toDomainVerbs. A field on both types that the shim "+
				"does not copy is silently absent: it compiles, and the generator reads a zero value.", f.Name)
	}

	// Guard against the guard passing for the wrong reason: if Verb ever loses its exported fields, or
	// reflection returns nothing, the loop above makes no assertions at all and still reports success.
	assert.GreaterOrEqual(t, typ.NumField(), 8,
		"expected at least the 8 known fields on oas2jsonschema.Verb; a smaller count means this test "+
			"inspected less than it thinks")

	// And pin the motivating field by name, so the reason this test exists survives a refactor of the
	// reflection loop.
	assert.Equal(t, "AND", got[0].IdentifiersMatchPolicy)
	_ = oas2jsonschema.Verb{}
}

// everyResourceFieldSet is a RestDefinition resource block with a value for every field
// oas2jsonschema.ResourceConfig has a counterpart for.
func everyResourceFieldSet() *definitionv1alpha1.RestDefinition {
	return &definitionv1alpha1.RestDefinition{
		Spec: definitionv1alpha1.RestDefinitionSpec{
			Resource: definitionv1alpha1.Resource{
				Kind:                   "Widget",
				Identifiers:            []string{"name", "owner"},
				AdditionalStatusFields: []string{"id"},
				ExcludedSpecFields:     []string{"secret"},
				IncludedSpecFields:     []string{"name"},
				VerbsDescription:       []definitionv1alpha1.VerbsDescription{everyFieldSet()},
				ConfigurationFields: []definitionv1alpha1.ConfigurationField{{
					FromOpenAPI:        definitionv1alpha1.FromOpenAPI{Name: "X-Tenant", In: "header"},
					FromRestDefinition: definitionv1alpha1.FromRestDefinition{Actions: []string{"findby"}},
				}},
				DependentLookups: []definitionv1alpha1.DependentLookup{{
					Name:   "parent",
					Method: "GET",
					Path:   "/parents/{id}",
				}},
				ObserveApiRef: &definitionv1alpha1.ApiRef{Namespace: "krateo-system", Name: "observe-widget"},
				CreateApiRef:  &definitionv1alpha1.ApiRef{Namespace: "krateo-system", Name: "observe-widget"},
			},
		},
	}
}

// TestEveryResourceConfigFieldIsCarriedByTheShim is the same anti-drift guard as the verb one, for the
// struct the #444 gate's suppression depends on.
//
// ObserveDelegated is why this is worth the fixture. The warning stays SILENT when observation is
// delegated, so if the shim stopped setting it the failure would not be a lost feature — it would be the
// warning firing on exactly the resources it was taught to ignore, which is the false-positive mode the
// whole gate exists to prevent. A dropped assignment there is invisible: it compiles, and a bool reads
// false.
func TestEveryResourceConfigFieldIsCarriedByTheShim(t *testing.T) {
	cr := everyResourceFieldSet()
	got, err := toDomainResourceConfig(cr, toDomainVerbs(cr.Spec.Resource.VerbsDescription))
	require.NoError(t, err)

	v := reflect.ValueOf(*got)
	typ := v.Type()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		assert.False(t, v.Field(i).IsZero(),
			"oas2jsonschema.ResourceConfig.%s was left zero by toDomainResourceConfig. A field the "+
				"generator reads and this shim does not set is silently zero, not a compile error.", f.Name)
	}

	assert.GreaterOrEqual(t, typ.NumField(), 8,
		"expected at least the 8 known fields on ResourceConfig; a smaller count means this test "+
			"inspected less than it thinks")

	// Pin the motivating field by name and by its derivation, not just its truthiness: it is a bool
	// computed from a pointer, so "set" and "set correctly" are different claims.
	assert.True(t, got.ObserveDelegated, "observeApiRef present must mean ObserveDelegated")
	cr.Spec.Resource.ObserveApiRef = nil
	got2, err := toDomainResourceConfig(cr, nil)
	require.NoError(t, err)
	assert.False(t, got2.ObserveDelegated, "observeApiRef absent must mean !ObserveDelegated")

	// CreateDelegated's derivation, and critically its INDEPENDENCE from ObserveDelegated, is pinned
	// by TestDelegationFlagsArePlumbedIndependently in delegation_plumbing_test.go. The two plumbing
	// lines are adjacent in helper.go, so a cross-wiring is one word away and passes any test that
	// only ever sets both refs together -- which this fixture does.
	assert.True(t, got.CreateDelegated, "createApiRef present must mean CreateDelegated")
}

// TestTheShimCarriesStaticInjections closes the blind spot that lost them.
//
// The reflection guard in this file iterates the DOMAIN type's fields, so it catches a domain field the
// shim forgets to SET. It cannot catch a field present on VerbsDescription and ABSENT from the domain
// type -- which is how Queries and Headers were dropped silently, and why a generation check later fired
// on a parameter a static query always supplies.
//
// This asserts the carry directly. It does not close the general class: any other VerbsDescription field
// the generator comes to need has the same invisibility, and a guard for that needs an explicit
// carried/not-carried inventory of the source type.
func TestTheShimCarriesStaticInjections(t *testing.T) {
	verbs := toDomainVerbs([]definitionv1alpha1.VerbsDescription{{
		Action: "get", Method: "GET", Path: "/x",
		Queries: []definitionv1alpha1.QueryParam{{Name: "api-version", Value: "2024-01-01"}},
		Headers: []definitionv1alpha1.HeaderItem{{Name: "Accept", Value: "application/json"}},
	}})
	require.Len(t, verbs, 1)

	require.Len(t, verbs[0].StaticQueries, 1, "the verb's static queries were dropped by the shim")
	assert.Equal(t, "api-version", verbs[0].StaticQueries[0].Name)
	assert.Equal(t, "2024-01-01", verbs[0].StaticQueries[0].Value)

	require.Len(t, verbs[0].StaticHeaders, 1, "the verb's static headers were dropped by the shim")
	assert.Equal(t, "Accept", verbs[0].StaticHeaders[0].Name)
	assert.Equal(t, "application/json", verbs[0].StaticHeaders[0].Value)
}
