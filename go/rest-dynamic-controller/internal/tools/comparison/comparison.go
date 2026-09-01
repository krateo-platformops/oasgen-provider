package comparison

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/google/go-cmp/cmp"
)

type Reason struct {
	Reason      string
	FirstValue  any
	SecondValue any
}

type ComparisonResult struct {
	IsEqual bool
	Reason  *Reason
}

func (r ComparisonResult) String() string {
	if r.IsEqual {
		return "ComparisonResult: IsEqual=true"
	}
	if r.Reason == nil {
		return "ComparisonResult: IsEqual=false, Reason=nil"
	}
	return fmt.Sprintf("ComparisonResult: IsEqual=false, Reason=%s, FirstValue=%v, SecondValue=%v", r.Reason.Reason, r.Reason.FirstValue, r.Reason.SecondValue)
}

// CompareExisting recursively compares fields between two maps and logs differences.
// If a field exists in the first map but not in the second, it is ignored.
// If a field exists in the second map but not in the first, it is ignored.
// If both maps have the same field, it compares their values.
// Slices order is considered, so if the order of elements in slices is different, they are considered unequal.
// If the values are maps or slices, it recursively compares them.
func CompareExisting(mg map[string]interface{}, rm map[string]interface{}, path ...string) (ComparisonResult, error) {
	// Iterate over keys in the first map (mg, representing the CR on the cluster)
	for key, value := range mg {
		currentPath := append(path, key)
		pathStr := fmt.Sprintf("%v", currentPath)

		rmValue, ok := rm[key]
		if !ok {
			// Key does not exist in rm, ignore and continue
			// TODO: to be understood if this is the desired behavior
			// Examples:
			// Key [configurationRef] not found in rm, ignoring and continuing (this is desired, but maybe can be whitelisted)
			continue
		}

		// Handle case where one or both values are nil
		if value == nil || rmValue == nil {
			if value == nil && rmValue == nil {
				continue // Both are nil, considered equal
			}
			// One is nil but the other isn't, so they are not equal.
			return ComparisonResult{
				IsEqual: false,
				Reason: &Reason{
					Reason:      "values differ (one is nil)",
					FirstValue:  value,
					SecondValue: rmValue,
				},
			}, nil
		}

		switch reflect.TypeOf(value).Kind() {
		case reflect.Map:
			mgMap, ok1 := value.(map[string]interface{})
			if !ok1 {
				return ComparisonResult{
					IsEqual: false,
					Reason: &Reason{
						Reason:      "type assertion failed",
						FirstValue:  value,
						SecondValue: rmValue,
					},
				}, fmt.Errorf("type assertion failed for map at %s", pathStr)
			}
			rmMap, ok2 := rmValue.(map[string]interface{})
			if !ok2 {
				return ComparisonResult{
					IsEqual: false,
					Reason: &Reason{
						Reason:      "type assertion failed",
						FirstValue:  value,
						SecondValue: rmValue,
					},
				}, fmt.Errorf("type assertion failed for map at %s", pathStr)
			}
			res, err := CompareExisting(mgMap, rmMap, currentPath...)
			if err != nil {
				return ComparisonResult{
					IsEqual: false,
					Reason: &Reason{
						Reason:      "error comparing maps",
						FirstValue:  value,
						SecondValue: rmValue,
					},
				}, err
			}
			if !res.IsEqual {
				return ComparisonResult{
					IsEqual: false,
					Reason: &Reason{
						Reason:      "values differ",
						FirstValue:  value,
						SecondValue: rmValue,
					},
				}, nil
			}
		case reflect.Slice:
			valueSlice, ok1 := value.([]interface{})
			if !ok1 || reflect.TypeOf(rmValue).Kind() != reflect.Slice {
				return ComparisonResult{
					IsEqual: false,
					Reason: &Reason{
						Reason:      "values are not both slices or type assertion failed",
						FirstValue:  value,
						SecondValue: rmValue,
					},
				}, fmt.Errorf("values are not both slices or type assertion failed at %s", pathStr)
			}
			rmSlice, ok2 := rmValue.([]interface{})
			if !ok2 {
				return ComparisonResult{
					IsEqual: false,
					Reason: &Reason{
						Reason:      "values are not both slices or type assertion failed",
						FirstValue:  value,
						SecondValue: rmValue,
					},
				}, fmt.Errorf("type assertion failed for slice at %s", pathStr)
			}
			res, err := compareSlices(valueSlice, rmSlice, currentPath)
			if err != nil {
				return res, err
			}
			if !res.IsEqual {
				return res, nil
			}
		default:
			// Here we compare primary types (string, bool, numbers)
			ok := CompareAny(value, rmValue)
			if !ok {
				return ComparisonResult{
					IsEqual: false,
					Reason: &Reason{
						Reason:      "values differ",
						FirstValue:  value,
						SecondValue: rmValue,
					},
				}, nil
			}
		}
	}

	return ComparisonResult{IsEqual: true}, nil
}

// compareSlices compares two []interface{} element-by-element and in order. Nested maps recurse into
// CompareExisting and nested slices recurse into compareSlices; primitive elements are compared with the
// same type-normalizing CompareAny used for scalar fields.
//
// Unlike CompareExisting, which deliberately ignores map keys ABSENT from the CR (configurationRef and
// other cluster-only fields must not count as drift), a slice is compared by LENGTH as well as content.
// A key present in the CR holding an explicit [] is not an absent key: it is the user declaring the list
// empty. Treating present-and-empty as "no opinion" removed the only way to express that state, and did
// so while reporting success.
//
// This closes two latent defects in the previous inline implementation:
//   - primitive elements were compared with a raw `v != rmSlice[i]`, so an int64 CR value and the same
//     number decoded as float64 from an API response (or a "true" string vs a bool) drifted forever
//     inside arrays even though scalar fields already normalized them;
//   - a nested slice element panicked, because `!=` on []interface{} is a runtime panic, and a nil
//     element panicked on reflect.TypeOf(nil).Kind().
func compareSlices(valueSlice, rmSlice []interface{}, path []string) (ComparisonResult, error) {
	pathStr := fmt.Sprintf("%v", path)

	// Lengths must match. The previous rule only rejected a CR slice LONGER than the remote one and
	// ignored a longer remote as "extra elements", which made an empty CR slice compare equal to any
	// remote slice at all: 0 > n is false, the range body never runs, and the function returns equal.
	// Emptying a list could therefore never be enforced, and because the walk is positional, neither
	// could reordering against a shorter CR slice -- both reported Ready=True while diverged (#76).
	if len(valueSlice) != len(rmSlice) {
		return ComparisonResult{
			IsEqual: false,
			Reason:  &Reason{Reason: "slice lengths differ", FirstValue: valueSlice, SecondValue: rmSlice},
		}, nil
	}

	for i, v := range valueSlice {
		rmv := rmSlice[i]

		// Nil handling: one nil and one non-nil differ; two nils are equal. Guards the
		// reflect.TypeOf(nil).Kind() panic below.
		if v == nil || rmv == nil {
			if v == nil && rmv == nil {
				continue
			}
			return ComparisonResult{
				IsEqual: false,
				Reason:  &Reason{Reason: "values differ (one is nil)", FirstValue: v, SecondValue: rmv},
			}, nil
		}

		switch reflect.TypeOf(v).Kind() {
		case reflect.Map:
			mgMap, okMg := v.(map[string]interface{})
			if !okMg {
				return ComparisonResult{
					IsEqual: false,
					Reason:  &Reason{Reason: "type assertion failed", FirstValue: v, SecondValue: rmv},
				}, fmt.Errorf("type assertion failed for map at %s", pathStr)
			}
			rmMap, okRm := rmv.(map[string]interface{})
			if !okRm {
				return ComparisonResult{
					IsEqual: false,
					Reason:  &Reason{Reason: "type assertion failed", FirstValue: v, SecondValue: rmv},
				}, fmt.Errorf("type assertion failed for map at %s", pathStr)
			}
			res, err := CompareExisting(mgMap, rmMap, path...)
			if err != nil {
				return ComparisonResult{
					IsEqual: false,
					Reason:  &Reason{Reason: "error comparing maps", FirstValue: v, SecondValue: rmv},
				}, err
			}
			if !res.IsEqual {
				return ComparisonResult{
					IsEqual: false,
					Reason:  &Reason{Reason: "values differ", FirstValue: v, SecondValue: rmv},
				}, nil
			}
		case reflect.Slice:
			nestedV, okv := v.([]interface{})
			if !okv {
				return ComparisonResult{
					IsEqual: false,
					Reason:  &Reason{Reason: "type assertion failed", FirstValue: v, SecondValue: rmv},
				}, fmt.Errorf("type assertion failed for nested slice at %s", pathStr)
			}
			nestedRM, okr := rmv.([]interface{})
			if !okr {
				return ComparisonResult{
					IsEqual: false,
					Reason:  &Reason{Reason: "values are not both slices or type assertion failed", FirstValue: v, SecondValue: rmv},
				}, fmt.Errorf("type assertion failed for nested slice at %s", pathStr)
			}
			res, err := compareSlices(nestedV, nestedRM, path)
			if err != nil {
				return res, err
			}
			if !res.IsEqual {
				return res, nil
			}
		default:
			if !CompareAny(v, rmv) {
				return ComparisonResult{
					IsEqual: false,
					Reason:  &Reason{Reason: "values differ", FirstValue: v, SecondValue: rmv},
				}, nil
			}
		}
	}

	return ComparisonResult{IsEqual: true}, nil
}

func CompareAny(a any, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	strA := fmt.Sprintf("%v", a)
	strB := fmt.Sprintf("%v", b)

	a = InferType(strA)
	b = InferType(strB)

	//log.Printf("Values to compare: '%v' and '%v'\n", a, b)
	//diff := cmp.Diff(a, b)
	//log.Printf("cmp diff:\n%s", diff)

	return cmp.Equal(a, b)
}

// DeepEqual performs a deep comparison between two values.
// This function is currently used in FindBy identifier comparisons (see isInResource in clienttools.go).
// It is suitable for comparing also complex structures like maps and slices.
// For maps (objects), key order does not matter.
// For slices (arrays), element order and content are strictly compared.
// Map and slice comparisons normalize nil values before comparison to avoid discrepancies due to nil entries.
func DeepEqual(a, b interface{}) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	aKind := reflect.TypeOf(a).Kind()
	bKind := reflect.TypeOf(b).Kind()
	// For complex types, a direct recursive comparison is correct and respects
	// the nuances of map and slice comparison.
	if aKind == reflect.Map || aKind == reflect.Slice || bKind == reflect.Map || bKind == reflect.Slice {
		//log.Printf("Using direct comparison for complex types: '%v' and '%v'\n", a, b)
		//diff := cmp.Diff(a, b)
		//log.Printf("cmp diff before normalization:\n%s", diff)
		// TODO: evaluate this to be configurable if needed
		normA := normalizeAny(a)
		normB := normalizeAny(b)
		//log.Printf("Normalized values for complex types: '%v' and '%v'\n", normA, normB)
		//diff = cmp.Diff(normA, normB)
		//log.Printf("cmp diff after normalization:\n%s", diff)
		return cmp.Equal(normA, normB)
	}

	// For primary types (string, bool, numbers), we use a normalization
	// step to handle type discrepancies, such as idifferent numeric types for integers and floats.
	strA := fmt.Sprintf("%v", a)
	strB := fmt.Sprintf("%v", b)

	normA := InferType(strA)
	normB := InferType(strB)

	// DEBUG
	//log.Printf("Comparing normalized values: '%v' and '%v'\n", normA, normB)
	//diff := cmp.Diff(normA, normB)
	//log.Printf("cmp diff:\n%s", diff)

	return cmp.Equal(normA, normB)

}

// Note: forked from plumbing library to solve UUID case and similar cases
// InferType attempts to infer and convert a string value to its most appropriate Go type.
// It supports primitive types (bool, int32, int64, float64, string), as well as
// structured types commonly found in Kubernetes configurations (map[string]any and []any).
// The function first tries to parse the input as JSON. If that fails, it falls back to
// custom parsing logic for booleans, nil/null, integers, and floats.
// If no conversion is possible, the original string is returned.
func InferType(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}

	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()

	var jsonVal any
	if err := decoder.Decode(&jsonVal); err == nil {
		// Check if there's more data after what was decoded
		// This ensures we only accept values where the entire string is valid JSON
		if decoder.More() {
			// There's more data, so this isn't a complete JSON value
			// E.g., UUID that starts with numbers like: "90f9629b-664b-4804-a560-dd79b0c628f8"
			// Decoder will parse "90" as a number and leave the rest which is not desired
			// Instead, we want to treat the whole string as a regular string and so to avoid the partial parsing in the switch below
		} else {
			switch v := jsonVal.(type) {
			case json.Number:
				if i, err := v.Int64(); err == nil {
					if i >= math.MinInt32 && i <= math.MaxInt32 {
						return int32(i)
					}
					return i
				}
				if f, err := v.Float64(); err == nil {
					// A whole-number float in int64 range must normalize to an
					// integer. Large integers (e.g. a byte-sized disk size or
					// memory) are printed by fmt "%v" in exponential form
					// ("2.147483648e+10"), which json.Number.Int64() rejects; without
					// this an int64 CR value and the same number decoded as float64
					// from an API response would compare unequal, causing perpetual
					// spurious drift on every int64 field.
					if f == math.Trunc(f) && f >= math.MinInt64 && f <= math.MaxInt64 {
						if i := int64(f); i >= math.MinInt32 && i <= math.MaxInt32 {
							return int32(i)
						} else {
							return i
						}
					}
					return f
				}
			default:
				return jsonVal
			}
		}
	}

	if strings.EqualFold(value, "true") {
		return true
	}
	if strings.EqualFold(value, "false") {
		return false
	}

	if strings.EqualFold(value, "null") || strings.EqualFold(value, "nil") {
		return nil
	}

	if intVal, err := strconv.ParseInt(value, 10, 64); err == nil {
		if intVal >= math.MinInt32 && intVal <= math.MaxInt32 {
			return int32(intVal)
		}
		return intVal
	}

	if floatVal, err := strconv.ParseFloat(value, 64); err == nil {
		if floatVal == math.Trunc(floatVal) {
			if floatVal >= math.MinInt64 && floatVal <= math.MaxInt64 {
				return int64(floatVal)
			}
		}
		return floatVal
	}

	return value
}

// normalizeMap recursively removes nil values from maps
func normalizeMap(m map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range m {
		if v == nil {
			continue // skip nil values
		}

		switch val := v.(type) {
		case map[string]interface{}:
			normalized := normalizeMap(val)
			if len(normalized) > 0 {
				result[k] = normalized
			}
		case []interface{}:
			normalized := normalizeSlice(val)
			if len(normalized) > 0 {
				result[k] = normalized
			}
		default:
			result[k] = v
		}
	}
	return result
}

// normalizeSlice recursively removes nil values from slices
func normalizeSlice(s []interface{}) []interface{} {
	result := make([]interface{}, 0, len(s))
	for _, item := range s {
		if item == nil {
			continue
		}
		if m, ok := item.(map[string]interface{}); ok {
			normalized := normalizeMap(m)
			if len(normalized) > 0 {
				result = append(result, normalized)
			}
		} else {
			result = append(result, item)
		}
	}
	return result
}

func normalizeAny(value any) any {
	switch v := value.(type) {
	case map[string]interface{}:
		return normalizeMap(v)
	case []interface{}:
		return normalizeSlice(v)
	default:
		return value
	}
}
