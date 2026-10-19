package oas2jsonschema

import (
	"context"
	"fmt"
	"sort"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/pathparsing"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/safety"
)

// includeNode is one level of the tree built from the includedSpecFields paths.
//
// A tree rather than a flat set because dot-notation has to express both "keep this whole subtree"
// (`metadata`) and "keep this parent, but only these children" (`metadata.owner`). A flat set cannot
// tell those apart, and an author who writes both means the broader one.
type includeNode struct {
	// terminal marks a path that ends here, so this property is kept WHOLE -- every descendant, with no
	// further pruning. A parent listed explicitly outranks any of its children also being listed.
	terminal bool
	children map[string]*includeNode
}

func newIncludeNode() *includeNode {
	return &includeNode{children: map[string]*includeNode{}}
}

func (n *includeNode) insert(segments []string) {
	cur := n
	for _, seg := range segments {
		child, ok := cur.children[seg]
		if !ok {
			child = newIncludeNode()
			cur.children[seg] = child
		}
		cur = child
	}
	cur.terminal = true
}

// buildIncludeTree parses every declared path into one tree, rejecting paths it cannot parse.
func buildIncludeTree(paths []string) (*includeNode, error) {
	root := newIncludeNode()
	for _, p := range paths {
		segments, err := pathparsing.ParsePath(p)
		if err != nil {
			return nil, SchemaGenerationError{
				Code:    CodeIncludedFieldNotFound,
				Message: fmt.Sprintf("invalid path format for included field %q: %v", p, err),
			}
		}
		if len(segments) == 0 {
			return nil, SchemaGenerationError{
				Code:    CodeIncludedFieldNotFound,
				Message: fmt.Sprintf("included field %q resolves to no field path", p),
			}
		}
		root.insert(segments)
	}
	return root, nil
}

// alwaysKeptFields are the spec fields an allowlist may never drop, because the resource cannot
// function without them. See the IncludedSpecFields doc comment on the API type for the reasoning.
//
// Collected rather than inferred from the schema: a field's origin is not recoverable from the finished
// schema (a parameter and a body property look identical by then), and sniffing the "PARAMETER: " text
// the description happens to carry would be a guess dressed as a check.
type alwaysKeptFields struct {
	// requiredParameters are spec fields derived from REQUIRED OAS parameters. Dropping one leaves a kind
	// that cannot build its own URL -- exactly the breakage #197 fixed, reintroduced by configuration.
	requiredParameters map[string]bool
	// identifierSelectors are the identifiers materialised into the spec of a resource with no create
	// verb. They ARE that resource's spec; without them findby can never match.
	identifierSelectors map[string]bool
	// configurationRef is generator machinery: without it the resource cannot reference its credentials.
	configurationRef bool
}

func (a alwaysKeptFields) keeps(name string) bool {
	if a.configurationRef && name == configurationRefFieldName {
		return true
	}
	return a.requiredParameters[name] || a.identifierSelectors[name]
}

func (a alwaysKeptFields) names() []string {
	var out []string
	for n := range a.requiredParameters {
		out = append(out, n)
	}
	for n := range a.identifierSelectors {
		out = append(out, n)
	}
	if a.configurationRef {
		out = append(out, configurationRefFieldName)
	}
	sort.Strings(out)
	return out
}

const configurationRefFieldName = "configurationRef"

// retainIncludedSpecFields prunes schema down to the declared allowlist.
//
// Fatal, not advisory, on a path that names nothing: see CodeIncludedFieldNotFound.
func (g *OASSchemaGenerator) retainIncludedSpecFields(schema *Schema, kept alwaysKeptFields) error {
	if len(g.resourceConfig.IncludedSpecFields) == 0 {
		return nil
	}

	root, err := buildIncludeTree(g.resourceConfig.IncludedSpecFields)
	if err != nil {
		return err
	}

	guard := safety.NewRecursionGuard(g.generatorConfig.MaxRecursionDepth, g.generatorConfig.MaxRecursionNodes, g.generatorConfig.RecursionTimeout)
	ctx, cancel := guard.WithContext()
	defer cancel()

	// Validate BEFORE pruning. Checking afterwards would mean reporting against a schema the prune had
	// already emptied, which cannot distinguish "you misspelled it" from "it was removed because you
	// misspelled something else".
	for _, declared := range g.resourceConfig.IncludedSpecFields {
		segments, perr := pathparsing.ParsePath(declared)
		if perr != nil {
			// Already reported by buildIncludeTree; unreachable.
			continue
		}
		if !fieldExistsAtPath(ctx, schema, segments, guard, 0) {
			return SchemaGenerationError{
				Path: "spec." + declared,
				Code: CodeIncludedFieldNotFound,
				Message: fmt.Sprintf(
					"field %q set in includedSpecFields does not exist in the generated spec schema; an allowlist entry that matches nothing removes "+
						"nothing and is silently invisible in the result, so it is refused rather than warned about. Check the spelling and the "+
						"dot-notation path against the OAS document", declared),
			}
		}
	}

	pruneToIncluded(ctx, schema, root, kept, guard, 0)
	return nil
}

// fieldExistsAtPath reports whether segments resolves to a property of schema.
func fieldExistsAtPath(ctx context.Context, schema *Schema, segments []string, guard *safety.RecursionGuard, depth int) bool {
	if schema == nil || len(segments) == 0 {
		return false
	}
	if err := guard.Check(ctx, depth); err != nil {
		return false
	}
	for _, prop := range schema.Properties {
		if prop.Name != segments[0] {
			continue
		}
		if len(segments) == 1 {
			return true
		}
		return fieldExistsAtPath(ctx, prop.Schema, segments[1:], guard, depth+1)
	}
	return false
}

// pruneToIncluded removes every property that the tree does not reach.
//
// kept applies at the ROOT only: configurationRef, parameter-derived fields and identifier selectors are
// all root-level spec properties, and a nested field that merely shares a name with one of them is an
// ordinary field.
func pruneToIncluded(ctx context.Context, schema *Schema, node *includeNode, kept alwaysKeptFields, guard *safety.RecursionGuard, depth int) {
	if schema == nil || node == nil {
		return
	}
	if err := guard.Check(ctx, depth); err != nil {
		return
	}

	surviving := make([]Property, 0, len(schema.Properties))
	for _, prop := range schema.Properties {
		if depth == 0 && kept.keeps(prop.Name) {
			surviving = append(surviving, prop)
			continue
		}

		child, listed := node.children[prop.Name]
		if !listed {
			continue
		}
		// A listed parent keeps its whole subtree. Recursing into it with an empty child set would
		// delete every one of its properties, turning "keep metadata" into "keep an empty metadata".
		if !child.terminal {
			pruneToIncluded(ctx, prop.Schema, child, kept, guard, depth+1)
		}
		surviving = append(surviving, prop)
	}
	schema.Properties = surviving

	pruneRequired(schema)
}

// pruneRequired drops required entries whose property is no longer present. A required name with no
// property is a schema the apiserver accepts and no object can ever satisfy.
func pruneRequired(schema *Schema) {
	if len(schema.Required) == 0 {
		return
	}
	present := make(map[string]bool, len(schema.Properties))
	for _, p := range schema.Properties {
		present[p.Name] = true
	}
	surviving := make([]string, 0, len(schema.Required))
	for _, r := range schema.Required {
		if present[r] {
			surviving = append(surviving, r)
		}
	}
	schema.Required = surviving
}
