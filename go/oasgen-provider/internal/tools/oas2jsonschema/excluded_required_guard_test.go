package oas2jsonschema

import (
	"strings"
	"testing"
)

// #410: `requiredParameters` was documented as protecting a field from `excludedSpecFields` pruning and
// was never passed to the pruning, so the guard existed only in a comment.
//
// THE REACHABLE CASE, which is why this is worth a refusal rather than a note: excluding a REQUIRED
// parameter that no request-direction fieldMapping supplies. The exclusion succeeds, the parameter leaves
// the spec, every verb's path then has an unresolvable placeholder, and the resulting kind is generated,
// statically valid, admitted by the API server, and cannot build a single request. That is the #75 shape
// arrived at from the author's own configuration.
//
// Asserted on the WARNING and on the SCHEMA together, because either alone is satisfiable by the broken
// behaviour: the old code also left the field alone when the path did not match, and the old code also
// produced a warning (a different one) in that case.
func TestExcludingARequiredParameterIsRefusedAndTheFieldSurvives(t *testing.T) {
	g := &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		resourceConfig:  &ResourceConfig{ExcludedSpecFields: []string{"projectId"}},
	}

	schema := &Schema{
		Type: []string{"object"},
		Properties: []Property{
			{Name: "projectId", Schema: &Schema{Type: []string{"string"}}},
			{Name: "name", Schema: &Schema{Type: []string{"string"}}},
		},
	}

	warnings := g.removeExcludedSpecFields(schema, map[string]bool{"projectId": true})

	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want 1: the refusal has to say something, or an author whose kind "+
			"cannot build a URL gets no indication why", len(warnings))
	}
	msg := warnings[0].Error()
	for _, want := range []string{"projectId", "REQUIRED parameter", "cannot build its own request URL", "fieldMapping"} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning = %q, want it to mention %q. A refusal that does not name the remedy leaves "+
				"the author guessing between removing the stanza and changing the document", msg, want)
		}
	}

	// THE HALF THAT DISTINGUISHES THIS FROM THE OLD BEHAVIOUR. Before #410 the exclusion SUCCEEDED and
	// the property was gone. A test asserting only the warning would pass against code that warns and
	// prunes anyway.
	if !hasProperty(schema, "projectId") {
		t.Error("the required parameter was pruned despite the refusal. The warning without the retention " +
			"is worse than neither: the kind is still unable to build a URL and now looks diagnosed")
	}
	if !hasProperty(schema, "name") {
		t.Error("an unrelated property was removed")
	}
}

// A NON-required parameter, and an exclusion of a plain body field, must still be pruned. Otherwise the
// guard has not protected a field from a mistake, it has broken `excludedSpecFields` for everyone.
func TestExcludingANonRequiredFieldStillWorks(t *testing.T) {
	cases := []struct {
		name     string
		required map[string]bool
	}{
		{"no parameters are required", nil},
		{"a DIFFERENT parameter is required", map[string]bool{"somethingElse": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &OASSchemaGenerator{
				generatorConfig: DefaultGeneratorConfig(),
				resourceConfig:  &ResourceConfig{ExcludedSpecFields: []string{"password"}},
			}
			schema := &Schema{
				Type: []string{"object"},
				Properties: []Property{
					{Name: "password", Schema: &Schema{Type: []string{"string"}}},
					{Name: "username", Schema: &Schema{Type: []string{"string"}}},
				},
			}
			warnings := g.removeExcludedSpecFields(schema, tc.required)
			if len(warnings) != 0 {
				t.Errorf("warnings = %v, want none: this is the case excludedSpecFields exists for", warnings)
			}
			if hasProperty(schema, "password") {
				t.Error("the excluded field was NOT pruned; the guard has broken excludedSpecFields")
			}
			if !hasProperty(schema, "username") {
				t.Error("an unrelated property was removed")
			}
		})
	}
}

// The guard consults the set on a TOP-LEVEL name only. requiredParameters is keyed by parameter name and
// a parameter is always a top-level spec property, so an exclusion of `a.b` cannot be naming one —
// comparing them would be matching across two namespaces, and a false match would refuse a legitimate
// nested exclusion.
func TestANestedExclusionIsNotMistakenForAParameter(t *testing.T) {
	g := &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		resourceConfig:  &ResourceConfig{ExcludedSpecFields: []string{"metadata.projectId"}},
	}
	schema := &Schema{
		Type: []string{"object"},
		Properties: []Property{{Name: "metadata", Schema: &Schema{
			Type:       []string{"object"},
			Properties: []Property{{Name: "projectId", Schema: &Schema{Type: []string{"string"}}}},
		}}},
	}

	warnings := g.removeExcludedSpecFields(schema, map[string]bool{"projectId": true})

	if len(warnings) != 0 {
		t.Errorf("warnings = %v. A nested `metadata.projectId` shares only its LAST segment with the "+
			"required parameter `projectId`; refusing it would block a legitimate exclusion on a name "+
			"collision between two different namespaces", warnings)
	}
	meta := propertyNamed(schema, "metadata")
	if meta == nil {
		t.Fatal("metadata was removed entirely")
	}
	if hasProperty(meta, "projectId") {
		t.Error("the nested field was not pruned, so the guard is over-matching")
	}
}

func hasProperty(s *Schema, name string) bool { return propertyNamed(s, name) != nil }

func propertyNamed(s *Schema, name string) *Schema {
	if s == nil {
		return nil
	}
	for i := range s.Properties {
		if s.Properties[i].Name == name {
			return s.Properties[i].Schema
		}
	}
	return nil
}
