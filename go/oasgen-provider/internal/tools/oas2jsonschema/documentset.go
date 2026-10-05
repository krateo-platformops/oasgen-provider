package oas2jsonschema

import "strings"

// DocumentSet resolves which OAS document a verb's path must be looked up in.
//
// A RestDefinition carries one spec.oasPath, so every verb has always come from one document. Some APIs
// split a resource's verbs across documents -- typically because an operation was introduced in a later
// API version -- and such a resource is not merely degraded but IMPOSSIBLE to express. Aruba's
// CloudServer is the motivating case: findby/get/delete live in compute-provider.json (1.0.0) while
// create lives in compute-provider_v1.1.json (1.1.0), and Aruba's own SDK pins exactly that split.
//
// This type is the seam for krateo-platformops/oasgen-provider#108. TODAY IT ALWAYS HOLDS EXACTLY ONE
// DOCUMENT and For() always returns it, so behaviour is unchanged and every existing test still applies.
// What it buys is that per-verb resolution now has exactly one place to happen, rather than being
// threaded through the twelve functions that take an OASDocument.
//
// Documents are never merged. Pre-merging would break the guarantee that a generated CRD traces back to
// one published vendor document, which this repo checksum-enforces.
type DocumentSet struct {
	def    OASDocument
	byVerb map[string]OASDocument
}

// NewDocumentSet builds a set whose every verb resolves to def.
//
// This is the only constructor while #108 is unimplemented; the per-verb form arrives with the feature.
func NewDocumentSet(def OASDocument) *DocumentSet {
	return &DocumentSet{def: def}
}

// Default is the document named by spec.oasPath.
//
// It drives everything that is a property of the RESOURCE rather than of one verb: the CRD version,
// the security schemes, and the status/identifier schema that comes from the observe document. #108
// settled those explicitly, and keeping them on Default is what makes per-verb overrides a narrow
// change rather than a redesign.
func (s *DocumentSet) Default() OASDocument {
	if s == nil {
		return nil
	}
	return s.def
}

// For returns the document a verb's path must be resolved against.
//
// Case-insensitive on the action to match ExtractSchemaForAction's own comparison, so a set keyed
// "Create" and a verb declared "create" cannot silently disagree -- that mismatch would resolve the verb
// against the default document and produce a CRD built from the wrong one, which is the exact failure
// #108's guard currently refuses to let anyone reach.
func (s *DocumentSet) For(action string) OASDocument {
	if s == nil {
		return nil
	}
	if len(s.byVerb) > 0 {
		for a, d := range s.byVerb {
			if strings.EqualFold(a, action) {
				return d
			}
		}
	}
	return s.def
}
