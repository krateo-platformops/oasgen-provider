//go:build unit || integration

package restclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/pb33f/libopenapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// githubPullItem is the shape GitHub actually returns in a pulls list: head and base are OBJECTS carrying
// a ref, not the branch strings a user writes in the spec.
func githubPullItem(number int64, headRef, baseRef string) map[string]interface{} {
	return map[string]interface{}{
		"number": number,
		"title":  "some title",
		"head": map[string]interface{}{
			"ref":   headRef,
			"label": "owner:" + headRef,
			"repo":  map[string]interface{}{"name": "demo"},
		},
		"base": map[string]interface{}{
			"ref":   baseRef,
			"label": "owner:" + baseRef,
		},
	}
}

// flattenHeadBase is the same responseTransform the kind already declares for its get verb: it turns the
// head/base objects into the branch strings the spec holds.
func flattenHeadBase() *getter.VerbsDescription {
	return &getter.VerbsDescription{
		Action: "findby",
		Method: "GET",
		Path:   "/repos/{owner}/{repo}/pulls",
		ResponseTransform: &getter.JQProgram{
			Inline: `.head = .head.ref | .base = .base.ref`,
		},
	}
}

func crWithSpec(spec map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "github.krateo.io/v1alpha1",
		"kind":       "PullRequest",
		"metadata":   map[string]interface{}{"name": "pr", "namespace": "demo"},
		"spec":       spec,
	}}
}

// TestFindByMatchesOnNormalizedShape is the fix for #145.
//
// The match compares an identifier's value against spec.<field> with DeepEqual, so it compares API-domain
// data against CR-domain data — and the responseTransform is exactly the thing that bridges those. Running
// it after the match meant DeepEqual("feature-x", map[...]) for every item, so no item ever matched.
//
// The failure was worse than "findby does not work": a no-match returns not-found, and the reconciler acts
// on not-found by CREATING. A findby written against a shape-mismatched key therefore does not fail — it
// re-creates a resource that already exists (here, a duplicate-PR 422 loop).
func TestFindByMatchesOnNormalizedShape(t *testing.T) {
	items := []interface{}{
		githubPullItem(1, "other-branch", "main"),
		githubPullItem(42, "feature-x", "main"),
	}

	u := &UnstructuredClient{
		IdentifierFields:       []string{"head", "base"},
		IdentifiersMatchPolicy: "AND",
		Resource:               crWithSpec(map[string]interface{}{"head": "feature-x", "base": "main"}),
	}

	// Before normalization the CR cannot match anything: spec.head is a string, the item's head is an object.
	_, found := u.findItemInList(items)
	require.False(t, found,
		"precondition: the raw shape cannot match, which is exactly why normalization has to come first")

	normalized, err := normalizeItems(context.Background(), flattenHeadBase(), items)
	require.NoError(t, err)

	matched, found := u.findItemInList(normalized)
	require.True(t, found, "once the item is in the CR's shape the match is possible at all")
	assert.EqualValues(t, 42, matched["number"], "and it must be the right item, not merely some item")
}

// normalizeItems must not mutate the caller's items. The slice it receives is also the paginator's view of
// the page, and a transform that rewrote it in place would corrupt anything that reads the page afterwards.
func TestNormalizeItemsDoesNotMutateTheInput(t *testing.T) {
	raw := githubPullItem(42, "feature-x", "main")
	items := []interface{}{raw}

	normalized, err := normalizeItems(context.Background(), flattenHeadBase(), items)
	require.NoError(t, err)

	assert.IsType(t, map[string]interface{}{}, raw["head"],
		"the ORIGINAL item must still hold the raw object shape")
	assert.Equal(t, "feature-x", normalized[0].(map[string]interface{})["head"],
		"while the copy carries the normalized scalar")
}

// A verb with no declared transforms must be left completely alone — same slice, no per-item deep copies.
// findby normalizes every item of every page, so kinds that need none of this must pay none of it.
func TestNormalizeItemsIsANoOpWithoutTransforms(t *testing.T) {
	items := []interface{}{githubPullItem(1, "a", "main")}
	plain := &getter.VerbsDescription{Action: "findby", Method: "GET", Path: "/x"}

	got, err := normalizeItems(context.Background(), plain, items)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Same(t, &items[0], &got[0], "no transforms declared means the very same backing array")
}

// A nil verb (callers that do not have it to hand) must not panic, and must not claim normalization.
func TestNormalizeItemsToleratesNilVerb(t *testing.T) {
	items := []interface{}{githubPullItem(1, "a", "main")}
	got, err := normalizeItems(context.Background(), nil, items)
	require.NoError(t, err)
	assert.Equal(t, items, got)
}

// Non-object elements are carried through rather than failing the search. findItemInList skips them anyway,
// and failing the whole findby over an element nobody will look at would turn a cosmetic oddity in a
// response into a not-found — which the reconciler acts on by creating.
func TestNormalizeItemsCarriesNonObjectElements(t *testing.T) {
	items := []interface{}{"a string", githubPullItem(42, "feature-x", "main")}

	got, err := normalizeItems(context.Background(), flattenHeadBase(), items)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "a string", got[0])
	assert.Equal(t, "feature-x", got[1].(map[string]interface{})["head"])
}

// TestNormalizingTwiceIsHarmful is why Response.Normalized exists.
//
// It asserts the hazard rather than the guard: normalizing before the match makes the returned body
// already-normalized, and the observe path would otherwise normalize it again. This shows that a second
// pass is NOT a harmless repeat — `.head = .head.ref` reads .ref off a value that is already the string,
// so the field is destroyed. Had the guard been omitted, the visible symptom would not have been an error
// but a status field silently going empty.
func TestNormalizingTwiceIsHarmful(t *testing.T) {
	items := []interface{}{githubPullItem(42, "feature-x", "main")}
	verb := flattenHeadBase()

	once, err := normalizeItems(context.Background(), verb, items)
	require.NoError(t, err)
	assert.Equal(t, "feature-x", once[0].(map[string]interface{})["head"], "one pass flattens correctly")

	twice, err := normalizeItems(context.Background(), verb, once)
	if err != nil {
		return // erroring on the second pass is an equally good demonstration
	}
	assert.NotEqual(t, "feature-x", twice[0].(map[string]interface{})["head"],
		"a second pass must be shown to damage the value; if this ever becomes idempotent the guard "+
			"in restResources.go is still correct, but this test no longer demonstrates why")
}

// pullsOpenAPISpec is the minimum document FindBy needs to issue the call.
const pullsOpenAPISpec = `
openapi: 3.0.0
info:
  title: Pulls API
  version: 1.0.0
paths:
  /pulls:
    get:
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema:
                type: array
                items:
                  type: object
`

// TestFindByEndToEndNormalizesBeforeMatching drives the REAL entry point.
//
// This test exists because the unit tests above did not, on their own, prove the fix. They showed that
// normalizeItems transforms correctly and that findItemInList matches normalized data — but nothing in
// them would have failed if FindBy had simply never called normalizeItems. Deleting the call from
// restclient.go left every one of them green.
//
// So this goes through FindBy against a live server and asserts the OUTCOME: a CR whose spec holds branch
// strings finds the pull request whose list item holds branch objects. That can only pass if the wiring
// is real.
func TestFindByEndToEndNormalizesBeforeMatching(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]interface{}{
			githubPullItem(1, "other-branch", "main"),
			githubPullItem(42, "feature-x", "main"),
		})
	}))
	defer server.Close()

	doc, err := libopenapi.NewDocument([]byte(pullsOpenAPISpec))
	require.NoError(t, err)
	v3Doc, errs := doc.BuildV3Model()
	require.Empty(t, errs)

	client := &UnstructuredClient{
		Server:                 server.URL,
		DocScheme:              v3Doc,
		IdentifierFields:       []string{"head", "base"},
		IdentifiersMatchPolicy: "AND",
		Resource:               crWithSpec(map[string]interface{}{"head": "feature-x", "base": "main"}),
	}

	resp, err := client.FindBy(context.Background(), server.Client(), "/pulls",
		&RequestConfiguration{Method: http.MethodGet}, flattenHeadBase())
	require.NoError(t, err,
		"findby must locate the PR; not-found here is not a benign failure -- the reconciler creates on "+
			"not-found, so this is the duplicate-PR loop the issue reported")

	body, ok := resp.ResponseBody.(map[string]interface{})
	require.True(t, ok)
	assert.EqualValues(t, 42, body["number"], "the right pull request, not merely some pull request")

	assert.True(t, resp.Normalized,
		"the returned body is already normalized, and the observe path must be told so -- normalizing it a "+
			"second time destroys the very fields this fix exists to produce")
}

// Without the transform declared, the same search legitimately finds nothing: the shapes genuinely do not
// match. This pins that the fix works by NORMALIZING, not by loosening the comparison.
func TestFindByEndToEndStillMissesWithoutATransform(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]interface{}{githubPullItem(42, "feature-x", "main")})
	}))
	defer server.Close()

	doc, err := libopenapi.NewDocument([]byte(pullsOpenAPISpec))
	require.NoError(t, err)
	v3Doc, _ := doc.BuildV3Model()

	client := &UnstructuredClient{
		Server:                 server.URL,
		DocScheme:              v3Doc,
		IdentifierFields:       []string{"head"},
		IdentifiersMatchPolicy: "AND",
		Resource:               crWithSpec(map[string]interface{}{"head": "feature-x"}),
	}

	plain := &getter.VerbsDescription{Action: "findby", Method: "GET", Path: "/pulls"}
	_, err = client.FindBy(context.Background(), server.Client(), "/pulls",
		&RequestConfiguration{Method: http.MethodGet}, plain)
	require.Error(t, err, "a string spec value must still not match an object item when nothing bridges them")
}
