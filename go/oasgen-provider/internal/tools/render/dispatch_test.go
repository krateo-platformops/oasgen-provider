package render

import (
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/oas2jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dispatchDoc reports paths AND their methods, which the correlation validator needs and the async poll
// validator did not: a collection declared under the wrong verb is a distinct, likely mistake.
type dispatchDoc struct{ paths map[string][]string }

func (d *dispatchDoc) FindPath(p string) (oas2jsonschema.PathItem, bool) {
	methods, ok := d.paths[p]
	if !ok {
		return nil, false
	}
	return &dispatchPathItem{methods: methods}, true
}
func (d *dispatchDoc) SecuritySchemes() []oas2jsonschema.SecuritySchemeInfo { return nil }
func (d *dispatchDoc) Version() string                                      { return "1.0" }

type dispatchPathItem struct{ methods []string }

func (p *dispatchPathItem) GetOperations() map[string]oas2jsonschema.Operation {
	out := map[string]oas2jsonschema.Operation{}
	for _, m := range p.methods {
		out[m] = nil
	}
	return out
}

func dispatchCR(observePath, observeMethod, fromSpec string) *definitionv1alpha1.RestDefinition {
	return &definitionv1alpha1.RestDefinition{
		Spec: definitionv1alpha1.RestDefinitionSpec{
			Resource: definitionv1alpha1.Resource{
				VerbsDescription: []definitionv1alpha1.VerbsDescription{{
					Action: "dispatch", Method: "POST",
					Path: "/repos/{owner}/{repo}/actions/workflows/{workflow_id}/dispatches",
					Correlation: &definitionv1alpha1.Correlation{
						FromSpec: fromSpec,
						Observe: definitionv1alpha1.CorrelationObserve{
							Method: observeMethod, Path: observePath, ItemsPath: "workflow_runs",
						},
						EchoedAt: ".inputs.correlationId",
					},
				}},
			},
		},
	}
}

const runsPath = "/repos/{owner}/{repo}/actions/runs"

func dispatchDocs() *oas2jsonschema.DocumentSet {
	return oas2jsonschema.NewDocumentSet(&dispatchDoc{paths: map[string][]string{
		"/repos/{owner}/{repo}/actions/workflows/{workflow_id}/dispatches": {"post"},
		runsPath: {"get"},
	}})
}

func TestValidateDispatchCorrelationAcceptsADeclaredCollection(t *testing.T) {
	require.NoError(t, validateDispatchCorrelation(dispatchCR(runsPath, "GET", "inputs.correlationId"), dispatchDocs()))
}

// The failure this prevents is the expensive one: with no collection to look the token up in, Observe
// can never report the invocation as existing, so the dispatch fires again on every single reconcile.
func TestValidateDispatchCorrelationRefusesAnUndeclaredCollectionPath(t *testing.T) {
	err := validateDispatchCorrelation(dispatchCR("/repos/{owner}/{repo}/actions/run", "GET", "inputs.correlationId"), dispatchDocs())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a path declared in the OAS document")
	assert.Contains(t, err.Error(), "every reconcile",
		"the error has to say what it costs, or it reads as pedantry about a typo")
	var fe *FieldError
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, "spec.resource.verbsDescription[0].correlation.observe.path", fe.Field,
		"an author needs the line to fix")
}

// A collection declared under the wrong method is a different mistake from a wrong path, and a likely
// one: POST is legal here (some APIs expose search that way), so the author cannot infer GET.
func TestValidateDispatchCorrelationRefusesAnUndeclaredCollectionMethod(t *testing.T) {
	err := validateDispatchCorrelation(dispatchCR(runsPath, "POST", "inputs.correlationId"), dispatchDocs())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not declared for path")
	var fe *FieldError
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, "spec.resource.verbsDescription[0].correlation.observe.method", fe.Field)
}

func TestValidateDispatchCorrelationIgnoresNonDispatchVerbs(t *testing.T) {
	cr := dispatchCR(runsPath, "GET", "inputs.correlationId")
	cr.Spec.Resource.VerbsDescription[0].Action = "create"
	cr.Spec.Resource.VerbsDescription[0].Correlation = nil

	require.NoError(t, validateDispatchCorrelation(cr, dispatchDocs()))
}

// The token has to be a field an instance can actually carry, but only its ROOT can be required to be
// declared -- and that asymmetry is the whole point of this check. GitHub's workflow_dispatch body is
// {ref, inputs} with `inputs` an OPEN object, so "inputs.correlationId" is a good token that is not and
// cannot be a declared property. Demanding the full path would refuse #199's motivating case.
func TestCorrelationTokenAcceptsAPathInsideAFreeFormObject(t *testing.T) {
	spec := []byte(`{"properties":{"ref":{"type":"string"},"inputs":{"type":"object"}}}`)

	require.NoError(t, validateCorrelationTokenInSpec(dispatchCR(runsPath, "GET", "inputs.correlationId"), spec))
}

func TestCorrelationTokenAcceptsAnExactTopLevelField(t *testing.T) {
	spec := []byte(`{"properties":{"correlationId":{"type":"string"}}}`)

	require.NoError(t, validateCorrelationTokenInSpec(dispatchCR(runsPath, "GET", "correlationId"), spec))
}

// Requiring nothing would accept this, and it can never match, so the dispatch would re-fire forever.
func TestCorrelationTokenRefusesARootThatIsNotInTheSpec(t *testing.T) {
	spec := []byte(`{"properties":{"ref":{"type":"string"},"inputs":{"type":"object"}}}`)

	err := validateCorrelationTokenInSpec(dispatchCR(runsPath, "GET", "nosuchfield.token"), spec)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nosuchfield")
	assert.Contains(t, err.Error(), "fire on every reconcile")
	assert.Contains(t, err.Error(), "inputs, ref",
		"naming the available fields is what turns this from a refusal into a fix; sorted, so the message is stable")
	var fe *FieldError
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, "spec.resource.verbsDescription[0].correlation.fromSpec", fe.Field)
}

// Fail open. An unparsable spec schema is a bigger problem that crdgen is about to report properly;
// refusing here would misattribute it to the correlation field.
func TestCorrelationTokenFailsOpenOnAnUnparsableSpec(t *testing.T) {
	require.NoError(t, validateCorrelationTokenInSpec(dispatchCR(runsPath, "GET", "inputs.correlationId"), []byte("{not json")))
	require.NoError(t, validateCorrelationTokenInSpec(dispatchCR(runsPath, "GET", "inputs.correlationId"), nil))
}
