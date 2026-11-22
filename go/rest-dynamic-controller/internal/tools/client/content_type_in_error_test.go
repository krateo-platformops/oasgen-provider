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

const ctSpec = `
openapi: 3.0.0
info:
  title: ct
  version: 1.0.0
paths:
  /thing:
    get:
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: {type: object}
`

// bodyServer answers 200 with whatever body and Content-Type the case names, including none.
func bodyServer(t *testing.T, contentType, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		} else {
			// Go sets a Content-Type by sniffing unless the key is present and empty.
			w.Header()["Content-Type"] = nil
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func ctClientFor(t *testing.T, serverURL string) *UnstructuredClient {
	t.Helper()
	doc, err := libopenapi.NewDocument([]byte(strings.TrimSpace(ctSpec)))
	require.NoError(t, err)
	v3Doc, errs := doc.BuildV3Model()
	require.Empty(t, errs)
	return &UnstructuredClient{Server: serverURL, HostPolicy: allowedPolicy(t, serverURL), DocScheme: v3Doc}
}

// TestTheErrorNamesWhatTheServerSent is the point of #894: the header was on the response all along and
// nothing read it, so an HTML block page or a login page surfaced as a bare JSON parse error.
//
// MEASURED INSTANCE: openstack-nova-operator-kog spent the first hour of #379's credential-follows-a-302
// investigation starting from "invalid character '<'", when the response Content-Type was the one field
// distinguishing "the API answered badly" from "something else answered".
func TestTheErrorNamesWhatTheServerSent(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
		wantInError []string
	}{
		{
			// A WAF block page, a proxy error page, or a redirect to a login form.
			name:        "an HTML body names text/html, with the charset KEPT",
			contentType: "text/html; charset=utf-8",
			body:        "<html><body>Access Denied</body></html>",
			// VERBATIM, not normalised: the charset is what sometimes identifies the intermediary.
			wantInError: []string{"text/html; charset=utf-8", "did not parse as JSON"},
		},
		{
			name:        "a plain-text gateway error keeps its exact label",
			contentType: "text/plain",
			body:        "502 Bad Gateway",
			wantInError: []string{"text/plain", "did not parse as JSON"},
		},
		{
			// AN ABSENT HEADER IS ITS OWN FACT, and must not render as an empty gap in the sentence.
			name:        "no Content-Type at all says so in words",
			contentType: "",
			body:        "not json either way",
			wantInError: []string{"(none sent)", "did not parse as JSON"},
		},
		{
			// THE MISLABELLED-BUT-VALID CASE, which must NOT fail. Gateways do this constantly, and
			// rejecting on the header would break a working resource to improve a message.
			name:        "a JSON body labelled text/plain still parses",
			contentType: "text/plain",
			body:        `{"id":"ok"}`,
			wantInError: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := bodyServer(t, tc.contentType, tc.body)
			cli := ctClientFor(t, srv.URL)

			_, err := cli.Call(context.Background(), srv.Client(), "/thing", &RequestConfiguration{Method: http.MethodGet})

			if tc.wantInError == nil {
				assert.NoError(t, err, "a mislabelled but valid JSON body must still parse; this field is "+
					"diagnostic and must never reject a response")
				return
			}
			require.Error(t, err)
			for _, want := range tc.wantInError {
				assert.Contains(t, err.Error(), want,
					"the error must name what the server sent; got: %s", err.Error())
			}
			// AND THE OLD MESSAGE MUST BE GONE: "converting JSON to YAML" named an operation that never
			// happened, because the body was never JSON.
			assert.NotContains(t, err.Error(), "converting JSON to YAML",
				"the old wording named the wrong operation and should not survive")
		})
	}
}

// TestDescribeContentTypeIsVerbatimAndBounded pins the renderer, including the truncation.
func TestDescribeContentTypeIsVerbatimAndBounded(t *testing.T) {
	assert.Equal(t, "(none sent)", describeContentType(""))
	assert.Equal(t, "(none sent)", describeContentType("   "))
	assert.Equal(t, "application/json;charset=UTF-8", describeContentType("application/json;charset=UTF-8"),
		"no normalisation: the exact spelling, missing space and all, is what identifies the sender")

	long := "application/" + strings.Repeat("x", 300)
	got := describeContentType(long)
	assert.True(t, strings.HasSuffix(got, "...(truncated)"), "a hostile header must not flood the condition")
	assert.Less(t, len(got), 160, "bounded")
	assert.True(t, strings.HasPrefix(got, "application/xxx"), "the beginning is kept, which is the useful part")
}

// TestTheErrorNamesTheContentTypeOnThePaginatedPathToo covers the SECOND handleResponse caller.
//
// WHY THIS ARM EXISTS AT ALL: handleResponse is called from Call (restclient.go:480) and from
// CallForPagination (:1084). On #892 I shipped an identical two-site change and tested only the first
// path -- github-provider-kog asked for the missing control and it turned out to be the one gap in the
// set. Writing it here instead of being asked again.
func TestTheErrorNamesTheContentTypeOnThePaginatedPathToo(t *testing.T) {
	srv := bodyServer(t, "text/html; charset=utf-8", "<html>nope</html>")
	cli := ctClientFor(t, srv.URL)

	_, _, err := cli.CallForPagination(context.Background(), srv.Client(), "/thing",
		&RequestConfiguration{Method: http.MethodGet}, ctOnePage{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "text/html; charset=utf-8",
		"CallForPagination is a SEPARATE handleResponse call site and needs the header independently")
	assert.NotContains(t, err.Error(), "converting JSON to YAML")
}

// ctOnePage is the smallest pagination.Paginator: the subject is the error message, so pagination must
// not be part of this test's failure surface.
type ctOnePage struct{}

func (ctOnePage) Init() {}

func (ctOnePage) UpdateRequest(req *http.Request) error { return nil }

func (ctOnePage) Next(p pagination.Page) (pagination.PageResult, error) {
	return pagination.Exhausted, nil
}
