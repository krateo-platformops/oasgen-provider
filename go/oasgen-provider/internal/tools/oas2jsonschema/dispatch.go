package oas2jsonschema

import (
	"fmt"
	"sort"
	"strings"
)

// dispatchVerb returns the resource's dispatch verb, or nil. CEL admits at most one, so the first is the
// only one.
func (g *OASSchemaGenerator) dispatchVerb() *Verb {
	if g.resourceConfig == nil {
		return nil
	}
	for i := range g.resourceConfig.Verbs {
		if strings.EqualFold(g.resourceConfig.Verbs[i].Action, ActionDispatch) {
			return &g.resourceConfig.Verbs[i]
		}
	}
	return nil
}

// correlationItemSchema resolves the schema of ONE ITEM of a dispatch verb's correlation collection: the
// status base schema for a dispatch resource.
//
// This is the same unwrap findby performs, and deliberately the same code, because the two answer the
// same question about the same kind of response. A dispatch has no get or findby verb to observe through
// -- its observation IS this collection read -- so without this a dispatch kind would generate an empty
// status and nothing downstream could resolve the type of the field the token is echoed at.
//
// Returns a nil schema with a nil error when the resource declares no dispatch, so callers can chain it
// behind get/findby without special-casing the ordinary resource.
func (g *OASSchemaGenerator) correlationItemSchema() (*Schema, error) {
	verb := g.dispatchVerb()
	if verb == nil || verb.Correlation == nil {
		return nil, nil
	}
	c := verb.Correlation

	doc := g.docFor(verb.Action)
	if doc == nil {
		return nil, fmt.Errorf("no OAS document for the dispatch verb")
	}
	path, ok := doc.FindPath(c.ObservePath)
	if !ok {
		return nil, fmt.Errorf("correlation.observe.path %q is not a path declared in the OAS document", c.ObservePath)
	}
	op, ok := path.GetOperations()[strings.ToLower(c.ObserveMethod)]
	if !ok {
		return nil, fmt.Errorf("correlation.observe: method %q not declared for path %q", c.ObserveMethod, c.ObservePath)
	}

	responses := op.GetResponses()
	if responses == nil {
		return nil, fmt.Errorf("correlation.observe: %s %s declares no responses", c.ObserveMethod, c.ObservePath)
	}
	for _, code := range g.generatorConfig.SuccessCodes {
		resp, ok := responses[code]
		if !ok {
			continue
		}
		for _, mimeType := range g.generatorConfig.AcceptedMIMETypes {
			schema, ok := resp.Content[mimeType]
			if !ok || schema == nil {
				continue
			}
			// The ITEM, not the envelope -- exactly as findby does, and for the same reason: everything
			// downstream would otherwise look for the echoed token one level shallower than it lives.
			return unwrapFindByItems(schema, c.ObserveItemsPath)
		}
	}
	return nil, fmt.Errorf("correlation.observe: no suitable response schema found for %s %s", c.ObserveMethod, c.ObservePath)
}

// ValidateCorrelationEcho refuses an echoedAt whose ROOT segment is not a declared property of the
// correlation collection's item.
//
// This closes the last way to reach the failure the dispatch verb exists to prevent. The other checks
// cover the collection endpoint and the token's spec field, but nothing covered the ECHO path -- and a
// mistyped one never matches, so Observe reports the invocation absent forever while Create fires on
// every reconcile. A duplicate run per resync is exactly what #199 is about.
//
// Only the ROOT is required to be declared, for the same reason only fromSpec's root is: the token
// routinely lives inside a free-form object (GitHub's workflow_run `inputs`), so requiring the leaf
// would refuse the motivating case. The root is the part the document does promise. The check is skipped
// when the item declares no properties at all, or explicitly permits additional ones, since then the
// document promises nothing to check against.
func (g *OASSchemaGenerator) ValidateCorrelationEcho() error {
	verb := g.dispatchVerb()
	if verb == nil || verb.Correlation == nil || verb.Correlation.EchoedAt == "" {
		return nil
	}
	item, err := g.correlationItemSchema()
	if err != nil {
		return err
	}
	if item == nil || len(item.Properties) == 0 {
		return nil // nothing promised, nothing to check
	}
	if item.AdditionalProperties != nil {
		return nil // the item is explicitly open; an undeclared root may still be real
	}

	root := strings.SplitN(strings.TrimPrefix(verb.Correlation.EchoedAt, "."), ".", 2)[0]
	declared := make([]string, 0, len(item.Properties))
	for _, p := range item.Properties {
		if p.Name == root {
			return nil
		}
		declared = append(declared, p.Name)
	}
	sort.Strings(declared)
	return fmt.Errorf(
		"correlation.echoedAt %q names %q, which the collection's items do not declare, so the token would "+
			"never match and the dispatch would fire on every reconcile; the item's fields are: %s",
		verb.Correlation.EchoedAt, root, strings.Join(declared, ", "))
}
