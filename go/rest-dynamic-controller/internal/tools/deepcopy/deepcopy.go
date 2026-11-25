package deepcopy

import (
	"fmt"
	"math"
)

// Note: forked from plumbing/maps/deepcopy.go
// modified the float handling
//
// A WHOLE-NUMBER FLOAT BECOMES int64; A FRACTION STAYS float64 (issue 927). Before this guard every
// float64 in int64 range was coerced, so 90.5 became 90 and 0.1 became 0 -- a fraction below one was
// DISCARDED rather than merely rounded. It has two callers with opposite consequences:
//
//	helpers.go projectSpecFields     builds the map the comparison runs against, so a coerced value
//	                                 guarantees a mismatch against the vendor's 90.5: drift on every
//	                                 reconcile, which no update can resolve
//	helpers.go populateStatusFields  writes vendor fields into the CR's status, so a coerced value
//	                                 stores 90 where the vendor said 90.5
//
// THE UNGUARDED VERSION WAS CORRECT WHEN IT WAS WRITTEN, which is why its call site calls the
// conversion out as intentional. oasgen's converter used to narrow every OAS `number` to `integer`,
// INCLUDING in status schemas, so a float64 reaching status would have been refused at admission.
// Issue 922 removed that narrowing in 0.93.0: status now keeps `number`, the coercion is no longer
// required, and the same change made the projectSpecFields defect reachable for spec fields, because
// until then a fraction could not be stored in a CR at all. A remedy outlived its precondition.
//
// int, int32 AND WHOLE FLOATS STILL MAP TO int64 deliberately -- that is the JSON contract
// apimachinery's unstructured values follow, and comparison.go's own numeric normalisation guards the
// identical conversion with the identical `== math.Trunc` test. Two implementations of one idea
// existed here; this one was missing the guard.
func DeepCopyJSONValue(x any) any {
	switch x := x.(type) {
	case map[string]any:
		if x == nil {
			// Typed nil - an any that contains a type map[string]any with a value of nil
			return x
		}
		clone := make(map[string]any, len(x))
		for k, v := range x {
			clone[k] = DeepCopyJSONValue(v)
		}
		return clone
	case []any:
		if x == nil {
			// Typed nil - an any that contains a type []any with a value of nil
			return x
		}
		clone := make([]any, len(x))
		for i, v := range x {
			clone[i] = DeepCopyJSONValue(v)
		}
		return clone
	case []map[string]any:
		if x == nil {
			return x
		}
		clone := make([]any, len(x))
		for i, v := range x {
			clone[i] = DeepCopyJSONValue(v)
		}
		return clone
	case string, int64, bool, nil:
		return x
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case float32:
		// A WHOLE float becomes int64; a FRACTION is preserved as float64. See the block comment on
		// this function for why the guard exists and why the unguarded version was once correct.
		if f := float64(x); f == math.Trunc(f) && f >= math.MinInt64 && f <= math.MaxInt64 {
			return int64(f)
		}
		return float64(x)
	case float64:
		if x == math.Trunc(x) && x >= math.MinInt64 && x <= math.MaxInt64 {
			return int64(x)
		}
		return x
	default:
		return fmt.Sprintf("%v", x)
	}
	return fmt.Sprintf("%v", x) // Fallback for unsupported types
}
