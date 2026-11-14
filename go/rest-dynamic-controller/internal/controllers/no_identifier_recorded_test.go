//go:build unit || integration

package restResources

import (
	"os"
	"strings"
	"testing"

	customcondition "github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// declaresVerb separates the two situations IsResourceKnown collapses into one `false`. Getting this
// wrong in either direction is a behaviour change: a false positive makes an unobservable-by-design kind
// start refusing, and a false negative leaves issue 682's latch in place.
func TestDeclaresVerbSeparatesDeclaredFromAbsent(t *testing.T) {
	verbs := []getter.VerbsDescription{
		{Action: "create", Method: "POST", Path: "/x"},
		{Action: "GET", Method: "GET", Path: "/x/{id}"}, // deliberately upper-case
	}

	assert.True(t, declaresVerb(verbs, "get"),
		"a declared get must be found case-insensitively; the corpus contains both spellings")
	assert.False(t, declaresVerb(verbs, "findby"),
		"findby is NOT declared here, and reporting it as declared would skip the refusal branch")

	// CONTROL: an empty verb list must not report anything as declared, or every assertion above
	// passes for a reason unrelated to matching.
	assert.False(t, declaresVerb(nil, "get"),
		"control: nothing is declared on a nil verb list")
	assert.False(t, declaresVerb([]getter.VerbsDescription{}, "get"),
		"control: nothing is declared on an empty verb list")
}

// THE CONDITION MUST BE Ready=False, not Available. That inversion IS issue 682.
func TestNoIdentifierRecordedIsNotReady(t *testing.T) {
	c := customcondition.NoIdentifierRecorded("the create reported success but no identifier was recorded")

	assert.Equal(t, condition.TypeReady, c.Type)
	assert.Equal(t, metav1.ConditionFalse, c.Status,
		"ConditionTrue here would be the defect this exists to remove: a CR reporting healthy for a "+
			"resource it cannot address")
	assert.Equal(t, customcondition.ReasonNoIdentifierRecorded, c.Reason)
	assert.NotEmpty(t, c.Message, "the cause must travel on the condition, not only in the log")

	// CONTROL: Available is the value this replaced, and it must differ in BOTH fields that matter --
	// otherwise the assertions above could pass against the old behaviour.
	av := condition.Available()
	require.Equal(t, condition.TypeReady, av.Type, "control: Available is also a Ready condition")
	assert.NotEqual(t, av.Status, c.Status,
		"control: Available is True and this must be False, or nothing changed")
	assert.NotEqual(t, av.Reason, c.Reason,
		"control: the reason must differ too, since consumers key on it")
}

// The refusal MUST be wrapped in controller.ReadyAssessed, and no behavioural test can see it: the wrap
// changes what unstructured-runtime's handleObserve does to the Ready condition AFTER this handler
// returns. Without it the runtime replaces Ready with ObserveFailed, and the reason naming the cause --
// the entire point of the fix -- is overwritten before anyone reads it.
//
// Same contract and same test shape as the async failure branch, which has its own ReadyAssessed guard.
func TestTheRefusalIsReadyAssessedWrapped(t *testing.T) {
	b, err := os.ReadFile("restResources.go")
	require.NoError(t, err)
	src := string(b)

	i := strings.Index(src, "if declaresVerb(clientInfo.Resource.VerbsDescription, string(apiaction.Get))")
	require.NotEqual(t, -1, i, "the refusal branch moved or was removed; re-point this test")

	// Slice to the end of that block: the next line at the same indentation that closes it.
	end := strings.Index(src[i:], "\n\t\t\t}\n")
	require.NotEqual(t, -1, end, "could not find the end of the refusal branch")
	branch := src[i : i+end]

	// CONTROL: the slice must really be the refusal branch, or the assertion below is vacuous.
	require.Contains(t, branch, "NoIdentifierRecorded",
		"control: this slice does not look like the refusal branch")

	assert.Contains(t, branch, "controller.ReadyAssessed(",
		"the refusal's error return must be wrapped in controller.ReadyAssessed, or handleObserve "+
			"overwrites the Ready condition with ObserveFailed and the cause is lost")

	// And it must NOT return a bare observation-with-error that the runtime would read as not-exists.
	assert.NotContains(t, branch, "ResourceExists:   false",
		"reporting not-exists sends the runtime back to Create, which duplicates the resource on a "+
			"vendor that mints the identifier -- that is the other half of why this branch exists")
}

// The historical behaviour must SURVIVE for a kind declaring neither get nor findby: it is unobservable
// by declaration rather than by accident, and there is no better answer available for it.
func TestTheAssumeUpToDateFallbackSurvivesForAnUnobservableKind(t *testing.T) {
	b, err := os.ReadFile("restResources.go")
	require.NoError(t, err)
	src := string(b)

	assert.Contains(t, src, "Resource is assumed to be up-to-date. API call not found for FindBy.",
		"the fallback must still exist for a kind with neither get nor findby; removing it would "+
			"make every such kind start refusing, which is a behaviour change nobody asked for")

	// It must sit AFTER the refusal, or the refusal is unreachable.
	refusal := strings.Index(src, "if declaresVerb(clientInfo.Resource.VerbsDescription, string(apiaction.Get))")
	fallback := strings.Index(src, "Resource is assumed to be up-to-date. API call not found for FindBy.")
	require.NotEqual(t, -1, refusal)
	require.NotEqual(t, -1, fallback)
	assert.Less(t, refusal, fallback,
		"the refusal must come FIRST; if the fallback precedes it the refusal can never run and this "+
			"whole fix is dead code that still compiles")
}
