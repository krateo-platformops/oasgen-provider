//go:build unit

package restResources

import (
	"context"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func lookupCR(spec map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{"spec": spec}}
}

func lookupResolverMapping(field, lookupName string, when *getter.LookupWhen) getter.FieldMappingItem {
	return getter.FieldMappingItem{
		InBody: field,
		Resolver: &getter.FieldResolver{
			Type:            "dependentLookup",
			DependentLookup: &getter.DependentLookupRef{Name: lookupName, When: when},
		},
	}
}

// TestNeededLookupNames pins WHICH lookups run. It matters twice over: a lookup that runs when it should
// not costs an API call per reconcile on every CR, and one that does not run when it should leaves the
// request missing a field, which builder.applyFieldMapping skips silently.
func TestNeededLookupNames(t *testing.T) {
	// defaultBranch is referenced only as baseSha's parameter, never by a resolver, so it must still run
	// whenever baseSha does -- and must NOT run when baseSha is guarded off.
	lookups := []getter.DependentLookup{
		{Name: "defaultBranch", Method: "GET", Path: "/repos/{owner}/{repo}", Select: ".default_branch"},
		{Name: "baseSha", Method: "GET", Path: "/repos/{owner}/{repo}/git/ref/{ref}", Select: ".object.sha",
			Parameters: []getter.LookupParameter{{InPath: "ref", FromLookup: "defaultBranch"}}},
		{Name: "unused", Method: "GET", Path: "/nope", Select: ".x"},
	}

	cases := []struct {
		name    string
		mapping []getter.FieldMappingItem
		mg      *unstructured.Unstructured
		want    []string
	}{
		{
			name:    "a resolver pulls in its lookup AND that lookup's own dependency",
			mapping: []getter.FieldMappingItem{lookupResolverMapping("sha", "baseSha", nil)},
			mg:      lookupCR(map[string]interface{}{"owner": "krateo"}),
			want:    []string{"defaultBranch", "baseSha"},
		},
		{
			name:    "declaration order is preserved, not resolver order",
			mapping: []getter.FieldMappingItem{lookupResolverMapping("sha", "baseSha", nil)},
			mg:      lookupCR(map[string]interface{}{}),
			want:    []string{"defaultBranch", "baseSha"}, // never baseSha first
		},
		{
			name: "guard satisfied: the CR supplies the value, so NOTHING runs",
			mapping: []getter.FieldMappingItem{lookupResolverMapping("sha", "baseSha",
				&getter.LookupWhen{CustomResourceFieldAbsent: "spec.sha"})},
			mg:   lookupCR(map[string]interface{}{"sha": "abc123"}),
			want: nil,
		},
		{
			name: "guard unsatisfied: the field is absent, so the chain runs",
			mapping: []getter.FieldMappingItem{lookupResolverMapping("sha", "baseSha",
				&getter.LookupWhen{CustomResourceFieldAbsent: "spec.sha"})},
			mg:   lookupCR(map[string]interface{}{"owner": "krateo"}),
			want: []string{"defaultBranch", "baseSha"},
		},
		{
			name: "an explicit null counts as absent, so the chain runs",
			mapping: []getter.FieldMappingItem{lookupResolverMapping("sha", "baseSha",
				&getter.LookupWhen{CustomResourceFieldAbsent: "spec.sha"})},
			mg:   lookupCR(map[string]interface{}{"sha": nil}),
			want: []string{"defaultBranch", "baseSha"},
		},
		{
			name:    "a secretRef resolver pulls in no lookups",
			mapping: []getter.FieldMappingItem{{InBody: "token", Resolver: &getter.FieldResolver{Type: "secretRef"}}},
			mg:      lookupCR(map[string]interface{}{}),
			want:    nil,
		},
		{
			name:    "no resolvers at all",
			mapping: []getter.FieldMappingItem{{InBody: "name", InCustomResource: "spec.name"}},
			mg:      lookupCR(map[string]interface{}{}),
			want:    nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := neededLookupNames(lookups, tc.mapping, tc.mg)
			require.NoError(t, err)
			if len(tc.want) == 0 {
				// Emptiness is the contract; nil vs an empty slice is not, and the caller only reads len().
				assert.Empty(t, got)
			} else {
				assert.Equal(t, tc.want, got)
			}
			assert.NotContains(t, got, "unused", "a lookup nothing references must never run")
		})
	}
}

// TestMatchLookupItemAmbiguityIsAnError is the test this feature exists to make possible.
//
// apiLookup scoped its list server-side and then took items[0]. Against Keycloak, whose
// /authentication/flows ignores ?alias= and returns every flow, that bound the WRONG id while reporting
// success -- any alias but the first. There is deliberately no option to restore that behaviour, so the
// assertion here is that two matches FAIL rather than resolving to either one.
func TestMatchLookupItemAmbiguityIsAnError(t *testing.T) {
	flows := []interface{}{
		map[string]interface{}{"alias": "browser", "id": "id-1"},
		map[string]interface{}{"alias": "browser-mfa", "id": "id-2"},
		map[string]interface{}{"alias": "browser-mfa", "id": "id-3"},
	}
	base := getter.DependentLookup{
		Name: "flowByAlias", Method: "GET", Path: "/flows",
		Match: &getter.LookupMatch{
			ItemsAt: ".", WhereField: "alias", SelectField: "id",
			EqualsFromCustomResource: "spec.flow",
		},
	}

	t.Run("exactly one match resolves", func(t *testing.T) {
		got, err := matchLookupItem(context.Background(), base, flows, lookupCR(map[string]interface{}{"flow": "browser"}))
		require.NoError(t, err)
		assert.Equal(t, "id-1", got)
	})

	t.Run("two matches is an error, not items[0]", func(t *testing.T) {
		_, err := matchLookupItem(context.Background(), base, flows, lookupCR(map[string]interface{}{"flow": "browser-mfa"}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "refusing to guess")
		assert.NotContains(t, err.Error(), "id-2", "the error must not leak a value it declined to choose")
	})

	t.Run("no match defaults to a requeue signal", func(t *testing.T) {
		_, err := matchLookupItem(context.Background(), base, flows, lookupCR(map[string]interface{}{"flow": "absent"}))
		require.Error(t, err)
		assert.True(t, isLookupReferentMissing(err), "onMissing defaults to requeue: the referent may still be converging")
	})

	t.Run("no match with onMissing error is a hard failure", func(t *testing.T) {
		strict := base
		m := *base.Match
		m.OnMissing = "error"
		strict.Match = &m
		_, err := matchLookupItem(context.Background(), strict, flows, lookupCR(map[string]interface{}{"flow": "absent"}))
		require.Error(t, err)
		assert.False(t, isLookupReferentMissing(err), "onMissing: error must NOT read as a requeue")
	})
}

// TestBindLookupParametersChaining covers the chain GitRef needs, including the transform that makes the
// chained value addressable: GitHub declares ONE {ref} parameter whose value is "heads/<branch>", so the
// default branch name alone cannot address the URL.
func TestBindLookupParametersChaining(t *testing.T) {
	l := getter.DependentLookup{
		Name: "baseSha", Method: "GET", Path: "/repos/{owner}/{repo}/git/ref/{ref}", Select: ".object.sha",
		Parameters: []getter.LookupParameter{
			{InPath: "owner", InCustomResource: "spec.owner"},
			{InPath: "ref", FromLookup: "defaultBranch", ValueMapping: &getter.ValueMapping{
				Type: "jq", JQ: &getter.JQProgram{Inline: `"heads/" + .`},
			}},
			{InQuery: "per_page", InCustomResource: "spec.perPage"},
		},
	}
	mg := lookupCR(map[string]interface{}{"owner": "krateo", "perPage": int64(50)})

	params, query, err := bindLookupParameters(context.Background(), l, mg, map[string]interface{}{"defaultBranch": "main"})
	require.NoError(t, err)
	assert.Equal(t, "krateo", params["owner"])
	assert.Equal(t, "heads/main", params["ref"], "the chained value must carry the prefix the declared path needs")
	assert.Equal(t, "50", query["per_page"], "a non-string CR value must still be bindable")

	t.Run("an unresolved chain reference fails rather than sending a blank", func(t *testing.T) {
		_, _, err := bindLookupParameters(context.Background(), l, mg, map[string]interface{}{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "has not been resolved")
	})

	t.Run("a CR path that does not exist fails rather than sending a blank", func(t *testing.T) {
		_, _, err := bindLookupParameters(context.Background(), l, lookupCR(map[string]interface{}{}), map[string]interface{}{"defaultBranch": "main"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found on the custom resource")
	})
}
