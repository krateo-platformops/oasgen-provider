//go:build unit

package restResources

import (
	"context"
	"encoding/json"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// R1: "THE GUARD SUPPRESSES THE VENDOR CALL" WAS AN UNASSERTED CLAIM.
//
// The commit message and the release log both say the guard prevents the request, not just the mapping.
// Until this test, that rested on neededLookupNames returning an empty list -- and nothing checked that
// runDependentLookups HONOURS the list. A reviewer pointed out that a mutant making the runner ignore
// `needed` passes the entire unit suite, so the claim was never proven at the call itself.
//
// This uses the package's existing recordingClient rather than a new fake, because that type was built for
// exactly this question (#284): "was an external call made?" -- a question no delete-path test here could
// previously ask. It counts requests that PASSED validation and were dialled, which is stricter than a raw
// call count: a request can fail before it is ever attempted, and a test that cannot tell those apart
// passes for the wrong reason.
func TestAGuardedMappingMakesNoVendorCall(t *testing.T) {
	const lookupName = "projectByName"

	info := &getter.Info{URL: "http://vendor.invalid"}
	// Method is set because recordingClient keys its dial count BY HTTP METHOD. My first fixture omitted
	// it, so the count landed under "" while the control summed named methods -- and the control failed,
	// correctly, reporting zero attempts without a guard. That is the control doing its job: without it
	// the guarded test above would have passed because nothing was ever going to be called.
	info.Resource.DependentLookups = []getter.DependentLookup{
		{Name: lookupName, Method: "GET", Path: "/projects", Select: ".id"},
	}

	mapping := []getter.FieldMappingItem{{
		InBody: "projectId",
		Resolver: &getter.FieldResolver{
			Type: "dependentLookup",
			// The guard path is ABSENT on the CR below, so the mapping does not apply.
			When:            &getter.ResolverWhen{CustomResourceFieldPresent: "spec.projectName"},
			DependentLookup: &getter.DependentLookupRef{Name: lookupName},
		},
	}}

	mg := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"other": "x"}}}
	mg.SetNamespace("ns1")
	mg.SetName("r1")

	rec := newRecordingClient()
	h := &handler{logger: logging.NewNopLogger()}

	out, err := h.runDependentLookups(context.Background(), setFor(info, rec), info, mapping, mg)
	require.NoError(t, err, "a guarded-away mapping must not fail the reconcile")
	require.Empty(t, out, "no lookup ran, so no value")

	// THE ASSERTION THE CLAIM ACTUALLY NEEDS.
	total := rec.Dialled("GET") + rec.Dialled("POST") + rec.Dialled("PUT") +
		rec.Dialled("PATCH") + rec.Dialled("DELETE")
	require.Zero(t, total,
		"the guard says this mapping does not apply, so NO request may be dialled. A request made and "+
			"discarded is not merely wasteful: if it fails -- a vendor 404, an auth refusal, a timeout -- "+
			"the reconcile fails for a mapping the definition says does not apply")
	require.Zero(t, rec.Rejected(),
		"and none may be attempted-then-rejected either; the call must not be reached at all")
}

// AND THE CASE THAT ISOLATES THE LOOP'S CHECK, which the test above cannot reach.
//
// runDependentLookups has TWO independent suppressors: `if len(needed) == 0` returns before the loop, and
// the loop itself skips any lookup not in `needed`. With a single guarded lookup, `needed` is empty and the
// EARLY RETURN does the work -- so a mutant disabling the loop's check survives, which is exactly what a
// reviewer's mutant on that line showed.
//
// Two lookups, ONE guarded, makes `needed` non-empty. The early return no longer fires, so the loop's check
// is the only thing that can suppress the guarded one. Defence in depth is good; a test that cannot tell
// which layer is holding is not.
func TestWithTwoLookupsOnlyTheGuardedOneIsNotCalled(t *testing.T) {
	info := &getter.Info{URL: "http://vendor.invalid"}
	info.Resource.DependentLookups = []getter.DependentLookup{
		{Name: "guarded", Method: "GET", Path: "/guarded", Select: ".id"},
		{Name: "plain", Method: "POST", Path: "/plain", Select: ".id"},
	}

	mapping := []getter.FieldMappingItem{
		{
			InBody: "guardedId",
			Resolver: &getter.FieldResolver{
				Type:            "dependentLookup",
				When:            &getter.ResolverWhen{CustomResourceFieldPresent: "spec.absent"},
				DependentLookup: &getter.DependentLookupRef{Name: "guarded"},
			},
		},
		{
			InBody: "plainId",
			Resolver: &getter.FieldResolver{
				Type:            "dependentLookup",
				DependentLookup: &getter.DependentLookupRef{Name: "plain"},
			},
		},
	}

	mg := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"other": "x"}}}
	mg.SetNamespace("ns1")
	mg.SetName("r1")

	rec := newRecordingClient()
	h := &handler{logger: logging.NewNopLogger()}
	_, _ = h.runDependentLookups(context.Background(), setFor(info, rec), info, mapping, mg)

	// DIFFERENT METHODS so the two lookups are distinguishable in the dial counts. Same method would make
	// "one call happened" ambiguous about WHICH, and that ambiguity is the whole point of this case.
	require.Zero(t, rec.Dialled("GET"),
		"the GUARDED lookup must not be dialled even though another lookup in the same pass is needed -- "+
			"if this is non-zero, only the len(needed)==0 early return was ever protecting it")
	require.NotZero(t, rec.Dialled("POST")+rec.Rejected(),
		"and the UNGUARDED one must still be attempted, or this test proves nothing about selectivity")
}

// THE CONTROL. Without a guard, the same lookup IS dialled. Otherwise the assertion above would pass
// against a runner that never calls anything -- which is the shape that makes a zero meaningless.
func TestWithoutAGuardTheVendorCallIsMade(t *testing.T) {
	const lookupName = "projectByName"

	info := &getter.Info{URL: "http://vendor.invalid"}
	// Method is set because recordingClient keys its dial count BY HTTP METHOD. My first fixture omitted
	// it, so the count landed under "" while the control summed named methods -- and the control failed,
	// correctly, reporting zero attempts without a guard. That is the control doing its job: without it
	// the guarded test above would have passed because nothing was ever going to be called.
	info.Resource.DependentLookups = []getter.DependentLookup{
		{Name: lookupName, Method: "GET", Path: "/projects", Select: ".id"},
	}

	mapping := []getter.FieldMappingItem{{
		InBody: "projectId",
		Resolver: &getter.FieldResolver{
			Type:            "dependentLookup",
			DependentLookup: &getter.DependentLookupRef{Name: lookupName},
		},
	}}

	mg := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"other": "x"}}}
	mg.SetNamespace("ns1")
	mg.SetName("r1")

	rec := newRecordingClient()
	h := &handler{logger: logging.NewNopLogger()}

	// The call may well error -- the fake returns whatever it returns, and the lookup may not resolve.
	// What matters is that it was ATTEMPTED.
	_, _ = h.runDependentLookups(context.Background(), setFor(info, rec), info, mapping, mg)

	attempted := rec.Dialled("GET") + rec.Rejected()
	require.NotZero(t, attempted,
		"with no guard the lookup must be attempted -- if this is zero, the test above proves nothing, "+
			"because a runner that calls nothing would satisfy it")
}

// M4: THE GUARD IS INERT IF THE JSON TAG IS WRONG, AND NOTHING TESTED THE TAG.
//
// rest-dynamic-controller reads a RestDefinition as JSON through its own parallel structs. If the `when`
// field's tag were dropped or renamed, the guard would never populate from the stored object: every
// definition would behave as unguarded, with no error anywhere -- the CRD would still accept the field and
// `kubectl get` would still show it.
//
// A reviewer's mutant for this is `json:"-"` on the field, and it survived the whole suite.
func TestTheResolverWhenFieldRoundTripsThroughJSON(t *testing.T) {
	// Exactly the shape the API server stores, written as a literal rather than marshalled from the Go
	// struct -- marshalling and unmarshalling with the same tag would agree even if the tag were wrong.
	stored := []byte(`{
	  "type": "secretRef",
	  "when": { "customResourceFieldPresent": "spec.credentials[?type=otp]" },
	  "secretRef": {
	    "nameFromCustomResource": "spec.credentials[?type=otp].secretDataSecretRef.name",
	    "keyFromCustomResource": "spec.credentials[?type=otp].secretDataSecretRef.key"
	  }
	}`)

	var r getter.FieldResolver
	require.NoError(t, json.Unmarshal(stored, &r))

	require.Equal(t, "secretRef", r.Type)
	require.NotNil(t, r.When,
		"the guard must populate from the STORED json. If this is nil the field's tag does not match what "+
			"the API server stores, and every guarded definition silently behaves as unguarded")
	require.Equal(t, "spec.credentials[?type=otp]", r.When.CustomResourceFieldPresent)
	require.NotNil(t, r.SecretRef, "and the sibling must still populate, so the test cannot pass by reading nothing")
}
