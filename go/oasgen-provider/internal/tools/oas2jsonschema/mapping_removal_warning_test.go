package oas2jsonschema

import (
	"strings"
	"testing"
)

// #409: since 0.43.0 a parameter supplied by a request-direction fieldMapping is omitted from the
// generated spec ENTIRELY, and the removal emitted nothing.
//
// The asymmetry was backwards. `excludedSpecFields` warns when it matches nothing — a stanza doing no
// work is loud. A mapping removing a property an author may have been setting was silent, and because the
// generated spec sets neither `additionalProperties: false` nor `x-kubernetes-preserve-unknown-fields`,
// the API server PRUNES rather than rejects: an existing CR keeps working with the value dropped,
// `kubectl apply` succeeds, and `get -o yaml` stops showing it. The case that changes what a CR MEANS was
// the quiet one.
//
// The removal itself is correct and is not changed here — by #327's rationale no request changes, since
// the request already took the mapped value.
func TestAMappingRemovedParameterIsReported(t *testing.T) {
	warnings := mappingSuppliedWarnings(t, []Verb{
		{
			Action: "get",
			Path:   "/v1/nodes/{uuid}",
			FieldMapping: []FieldMappingEntry{
				{InPath: "uuid", InCustomResource: "status.uuid"},
			},
		},
	}, "uuid")

	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want 1. A property leaving the generated schema with no signal is how "+
			"#409 went eight releases without being noticed", len(warnings))
	}
	msg := warnings[0].Error()

	// NAMED BY THE MAPPING, which is the whole point: a message saying a field was removed and not by
	// WHAT sends the author to read every verb's fieldMapping to find out.
	for _, want := range []string{
		`"uuid"`,        // the property
		`"get"`,         // the verb whose mapping removed it
		"inPath",        // where the mapping addressed it
		`"status.uuid"`, // and the CR path the value now comes from
		"pruned by the API server rather than rejected", // the consequence an operator will meet
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning = %q, want it to contain %q", msg, want)
		}
	}
}

// ONCE PER PARAMETER, not once per verb declaring it. Three verbs mapping the same parameter is the
// ordinary shape — get, update and delete on `/{uuid}` — and three identical warnings for one property
// is noise an operator learns to scroll past, which is the failure mode this fix exists to avoid.
func TestTheRemovalIsReportedOncePerParameterNotOncePerVerb(t *testing.T) {
	warnings := mappingSuppliedWarnings(t, []Verb{
		{Action: "get", Path: "/v1/nodes/{uuid}", FieldMapping: []FieldMappingEntry{{InPath: "uuid", InCustomResource: "status.uuid"}}},
		{Action: "update", Path: "/v1/nodes/{uuid}", FieldMapping: []FieldMappingEntry{{InPath: "uuid", InCustomResource: "status.uuid"}}},
		{Action: "delete", Path: "/v1/nodes/{uuid}", FieldMapping: []FieldMappingEntry{{InPath: "uuid", InCustomResource: "status.uuid"}}},
	}, "uuid")

	if len(warnings) != 1 {
		t.Errorf("warnings = %d, want exactly 1 for one property across three verbs: %v", len(warnings), warnings)
	}
	// And the attribution is the FIRST declaration, so it is stable across renders rather than depending
	// on which verb verbsDescription happens to list last.
	if len(warnings) > 0 && !strings.Contains(warnings[0].Error(), `"get"`) {
		t.Errorf("warning = %q, want it attributed to the FIRST declaring verb", warnings[0].Error())
	}
}

// A parameter with NO mapping must produce no warning, or the signal fires for every ordinary parameter
// and means nothing. An always-firing warning is indistinguishable from none.
func TestAnUnmappedParameterIsNotReported(t *testing.T) {
	warnings := mappingSuppliedWarnings(t, []Verb{
		{Action: "get", Path: "/v1/nodes/{uuid}"},
	}, "uuid")
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none: this parameter IS the author's to set", warnings)
	}
}

// A RESPONSE-direction mapping does not supply a request parameter, so it must not suppress one and must
// not warn. An entry with no `inCustomResource` source is response-direction or malformed.
func TestAResponseDirectionMappingDoesNotReportARemoval(t *testing.T) {
	warnings := mappingSuppliedWarnings(t, []Verb{
		{Action: "get", Path: "/v1/nodes/{uuid}", FieldMapping: []FieldMappingEntry{{InPath: "uuid"}}},
	}, "uuid")
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none: an entry with no inCustomResource supplies nothing", warnings)
	}
}

// mappingSuppliedWarnings exercises mappingSuppliedParams and reports which of `names` it classified as
// mapping-supplied, with the message that would be emitted. Kept at this level deliberately: driving the
// whole of addParametersToSpec would need a parsed document per case, and the classification plus its
// provenance is the thing under test.
func mappingSuppliedWarnings(t *testing.T, verbs []Verb, names ...string) []error {
	t.Helper()
	supplied := mappingSuppliedParams(verbs)
	var out []error
	for _, name := range names {
		// anySupplier, not suppliesFor: this helper tests CLASSIFICATION and provenance, which is a
		// question about the resource. Per-verb scoping is tested where it matters, at the exemption.
		src, mapped := supplied.anySupplier(name)
		if !mapped {
			continue
		}
		out = append(out, SchemaGenerationError{
			Code:    CodeFieldNotFound,
			Message: mappingRemovalMessage(name, src),
		})
	}
	return out
}
