//go:build unit

package restdefinition

import (
	"testing"

	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestAConfigurationFieldThatPrunesAServedPropertyIsWarnedAbout covers the destructive half of unfreezing
// configurationFields.
//
// The field became mutable on the principle #666 settled -- gate on DATA LOSS, not on symmetry -- and its
// ADD prunes a served property exactly as an exclusion does, because removeConfiguredFields is called from
// the SPEC builder. Removing an entry lets the property return and loses nothing, so only the addition is
// warned about.
//
// ONE PREDICATE SERVES BOTH FIELDS. excludedFieldsStillServed is reused rather than reimplemented: a
// reader who finds the exclusion guarded would otherwise assume the pattern is applied, which is how the
// two drift.
func TestAConfigurationFieldThatPrunesAServedPropertyIsWarnedAbout(t *testing.T) {
	served := []map[string]apiextensionsv1.JSONSchemaProps{{
		"token":    {Type: "string"},
		"keepThis": {Type: "string"},
	}}

	cr := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "widget", Namespace: "demo"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			Resource: definitionv1alpha1.Resource{
				ConfigurationFields: []definitionv1alpha1.ConfigurationField{
					{FromOpenAPI: definitionv1alpha1.FromOpenAPI{Name: "token", In: "header"}},
				},
			},
		},
	}

	names := configurationFieldNames(cr)
	// SETUP CONTROL: the names really were extracted, so an empty findings list below means "not served"
	// rather than "nothing to look for". These two fail with different messages.
	require.Equal(t, []string{"token"}, names,
		"the configurationFields names were not extracted, so the assertion below proves nothing")

	hits := excludedFieldsStillServed(names, served)
	require.Equal(t, []string{"token"}, hits,
		"adding this entry prunes a property the live CRD still serves, and nothing said so")

	// NEGATIVE CONTROL, in the same served set: a property the CRD serves that is NOT configured must not
	// be reported. Without this the assertion above passes for an implementation that returns everything.
	require.NotContains(t, hits, "keepThis",
		"a served property that no configurationFields entry names is reported, which would warn about "+
			"every field in the spec")
}

// TestAConfigurationFieldForAnUnservedPropertyIsSilent is the ordinary case -- a first install, or a field
// the CRD never served -- and it must produce nothing, or the warning fires on every healthy render.
func TestAConfigurationFieldForAnUnservedPropertyIsSilent(t *testing.T) {
	cr := &definitionv1alpha1.RestDefinition{
		Spec: definitionv1alpha1.RestDefinitionSpec{
			Resource: definitionv1alpha1.Resource{
				ConfigurationFields: []definitionv1alpha1.ConfigurationField{
					{FromOpenAPI: definitionv1alpha1.FromOpenAPI{Name: "token", In: "header"}},
				},
			},
		},
	}
	names := configurationFieldNames(cr)
	require.Equal(t, []string{"token"}, names, "setup control: the name must be extracted")

	require.Empty(t, excludedFieldsStillServed(names, []map[string]apiextensionsv1.JSONSchemaProps{
		{"somethingElse": {Type: "string"}},
	}), "the property is not served, so there is nothing to prune and nothing to say")

	require.Empty(t, excludedFieldsStillServed(names, nil),
		"no served properties at all -- a first install -- must be silent")
}

// TestNoConfigurationFieldsYieldsNoNames pins the empty case, because a nil slice reaching the shared
// predicate is the input that would make it walk every served property for nothing.
func TestNoConfigurationFieldsYieldsNoNames(t *testing.T) {
	require.Nil(t, configurationFieldNames(&definitionv1alpha1.RestDefinition{}))

	// An entry with an empty name is skipped rather than passed through as "": the predicate would parse
	// it and a zero-length path has no meaningful comparison.
	cr := &definitionv1alpha1.RestDefinition{
		Spec: definitionv1alpha1.RestDefinitionSpec{
			Resource: definitionv1alpha1.Resource{
				ConfigurationFields: []definitionv1alpha1.ConfigurationField{{}},
			},
		},
	}
	require.Empty(t, configurationFieldNames(cr),
		"an entry with no name is passed to the predicate as an empty path")
}
