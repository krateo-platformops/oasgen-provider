package v1alpha1

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	"sigs.k8s.io/yaml"
)

// TestGeneratedCRDIsWithinCELCostBudget runs the shipped CRD through the apiserver's OWN validator, which
// is what decides at install time whether the CRD is accepted at all.
//
// Kubernetes costs every x-kubernetes-validations rule STATICALLY, against the bounds declared on the
// fields it traverses. A comprehension over a list with no maxItems estimates as effectively infinite, and
// there are two separate limits: a per-rule one and a total for the whole schema. Blowing either is not a
// validation that fails to fire -- the apiserver refuses the CustomResourceDefinition, so `helm install`
// fails and the provider does not come up. That is #169, found the hard way.
//
// Nothing else in the repo can catch this. The CEL admission tests compile and evaluate rules with cel-go,
// which happily runs a rule the apiserver would refuse to install; `make generate` emits whatever markers
// say; and CI's drift guard only checks the file is up to date, not that it is installable.
//
// This test is why the two "a dependentLookup resolver must name a declared lookup" checks live in
// validateDependentLookups instead of in CEL. Written as markers they measured as >100x the per-rule
// budget AND pushed the schema total over its limit -- which also flagged two PRE-EXISTING rules on
// fieldMapping as contributors, i.e. the schema was already close enough to its ceiling that any expensive
// addition takes the install down. Treat the remaining headroom as small: if this test fails on a rule you
// just added, move the check to generation time rather than bounding a user-facing list to fit.
func TestGeneratedCRDIsWithinCELCostBudget(t *testing.T) {
	raw, err := os.ReadFile(generatedCRDPath)
	require.NoError(t, err, "the generated CRD must be readable; run `make generate`")

	var external apiextensionsv1.CustomResourceDefinition
	require.NoError(t, yaml.Unmarshal(raw, &external))

	var internal apiextensions.CustomResourceDefinition
	require.NoError(t, apiextensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(&external, &internal, nil))

	// A CRD manifest on disk carries no status; the apiserver fills storedVersions on create. Supplying it
	// keeps the assertion at "zero errors" rather than having to allow-list an unrelated one.
	internal.Status.StoredVersions = []string{Version}

	var problems []string
	for _, e := range validation.ValidateCustomResourceDefinition(nil, &internal) {
		problems = append(problems, e.Error())
	}
	require.Empty(t, problems, "the apiserver would refuse to install this CRD:\n  %s", strings.Join(problems, "\n  "))
}
