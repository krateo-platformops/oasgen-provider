package oas2jsonschema

import (
	"strings"
	"testing"
)

// specShape returns the shape the guard operates on: a required parameter alongside an ordinary field.
func specShape(required, other string) *Schema {
	return &Schema{
		Type: []string{"object"},
		Properties: []Property{
			{Name: required, Schema: &Schema{Type: []string{"string"}}},
			{Name: other, Schema: &Schema{Type: []string{"string"}}},
		},
	}
}

func hasProp(s *Schema, name string) bool {
	for _, p := range s.Properties {
		if p.Name == name {
			return true
		}
	}
	return false
}

// TestExcludingARequiredParameterTheStatusCarriesIsHonoured is #455.
//
// #410's guard refuses an exclusion naming a required parameter, on the premise that "dropping a required
// parameter leaves a kind that cannot build its own request URL". That premise is FALSE when the status
// schema carries the value: rest-dynamic-controller fills request parameters from spec AND status, spec
// first (builder.go:170-174), so a vendor-assigned id placed in status by additionalStatusFields reaches
// the URL once create or findby has populated it.
//
// The guard already grants exactly this exemption to a request-direction fieldMapping, with the reasoning
// spelled out in addParametersToSpec -- "false once a mapping supplies it, so the protection would be
// guarding nothing". The argument was simply never carried across to status.
//
// This is not hypothetical and not a constructed case: `examples/rdc-sample-resource/restdefinition.yaml`
// ships this exact configuration, its README hands the user the commands, and its resource.yaml is the CR
// that 0.51.0 rejects. Four live openstack-ironic definitions do the same with a vendor-assigned `uuid`,
// one of them with twelve live CRs whose spec.uuid is correctly empty.
func TestExcludingARequiredParameterTheStatusCarriesIsHonoured(t *testing.T) {
	g := &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		resourceConfig: &ResourceConfig{
			ExcludedSpecFields:     []string{"id"},
			AdditionalStatusFields: []string{"id"},
			Identifiers:            []string{"name"},
		},
	}
	schema := specShape("id", "name")

	warnings := g.removeExcludedSpecFields(schema, map[string]bool{"id": true})

	if len(warnings) != 0 {
		t.Fatalf("warnings = %d, want 0: the status carries 'id', so excluding it cannot leave a kind "+
			"unable to build its URL. Got: %v", len(warnings), warnings)
	}
	if hasProp(schema, "id") {
		t.Error("'id' is still in the spec: the exclusion was refused rather than honoured, which is the " +
			"defect -- an author cannot supply a vendor-assigned value and the CR is rejected for omitting it")
	}
	if !hasProp(schema, "name") {
		t.Error("'name' was removed; only the excluded field should go")
	}
}

// An identifier also lands in status -- statusFieldsFor concatenates Identifiers and
// AdditionalStatusFields -- so the same exemption must apply, or the rule would depend on which of two
// stanzas the author used to put the same name in the same place.
func TestExcludingARequiredParameterThatIsAnIdentifierIsHonoured(t *testing.T) {
	g := &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		resourceConfig: &ResourceConfig{
			ExcludedSpecFields: []string{"uuid"},
			Identifiers:        []string{"uuid"},
		},
	}
	schema := specShape("uuid", "address")

	if warnings := g.removeExcludedSpecFields(schema, map[string]bool{"uuid": true}); len(warnings) != 0 {
		t.Fatalf("warnings = %d, want 0 (statusFieldsFor includes Identifiers): %v", len(warnings), warnings)
	}
	if hasProp(schema, "uuid") {
		t.Error("'uuid' survived: the identifier path is not exempt, so the exemption depends on which " +
			"stanza names the field rather than on where the value comes from")
	}
}

// THE CONTROL, and the one that matters most: #410 must stay fixed.
//
// The entire risk of #455's fix is re-opening #410 by exempting too much. A required parameter that
// NOTHING supplies -- not a mapping, not status -- must still be refused, because that kind genuinely
// cannot build its URL. If this arm ever goes green-by-exemption, the fix has gone too far.
func TestExcludingARequiredParameterNothingSuppliesIsStillRefused(t *testing.T) {
	g := &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		resourceConfig: &ResourceConfig{
			ExcludedSpecFields: []string{"projectId"},
			// deliberately NOT in Identifiers or AdditionalStatusFields
			AdditionalStatusFields: []string{"somethingElse"},
			Identifiers:            []string{"name"},
		},
	}
	schema := specShape("projectId", "name")

	warnings := g.removeExcludedSpecFields(schema, map[string]bool{"projectId": true})

	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want 1: #410's refusal must survive this change. Nothing supplies "+
			"'projectId', so the kind cannot build its URL and the exclusion must be refused", len(warnings))
	}
	if !strings.Contains(warnings[0].Error(), "REQUIRED parameter") {
		t.Errorf("warning = %q, want the #410 refusal", warnings[0].Error())
	}
	if !hasProp(schema, "projectId") {
		t.Error("'projectId' was removed: the refusal did not take effect, which is #410 re-opened")
	}
}

// A field in status that is NOT a required parameter was never guarded, and must still be excludable --
// otherwise the exemption would have become a blanket "status fields cannot be excluded".
func TestExcludingANonRequiredFieldTheStatusCarriesStillWorks(t *testing.T) {
	g := &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		resourceConfig: &ResourceConfig{
			ExcludedSpecFields:     []string{"createdAt"},
			AdditionalStatusFields: []string{"createdAt"},
		},
	}
	schema := specShape("createdAt", "name")

	if warnings := g.removeExcludedSpecFields(schema, map[string]bool{}); len(warnings) != 0 {
		t.Fatalf("warnings = %d, want 0: %v", len(warnings), warnings)
	}
	if hasProp(schema, "createdAt") {
		t.Error("'createdAt' survived an exclusion that was never guarded")
	}
}

// The shipped example's full stanza set, as committed, so a regression is caught against the artifact
// rather than against a reduction of it. examples/rdc-sample-resource/restdefinition.yaml excludes five
// fields of which exactly one -- id -- is a required path parameter of get/update/delete.
func TestTheShippedExampleGeneratesWithoutRefusal(t *testing.T) {
	g := &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		resourceConfig: &ResourceConfig{
			ExcludedSpecFields:     []string{"id", "status", "createdAt", "updatedAt", "metadata"},
			AdditionalStatusFields: []string{"id"},
			Identifiers:            []string{"name"},
		},
	}
	schema := &Schema{
		Type: []string{"object"},
		Properties: []Property{
			{Name: "id", Schema: &Schema{Type: []string{"string"}}},
			{Name: "name", Schema: &Schema{Type: []string{"string"}}},
			{Name: "description", Schema: &Schema{Type: []string{"string"}}},
			{Name: "status", Schema: &Schema{Type: []string{"string"}}},
			{Name: "createdAt", Schema: &Schema{Type: []string{"string"}}},
			{Name: "updatedAt", Schema: &Schema{Type: []string{"string"}}},
			{Name: "metadata", Schema: &Schema{Type: []string{"object"}}},
		},
	}

	warnings := g.removeExcludedSpecFields(schema, map[string]bool{"id": true})

	if len(warnings) != 0 {
		t.Fatalf("the shipped example must generate without a refusal; warnings = %v", warnings)
	}
	for _, gone := range []string{"id", "status", "createdAt", "updatedAt", "metadata"} {
		if hasProp(schema, gone) {
			t.Errorf("%q survived its exclusion", gone)
		}
	}
	for _, kept := range []string{"name", "description"} {
		if !hasProp(schema, kept) {
			t.Errorf("%q was removed and should not have been", kept)
		}
	}
}
