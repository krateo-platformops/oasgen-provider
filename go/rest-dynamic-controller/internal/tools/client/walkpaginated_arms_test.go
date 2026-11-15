package restclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	libopenapi "github.com/pb33f/libopenapi"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// These cover the arms of the shared paginated walk that NOTHING reached before.
//
// Written because two mutation controls on the extracted WalkPaginated both PASSED: collapsing the
// Exhausted arm into a plain error, and plumbing a nil verb into normalizeItems, each left the whole
// client package green. The 15 tests matching "FindBy|Paginat" run, but TestFindBy's subtests declare
// no pagination (so they take CallFindBySingle and never enter the loop) and the header-token test
// matches on page one, returning before the end-of-collection arm.
//
// A refactor whose tests cannot fail on the thing it moved is unverified, however green it is.

func tokenPagedServer(t *testing.T, pages []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := 0
		if tok := r.URL.Query().Get("token"); tok != "" {
			if _, err := fmt.Sscanf(tok, "p%d", &idx); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		if idx >= len(pages) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// A next-token on every page BUT the last: this strategy's end signal is token absence.
		if idx < len(pages)-1 {
			w.Header().Set("X-Next-Token", fmt.Sprintf("p%d", idx+1))
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, pages[idx])
	}))
}

func pagedClient(t *testing.T, serverURL, wantName string) *UnstructuredClient {
	t.Helper()
	doc, err := libopenapi.NewDocument([]byte(paginatedOpenAPISpec))
	assert.NoError(t, err)
	v3Doc, errs := doc.BuildV3Model()
	assert.Empty(t, errs)
	return &UnstructuredClient{
		Server:     serverURL,
		HostPolicy: allowedPolicy(t, serverURL),
		DocScheme:  v3Doc,
		Resource: &unstructured.Unstructured{
			Object: map[string]interface{}{"spec": map[string]interface{}{"name": wantName}},
		},
		IdentifierFields: []string{"name"},
	}
}

func tokenVerb() *getter.VerbsDescription {
	return &getter.VerbsDescription{
		Pagination: &getter.Pagination{
			Type: "continuationToken",
			ContinuationToken: &getter.ContinuationTokenConfig{
				Request:  getter.ContinuationTokenRequest{TokenIn: "query", TokenPath: "token"},
				Response: getter.ContinuationTokenResponse{TokenIn: "header", TokenPath: "X-Next-Token"},
			},
		},
	}
}

// TestWalkPaginatedExhaustedIsASynthesisedNotFound is the arm the first mutation broke silently.
//
// Exhausted must become a 404 marked Synthesised -- and ONLY Exhausted may become a 404, because the
// reconciler creates on 404. A plain error here would make the existence checker unable to tell "we
// scanned everything and it is absent" from "something went wrong", which is how a live vendor resource
// gets duplicated (#371).
func TestWalkPaginatedExhaustedIsASynthesisedNotFound(t *testing.T) {
	server := tokenPagedServer(t, []string{
		`{"items": [{"id": "1", "name": "one"}]}`,
		`{"items": [{"id": "2", "name": "two"}]}`,
	})
	defer server.Close()

	// Looking for a name on NEITHER page, so the walk runs to the end of the collection.
	client := pagedClient(t, server.URL, "absent")

	_, err := client.FindBy(context.Background(), server.Client(), "/items", &RequestConfiguration{Method: http.MethodGet}, tokenVerb())
	if err == nil {
		t.Fatal("a complete scan with no match must report not-found, got nil")
	}

	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("want a *StatusError so the existence checker can read it, got %T: %v", err, err)
	}
	if se.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want 404", se.StatusCode)
	}
	if !se.Synthesised {
		t.Error("the 404 must be marked Synthesised: it is ours, not the vendor's (#371)")
	}
	if !strings.Contains(err.Error(), "page(s)") {
		t.Errorf("the error should say how many pages were scanned, got %q", err.Error())
	}
}

// TestWalkPaginatedAppliesTheVerbsResponseTransform is the arm the second mutation broke silently.
//
// Items are normalized into the CR's shape BEFORE the predicate sees them (#145). Plumbing a nil verb
// through left the package green because nothing matched on a transformed field through the loop -- so
// this test matches on a field that EXISTS ONLY AFTER the transform runs.
func TestWalkPaginatedAppliesTheVerbsResponseTransform(t *testing.T) {
	server := tokenPagedServer(t, []string{
		`{"items": [{"id": "1", "vendorName": "one"}]}`,
		`{"items": [{"id": "2", "vendorName": "two"}]}`,
	})
	defer server.Close()

	// The CR wants name=two; the vendor only ever sends vendorName. Only the transform bridges them.
	client := pagedClient(t, server.URL, "two")

	verb := tokenVerb()
	verb.Action = "findby"
	verb.ResponseTransform = &getter.JQProgram{Inline: `{id: .id, name: .vendorName}`}

	resp, err := client.FindBy(context.Background(), server.Client(), "/items", &RequestConfiguration{Method: http.MethodGet}, verb)
	if err != nil {
		t.Fatalf("the transform should make the page-2 item matchable, got %v", err)
	}
	body, ok := resp.ResponseBody.(map[string]interface{})
	if !ok {
		t.Fatalf("want an object body, got %T", resp.ResponseBody)
	}
	if body["name"] != "two" {
		t.Errorf("matched item name = %v, want \"two\" (the transformed field)", body["name"])
	}
	if !resp.Normalized {
		t.Error("Normalized must be true when a verb was supplied")
	}
}
