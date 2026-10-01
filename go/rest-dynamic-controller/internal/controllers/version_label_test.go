//go:build unit || integration

package restResources

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func instance(labels map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "sample.krateo.io/v1alpha1",
		"kind":       "Sample",
		"metadata":   map[string]interface{}{"name": "s", "namespace": "demo"},
	}}
	if labels != nil {
		u.SetLabels(labels)
	}
	return u
}

// TestNeedsVersionLabelFillsOnlyAnAbsence pins the stamping rule.
//
// The label records which served CRD version an instance belongs to. It is normally written in the
// apiserver by the oas-version MutatingAdmissionPolicy, which is GA only from Kubernetes 1.36 while the
// chart's floor is 1.33 — so on 1.33–1.35 nothing would ever write it, and per-version reconciliation and
// version pruning both read it.
//
// The rule is deliberately narrow: fill an absence, never move an existing value. Rewriting it would be
// migrating the instance onto another version, which is a deliberate act (core-provider gates the
// equivalent behind upgradePolicy) and must not happen as a side effect of observing.
func TestNeedsVersionLabelFillsOnlyAnAbsence(t *testing.T) {
	t.Run("no labels at all", func(t *testing.T) {
		h := &handler{version: "v1-0-28"}
		mg := instance(nil)
		assert.True(t, h.needsVersionLabel(mg))
	})

	t.Run("labels present but no version", func(t *testing.T) {
		h := &handler{version: "v1-0-28"}
		mg := instance(map[string]string{"app": "demo"})
		assert.True(t, h.needsVersionLabel(mg))
		assert.Equal(t, "demo", mg.GetLabels()["app"], "existing labels must survive")
	})

	t.Run("an existing version is NEVER moved", func(t *testing.T) {
		h := &handler{version: "v1-0-28"}
		mg := instance(map[string]string{VersionLabel: "v1-0-27"})
		assert.False(t, h.needsVersionLabel(mg),
			"rewriting this would migrate the instance onto another version as a side effect of observing")
	})

	t.Run("empty string counts as absent", func(t *testing.T) {
		h := &handler{version: "v1-0-28"}
		mg := instance(map[string]string{VersionLabel: ""})
		assert.True(t, h.needsVersionLabel(mg))
	})

	t.Run("a controller with no version of its own stamps nothing", func(t *testing.T) {
		h := &handler{version: ""}
		mg := instance(nil)
		assert.False(t, h.needsVersionLabel(mg),
			"a controller with no version must stamp nothing: an empty label would read as a version named \"\"")
	})

	t.Run("nil instance", func(t *testing.T) {
		h := &handler{version: "v1-0-28"}
		assert.False(t, h.needsVersionLabel(nil))
	})
}
