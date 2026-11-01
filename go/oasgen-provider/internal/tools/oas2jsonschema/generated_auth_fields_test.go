package oas2jsonschema

import (
	"reflect"
	"sort"
	"testing"
)

// The generated authentication field set is DERIVED FROM GO STRUCTS by reflection:
//
//	createSchemaForSecurityScheme -> reflectSchema(reflect.TypeOf(APIKeyAuth{}))
//
// Which means renaming a field, or editing a json tag, silently changes the generated Configuration CRD.
// Nothing asserted that set (#319), so a dropped or renamed field would ship green: the existing apiKey
// tests all pin the INPUT side -- the scheme kind, rejection of a bad `in`, the parsed scheme -- and none
// of them look at what comes out.
//
// This path is worth more care than its size suggests. It is where a credential leak already happened: the
// window where `connector-verbose` wrote credential headers to pod logs in cleartext, and for an
// X-Auth-Token-style scheme the credential IS a header. The field that silently disappears may be the one
// an operator relies on to keep a secret out of a log.
//
// Asserted at the TOP LEVEL only, deliberately. SecretKeySelector's own shape belongs to provider-runtime;
// pinning its internals here would make an upstream refactor fail in this repo with a confusing message,
// and the thing this repo owns is which credential fields it asks for -- not how a secret reference is
// spelled.
// Worth recording: this test failed on its first run and the EXPECTATION was wrong, not the code. I wrote
// the apiKey set as {header, tokenRef} after reading the struct's first two fields and stopping before
// `valuePrefix` -- which is the field that carries wire framing ("Bearer " including the trailing space)
// for APIs declaring apiKey with an Authorization header. Omitting it from a credential CRD would be a
// silent downgrade to a worse failure than a missing field: the wire value becomes "Bearerxyz", which
// fails identically to a wrong credential.
//
// That is the case for a test over a careful reading. A reflected field set has no diff to review.
func TestGeneratedAuthFieldSets(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  reflect.Type
		want []string
	}{
		{"apiKey in header", reflect.TypeOf(APIKeyAuth{}), []string{"header", "tokenRef", "valuePrefix"}},
		{"http bearer", reflect.TypeOf(BearerAuth{}), []string{"tokenRef"}},
		{"http basic", reflect.TypeOf(BasicAuth{}), []string{"passwordRef", "usernameRef"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sch, err := reflectSchema(tc.typ)
			if err != nil {
				t.Fatalf("reflectSchema(%s): %v", tc.typ, err)
			}

			got := make([]string, 0, len(sch.Properties))
			for _, p := range sch.Properties {
				got = append(got, p.Name)
			}
			sort.Strings(got)

			if len(got) != len(tc.want) {
				t.Fatalf("field set = %v, want %v.\n\n"+
					"This set lands in the generated Configuration CRD. A field removed here disappears "+
					"from every CR that uses this auth method, and an added one appears unannounced -- "+
					"neither is visible in a diff of this test's subject, because the set is derived from "+
					"a Go struct by reflection rather than written down anywhere", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("field[%d] = %q, want %q. A RENAME is the dangerous case: existing CRs keep "+
						"the old spelling, the new CRD does not accept it, and the symptom is a validation "+
						"error on a resource nobody edited", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// And the header default must stay a real, defaultable field rather than becoming a hidden constant.
//
// The struct comment says why: the value the controller will send has to be visible and overridable on the
// CR, because a document can declare a header name that the endpoint does not actually enforce. A test that
// only counted fields would not notice `header` turning into an internal.
func TestAPIKeyHeaderIsADefaultableField(t *testing.T) {
	sch, err := reflectSchema(reflect.TypeOf(APIKeyAuth{}))
	if err != nil {
		t.Fatal(err)
	}

	var header *Property
	for i := range sch.Properties {
		if sch.Properties[i].Name == "header" {
			header = &sch.Properties[i]
			break
		}
	}
	if header == nil {
		t.Fatal(`no "header" property: the credential's header name is no longer settable on the CR, so a ` +
			`document declaring a header the endpoint does not enforce has no recourse but editing the ` +
			`vendor spec`)
	}
	if header.Schema == nil {
		t.Fatal(`the "header" property has no schema, so applyAPIKeyHeaderDefault has nothing to default`)
	}
	if got := header.Schema.Type; len(got) != 1 || got[0] != "string" {
		t.Errorf("header type = %v, want [string]: applyAPIKeyHeaderDefault assigns a string default", got)
	}
}
