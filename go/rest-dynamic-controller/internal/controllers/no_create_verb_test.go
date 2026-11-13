//go:build unit || integration

package restResources

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WHAT THIS PINS, AFTER A CORRECTION MEASURED ON SHIPPED 0.64.0.
//
// `Create` returning nil when no create action is declared told the runtime the create SUCCEEDED,
// and the runtime recorded exactly that: a Normal `CreatedExternalResource` event reading
// "Successfully requested creation of external resource", once per poll, forever, for a resource
// absent at the vendor (#624). Returning an error fixes that, and it is the whole fix.
//
// THE CONDITION THIS BRANCH USED TO SET WAS INERT, and shipped inert. Returning an error sends the
// runtime down its own failure path, which does
//
//	SetConditions(el, condition.WithMessage(condition.Creating(), wrapped.Error()), ...)
//
// at unstructured-runtime worker.go:1213. Both are TypeReady and SetConditions upserts by TYPE, so
// the runtime's Creating() replaced ours every time.
//
// Measured by a consumer on two real kinds (LoadBalancer findby+get, AiKey no POST) against shipped
// 0.64.0:
//
//	Ready=False   reason=Creating        msg="create failed: no create action is declared..."
//	Synced=False  reason=ReconcileError  msg=<same>
//	event Warning/CannotCreateExternalResource  "no create action is declared for this resource..."
//	vendor state unchanged
//
// So the DETAIL survives and the behaviour is right; the REASON is the runtime's, not ours. This
// suite asserts the error and the message, and asserts that no condition is written here -- because a
// write that is always overwritten is worse than none: it reads as a contract nobody can rely on.
func TestTheMissingCreateActionBranchReturnsAnErrorAndSetsNoCondition(t *testing.T) {
	b, err := os.ReadFile("restResources.go")
	require.NoError(t, err)
	src := string(b)
	require.NotEmpty(t, src, "restResources.go is empty, so every assertion below would be vacuous")

	// Scope to Create first: `if apiCall == nil || callInfo == nil {` is NOT unique -- Observe's get
	// path has the same line, and anchoring on it alone reads the wrong branch.
	fn := strings.Index(src, "func (h *handler) Create(")
	require.NotEqual(t, -1, fn, "Create's signature changed; re-point this test rather than deleting it")
	body := src[fn:]
	if next := strings.Index(body[1:], "\nfunc (h *handler) "); next != -1 {
		body = body[:next+1]
	}
	require.NotContains(t, body, "API action get not found",
		"the scoped region leaked into another function, so the assertions below read the wrong branch")

	i := strings.Index(body, "if apiCall == nil || callInfo == nil {")
	require.NotEqual(t, -1, i, "the missing-create-action branch is gone or reworded")
	rest := body[i:]
	stop := strings.Index(rest, "resolved, err :=")
	require.NotEqual(t, -1, stop, "cannot bound the branch; the code after it changed shape")

	// STRIP COMMENTS BEFORE ASSERTING ON CODE. The first version of this test failed against its own
	// explanation: the comment in that branch QUOTES `SetConditions(el, condition.WithMessage(...))`
	// to say why no condition is written, and a raw substring check cannot tell a quotation from a
	// call. An assertion that reads prose is testing the documentation.
	var code []string
	for _, ln := range strings.Split(rest[:stop], "\n") {
		if t := strings.TrimSpace(ln); t != "" && !strings.HasPrefix(t, "//") {
			code = append(code, ln)
		}
	}
	branch := strings.Join(code, "\n")
	require.NotEmpty(t, branch, "the branch has no non-comment lines, which cannot compile")

	// THE LOAD-BEARING ASSERTION, unchanged in purpose: a bare `return nil` here reports success.
	assert.NotContains(t, branch, "\n\t\treturn nil\n",
		"the missing-create-action branch returns nil again. That tells the runtime the create "+
			"SUCCEEDED, and it emits a Normal CreatedExternalResource event saying so -- for a "+
			"resource that does not exist and never will (#624)")

	// The error must still carry all three escapes, because the runtime wraps it into the condition
	// message and that message is the ONLY place the cause appears.
	for _, want := range []string{"no create verb", "dispatch", "createApiRef"} {
		assert.Contains(t, branch, want,
			"the error text must name %q: the runtime wraps this error into the Ready condition's "+
				"message, which is where an operator reads the cause", want)
	}

	// AND NO CONDITION IS WRITTEN HERE. A write the runtime always replaces is a contract nobody can
	// rely on, and documenting a reason that never reaches a CR sends readers grepping for nothing.
	assert.NotContains(t, branch, "SetConditions",
		"this branch sets a condition again. Returning an error makes the runtime set "+
			"Creating()+message at worker.go:1213, and SetConditions upserts by TYPE, so any TypeReady "+
			"condition written here is overwritten before the reconcile returns. Measured on shipped "+
			"0.64.0: the reason observed was Creating, and NoCreateVerbDeclared appeared nowhere")
}

// The reason constant and its constructor are GONE, asserted so they are not reintroduced by someone
// reading the issue and assuming the condition was simply forgotten.
func TestTheRemovedConditionStaysRemoved(t *testing.T) {
	b, err := os.ReadFile("condition/condition.go")
	require.NoError(t, err)
	src := string(b)
	require.NotEmpty(t, src)
	require.Contains(t, src, "ReasonCreateUnreachable",
		"control: the sibling reason must still be here, or this file is not the one being checked")

	assert.NotContains(t, src, "NoCreateVerbDeclared",
		"the NoCreateVerbDeclared condition is back. It cannot reach a CR from the missing-create-action "+
			"branch: returning an error makes the runtime overwrite it with Creating(). If a future "+
			"change gives this provider the final TypeReady write on that path, reintroduce it then and "+
			"delete this assertion with a measurement showing it survives")
}
