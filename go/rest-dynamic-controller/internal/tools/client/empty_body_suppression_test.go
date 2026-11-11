//go:build unit || integration

package restclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/pb33f/libopenapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WHAT THIS PINS, AND WHY IT HAD NO TEST
//
// `Call` decides whether to send a request body at all:
//
//	hasBody := false
//	switch b := opts.Body.(type) {
//	case nil:
//	case map[string]any:
//	        hasBody = len(b) != 0      // an EMPTY MAP is NOT sent
//	case []any:
//	        hasBody = true             // an EMPTY ARRAY IS sent
//	default:
//	        return Response{}, fmt.Errorf("invalid body type: %T", opts.Body)
//	}
//
// The asymmetry between the two empty cases is deliberate and is documented in the source: `[]` is a
// well-formed no-op JSON Patch and must reach the server, while `{}` is the assembled-but-unset body this
// code declines to send. Folding them together would make a legitimate no-op indistinguishable from
// "nothing to send".
//
// **Nothing asserted any of it.** Before this file, `hasBody` appeared in exactly one file --
// restclient.go -- and in zero test files. So the empty-map suppression, which a consumer's Keystone kind
// depends on via `requestTransform: '{}'`, and the empty-array exemption, which Ironic's PATCH depends on
// (#308), were both load-bearing and unguarded. Either could have been "tidied" into the other by a
// reader who saw two empty collections treated differently and assumed it was an oversight.
//
// GROUND TRUTH IS THE SERVER, NOT THE CLIENT. Each case asserts on what the httptest handler actually
// received, because the question is what reached the wire. Asking the client what it believes it sent
// would re-ask the code under test.

const emptyBodySpec = `
openapi: 3.0.0
info: {title: clients, version: "1.0"}
paths:
  /clients:
    post:
      operationId: createClient
      responses:
        "200": {description: ok}
    patch:
      operationId: patchClient
      responses:
        "200": {description: ok}
`

// recordingServer captures the raw bytes of the request body and whether a body was present at all.
type recorded struct {
	body    []byte
	present bool
	hits    int
}

func recordingServer(t *testing.T, rec *recorded) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.hits++
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		rec.body = b
		// Content-Length is the honest signal for "a body was sent": net/http sets it from the payload,
		// and a request built with no payload has no body for the handler to read.
		rec.present = len(b) > 0
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"id":"c1"}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func clientFor(t *testing.T, serverURL string) *UnstructuredClient {
	t.Helper()
	doc, err := libopenapi.NewDocument([]byte(emptyBodySpec))
	require.NoError(t, err)
	v3Doc, errs := doc.BuildV3Model()
	require.Empty(t, errs)
	return &UnstructuredClient{
		Server:     serverURL,
		HostPolicy: allowedPolicy(t, serverURL),
		DocScheme:  v3Doc,
	}
}

func TestOnlyAnEmptyMapIsSuppressedFromTheRequestBody(t *testing.T) {
	cases := []struct {
		name        string
		body        any
		method      string
		wantSent    bool
		wantPayload string
		wantErr     string
	}{
		{
			name:     "nil body sends nothing",
			body:     nil,
			method:   http.MethodPost,
			wantSent: false,
		},
		{
			// THE CONSUMER CASE: `requestTransform: '{}'` assembles an empty map, and the point is that
			// no body is sent. A Keystone kind relies on this.
			name:     "an EMPTY MAP is suppressed",
			body:     map[string]any{},
			method:   http.MethodPost,
			wantSent: false,
		},
		{
			name:        "a non-empty map is sent",
			body:        map[string]any{"name": "one"},
			method:      http.MethodPost,
			wantSent:    true,
			wantPayload: `{"name":"one"}`,
		},
		{
			// THE DELIBERATE ASYMMETRY. If someone folds []any into a length check to "match" the map
			// case, this is the arm that fails -- and it is the only thing standing between a well-formed
			// no-op JSON Patch and silently not being sent.
			name:        "an EMPTY ARRAY is NOT suppressed",
			body:        []any{},
			method:      http.MethodPatch,
			wantSent:    true,
			wantPayload: `[]`,
		},
		{
			name:        "a non-empty array is sent (RFC-6902, #308)",
			body:        []any{map[string]any{"op": "replace", "path": "/instance_info", "value": 1}},
			method:      http.MethodPatch,
			wantSent:    true,
			wantPayload: `[{"op":"replace","path":"/instance_info","value":1}]`,
		},
		{
			// The default arm still rejects genuinely wrong shapes: the fix for #308 widened the accepted
			// set by exactly one type rather than removing the guard.
			name:    "an unsupported body type is refused before any request",
			body:    "a string",
			method:  http.MethodPost,
			wantErr: "invalid body type: string",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rec recorded
			srv := recordingServer(t, &rec)
			client := clientFor(t, srv.URL)

			opts := &RequestConfiguration{Method: tc.method, Body: tc.body}
			_, err := client.Call(context.Background(), srv.Client(), "/clients", opts)

			if tc.wantErr != "" {
				require.Error(t, err, "an unsupported body type must fail rather than be sent")
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Zero(t, rec.hits,
					"the request reached the server despite an unsupported body type, so the guard "+
						"rejected it only after the call had already been made")
				return
			}

			require.NoError(t, err)
			require.Equal(t, 1, rec.hits, "expected exactly one request to reach the server")

			if !tc.wantSent {
				assert.False(t, rec.present,
					"a body reached the server that should have been suppressed; the server received %q", rec.body)
				return
			}
			assert.True(t, rec.present,
				"no body reached the server, but this shape must be sent")
			assert.JSONEq(t, tc.wantPayload, string(rec.body))
		})
	}
}

// THE TWO EMPTY CASES MUST DIFFER, asserted directly rather than left as an inference from two arms of
// the table above.
//
// A table whose arms are read one at a time can be satisfied by a reader who "fixes" one of them: both
// `{}` and `[]` are empty collections, and treating them differently looks like an inconsistency until
// you know why. This test fails with a message that says why, so the next person changing that switch
// learns the reason from the failure rather than having to find the comment.
func TestTheTwoEmptyBodiesAreTreatedDifferentlyOnPurpose(t *testing.T) {
	send := func(t *testing.T, body any, method string) bool {
		t.Helper()
		var rec recorded
		srv := recordingServer(t, &rec)
		client := clientFor(t, srv.URL)
		_, err := client.Call(context.Background(), srv.Client(),
			"/clients", &RequestConfiguration{Method: method, Body: body})
		require.NoError(t, err)
		return rec.present
	}

	emptyMapSent := send(t, map[string]any{}, http.MethodPost)
	emptyArraySent := send(t, []any{}, http.MethodPatch)

	assert.False(t, emptyMapSent, "an empty map must NOT be sent: it is an assembled-but-unset body")
	assert.True(t, emptyArraySent, "an empty array MUST be sent: it is a well-formed no-op JSON Patch")
	assert.NotEqual(t, emptyMapSent, emptyArraySent,
		"the empty map and the empty array are now treated the same. They must not be: `{}` is an "+
			"assembled-but-unset body that this code declines to send, while `[]` is a valid no-op JSON "+
			"Patch that a vendor (Ironic, #308) expects to receive. Folding them together makes a "+
			"legitimate no-op indistinguishable from \"nothing to send\".")
}

// BOTH SITES MUST STAY IN STEP.
//
// The same switch exists twice -- in Call and in CallForPagination -- because they build their requests
// separately. The table above exercises Call only, so a change applied to one site and not the other
// would leave this file green while half the behaviour moved. That is the exact shape of #566, where
// dumpResponseWithPrettyJSON and httputil.DumpResponse leaked identically and fixing one would have
// looked complete.
//
// Asserted on the source, following TestTheProductionDumpTargetIsStdout in this package: a count, so
// adding a third request-building path fails here and makes someone decide deliberately.
func TestBothRequestBuildersSuppressTheEmptyMapIdentically(t *testing.T) {
	b, err := os.ReadFile("restclient.go")
	require.NoError(t, err)
	src := string(b)

	mapArm := strings.Count(src, "hasBody = len(b) != 0")
	arrayArm := strings.Count(src, "hasBody = true")
	decl := strings.Count(src, "hasBody := false")

	assert.Equal(t, 2, decl,
		"expected exactly 2 request-building paths deciding whether to send a body (Call and "+
			"CallForPagination); found %d. If a third was added, give it the same switch and update this "+
			"count; if one was removed, the table test above may now cover the only remaining path.", decl)
	assert.Equal(t, decl, mapArm,
		"%d path(s) declare hasBody but only %d suppress the empty map, so at least one path sends a "+
			"body this code is supposed to decline", decl, mapArm)
	assert.Equal(t, decl, arrayArm,
		"%d path(s) declare hasBody but only %d exempt the empty array, so at least one path suppresses "+
			"a valid no-op JSON Patch", decl, arrayArm)

	// And the default arm must survive on every path: widening the accepted set by one type is not the
	// same as removing the type check.
	invalid := regexp.MustCompile(`invalid body type: %T`).FindAllString(src, -1)
	assert.Len(t, invalid, decl,
		"expected every request-building path to keep its `invalid body type` guard; found %d for %d paths",
		len(invalid), decl)
}
