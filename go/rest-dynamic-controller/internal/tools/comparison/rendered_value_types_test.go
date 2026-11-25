//go:build unit || integration

package comparison

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRenderedValueDistinguishesLookalikes is the test issue 927 should have been able to rely on.
//
// A consumer reported `FirstValue=90, SecondValue=90.5` on a field that drifted every resync. The 90
// came from inside the controller, but the OLD format printed int64(90), float64(90) and "90"
// IDENTICALLY -- and those are three different bugs: something typed the field as an integer,
// something truncated the value, or something stringified it. Four mechanisms were eliminated one at a
// time because the log could not tell them apart.
//
// Each arm below is a pair that the old format rendered the same and the new one separates.
func TestRenderedValueDistinguishesLookalikes(t *testing.T) {
	t.Run("the three lookalikes of 90 all render differently", func(t *testing.T) {
		got := []string{renderValue(int64(90)), renderValue(float64(90)), renderValue("90")}
		require.Equal(t, []string{`90 (int64)`, `90 (float64)`, `"90" (string)`}, got,
			"these three are the candidate bugs behind issue 927 and the log must separate them")
		require.Len(t, uniq(got), 3, "if any two collide the instrument cannot localise the defect")
	})

	t.Run("int32 is distinguishable from int64, because the comparer produces both", func(t *testing.T) {
		require.NotEqual(t, renderValue(int32(90)), renderValue(int64(90)),
			"InferType returns int32 inside int32 range and int64 outside it, so a reader needs to know which")
	})

	t.Run("an empty string is visible rather than blank", func(t *testing.T) {
		require.Equal(t, `"" (string)`, renderValue(""),
			`the old format printed an empty value as nothing at all: FirstValue=, SecondValue=`)
	})

	t.Run("a string with spaces keeps its boundaries", func(t *testing.T) {
		require.Equal(t, `" 90 " (string)`, renderValue(" 90 "),
			"leading and trailing space is exactly the kind of difference that reads as no difference")
	})

	t.Run("nil is explicit, not an empty rendering", func(t *testing.T) {
		require.Equal(t, "<nil>", renderValue(nil))
		require.NotEqual(t, renderValue(nil), renderValue(""),
			"absent and empty-string are different states and issue 911 turns on exactly that distinction")
	})

	// CONTROL: the fraction case the consumer actually hit must still show the VALUES differing, not
	// only the types -- a format that printed types alone would lose the measurement.
	t.Run("CONTROL: the 927 pair shows both value and type on each side", func(t *testing.T) {
		require.Equal(t, "90 (int64)", renderValue(int64(90)))
		require.Equal(t, "90.5 (float64)", renderValue(float64(90.5)))
	})
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
