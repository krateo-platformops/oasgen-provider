package restResources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/fieldmapping"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/jqengine"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/pathparsing"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// lookupRequestTimeout bounds a single dependent-lookup read. A lookup runs BEFORE the call it feeds, so
// a hanging lookup stalls the reconcile without ever reaching the API it was preparing for.
const lookupRequestTimeout = 30 * time.Second

// errLookupReferentMissing reports that a match found no item, on a lookup declaring onMissing: requeue.
//
// It is a distinct type rather than a generic error because the two outcomes need opposite handling: a
// referent that does not exist YET is the expected case while the CR that defines it is still converging,
// and must become a requeue, whereas every other lookup failure is a real error. Collapsing them would
// either spin forever on a typo'd alias or fail permanently on an ordering race.
type errLookupReferentMissing struct {
	lookup string
	want   string
}

func (e *errLookupReferentMissing) Error() string {
	return fmt.Sprintf("dependent lookup %q found no item matching %q", e.lookup, e.want)
}

// isLookupReferentMissing reports whether err is a missing-referent signal.
func isLookupReferentMissing(err error) bool {
	var target *errLookupReferentMissing
	return errors.As(err, &target)
}

// runDependentLookups executes the resource's dependent lookups that the given field mappings actually
// need, in declaration order, and returns their values keyed by lookup name.
//
// Only what is needed runs. A resolver whose `when` guard is satisfied -- the CR already supplies the
// value -- contributes nothing, and a lookup reached only through such a resolver is never executed. That
// is the guard's entire purpose: an explicitly supplied sha must not cost two API calls to be ignored.
//
// Resolution is in declaration order and a parameter may only read an EARLIER lookup, which admission
// enforces. So a single forward pass is sufficient and there is no cycle to detect here.
func (h *handler) runDependentLookups(
	ctx context.Context,
	clients *clientSet,
	info *getter.Info,
	mapping []getter.FieldMappingItem,
	mg *unstructured.Unstructured,
) (map[string]interface{}, error) {
	if info == nil || len(info.Resource.DependentLookups) == 0 {
		return nil, nil
	}

	needed, err := neededLookupNames(info.Resource.DependentLookups, mapping, mg)
	if err != nil {
		return nil, err
	}
	if len(needed) == 0 {
		return nil, nil
	}

	out := make(map[string]interface{}, len(needed))
	for _, l := range info.Resource.DependentLookups {
		if !slices.Contains(needed, l.Name) {
			continue
		}
		v, err := h.runOneLookup(ctx, clients, l, mg, out)
		if err != nil {
			return nil, err
		}
		out[l.Name] = v
	}
	return out, nil
}

// neededLookupNames returns the names of the lookups that must run, including those reached only as a
// parameter of another lookup.
//
// The walk is BACKWARD through declaration order. Because a parameter may only reference an earlier
// lookup, every dependency of a needed lookup sits before it, so one reverse pass closes the set -- no
// fixpoint loop, no visited bookkeeping.
func neededLookupNames(
	lookups []getter.DependentLookup,
	mapping []getter.FieldMappingItem,
	mg *unstructured.Unstructured,
) ([]string, error) {
	needed := map[string]bool{}

	for _, m := range mapping {
		if m.Resolver == nil || m.Resolver.Type != "dependentLookup" || m.Resolver.DependentLookup == nil {
			continue
		}
		skip, err := lookupGuardSatisfied(m.Resolver.DependentLookup.When, mg)
		if err != nil {
			return nil, err
		}
		if skip {
			continue
		}
		needed[m.Resolver.DependentLookup.Name] = true
	}

	for i := len(lookups) - 1; i >= 0; i-- {
		if !needed[lookups[i].Name] {
			continue
		}
		for _, p := range lookups[i].Parameters {
			if p.FromLookup != "" {
				needed[p.FromLookup] = true
			}
		}
	}

	names := make([]string, 0, len(needed))
	for _, l := range lookups {
		if needed[l.Name] {
			names = append(names, l.Name)
		}
	}
	return names, nil
}

// lookupGuardSatisfied reports whether the guard means the lookup can be SKIPPED, i.e. the CR already
// carries the value. A nil guard never skips.
func lookupGuardSatisfied(when *getter.LookupWhen, mg *unstructured.Unstructured) (bool, error) {
	if when == nil || when.CustomResourceFieldAbsent == "" {
		return false, nil
	}
	segments, err := pathparsing.ParsePath(when.CustomResourceFieldAbsent)
	if err != nil || len(segments) == 0 {
		return false, fmt.Errorf("invalid when.customResourceFieldAbsent path %q", when.CustomResourceFieldAbsent)
	}
	val, found, err := pathparsing.GetNestedField(mg.Object, segments)
	if err != nil {
		return false, fmt.Errorf("reading when.customResourceFieldAbsent %q: %w", when.CustomResourceFieldAbsent, err)
	}
	// Present AND non-nil means the value is already supplied, so the lookup is skipped. An explicit null
	// counts as absent: it is how a CR says "I am not supplying this".
	return found && val != nil, nil
}

// runOneLookup executes a single lookup and extracts its value.
func (h *handler) runOneLookup(
	ctx context.Context,
	clients *clientSet,
	l getter.DependentLookup,
	mg *unstructured.Unstructured,
	earlier map[string]interface{},
) (interface{}, error) {
	cli, err := clients.ForLookup(ctx, l)
	if err != nil {
		return nil, fmt.Errorf("dependent lookup %q: %w", l.Name, err)
	}

	params, query, err := bindLookupParameters(ctx, l, mg, earlier)
	if err != nil {
		return nil, err
	}

	conf := &restclient.RequestConfiguration{
		Method:     l.Method,
		Parameters: params,
		Query:      query,
	}
	resp, err := cli.Call(ctx, &http.Client{Timeout: lookupRequestTimeout}, l.Path, conf)
	if err != nil {
		return nil, fmt.Errorf("dependent lookup %q (%s %s): %w", l.Name, l.Method, l.Path, err)
	}

	if l.Match != nil {
		return matchLookupItem(ctx, l, resp.ResponseBody, mg)
	}
	return selectLookupValue(ctx, l, resp.ResponseBody)
}

// bindLookupParameters resolves a lookup's own path and query parameters, from the CR or from an earlier
// lookup's result, applying any valueMapping.
func bindLookupParameters(
	ctx context.Context,
	l getter.DependentLookup,
	mg *unstructured.Unstructured,
	earlier map[string]interface{},
) (params, query map[string]string, err error) {
	params = map[string]string{}
	query = map[string]string{}

	for _, p := range l.Parameters {
		var raw interface{}

		switch {
		case p.FromLookup != "":
			v, ok := earlier[p.FromLookup]
			if !ok {
				// Unreachable given admission's earlier-only rule plus the needed-set walk, so if it
				// happens one of those two is wrong and the request must not go out regardless.
				return nil, nil, fmt.Errorf("dependent lookup %q reads %q, which has not been resolved", l.Name, p.FromLookup)
			}
			raw = v
		default:
			segments, perr := pathparsing.ParsePath(p.InCustomResource)
			if perr != nil || len(segments) == 0 {
				return nil, nil, fmt.Errorf("dependent lookup %q: invalid inCustomResource path %q", l.Name, p.InCustomResource)
			}
			v, found, gerr := pathparsing.GetNestedField(mg.Object, segments)
			if gerr != nil || !found {
				return nil, nil, fmt.Errorf("dependent lookup %q: %q not found on the custom resource", l.Name, p.InCustomResource)
			}
			raw = v
		}

		if p.ValueMapping != nil {
			raw, err = applyLookupValueMapping(ctx, l.Name, p, raw)
			if err != nil {
				return nil, nil, err
			}
		}

		s := fmt.Sprintf("%v", raw)
		if p.InPath != "" {
			params[p.InPath] = s
		} else {
			query[p.InQuery] = s
		}
	}
	return params, query, nil
}

// applyLookupValueMapping runs a parameter's value transform, through the SAME helpers the verb path uses.
//
// Reused rather than reimplemented so there is one definition of what an alias or a jq valueMapping means;
// a second copy is how the two drift, and it would also have to be fixed twice to support a jq module Ref
// (neither supports one today). A lookup parameter is request-direction, CR value -> API value.
func applyLookupValueMapping(ctx context.Context, lookupName string, p getter.LookupParameter, in interface{}) (interface{}, error) {
	switch p.ValueMapping.Type {
	case "alias":
		return fieldmapping.ApplyAlias(in, p.ValueMapping.Aliases, fieldmapping.RequestCRToAPI), nil
	case "jq":
		what := fmt.Sprintf("dependent lookup %q parameter %q", lookupName, p.InPath+p.InQuery)
		return fieldmapping.ApplyValueJQ(ctx, p.ValueMapping.JQ, in, what)
	default:
		return nil, fmt.Errorf("dependent lookup %q: unknown valueMapping type %q", lookupName, p.ValueMapping.Type)
	}
}

// selectLookupValue extracts the value from a single-object response.
func selectLookupValue(ctx context.Context, l getter.DependentLookup, body interface{}) (interface{}, error) {
	prog, err := jqengine.Compile(l.Select)
	if err != nil {
		return nil, fmt.Errorf("dependent lookup %q: compiling select %q: %w", l.Name, l.Select, err)
	}
	out, err := prog.Run(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("dependent lookup %q: running select %q: %w", l.Name, l.Select, err)
	}
	if out == nil {
		return nil, fmt.Errorf("dependent lookup %q: select %q resolved to null", l.Name, l.Select)
	}
	return out, nil
}

// matchLookupItem finds the ONE item whose field equals the CR's value and takes a field off it.
//
// Ambiguity is an error, always. The removed apiLookup resolver scoped its list server-side and then took
// items[0]; against Keycloak, whose /authentication/flows ignores ?alias= and returns every flow, that
// bound the WRONG id while reporting success. There is deliberately no option to restore that behaviour,
// so an API that cannot distinguish its items fails loudly instead of silently binding whichever came
// first.
func matchLookupItem(ctx context.Context, l getter.DependentLookup, body interface{}, mg *unstructured.Unstructured) (interface{}, error) {
	m := l.Match

	itemsAt := m.ItemsAt
	if itemsAt == "" {
		itemsAt = "."
	}
	prog, err := jqengine.Compile(itemsAt)
	if err != nil {
		return nil, fmt.Errorf("dependent lookup %q: compiling itemsAt %q: %w", l.Name, itemsAt, err)
	}
	raw, err := prog.Run(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("dependent lookup %q: running itemsAt %q: %w", l.Name, itemsAt, err)
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("dependent lookup %q: itemsAt %q did not yield an array", l.Name, itemsAt)
	}

	segments, perr := pathparsing.ParsePath(m.EqualsFromCustomResource)
	if perr != nil || len(segments) == 0 {
		return nil, fmt.Errorf("dependent lookup %q: invalid equalsFromCustomResource path %q", l.Name, m.EqualsFromCustomResource)
	}
	wantRaw, found, gerr := pathparsing.GetNestedField(mg.Object, segments)
	if gerr != nil || !found {
		return nil, fmt.Errorf("dependent lookup %q: %q not found on the custom resource", l.Name, m.EqualsFromCustomResource)
	}
	want := fmt.Sprintf("%v", wantRaw)

	var hits []interface{}
	for _, it := range items {
		obj, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		if fmt.Sprintf("%v", obj[m.WhereField]) == want {
			hits = append(hits, obj[m.SelectField])
		}
	}

	switch len(hits) {
	case 1:
		if hits[0] == nil {
			return nil, fmt.Errorf("dependent lookup %q: matched an item whose %q is null", l.Name, m.SelectField)
		}
		return hits[0], nil
	case 0:
		if !strings.EqualFold(m.OnMissing, "error") {
			return nil, &errLookupReferentMissing{lookup: l.Name, want: want}
		}
		return nil, fmt.Errorf("dependent lookup %q: no item has %s == %q", l.Name, m.WhereField, want)
	default:
		return nil, fmt.Errorf("dependent lookup %q: %d items have %s == %q; refusing to guess which one was meant", l.Name, len(hits), m.WhereField, want)
	}
}
