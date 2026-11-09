//go:build unit

package restResources

import (
	"context"
	"strings"
	"testing"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/fieldmapping"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// THE RESOLVER-LEVEL GUARD IS EVALUATED IN TWO PLACES, AND THEY MUST AGREE (#524).
//
//	neededLookupNames          decides whether the vendor call is MADE
//	ResolveRequestResolvers    decides whether the mapping is RESOLVED
//
// They hold separate copies of the predicate, because one is unexported in another package. A reviewer
// named this shape precisely: two copies that must agree are the classic place for a surviving mutant --
// a test that drives only one copy passes while the other diverges.
//
// So this table drives BOTH from the same rows and asserts they reach the same verdict. A mutant that
// changes one copy and not the other must fail here.
//
// The divergence is not symmetric, which is why both directions matter:
//
//	selection says RESOLVE, resolver says SKIP   a vendor call is made and its result discarded -- and
//	                                             if that call FAILS, the reconcile fails for a mapping
//	                                             the definition says does not apply. This was the live
//	                                             bug before the fix.
//	selection says SKIP, resolver says RESOLVE   the lookup never runs, so the resolver finds no value
//	                                             and the request goes out missing a field the definition
//	                                             requires -- the #117 silent-wrong-request class.
func TestBothGuardCallSitesAgree(t *testing.T) {
	const lookupName = "projectByName"

	mappingWith := func(guard string) []getter.FieldMappingItem {
		var when *getter.ResolverWhen
		if guard != "" {
			when = &getter.ResolverWhen{CustomResourceFieldPresent: guard}
		}
		return []getter.FieldMappingItem{{
			InBody: "projectId",
			Resolver: &getter.FieldResolver{
				Type:            "dependentLookup",
				When:            when,
				DependentLookup: &getter.DependentLookupRef{Name: lookupName},
			},
		}}
	}
	lookups := []getter.DependentLookup{{Name: lookupName}}

	cases := []struct {
		name     string
		guard    string
		spec     map[string]any
		wantSkip bool
	}{
		{"no guard at all", "", map[string]any{"other": "x"}, false},
		{"guard path present", "spec.projectName", map[string]any{"projectName": "p1"}, false},
		{"guard path absent", "spec.projectName", map[string]any{"other": "x"}, true},
		{"guard path explicitly null", "spec.projectName", map[string]any{"projectName": nil}, true},
		{"guard path present but empty string", "spec.projectName", map[string]any{"projectName": ""}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mg := &unstructured.Unstructured{Object: map[string]any{"spec": c.spec}}
			mg.SetNamespace("ns1")
			mg.SetName("r1")
			mapping := mappingWith(c.guard)

			// SITE 1: does the vendor call get made?
			needed, err := neededLookupNames(lookups, mapping, mg)
			if err != nil {
				t.Fatalf("neededLookupNames: %v", err)
			}
			selectionSkipped := len(needed) == 0

			// SITE 2: does the mapping get resolved? With no lookup value supplied, a mapping the
			// resolver does NOT skip must complain about the missing lookup; one it skips must not.
			_, rerr := fieldmapping.ResolveRequestResolvers(context.Background(), nil, mapping, mg, nil)
			resolverSkipped := rerr == nil

			if selectionSkipped != c.wantSkip {
				t.Errorf("neededLookupNames: skipped=%v, want %v", selectionSkipped, c.wantSkip)
			}
			if resolverSkipped != c.wantSkip {
				t.Errorf("ResolveRequestResolvers: skipped=%v, want %v (err=%v)", resolverSkipped, c.wantSkip, rerr)
			}
			if selectionSkipped != resolverSkipped {
				t.Fatalf("THE TWO CALL SITES DISAGREE: selection skipped=%v, resolver skipped=%v. One "+
					"predicate copy has diverged from the other, which means either a vendor call whose "+
					"result is discarded, or a request missing a field the definition requires",
					selectionSkipped, resolverSkipped)
			}
		})
	}
}

// An ambiguous guard path must be an ERROR at BOTH sites, not a skip at one and an error at the other.
func TestBothCallSitesRefuseAnAmbiguousGuardPath(t *testing.T) {
	mapping := []getter.FieldMappingItem{{
		InBody: "projectId",
		Resolver: &getter.FieldResolver{
			Type:            "dependentLookup",
			When:            &getter.ResolverWhen{CustomResourceFieldPresent: "spec.items[?type=a].v"},
			DependentLookup: &getter.DependentLookupRef{Name: "l1"},
		},
	}}
	mg := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"items": []any{
			map[string]any{"type": "a", "v": "1"},
			map[string]any{"type": "a", "v": "2"},
		}},
	}}
	mg.SetNamespace("ns1")
	mg.SetName("r1")

	if _, err := neededLookupNames([]getter.DependentLookup{{Name: "l1"}}, mapping, mg); err == nil {
		t.Error("neededLookupNames must refuse an ambiguous guard path, not silently include or skip the lookup")
	}

	// THE LOOKUP VALUE IS SUPPLIED ON PURPOSE. A reviewer found that this half passed for the wrong
	// reason: with lookupValues nil, ResolveRequestResolvers errors about the MISSING LOOKUP whether or
	// not the guard is ambiguous, so the assertion was vacuous -- it could not distinguish the thing it
	// claimed to test. Supplying the value removes the other reason to fail, so only the guard can error.
	supplied := map[string]interface{}{"l1": "already-resolved"}
	_, err := fieldmapping.ResolveRequestResolvers(context.Background(), nil, mapping, mg, supplied)
	if err == nil {
		t.Fatal("ResolveRequestResolvers must refuse an ambiguous guard path even when the lookup value " +
			"is already available: the guard is evaluated before anything else")
	}
	if !strings.Contains(err.Error(), "when.customResourceFieldPresent") {
		t.Fatalf("the error must name the GUARD, not something incidental; got: %v", err)
	}

	// AND THE CONTROL: the same mapping with an UNAMBIGUOUS guard and the value supplied must succeed.
	// Without it, the assertion above would pass against a resolver that refused everything.
	okMapping := []getter.FieldMappingItem{{
		InBody: "projectId",
		Resolver: &getter.FieldResolver{
			Type:            "dependentLookup",
			When:            &getter.ResolverWhen{CustomResourceFieldPresent: "spec.items"},
			DependentLookup: &getter.DependentLookupRef{Name: "l1"},
		},
	}}
	if _, err := fieldmapping.ResolveRequestResolvers(context.Background(), nil, okMapping, mg, supplied); err != nil {
		t.Fatalf("an unambiguous guard with the value supplied must resolve cleanly; got: %v", err)
	}
}
