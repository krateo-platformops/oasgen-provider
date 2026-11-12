package restResources

import (
	"fmt"

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
func isCRUpdated(mg *unstructured.Unstructured, rm map[string]interface{}, compareScope string, scopeFields []string, setTypedPaths []string) (comparison.ComparisonResult, error) {
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

	// setTypedPaths names the arrays the OAS declares as sets (`uniqueItems: true` with scalar items).
	// Those are compared as multisets; every other array stays positional, and an empty list yields a
	// comparer identical to the historical one (#617).
	return comparison.NewComparer(setTypedPaths).CompareExisting(m, rm)
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

	// Combine identifiers and additionalStatusFields into one list.
	allFields := append(clientInfo.Resource.Identifiers, clientInfo.Resource.AdditionalStatusFields...)
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

// clearStatusFields removes the status field from the Custom Resource.
// This is used during Create and Update operations to ensure no stale fields remain.
func clearStatusFields(mg *unstructured.Unstructured) {
	if mg == nil {
		return
	}

	unstructured.RemoveNestedField(mg.Object, "status")
}
