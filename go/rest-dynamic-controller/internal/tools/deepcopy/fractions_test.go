//go:build unit || integration

package deepcopy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFractionsSurviveTheDeepCopy pins issue 927. Every value below was MEASURED against the
// unguarded version first, so each arm is a real regression test rather than a restatement.
func TestFractionsSurviveTheDeepCopy(t *testing.T) {
	t.Run("a fraction stays float64 -- the defect", func(t *testing.T) {
		// Measured before the fix: 90.5 -> int64(90). This is the value a consumer reported as
		// FirstValue=90 (int64) against the vendor's SecondValue=90.5 (float64), drifting on all 8
		// resyncs of a 21-minute run.
		require.Equal(t, 90.5, DeepCopyJSONValue(90.5))
	})

	t.Run("a fraction BELOW ONE is not discarded -- the worst arm", func(t *testing.T) {
		// Measured before the fix: 0.1 -> int64(0). Not a rounding error: the value is gone, and a
		// CR declaring 0.1 would compare equal to one declaring 0.4.
		require.Equal(t, 0.1, DeepCopyJSONValue(0.1))
		require.NotEqual(t, DeepCopyJSONValue(0.1), DeepCopyJSONValue(0.4),
			"if these collide, two different declared values are indistinguishable after the copy")
	})

	t.Run("float32 has the same defect and the same fix", func(t *testing.T) {
		got := DeepCopyJSONValue(float32(90.5))
		require.Equal(t, 90.5, got, "measured before the fix: float32(90.5) -> int64(90)")
		require.IsType(t, float64(0), got, "and it must normalise to float64, not stay float32")
	})

	// THE COERCION THAT MUST SURVIVE. int, int32 and WHOLE floats map to int64 deliberately: that is
	// the JSON contract apimachinery's unstructured values follow, and removing it wholesale would be
	// a different regression. These arms are the reason the fix is a guard and not a deletion.
	t.Run("a WHOLE float still becomes int64", func(t *testing.T) {
		require.Equal(t, int64(720), DeepCopyJSONValue(720.0))
		require.Equal(t, int64(720), DeepCopyJSONValue(float32(720)))
		require.Equal(t, int64(0), DeepCopyJSONValue(0.0))
		require.Equal(t, int64(-5), DeepCopyJSONValue(-5.0))
	})

	t.Run("int and int32 still become int64", func(t *testing.T) {
		require.Equal(t, int64(90), DeepCopyJSONValue(90))
		require.Equal(t, int64(90), DeepCopyJSONValue(int32(90)))
		require.Equal(t, int64(90), DeepCopyJSONValue(int64(90)))
	})

	t.Run("strings, bools and nil are untouched", func(t *testing.T) {
		require.Equal(t, "90.5", DeepCopyJSONValue("90.5"),
			"a numeric-looking STRING must not be converted: that would be a different defect")
		require.Equal(t, true, DeepCopyJSONValue(true))
		require.Nil(t, DeepCopyJSONValue(nil))
	})

	// IT RECURSES, so the defect was never limited to top-level fields. A consumer measured their one
	// exposed kind carrying the fractional field at rules[].parameters.minimum_coverage -- inside an
	// array, inside a map. A fix tested only on scalars would have left that path broken.
	t.Run("a fraction nested in a map inside an array survives", func(t *testing.T) {
		in := map[string]any{
			"rules": []any{
				map[string]any{"parameters": map[string]any{
					"minimum_coverage":  80.5,
					"max_coverage_drop": 0.5,
					"required_checks":   3.0,
				}},
			},
		}
		out := DeepCopyJSONValue(in).(map[string]any)
		params := out["rules"].([]any)[0].(map[string]any)["parameters"].(map[string]any)
		require.Equal(t, 80.5, params["minimum_coverage"], "the real consumer path is array -> map -> map")
		require.Equal(t, 0.5, params["max_coverage_drop"])
		require.Equal(t, int64(3), params["required_checks"], "and a whole one alongside it still coerces")
	})

	t.Run("it is a COPY: mutating the result does not touch the input", func(t *testing.T) {
		in := map[string]any{"a": map[string]any{"b": 1.5}}
		out := DeepCopyJSONValue(in).(map[string]any)
		out["a"].(map[string]any)["b"] = 9.5
		require.Equal(t, 1.5, in["a"].(map[string]any)["b"],
			"the guard must not have turned the copy into an alias")
	})
}
