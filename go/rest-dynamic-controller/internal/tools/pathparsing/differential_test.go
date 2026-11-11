package pathparsing

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// doc is the one document both walkers are driven over. A single fixture is the point: a differential
// result means nothing if the two sides are reading different data.
//
// It carries one element of every shape the two walkers could disagree about — a selector that matches
// once, one that matches twice, a scalar element a field selector cannot satisfy, the same id as int,
// float and string, a map whose key is literally "0", and an element whose selector value is empty.
func doc() map[string]any {
	return map[string]any{
		"spec": map[string]any{
			"name": "n1",
			"credentials": []any{
				map[string]any{"type": "password", "valueSecretRef": map[string]any{"name": "pw-secret", "key": "password"}},
				map[string]any{"type": "otp", "secretDataSecretRef": map[string]any{"name": "otp-secret", "key": "secretData"}},
				"a-scalar-element",
				map[string]any{"type": "dup", "n": 1},
				map[string]any{"type": "dup", "n": 2},
			},
			"numeric":  []any{map[string]any{"id": 2, "hit": "int"}},
			"floaty":   []any{map[string]any{"id": 2.0, "hit": "float"}},
			"stringy":  []any{map[string]any{"id": "2", "hit": "string"}},
			"zeroKey":  map[string]any{"0": "a-map-key-named-zero"},
			"emptyVal": []any{map[string]any{"type": "", "hit": "empty"}},
			// azuredevops-provider-kog's only non-lowerCamel segment, from their own census: the key
			// mirrors Azure DevOps' field `AccessToken`. It is a plain key and must stay one.
			"authorization": map[string]any{
				"parameters": map[string]any{
					"AccessTokenSecretRef": map[string]any{"name": "ado-token", "key": "token"},
				},
			},
		},
	}
}

// TestTheTwoWalkersAgreeExceptWhereStated is what makes "the provider and this controller select the
// same element" measured rather than assumed.
//
// # The failure it exists to prevent
//
// oasgen-provider's #510 collector resolves a Secret NAME out of a custom resource and writes a Role
// naming exactly that Secret; this controller later READS that Secret. The two use the same parser
// (#513) and, until now, different walkers: the collector plumbing's typed fieldpath.Get, this module
// the flat-form GetNestedField. A disagreement about which element a selector picks means the provider
// grants for one Secret and the controller reads another — #510's own failure mode, one level down, in
// the half of the semantics a shared grammar does not cover.
//
// It was invisible while this controller ALSO granted itself a per-CR Role computed by its own walker:
// each side was internally consistent. Removing that self-granting (#540) makes the provider's Role the
// only grant, and this test the thing standing between a path shape and a silent `forbidden`.
//
// # Why it lives here and not in oasgen-provider
//
// This is the module where both walkers exist. oasgen-provider has only the typed one, and cannot import
// this module's internal packages to get the other. plumbing v1.33.0's walk.go says a differential test
// "stays there" in oasgen-provider — it stayed on a branch that was never merged, so from the port until
// now the agreement ran in no CI at all.
//
// # The corrections assert a DIRECTION, not just a difference
//
// A row listed as corrected must differ, AND the typed walker must be the one that refuses. "They
// differ" alone would still pass if the polarity flipped, and the polarity is the whole safety argument:
// the typed walker refusing means the provider's collector names no Secret, so there is nothing for this
// controller to read with no grant. If the flat walker were the stricter one, the same divergence would
// be harmless.
func TestTheTwoWalkersAgreeExceptWhereStated(t *testing.T) {
	agree := []string{
		"spec.name",
		// Both consumers' real shapes, from their own censuses. keycloak-config-kog 0.4.5's four
		// secretRef paths are all selectors of this form; azuredevops-provider-kog's four are all plain
		// dotted, one of them through an uppercase-initial key.
		"spec.credentials[?type=password].valueSecretRef.name",
		"spec.credentials[?type=password].valueSecretRef.key",
		"spec.credentials[?type=otp].secretDataSecretRef.name",
		"spec.credentials[?type=otp].secretDataSecretRef.key",
		"spec.authorization.parameters.AccessTokenSecretRef.name",
		// Index into an array, which both walkers take.
		"spec.credentials[0].valueSecretRef.name",
		"spec.credentials[1].secretDataSecretRef.key",
		// A selector matching nothing is an ABSENCE on both sides.
		"spec.credentials[?type=nosuch].valueSecretRef.name",
		// A selector matching twice is an ERROR on both sides, never "take the first". This is the one
		// keycloak exercises with two `type: otp` credentials.
		"spec.credentials[?type=dup].n",
		// Comparison is by string form on both sides, so 2, 2.0 and "2" all match "2".
		"spec.numeric[?id=2].hit",
		"spec.floaty[?id=2].hit",
		"spec.stringy[?id=2].hit",
		// An empty selector value is a value, not a missing one.
		"spec.emptyVal[?type=].hit",
		// A field selector cannot be satisfied by a scalar element; neither walker treats it as a match.
		"spec.credentials[?type=a-scalar-element].x",
		// Out of range is ABSENT, not an error, on both sides.
		"spec.credentials[99].name",
		"spec.missing.deeper",
		// A quoted key against a MAP is a key on both sides.
		"spec.zeroKey['0']",
		// Both refuse to walk through a scalar; only the message differs, and a message is not an
		// outcome. The branch this test comes from listed this as a correction and its own differential
		// caught the mistake.
		"spec.name.deeper",
	}

	corrections := map[string]string{
		"spec.credentials['0'].valueSecretRef.name": "a quoted key against an ARRAY silently indexed it in the flat form; " +
			"the author asked for a key and got element 0",
		"spec.zeroKey[0]": "an unquoted index against a MAP resolved as the key \"0\"; " +
			"the author wrote an index and a map has none",
		// THE THIRD ROW, AND THE CORPUS DID NOT FIND IT. An existing builder test did, by failing:
		// TestBuildCallConfig_FieldMappingKeepsUnmappedSiblings maps spec.credentials.0.pw, and the
		// branch this test came from listed exactly two corrections because its corpus had
		// `credentials[0]` and `credentials['0']` and never the DOTTED index against an array.
		//
		// It is the same collapse as the quoted row and that is why it cannot be exempted: `a.0` and
		// `a['0']` parse to byte-identical segments (key "0"), so no walker taking []Segment can accept
		// one and refuse the other. Dropping the dotted form on custom-resource reads is a deliberate
		// breaking change, taken because resolving "0" by asking the DATA what it means is the content
		// sniffing the typed dialect exists to remove.
		"spec.credentials.0.type": "the DOTTED index against an ARRAY. Parses identically to " +
			"spec.credentials['0'].type, so the typed walker refuses both; the flat walker indexed it. " +
			"A documented form, removed on purpose -- see resolver.go's readAnyPath",
	}

	for _, p := range agree {
		t.Run("agree/"+p, func(t *testing.T) {
			segs, perr := ParseTyped(p)
			require.NoError(t, perr, "the shared parser must accept this path; if it does not, the row is wrong")
			flatSegs, ferr := ParsePath(p)
			require.NoError(t, ferr, "ParsePath and ParseTyped are the same parser and must agree on acceptance")

			typedVal, typedFound, typedErr := GetTyped(doc(), segs)
			flatVal, flatFound, flatErr := GetNestedField(doc(), flatSegs)

			assert.Equal(t, flatErr == nil, typedErr == nil,
				"one walker errored and the other did not: flat=%v typed=%v", flatErr, typedErr)
			if flatErr != nil || typedErr != nil {
				return
			}
			assert.Equal(t, flatFound, typedFound, "found must agree")
			if flatFound {
				assert.Equal(t, fmt.Sprintf("%v", flatVal), fmt.Sprintf("%v", typedVal),
					"the two walkers selected DIFFERENT values for %q -- the provider would grant a Role for "+
						"one Secret and this controller would read another (#510, #540)", p)
			}
		})
	}

	for p, why := range corrections {
		t.Run("corrected/"+p, func(t *testing.T) {
			segs, perr := ParseTyped(p)
			require.NoError(t, perr)
			flatSegs, ferr := ParsePath(p)
			require.NoError(t, ferr)

			_, typedFound, typedErr := GetTyped(doc(), segs)
			flatVal, flatFound, flatErr := GetNestedField(doc(), flatSegs)

			same := (flatErr == nil) == (typedErr == nil) && flatFound == typedFound
			require.False(t, same,
				"%q is listed as a CORRECTION but the two walkers agree (flat: found=%v err=%v / typed: "+
					"found=%v err=%v). Remove the row, or it claims a change that is not made. %s",
				p, flatFound, flatErr, typedFound, typedErr, why)

			// THE DIRECTION, which is the safety argument. See the note on this function.
			assert.Error(t, typedErr,
				"the TYPED walker must be the one that refuses %q: the provider's collector uses it, so its "+
					"refusal means no Secret is named and there is nothing for this controller to read "+
					"without a grant", p)
			assert.NoError(t, flatErr, "the flat walker is expected to resolve %q; that is what makes this a correction", p)
			assert.True(t, flatFound, "the flat walker resolved %q to %v", p, flatVal)
		})
	}
}
