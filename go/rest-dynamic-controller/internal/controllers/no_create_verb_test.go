//go:build unit || integration

package restResources

import (
	"os"
	"strings"
	"testing"

	customcondition "github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The condition itself: a findby-only resource must be reported NOT ready, with a reason distinct
// from CreateUnreachable.
//
// THE TWO REASONS MUST DIFFER, asserted rather than assumed. Both mean "this will never be created",
// but the remedies are incompatible: CreateUnreachable is fixed by changing identifiers or using a
// dispatch verb, NoCreateVerbDeclared by declaring a create verb or accepting that the Kind only
// observes. One reason with two fixes is worse than two reasons.
func TestNoCreateVerbDeclaredIsNotReadyAndIsItsOwnReason(t *testing.T) {
	got := customcondition.NoCreateVerbDeclared()

	assert.Equal(t, metav1.ConditionFalse, got.Status,
		"a resource nothing will ever create must not report Ready")
	assert.Equal(t, customcondition.ReasonNoCreateVerbDeclared, got.Reason)
	assert.NotEqual(t, customcondition.ReasonCreateUnreachable, got.Reason,
		"the two reasons have incompatible remedies and must stay distinguishable")

	// The message must name all three ways a create can be declared, or an author reading it cannot
	// tell which one they are missing.
	for _, want := range []string{"create verb", "dispatch", "createApiRef"} {
		assert.Contains(t, got.Message, want,
			"the message must name %q as a way to declare a create, or it does not say what to do", want)
	}
	assert.Contains(t, got.Message, "only observe",
		"a findby-only resource is a legitimate shape; the message must say so rather than imply error")
}

// THE DEFECT ITSELF, pinned on the source because the branch cannot be reached from a unit test.
//
// `Create` needs a fully wired definition getter and swagger info to reach the create builder at all
// — TestCreateProceedsWhenNotDeleting asserts exactly that by checking the call fails at the
// "swagger info getter" step. So a unit test cannot drive a findby-only definition through to this
// branch, and an end-to-end arm belongs in the integration suite.
//
// What CAN be asserted here is the thing that actually went wrong: the branch returned nil. Returning
// nil tells the runtime the create SUCCEEDED, and the runtime records a Normal
// `CreatedExternalResource` event reading "Successfully requested creation of external resource"
// (unstructured-runtime worker.go:1296). So the old code did not fail to report a problem — it
// reported a success that never happened, once per poll, forever.
func TestTheMissingCreateActionBranchDoesNotReportSuccess(t *testing.T) {
	b, err := os.ReadFile("restResources.go")
	require.NoError(t, err)
	src := string(b)
	require.NotEmpty(t, src, "restResources.go is empty, so every assertion below would be vacuous")

	// SCOPE TO Create FIRST. `if apiCall == nil || callInfo == nil {` is NOT unique -- Observe's get
	// path has the same line, and anchoring on it alone reads the wrong branch and fails for a reason
	// that has nothing to do with this fix. (It did, on the first version of this test.)
	fn := strings.Index(src, "func (h *handler) Create(")
	require.NotEqual(t, -1, fn, "Create's signature changed; re-point this test rather than deleting it")
	body := src[fn:]
	if next := strings.Index(body[1:], "\nfunc (h *handler) "); next != -1 {
		body = body[:next+1]
	}
	require.NotContains(t, body, "API action get not found",
		"the scoped region leaked into another function, so the assertions below would read the wrong branch")

	i := strings.Index(body, "if apiCall == nil || callInfo == nil {")
	require.NotEqual(t, -1, i,
		"the missing-create-action branch is gone or reworded; if it moved, re-point this test rather "+
			"than deleting it")
	rest := body[i:]
	stop := strings.Index(rest, "resolved, err :=")
	require.NotEqual(t, -1, stop, "cannot bound the branch; the code after it changed shape")
	branch := rest[:stop]

	assert.Contains(t, branch, "customcondition.NoCreateVerbDeclared()",
		"the branch no longer sets the condition, so the only durable explanation is gone and the CR "+
			"is back to reporting nothing")

	// THE LOAD-BEARING ASSERTION. A bare `return nil` here is the defect: it reports success.
	assert.NotContains(t, branch, "\n\t\treturn nil\n",
		"the missing-create-action branch returns nil again. That tells the runtime the create "+
			"SUCCEEDED, and it emits a Normal CreatedExternalResource event saying so -- for a "+
			"resource that does not exist and never will (#624)")
}
