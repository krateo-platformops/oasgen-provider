package oas2jsonschema

import (
	"github.com/pb33f/libopenapi/orderedmap"
	"gopkg.in/yaml.v3"
)

// The two Kubernetes list-semantics extensions this generator passes through from an OAS document
// into the generated CRD (#650).
//
// They are what lets rest-dynamic-controller compare an array of OBJECTS by KEY rather than by
// position. A vendor that stores such an array as a set returns it in an arbitrary order -- measured
// on Keycloak, where the same declared list comes back differently on a client than on a scope -- and
// a positional comparison then reports drift that no update can retire.
const (
	ExtListType    = "x-kubernetes-list-type"
	ExtListMapKeys = "x-kubernetes-list-map-keys"
)

// allowedExtensions returns the allowlisted extensions from a libopenapi schema, decoded, or nil when
// the document declares none of them.
//
// Returning nil rather than an empty map matters: `schemaToMap` guards on `len(schema.Extensions) > 0`,
// and the reflection guard in schema_reflection.go compares field-by-field, so an empty non-nil map
// would be a shape change on every schema in every document for no reason.
//
// TYPES ARE NORMALISED, not passed through as whatever YAML decoded to. `x-kubernetes-list-type` must
// reach the CRD as a string and `x-kubernetes-list-map-keys` as an array of strings, because that is
// what apiextensions validates against. A document declaring `x-kubernetes-list-map-keys: name` (a
// bare scalar rather than a sequence) is a plausible authoring mistake, and emitting it unchanged
// would make the apiserver refuse the WHOLE CRD -- so a scalar is lifted into a single-element list,
// and anything that is neither a string nor a list of strings is dropped. Dropping is reported by
// warnListSemantics rather than being silent.
func allowedExtensions(ext *orderedmap.Map[string, *yaml.Node]) map[string]interface{} {
	if ext == nil {
		return nil
	}
	var out map[string]interface{}
	for _, key := range []string{ExtListType, ExtListMapKeys} {
		n := ext.GetOrZero(key)
		if n == nil {
			continue
		}
		value := normaliseExtension(key, decodeYAMLNode(n))
		if value == nil {
			continue
		}
		if out == nil {
			out = map[string]interface{}{}
		}
		out[key] = value
	}
	return out
}

// normaliseExtension IS THE ALLOWLIST. One gate, not two.
//
// An earlier version of this file had a `passThroughExtensions` set AND a type switch, and only the
// switch was load-bearing: the set narrowed which keys were looked up, but an unknown key reaching the
// switch fell through to a nil value and was dropped anyway. So removing the set changed nothing, the
// allowlist test could not tell the two apart, and a reader would reasonably have believed the set was
// the protection. Two mechanisms where one is decorative is worse than one, because it is the
// decorative one a future edit removes.
//
// `default` is therefore explicit and returns nil. Adding a key here is a deliberate act with a
// consumer-visible consequence: `schemaToMap` copies every entry of `Schema.Extensions` verbatim into
// the emitted schema, so anything that gets past this function lands in every affected consumer's
// generated CRD -- and at least one of them fingerprints theirs and treats an unattributable diff as a
// spoiled instrument. The emit side cannot filter, because by then the provenance is gone.
//
// `x-nullable` is deliberately NOT allowed through: the adapter already consumes it into
// `Schema.Nullable`, and it has no business reaching the CRD as an extension.
func normaliseExtension(key string, decoded interface{}) interface{} {
	switch key {
	case ExtListType:
		// A string, and only the values apiextensions accepts. An unrecognised list type would make
		// the apiserver refuse the whole CRD, taking down every kind in it rather than this field.
		s, ok := decoded.(string)
		if !ok {
			return nil
		}
		switch s {
		case "atomic", "set", "map":
			return s
		}
		return nil
	case ExtListMapKeys:
		return normaliseStringList(decoded)
	default:
		return nil
	}
}

// normaliseStringList accepts a string or a list of strings and returns []interface{} of strings.
// Anything else returns nil, which the caller treats as "not declared".
//
// []interface{} rather than []string because this value is JSON-marshalled straight into the CRD, and
// a []string would marshal identically while making the reflection guard's deep-compare see a
// different type from what a round-trip produces.
func normaliseStringList(v interface{}) []interface{} {
	switch t := v.(type) {
	case string:
		// A bare scalar where a sequence was meant. Lifting it is strictly better than dropping it:
		// the author's intent is unambiguous and the alternative is a CRD the apiserver refuses.
		if t == "" {
			return nil
		}
		return []interface{}{t}
	case []interface{}:
		if len(t) == 0 {
			return nil
		}
		out := make([]interface{}, 0, len(t))
		for _, e := range t {
			s, ok := e.(string)
			if !ok || s == "" {
				return nil // one bad element invalidates the whole declaration
			}
			out = append(out, s)
		}
		return out
	}
	return nil
}
