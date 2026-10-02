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

// This file used to test needsVersionLabel/ensureVersionLabel, the fallback that stamped the version
// label at the top of Observe when an instance carried none. Both are gone, so the tests that pinned
// their behaviour are gone with them.
//
// They were removed rather than kept-and-skipped because the behaviour they protected is now impossible
// to reach, not merely unused: under version-scoped watching the selector is exact equality on
// VersionLabel, so an unlabelled instance matches no watch and is never delivered to Observe. A test
// asserting "an unlabelled instance gets stamped here" would pass against code that can never run.
//
// What covers that case now, and where it is tested:
//
//   - the MutatingAdmissionPolicy stamps at admission -- oasgen-provider, internal/tools/policy
//   - oasgen-provider backfills instances predating the policy before deploying the version-scoped
//     controller -- internal/controllers/restdefinition, backfillVersionLabel
//
// What remains testable here is the constant itself, which is load-bearing across two repositories.

// TestVersionLabelMatchesTheProviderConstant pins the one thing this module still owns.
//
// VersionLabel mirrors oasgen-provider's crd/generation.VersionLabel. The provider generates the CRD
// whose printer column reads this label, backfills it, and renders the selector this controller is
// started with; this module selects on it. A rename on either side alone would not fail to compile --
// it would silently produce a controller that watches a label nobody writes, which is a controller that
// reconciles nothing while reporting healthy.
func TestVersionLabelMatchesTheProviderConstant(t *testing.T) {
	assert.Equal(t, "krateo.io/oas-version", VersionLabel,
		"must stay identical to oasgen-provider's crd/generation.VersionLabel")
}

// An instance carrying the label is unremarkable; one carrying none is now simply never delivered here.
// This records that expectation so the helper above keeps a user and the assumption stays written down.
func TestAnInstanceCarriesTheLabelItWasAdmittedWith(t *testing.T) {
	mg := instance(map[string]string{VersionLabel: "v1-0-27"})
	assert.Equal(t, "v1-0-27", mg.GetLabels()[VersionLabel])

	// Not stamped here any more: nothing in this package writes the label, by design.
	unlabelled := instance(nil)
	assert.Empty(t, unlabelled.GetLabels()[VersionLabel],
		"this controller no longer stamps; an unlabelled instance never reaches it under an exact selector")
}
