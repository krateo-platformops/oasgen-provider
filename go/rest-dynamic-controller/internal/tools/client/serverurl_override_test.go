//go:build unit || integration

package restclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/hostpolicy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// spec.serverURL replaces the document's servers[0] for every request a resource makes (#374). It exists
// for a self-hosted instance of a service whose published document names the vendor's own endpoint --
// GitHub Enterprise Server serving the same API at https://<host>/api/v3.
//
// The override is applied by mutating the parsed model once, before anything reads it, so the Server
// field, DocumentHosts (the host policy's document half) and Call/CallForPagination's per-operation
// branch all see one value. These tests pin the three consequences that a later refactor could
// individually undo.

const serverURLTestDoc = `openapi: 3.0.0
info:
  title: widgets
  version: "1.0"
servers:
  - url: https://api.github.com
paths:
  /repos/{org}:
    get:
      operationId: getRepo
      parameters:
        - name: org
          in: path
          required: true
          schema:
            type: string
      responses:
        "200":
          description: ok
`

// The same document, but the operation declares its own servers -- a SECOND base URL.
const serverURLTestDocPerOperation = `openapi: 3.0.0
info:
  title: widgets
  version: "1.0"
servers:
  - url: https://api.github.com
paths:
  /repos/{org}:
    get:
      operationId: getRepo
      servers:
        - url: https://uploads.github.com
      parameters:
        - name: org
          in: path
          required: true
          schema:
            type: string
      responses:
        "200":
          description: ok
`

// buildWithServerURL serves doc over httptest and calls BuildClient with the given override.
func buildWithServerURL(t *testing.T, doc, serverURL string, cfg hostpolicy.Config) (*UnstructuredClient, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write([]byte(doc))
	}))
	t.Cleanup(srv.Close)

	kube := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	return BuildClient(context.Background(), kube, srv.URL+"/oas.yaml", serverURL, cfg)
}

func TestServerURLReplacesTheDocumentsServerIncludingItsPath(t *testing.T) {
	u, err := buildWithServerURL(t, serverURLTestDoc, "https://ghe.corp.example.com/api/v3", hostpolicy.Config{})
	require.NoError(t, err)

	// The PATH must survive. GHES serves its REST API under /api/v3, so an override that kept only the
	// host would 404 every request -- and it would look like a working configuration.
	assert.Equal(t, "https://ghe.corp.example.com/api/v3", u.Server,
		"spec.serverURL must replace servers[0] WHOLLY, path segment included")
}

func TestServerURLToleratesATrailingSlash(t *testing.T) {
	u, err := buildWithServerURL(t, serverURLTestDoc, "https://ghe.corp.example.com/api/v3/", hostpolicy.Config{})
	require.NoError(t, err)
	assert.Equal(t, "https://ghe.corp.example.com/api/v3/", u.Server)

	// buildPath's Clean collapses the double slash, so the joined URI is the same either way. Asserted
	// on the joined result rather than on the stored field, because that is what reaches the wire.
	uri := buildPath(u.Server, "/repos/{org}", map[string]string{"org": "krateo"}, nil)
	require.NotNil(t, uri)
	assert.Equal(t, "https://ghe.corp.example.com/api/v3/repos/krateo", uri.String(),
		"a trailing slash on spec.serverURL must not produce a double slash on the wire")
}

// THE SECURITY PROPERTY, and the reason the override is applied before the policy is built rather than
// at the Server field. The host policy's document half comes from DocumentHosts(doc), so replacing
// servers[0] first means the vendor's PUBLIC host is not in the permitted set at all: a self-hosted
// install pointed at the wrong place fails closed instead of reaching github.com with the credential
// meant for the private appliance.
func TestServerURLRemovesTheVendorsPublicHostFromThePolicy(t *testing.T) {
	public, err := url.Parse("https://api.github.com/repos/krateo")
	require.NoError(t, err)
	private, err := url.Parse("https://ghe.corp.example.com/api/v3/repos/krateo")
	require.NoError(t, err)

	// CONTROL FIRST. Without the override the public host IS permitted -- so if this arm ever stops
	// discriminating because the policy refuses everything, that shows up here rather than being
	// mistaken for the fix working.
	t.Run("control: without an override the document's own host is permitted", func(t *testing.T) {
		u, err := buildWithServerURL(t, serverURLTestDoc, "", hostpolicy.Config{})
		require.NoError(t, err)
		require.NotNil(t, u.HostPolicy)
		assert.NoError(t, u.HostPolicy.PermitsURL(public),
			"premise: the document declares api.github.com, so it must be permitted when nothing overrides it")
	})

	t.Run("with an override the public host is refused and the private one permitted", func(t *testing.T) {
		u, err := buildWithServerURL(t, serverURLTestDoc, "https://ghe.corp.example.com/api/v3", hostpolicy.Config{})
		require.NoError(t, err)
		require.NotNil(t, u.HostPolicy)

		assert.Error(t, u.HostPolicy.PermitsURL(public),
			"api.github.com must NOT be permitted once spec.serverURL replaces servers[0]: a misconfigured "+
				"self-hosted install has to fail closed, not reach the public service with the private credential")
		assert.NoError(t, u.HostPolicy.PermitsURL(private),
			"the overridden host must be permitted, or the override makes every request fail")
	})
}

// #402 made an OPERATOR allowlist entry carrying a path a refusal, because a path plays no part in
// matching a host and silently discarding it is how `example.com/*` became an exact entry. A DOCUMENT
// server URL legitimately carries one -- GHES's /api/v3 -- and spec.serverURL is a document server URL
// even though an operator wrote it.
//
// So this pins the origin rather than the author: parsed as originDocument the path is discarded for
// matching and accepted; parsed as originOperator it would be REFUSED, and the refusal would tell the
// author to write the bare host, which breaks GHES. Who typed the value is irrelevant; what the value
// IS decides the origin.
func TestAPathCarryingServerURLIsNotRefusedAsAnOperatorEntry(t *testing.T) {
	u, err := buildWithServerURL(t, serverURLTestDoc, "https://ghe.corp.example.com/api/v3", hostpolicy.Config{})
	require.NoError(t, err,
		"a spec.serverURL carrying a path must be accepted: it is a document server URL, so #402's "+
			"operator-entry path refusal must not reach it")

	private, perr := url.Parse("https://ghe.corp.example.com/api/v3/repos/krateo")
	require.NoError(t, perr)
	assert.NoError(t, u.HostPolicy.PermitsURL(private))

	// And the discriminating half: the same string as an OPERATOR entry is still refused, so the test
	// cannot pass because the path rule was removed outright.
	_, _, operr := hostpolicy.Parse([]string{"ghe.corp.example.com/api/v3"}, nil, false)
	require.Error(t, operr,
		"premise: #402's refusal of an operator entry carrying a path must still be in force, or this "+
			"test proves nothing about origins")
}

// REFUSED rather than guessed. A per-operation servers[0] is a second base URL, and with an override
// there are two defensible rules -- replace it wholesale, or replace its host and keep its own path --
// with nothing to choose between them: consumers measured ZERO per-operation overrides across 59
// documents, so either would ship unexercised. Silently picking one sends requests to a host the
// operator did not name, which is what spec.serverURL exists to prevent.
func TestServerURLIsRefusedWhenAnOperationDeclaresItsOwnServers(t *testing.T) {
	_, err := buildWithServerURL(t, serverURLTestDocPerOperation, "https://ghe.corp.example.com/api/v3", hostpolicy.Config{})
	require.Error(t, err, "a document with a per-operation servers[0] and a spec.serverURL is ambiguous and must be refused")

	assert.Contains(t, err.Error(), "serverURL",
		"the error must name the field, so the operator knows which of the two to remove")
	assert.Contains(t, err.Error(), "/repos/{org}",
		"the error must name the OPERATION, so the operator can go and look at it rather than search the document")

	// The control: the same document WITHOUT an override must still build. The per-operation server is
	// legal OpenAPI and supported -- it is only its combination with an override that has no answer.
	t.Run("control: the same document builds when nothing overrides it", func(t *testing.T) {
		u, cerr := buildWithServerURL(t, serverURLTestDocPerOperation, "", hostpolicy.Config{})
		require.NoError(t, cerr,
			"a per-operation servers[0] is legal and supported; refusing it unconditionally would break "+
				"documents that work today")
		assert.Equal(t, "https://api.github.com", u.Server)
	})
}

func TestWithoutServerURLTheDocumentsOwnServerStands(t *testing.T) {
	u, err := buildWithServerURL(t, serverURLTestDoc, "", hostpolicy.Config{})
	require.NoError(t, err)
	assert.Equal(t, "https://api.github.com", u.Server,
		"an empty spec.serverURL must leave the document untouched, or every existing install changes behaviour")
}

// A guard on the mechanism rather than on an outcome: the override is applied by MUTATING the parsed
// model, which is what makes Server, DocumentHosts and the per-operation branch agree without a second
// value to keep in step. If a refactor ever carries the override alongside the model instead, Server
// would still be right while the POLICY silently kept the vendor's host -- the strictly worse failure,
// because it reads as working.
func TestTheOverrideReachesThePolicyAndNotOnlyTheServerField(t *testing.T) {
	u, err := buildWithServerURL(t, serverURLTestDoc, "https://ghe.corp.example.com/api/v3", hostpolicy.Config{})
	require.NoError(t, err)

	assert.Equal(t, "https://ghe.corp.example.com/api/v3", u.Server)

	public, perr := url.Parse("https://api.github.com/repos/krateo")
	require.NoError(t, perr)
	err = u.HostPolicy.PermitsURL(public)
	require.Error(t, err, "Server was overridden but the policy still permits the vendor's public host: "+
		"the override was applied AFTER DocumentHosts read the document, so the guard is weaker than it looks")
	assert.True(t, strings.Contains(err.Error(), "api.github.com") || strings.Contains(err.Error(), "not permitted"),
		"the refusal should name the host or say it is not permitted, got: %v", err)
}
