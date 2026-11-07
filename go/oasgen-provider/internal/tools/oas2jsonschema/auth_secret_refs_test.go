//go:build unit

package oas2jsonschema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEveryAuthMethodKeyHasAShape is the guard for #500, and it is written over the SPACE OF SCHEMES
// rather than over a list of keys.
//
// The defect it exists to prevent: authMethodKey returns three keys and the RBAC collector's path list
// carried two. A test asserting "the list contains basic, bearer and apiKey" would have been written from
// the same misunderstanding as the list itself, and would pass today and fail to notice a fourth.
//
// So this enumerates every scheme shape authMethodKey is given in practice, takes whatever keys it accepts,
// and requires each to have an entry in authMethodShapes -- which is what AuthSecretRefPaths derives from.
// Adding a case to authMethodKey without adding its type here fails HERE, naming the key.
func TestEveryAuthMethodKeyHasAShape(t *testing.T) {
	// The cross product oasgen actually meets: the two http schemes, apiKey in each location, plus the
	// shapes that must be REFUSED so an accidental widening of authMethodKey is also caught.
	candidates := []SecuritySchemeInfo{
		{Name: "a", Type: SchemeTypeHTTP, Scheme: "basic"},
		{Name: "b", Type: SchemeTypeHTTP, Scheme: "bearer"},
		{Name: "c", Type: SchemeTypeAPIKey, In: "header"},
		{Name: "d", Type: SchemeTypeAPIKey, In: "query"},
		{Name: "e", Type: SchemeTypeAPIKey, In: "cookie"},
		{Name: "f", Type: SchemeTypeHTTP, Scheme: "digest"},
		{Name: "g", Type: SecuritySchemeType("oauth2")},
		{Name: "h", Type: SecuritySchemeType("openIdConnect")},
	}

	accepted := map[string]bool{}
	for _, c := range candidates {
		key, ok := authMethodKey(c)
		if !ok {
			continue
		}
		accepted[key] = true
		require.Contains(t, authMethodShapes, key,
			"authMethodKey accepts the key %q, so the generator renders spec.authentication.%s -- but "+
				"authMethodShapes has no type for it, so AuthSecretRefPaths cannot derive its Secret "+
				"reference paths. The per-resource RBAC Role would then grant nothing for that scheme, "+
				"and since #475 removed the blanket cluster-wide secrets grant there is no fallback: "+
				"no resource using it could read its credential in any namespace. That is #500.", key, key)
	}

	require.NotEmpty(t, accepted, "no scheme was accepted, so this test asserted nothing about anything")

	// And the converse: a shape with no key must not be carried, or AuthSecretRefPaths would emit a path
	// the generator never renders -- harmless but a lie about the CR's shape.
	require.Len(t, authMethodShapes, len(accepted),
		"authMethodShapes carries %d entries but authMethodKey accepts %d keys: %v",
		len(authMethodShapes), len(accepted), accepted)
}

// TestAuthSecretRefPathsCoversTheApiKeyToken is the direct #500 regression: the one path whose absence
// broke 0.54.0.
//
// Kept separate from the guard above because it fails for a DIFFERENT reason -- the guard catches a key
// with no type, this catches a type whose Secret reference field stops being recognised (renamed, retyped,
// or given a json tag the reflection does not read).
func TestAuthSecretRefPathsCoversTheApiKeyToken(t *testing.T) {
	want := map[string]bool{
		"spec/authentication/basic/usernameRef": false,
		"spec/authentication/basic/passwordRef": false,
		"spec/authentication/bearer/tokenRef":   false,
		"spec/authentication/apiKey/tokenRef":   false,
	}
	for _, p := range AuthSecretRefPaths() {
		k := p[0] + "/" + p[1] + "/" + p[2] + "/" + p[3]
		_, known := want[k]
		require.True(t, known, "AuthSecretRefPaths emitted an unexpected path %v", p)
		want[k] = true
	}
	for k, seen := range want {
		require.True(t, seen,
			"AuthSecretRefPaths does not emit %q. If that is apiKey/tokenRef, this is #500 exactly: a "+
				"consumer fleet of 25 apiKey kinds could not read its credential in any namespace on "+
				"0.54.0, because #475 had removed the blanket grant that was covering the gap.", k)
	}
}
