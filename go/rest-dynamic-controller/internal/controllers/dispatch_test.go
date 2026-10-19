package restResources

import (
	"testing"

	"github.com/krateo-platformops/plumbing/kubeutil/event"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func cr(spec map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{"spec": spec}}
}

func TestDispatchVerbFound(t *testing.T) {
	info := &getter.Info{Resource: getter.Resource{VerbsDescription: []getter.VerbsDescription{
		{Action: "get", Method: "GET", Path: "/a"},
		{Action: "dispatch", Method: "POST", Path: "/b"},
	}}}

	v := dispatchVerb(info)

	require.NotNil(t, v)
	assert.Equal(t, "/b", v.Path)
}

func TestDispatchVerbAbsentAndNilSafe(t *testing.T) {
	assert.Nil(t, dispatchVerb(nil))
	assert.Nil(t, dispatchVerb(&getter.Info{Resource: getter.Resource{VerbsDescription: []getter.VerbsDescription{
		{Action: "create", Method: "POST", Path: "/a"},
	}}}))
}

func TestCorrelationTokenReadsANestedSpecPath(t *testing.T) {
	got, ok := correlationToken(cr(map[string]interface{}{
		"inputs": map[string]interface{}{"correlationId": "run-7"},
	}), "inputs.correlationId")

	require.True(t, ok)
	assert.Equal(t, "run-7", got)
}

func TestCorrelationTokenAbsentIsNotAnEmptyMatch(t *testing.T) {
	for name, tc := range map[string]struct {
		spec     map[string]interface{}
		fromSpec string
	}{
		"missing leaf":   {map[string]interface{}{"inputs": map[string]interface{}{}}, "inputs.correlationId"},
		"missing root":   {map[string]interface{}{"ref": "main"}, "inputs.correlationId"},
		"explicit nil":   {map[string]interface{}{"token": nil}, "token"},
		"empty fromSpec": {map[string]interface{}{"token": "x"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := correlationToken(cr(tc.spec), tc.fromSpec)
			assert.False(t, ok, "an absent token must be distinguishable from an empty one")
		})
	}
	_, ok := correlationToken(nil, "token")
	assert.False(t, ok)
}

// A number the CR holds as int64 comes back from JSON as float64. Comparing with DeepEqual, the way
// findby compares identifiers, would miss the resource's OWN token and fire the dispatch a second time.
// Both sides are stringified for exactly this reason, so this is the regression test for a duplicate run.
func TestCorrelationSurvivesTheJSONNumberRoundTrip(t *testing.T) {
	token, ok := correlationToken(cr(map[string]interface{}{"runId": int64(170425)}), "runId")
	require.True(t, ok)

	echoed, ok := echoedToken(map[string]interface{}{"runId": float64(170425)}, "runId")
	require.True(t, ok)

	assert.Equal(t, token, echoed, "int64 out, float64 back: the match must still hold")
}

func TestEchoedTokenAcceptsBothPathSpellings(t *testing.T) {
	item := map[string]interface{}{"inputs": map[string]interface{}{"correlationId": "run-7"}}

	withDot, ok := echoedToken(item, ".inputs.correlationId")
	require.True(t, ok)
	withoutDot, ok := echoedToken(item, "inputs.correlationId")
	require.True(t, ok)

	assert.Equal(t, "run-7", withDot)
	assert.Equal(t, withDot, withoutDot,
		"the documented dialect has a leading dot; rejecting one spelling would be a silent never-match")
}

func TestFindCorrelatedItemFindsTheInvocation(t *testing.T) {
	items := []interface{}{
		map[string]interface{}{"id": 1, "inputs": map[string]interface{}{"correlationId": "other"}},
		map[string]interface{}{"id": 2, "inputs": map[string]interface{}{"correlationId": "run-7"}},
	}

	item, found, err := findCorrelatedItem(items, ".inputs.correlationId", "run-7")

	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, 2, item["id"])
}

func TestFindCorrelatedItemReportsAbsence(t *testing.T) {
	items := []interface{}{
		map[string]interface{}{"id": 1, "inputs": map[string]interface{}{"correlationId": "other"}},
		"not an object",
		map[string]interface{}{"id": 3},
	}

	_, found, err := findCorrelatedItem(items, ".inputs.correlationId", "run-7")

	require.NoError(t, err)
	assert.False(t, found, "absence is how the dispatch gets to fire; it must not be an error")
}

func TestFindCorrelatedItemOnAnEmptyCollection(t *testing.T) {
	_, found, err := findCorrelatedItem(nil, ".inputs.correlationId", "run-7")
	require.NoError(t, err)
	assert.False(t, found)
}

// The most important one. apiLookup took items[0] on an ambiguous match and that silent wrong answer is
// what got it removed. Here the stakes are higher still: the remedy for "not found" is to POST again, so
// an ambiguous collection quietly resolved either way becomes a duplicate-run loop. It must be a hard
// error naming the token, the count and the way out.
func TestFindCorrelatedItemRefusesAnAmbiguousMatch(t *testing.T) {
	items := []interface{}{
		map[string]interface{}{"id": 1, "inputs": map[string]interface{}{"correlationId": "run-7"}},
		map[string]interface{}{"id": 2, "inputs": map[string]interface{}{"correlationId": "run-7"}},
	}

	_, found, err := findCorrelatedItem(items, ".inputs.correlationId", "run-7")

	require.Error(t, err)
	assert.False(t, found)
	assert.Contains(t, err.Error(), "ambiguous")
	assert.Contains(t, err.Error(), "2 items")
	assert.Contains(t, err.Error(), "run-7")
	assert.Contains(t, err.Error(), "narrow the collection", "an error with no way out is just a stop")
}

func TestDeleteDispatchIsANoOpOnlyForADispatchResource(t *testing.T) {
	h := &handler{eventRecorder: event.NewNopRecorder(), logger: logging.NewNopLogger()}

	assert.False(t, h.deleteDispatch(cr(nil), &getter.Info{Resource: getter.Resource{
		VerbsDescription: []getter.VerbsDescription{{Action: "delete", Method: "DELETE", Path: "/a"}},
	}}, logging.NewNopLogger()), "a normal resource must fall through to the real delete")

	assert.True(t, h.deleteDispatch(cr(nil), &getter.Info{Resource: getter.Resource{
		VerbsDescription: []getter.VerbsDescription{{Action: "dispatch", Method: "POST", Path: "/a",
			Correlation: &getter.Correlation{FromSpec: "token"}}},
	}}, logging.NewNopLogger()), "a dispatch resource's delete is handled here, as a no-op")
}

func annotated(ann map[string]string) *unstructured.Unstructured {
	u := cr(map[string]interface{}{"token": "t1"})
	u.SetAnnotations(ann)
	return u
}

// The guarantee: exactly one POST per token, ever. Observe returns not-exists only when this is false,
// and not-exists is what the reconciler acts on by calling Create — so a true answer here is the only
// thing standing between a mistyped echoedAt and a duplicate run on every single reconcile.
func TestAlreadyDispatchedIsPerTokenNotPerResource(t *testing.T) {
	for name, tc := range map[string]struct {
		ann   map[string]string
		token string
		want  bool
	}{
		"never dispatched":        {nil, "t1", false},
		"dispatched this token":   {map[string]string{annotationDispatchedToken: "t1"}, "t1", true},
		"token changed => fire":   {map[string]string{annotationDispatchedToken: "t1"}, "t2", false},
		"empty token never fired": {map[string]string{annotationDispatchedToken: ""}, "", false},
		"other annotations only":  {map[string]string{"krateo.io/something": "t1"}, "t1", false},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, alreadyDispatched(annotated(tc.ann), tc.token))
		})
	}
	assert.False(t, alreadyDispatched(nil, "t1"))
}

// A corrupt counter must not be able to fail a reconcile: it is a diagnosis aid, not state.
func TestDispatchObserveMissesToleratesRubbish(t *testing.T) {
	for in, want := range map[string]int{"": 0, "3": 3, "nonsense": 0, "-4": 0, "0": 0} {
		assert.Equal(t, want, dispatchObserveMisses(annotated(map[string]string{annotationDispatchObserveMisses: in})), "input %q", in)
	}
	assert.Equal(t, 0, dispatchObserveMisses(annotated(nil)))
}

// The deadline is a DIAGNOSIS deadline, not a retry budget — nothing is re-POSTed when it passes. This
// pins the constant against being read as "retry 5 times".
func TestDispatchObserveMissesDeadlineIsSmallAndPositive(t *testing.T) {
	assert.Greater(t, maxDispatchObserveMisses, 1, "one slow reconcile must not be reported as a defect")
	assert.Less(t, maxDispatchObserveMisses, 20, "a persistent miss is a declaration defect; do not wait indefinitely")
}
