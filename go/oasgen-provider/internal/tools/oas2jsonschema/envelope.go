package oas2jsonschema

import (
	"fmt"
	"sort"
	"strings"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/pathparsing"
)

// ErrAmbiguousCollection is returned when a findby response is an envelope with more than one array
// property and the RestDefinition did not say which one holds the collection.
//
// It is an ERROR rather than a choice, and that is the whole design decision. The runtime side used to
// take "the first value that is a list" while ranging a Go map, and Go randomises map iteration order --
// so with two arrays it searched a different one on different reconciles. Nobody would write that rule
// deliberately; it read as a sensible default and behaved as a coin flip.
//
// Failing generation is loud, happens once, at the moment the author can still fix it, and names the
// candidates. Guessing is quiet, happens forever, and when it guesses wrong the findby reports not-found
// -- which the reconciler acts on by CREATING, so the cost of a wrong guess is a duplicated external
// resource rather than an error anyone sees (#110).
type ErrAmbiguousCollection struct {
	Candidates []string
}

func (e *ErrAmbiguousCollection) Error() string {
	return fmt.Sprintf(
		"findby response is an envelope with %d array properties (%s), so which one holds the collection "+
			"cannot be determined; declare itemsPath on the findby verb to say which",
		len(e.Candidates), strings.Join(e.Candidates, ", "))
}

// unwrapFindByItems returns the schema of ONE item of the collection a findby response carries.
//
// The rule below is the generator's half of a contract the controller must implement identically: the
// generator uses it to find the item's SCHEMA, the controller to find the item DATA. If the two picked
// different properties, the generated CRD would describe a list the controller never reads -- the same
// class of silent divergence as a field that exists in only one of the two type systems.
//
//	bare array            -> its Items, as before
//	itemsPath declared    -> that property's Items
//	exactly one array     -> that one, unambiguously
//	two or more arrays    -> *ErrAmbiguousCollection; never a guess
//	no array at all       -> the schema unchanged (a findby may legitimately return a single object)
func unwrapFindByItems(schema *Schema, itemsPath string) (*Schema, error) {
	if schema == nil {
		return nil, nil
	}

	// A bare array response. This is the case that always worked, and it needs no envelope reasoning.
	if schema.Items != nil {
		return schema.Items.deepCopy(), nil
	}

	if itemsPath != "" {
		found, err := arrayAtPath(schema, itemsPath)
		if err != nil {
			return nil, err
		}
		return found, nil
	}

	arrays := arrayPropertyNames(schema)
	switch len(arrays) {
	case 0:
		// No collection property. A findby whose 200 is a single object is legitimate -- the controller
		// treats such a body as a list of one -- so this is not an error, and the envelope IS the item.
		return schema.deepCopy(), nil
	case 1:
		for _, p := range schema.Properties {
			if p.Name == arrays[0] && p.Schema != nil && p.Schema.Items != nil {
				return p.Schema.Items.deepCopy(), nil
			}
		}
		return schema.deepCopy(), nil
	default:
		return nil, &ErrAmbiguousCollection{Candidates: arrays}
	}
}

// arrayPropertyNames returns the names of every top-level array-typed property, sorted so the error
// message is stable rather than dependent on document order.
func arrayPropertyNames(schema *Schema) []string {
	var out []string
	for _, p := range schema.Properties {
		if p.Schema != nil && p.Schema.Items != nil {
			out = append(out, p.Name)
		}
	}
	sort.Strings(out)
	return out
}

// arrayAtPath walks a declared itemsPath and returns the schema of ONE element of the array it names.
//
// A declared path that does not resolve, or resolves to something that is not an array, is an error and
// not a fallback to the single-array rule. The author stated where the collection is; if that statement
// is wrong, quietly searching somewhere else would hide the mistake at the one moment it is cheap to fix.
func arrayAtPath(schema *Schema, itemsPath string) (*Schema, error) {
	segs, err := pathparsing.ParsePath(itemsPath)
	if err != nil || len(segs) == 0 {
		return nil, fmt.Errorf("itemsPath %q is not a valid path: %v", itemsPath, err)
	}

	cur := schema
	for i, seg := range segs {
		if cur == nil {
			return nil, fmt.Errorf("itemsPath %q: no schema at %q", itemsPath, strings.Join(segs[:i], "."))
		}
		var next *Schema
		for _, p := range cur.Properties {
			if p.Name == seg {
				next = p.Schema
				break
			}
		}
		if next == nil {
			return nil, fmt.Errorf(
				"itemsPath %q names %q, which is not present in the findby response schema (available: %s)",
				itemsPath, seg, strings.Join(propertyNames(cur), ", "))
		}
		cur = next
	}

	if cur.Items == nil {
		return nil, fmt.Errorf("itemsPath %q resolves to a %s, not an array; it must name the collection property",
			itemsPath, typeOrObject(cur))
	}
	return cur.Items.deepCopy(), nil
}

func propertyNames(schema *Schema) []string {
	out := make([]string, 0, len(schema.Properties))
	for _, p := range schema.Properties {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}

func typeOrObject(schema *Schema) string {
	if len(schema.Type) > 0 {
		return strings.Join(schema.Type, "|")
	}
	return "object"
}
