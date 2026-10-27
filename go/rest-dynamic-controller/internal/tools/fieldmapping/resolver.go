package fieldmapping

import (
	"context"
	"fmt"
	"sort"

	"github.com/krateo-platformops/plumbing/kubeutil/secretref"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/pathparsing"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// ResolverKey returns a stable key identifying a FieldMappingItem's resolver entry. It is a content key
// (the entry's anchor + source path), not a slice index, so it stays correct even if the caller resolves
// against one copy of a FieldMapping slice and applies against another.
func ResolverKey(m getter.FieldMappingItem) string {
	return m.InPath + "\x00" + m.InQuery + "\x00" + m.InBody + "\x00" + m.InCustomResource
}

// ResolveRequestResolvers resolves every request-direction FieldMapping entry's Resolver against the
// given CR instance, returning a map from ResolverKey to the resolved value. Entries without a Resolver
// are absent from the result. secretRef is resolved directly. Any other resolver type is a hard error —
// an unresolvable entry
// must never be silently skipped here, since that would send the request without the value it was
// supposed to carry.
//
// Resolution is cached per call, keyed by content (the secret's name+key) rather than by ResolverKey: two
// different FieldMapping entries referencing the same secret trigger exactly one Secret read, not one per
// entry.
// The cache does not persist across calls (a fresh ResolveRequestResolvers call per Create/Update/Delete),
// so it never serves a stale value across reconciles.
// lookupValues carries the already-executed dependent lookups, keyed by lookup name. Resolution is split
// that way because running a lookup needs an HTTP client and the resource's documents, which belong to the
// controller, while SUBSTITUTION belongs here with the other resolver kinds.
//
// A dependentLookup entry whose lookup is absent from lookupValues is a hard ERROR, not a skip. That is
// load-bearing: builder.applyFieldMapping skips a resolver entry with no resolved value, treating it as
// "not applicable here", so without this check a caller that forgot to run the lookups would send the
// request with the field simply missing -- exactly the silent-wrong-request class this whole resolver path
// exists to prevent (#117).
func ResolveRequestResolvers(ctx context.Context, dyn dynamic.Interface, mapping []getter.FieldMappingItem, mg *unstructured.Unstructured, lookupValues map[string]interface{}) (map[string]interface{}, error) {
	if len(mapping) == 0 {
		return nil, nil
	}
	out := make(map[string]interface{})
	secretCache := make(map[string]string)

	for _, m := range mapping {
		if m.Resolver == nil {
			continue
		}
		if m.InPath == "" && m.InQuery == "" && m.InBody == "" {
			continue // response-direction entry; CRD-level validation already forbids resolver here
		}
		switch m.Resolver.Type {
		case "secretRef":
			r := m.Resolver.SecretRef
			if r == nil {
				return nil, fmt.Errorf("secretRef resolver is not configured (field %q)", m.InCustomResource)
			}
			name, err := readStringPath(mg, r.NameFromCustomResource)
			if err != nil {
				return nil, fmt.Errorf("reading secret name from %q: %w", r.NameFromCustomResource, err)
			}
			key, err := readStringPath(mg, r.KeyFromCustomResource)
			if err != nil {
				return nil, fmt.Errorf("reading secret key from %q: %w", r.KeyFromCustomResource, err)
			}
			cacheKey := name + "\x00" + key
			val, cached := secretCache[cacheKey]
			if !cached {
				val, err = secretref.GetSecretValue(ctx, dyn, mg.GetNamespace(), name, key)
				if err != nil {
					return nil, fmt.Errorf("resolving secretRef for field %q: %w", m.InCustomResource, err)
				}
				secretCache[cacheKey] = val
			}
			out[ResolverKey(m)] = val
		case "dependentLookup":
			r := m.Resolver.DependentLookup
			if r == nil {
				return nil, fmt.Errorf("dependentLookup resolver is not configured (field %q)", m.InBody)
			}
			v, ok := lookupValues[r.Name]
			if !ok {
				// Either the guard skipped this lookup -- in which case the CR supplies the value and
				// there must be NO resolved entry, so the CR's own field flows through normally -- or the
				// caller never ran the lookups at all. The two are told apart by re-evaluating the guard
				// here rather than trusting the absence, because the second case must be loud.
				skip, gerr := guardSkips(r.When, mg)
				if gerr != nil {
					return nil, gerr
				}
				if skip {
					continue
				}
				return nil, fmt.Errorf("dependentLookup %q was not resolved for field %q: the lookups were not run before building this request", r.Name, m.InBody)
			}
			out[ResolverKey(m)] = v
		default:
			return nil, fmt.Errorf("resolver type %q is not supported (field %q)", m.Resolver.Type, m.InCustomResource)
		}
	}
	return out, nil
}

// CollectSecretRefNames returns the deduped, sorted set of Secret names every secretRef resolver, across
// ALL of the resource's verbs (not just the one currently being invoked), currently resolves to for this
// CR instance. The RBAC self-provisioning grant must cover every verb's need: EnsureSecretRole fully
// replaces the granted secret-name set on each call, so scoping this to only the current verb would drop
// access another verb still needs (e.g. an Update call would revoke a secret only Create's resolvers use).
// An entry whose name path is not yet resolvable on this instance is skipped rather than failing the
// whole collection — a genuinely required-but-missing secretRef surfaces later, with a field-specific
// error, when ResolveRequestResolvers actually resolves it for the verb being invoked.
func CollectSecretRefNames(verbs []getter.VerbsDescription, mg *unstructured.Unstructured) []string {
	seen := map[string]struct{}{}
	for _, verb := range verbs {
		for _, m := range verb.FieldMapping {
			if m.Resolver == nil || m.Resolver.Type != "secretRef" || m.Resolver.SecretRef == nil {
				continue
			}
			name, err := readStringPath(mg, m.Resolver.SecretRef.NameFromCustomResource)
			if err != nil {
				continue
			}
			seen[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// readAnyPath reads the raw value at path, whatever its JSON type. The path may index into an array (e.g.
// "credentials[0].value" or "credentials.0.value") as well as nest through maps.
func readAnyPath(mg *unstructured.Unstructured, path string) (interface{}, error) {
	segments, err := pathparsing.ParsePath(path)
	if err != nil || len(segments) == 0 {
		return nil, fmt.Errorf("invalid path %q", path)
	}
	val, found, err := pathparsing.GetNestedField(mg.Object, segments)
	if err != nil || !found {
		return nil, fmt.Errorf("path %q not found on the custom resource", path)
	}
	return val, nil
}

func readStringPath(mg *unstructured.Unstructured, path string) (string, error) {
	segments, err := pathparsing.ParsePath(path)
	if err != nil || len(segments) == 0 {
		return "", fmt.Errorf("invalid path %q", path)
	}
	val, found, err := pathparsing.GetNestedField(mg.Object, segments)
	if err != nil || !found {
		return "", fmt.Errorf("path %q not found on the custom resource", path)
	}
	s, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("path %q is not a string", path)
	}
	return s, nil
}

// guardSkips reports whether a dependentLookup's `when` guard means the lookup is skipped because the CR
// already carries the value. A nil guard never skips. An explicit null counts as absent: that is how a CR
// says "I am not supplying this".
func guardSkips(when *getter.LookupWhen, mg *unstructured.Unstructured) (bool, error) {
	if when == nil || when.CustomResourceFieldAbsent == "" {
		return false, nil
	}
	segments, err := pathparsing.ParsePath(when.CustomResourceFieldAbsent)
	if err != nil || len(segments) == 0 {
		return false, fmt.Errorf("invalid when.customResourceFieldAbsent path %q", when.CustomResourceFieldAbsent)
	}
	val, found, err := pathparsing.GetNestedField(mg.Object, segments)
	if err != nil {
		return false, fmt.Errorf("reading when.customResourceFieldAbsent %q: %w", when.CustomResourceFieldAbsent, err)
	}
	return found && val != nil, nil
}
