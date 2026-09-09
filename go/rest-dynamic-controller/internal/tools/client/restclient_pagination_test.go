package restclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/pb33f/libopenapi"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	paginatedOpenAPISpec = `
openapi: 3.0.0
info:
  title: Paginated API
  version: 1.0.0
paths:
  /items:
    get:
      summary: List all items with pagination
      responses:
        '200':
          description: A paginated list of items
          headers:
            X-Next-Token:
              schema:
                type: string
          content:
            application/json:
              schema:
                type: object
                properties:
                  items:
                    type: array
                    items:
                      type: object
                      properties:
                        id:
                          type: string
                        name:
                          type: string
                  nextToken:
                    type: string
`
)

func TestFindBy_Pagination_HeaderToken(t *testing.T) {
	// Mock server that simulates pagination via headers
	page1Response := `{"items": [{"id": "1", "name": "one"}]}`
	page2Response := `{"items": [{"id": "2", "name": "two"}]}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "page2" {
			// Return second page without next token
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, page2Response)
		} else {
			// Return first page with next token to get second page
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Next-Token", "page2")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, page1Response)
		}
	}))
	defer server.Close()

	doc, err := libopenapi.NewDocument([]byte(paginatedOpenAPISpec))
	assert.NoError(t, err)
	v3Doc, errs := doc.BuildV3Model()
	assert.Empty(t, errs)

	client := &UnstructuredClient{
		Server:    server.URL,
		DocScheme: v3Doc,
		Resource: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"spec": map[string]interface{}{
					"name": "two",
				},
			},
		},
		IdentifierFields: []string{"name"},
	}

	findByAction := &getter.VerbsDescription{
		Pagination: &getter.Pagination{
			Type: "continuationToken",
			ContinuationToken: &getter.ContinuationTokenConfig{
				Request: getter.ContinuationTokenRequest{
					TokenIn:   "query",
					TokenPath: "token",
				},
				Response: getter.ContinuationTokenResponse{
					TokenIn:   "header",
					TokenPath: "X-Next-Token",
				},
			},
		},
	}

	opts := &RequestConfiguration{
		Method: http.MethodGet,
	}

	resp, err := client.FindBy(context.Background(), server.Client(), "/items", opts, findByAction)
	assert.NoError(t, err)
	assert.NotNil(t, resp.ResponseBody)

	bodyMap, ok := resp.ResponseBody.(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, "2", bodyMap["id"])
	assert.Equal(t, "two", bodyMap["name"])
}

//func TestFindBy_Pagination_BodyToken(t *testing.T) {
//	// Mock server that simulates pagination via body
//	page1Response := `{"items": [{"id": "1", "name": "one"}], "nextToken": "page2"}`
//	page2Response := `{"items": [{"id": "2", "name": "two"}], "nextToken": ""}`
//
//	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
//		token := r.URL.Query().Get("token")
//		w.Header().Set("Content-Type", "application/json")
//		if token == "page2" {
//			fmt.Fprintln(w, page2Response)
//		} else {
//			fmt.Fprintln(w, page1Response)
//		}
//	}))
//	defer server.Close()
//
//	doc, err := libopenapi.NewDocument([]byte(paginatedOpenAPISpec))
//	assert.NoError(t, err)
//	v3Doc, errs := doc.BuildV3Model()
//	assert.Empty(t, errs)
//
//	client := &UnstructuredClient{
//		Server:    server.URL,
//		DocScheme: v3Doc,
//		Resource: &unstructured.Unstructured{
//			Object: map[string]interface{}{
//				"spec": map[string]interface{}{
//					"name": "two",
//				},
//			},
//		},
//		IdentifierFields: []string{"name"},
//	}
//
//	findByAction := &getter.VerbsDescription{
//		Pagination: &getter.Pagination{
//			Type: "continuationToken",
//			ContinuationToken: &getter.ContinuationTokenConfig{
//				Request: getter.ContinuationTokenRequest{
//					TokenIn:   "query",
//					TokenPath: "token",
//				},
//				Response: getter.ContinuationTokenResponse{
//					TokenIn:   "body",
//					TokenPath: "nextToken",
//				},
//			},
//		},
//	}
//
//	opts := &RequestConfiguration{
//		Method: http.MethodGet,
//	}
//
//	resp, err := client.FindBy(context.Background(), server.Client(), "/items", opts, findByAction)
//	assert.NoError(t, err)
//	assert.NotNil(t, resp.ResponseBody)
//
//	bodyMap, ok := resp.ResponseBody.(map[string]interface{})
//	assert.True(t, ok)
//	assert.Equal(t, "2", bodyMap["id"])
//}

func TestFindBy_NoPagination(t *testing.T) {
	// Mock server
	responsePayload := `[{"id": "1", "name": "one"}]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, responsePayload)
	}))
	defer server.Close()

	doc, err := libopenapi.NewDocument([]byte(paginatedOpenAPISpec))
	assert.NoError(t, err)
	v3Doc, errs := doc.BuildV3Model()
	assert.Empty(t, errs)

	client := &UnstructuredClient{
		Server:    server.URL,
		DocScheme: v3Doc,
		Resource: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"spec": map[string]interface{}{
					"name": "one",
				},
			},
		},
		IdentifierFields: []string{"name"},
	}

	// No pagination in FindBy action
	findByAction := &getter.VerbsDescription{}

	opts := &RequestConfiguration{
		Method: http.MethodGet,
	}

	resp, err := client.FindBy(context.Background(), server.Client(), "/items", opts, findByAction)
	assert.NoError(t, err)
	assert.NotNil(t, resp.ResponseBody)

	bodyMap, ok := resp.ResponseBody.(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, "1", bodyMap["id"])
}

// TestFindBy_Pagination_NeverTerminating covers #119: the paginated findby loop had no page cap, so a
// server that always advertises a next page walked forever. A reconcile that never returns is worse
// than one that fails — it holds a worker and produces no diagnosis.
//
// The assertion that matters is not merely "it stops": it is that stopping is reported as a DISTINCT
// outcome from not-found. IsNotFoundError keys on a 404, and the reconciler acts on not-found by
// CREATING the resource. Folding the cap into a 404 would tell it "this does not exist" when the truth
// is "I stopped looking", and it would create a duplicate of the very object it never finished
// searching for — reintroducing, through a different door, the silent-wrong failure #119 is about.
func TestFindBy_Pagination_NeverTerminating(t *testing.T) {
	var requests int32
	// Always answers with a next-page token and never the item being searched for.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Next-Token", "always-more")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"items": [{"id": "x", "name": "not-the-one"}]}`)
	}))
	defer server.Close()

	doc, err := libopenapi.NewDocument([]byte(paginatedOpenAPISpec))
	assert.NoError(t, err)
	v3Doc, errs := doc.BuildV3Model()
	assert.Empty(t, errs)

	client := &UnstructuredClient{
		Server:    server.URL,
		DocScheme: v3Doc,
		Resource: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"spec": map[string]interface{}{"name": "never-present"},
			},
		},
		IdentifierFields: []string{"name"},
	}
	findByAction := &getter.VerbsDescription{
		Pagination: &getter.Pagination{
			Type: "continuationToken",
			ContinuationToken: &getter.ContinuationTokenConfig{
				Request:  getter.ContinuationTokenRequest{TokenIn: "query", TokenPath: "token"},
				Response: getter.ContinuationTokenResponse{TokenIn: "header", TokenPath: "X-Next-Token"},
			},
		},
	}

	_, err = client.FindBy(context.Background(), server.Client(), "/items",
		&RequestConfiguration{Method: http.MethodGet}, findByAction)

	// 1. It terminates at all.
	assert.Error(t, err, "an endlessly-paginating server must not loop forever")

	// 2. It is NOT reported as absence. This is the load-bearing assertion.
	assert.False(t, IsNotFoundError(err),
		"hitting the page cap must not look like not-found: the reconciler creates on not-found, so it "+
			"would duplicate a resource it never finished searching for (#119)")

	// 3. The error says which of the two happened, so an operator is not left guessing.
	assert.Contains(t, err.Error(), "safety limit")

	// 4. It stopped at the cap rather than merely somewhere.
	assert.Equal(t, int32(maxFindByPages), atomic.LoadInt32(&requests),
		"should scan exactly maxFindByPages pages before giving up")
}
