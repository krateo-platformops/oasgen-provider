//go:build unit || integration

package restResources

import (
	"encoding/json"
	"os"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE WRITING HALF OF THE #598 CROSS-MODULE CONTRACT.
//
// oasgen-provider DECLARES status.resolvedResolversDigest on the generated CRD; this controller
// WRITES and COMPARES it. Separate Go modules in this one repository, both predicates under
// internal/, so they cannot share a definition -- they share
// `testdata/resolver-digest-contract.json` at the repo root, and each module's suite asserts its own
// side against every row. The declaring half is
// oasgen-provider/internal/tools/oas2jsonschema/shared_resolver_digest_test.go.
//
// TWO THINGS CAN DRIFT, AND BOTH ARE SILENT:
//
//	THE NAME. If the two disagree, this controller writes a key the CRD does not declare. The
//	apiserver prunes it, the write exits 0, and the rotation is never detected -- issue 598's own
//	defect arriving through its fix. Nothing anywhere goes red. (The read-back added in
//	resolver_digest.go catches it at RUNTIME, per resource; this catches it at build time, once.)
//
//	THE SCOPE RULE. If this side writes for more verbs than the other declares, a create-only
//	secretRef reports a difference no update can retire: one vendor write per poll, per CR, forever.
//	If it writes for fewer, kinds carry a permanently empty field.
const sharedResolverDigestContract = "../../../../testdata/resolver-digest-contract.json"

type digestContract struct {
	StatusField  string `json:"statusField"`
	ResolverType string `json:"resolverType"`
	ScopeAction  string `json:"scopeAction"`
	Cases        []struct {
		Name  string `json:"name"`
		Verbs []struct {
			Action          string `json:"action"`
			SecretRef       bool   `json:"secretRef"`
			DependentLookup bool   `json:"dependentLookup"`
			Plain           bool   `json:"plain"`
			When            bool   `json:"when"`
		} `json:"verbs"`
		InScope bool   `json:"inScope"`
		Why     string `json:"why"`
	} `json:"cases"`
}

func loadDigestContract(t *testing.T) digestContract {
	t.Helper()
	b, err := os.ReadFile(sharedResolverDigestContract)
	require.NoError(t, err,
		"the shared resolver-digest contract is missing. It is the only build-time check that this "+
			"controller's field name and scope rule match the generator's, so a missing file is a "+
			"failure, not a skip")
	var c digestContract
	require.NoError(t, json.Unmarshal(b, &c))
	require.NotEmpty(t, c.Cases, "the contract is empty, so every assertion over it is vacuous")
	return c
}

// The NAME half.
func TestTheWrittenFieldNameMatchesTheSharedContract(t *testing.T) {
	c := loadDigestContract(t)
	require.NotEmpty(t, c.StatusField, "the contract declares no statusField, so this comparison is vacuous")
	assert.Equal(t, c.StatusField, statusResolvedResolversDigest,
		"this controller writes status.%q but the shared contract says %q. oasgen-provider declares the "+
			"contract's name, so a mismatch means the apiserver prunes every write this controller makes "+
			"and rotation detection is silently inert", statusResolvedResolversDigest, c.StatusField)
}

// The SCOPE half: this module's selector, against every row.
//
// It asserts on resolverDigestSources, which is the composition the controller actually calls --
// verb selection plus source collection -- rather than on updateScopedSecretRefMapping alone. A row
// is in scope exactly when that composition yields at least one source to fingerprint, because that
// is the condition under which a digest is ever computed or written.
func TestTheWriteScopeMatchesTheSharedContract(t *testing.T) {
	c := loadDigestContract(t)
	require.Equal(t, "update", c.ScopeAction,
		"this suite asserts the update verb's resolvers are the scope; the contract names %q", c.ScopeAction)

	var wantTrue, wantFalse int
	for _, tc := range c.Cases {
		if tc.InScope {
			wantTrue++
		} else {
			wantFalse++
		}
	}
	require.NotZero(t, wantTrue, "no row expects in-scope: this contract passes against a selector that never selects")
	require.NotZero(t, wantFalse, "no row forbids it: it passes against one that always selects")

	// Pinned so a row added on the OTHER module's behalf, without this suite being re-run, cannot go
	// unnoticed here.
	assert.Equal(t, 11, len(c.Cases),
		"the shared contract has %d rows, not the 11 this suite was written against. A row was added or "+
			"removed -- confirm this selector agrees with it, then update this count deliberately", len(c.Cases))

	// Every row's CR carries the ref and the guard field, so a row that comes back out of scope did so
	// because of the VERB or the RESOLVER TYPE -- never because the instance was missing a path.
	cr := crWith(map[string]any{"spec": map[string]any{
		"ref":      map[string]any{"name": "s1", "key": "k1"},
		"enableIt": "yes",
	}})

	for _, tc := range c.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			r := &getter.Resource{}
			for _, v := range tc.Verbs {
				verb := getter.VerbsDescription{Action: v.Action, Method: "POST", Path: "/v1/nodes"}
				switch {
				case v.SecretRef:
					e := secretRefEntry("password", "spec.ref.name", "spec.ref.key")
					if v.When {
						e.Resolver.When = &getter.ResolverWhen{CustomResourceFieldPresent: "spec.enableIt"}
					}
					verb.FieldMapping = []getter.FieldMappingItem{e}
				case v.DependentLookup:
					verb.FieldMapping = []getter.FieldMappingItem{{
						InBody: "userId",
						Resolver: &getter.FieldResolver{
							Type:            "dependentLookup",
							DependentLookup: &getter.DependentLookupRef{Name: "l"},
						},
					}}
				case v.Plain:
					verb.FieldMapping = []getter.FieldMappingItem{{
						InBody: "token", InCustomResource: "spec.token",
					}}
				}
				r.VerbsDescription = append(r.VerbsDescription, verb)
			}

			sources, err := resolverDigestSources(r, cr)
			require.NoError(t, err)
			assert.Equal(t, tc.InScope, len(sources) > 0, tc.Why)
		})
	}
}
