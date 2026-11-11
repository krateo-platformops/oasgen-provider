package restclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/pb33f/libopenapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// echoServer answers with the given body and headers, so a test can make the RESPONSE the subject. The
// existing redaction test points at a server that returns no body at all, which is why it proved request
// redaction and could never have caught #566.
func echoServer(t *testing.T, body string, hdr map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// THE REGRESSION TEST FOR #566, part 1: the resource's OWN resolved secret, echoed back by the vendor.
//
// RedactValues holds this string, so value-based redaction is sufficient here -- it simply was never
// applied to the response. Run over BOTH PrettyJSON values because that is the axis the original report
// did not cover: the defect was found in dumpResponseWithPrettyJSON, and httputil.DumpResponse leaked
// identically, so a fix to one would have looked complete.
func TestTheResponseBodyIsRedactedOnBothDumpPaths(t *testing.T) {
	for _, pretty := range []bool{false, true} {
		t.Run(fmt.Sprintf("PrettyJSON=%v", pretty), func(t *testing.T) {
			srv := echoServer(t, `{"id":"c1","secret":"hunter2"}`, nil)

			var out bytes.Buffer
			rt := &debuggingRoundTripper{
				Transport:    http.DefaultTransport,
				Out:          &out,
				PrettyJSON:   pretty,
				RedactValues: []string{"hunter2"},
			}
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/clients/c1", nil)
			require.NoError(t, err)
			resp, err := (&http.Client{Transport: rt}).Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.NotContains(t, out.String(), "hunter2",
				"the resolved secret reached the log in cleartext through the response dump (#566)")
			assert.Contains(t, out.String(), "***REDACTED***")

			// CONTROL: the dump must still contain the non-sensitive part of the body, or this test would
			// also pass against a round tripper that had stopped dumping responses altogether.
			assert.Contains(t, out.String(), "c1",
				"CONTROL FAILED: nothing of the response body was dumped, so the absence above is not evidence")

			// And the body must still be READABLE by the caller: the dump reads it and has to put it back.
			b, rerr := io.ReadAll(resp.Body)
			require.NoError(t, rerr)
			assert.Contains(t, string(b), "hunter2", "dumping consumed the response body")
		})
	}
}

// #566, part 3: the response HEADERS. redact applies credentialHeaderRe as well as the value list, and
// because it never ran on the response path a credential header came back untouched. Not in the original
// report -- the same single omission causes it.
func TestResponseHeadersAreRedacted(t *testing.T) {
	for _, pretty := range []bool{false, true} {
		t.Run(fmt.Sprintf("PrettyJSON=%v", pretty), func(t *testing.T) {
			srv := echoServer(t, `{"ok":true}`, map[string]string{"X-Api-Key": "leaked-key-value"})

			var out bytes.Buffer
			rt := &debuggingRoundTripper{Transport: http.DefaultTransport, Out: &out, PrettyJSON: pretty}
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/thing", nil)
			require.NoError(t, err)
			resp, err := (&http.Client{Transport: rt}).Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.NotContains(t, out.String(), "leaked-key-value",
				"a credential header in the RESPONSE reached the log in cleartext (#566)")
			assert.Contains(t, out.String(), "X-Api-Key",
				"CONTROL FAILED: the header name is absent too, so headers were not dumped at all and the "+
					"assertion above proves nothing")
		})
	}
}

// #566, part 2 -- the leak no redaction can reach. A collection response carries objects nobody resolved,
// so their secrets are not in RedactValues. The body is therefore not written at all.
func TestACollectionResponseBodyIsNotDumped(t *testing.T) {
	// Two clients, and the secret of the one NOT being reconciled. Value-based redaction cannot match it:
	// nothing ever resolved it, which is the whole point.
	const list = `[{"id":"mine","secret":"my-own-secret"},{"id":"someone-else","secret":"other-tenants-secret"}]`

	for _, pretty := range []bool{false, true} {
		t.Run(fmt.Sprintf("PrettyJSON=%v", pretty), func(t *testing.T) {
			srv := echoServer(t, list, nil)

			var out bytes.Buffer
			rt := &debuggingRoundTripper{
				Transport:            http.DefaultTransport,
				Out:                  &out,
				PrettyJSON:           pretty,
				RedactValues:         []string{"my-own-secret"}, // only OUR value is known
				SuppressResponseBody: true,
			}
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/clients", nil)
			require.NoError(t, err)
			resp, err := (&http.Client{Transport: rt}).Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.NotContains(t, out.String(), "other-tenants-secret",
				"another object's secret reached the log from a collection response (#566)")
			assert.NotContains(t, out.String(), "my-own-secret")
			assert.NotContains(t, out.String(), "someone-else",
				"the collection body was dumped; suppression did not take effect")

			// The dump must still be USEFUL and must explain itself.
			assert.Contains(t, out.String(), "200 OK", "the status line was suppressed along with the body")
			assert.Contains(t, out.String(), "body omitted",
				"the body is missing with no explanation, which reads as an empty response")
			assert.Contains(t, out.String(), "566", "the notice does not point anywhere")

			// The caller still gets the body: suppression is about the LOG, not about the response.
			b, rerr := io.ReadAll(resp.Body)
			require.NoError(t, rerr)
			assert.Contains(t, string(b), "other-tenants-secret",
				"suppressing the dump also withheld the body from the caller, which would break findby")
		})
	}
}

// THE CONTROL FOR THE TEST ABOVE, and the one that makes it readable: with suppression OFF the body IS
// dumped. Without this, every assertion in the previous test would also pass against a round tripper
// that had stopped dumping response bodies entirely -- the setup and the subject would fail identically.
func TestANonCollectionResponseBodyIsStillDumped(t *testing.T) {
	for _, pretty := range []bool{false, true} {
		t.Run(fmt.Sprintf("PrettyJSON=%v", pretty), func(t *testing.T) {
			srv := echoServer(t, `{"id":"mine","name":"visible"}`, nil)

			var out bytes.Buffer
			rt := &debuggingRoundTripper{
				Transport:            http.DefaultTransport,
				Out:                  &out,
				PrettyJSON:           pretty,
				SuppressResponseBody: false,
			}
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/clients/mine", nil)
			require.NoError(t, err)
			resp, err := (&http.Client{Transport: rt}).Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Contains(t, out.String(), "visible",
				"a single-object response body is no longer dumped, so the fix has disabled verbose "+
					"debugging rather than narrowing it")
			assert.NotContains(t, out.String(), "body omitted")
		})
	}
}

// FOUND BY A SURVIVING MUTANT, and it is the combination this fix itself creates.
//
// dumpResponseWithPrettyJSON has a SECOND write, in its `!dumpBody` early return, and that is the path a
// SUPPRESSED collection read now takes. Removing redact from it left every other arm green:
// TestResponseHeadersAreRedacted sends a body, so it reaches the final write instead and never exercises
// this branch at all.
//
// So the headers-only path was the one place redaction was unasserted, and suppression is what routes
// traffic into it -- the fix moved the dangerous case onto the untested branch. A credential header on a
// collection response would have leaked with every other test passing.
func TestASuppressedCollectionStillRedactsItsHeaders(t *testing.T) {
	for _, pretty := range []bool{false, true} {
		t.Run(fmt.Sprintf("PrettyJSON=%v", pretty), func(t *testing.T) {
			srv := echoServer(t, `[{"id":"a","secret":"other-tenants-secret"}]`,
				map[string]string{"X-Api-Key": "leaked-key-value", "Set-Cookie": "session=tok-abc123"})

			var out bytes.Buffer
			rt := &debuggingRoundTripper{
				Transport:            http.DefaultTransport,
				Out:                  &out,
				PrettyJSON:           pretty,
				RedactValues:         []string{"tok-abc123"},
				SuppressResponseBody: true,
			}
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/clients", nil)
			require.NoError(t, err)
			resp, err := (&http.Client{Transport: rt}).Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			got := out.String()
			assert.NotContains(t, got, "leaked-key-value",
				"a credential header leaked on the headers-only path, which is the path a suppressed "+
					"collection read takes (#566)")
			assert.NotContains(t, got, "tok-abc123",
				"a RedactValues value leaked on the headers-only path")
			assert.NotContains(t, got, "other-tenants-secret", "the collection body was dumped")

			// CONTROLS: the dump must still be a dump, or the absences above are vacuous.
			assert.Contains(t, got, "X-Api-Key", "CONTROL FAILED: no headers were dumped at all")
			assert.Contains(t, got, "200 OK", "CONTROL FAILED: no status line")
			assert.Contains(t, got, "body omitted")
		})
	}
}

const findByCollectionSpec = `
openapi: 3.0.0
info: {title: clients, version: "1.0"}
paths:
  /clients:
    get:
      operationId: listClients
      responses:
        "200": {description: ok}
`

// THE WIRING, end to end through the real FindBy. The round-tripper tests above prove the flag is
// HONOURED; this proves it is SET, and that no call site has to remember to set it.
//
// Asserted on the shipped field rather than on a dump, because do() writes to os.Stdout directly and
// capturing it would make this test order-dependent under -race. The two halves meet at one named field,
// and the assignment between them -- SuppressResponseBody: collectionRead -- is a direct field
// initialisation the compiler checks.
func TestFindByMarksTheCallAsACollectionRead(t *testing.T) {
	srv := echoServer(t, `[{"id":"mine","name":"two"}]`, nil)

	doc, err := libopenapi.NewDocument([]byte(findByCollectionSpec))
	require.NoError(t, err)
	v3Doc, errs := doc.BuildV3Model()
	require.Empty(t, errs)

	client := &UnstructuredClient{
		Server:     srv.URL,
		HostPolicy: allowedPolicy(t, srv.URL),
		DocScheme:  v3Doc,
		Resource: &unstructured.Unstructured{
			Object: map[string]interface{}{"spec": map[string]interface{}{"name": "two"}},
		},
		IdentifierFields: []string{"name"},
	}

	opts := &RequestConfiguration{Method: http.MethodGet}
	require.False(t, opts.CollectionRead, "precondition: the flag starts unset")

	_, _ = client.FindBy(context.Background(), srv.Client(), "/clients", opts, &getter.VerbsDescription{})

	assert.True(t, opts.CollectionRead,
		"FindBy did not mark the call as a collection read, so a verbose dump would write the whole "+
			"list body -- including the secrets of objects this CR does not own (#566)")
}

// A get/create/update/delete must NOT be marked, or the fix would suppress every response body and the
// previous test would pass for the wrong reason.
func TestAPlainCallIsNotMarkedAsACollectionRead(t *testing.T) {
	srv := echoServer(t, `{"id":"mine"}`, nil)

	doc, err := libopenapi.NewDocument([]byte(findByCollectionSpec))
	require.NoError(t, err)
	v3Doc, errs := doc.BuildV3Model()
	require.Empty(t, errs)

	client := &UnstructuredClient{
		Server:     srv.URL,
		HostPolicy: allowedPolicy(t, srv.URL),
		DocScheme:  v3Doc,
	}

	opts := &RequestConfiguration{Method: http.MethodGet}
	_, _ = client.Call(context.Background(), srv.Client(), "/clients", opts)

	assert.False(t, opts.CollectionRead,
		"Call marked a non-collection request as a collection read, which would suppress the response "+
			"body of every get, create, update and delete")
}

// Guards the one thing a reader of this file would otherwise have to take on trust: that os.Stdout is
// where production dumps go, so the Out field above is a test seam and not a second, divergent path.
func TestTheProductionDumpTargetIsStdout(t *testing.T) {
	b, err := os.ReadFile("restclient.go")
	require.NoError(t, err)
	src := string(b)

	i := strings.Index(src, "base = &debuggingRoundTripper{")
	require.NotEqual(t, -1, i, "the round tripper is no longer constructed here; this guard needs updating")
	block := src[i : i+400]

	assert.Contains(t, block, "Out:", "the constructed round tripper sets no Out")
	assert.Contains(t, block, "os.Stdout")
	assert.Contains(t, block, "SuppressResponseBody: collectionRead",
		"the production round tripper does not receive the collection-read flag, so the suppression "+
			"proved by the tests above never happens in a real controller")
}

// -------- #567: the vendor error snippet, a channel that needs no flag --------

func errorServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintln(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// THE REGRESSION TEST FOR #567 on the plain path. logVendorBody puts this string in the log at INFO on
// every failed call, with no krateo.io/connector-verbose required -- so it was the wider of the two leaks
// even though its content is narrower than a full dump.
func TestTheVendorErrorSnippetIsRedacted(t *testing.T) {
	srv := errorServer(t, `{"error":"invalid value for 'secret': hunter2 is too short"}`)

	doc, err := libopenapi.NewDocument([]byte(findByCollectionSpec))
	require.NoError(t, err)
	v3Doc, errs := doc.BuildV3Model()
	require.Empty(t, errs)

	client := &UnstructuredClient{
		Server: srv.URL, HostPolicy: allowedPolicy(t, srv.URL), DocScheme: v3Doc,
	}
	opts := &RequestConfiguration{Method: http.MethodGet, SensitiveValues: []string{"hunter2"}}

	_, callErr := client.Call(context.Background(), srv.Client(), "/clients", opts)
	require.Error(t, callErr, "the server answered 400, so a StatusError must have been produced")

	snippet := VendorBodySnippet(callErr)
	require.NotEmpty(t, snippet,
		"CONTROL FAILED: no snippet was captured at all, so the assertion below proves nothing")
	assert.NotContains(t, snippet, "hunter2",
		"the resolved secret is in the vendor error snippet, which logVendorBody writes to the log at "+
			"Info on every default install (#567)")
	assert.Contains(t, snippet, "invalid value",
		"CONTROL FAILED: the vendor's explanation was destroyed rather than redacted -- the whole point "+
			"of keeping this channel is that the operator can read the reason")
}

// AND THE PAGINATED SITE. Two parallel construction sites, and fixing one while the issue reads as closed
// is precisely how the response-dump half of this hid for as long as it did.
func TestTheVendorErrorSnippetIsRedactedOnThePaginatedPath(t *testing.T) {
	srv := errorServer(t, `{"error":"bad token: hunter2"}`)

	doc, err := libopenapi.NewDocument([]byte(findByCollectionSpec))
	require.NoError(t, err)
	v3Doc, errs := doc.BuildV3Model()
	require.Empty(t, errs)

	client := &UnstructuredClient{
		Server: srv.URL, HostPolicy: allowedPolicy(t, srv.URL), DocScheme: v3Doc,
		Resource: &unstructured.Unstructured{
			Object: map[string]interface{}{"spec": map[string]interface{}{"name": "x"}},
		},
		IdentifierFields: []string{"name"},
	}
	opts := &RequestConfiguration{Method: http.MethodGet, SensitiveValues: []string{"hunter2"}}

	findByAction := &getter.VerbsDescription{
		Pagination: &getter.Pagination{
			Type: "continuationToken",
			ContinuationToken: &getter.ContinuationTokenConfig{
				Request:  getter.ContinuationTokenRequest{TokenIn: "query", TokenPath: "token"},
				Response: getter.ContinuationTokenResponse{TokenIn: "header", TokenPath: "X-Next-Token"},
			},
		},
	}

	_, callErr := client.FindBy(context.Background(), srv.Client(), "/clients", opts, findByAction)
	require.Error(t, callErr)

	snippet := VendorBodySnippet(callErr)
	require.NotEmpty(t, snippet, "CONTROL FAILED: the paginated path captured no snippet")
	assert.NotContains(t, snippet, "hunter2",
		"the paginated construction site still captures the vendor body unredacted (#567)")
	assert.Contains(t, snippet, "bad token", "CONTROL FAILED: the explanation was destroyed")
}
