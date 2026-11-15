//go:build unit || integration

package restResources

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func crFor692(annotations map[string]interface{}, conditions ...map[string]interface{}) *unstructured.Unstructured {
	meta := map[string]interface{}{"name": "x", "namespace": "n"}
	if annotations != nil {
		meta["annotations"] = annotations
	}
	obj := map[string]interface{}{
		"apiVersion": "example.krateo.io/v1alpha1",
		"kind":       "Thing",
		"metadata":   meta,
	}
	if len(conditions) > 0 {
		cs := make([]interface{}, 0, len(conditions))
		for _, c := range conditions {
			cs = append(cs, c)
		}
		obj["status"] = map[string]interface{}{"conditions": cs}
	}
	return &unstructured.Unstructured{Object: obj}
}

func ready692(status, reason string) map[string]interface{} {
	return map[string]interface{}{
		"type": "Ready", "status": status, "reason": reason,
		"lastTransitionTime": "2026-11-15T08:40:21Z",
	}
}

// Both routes measured in issue 692 must answer TRUE, and the first one is what the previous
// condition-only guard got wrong.
func TestCreateHasBeenAttemptedCoversBothMeasuredRoutes(t *testing.T) {
	t.Run("azuredevops route: the status write lost a race, so NO condition was persisted", func(t *testing.T) {
		// The marker is an ANNOTATION, written by a different request than the conditions, so it
		// survives the status-write failure that is the trigger. This CR has no status at all.
		mg := crFor692(map[string]interface{}{
			"krateo.io/external-create-succeeded": "2026-11-15T08:38:35Z",
		})
		assert.True(t, createHasBeenAttempted(mg),
			"a create WAS attempted -- the runtime stamped succeeded. Reading false here reports "+
				"not-exists with a nil error, which the runtime turns into a fresh Create: 5 of 5 "+
				"duplicates at a vendor, extras orphaned on delete")
	})

	t.Run("github route: the refusal displaced Creating", func(t *testing.T) {
		mg := crFor692(nil, ready692("False", "NoIdentifierRecorded"))
		assert.True(t, createHasBeenAttempted(mg),
			"NoIdentifierRecorded is only ever written AFTER a create reached the vendor")
	})

	t.Run("pending alone is enough", func(t *testing.T) {
		mg := crFor692(map[string]interface{}{
			"krateo.io/external-create-pending": "2026-11-15T08:40:22Z",
		})
		assert.True(t, createHasBeenAttempted(mg),
			"pending is stamped before the handler's Create runs, so it is set even for a create "+
				"still in flight")
	})

	// Status matters as well as reason: IsConditionSet compares Type, Status AND Reason, and Available
	// is a Ready=TRUE condition while Creating is Ready=False. Writing both as False made this table
	// fail on Available -- the test catching my own error rather than the code's.
	for _, c := range []struct{ reason, status string }{
		{"Creating", "False"},
		{"Available", "True"},
	} {
		t.Run("historical: Ready="+c.status+"/"+c.reason, func(t *testing.T) {
			mg := crFor692(nil, ready692(c.status, c.reason))
			assert.True(t, createHasBeenAttempted(mg),
				"the pre-existing behaviour must be preserved exactly; this is what #347 relies on")
		})
	}

	// THE CONTROL, and it is the one that must stay false: a first-ever reconcile has to report
	// not-exists so that a create is dispatched at all. If this flips, nothing is ever created.
	t.Run("CONTROL: a fresh CR has no marker and no condition", func(t *testing.T) {
		mg := crFor692(nil)
		require.False(t, createHasBeenAttempted(mg),
			"control: a CR nobody has acted on must report false, or no resource is ever created")
		mg2 := crFor692(map[string]interface{}{"krateo.io/connector-verbose": "true"})
		require.False(t, createHasBeenAttempted(mg2),
			"control: an UNRELATED annotation must not count as a create marker")
	})
}
