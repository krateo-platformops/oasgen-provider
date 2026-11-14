package restResources

import (
	"context"
	"fmt"
	"strings"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/jqengine"
)

// declaresVerb reports whether the resource declares a verb for action.
//
// Used to tell two cases apart that IsResourceKnown collapses into one `false`: a kind with NO get verb
// (unobservable by declaration) and a kind WITH a get verb whose identifier is missing (observable in
// principle, and we have lost the handle). Only the second is a defect, and only the second must refuse
// to answer (issue 682).
func declaresVerb(verbs []getter.VerbsDescription, action string) bool {
	for _, v := range verbs {
		if strings.EqualFold(v.Action, action) {
			return true
		}
	}
	return false
}

// notFoundBodyForAction returns the NotFoundBody predicate declared on the verb matching action, or nil.
func notFoundBodyForAction(verbs []getter.VerbsDescription, action string) *getter.JQProgram {
	for _, v := range verbs {
		if strings.EqualFold(v.Action, action) {
			return v.NotFoundBody
		}
	}
	return nil
}

// evalNotFoundBody runs the NotFoundBody gojq predicate against the (raw) observe-response body and reports
// whether the external resource should be treated as not existing.
func evalNotFoundBody(ctx context.Context, prog *getter.JQProgram, body interface{}) (bool, error) {
	return evalBoolPredicate(ctx, prog, body, "notFoundBody")
}

// evalBoolPredicate runs a gojq boolean predicate against body and returns its result. A nil program, or one
// with no inline source, yields (false, nil). The program must return a boolean; any other result is an
// error so a mistyped predicate fails loudly rather than silently reading as false. label names the field in
// error messages.
func evalBoolPredicate(ctx context.Context, prog *getter.JQProgram, body interface{}, label string) (bool, error) {
	if prog == nil {
		return false, nil
	}
	if prog.Inline == "" {
		// A module ref (configmap:// / http(s)://) is materialized into Inline by the definitiongetter at
		// load time, so a still-present Ref here means resolution was skipped/failed — fail loudly.
		if prog.Ref != "" {
			return false, fmt.Errorf("%s jq module %q was not resolved to inline source", label, prog.Ref)
		}
		return false, nil
	}
	p, err := jqengine.Compile(prog.Inline)
	if err != nil {
		return false, fmt.Errorf("compiling %s: %w", label, err)
	}
	out, err := p.Run(ctx, body)
	if err != nil {
		return false, fmt.Errorf("running %s: %w", label, err)
	}
	b, ok := out.(bool)
	if !ok {
		return false, fmt.Errorf("%s must return a boolean, got %T", label, out)
	}
	return b, nil
}
