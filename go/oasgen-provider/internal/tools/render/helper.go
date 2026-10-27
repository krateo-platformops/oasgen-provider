package render

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/oas2jsonschema"
)

// toDomainFieldMapping converts a RestDefinition verb's field mappings into the library-agnostic
// oas2jsonschema representation used by the generator. It maps every unified FieldMapping entry
// (carrying the value-transform kind) and also translates the deprecated request-only RequestFieldMapping
// entries into equivalent request-direction entries, so the generator sees a single, complete model.
func toDomainFieldMapping(v definitionv1alpha1.VerbsDescription) []oas2jsonschema.FieldMappingEntry {
	if len(v.FieldMapping) == 0 && len(v.RequestFieldMapping) == 0 {
		return nil
	}
	out := make([]oas2jsonschema.FieldMappingEntry, 0, len(v.FieldMapping)+len(v.RequestFieldMapping))
	for _, m := range v.FieldMapping {
		var vmType string
		if m.ValueMapping != nil {
			vmType = m.ValueMapping.Type
		}
		out = append(out, oas2jsonschema.FieldMappingEntry{
			InPath:           m.InPath,
			InQuery:          m.InQuery,
			InBody:           m.InBody,
			InResponse:       m.InResponse,
			InCustomResource: m.InCustomResource,
			ValueMappingType: vmType,
		})
	}
	// Legacy requestFieldMapping: request-direction only, no value transform.
	for _, m := range v.RequestFieldMapping {
		out = append(out, oas2jsonschema.FieldMappingEntry{
			InPath:           m.InPath,
			InQuery:          m.InQuery,
			InBody:           m.InBody,
			InCustomResource: m.InCustomResource,
		})
	}
	return out
}

// expandWildcardActions expands "*" wildcard to all available verb actions
func expandWildcardActions(actions []string, verbsDescription []definitionv1alpha1.VerbsDescription) ([]string, error) {
	// Check for mixed wildcard usage first
	hasWildcard := false
	hasOthers := false
	for _, action := range actions {
		if action == "*" {
			hasWildcard = true
		} else {
			hasOthers = true
		}
	}

	if hasWildcard && hasOthers {
		return nil, fmt.Errorf("invalid configuration: '*' wildcard cannot be mixed with specific actions in the list")
	}

	if hasWildcard {
		expandedActions := make([]string, 0, len(verbsDescription))
		for _, verb := range verbsDescription {
			expandedActions = append(expandedActions, verb.Action)
		}
		return expandedActions, nil
	}

	return actions, nil
}

// defaultHandleParam is the path-parameter name rest-dynamic-controller binds the extracted async
// operation handle to when poll.handleParam is not declared. It mirrors RDC's own default; the two must
// agree or this validation would accept paths RDC then fails to resolve.
const defaultHandleParam = "operationId"

// validateAsyncPollPaths checks every verb's async.poll.path against rest-dynamic-controller's runtime
// contract, so a violation is reported when the RestDefinition is processed instead of on the first poll
// after a create has already fired.
//
// Two things must hold simultaneously, and neither was checked anywhere before:
//
//  1. The poll path must be an EXACT key of the OAS paths object. The poll call goes through the same
//     client as every other call, which resolves the path by exact string lookup — so a path differing from
//     the OAS key by even a parameter NAME is "path not found" on every poll.
//  2. The path must contain the {handleParam} token, because that is the parameter the extracted operation
//     handle is bound to. handleParam defaults to "operationId".
//
// Both used to fail only at poll time, after a create had already fired. The pair is also why handleParam
// exists: when the name was hardcoded, an author writing the OAS's own path (.../monitor/{id}) got an
// unresolved required parameter, and one writing .../{operationId} got a lookup miss — both spellings
// broken, both accepted. Declaring handleParam: id makes the vendor document work as written, and this
// check then confirms the declared name actually appears in the path.
// The poll path is resolved against the VERB's own document (#108): an async verb that overrode
// spec.oasPath declares a poll endpoint from the document it named, and checking it against the default
// would reject a path that is present exactly where the verb said it was.
func validateAsyncPollPaths(cr *definitionv1alpha1.RestDefinition, docs *oas2jsonschema.DocumentSet) error {
	if cr == nil || docs.Default() == nil {
		return nil
	}
	for i, v := range cr.Spec.Resource.VerbsDescription {
		if v.Async == nil {
			continue
		}
		pollPath := v.Async.Poll.Path
		if pollPath == "" {
			continue // required by the CRD; nothing useful to add here
		}
		handleParam := v.Async.Poll.HandleParam
		if handleParam == "" {
			handleParam = defaultHandleParam
		}
		token := "{" + handleParam + "}"
		field := fmt.Sprintf("spec.resource.verbsDescription[%d].async.poll.path", i)
		if !strings.Contains(pollPath, token) {
			return fieldErrorf(field,
				"verb %q: async.poll.path %q does not contain the %s token, so the extracted operation handle "+
					"has nothing to bind to and every poll would fail; either use the parameter name the OAS "+
					"document declares for the poll endpoint, or set async.poll.handleParam to it",
				v.Action, pollPath, token)
		}
		if _, ok := docs.For(v.Action).FindPath(pollPath); !ok {
			return fieldErrorf(field,
				"verb %q: async.poll.path %q is not a path declared in the OAS document (paths are matched by "+
					"exact string, so a differing parameter name is a miss); write the path exactly as the "+
					"document declares it and set async.poll.handleParam to its handle parameter's name",
				v.Action, pollPath)
		}
	}
	return nil
}

// toDomainCorrelation shims a dispatch verb's correlation block into the generator's domain type. nil in,
// nil out: every non-dispatch verb takes this path, and CEL already guarantees the two agree.
func toDomainCorrelation(c *definitionv1alpha1.Correlation) *oas2jsonschema.Correlation {
	if c == nil {
		return nil
	}
	return &oas2jsonschema.Correlation{
		FromSpec:         c.FromSpec,
		ObserveMethod:    c.Observe.Method,
		ObservePath:      c.Observe.Path,
		ObserveItemsPath: c.Observe.ItemsPath,
		EchoedAt:         c.EchoedAt,
	}
}

// toDomainAsync mirrors a verb's async block into the generator's library-agnostic type. Nil when the
// verb is synchronous, which is what the schema comparison keys on: an async verb's own response is an
// operation handle rather than the resource, so there is nothing to compare it against (#228).
func toDomainAsync(a *definitionv1alpha1.AsyncConfig) *oas2jsonschema.Async {
	if a == nil {
		return nil
	}
	return &oas2jsonschema.Async{
		Mode:       a.Mode,
		PollMethod: a.Poll.Method,
		PollPath:   a.Poll.Path,
		PostGet:    a.PostGet,
	}
}

// validateDispatchCorrelation refuses a dispatch verb whose correlation cannot possibly close the loop,
// here rather than at reconcile time.
//
// This is the mandatory-correlation rule from #199 made mechanical. An uncorrelated or mis-correlated
// dispatch is not a degraded resource but an actively harmful one: it reports Ready and re-fires its POST
// on every reconcile, which for a dispatch means a duplicate run every resync. That is the failure class
// behind this repo's worst regressions -- pagination duplicates, apiLookup's items[0], envelope
// inference, #194's wrong default -- every one of which passed a static suite and surfaced on a real
// install. Refusing is cheap; an undetectable duplicate-dispatch loop is not.
//
// The echoed path is NOT checked against the item schema here, deliberately. The collections these
// endpoints return are routinely free-form at exactly the point the token lives (GitHub's workflow_run
// `inputs` is an open object), so requiring the leaf to be a declared property would refuse the
// motivating example. What IS checkable -- and is checked -- is that the collection endpoint exists and
// that the token names a spec field the CR can actually carry.
func validateDispatchCorrelation(cr *definitionv1alpha1.RestDefinition, docs *oas2jsonschema.DocumentSet) error {
	if cr == nil || docs == nil || docs.Default() == nil {
		return nil
	}
	for i, v := range cr.Spec.Resource.VerbsDescription {
		if !strings.EqualFold(v.Action, oas2jsonschema.ActionDispatch) || v.Correlation == nil {
			continue
		}
		field := fmt.Sprintf("spec.resource.verbsDescription[%d].correlation", i)
		doc := docs.For(v.Action)
		if doc == nil {
			continue
		}

		path, ok := doc.FindPath(v.Correlation.Observe.Path)
		if !ok {
			return fieldErrorf(field+".observe.path",
				"correlation.observe.path %q is not a path declared in the OAS document (paths are matched by "+
					"exact string, so a differing parameter name is a miss); without a collection to look the "+
					"token up in, the dispatch would fire again on every reconcile",
				v.Correlation.Observe.Path)
		}
		// A document adapter may report a path without handing back an inspectable item. Skip the method
		// check rather than dereference: the path check above has already done the load-bearing work, and
		// panicking inside a validator would take down a reconcile over a diagnostic.
		if path == nil {
			continue
		}
		if _, ok := path.GetOperations()[strings.ToLower(v.Correlation.Observe.Method)]; !ok {
			return fieldErrorf(field+".observe.method",
				"correlation.observe: method %q is not declared for path %q in the OAS document",
				v.Correlation.Observe.Method, v.Correlation.Observe.Path)
		}
	}
	return nil
}

// validateCorrelationTokenInSpec refuses a dispatch whose correlation token names nothing in the
// generated spec.
//
// Only the token's ROOT segment is required to exist, and that is the whole subtlety. These endpoints
// routinely carry the token inside a free-form object -- GitHub's workflow_dispatch body is
// {ref, inputs} where `inputs` is an open object -- so "inputs.correlationId" is a perfectly good token
// that is not, and cannot be, a declared property. Requiring the full path would refuse the motivating
// case of #199. Requiring nothing would accept "nosuchfield.token", which can never match and therefore
// re-fires forever. The root is exactly the part the document does promise.
func validateCorrelationTokenInSpec(cr *definitionv1alpha1.RestDefinition, specSchema []byte) error {
	if cr == nil || len(specSchema) == 0 {
		return nil
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(specSchema, &schema); err != nil {
		// Fail open: an unparsable spec schema is a different and larger problem, and crdgen is about to
		// report it properly. Refusing here would attribute it to the correlation field.
		return nil
	}

	for i, v := range cr.Spec.Resource.VerbsDescription {
		if !strings.EqualFold(v.Action, oas2jsonschema.ActionDispatch) || v.Correlation == nil {
			continue
		}
		token := v.Correlation.FromSpec
		if token == "" {
			continue // required by the CRD; nothing useful to add here
		}
		root := strings.SplitN(token, ".", 2)[0]
		if _, ok := schema.Properties[root]; ok {
			continue
		}
		available := make([]string, 0, len(schema.Properties))
		for name := range schema.Properties {
			available = append(available, name)
		}
		sort.Strings(available)
		return fieldErrorf(fmt.Sprintf("spec.resource.verbsDescription[%d].correlation.fromSpec", i),
			"correlation.fromSpec %q names %q, which is not a field of the generated spec, so no instance "+
				"could ever supply the token and the dispatch would fire on every reconcile; the spec's "+
				"top-level fields are: %s",
			token, root, strings.Join(available, ", "))
	}
	return nil
}
