//go:build unit

package oas2jsonschema

import (
	"testing"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/pathparsing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPredicateMappingSourceIsNotReportedUnresolvable is the false warning a live consumer found within
// an hour of it merging.
//
// Their KeycloakUser uses `spec.credentials[?type=otp].secretDataSecretRef` in sixteen places. The
// generator refused the path and reported it as "not a parseable field path", advising the author to
// "point inCustomResource at a field the CR carries, or remove the mapping" -- about a path the controller
// resolves correctly.
//
// The refusal was right in principle and applied to the wrong population: a schema has no indexed
// elements, but `inCustomResource` addresses a CR INSTANCE at runtime, which does.
func TestAPredicateMappingSourceIsNotReportedUnresolvable(t *testing.T) {
	specProps := map[string]bool{"credentials": true, "username": true}
	statusProps := map[string]bool{}

	for _, in := range []string{
		"spec.credentials[?type=otp].secretDataSecretRef",
		"spec.credentials[?type=otp].secretDataSecretRef.name",
		"spec.credentials[?type=password].value",
		"spec.credentials[0].value",
	} {
		root, _, reason := resolveCustomResourcePath(in, specProps, statusProps)
		assert.Empty(t, reason,
			"%q must resolve: the controller parses it, and the screen only inspects the first two "+
				"segments, which are keys. root=%q", in, root)
	}
}

// TestTheScreenStillCatchesAGenuinelyUnresolvableSource is the other half: accepting the grammar must not
// turn the screen off. Without this, the test above could pass because nothing is checked at all.
func TestTheScreenStillCatchesAGenuinelyUnresolvableSource(t *testing.T) {
	specProps := map[string]bool{"credentials": true}
	statusProps := map[string]bool{}

	for _, tc := range []struct{ in, want string }{
		{in: "spec.notAProperty", want: "is not a property of the generated spec"},
		{in: "spec.notAProperty[?type=otp].x", want: "is not a property of the generated spec"},
		{in: "spec", want: "names the whole spec rather than a field in it"},
		{in: "a..b", want: "is not a parseable field path"},
		{in: "spec.['unterminated", want: "is not a parseable field path"},
	} {
		_, _, reason := resolveCustomResourcePath(tc.in, specProps, statusProps)
		require.NotEmpty(t, reason, "%q must still be reported", tc.in)
		assert.Contains(t, reason, tc.want, tc.in)
	}
}

// TestASchemaPathStillRefusesAnIndex keeps the original decision intact where it was right. A path walked
// against a *Schema has no indexed elements, and #520's refusal must survive this fix -- otherwise the
// correction would have undone the thing it was narrowing.
func TestASchemaPathStillRefusesAnIndex(t *testing.T) {
	for _, in := range []string{"credentials[0].value", "credentials[?type=otp].value"} {
		_, err := pathparsingParsePath(in)
		assert.Error(t, err, "%q addresses a schema here and must still be refused", in)
	}
}

func pathparsingParsePath(p string) ([]string, error) { return pathparsing.ParsePath(p) }
