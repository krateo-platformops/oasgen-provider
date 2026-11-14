package restResources

import (
	"fmt"
	"slices"
	"strings"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/comparison"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/deepcopy"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/pathparsing"
	unstructuredtools "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// CompareScope values for Resource.CompareScope, controlling which spec fields the drift comparison considers.
const (
	// compareScopeFullSpec (the default, also when unset) compares every spec field against the response.
	compareScopeFullSpec = "fullSpec"
	// compareScopeIdentifiersAndStatus compares ONLY the identifiers + additionalStatusFields, ignoring the
	// rest of the spec for drift purposes.
	compareScopeIdentifiersAndStatus = "identifiersAndStatus"
	// compareScopeUpdatable compares only those spec fields the UPDATE verb's request body can actually
	// express. A field the update cannot send is a field the controller cannot fix, so comparing it can only
	// produce an update that changes nothing and a difference that returns on the next reconcile. This sits
	// between fullSpec (which loops on server-assigned or create-only fields) and identifiersAndStatus (which
	// stops comparing almost everything, including drift that IS fixable).
	compareScopeUpdatable = "updatable"
)

// isCRUpdated checks if the CR was updated by comparing the fields in the CR with the response from the API call, if existing cr fields are different from the response, it returns false.
// compareScope selects which spec fields participate: "" / "fullSpec" compares the whole spec;
// "identifiersAndStatus" and "updatable" both restrict the comparison to scopeFields, which the caller has
// populated accordingly (identifiers + additionalStatusFields, or the update body's expressible paths).
// resolverSourcePaths returns the CR-side paths that stripResolverSources removes from a request body
// for this verb, normalised the way the comparison looks them up (#663).
//
// IT MIRRORS stripResolverSources EXACTLY -- the same `Resolver != nil && InCustomResource != ""`
// condition and the same chained TrimPrefix of "spec." then "status." (builder.go:739). The two must
// agree about which paths the request never carried, and the failure of disagreement is silent in both
// directions: a path this function misses is compared against a vendor that never received it, and a
// path it invents stops being compared at all.
func resolverSourcePaths(verb *getter.VerbsDescription) []string {
	if verb == nil {
		return nil
	}
	var out []string
	for _, m := range verb.FieldMapping {
		if m.Resolver == nil || m.InCustomResource == "" {
			continue
		}
		trimmed := strings.TrimPrefix(strings.TrimPrefix(m.InCustomResource, "spec."), "status.")
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func isCRUpdated(mg *unstructured.Unstructured, rm map[string]interface{}, compareScope string, scopeFields []string, cmp *comparison.Comparer) (comparison.ComparisonResult, error) {
	//log.Print("isCRUpdated - starting comparison between mg spec and rm")
	if mg == nil {
		return comparison.ComparisonResult{
			IsEqual: false,
			Reason: &comparison.Reason{
				Reason: "mg is nil",
			},
		}, fmt.Errorf("mg is nil")
	}

	// Extract the "spec" fields from the mg object
	m, err := unstructuredtools.GetFieldsFromUnstructured(mg, "spec")

	if err != nil {
		return comparison.ComparisonResult{
			IsEqual: false,
			Reason: &comparison.Reason{
				Reason: "getting spec fields",
			},
		}, fmt.Errorf("getting spec fields: %w", err)
	}

	// When compareScope restricts drift to the resource's identity + observable-state fields, project the spec
	// down to just those paths before comparing; everything else in the spec is intentionally ignored for drift.
	if compareScope == compareScopeIdentifiersAndStatus || compareScope == compareScopeUpdatable {
		m, err = projectSpecFields(m, scopeFields)
		if err != nil {
			return comparison.ComparisonResult{
				IsEqual: false,
				Reason:  &comparison.Reason{Reason: "projecting compareScope fields"},
			}, fmt.Errorf("projecting compareScope fields: %w", err)
		}
	}

	// THE COMPARER IS ASSEMBLED BY THE CALLER, not here. It needs three unrelated facts -- what the
	// document says about this operation's arrays (#617, #650) and what this controller strips from the
	// request body (#663) -- and only the call site has all of them. Passing them in as parameters
	// instead would have made this a six-argument function whose arguments must be kept in the right
	// order by hand.
	//
	// A nil comparer is the historical behaviour, so every existing caller and test is unaffected.
	if cmp == nil {
		cmp = comparison.NewComparer(nil)
	}
	return cmp.CompareExisting(m, rm)
}

// projectSpecFields returns a new map containing only the given dot-notation paths that are present in spec,
// deep-copied so the projection never aliases the live object. Paths absent from spec are skipped (e.g. a
// server-assigned identifier that only appears in the response contributes nothing to drift). It backs
// compareScope=identifiersAndStatus, restricting the drift comparison to the resource's identity + status fields.
func projectSpecFields(spec map[string]interface{}, paths []string) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	for _, p := range paths {
		pathSegments, err := pathparsing.ParsePath(p)
		if err != nil || len(pathSegments) == 0 {
			continue
		}
		value, found, err := unstructured.NestedFieldNoCopy(spec, pathSegments...)
		if err != nil || !found {
			continue
		}
		if err := unstructured.SetNestedField(out, deepcopy.DeepCopyJSONValue(value), pathSegments...); err != nil {
			return nil, fmt.Errorf("projecting field %q: %w", p, err)
		}
	}
	return out, nil
}

// populateStatusFields populates the status fields in the mg object with the values from the response body of the API call.
// It supports dot notation for nested fields and performs necessary type conversions.
// It uses the identifiers and additionalStatusFields from the clientInfo to determine which fields to populate.
func populateStatusFields(clientInfo *getter.Info, mg *unstructured.Unstructured, body map[string]interface{}) error {
	// Handle nil inputs
	if mg == nil {
		return fmt.Errorf("unstructured object is nil")
	}
	if clientInfo == nil {
		return fmt.Errorf("client info is nil")
	}
	if body == nil {
		return nil // Nothing to populate, but not an error
	}

	// Combine identifiers and additionalStatusFields into one list, in a FRESH slice.
	//
	// `append(Resource.Identifiers, ...)` would write into the identifier slice's backing array
	// whenever it has spare capacity, which after a JSON unmarshal it often does. Measured by
	// openstack-nova-operator-kog across identifier counts:
	//
	//	n   len  cap   aliases?
	//	1    1    1    no
	//	2    2    2    no
	//	3    3    4    YES -- [id0 id1 id2 <additionalStatusField>]
	//	4    4    4    no
	//	5    5    8    YES
	//	6    6    8    YES
	//
	// `len(Resource.Identifiers)` does not change, so nothing reading that slice normally sees the
	// extra element: this is a latent hazard rather than a reproduced bug, and it needs a second
	// consumer re-slicing or appending to the same array to surface. It is fixed anyway because
	// THIS REPO ALREADY DECIDED IT MATTERS -- restResources.go builds the equivalent list the safe
	// way, with a comment saying exactly why. One site documenting a hazard while another does the
	// unguarded form is worse than either alone, because a reader who finds the guarded one
	// reasonably assumes the pattern is applied.
	//
	// WHO COULD REACH THIS, measured rather than assumed. A consumer with 36 generated definitions
	// audited their whole fleet: the maximum identifier count on any kind is ONE, 36 of 36, with a
	// >=1 control proving the walk read something. Their immunity is a property of their GENERATOR
	// -- it emits a single identifier per kind -- not of their vendor.
	//
	// So the growth points at 3, 5 and 6 are unreachable from a generated corpus, and this would
	// have waited for a HAND-WRITTEN definition with three or more identifiers. Composite
	// identifiers are the shape that needs them (role assignments and similar), which is both why
	// nobody has hit it and why someone eventually will.
	// slices.Concat ALWAYS allocates, so the result can never share an array with either input --
	// which is the aliasing guarantee, stated by the function rather than reconstructed from a
	// make-then-append pair. It also avoids computing an allocation size as `len(a)+len(b)`, which
	// CodeQL flags as a possible overflow (go/allocation-size-overflow) and which this file's
	// previous form was flagged for.
	allFields := slices.Concat(clientInfo.Resource.Identifiers, clientInfo.Resource.AdditionalStatusFields)
	// Early return if no fields to populate
	if len(allFields) == 0 {
		return nil
	}

	for _, fieldName := range allFields {
		// Parse the field name into path segments.
		pathSegments, err := pathparsing.ParsePath(fieldName)
		if err != nil || len(pathSegments) == 0 {
			continue
		}

		// Extract the raw value from the response body.
		value, found, err := unstructured.NestedFieldNoCopy(body, pathSegments...)
		if err != nil || !found {
			// An error here means the path was invalid or not found.
			// We can safely continue to the next field.
			continue
		}

		// Perform deep copy and type conversions (e.g., float64 to int64).
		convertedValue := deepcopy.DeepCopyJSONValue(value)

		// The destination path in the status should mirror the source path.
		statusPath := append([]string{"status"}, pathSegments...)
		if err := unstructured.SetNestedField(mg.Object, convertedValue, statusPath...); err != nil {
			return fmt.Errorf("setting nested field '%s' in status: %w", fieldName, err)
		}
	}
	return nil
}

// controllerOwnedStatusKeys are the status keys this controller writes about ITSELF, as opposed to
// values copied out of a vendor response. They must survive a status clear.
//
// ONE LIST, USED BY BOTH SITES. writeObservedStatus already guarded `conditions` and
// `observedGeneration` -- "never let the composed result clobber runtime-managed status fields" --
// while clearStatusFields removed the whole subtree. Two sites disagreeing about the same rule is the
// defect one level up, and it is why this was not found by reading: a reader who finds the guarded
// site reasonably assumes the rule is applied.
var controllerOwnedStatusKeys = []string{
	"conditions",
	"observedGeneration",
	statusResolvedResolversDigest,
}

func isControllerOwnedStatusKey(k string) bool {
	return slices.Contains(controllerOwnedStatusKeys, k)
}

// clearStatusFields drops the RESPONSE-DERIVED status fields of a Custom Resource, so that no stale
// value survives into the next reconcile. Used during Create and Update.
//
// WHAT IT MUST NOT DROP, and why this is not a whole-subtree removal (issue 667). The stated purpose
// is stale identifiers: a `status.id` left over from a previously-deleted external resource deadlocks
// Observe. Removing all of `status` also destroyed three keys the controller owns, with two distinct
// consequences:
//
//   - EVERY condition type except the one the caller re-adds was destroyed. Ready survived only
//     because Observe re-decides it every pass, so the surviving set was "whatever something else
//     happens to rewrite" -- a property nobody declared. ResolverDigestRecorded was simply the first
//     condition whose write is gated on a non-recurring branch (`if bootstrappedDigest`), so it never
//     came back at all. Scoping a fix to that one condition would hold only until the next one.
//
//   - status.resolvedResolversDigest was wiped BEFORE the pending/success split. The success branch
//     re-records it; the PENDING branch deliberately does not, because a pending update has not
//     landed -- so the digest was already gone, the next Observe saw recorded == "" and ADOPTED the
//     rotated fingerprints, and the rotation was retired without ever reaching the vendor.
//
// PRESERVE BY CONSTRUCTION, NOT BY RE-RECORDING. Re-recording the digest on more branches shrinks
// that window; keeping it out of what this function touches closes it. The distinction matters
// because the branch that was missed is the one that only occurs when an update is SLOW -- so the
// rotation was retired by the act of being slow, and slowness is a vendor property, not a code path
// anyone here controls. For a write-only field the only safe behaviour is to preserve the prior
// digest untouched and never re-derive it.
func clearStatusFields(mg *unstructured.Unstructured) {
	if mg == nil {
		return
	}

	status, _, err := unstructured.NestedMap(mg.Object, "status")
	if err != nil || status == nil {
		// Unreadable or absent: there is nothing worth preserving, and the old behaviour is still the
		// right answer for a malformed status.
		unstructured.RemoveNestedField(mg.Object, "status")
		return
	}
	for k := range status {
		if isControllerOwnedStatusKey(k) {
			continue
		}
		delete(status, k)
	}
	if len(status) == 0 {
		// NOTHING SURVIVED, so remove the key rather than leaving `status: {}`.
		//
		// This keeps the pre-667 result byte-identical whenever there was nothing to preserve, which
		// is what TestClearCRStatusFields pins -- and it caught this on the first run. The
		// distinction is not cosmetic in this codebase: CompareExisting reports a CR-populated object
		// against a remote-EMPTY one as drift while treating an ABSENT key as equal
		// (comparison.go:184 vs :223, issue 663). Leaving an empty map where there used to be no key
		// is exactly the absent-versus-empty difference that bug turns on.
		unstructured.RemoveNestedField(mg.Object, "status")
		return
	}
	if serr := unstructured.SetNestedMap(mg.Object, status, "status"); serr != nil {
		// SetNestedMap only fails on a non-map at the path, which NestedMap just ruled out.
		unstructured.RemoveNestedField(mg.Object, "status")
	}
}
