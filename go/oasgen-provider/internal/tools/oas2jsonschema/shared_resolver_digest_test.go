package oas2jsonschema

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE DECLARING HALF OF THE #598 CROSS-MODULE CONTRACT.
//
// This package DECLARES status.resolvedResolversDigest on the generated CRD;
// rest-dynamic-controller WRITES and COMPARES it. Separate Go modules in this one repository, both
// predicates under internal/, so they cannot share a definition -- they share
// `testdata/resolver-digest-contract.json` at the repo root, and each module's suite asserts its own
// side against every row.
//
// TWO THINGS CAN DRIFT, AND BOTH ARE SILENT:
//
//	THE NAME. If the two disagree, the controller writes a key this CRD does not declare. The
//	apiserver prunes it, the write exits 0, and the rotation is never detected -- issue 598's own
//	defect arriving through its fix. Nothing anywhere goes red.
//
//	THE SCOPE RULE. If this side declares for more verbs than the controller writes for, kinds carry
//	a permanently empty field. If the controller writes for more than this side declares, a
//	create-only secretRef reports a difference no update can retire: one update per poll, per CR,
//	forever.
//
// A "keep these in sync" comment would not catch either. This does, and it names the row.
const sharedResolverDigestContract = "../../../../../testdata/resolver-digest-contract.json"

type resolverDigestContract struct {
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

func loadResolverDigestContract(t *testing.T) resolverDigestContract {
	t.Helper()
	b, err := os.ReadFile(sharedResolverDigestContract)
	require.NoError(t, err,
		"the shared resolver-digest contract is missing. It is the only thing keeping this "+
			"declaration and rest-dynamic-controller's write from disagreeing about the field name "+
			"or the scope rule, so a missing file is a failure, not a skip")

	var c resolverDigestContract
	require.NoError(t, json.Unmarshal(b, &c))
	require.NotEmpty(t, c.Cases, "the contract is empty, so every assertion over it is vacuous")
	return c
}

// The NAME half. A constant compared against a literal in a file both modules read.
func TestTheDeclaredFieldNameMatchesTheSharedContract(t *testing.T) {
	c := loadResolverDigestContract(t)
	require.NotEmpty(t, c.StatusField, "the contract declares no statusField, so this comparison is vacuous")
	assert.Equal(t, c.StatusField, StatusResolvedResolversDigest,
		"this module declares status.%q but the shared contract says %q. The controller writes the "+
			"contract's name, so a mismatch means the apiserver prunes every write and the feature is "+
			"silently inert", StatusResolvedResolversDigest, c.StatusField)
	assert.Equal(t, "update", c.ScopeAction,
		"this suite asserts the gate against the update verb; the contract names %q", c.ScopeAction)
}

// The SCOPE half: this module's gate, against every row.
func TestTheDeclarationGateMatchesTheSharedContract(t *testing.T) {
	c := loadResolverDigestContract(t)

	// Both outcomes must be present, or the table passes against a constant predicate.
	var wantTrue, wantFalse int
	for _, tc := range c.Cases {
		if tc.InScope {
			wantTrue++
		} else {
			wantFalse++
		}
	}
	require.NotZero(t, wantTrue, "no row expects in-scope: this contract passes against a gate that never declares")
	require.NotZero(t, wantFalse, "no row forbids it: this contract passes against a gate that always declares")

	// Pinned so a row added on the OTHER module's behalf, without this suite being re-run, cannot go
	// unnoticed here.
	assert.Equal(t, 11, len(c.Cases),
		"the shared contract has %d rows, not the 11 this suite was written against. A row was added "+
			"or removed -- confirm this gate agrees with it, then update this count deliberately",
		len(c.Cases))

	for _, tc := range c.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			var verbs []Verb
			for _, v := range tc.Verbs {
				verb := Verb{Action: v.Action, Method: "POST", Path: "/v1/nodes"}
				switch {
				case v.SecretRef:
					verb.FieldMapping = []FieldMappingEntry{{
						InBody: "password", ResolverType: c.ResolverType, ResolverHasWhen: v.When,
					}}
				case v.DependentLookup:
					verb.FieldMapping = []FieldMappingEntry{{
						InPath: "userId", ResolverType: "dependentLookup", ResolverLookupName: "l",
					}}
				case v.Plain:
					verb.FieldMapping = []FieldMappingEntry{{
						InBody: "token", InCustomResource: "spec.token",
					}}
				}
				verbs = append(verbs, verb)
			}
			g := matchGen([]string{"uuid"}, verbs, ironicShapedDoc())
			assert.Equal(t, tc.InScope, g.hasUpdatableSecretRefResolver(), tc.Why)
		})
	}
}
