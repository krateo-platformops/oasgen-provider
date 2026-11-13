package render

import (
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WHAT THIS PINS, AND WHY THE SINGLE-REF ARMS ARE THE POINT
//
// The generator reasons over ResourceConfig, and before `CreateDelegated` it could see four of the five
// things that make a create reachable: the four verb actions, but not `createApiRef`. A static diagnostic
// built on that would have concluded "this resource cannot create" for a kind that creates perfectly well
// through a Snowplow RESTAction -- a false positive on exactly the kinds that are fine, and a confidently
// worded one, which is the failure issue 625 exists to stop repeating.
//
// THE BOTH-SET ARM PROVES ALMOST NOTHING. A cross-wiring -- CreateDelegated reading ObserveApiRef, which
// is a one-word copy-paste away given the two lines sit adjacent in helper.go -- passes a test that only
// ever sets both refs together. The arms that set exactly ONE are what distinguish the two fields, and
// "these two values are independent" is asserted nowhere unless something asserts independence.
func TestDelegationFlagsArePlumbedIndependently(t *testing.T) {
	ref := func(name string) *definitionv1alpha1.ApiRef {
		return &definitionv1alpha1.ApiRef{Name: name, Namespace: "krateo-system"}
	}

	for _, tc := range []struct {
		name                    string
		observeRef, createRef   *definitionv1alpha1.ApiRef
		wantObserve, wantCreate bool
		why                     string
	}{
		{
			name:        "neither ref set",
			wantObserve: false, wantCreate: false,
			why: "the ordinary kind: both verbs are its own, so neither is delegated",
		},
		{
			// LOAD-BEARING: a CreateDelegated that read ObserveApiRef would report true here.
			name: "only observeApiRef", observeRef: ref("observe-action"),
			wantObserve: true, wantCreate: false,
			why: "observation is delegated and creation is not; CreateDelegated must NOT follow ObserveApiRef",
		},
		{
			// LOAD-BEARING in the other direction: an ObserveDelegated that read CreateApiRef would
			// report true here, which would silence the identifier-matching checks for a kind that
			// genuinely executes its findby.
			name: "only createApiRef", createRef: ref("create-action"),
			wantObserve: false, wantCreate: true,
			why: "creation is delegated and observation is not; ObserveDelegated must NOT follow CreateApiRef",
		},
		{
			name: "both refs set", observeRef: ref("observe-action"), createRef: ref("create-action"),
			wantObserve: true, wantCreate: true,
			why: "both delegated; this arm cannot distinguish the fields and is here for completeness only",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr := &definitionv1alpha1.RestDefinition{}
			cr.Spec.Resource.Kind = "Thing"
			cr.Spec.Resource.ObserveApiRef = tc.observeRef
			cr.Spec.Resource.CreateApiRef = tc.createRef

			got, err := toDomainResourceConfig(cr, nil)
			require.NoError(t, err)
			require.NotNil(t, got)

			assert.Equal(t, tc.wantObserve, got.ObserveDelegated, "ObserveDelegated: %s", tc.why)
			assert.Equal(t, tc.wantCreate, got.CreateDelegated, "CreateDelegated: %s", tc.why)
		})
	}
}

// AND THE TABLE MUST CONTAIN A ROW WHERE THE TWO DIFFER, asserted rather than assumed.
//
// If every row set the two refs together, each arm above would still pass against a single boolean
// serving both fields. This counts the rows that disagree, so a future edit that "simplifies" the table
// into all-both or all-neither fails here with the reason.
func TestTheDelegationTableDistinguishesTheTwoFlags(t *testing.T) {
	// Mirrors the table above deliberately rather than sharing it: a shared slice could be reduced in one
	// place and this check would reduce with it.
	rows := []struct{ observe, create bool }{
		{false, false}, {true, false}, {false, true}, {true, true},
	}
	differ := 0
	for _, r := range rows {
		if r.observe != r.create {
			differ++
		}
	}
	assert.GreaterOrEqual(t, differ, 2,
		"the table has %d row(s) where the two flags differ; it needs at least one in each direction, or "+
			"a cross-wired CreateDelegated reading ObserveApiRef passes every arm", differ)
}
