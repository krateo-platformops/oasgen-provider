package oas2jsonschema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateDependentLookups covers the two checks that moved out of CRD validation because they could
// not be expressed as CEL inside the apiserver's rule cost budget (#169). The CEL admission test
// (apis/restdefinitions/v1alpha1/dependent_lookups_cel_test.go) points here for exactly these cases.
//
// A nil document is deliberate: this check is pure declaration coherence and must not need a document to
// run. Validating that a lookup's PATH exists belongs with the per-lookup document plumbing, which the
// controller half needs anyway.
func TestValidateDependentLookups(t *testing.T) {
	lookupResolver := func(name string) Verb {
		return Verb{
			Action: "create", Method: "POST", Path: "/x",
			FieldMapping: []FieldMappingEntry{{InBody: "sha", ResolverLookupName: name}},
		}
	}

	cases := []struct {
		name      string
		rc        *ResourceConfig
		wantErr   bool
		wantInMsg string
	}{
		{
			name: "resolver names a declared lookup",
			rc: &ResourceConfig{
				Verbs:            []Verb{lookupResolver("baseSha")},
				DependentLookups: []DependentLookup{{Name: "defaultBranch"}, {Name: "baseSha"}},
			},
		},
		{
			name: "no resolvers and no lookups",
			rc: &ResourceConfig{
				Verbs: []Verb{{Action: "create", Method: "POST", Path: "/x"}},
			},
		},
		{
			name: "a fieldMapping entry with no resolver is ignored",
			rc: &ResourceConfig{
				Verbs: []Verb{{
					Action: "create", Method: "POST", Path: "/x",
					FieldMapping: []FieldMappingEntry{{InBody: "token", InCustomResource: "spec.token"}},
				}},
			},
		},
		{
			name: "resolver names an undeclared lookup",
			rc: &ResourceConfig{
				Verbs:            []Verb{lookupResolver("baseSha")},
				DependentLookups: []DependentLookup{{Name: "defaultBranch"}},
			},
			wantErr:   true,
			wantInMsg: "not a declared dependentLookups entry",
		},
		{
			name: "resolver with no lookups declared at all",
			rc: &ResourceConfig{
				Verbs: []Verb{lookupResolver("baseSha")},
			},
			wantErr:   true,
			wantInMsg: "declares no dependentLookups",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := NewOASSchemaGenerator(nil, DefaultGeneratorConfig(), tc.rc).ValidateDependentLookups()
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantInMsg)
			// The message has to name what was wrong, not just that something was: the caller turns this
			// into a field path and the author has to be able to tell which resolver to fix.
			require.Contains(t, err.Error(), "baseSha")
			require.Contains(t, err.Error(), "create")
		})
	}
}
