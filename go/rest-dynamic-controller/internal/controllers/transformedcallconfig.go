package restResources

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/builder"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/fieldmapping"
)

// buildTransformedCallConfig assembles a request body and applies the verb's whole-document
// requestTransform. They are ONE step and must never again be two.
//
// WHY THIS EXISTS (issue 718). Create, Update and Delete each did the pair in place, and `dispatch` --
// a hand-built call path next to the three -- did only the first half. `callInfo.RequestTransform` is
// populated by the builder for EVERY action, so a `requestTransform` declared on an `action: dispatch`
// verb was parsed, validated, admitted, plumbed all the way to the call, and then dropped. No dispatch
// test mentioned it.
//
// THE FAILURE IS SILENT, WHICH IS WHY IT IS WORSE THAN A 422. A dispatch's correlation token is the only
// handle on what the invocation created: the next Observe finds it again in a collection. Where the
// token is injected BY the transform, an untransformed body still gets accepted by some vendors -- and
// then the record exists, carries no token, can never be correlated, and the CR reports
// DispatchedButNotObserved with no error at any layer. A real vendor record is stranded and nothing says
// so. A 422 would have been the good outcome.
//
// ONE HELPER, FOUR CALLERS, deliberately -- rather than a fourth copy of the pair. The three copies were
// identical and the fourth place was the one that was wrong; a fix that edited the three in place would
// leave exactly this shape for the next action someone adds. A feature reaching one path for free
// through a shared helper and skipping every hand-built one is how #249 shipped OAS-3-only.
//
// A NIL CONFIG IS PASSED THROUGH, NOT RAISED, and that is behaviour-preserving rather than tidy.
// BuildCallConfig returns nil when callInfo or mg is nil. Create and Update already guarded the
// transform with `if reqConfiguration != nil` and then called the API with whatever they had; dispatch
// raised its own error on nil. Deciding here would change one of those, so the decision stays with the
// caller and this function only promises not to transform what it cannot.
func buildTransformedCallConfig(
	ctx context.Context,
	callInfo *builder.CallInfo,
	mg *unstructured.Unstructured,
	configSpec map[string]interface{},
	resolved map[string]interface{},
) (*restclient.RequestConfiguration, error) {
	conf := builder.BuildCallConfig(ctx, callInfo, mg, configSpec, resolved)
	if conf == nil {
		return nil, nil
	}
	// Applied to the assembled body immediately before the call -- AFTER the per-field mappings have
	// composed it, so the program sees the finished article. A failure must fail the caller rather than
	// send a partially transformed body.
	body, err := fieldmapping.ApplyRequestTransform(ctx, callInfo.RequestTransform, conf.Body)
	if err != nil {
		return nil, err
	}
	conf.Body = body
	return conf, nil
}
