//go:build unit

package builder

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

// equivInfo builds a kind whose get has ONE path param, resolvable only by a mapping, declared on
// whichever field the caller names. Nothing declares a spec field named `uuid`, so step 3's
// by-name auto-populate can never fill it -- the mapping is the only route, which is what makes the
// two sides comparable.
//
// Built fresh per call rather than shared: BuildCallConfig may rewrite the mapping it is handed
// (stripResolverSources removes inCustomResource), so a reused Info would make each side depend on
// which ran first. That hazard is the contributed arms' finding, not mine.
func equivInfo(deprecated bool, source string) *getter.Info {
	get := getter.VerbsDescription{Action: "get", Method: "GET", Path: "/v1/widgets/{uuid}"}
	if deprecated {
		get.RequestFieldMapping = []getter.RequestFieldMappingItem{
			{InPath: "uuid", InCustomResource: source},
		}
	} else {
		get.FieldMapping = []getter.FieldMappingItem{
			{InPath: "uuid", InCustomResource: source},
		}
	}
	return &getter.Info{Resource: getter.Resource{
		Identifiers:      []string{"name"},
		VerbsDescription: []getter.VerbsDescription{get},
	}}
}

func equivCR(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "example.com/v1alpha1",
		"kind":       "Widget",
		"metadata":   map[string]interface{}{"name": "w1", "namespace": "ns"},
		"spec":       map[string]interface{}{"name": "blade01"},
	}}
}

// buildFor runs BuildCallConfig for the get verb on whichever mapping field was declared.
func buildFor(t *testing.T, deprecated bool, source string) *restConfig {
	t.Helper()
	info := equivInfo(deprecated, source)
	_, callInfo, err := APICallBuilder(&mockUnstructuredClient{}, info, "get")
	require.NoError(t, err)
	require.NotNil(t, callInfo, "no call info for the get verb")

	rc := BuildCallConfig(context.Background(), callInfo, equivCR(t), nil, nil)
	require.NotNil(t, rc, "BuildCallConfig returned nil")
	require.NoError(t, rc.BuildErr, "the declaration could not produce a request")
	return &restConfig{
		Parameters: rc.Parameters,
		Query:      rc.Query,
		Headers:    rc.Headers,
		Cookies:    rc.Cookies,
		Body:       rc.Body,
	}
}

// restConfig is the comparable surface: every write target applyFieldMapping and
// applyRequestFieldMapping can touch. Comparing only Parameters would pass while a Query or
// Headers divergence went unmeasured -- the same claim-vs-query gap that produced today's other
// findings, so it is named rather than left to the reader.
type restConfig struct {
	Parameters map[string]string
	Query      map[string]string
	Headers    map[string]string
	Cookies    map[string]string
	Body       any
}

// TestTheTwoMappingFieldsProduceTheSameRequest is what a consumer needs before migrating off the
// deprecated field.
//
// `requestFieldMapping` is deprecated in favour of the unified `fieldMapping`, and a downstream repo
// carries FOURTEEN verbs on the deprecated one. Migrating means editing spec.resource, which
// regenerates every CRD there -- so "the two are equivalent" cannot be taken on faith, and nothing
// in this tree pinned it until now.
func TestTheTwoMappingFieldsProduceTheSameRequest(t *testing.T) {
	for _, source := range []string{"spec.name"} {
		deprecatedSide := buildFor(t, true, source)
		currentSide := buildFor(t, false, source)

		// SETUP CONTROL: the mapping actually fired on BOTH sides. Without this, two empty configs
		// would compare equal and the test would pass having measured nothing -- which is the exact
		// shape of a vacuous equivalence assertion.
		require.Equal(t, "blade01", deprecatedSide.Parameters["uuid"],
			"the DEPRECATED field did not fill the path param, so there is nothing to compare")
		require.Equal(t, "blade01", currentSide.Parameters["uuid"],
			"the CURRENT field did not fill the path param, so there is nothing to compare")

		require.Equal(t, deprecatedSide.Parameters, currentSide.Parameters, "Parameters diverge")
		require.Equal(t, deprecatedSide.Query, currentSide.Query, "Query diverges")
		require.Equal(t, deprecatedSide.Headers, currentSide.Headers, "Headers diverge")
		require.Equal(t, deprecatedSide.Cookies, currentSide.Cookies, "Cookies diverge")
		require.Equal(t, deprecatedSide.Body, currentSide.Body, "Body diverges")
	}
}

// TestTheCurrentMappingFieldWinsOnConflict pins the precedence, which is stated nowhere else.
//
// BuildCallConfig calls applyRequestFieldMapping (deprecated) and THEN applyFieldMapping (current),
// and both assign directly with no existence guard -- the "avoid overwriting" guard is on step 3's
// auto-populate only. So last write wins and the CURRENT field overrides.
//
// A consumer inferred the opposite from the ORDER IN A SUMMARY rather than from the file, and
// "prefer FieldMapping" plus "the deprecated one overrides it" would have been a genuinely
// surprising pair. It is not the pair; this test is why nobody has to infer it again.
func TestTheCurrentMappingFieldWinsOnConflict(t *testing.T) {
	info := &getter.Info{Resource: getter.Resource{
		Identifiers: []string{"name"},
		VerbsDescription: []getter.VerbsDescription{{
			Action: "get", Method: "GET", Path: "/v1/widgets/{uuid}",
			RequestFieldMapping: []getter.RequestFieldMappingItem{
				{InPath: "uuid", InCustomResource: "spec.deprecatedSource"},
			},
			FieldMapping: []getter.FieldMappingItem{
				{InPath: "uuid", InCustomResource: "spec.currentSource"},
			},
		}},
	}}
	cr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "example.com/v1alpha1",
		"kind":       "Widget",
		"metadata":   map[string]interface{}{"name": "w1", "namespace": "ns"},
		"spec": map[string]interface{}{
			"deprecatedSource": "FROM-DEPRECATED",
			"currentSource":    "FROM-CURRENT",
		},
	}}

	_, callInfo, err := APICallBuilder(&mockUnstructuredClient{}, info, "get")
	require.NoError(t, err)
	require.NotNil(t, callInfo)

	rc := BuildCallConfig(context.Background(), callInfo, cr, nil, nil)
	require.NotNil(t, rc)
	require.NoError(t, rc.BuildErr)

	// THE TWO SOURCES ARE DISTINGUISHABLE BY VALUE, which is the whole design of this arm. Had both
	// resolved to the same string, the assertion would pass whichever field won and prove nothing.
	require.Equal(t, "FROM-CURRENT", rc.Parameters["uuid"],
		"the DEPRECATED field won the conflict. BuildCallConfig calls applyRequestFieldMapping first "+
			"and applyFieldMapping second, both writing directly, so the CURRENT field must override. "+
			"If this fails, either the call order changed or a guard was added -- and the API doc "+
			"comment promising 'prefer fieldMapping' is now false")
}
