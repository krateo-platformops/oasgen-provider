// Package fieldmapping implements the runtime side of the RestDefinition unified field mapping.
//
// For the response direction it normalizes the observed API body into the CR-domain shape at the reconcile
// chokepoint — before status population and drift comparison — so those consumers keep working unchanged.
//
// What a transform actually does depends on WHERE it is attached, not on whether it is inline or a module
// reference:
//
//	                                 alias    jq
//	per-field, response (API -> CR)  applied  applied
//	per-field, request  (CR -> API)  applied  ENTRY SKIPPED
//	responseTransform (whole doc)    n/a      applied
//	requestTransform  (whole doc)    n/a      applied
//
// Inline and ref: are equivalent by the time execution happens: definitiongetter.resolveJQRefs walks every
// JQProgram on every definition fetch and materializeJQ rewrites Ref into Inline (appending Entrypoint),
// clearing Ref. The `Inline == ""` guards in this package are therefore a defensive fallback for a program
// that was never materialized — NOT a statement that module references are unsupported. They are.
//
// The one remaining gap is silent and worth knowing:
//
//   - A jq valueMapping on a REQUEST entry makes builder.BuildCallConfig skip that mapping entirely, so the
//     target field never reaches the outgoing body. Failing closed beats sending an untransformed value,
//     but the field simply vanishing is hard to diagnose from outside.
//   - requestTransform is applied by ApplyRequestTransform, called from Create/Update/Delete after the body
//     is assembled. It was accepted-but-never-run until oasgen-provider#43; oasgen rejected it at admission
//     in the interim so it could not be a silent no-op.
//
// Request-direction alias handling lives in builder.BuildCallConfig (ApplyAlias with RequestCRToAPI);
// everything else above is in this package.
package fieldmapping

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/async"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/jqengine"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/pathparsing"
	"strconv"
)

// Direction indicates which way a value transform is applied.
type Direction int

const (
	// ResponseAPIToCR transforms an API-domain value into the CR domain (response direction).
	ResponseAPIToCR Direction = iota
	// RequestCRToAPI transforms a CR-domain value into the API domain (request direction).
	RequestCRToAPI
)

// NormalizeResponseBody normalizes the observed body, in place, into the CR-domain shape using the
// response-direction transforms declared on the verbs matching any of the given actions. It runs, in order:
//
//  1. the whole-document responseTransform jq program (if any), which rewrites the entire body; then
//  2. each per-entry response fieldMapping, relocating a response field to its CR-domain destination
//     (dropping the stale source when it moves) and applying its value transform — a Tier-1 alias or a
//     Tier-2 inline jq program.
//
// Module-referenced (ref:) jq programs are not yet supported and are left unapplied. Execution is bounded
// by ctx. It is a no-op when no matching response transforms are declared, so unmapped resources are
// unaffected.
func NormalizeResponseBody(ctx context.Context, verbs []getter.VerbsDescription, actions []string, body map[string]interface{}) error {
	// A nil map cannot be written to (SetNestedField). An empty but non-nil body is still processed, because
	// defaultIfAbsent may need to inject a field into it.
	if body == nil {
		return nil
	}
	// 1) Whole-document responseTransform first, so per-entry mappings see a normalized body.
	for _, prog := range documentResponseTransforms(verbs, actions) {
		if err := applyDocumentTransform(ctx, prog, body); err != nil {
			return err
		}
	}
	// 2) Per-entry response field mappings, in two phases so an earlier entry that lifts a path cannot
	//    delete a later entry's source before it is read: resolve every value first (no mutation), then
	//    write, then remove lifted sources.
	var resolved []resolvedEntry
	for _, entry := range responseEntries(verbs, actions) {
		r, ok, err := resolveResponseEntry(ctx, entry, body)
		if err != nil {
			return err
		}
		if ok {
			resolved = append(resolved, r)
		}
	}
	dstKeys := make(map[string]bool, len(resolved))
	for _, r := range resolved {
		dstKeys[pathKey(r.dst)] = true
	}
	for _, r := range resolved {
		if err := pathparsing.SetNestedField(body, r.val, r.dst); err != nil {
			return fmt.Errorf("setting normalized field: %w", err)
		}
	}
	for _, r := range resolved {
		// Never remove an in-place transform, nor a source that another entry writes to (chained relocation).
		if equalPath(r.src, r.dst) || dstKeys[pathKey(r.src)] {
			continue
		}
		pathparsing.RemoveNestedField(body, r.src)
	}
	return nil
}

func pathKey(p []string) string { return strings.Join(p, "\x00") }

// documentResponseTransforms collects the inline whole-document responseTransform programs from the verbs
// whose action matches any of the requested actions, preserving order. Module-ref transforms are deferred.
func documentResponseTransforms(verbs []getter.VerbsDescription, actions []string) []*getter.JQProgram {
	var out []*getter.JQProgram
	for _, action := range actions {
		for _, verb := range verbs {
			if !strings.EqualFold(verb.Action, action) {
				continue
			}
			if verb.ResponseTransform != nil && verb.ResponseTransform.Inline != "" {
				out = append(out, verb.ResponseTransform)
			}
		}
	}
	return out
}

// applyDocumentTransform runs a whole-document jq program against the body and replaces the body's contents
// in place with the (object) result.
func applyDocumentTransform(ctx context.Context, jq *getter.JQProgram, body map[string]interface{}) error {
	prog, err := jqengine.Compile(jq.Inline)
	if err != nil {
		return fmt.Errorf("compiling responseTransform: %w", err)
	}
	out, err := prog.Run(ctx, body)
	if err != nil {
		return fmt.Errorf("running responseTransform: %w", err)
	}
	newBody, ok := out.(map[string]interface{})
	if !ok {
		return fmt.Errorf("responseTransform must output a JSON object, got %T", out)
	}
	for k := range body {
		delete(body, k)
	}
	for k, v := range newBody {
		body[k] = v
	}
	return nil
}

// responseEntries collects the response-direction (inResponse) mapping entries from the verbs whose action
// matches any of the requested actions, preserving declaration order.
func responseEntries(verbs []getter.VerbsDescription, actions []string) []getter.FieldMappingItem {
	var out []getter.FieldMappingItem
	for _, action := range actions {
		for _, verb := range verbs {
			if !strings.EqualFold(verb.Action, action) {
				continue
			}
			for _, m := range verb.FieldMapping {
				if m.InResponse != "" {
					out = append(out, m)
				}
			}
		}
	}
	return out
}

// resolvedEntry is a response mapping whose value has been read and transformed but not yet written.
type resolvedEntry struct {
	src []string
	dst []string
	val interface{}
}

// resolveResponseEntry reads the source value for a response entry, applies its value transform (Tier-1
// alias or Tier-2 inline jq), and canonicalizes the result — WITHOUT mutating the body. It returns
// ok=false when the entry is not applicable (missing anchor, source not present, or a deferred module-ref
// jq). Canonicalizing here guarantees SetNestedField never panics on a non-JSON value on any write path
// (the alias/relocate path can otherwise carry a raw int/int64/yaml-decoded value).
func resolveResponseEntry(ctx context.Context, entry getter.FieldMappingItem, body map[string]interface{}) (resolvedEntry, bool, error) {
	if entry.InResponse == "" || entry.InCustomResource == "" {
		return resolvedEntry{}, false, nil
	}
	srcPath, err := pathparsing.ParsePath(entry.InResponse)
	if err != nil || len(srcPath) == 0 {
		return resolvedEntry{}, false, nil
	}
	val, found, err := pathparsing.GetNestedField(body, srcPath)
	if err != nil {
		return resolvedEntry{}, false, nil
	}
	if !found {
		// Source absent: inject defaultIfAbsent if declared, otherwise skip. No value transform or source
		// lift applies (there is nothing to read or remove) — src == dst so the write phase leaves it be.
		if len(entry.DefaultIfAbsent) == 0 {
			return resolvedEntry{}, false, nil
		}
		def, derr := decodeDefault(entry.DefaultIfAbsent)
		if derr != nil {
			return resolvedEntry{}, false, fmt.Errorf("decoding defaultIfAbsent for response field %q: %w", entry.InResponse, derr)
		}
		dstPath, perr := bodyRelativePath(entry.InCustomResource)
		if perr != nil || len(dstPath) == 0 {
			return resolvedEntry{}, false, nil
		}
		return resolvedEntry{src: dstPath, dst: dstPath, val: def}, true, nil
	}

	if entry.ValueMapping != nil {
		switch entry.ValueMapping.Type {
		case "alias":
			val = ApplyAlias(val, entry.ValueMapping.Aliases, ResponseAPIToCR)
		case "jq":
			jq := entry.ValueMapping.JQ
			if jq == nil || jq.Inline == "" {
				// Module-referenced (ref:) jq is not yet supported; leave the entry unapplied.
				return resolvedEntry{}, false, nil
			}
			prog, cErr := jqengine.Compile(jq.Inline)
			if cErr != nil {
				return resolvedEntry{}, false, fmt.Errorf("compiling jq for response field %q: %w", entry.InResponse, cErr)
			}
			out, rErr := prog.Run(ctx, val)
			if rErr != nil {
				return resolvedEntry{}, false, fmt.Errorf("running jq for response field %q: %w", entry.InResponse, rErr)
			}
			val = out
		}
	}

	dstPath, err := bodyRelativePath(entry.InCustomResource)
	if err != nil || len(dstPath) == 0 {
		return resolvedEntry{}, false, nil
	}

	val, err = jqengine.Canonical(val)
	if err != nil {
		return resolvedEntry{}, false, fmt.Errorf("canonicalizing normalized field %q: %w", entry.InCustomResource, err)
	}
	return resolvedEntry{src: srcPath, dst: dstPath, val: val}, true, nil
}

// ApplyAlias rewrites a value across the CR<->API boundary using a finite set of bidirectional pairs.
// Any value without a matching pair passes through unchanged.
func ApplyAlias(v interface{}, aliases []getter.ValueAlias, dir Direction) interface{} {
	s := fmt.Sprintf("%v", v)
	for _, a := range aliases {
		switch dir {
		case ResponseAPIToCR:
			if s == a.APIValue {
				return a.CustomResourceValue
			}
		case RequestCRToAPI:
			if s == a.CustomResourceValue {
				return a.APIValue
			}
		}
	}
	return v
}

// bodyRelativePath converts a CR-domain destination (e.g. "spec.owner", "status.metadata.id") into a path
// relative to the response body by stripping a leading "spec." or "status." segment: the observed body is
// compared against spec and read for status by bare field paths, so the normalized value must land there.
func bodyRelativePath(inCustomResource string) ([]string, error) {
	trimmed := inCustomResource
	switch {
	case strings.HasPrefix(trimmed, "spec."):
		trimmed = strings.TrimPrefix(trimmed, "spec.")
	case strings.HasPrefix(trimmed, "status."):
		trimmed = strings.TrimPrefix(trimmed, "status.")
	}
	return pathparsing.ParsePath(trimmed)
}

// decodeDefault unmarshals a defaultIfAbsent JSON value into a JSON-canonical Go value.
func decodeDefault(raw json.RawMessage) (interface{}, error) {
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func equalPath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ApplyRequestTransform runs a whole-document requestTransform over an assembled request body and returns
// the replacement body. Input '.' is the entire body and the single output replaces it — the request-side
// mirror of the document responseTransform, and the document-scoped sibling of a per-field jq valueMapping.
//
// Ordering is deliberately the inverse of the response side. Going out, the per-field mappings compose the
// body first and this transform receives the finished article; coming in, the document transform normalizes
// the payload first and the per-field mappings read the normalized shape. Both directions therefore give
// the document program the "API-shaped" view.
//
// A nil program, or a body with nothing in it, is a no-op: there is nothing to transform, and inventing a
// body from a jq program that expected one would turn a GET into a request it was never meant to be. A
// compile or run failure is returned as an error so the caller fails the reconcile — sending a partially
// transformed body would be worse than not sending one, which is the same call the response path makes.
//
// prog.Inline is the only field read: definitiongetter.resolveJQRefs has already materialized any module
// ref into it by the time a call reaches here.
func ApplyRequestTransform(ctx context.Context, prog *getter.JQProgram, body interface{}) (interface{}, error) {
	if prog == nil || prog.Inline == "" {
		return body, nil
	}
	m, ok := body.(map[string]interface{})
	if !ok || len(m) == 0 {
		return body, nil
	}
	compiled, err := jqengine.Compile(prog.Inline)
	if err != nil {
		return nil, fmt.Errorf("compiling requestTransform: %w", err)
	}
	out, err := compiled.Run(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("running requestTransform: %w", err)
	}
	return out, nil
}

// ApplyValueJQ runs a per-field jq program over a single value and returns the canonical result.
//
// It is the REQUEST-direction counterpart of what resolveResponseEntry does inbound, and it exists so a
// path or query parameter can be transformed on its way out. That was the one part of the request surface
// with no transformation hook at all: alias needs the value set enumerated up front (branch names are
// unbounded), requestTransform operates on the request BODY and a path parameter is not in it, and a
// resolver sources a value rather than transforming one. So "strip a prefix from this path parameter
// before calling" required a Go plugin -- a deployment, service, image and release pipeline to run
// strings.TrimPrefix (#117).
//
// Module-referenced (ref:) programs are already materialized into Inline by resolveJQRefs before anything
// executes, so only Inline is read here.
func ApplyValueJQ(ctx context.Context, jq *getter.JQProgram, val interface{}, what string) (interface{}, error) {
	if jq == nil || jq.Inline == "" {
		return nil, fmt.Errorf("%s declares a jq valueMapping with no program", what)
	}
	prog, err := jqengine.Compile(jq.Inline)
	if err != nil {
		return nil, fmt.Errorf("compiling jq for %s: %w", what, err)
	}
	out, err := prog.Run(ctx, val)
	if err != nil {
		return nil, fmt.Errorf("running jq for %s: %w", what, err)
	}
	return jqengine.Canonical(out)
}

// ScalarForURL renders a transformed value for use as a path or query parameter, and REFUSES anything
// that is not a scalar.
//
// A URL component has no sensible non-scalar form. Rendering an object or array with %v would produce
// something like map[ref:heads/x], which is not an error anywhere -- it is a syntactically fine URL
// segment that addresses the wrong thing, and the API answers 404. Since the reconciler acts on not-found
// by CREATING, a jq program that returns the wrong SHAPE would quietly create a duplicate rather than
// fail. Null is refused for the same reason: it renders as "<nil>" or empty and silently corrupts the path.
func ScalarForURL(val interface{}, what string) (string, error) {
	switch v := val.(type) {
	case string:
		return v, nil
	case bool:
		return strconv.FormatBool(v), nil
	case int, int32, int64, float32, float64, json.Number:
		return async.RenderScalar(v), nil
	case nil:
		return "", fmt.Errorf("%s produced null, which cannot address anything; "+
			"a jq program feeding a URL must return a scalar", what)
	default:
		return "", fmt.Errorf("%s produced %T, which has no meaningful form in a URL; "+
			"a jq program feeding a path or query parameter must return a scalar", what, val)
	}
}

// HasResponseTransforms reports whether any of the named actions declares a whole-document
// responseTransform or a response-direction fieldMapping entry.
//
// It exists so a caller can skip work that NormalizeResponseBody would treat as a no-op anyway. That
// matters for findby, which normalizes EVERY item of a page before matching (#145): without this check
// each item would be deep-copied for a transform that was never declared, on kinds that need none of it.
func HasResponseTransforms(verbs []getter.VerbsDescription, actions []string) bool {
	return len(documentResponseTransforms(verbs, actions)) > 0 || len(responseEntries(verbs, actions)) > 0
}
