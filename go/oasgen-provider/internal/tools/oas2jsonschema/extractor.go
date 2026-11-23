package oas2jsonschema

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// findParameterForAction resolves field's parameter against ONE action, in the document THAT action reads.
//
// The per-action form exists because a configuration field scoped to several actions can now span two
// documents (#108), and those documents can describe the same parameter differently. Aruba's CloudServer
// is the live case: `api-version` is declared by both compute documents, required with default "1.1" in
// the 1.1 document that serves create and optional with default "1" in the 1.0 document that serves
// everything else. One resolution cannot be right for both.
// UnresolvedCause says WHY a configurationFields entry did not resolve, because four different things
// used to produce one message and only ONE of them is the author's parameter being undeclared.
//
// This exists as a precondition for #828's refusal. A refusal that fires on the wrong cause names the
// wrong fix: "check the spelling" is useless to an author whose spelling is right and whose document is
// stale, and worse than useless to one who bound the entry to an action their resource does not declare.
type UnresolvedCause int

const (
	// CauseParameterUndeclared is the ONLY cause #828 should refuse on: the action's own document
	// serves the verb's path and method, and does not declare the named parameter.
	CauseParameterUndeclared UnresolvedCause = iota
	// CauseNoSuchAction: the entry binds to an action the resource does not declare at all. An author
	// error, but a different one -- nothing about the document is wrong.
	CauseNoSuchAction
	// CausePathAbsent: the verb's path is not in the document that serves that action. Usually a stale
	// document or the wrong document for the verb, and NOT something the author can fix by renaming a
	// parameter.
	CausePathAbsent
	// CauseMethodAbsent: the path is there and the verb's method is not declared on it.
	CauseMethodAbsent
)

func (c UnresolvedCause) String() string {
	switch c {
	case CauseNoSuchAction:
		return "the resource declares no such action"
	case CausePathAbsent:
		return "the action's document does not contain the verb's path"
	case CauseMethodAbsent:
		return "the verb's method is not declared on that path"
	default:
		return "the document serving that action does not declare the parameter"
	}
}

// UnresolvedField carries the cause alongside the message, so a caller can act on WHICH failure it was
// rather than on the fact that there was one.
type UnresolvedField struct {
	Name   string
	In     string
	Action string
	Cause  UnresolvedCause
	Detail string
}

func (e *UnresolvedField) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("parameter '%s' in '%s' not found for action '%s': %s (%s)",
			e.Name, e.In, e.Action, e.Cause, e.Detail)
	}
	return fmt.Sprintf("parameter '%s' in '%s' not found for action '%s': %s",
		e.Name, e.In, e.Action, e.Cause)
}

func (g *OASSchemaGenerator) findParameterForAction(field ConfigurationField, action string) (*ParameterInfo, error) {
	// The LAST cause seen wins, and the order of the checks below makes that the most specific one: a
	// resource can declare the same action on several verbs, and reporting "no such action" because the
	// final verb in the list did not match would name a cause that is false.
	cause, detail := CauseNoSuchAction, ""
	for _, verb := range g.resourceConfig.Verbs {
		if !strings.EqualFold(verb.Action, action) {
			continue
		}
		path, ok := g.docFor(verb.Action).FindPath(verb.Path)
		if !ok {
			cause, detail = CausePathAbsent, verb.Path
			continue
		}
		ops := path.GetOperations()
		op, ok := ops[strings.ToLower(verb.Method)]
		if !ok {
			cause, detail = CauseMethodAbsent, strings.ToUpper(verb.Method)+" "+verb.Path
			continue
		}
		for _, param := range op.GetParameters() {
			if param.Name == field.FromOpenAPI.Name && param.In == field.FromOpenAPI.In {
				return &param, nil
			}
		}
		// The path and the method are both served, so this is the author's parameter being absent --
		// the one cause a refusal may name as a spelling or document-version problem.
		cause, detail = CauseParameterUndeclared, strings.ToUpper(verb.Method)+" "+verb.Path
	}
	return nil, &UnresolvedField{
		Name: field.FromOpenAPI.Name, In: field.FromOpenAPI.In,
		Action: action, Cause: cause, Detail: detail,
	}
}

// findParameterInOAS resolves field's parameter against the FIRST of its actions that declares it.
//
// Implemented on top of findParameterForAction so there is one definition of "resolve this field against
// this action" rather than two that can drift. It stays because the configuration builder still needs a
// field-level answer: whether the field resolves anywhere at all, and which parameter location (query,
// header, ...) its bucket belongs under.
func (g *OASSchemaGenerator) findParameterInOAS(field ConfigurationField) (*ParameterInfo, error) {
	// EVERY per-action error used to be discarded here and replaced with one generic sentence, so a
	// caller could not tell "the parameter is undeclared" from "that action does not exist" from "the
	// document is stale". They are kept and aggregated: a field bound to several actions can fail for a
	// DIFFERENT reason on each, and the refusal #828 decided on may only fire where every failure is
	// CauseParameterUndeclared.
	var causes []*UnresolvedField
	for _, action := range field.FromRestDefinition.Actions {
		param, err := g.findParameterForAction(field, action)
		if err == nil {
			return param, nil
		}
		var uf *UnresolvedField
		if errors.As(err, &uf) {
			causes = append(causes, uf)
		}
	}
	return nil, &UnresolvedFieldSet{
		Name: field.FromOpenAPI.Name, In: field.FromOpenAPI.In, PerAction: causes,
	}
}

// UnresolvedFieldSet is why a configurationFields entry resolved against NONE of its actions, per action.
//
// AllUndeclared is the question #828's refusal turns on: refusing when some action failed because the
// resource does not declare it, or because its document is stale, would name a cause that is false and
// send the author to fix the wrong thing.
type UnresolvedFieldSet struct {
	Name      string
	In        string
	PerAction []*UnresolvedField
}

func (e *UnresolvedFieldSet) Error() string {
	if len(e.PerAction) == 0 {
		return fmt.Sprintf("parameter '%s' in '%s' not found for any of the specified actions", e.Name, e.In)
	}
	parts := make([]string, 0, len(e.PerAction))
	for _, c := range e.PerAction {
		parts = append(parts, fmt.Sprintf("%s: %s", c.Action, c.Cause))
	}
	return fmt.Sprintf("parameter '%s' in '%s' not found for any of the specified actions (%s)",
		e.Name, e.In, strings.Join(parts, "; "))
}

// AllUndeclared reports whether every action failed because the document serving it does not declare the
// parameter -- the one cause that is the author's parameter being wrong rather than their wiring.
//
// An EMPTY set returns false deliberately: "no causes recorded" is not "all causes were this one", and a
// refusal built on a vacuous true is the shape this whole precondition exists to avoid.
func (e *UnresolvedFieldSet) AllUndeclared() bool {
	if len(e.PerAction) == 0 {
		return false
	}
	for _, c := range e.PerAction {
		if c.Cause != CauseParameterUndeclared {
			return false
		}
	}
	return true
}

// getBaseSchemaForSpec returns the base schema for the spec: the request body of the verb that DEFINES
// the resource.
//
// Precedence is explicit -- create, then dispatch, then update -- rather than whichever happens to be
// declared first. That distinction is the whole reason this is not a single loop with a widened
// condition: a resource declaring update before create in its verbsDescription would otherwise derive
// its spec from the update body, silently, on the strength of YAML ordering.
//
// The update fallback exists because a document with no create endpoint is not necessarily a read-only
// resource (#236). Aruba's CMP profile declares PUT/GET/DELETE for aiKeys and no POST at all; treating
// that as read-only gave it a spec of nothing but parameters and identifiers, so the declared update
// PUT a body missing every vendor field it was supposed to carry -- and generation reported nothing,
// because each half was behaving as written.
//
// The fallback is taken only when NO create or dispatch verb is DECLARED. A create verb that is
// declared but carries no body in an accepted MIME type keeps its existing behaviour (an empty base),
// because borrowing the update body there would inject fields into the spec of a resource that defines
// itself differently.
func (g *OASSchemaGenerator) getBaseSchemaForSpec() (*Schema, error) {
	schema, err := g.requestBodySchemaFor(ActionCreate, ActionDispatch)
	if err != nil {
		return nil, err
	}
	if schema != nil {
		return schema, nil
	}

	// Declared but bodyless: not the #236 case, so do not fall back.
	if g.declaresAnyAction(ActionCreate, ActionDispatch) {
		return &Schema{}, nil
	}

	schema, err = g.requestBodySchemaFor(ActionUpdate)
	if err != nil {
		return nil, err
	}
	if schema != nil {
		return schema, nil
	}
	return &Schema{}, nil
}

// declaresAnyAction reports whether the resource declares a verb for any of the given actions.
func (g *OASSchemaGenerator) declaresAnyAction(actions ...string) bool {
	for _, verb := range g.resourceConfig.Verbs {
		for _, action := range actions {
			if strings.EqualFold(verb.Action, action) {
				return true
			}
		}
	}
	return false
}

// requestBodySchemaFor returns the request body schema of the first of the given actions that declares
// one, trying the actions IN THE ORDER GIVEN rather than in verb-declaration order. A nil schema with a
// nil error means none of them contributed a body.
func (g *OASSchemaGenerator) requestBodySchemaFor(actions ...string) (*Schema, error) {
	for _, action := range actions {
		for _, verb := range g.resourceConfig.Verbs {
			if !strings.EqualFold(verb.Action, action) {
				continue
			}
			path, ok := g.docFor(verb.Action).FindPath(verb.Path)
			if !ok {
				return nil, fmt.Errorf("path '%s' not found in OpenAPI spec", verb.Path)
			}
			ops := path.GetOperations()
			op, ok := ops[strings.ToLower(verb.Method)]
			if !ok {
				return nil, fmt.Errorf("operation '%s' not found for path '%s'", verb.Method, verb.Path)
			}

			rb := op.GetRequestBody()
			for _, mimeType := range g.generatorConfig.AcceptedMIMETypes {
				if schema, ok := rb.Content[mimeType]; ok {
					if getPrimaryType(schema.Type) == "array" {
						schema.Properties = append(schema.Properties, Property{Name: "items", Schema: &Schema{Type: []string{"array"}, Items: schema.Items}})
						schema.Type = []string{"object"}
					}
					return schema.deepCopy(), nil
				}
			}
		}
	}
	return nil, nil
}

// getBaseSchemaForStatus returns the base schema for the status, which is the response body of the 'get' or 'findby' action.
// TODO: what about no get/findby action but only update? (maybe this could be configured in the GeneratorConfig)
func (g *OASSchemaGenerator) getBaseSchemaForStatus() (*Schema, error) {
	// A DECLARED DELEGATED-OBSERVE SCHEMA WINS, and it is tried FIRST (issue 889).
	//
	// WHY FIRST, where correlationItemSchema is deliberately last. A kind may declare observeApiRef AND
	// a get or findby verb -- #887's own rule made observeApiRef+notFoundExpr an ALTERNATIVE to them,
	// not a replacement, and the only exclusion in the set is dispatch. So when both exist the question
	// is which response describes the status, and when observeApiRef is set the observe is DELEGATED:
	// the get/findby response describes a read that never happens. Typing status from it would type the
	// status of a request nobody sends.
	//
	// correlationItemSchema is last for the mirror reason given at its own call below: a dispatch verb
	// may legally coexist with a get or findby and is the less authoritative source of the two. This one
	// is the more authoritative, so it goes at the other end.
	if schema := g.declaredObserveStatusSchema(); schema != nil {
		return schema, nil
	}

	// Try each observe action in turn and only fail if NONE yields a schema. Returning the first action's
	// error aborted before findby was ever consulted, so a resource declaring findby but no get -- the
	// normal shape for a read-only resource -- lost its status schema entirely to
	// "action 'get' not defined in resource verbs", even though findby could have supplied it (#75).
	var firstErr error
	actions := []string{ActionGet, ActionFindBy}
	for _, action := range actions {
		schema, err := ExtractSchemaForAction(g.docs, g.resourceConfig.Verbs, action, g.generatorConfig)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if schema != nil {
			return schema, nil
		}
	}

	// A dispatch resource observes through its correlation collection and has no get or findby verb to
	// consult, so it is tried LAST and only once the ordinary sources have produced nothing. Last rather
	// than first because a dispatch verb may legally coexist with a get or findby (CEL forbids only
	// create and update), and where one exists it is the better status source: it describes the resource,
	// while the collection item describes an invocation of it.
	//
	// Its error REPLACES firstErr. A dispatch resource's get/findby failure is "no such verb", which is
	// not a defect for this kind and would bury the real problem -- a mistyped collection path.
	if schema, err := g.correlationItemSchema(); err != nil {
		return nil, err
	} else if schema != nil {
		return schema, nil
	}

	if firstErr != nil {
		return nil, firstErr
	}
	return nil, nil
}

// ExtractSchemaForAction resolves targetAction's path against the document that action belongs to.
//
// For findby it returns ONE ITEM of the collection; for every other action, the response schema as it
// stands. The path/operation/response walk itself lives in extractRawResponseSchemaForAction, which
// warnIdentifiersMatchPolicyOmitted also uses -- it needs the ENVELOPE rather than the item, to ask how
// many items the response can carry. Sharing the walk is deliberate: a second copy of it would be a
// second thing to keep in step with per-verb document resolution.
func ExtractSchemaForAction(docs *DocumentSet, verbs []Verb, targetAction string, config *GeneratorConfig) (*Schema, error) {
	schema, itemsPath, err := extractRawResponseSchemaForAction(docs, verbs, targetAction, config)
	if err != nil {
		return nil, err
	}

	// For 'findby' we want ONE ITEM of the collection, not the response envelope.
	//
	// This used to unwrap only when the 200 schema was itself an array, so an envelope --
	// {total, values:[...]}, which is what most real APIs return -- left the envelope as the base schema.
	// Everything downstream then looked for identifiers that were a level deeper than it was searching:
	// their types degraded to string, and the not-resolvable warning fired identically whether the
	// identifier was valid or genuinely absent, which made it useless for the one job it exists to do
	// (#110).
	if strings.EqualFold(targetAction, ActionFindBy) {
		return unwrapFindByItems(schema, itemsPath)
	}
	return schema.deepCopy(), nil
}

// extractRawResponseSchemaForAction returns targetAction's success-response schema exactly as the
// document declares it, together with the matched verb's ItemsPath.
//
// "Raw" is the whole point: for a findby this is the ENVELOPE, before unwrapFindByItems reduces it to one
// item. Callers that want the item call ExtractSchemaForAction; the only caller that wants the envelope is
// the identifiersMatchPolicy warning, which asks how many items the response can carry -- a question the
// unwrapped item schema can no longer answer, since by then a collection and a single object look alike.
//
// docs.For(targetAction) is the ONLY place in the codebase where a verb meets a document, which is why
// per-verb oasPath (#108) lands here and nowhere else. While that feature is unimplemented the set holds
// one document and For always returns it, so this is the same lookup it has always been.
func extractRawResponseSchemaForAction(docs *DocumentSet, verbs []Verb, targetAction string, config *GeneratorConfig) (*Schema, string, error) {
	doc := docs.For(targetAction)
	var verbFound bool
	// Set when a declared success response carries no content at all -- see the loop below.
	var sawBodylessSuccess bool
	for _, verb := range verbs {
		if !strings.EqualFold(verb.Action, targetAction) {
			continue
		}
		verbFound = true

		path, ok := doc.FindPath(verb.Path)
		if !ok {
			return nil, "", fmt.Errorf("path '%s' not found in OAS document", verb.Path)
		}

		ops := path.GetOperations()
		op, ok := ops[strings.ToLower(verb.Method)]
		if !ok {
			return nil, "", fmt.Errorf("method '%s' not found for path '%s'", verb.Method, verb.Path)
		}

		responses := op.GetResponses()
		if responses == nil {
			continue // Or return an error if responses are expected
		}

		// A DOCUMENTED 2xx WITH NO CONTENT means the vendor says success carries no body, so there is
		// nothing to compare and the author cannot act on it (#484's sibling).
		//
		// Detected OUTSIDE the SuccessCodes loop below, and that is the whole correction: SuccessCodes
		// defaults to {200, 201}, so a 204 is never looked up there at all. My first version of this
		// checked `len(resp.Content) == 0` inside that loop and therefore fired for nothing -- the test
		// for it failed, which is how the mistake surfaced rather than shipping as a suppression that
		// suppressed nothing.
		//
		// Narrow on purpose: only a 2xx, and only when it declares NO content. A success carrying content
		// in a MIME type we do not accept is a different fact and still warns, because something was
		// returned and the generator could not use it.
		for code, resp := range responses {
			if code < 200 || code >= 300 {
				continue
			}
			if len(resp.Content) == 0 {
				sawBodylessSuccess = true
			}
		}

		// The generator default UNION this verb's declared codes. Merged rather than replaced, because the
		// API field is documented as "additional ... beyond the 2xx codes declared for the operation" and
		// because an author adding 202 must not thereby lose 200.
		for _, code := range append(append([]int{}, config.SuccessCodes...), verb.SuccessCodes...) {
			resp, ok := responses[code]
			if !ok {
				continue
			}
			for _, mimeType := range config.AcceptedMIMETypes {
				schema, ok := resp.Content[mimeType]
				if !ok || schema == nil {
					continue
				}
				return schema, verb.ItemsPath, nil
			}
		}
	}

	if !verbFound {
		return nil, "", fmt.Errorf("action '%s' not defined in resource verbs", targetAction)
	}

	if sawBodylessSuccess {
		// Wrapped so errors.Is reaches it, and the message still says what happened for anyone reading a
		// log rather than branching on the error.
		return nil, "", fmt.Errorf("%w: action '%s'", ErrSuccessResponseHasNoBody, targetAction)
	}
	return nil, "", fmt.Errorf("no suitable response schema found for action '%s'", targetAction)
}

// declaredObserveStatusSchema builds the status base schema from observeApiRef.statusSchema (issue 889).
//
// Returns nil when nothing is declared, which is NOT an error and NOT an empty schema: the caller falls
// through to the get/findby/correlation sources, which is exactly the behaviour before this field
// existed. That is what keeps the field purely additive -- no existing RestDefinition's generated CRD
// changes shape.
//
// NO ERROR RETURN, deliberately. The only thing that could fail is an unknown type name, and the CRD's
// own enum on StatusFieldType refuses those at admission -- so a value reaching here is one of the four
// scalars by construction. Returning an error would add a path no input can take, and an unreachable
// error branch is worse than none: it reads as a handled case.
//
// The property ORDER is sorted rather than map order, because Schema.Properties is a slice to preserve
// order and a generated CRD that reorders between identical inputs is an unattributable diff for a
// consumer who fingerprints theirs.
func (g *OASSchemaGenerator) declaredObserveStatusSchema() *Schema {
	declared := g.resourceConfig.ObserveStatusSchema
	if len(declared) == 0 {
		return nil
	}
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)

	schema := &Schema{Type: []string{"object"}, Properties: make([]Property, 0, len(names))}
	for _, name := range names {
		schema.Properties = append(schema.Properties, Property{
			Name:   name,
			Schema: &Schema{Type: []string{declared[name]}},
		})
	}
	return schema
}
