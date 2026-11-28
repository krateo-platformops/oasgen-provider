package restclient

import (
	"fmt"
	"strings"

	"github.com/pb33f/libopenapi/datamodel/high/base"
)

// bodyShapeMaxDepth bounds the schema walk so a self-referential $ref cannot loop. Past it the sub-tree is
// OPEN, which means it passes through WHOLE: a pathological schema degrades to the behaviour this package
// had before the prune existed, never to an empty body.
const bodyShapeMaxDepth = 20

// BodyShape is what an operation's request-body schema allows, reduced to what a prune needs (issue 960).
//
// It exists because RequestedBody is a FLAT set of top-level names and its second consumer
// (updateBodyFields, feeding issue 400's unmodelled-key detection) wants exactly that, so the set cannot be
// reshaped. A nested field the CREATE schema has and the UPDATE schema lacks used to be sent on every spec
// edit, because processFields assigns the whole nested value under its top-level key.
//
// THE PRUNE IS SAFE ONLY BECAUSE OF WHAT IT REFUSES TO DELETE:
//
//   - a key the schema lists in `required`, at any depth, even when it is absent from `properties`
//     (issue 410 is the prior art for protecting a required parameter from an over-eager removal)
//   - a key whose property schema declares a `default`: it is in `properties` by construction, so the
//     ordinary rule retains it -- there is no special case, and nothing here pretends otherwise
//   - anything under a FREE-FORM object (`additionalProperties` true or a schema, no `properties`, or a bare
//     `type: object`): the vendor accepts keys the document does not enumerate, and pruning against an
//     unenumerated schema would delete everything
//
// allOf is merged, oneOf/anyOf are UNIONED (a key any branch allows is kept), and $ref is resolved by
// BuildSchema before comparison.
type BodyShape struct {
	// Open means "this sub-tree is not enumerated": it passes through whole.
	Open bool
	// Properties are the keys the schema lists, each with its own shape.
	Properties map[string]*BodyShape
	// Required are keys that are kept even when absent from Properties.
	Required map[string]struct{}
	// Items is the element shape when this is an array.
	Items *BodyShape
}

// BodyShape returns the shape of the given operation's request body, or nil when it declares none.
func (u *UnstructuredClient) BodyShape(httpMethod string, path string) (*BodyShape, error) {
	if u.DocScheme == nil || u.DocScheme.Model.Paths == nil {
		return nil, fmt.Errorf("document scheme or model is nil")
	}
	pathItem, ok := u.DocScheme.Model.Paths.PathItems.Get(path)
	if !ok {
		return nil, fmt.Errorf("path not found: %s", path)
	}
	op, ok := pathItem.GetOperations().Get(strings.ToLower(httpMethod))
	if !ok {
		return nil, fmt.Errorf("operation not found: %s", httpMethod)
	}
	if op.RequestBody == nil || op.RequestBody.Content == nil {
		return nil, nil
	}
	media, _, mErr := resolveRequestBodyMedia(op.RequestBody)
	if mErr != nil {
		return nil, fmt.Errorf("resolving request body media type for %s %s: %w", httpMethod, path, mErr)
	}
	if media == nil {
		return nil, nil
	}
	schema, err := media.Schema.BuildSchema()
	if err != nil {
		return nil, fmt.Errorf("building schema for %s: %w", path, err)
	}
	return buildBodyShape(schema, 0), nil
}

func buildBodyShape(schema *base.Schema, depth int) *BodyShape {
	if schema == nil || depth > bodyShapeMaxDepth {
		return &BodyShape{Open: true}
	}
	populateFromAllOf(schema)

	shape := &BodyShape{Required: map[string]struct{}{}}
	collectRequired(schema, shape.Required, 0)

	if len(schema.Type) > 0 && schema.Type[0] == "array" {
		if schema.Items == nil || schema.Items.N != 0 {
			return &BodyShape{Open: true}
		}
		items, err := schema.Items.A.BuildSchema()
		if err != nil {
			return &BodyShape{Open: true}
		}
		shape.Items = buildBodyShape(items, depth+1)
		return shape
	}

	// A free-form object: additionalProperties true, or a schema (typed map values).
	if ap := schema.AdditionalProperties; ap != nil {
		if ap.IsA() || (ap.IsB() && ap.B) {
			return &BodyShape{Open: true}
		}
	}

	shape.Properties = map[string]*BodyShape{}
	if schema.Properties != nil {
		for prop := schema.Properties.First(); prop != nil; prop = prop.Next() {
			child, err := prop.Value().BuildSchema()
			if err != nil {
				shape.Properties[prop.Key()] = &BodyShape{Open: true}
				continue
			}
			shape.Properties[prop.Key()] = buildBodyShape(child, depth+1)
		}
	}

	// oneOf / anyOf: UNION the branches, so a key any branch allows is kept.
	for _, group := range [][]*base.SchemaProxy{schema.OneOf, schema.AnyOf} {
		for _, proxy := range group {
			branchSchema, err := proxy.BuildSchema()
			if err != nil {
				return &BodyShape{Open: true}
			}
			branch := buildBodyShape(branchSchema, depth+1)
			if branch.Open {
				// A branch that enumerates nothing accepts anything, so the union does too.
				return &BodyShape{Open: true}
			}
			mergeBodyShape(shape, branch)
		}
	}

	// No enumerated properties at all (a bare `type: object`, or no type): nothing to compare against.
	if len(shape.Properties) == 0 {
		return &BodyShape{Open: true}
	}
	return shape
}

// collectRequired gathers `required` from the schema and from every allOf member.
func collectRequired(schema *base.Schema, out map[string]struct{}, depth int) {
	if schema == nil || depth > bodyShapeMaxDepth {
		return
	}
	for _, r := range schema.Required {
		out[r] = struct{}{}
	}
	for _, proxy := range schema.AllOf {
		member, err := proxy.BuildSchema()
		if err != nil {
			continue
		}
		collectRequired(member, out, depth+1)
	}
}

// mergeBodyShape unions src into dst. Open wins, because "the vendor accepts anything here" is the
// conservative answer when two branches disagree.
func mergeBodyShape(dst, src *BodyShape) {
	if dst == nil || src == nil {
		return
	}
	for k := range src.Required {
		dst.Required[k] = struct{}{}
	}
	for k, v := range src.Properties {
		if existing, ok := dst.Properties[k]; ok {
			if existing.Open || v.Open {
				dst.Properties[k] = &BodyShape{Open: true}
				continue
			}
			mergeBodyShape(existing, v)
			continue
		}
		dst.Properties[k] = v
	}
	if dst.Items == nil {
		dst.Items = src.Items
	}
}

// Prune returns a copy of body with every nested key the shape does not allow removed. It never mutates
// its input, because the values in an assembled body ALIAS the custom resource the reconciler holds.
//
// Top-level keys the shape does not list are kept as they are: RequestedBody already decided those, and
// this prune only owns what is NESTED under a top-level key.
func (s *BodyShape) Prune(body map[string]interface{}) map[string]interface{} {
	if s == nil || s.Open || body == nil {
		return body
	}
	out := make(map[string]interface{}, len(body))
	for k, v := range body {
		if child, ok := s.Properties[k]; ok {
			out[k] = child.pruneValue(v)
			continue
		}
		out[k] = v
	}
	return out
}

func (s *BodyShape) pruneValue(v interface{}) interface{} {
	if s == nil || s.Open {
		return v
	}
	switch val := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, item := range val {
			if child, ok := s.Properties[k]; ok {
				out[k] = child.pruneValue(item)
				continue
			}
			if _, req := s.Required[k]; req {
				out[k] = item
			}
			// else: absent from this schema and not required -- pruned.
		}
		return out
	case []interface{}:
		if s.Items == nil {
			return v
		}
		out := make([]interface{}, len(val))
		for i, item := range val {
			out[i] = s.Items.pruneValue(item)
		}
		return out
	default:
		return v
	}
}
