//go:build unit

package restclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pb33f/libopenapi"
	"github.com/stretchr/testify/require"
)

const wireDoc = `
openapi: 3.0.3
info: { title: t, version: "1.0" }
paths:
  /things:
    parameters:
      - name: api-version
        in: query
        required: true
        schema: { type: string, default: "1.1" }
      - name: X-Nova-Version
        in: header
        required: true
        schema: { type: string, default: "2.2" }
    get:
      responses: { "200": { description: ok } }
`

// TestAVendorsRequiredDefaultsREACHTHEVENDOR is the test whose absence let a real defect ship.
//
// The original tests called ApplyVendorRequiredDefaults directly and then ValidateRequest, and both
// passed. Neither went through Call, where the order turned out to be:
//
//	1. refuse if a path parameter is unresolved      <- returned first
//	2. buildPath(..., opts.Parameters, opts.Query)    <- the URI, INCLUDING the query string
//	   ...
//	3. ApplyVendorRequiredDefaults                    <- too late for both
//	4. ValidateRequest                                <- read the map and PASSED
//
// So a query default was filled after the URI was built: the request went out without it while validation
// reported it valid. That is worse than never filling it, because the loud refusal was replaced by a
// silent omission -- and no assertion in the package could see the difference, because none of them
// looked at what the server received.
//
// THIS TEST ASKS THE SERVER. That is the only channel that can distinguish "filled the map" from "sent
// the request".
func TestAVendorsRequiredDefaultsREACHTHEVENDOR(t *testing.T) {
	var gotQuery, gotHeader string
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotQuery = r.URL.Query().Get("api-version")
		gotHeader = r.Header.Get("X-Nova-Version")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	doc, err := libopenapi.NewDocument([]byte(wireDoc))
	require.NoError(t, err)
	v3Doc, errs := doc.BuildV3Model()
	require.Empty(t, errs)

	u := &UnstructuredClient{
		Server:     srv.URL,
		HostPolicy: allowedPolicy(t, srv.URL),
		DocScheme:  v3Doc,
	}

	_, err = u.Call(context.Background(), srv.Client(), "/things", &RequestConfiguration{
		Method:     "GET",
		Parameters: map[string]string{},
		Query:      map[string]string{},
		Headers:    map[string]string{},
		Cookies:    map[string]string{},
	})
	require.NoError(t, err)

	// SETUP CONTROL: the request actually reached the server. Without it, two empty strings below would
	// read as "the values were not sent" when the cause was that nothing was sent at all.
	require.Equal(t, 1, hits, "the server was never called, so the assertions below measure nothing")

	require.Equal(t, "2.2", gotHeader,
		"the vendor's required header default did not reach the vendor")
	require.Equal(t, "1.1", gotQuery,
		"the vendor's required QUERY default did not reach the vendor -- this is the arm that was filled "+
			"into the map AFTER buildPath had already constructed the URI, so validation passed on a "+
			"request that did not carry it")
}

// TestAnExplicitValueStillWinsOnTheWire pins the precedence through the real path too, because a
// precedence that holds in the map and not on the wire is the same class of defect.
func TestAnExplicitValueStillWinsOnTheWire(t *testing.T) {
	var gotQuery string
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotQuery = r.URL.Query().Get("api-version")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	doc, err := libopenapi.NewDocument([]byte(wireDoc))
	require.NoError(t, err)
	v3Doc, errs := doc.BuildV3Model()
	require.Empty(t, errs)

	u := &UnstructuredClient{Server: srv.URL, HostPolicy: allowedPolicy(t, srv.URL), DocScheme: v3Doc}
	_, err = u.Call(context.Background(), srv.Client(), "/things", &RequestConfiguration{
		Method:     "GET",
		Parameters: map[string]string{},
		Query:      map[string]string{"api-version": "1.0"},
		Headers:    map[string]string{"X-Nova-Version": "2.1"},
		Cookies:    map[string]string{},
	})
	require.NoError(t, err)
	require.Equal(t, 1, hits, "the server was never called")
	require.Equal(t, "1.0", gotQuery,
		"the document overwrote a value the author supplied, which would silently change a deliberate "+
			"choice -- and on the wire, where it is what the vendor acts on")
}
