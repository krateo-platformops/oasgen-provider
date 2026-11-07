package oas2jsonschema

import (
	"reflect"
	"sort"

	rtv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
)

// authMethodShapes maps each key authMethodKey can return to the Go type whose JSON shape the generator
// renders under `spec.authentication.<key>`.
//
// IT IS THE ONLY PLACE THESE TWO FACTS ARE PAIRED, and TestEveryAuthMethodKeyHasAShape fails if
// authMethodKey can return a key this map does not carry. That test is the whole reason this map exists
// as data rather than as a switch.
var authMethodShapes = map[string]reflect.Type{
	"basic":  reflect.TypeOf(BasicAuth{}),
	"bearer": reflect.TypeOf(BearerAuth{}),
	"apiKey": reflect.TypeOf(APIKeyAuth{}),
}

// AuthSecretRefPaths returns the spec path, on a generated Configuration CR, of every Secret reference the
// generator can place there -- DERIVED from the types the schemas are rendered from rather than written out.
//
// WHY THIS IS DERIVED AND EXPORTED (#500). A hand-maintained copy of these paths lived in
// internal/tools/deploy, used to collect the Secret references that the per-resource RBAC Role is built
// from. It listed basic's two refs and bearer's one, and its comment said so accurately -- "(basic and
// bearer auth)". The generator produces a THIRD shape, apiKey in a header, and that path was never added.
//
// On its own that was latent: the shipped RDC ClusterRole granted `resources: ["secrets"]` cluster-wide,
// so the narrow Role being incomplete cost nothing. 0.54.0 removed that blanket grant (#473/#475) on the
// reasoning that the narrow Role already covered the same ground -- and for apiKey consumers it never had.
// The result was a release in which no apiKey-authenticated resource could read its credential in any
// namespace, so nothing reconciled. Measured on released images by a consumer whose fleet is 25 apiKey
// kinds and 11 bearer: `can-i get secrets` returned no in every namespace, while the CR's own access was
// fine, which is what ruled out a namespace-scoping explanation.
//
// A FIXTURE COULD NOT HAVE CAUGHT IT, which is why the fix is a derivation rather than another test case.
// That consumer's fleet carries BOTH shapes against the same vendor -- 11 documents declare http/bearer and
// 25 declare apiKey in a header, several sending an identical `Authorization: Bearer ` value either way. A
// suite with one scheme per document passes while a collector handles a subset, however many documents it
// has. Deriving the paths removes the possibility instead of testing for it.
func AuthSecretRefPaths() [][]string {
	refType := reflect.TypeOf(rtv1.SecretKeySelector{})

	keys := make([]string, 0, len(authMethodShapes))
	for k := range authMethodShapes {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic output: the caller hashes and logs these

	out := make([][]string, 0, 4)
	for _, key := range keys {
		t := authMethodShapes[key]
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Type != refType {
				continue
			}
			tag := jsonFieldName(f)
			if tag == "" {
				continue
			}
			out = append(out, []string{"spec", "authentication", key, tag})
		}
	}
	return out
}

// jsonFieldName is the serialised name of a struct field: its json tag up to the first comma, or "" when
// the field is unexported or explicitly skipped. A field whose tag is absent would serialise under its Go
// name, which no generated CR carries, so returning "" is correct rather than defensive.
func jsonFieldName(f reflect.StructField) string {
	if f.PkgPath != "" {
		return ""
	}
	tag := f.Tag.Get("json")
	if tag == "" || tag == "-" {
		return ""
	}
	for i := 0; i < len(tag); i++ {
		if tag[i] == ',' {
			return tag[:i]
		}
	}
	return tag
}
