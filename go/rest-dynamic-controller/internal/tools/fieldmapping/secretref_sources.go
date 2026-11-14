package fieldmapping

import (
	"fmt"
	"sort"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/pathparsing"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// SecretRefSource is one secretRef resolver's source, as it resolves on one CR instance.
//
// It carries no secret VALUE and nothing is read from the cluster to build it -- it is derived
// entirely from the resource definition plus the CR's own fields.
type SecretRefSource struct {
	// ResolverKey is the entry's content key (see ResolverKey), not a slice index, so the set stays
	// stable when the mapping slice is copied or reordered.
	ResolverKey string
	Namespace   string
	Name        string
	Key         string
}

// String renders the source for digesting and for logs. It deliberately contains no secret value.
func (s SecretRefSource) String() string {
	return s.ResolverKey + "\x00" + s.Namespace + "/" + s.Name + "\x00" + s.Key
}

// CollectSecretRefSources returns the secretRef sources the given field mapping resolves to on this
// CR instance, sorted and deduplicated by source.
//
// WHY THIS IS NOT ResolveRequestResolvers. That function builds a REQUEST: it reaches the cluster for
// every secret value, it hard-errors on anything it cannot resolve, and -- the part that matters most
// here -- a `dependentLookup` entry sends it to the VENDOR. Calling it from Observe would cost a
// vendor round-trip per declared lookup per CR per poll, forever, just to compute a digest (#598).
// This collects sources only, performs no I/O at all, and the caller decides what to read.
//
// THREE SKIPS, AND THE THIRD IS THE ONE THAT NEEDS CARE.
//
//	A non-secretRef resolver is skipped, not an error. dependentLookup reads no Secret content, so it
//	has no rotation to detect and nothing to contribute.
//
//	A `when`-guarded entry whose guard does not hold is skipped. The guard reads only the CR, so it
//	is free to evaluate here -- no lookup, no API call. Skipping is the right answer rather than the
//	convenient one: an entry the guard excludes is one the request will not carry either, so
//	including it would digest a value that is never sent.
//
//	An entry whose name or key path this INSTANCE does not carry is skipped -- but an entry whose
//	path is MALFORMED is an error. readStringPath conflates the two, which is why this does its own
//	read: "the CR does not set this optional ref" and "the definition names a path that cannot
//	exist" are different facts, and swallowing the second would make a definition bug look like an
//	inapplicable resolver on every instance, forever.
func CollectSecretRefSources(mapping []getter.FieldMappingItem, mg *unstructured.Unstructured) ([]SecretRefSource, error) {
	seen := map[string]SecretRefSource{}
	for _, m := range mapping {
		if m.Resolver == nil || m.Resolver.Type != "secretRef" {
			continue
		}
		if m.InPath == "" && m.InQuery == "" && m.InBody == "" {
			continue // response-direction entry; CRD-level validation already forbids a resolver here
		}
		if skip, err := resolverGuardSkips(m.Resolver.When, mg); err != nil {
			return nil, fmt.Errorf("evaluating when.customResourceFieldPresent for field %q: %w",
				m.InCustomResource, err)
		} else if skip {
			continue
		}
		r := m.Resolver.SecretRef
		if r == nil {
			return nil, fmt.Errorf("secretRef resolver is not configured (field %q)", m.InCustomResource)
		}
		name, found, err := readOptionalStringPath(mg, r.NameFromCustomResource)
		if err != nil {
			return nil, fmt.Errorf("reading secret name from %q: %w", r.NameFromCustomResource, err)
		}
		if !found {
			continue // this instance does not carry the ref
		}
		key, found, err := readOptionalStringPath(mg, r.KeyFromCustomResource)
		if err != nil {
			return nil, fmt.Errorf("reading secret key from %q: %w", r.KeyFromCustomResource, err)
		}
		if !found {
			continue
		}
		src := SecretRefSource{
			ResolverKey: ResolverKey(m),
			Namespace:   mg.GetNamespace(),
			Name:        name,
			Key:         key,
		}
		seen[src.String()] = src
	}

	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]SecretRefSource, 0, len(keys))
	for _, k := range keys {
		out = append(out, seen[k])
	}
	return out, nil
}

// readOptionalStringPath separates "this instance does not set the field" (found=false, no error)
// from "the definition names a path that cannot be read" (error). readStringPath returns an error for
// both, which is correct where a missing value must fail the request and wrong here.
//
// A non-string value IS an error rather than an absence: a ref whose name is a number or an object is
// a definition or CR mistake, and treating it as "unset" would silently drop the resolver from the
// digest for as long as it stayed wrong.
func readOptionalStringPath(mg *unstructured.Unstructured, path string) (string, bool, error) {
	segments, err := pathparsing.ParseTyped(path)
	if err != nil {
		return "", false, fmt.Errorf("invalid path %q: %w", path, err)
	}
	if len(segments) == 0 {
		return "", false, fmt.Errorf("invalid path %q: it names no field", path)
	}
	val, found, err := pathparsing.GetTyped(mg.Object, segments)
	if err != nil {
		return "", false, fmt.Errorf("path %q cannot be read on the custom resource: %w", path, err)
	}
	if !found || val == nil {
		return "", false, nil
	}
	s, ok := val.(string)
	if !ok {
		return "", false, fmt.Errorf("path %q is not a string (got %T)", path, val)
	}
	return s, true, nil
}
