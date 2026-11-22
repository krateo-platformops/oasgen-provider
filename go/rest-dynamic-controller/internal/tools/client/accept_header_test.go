//go:build unit || integration

package restclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pb33f/libopenapi"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/pagination"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// acceptSpec declares one path per media-type shape the derivation has to distinguish.
const acceptSpec = `
openapi: 3.0.0
info:
  title: accept
  version: 1.0.0
paths:
  /plain:
    get:
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: {type: object}
  /vendor:
    get:
      responses:
        '200':
          description: ok
          content:
            application/vnd.github+json:
              schema: {type: object}
  /xml:
    get:
      responses:
        '200':
          description: ok
          content:
            application/xml:
              schema: {type: object}
  /undeclared:
    get:
      responses:
        '200':
          description: ok, and declaring NO content at all
  /nonnumeric:
    get:
      responses:
        default:
          description: whatever
          content:
            application/xml:
              schema: {type: object}
        '200':
          description: ok
          content:
            application/json:
              schema: {type: object}
  /lowest-wins:
    get:
      responses:
        '201':
          description: created
          content:
            application/vnd.late+json:
              schema: {type: object}
        '200':
          description: ok
          content:
            application/vnd.early+json:
              schema: {type: object}
`

// acceptRecorder answers every request 200 and records the Accept header AS THE SERVER SAW IT.
//
// WHY THE SERVER AND NOT THE HEADER MAP. A test that reads the same http.Header the code just wrote cannot
// tell "filled the map" from "sent the request" -- the map is the code's own account of itself. Asking the
// server is the only channel that carries the distinction.
func acceptRecorder(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Header.Get("Accept"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"items":[{"id":"1"}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func acceptClientFor(t *testing.T, serverURL string) *UnstructuredClient {
	t.Helper()
	doc, err := libopenapi.NewDocument([]byte(strings.TrimSpace(acceptSpec)))
	require.NoError(t, err)
	v3Doc, errs := doc.BuildV3Model()
	require.Empty(t, errs)
	return &UnstructuredClient{
		Server:     serverURL,
		HostPolicy: allowedPolicy(t, serverURL),
		DocScheme:  v3Doc,
	}
}

// TestAcceptReachesTheWire is the regression test for the 057 break: a correlation.observe is a GET with no
// body, and RDC sent it with NO Accept header at all, which Zendesk answers 415 to before checking auth.
func TestAcceptReachesTheWire(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{
			// THE BREAK. A GET has no body, so the Content-Type block never runs; Accept had to be set
			// outside it or this stays empty.
			name: "a bodyless GET still carries Accept",
			path: "/plain",
			want: "application/json",
		},
		{
			// The vendor document IS the authority for a type RDC can parse. A strict equality check
			// against application/json would have thrown this away.
			name: "a declared vendor JSON type is sent verbatim",
			path: "/vendor",
			want: "application/vnd.github+json",
		},
		{
			// THE HAZARD IN THE ORIGINAL PROPOSAL. Copying the declaration across would send
			// application/xml, which Zendesk itself answers 415 to -- the fix would have created the
			// same break on another API. Accept states what the CLIENT can parse.
			name: "a declared XML type is NOT sent; RDC cannot parse it",
			path: "/xml",
			want: "application/json",
		},
		{
			// 200 WITH NO `content` KEY. It must declare 200 rather than 204, because the client
			// validates the status against the OAS's declared codes before anything else -- a 204-only
			// path makes the recorder's 200 "invalid status code", and the arm would fail in the setup
			// rather than tell me anything about Accept.
			name: "a 2xx declaring no content falls back",
			path: "/undeclared",
			want: "application/json",
		},
		{
			// `default` is not a number. Skipped, not an error: an undiscoverable media type must not
			// make a request unsendable.
			name: "a non-numeric response key is skipped, not fatal",
			path: "/nonnumeric",
			want: "application/json",
		},
		{
			name: "the lowest 2xx wins, so map order cannot decide it",
			path: "/lowest-wins",
			want: "application/vnd.early+json",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen []string
			srv := acceptRecorder(t, &seen)
			cli := acceptClientFor(t, srv.URL)

			_, err := cli.Call(context.Background(), srv.Client(), tc.path, &RequestConfiguration{Method: http.MethodGet})
			require.NoError(t, err)

			require.Len(t, seen, 1, "the server must have been reached exactly once")
			assert.Equal(t, tc.want, seen[0],
				"this is the Accept header the SERVER received, not the map the client built")
		})
	}
}

// TestAcceptOnThePaginatedPath covers CallForPagination, which WalkPaginated delegates to -- and
// WalkPaginated is what a correlation.observe with pagination actually calls (dispatch.go:234).
//
// TWO SITES, NOT ONE: Call and CallForPagination each build their own &http.Request{} literal. A fix at one
// is half a fix, and the half that matters for 057 is whichever one the chain happens to take.
func TestAcceptOnThePaginatedPath(t *testing.T) {
	var seen []string
	srv := acceptRecorder(t, &seen)
	cli := acceptClientFor(t, srv.URL)

	_, _, err := cli.CallForPagination(context.Background(), srv.Client(), "/vendor",
		&RequestConfiguration{Method: http.MethodGet}, onePagePaginator{})
	require.NoError(t, err)

	require.Len(t, seen, 1)
	assert.Equal(t, "application/vnd.github+json", seen[0],
		"CallForPagination is a SEPARATE request literal; it needs the header independently of Call")
}

// TestAVerbsHeadersStillWin pins the override, which comes free from opts.Headers being applied after.
func TestAVerbsHeadersStillWin(t *testing.T) {
	var seen []string
	srv := acceptRecorder(t, &seen)
	cli := acceptClientFor(t, srv.URL)

	_, err := cli.Call(context.Background(), srv.Client(), "/plain", &RequestConfiguration{
		Method:  http.MethodGet,
		Headers: map[string]string{"Accept": "application/vnd.author-knows-best+json"},
	})
	require.NoError(t, err)

	require.Len(t, seen, 1)
	assert.Equal(t, "application/vnd.author-knows-best+json", seen[0],
		"a verbsDescription[].headers entry is the author's explicit override for a document that "+
			"declares the wrong thing; it must beat the derived value")
}

// TestAServerThatRefusesAMissingAccept is acmp's fake, in this repo: the exact behaviour Zendesk showed.
// It fails on the old code and passes on the new, which is what makes it a regression test rather than a
// restatement of the implementation.
func TestAServerThatRefusesAMissingAccept(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Zendesk's own rule, measured over 11 responses: 415 BEFORE auth when Accept is absent or
		// names something it will not serve.
		ok := false
		for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
			if isAcceptableMediaType(part) {
				ok = true
				break
			}
		}
		if !ok {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"items":[]}`)
	}))
	t.Cleanup(srv.Close)

	cli := acceptClientFor(t, srv.URL)
	_, err := cli.Call(context.Background(), srv.Client(), "/plain", &RequestConfiguration{Method: http.MethodGet})
	assert.NoError(t, err, "a server that 415s a missing Accept must now be reachable; this is the 057 break")
}

// TestIsAcceptableMediaType is the rule zendesk-kog measured against 11 Zendesk responses.
func TestIsAcceptableMediaType(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"application/json", true},
		{"application/vnd.github+json", true},
		{"application/json-patch+json", true},
		{"application/json; charset=utf-8", true}, // parameters dropped
		{"  APPLICATION/JSON  ", true},            // case and space insensitive
		{"*/*", true},
		{"application/xml", false}, // Zendesk answers 415 to this
		{"text/html", false},       // and to this
		{"", false},
		{"application/octet-stream", false},
	} {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, isAcceptableMediaType(tc.in))
		})
	}
}

// onePagePaginator is the smallest thing satisfying pagination.Paginator: it touches no header and reports
// the collection as ended.
//
// WHY A STUB AND NOT A REAL PAGINATOR. The subject here is the Accept header; a real page-number config
// would put maxPages validation, a response shape and a query-parameter name into the setup, any of which
// failing would fail this test for a reason that has nothing to do with Accept. The stub also proves
// something a real one would hide: Accept survives whatever the paginator does to the request afterwards,
// because UpdateRequest runs AFTER the header is set.
type onePagePaginator struct{}

func (onePagePaginator) Init() {}

func (onePagePaginator) UpdateRequest(req *http.Request) error { return nil }

func (onePagePaginator) Next(p pagination.Page) (pagination.PageResult, error) {
	return pagination.Exhausted, nil
}
