package render

import (
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/oas2jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandWildcardActions(t *testing.T) {
	allVerbs := []definitionv1alpha1.VerbsDescription{
		{Action: "create"},
		{Action: "get"},
		{Action: "update"},
		{Action: "delete"},
	}

	testCases := []struct {
		name           string
		actions        []string
		verbs          []definitionv1alpha1.VerbsDescription
		expectedResult []string
		expectedError  bool
	}{
		{
			name:           "Wildcard should expand to all verb actions",
			actions:        []string{"*"},
			verbs:          allVerbs,
			expectedResult: []string{"create", "get", "update", "delete"},
			expectedError:  false,
		},
		{
			name:           "Explicit actions should remain unchanged",
			actions:        []string{"create", "delete"},
			verbs:          allVerbs,
			expectedResult: []string{"create", "delete"},
			expectedError:  false,
		},
		{
			name:           "Empty actions list should remain empty",
			actions:        []string{},
			verbs:          allVerbs,
			expectedResult: []string{},
			expectedError:  false,
		},
		{
			name:           "Nil actions list should remain nil",
			actions:        nil,
			verbs:          allVerbs,
			expectedResult: nil,
			expectedError:  false,
		},
		{
			name:           "Wildcard with no verbs should result in an empty list",
			actions:        []string{"*"},
			verbs:          []definitionv1alpha1.VerbsDescription{},
			expectedResult: []string{},
			expectedError:  false,
		},
		{
			name:           "Actions list with other values alongside wildcard should get error",
			actions:        []string{"*", "get", "update"},
			verbs:          allVerbs,
			expectedResult: nil,
			expectedError:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := expandWildcardActions(tc.actions, tc.verbs)

			if tc.expectedError {
				assert.Error(t, err, "Expected an error but got none")
				assert.Nil(t, result, "Result should be nil when error occurs")
			} else {
				assert.NoError(t, err, "Unexpected error occurred")
				assert.Equal(t, tc.expectedResult, result, "The expanded actions did not match the expected result")
			}
		})
	}
}

// stubOASDoc is a minimal OASDocument whose only real behaviour is exact path lookup — which is precisely
// the runtime semantic being validated.
type stubOASDoc struct{ paths map[string]bool }

func (s *stubOASDoc) FindPath(p string) (oas2jsonschema.PathItem, bool) {
	if s.paths[p] {
		return nil, true
	}
	return nil, false
}
func (s *stubOASDoc) SecuritySchemes() []oas2jsonschema.SecuritySchemeInfo { return nil }
func (s *stubOASDoc) Version() string                                      { return "1.0" }

// TestValidateAsyncPollPaths is the regression for #46: both spellings of an async poll path used to be
// accepted at admission and fail only at poll time, after a create had already fired.
func TestValidateAsyncPollPaths(t *testing.T) {
	const oasPath = "/projects/{projectId}/providers/Aruba.Baremetal/hpcs/monitor/{operationId}"
	// What the vendor spec actually declares — the same endpoint under a different parameter name.
	const vendorPath = "/projects/{projectId}/providers/Aruba.Baremetal/hpcs/monitor/{id}"

	crWithHandle := func(pollPath, handleParam string) *definitionv1alpha1.RestDefinition {
		return &definitionv1alpha1.RestDefinition{
			Spec: definitionv1alpha1.RestDefinitionSpec{
				Resource: definitionv1alpha1.Resource{
					VerbsDescription: []definitionv1alpha1.VerbsDescription{{
						Action: "create", Method: "POST", Path: "/things",
						Async: &definitionv1alpha1.AsyncConfig{
							Poll: definitionv1alpha1.PollConfig{Path: pollPath, HandleParam: handleParam, StatusPath: "status", SuccessValues: []string{"Succeeded"}},
						},
					}},
				},
			},
		}
	}
	crWith := func(pollPath string) *definitionv1alpha1.RestDefinition { return crWithHandle(pollPath, "") }
	docs := oas2jsonschema.NewDocumentSet(&stubOASDoc{paths: map[string]bool{oasPath: true, vendorPath: true}})

	t.Run("valid poll path passes", func(t *testing.T) {
		require.NoError(t, validateAsyncPollPaths(crWith(oasPath), docs))
	})

	t.Run("vendor spelling without a declaration is rejected", func(t *testing.T) {
		err := validateAsyncPollPaths(crWith(vendorPath), docs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "{operationId}", "the default is what it looked for")
		assert.Contains(t, err.Error(), "handleParam", "the error must point at the way out")
		assert.Contains(t, err.Error(), "create", "the error must name the offending verb")
	})

	t.Run("vendor spelling PASSES once handleParam declares it — the whole point", func(t *testing.T) {
		require.NoError(t, validateAsyncPollPaths(crWithHandle(vendorPath, "id"), docs),
			"an unmodified vendor OAS must be usable by declaring its parameter name")
	})

	t.Run("a declared name absent from the path is rejected", func(t *testing.T) {
		err := validateAsyncPollPaths(crWithHandle(oasPath, "id"), docs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "{id}", "the error names the declared token, not the default")
	})

	t.Run("operationId spelling not in the document is rejected as an exact-lookup miss", func(t *testing.T) {
		err := validateAsyncPollPaths(crWith("/other/monitor/{operationId}"), docs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a path declared in the OAS document")
	})

	t.Run("a verb without async is untouched", func(t *testing.T) {
		cr := crWith(oasPath)
		cr.Spec.Resource.VerbsDescription[0].Async = nil
		require.NoError(t, validateAsyncPollPaths(cr, docs))
	})

	t.Run("nil cr or document set is a no-op", func(t *testing.T) {
		require.NoError(t, validateAsyncPollPaths(nil, docs))
		require.NoError(t, validateAsyncPollPaths(crWith(oasPath), nil))
	})
}
