//go:build unit

package restdefinition

import (
	"errors"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/stretchr/testify/require"
)

// renderWarnings is the whole of what status carries, so its edges are worth pinning directly.
func TestRenderWarnings(t *testing.T) {
	t.Run("none yields nil, not an empty slice", func(t *testing.T) {
		// nil rather than []string{} because omitempty only drops a nil slice: an empty one would put
		// `schemaWarnings: []` on every healthy RestDefinition in every `kubectl get -o yaml`.
		require.Nil(t, renderWarnings(nil))
		require.Nil(t, renderWarnings([]error{}))
	})

	t.Run("messages are carried verbatim", func(t *testing.T) {
		got := renderWarnings([]error{
			errors.New("generation error at identifiers.node_id: status field 'node_id' not found in response, defaulting to string"),
			errors.New("validation error: could not extract schema for action 'create' to compare: no suitable response"),
		})
		require.Equal(t, []string{
			"generation error at identifiers.node_id: status field 'node_id' not found in response, defaulting to string",
			"validation error: could not extract schema for action 'create' to compare: no suitable response",
		}, got, "the message is what a reader greps for, so it must not be reformatted")
	})

	t.Run("a nil entry is skipped rather than panicking", func(t *testing.T) {
		got := renderWarnings([]error{nil, errors.New("real"), nil})
		require.Equal(t, []string{"real"}, got)
	})

	t.Run("all-nil entries yield nil, not a slice of empty strings", func(t *testing.T) {
		require.Nil(t, renderWarnings([]error{nil, nil}),
			"otherwise a healthy definition gets schemaWarnings with blank entries")
	})
}

// THE CLEARING CASE, which is the property that makes the field worth reading.
//
// The old code emitted an Event only when warnings existed. A status field written on the same condition
// would leave a FIXED declaration still complaining forever, and a surface that cannot go quiet is one
// nobody trusts enough to check. The assignment at the render site is therefore unconditional, and this
// asserts the consequence rather than the code shape.
func TestSchemaWarningsAreClearedWhenTheRenderIsClean(t *testing.T) {
	cr := &definitionv1alpha1.RestDefinition{}
	cr.Status.SchemaWarnings = []string{"generation error: field 'spec.id' set in excludedSpecFields not found in schema"}
	require.NotEmpty(t, cr.Status.SchemaWarnings, "the fixture must start dirty or this test proves nothing")

	// What the render site does, with a clean render.
	var none []error
	cr.Status.SchemaWarnings = renderWarnings(none)

	require.Nil(t, cr.Status.SchemaWarnings,
		"a clean render must clear the field; leaving a stale complaint is the defect, not the fix")
}

// The field must survive snapshotStatus, which is the mechanism that actually persists status (#263).
// A field set in place but absent from the snapshot is never written -- that failure dropped two fields
// once already, and every test in the package passed while it did.
func TestSchemaWarningsSurviveTheStatusSnapshot(t *testing.T) {
	cr := &definitionv1alpha1.RestDefinition{}
	cr.Status.SchemaWarnings = []string{"generation error: field 'x' not found in schema"}
	cr.Status.GeneratorBuild = "0.53.0"

	st := snapshotStatus(cr)

	require.Equal(t, []string{"generation error: field 'x' not found in schema"}, st.status.SchemaWarnings,
		"the snapshot is a DeepCopy of the whole status, so a new field is carried for free -- this asserts "+
			"that remains true rather than assuming it")
	require.Equal(t, "0.53.0", st.status.GeneratorBuild, "the control: a field known to be snapshotted")

	// And the copy must be independent, or clearing the live field would silently clear the snapshot too.
	cr.Status.SchemaWarnings = nil
	require.NotNil(t, st.status.SchemaWarnings, "snapshotStatus must deep-copy, not alias")
}

// These warnings are deliberately non-fatal. Nothing about carrying them may touch readiness, because
// failing a working install over a declaration that names a missing field is worse than the warning.
func TestSchemaWarningsDoNotTouchReadiness(t *testing.T) {
	cr := &definitionv1alpha1.RestDefinition{}
	before := cr.Status.ConditionedStatus.DeepCopy()

	cr.Status.SchemaWarnings = renderWarnings([]error{errors.New("generation error: field 'x' not found in schema")})

	require.NotEmpty(t, cr.Status.SchemaWarnings)
	require.Equal(t, before, cr.Status.ConditionedStatus.DeepCopy(),
		"recording warnings must not alter any condition")
}
