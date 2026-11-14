//go:build unit || integration

package restResources

import (
	"os"
	"strings"
	"testing"

	customcondition "github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
	"github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WHAT THIS PINS, AFTER TWO CHANGES OF CONTRACT ON THE SAME BRANCH.
//
// THE CURRENT CONTRACT (0.66.0, unstructured-runtime v1.17.0): the branch returns
// CreateResult{Requested: false} with a NIL error, carrying both halves of the refusal condition.
// The runtime suppresses the phantom event itself and keeps the conditions, so the reason reaches the
// CR and core-provider's rollup can classify it.
//
// THE HISTORY MATTERS because the assertions here inverted twice and each inversion was right at the
// time. Below is the previous rationale, kept so nobody re-derives it as a regression.
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
func TestTheMissingCreateActionBranchRequestsNothingAndReportsWhy(t *testing.T) {
	b, err := os.ReadFile("restResources.go")
	require.NoError(t, err)
	src := string(b)
	require.NotEmpty(t, src, "restResources.go is empty, so every assertion below would be vacuous")

	// Scope to the create function first: `if apiCall == nil || callInfo == nil {` is NOT unique --
	// Observe's get path has the same line, and anchoring on it alone reads the wrong branch.
	fn := strings.Index(src, "func (h *handler) CreateWithResult(")
	require.NotEqual(t, -1, fn,
		"CreateWithResult's signature changed; re-point this test rather than deleting it")
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

	// STRIP COMMENTS BEFORE ASSERTING ON CODE. An earlier version of this test failed against its own
	// explanation, because the comment in that branch quotes the calls it is describing and a raw
	// substring check cannot tell a quotation from a call.
	var code []string
	for _, ln := range strings.Split(rest[:stop], "\n") {
		if t := strings.TrimSpace(ln); t != "" && !strings.HasPrefix(t, "//") {
			code = append(code, ln)
		}
	}
	branch := strings.Join(code, "\n")
	require.NotEmpty(t, branch, "the branch has no non-comment lines, which cannot compile")

	// COLLAPSE WHITESPACE BEFORE ASSERTING ON A STRUCT LITERAL'S SHAPE, because gofmt emits three
	// different spacings for the same field and a needle can only match one of them. Measured:
	//
	//     CreateResult{Requested: true}                 -> ONE space   (single line)
	//     CreateResult{\n Requested:  true\n Conditions: -> TWO spaces  (aligned to the longest key)
	//     CreateResult{\n Requested: true\n Reason:      -> ONE space
	//
	// The first version of this test asserted on the two-space form. `Contains` merely became
	// brittle, which fails LOUDLY. But `NotContains("Requested:  true")` failed OPEN: a single-line
	// `return CreateResult{Requested: true}, nil` -- the exact regression it exists to catch -- does
	// not contain that substring, so the guard would have passed while the branch claimed a create.
	branch = collapseSpaces(branch)

	// THE CONTRACT CHANGED IN SUBSTANCE, NOT IN PURPOSE. Until 0.65.0 this branch returned an error,
	// because an error was the only way to stop the runtime recording a phantom
	// "Successfully requested creation of external resource" (#624). The cost was that the runtime's
	// create-failure branch then overwrote Ready with Creating, which is why #645 removed the reason.
	//
	// CreateResult{Requested: false} buys the same suppression from the runtime directly, and the
	// conditions survive. So the assertions invert: the branch must NOT return a bare error, and it
	// MUST declare Requested: false.
	assert.Contains(t, branch, "Requested: false",
		"the missing-create-action branch no longer returns Requested: false. Without it the runtime "+
			"reads the nil error as a successful create, stamps external-create-succeeded, and emits "+
			"the Normal CreatedExternalResource event for a resource that does not exist (#624, #657)")

	assert.Contains(t, branch, "NoCreateVerbDeclaredConditions()",
		"the branch no longer returns the conditions. The runtime keeps a handler's conditions on this "+
			"path, so this is the only thing that puts a specific reason on the CR -- and without it "+
			"the resource settles at Ready=False/Creating, which core-provider's rollup classifies as "+
			"converging forever (#660)")

	// A BARE `return nil` WOULD BE THE OLD DEFECT. Requested defaults to false on a zero value, so the
	// dangerous shape is not `return nil` any more -- it is returning a result that claims Requested.
	assert.NotContains(t, branch, "Requested: true",
		"this branch claims a create was requested. Nothing was called.")

	// The three escapes must still be named SOMEWHERE the operator reads. They moved from an error
	// string into the condition message, so this asserts the constructor is used (above) and that the
	// message itself still carries them (below, in the condition package's own test).
	assert.NotContains(t, branch, "fmt.Errorf(",
		"the branch builds an error again. An error return takes the runtime's error path, which "+
			"overwrites Ready with Creating -- the exact reason #645 had to remove the condition")
}

// THE REVIVED CONDITION, AND WHY THE ASSERTION IT REPLACED WAS RIGHT TO DEMAND A MEASUREMENT.
//
// The test this replaces asserted the reason stayed REMOVED, and named its own deletion condition:
// "if a future change gives this provider the final TypeReady write on that path, reintroduce it then
// and delete this assertion with a measurement showing it survives".
//
// What I have is a verified READING of the released unstructured-runtime v1.17.0, not a cluster
// measurement: createNotRequested removes external-create-pending, does not stamp
// external-create-succeeded, emits no CreatedExternalResource, and calls setCreateConditions -- which
// stamps the default Synced=ReconcileSuccess ONLY when the handler returns no TypeSynced of its own.
//
// SO THE MEASUREMENT IS STILL OWED. aruba-provider-kog has offered it on LoadBalancer and AiKey, the
// only consumer instances of a kind that can never be created, once both sides ship. Until that
// lands, "the reason survives" is a code reading and this comment says so rather than implying a
// measurement exists.
func TestTheRevivedConditionCarriesBothHalves(t *testing.T) {
	b, err := os.ReadFile("condition/condition.go")
	require.NoError(t, err)
	src := string(b)
	require.NotEmpty(t, src)
	require.Contains(t, src, "ReasonCreateUnreachable",
		"control: the sibling reason must still be here, or this file is not the one being checked")

	conds := customcondition.NoCreateVerbDeclaredConditions()
	require.Len(t, conds, 2,
		"a refusal must carry BOTH halves. The runtime stamps Synced=ReconcileSuccess when a handler "+
			"returns no TypeSynced -- correct for a WAIT, and wrong for a REFUSAL, where it would report "+
			"a successful sync on a resource that can never exist")

	byType := map[string]metav1.Condition{}
	for _, c := range conds {
		byType[c.Type] = c
	}
	ready, okR := byType[condition.TypeReady]
	synced, okS := byType[condition.TypeSynced]
	require.True(t, okR, "no TypeReady condition; the rollup reads Ready and nothing else")
	require.True(t, okS, "no TypeSynced condition, so the runtime will stamp ReconcileSuccess over it")

	for _, c := range []metav1.Condition{ready, synced} {
		assert.Equal(t, metav1.ConditionFalse, c.Status, "%s must be False", c.Type)
		assert.Equal(t, customcondition.ReasonNoCreateVerbDeclared, c.Reason,
			"%s carries reason %q; core-provider's childFailed arm is mapped on the exact string %q",
			c.Type, c.Reason, customcondition.ReasonNoCreateVerbDeclared)
	}

	// The three escapes moved out of an error string and into this message, which is now the only
	// place an operator reads the cause.
	for _, want := range []string{"no create verb", "dispatch", "createApiRef"} {
		assert.Contains(t, ready.Message, want,
			"the condition message must name %q: it replaced the error text as the only place the "+
				"cause appears", want)
	}
}

// collapseSpaces reduces every run of spaces and tabs to a single space, so an assertion on a struct
// literal's shape is independent of gofmt's key alignment. Newlines are preserved: the branch
// assertions above rely on line structure.
func collapseSpaces(s string) string {
	var b strings.Builder
	space := false
	atLineStart := true
	for _, r := range s {
		if r == ' ' || r == '\t' {
			space = true
			continue
		}
		// A pending run of whitespace becomes ONE space -- unless it is leading indentation or sits
		// before a newline, where it becomes nothing. Emitting a space there was the first bug this
		// function's own control caught: `\n\tRequested:` normalised to `\n Requested:`, which is
		// harmless for Contains but makes the function's output hard to predict and reason about.
		if space {
			if r != '\n' && !atLineStart {
				b.WriteByte(' ')
			}
			space = false
		}
		b.WriteRune(r)
		atLineStart = r == '\n'
	}
	return b.String()
}

// TestCollapseSpacesActuallyCollapses is the control for the normaliser the shape assertions depend
// on. Without it, a normaliser that silently stopped working would restore the fail-open hole it was
// added to close -- and every assertion above would still pass.
func TestCollapseSpacesActuallyCollapses(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Requested:  true", "Requested: true"},
		{"Requested:\ttrue", "Requested: true"},
		{"Requested: true", "Requested: true"},
		{"a  b\n\tc   d", "a b\nc d"},
	} {
		assert.Equal(t, tc.want, collapseSpaces(tc.in), "input %q", tc.in)
	}

	// THE POINT, stated as the thing that was broken: all three gofmt forms of the same field must
	// normalise to ONE needle, or the NotContains assertion above fails open again.
	one := "return controller.CreateResult{Requested: true}, nil"
	two := "return controller.CreateResult{\n\t\tRequested:  true,\n\t\tConditions: nil,\n\t}, nil"
	for _, form := range []string{one, two} {
		assert.Contains(t, collapseSpaces(form), "Requested: true",
			"a gofmt form of Requested:true did not normalise to the single-space needle")
	}
}
