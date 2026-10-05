package restdefinition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/oas2jsonschema"
)

// oasPathsFor returns every distinct OAS document this RestDefinition references: spec.oasPath, plus
// each verb that overrides it.
//
// Sorted and deduplicated, so a resource whose verbs all override to the same second document fetches it
// once, and so the composite digest below is stable rather than depending on verb order.
func oasPathsFor(cr *definitionv1alpha1.RestDefinition) []string {
	seen := map[string]struct{}{cr.Spec.OASPath: {}}
	for _, v := range cr.Spec.Resource.VerbsDescription {
		if v.OASPath != "" {
			seen[v.OASPath] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// compositeOASDigest is a stable content hash across EVERY document a RestDefinition reads.
//
// The single-document digest it replaces covered spec.oasPath alone. With per-verb overrides that would
// be a silent hole: editing the second document would leave the stored hash unchanged, Observe would see
// no drift, and the CRD would never regenerate from the edited spec. The failure is invisible -- the
// resource stays Ready and simply serves a schema that no longer matches its document.
//
// Hashing path->digest pairs in sorted order rather than concatenating bytes means the result does not
// depend on fetch order, and a document moving between paths registers as a change even if its contents
// did not -- which it should, because which document a verb reads is part of what generated the CRD.
func compositeOASDigest(byPath map[string][]byte) string {
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, p := range paths {
		sum := sha256.Sum256(byPath[p])
		fmt.Fprintf(h, "%s=%s\n", p, hex.EncodeToString(sum[:]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// fetchAllOASBytes downloads every document this RestDefinition references, keyed by its oasPath.
func (e *external) fetchAllOASBytes(ctx context.Context, cr *definitionv1alpha1.RestDefinition) (map[string][]byte, error) {
	out := make(map[string][]byte, 1)
	for _, p := range oasPathsFor(cr) {
		contents, err := e.fetchOASBytesAt(ctx, cr, p)
		if err != nil {
			return nil, fmt.Errorf("fetching OAS document %q: %w", p, err)
		}
		out[p] = contents
	}
	return out, nil
}

// getDocumentSetFromCR fetches and parses every document, returning the set a verb resolves against and
// the composite hash of exactly the bytes that were parsed.
//
// The set is keyed by ACTION rather than by path, because that is what the generator asks for. Two verbs
// naming the same override document therefore share one parsed document rather than parsing it twice.
func (e *external) getDocumentSetFromCR(ctx context.Context, cr *definitionv1alpha1.RestDefinition) (*oas2jsonschema.DocumentSet, string, error) {
	byPath, err := e.fetchAllOASBytes(ctx, cr)
	if err != nil {
		return nil, "", err
	}

	parsed := make(map[string]oas2jsonschema.OASDocument, len(byPath))
	for p, contents := range byPath {
		doc, perr := e.parser.Parse(contents)
		if perr != nil {
			return nil, "", fmt.Errorf("parsing OAS document %q: %w", p, perr)
		}
		parsed[p] = doc
	}

	def, ok := parsed[cr.Spec.OASPath]
	if !ok {
		return nil, "", fmt.Errorf("the default OAS document %q was not fetched", cr.Spec.OASPath)
	}

	byVerb := map[string]oas2jsonschema.OASDocument{}
	for _, v := range cr.Spec.Resource.VerbsDescription {
		if v.OASPath == "" || strings.EqualFold(v.OASPath, cr.Spec.OASPath) {
			continue
		}
		byVerb[v.Action] = parsed[v.OASPath]
	}

	return oas2jsonschema.NewDocumentSetWithOverrides(def, byVerb), compositeOASDigest(byPath), nil
}
