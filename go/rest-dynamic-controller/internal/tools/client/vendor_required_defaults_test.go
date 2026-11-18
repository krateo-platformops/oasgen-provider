//go:build unit

package restclient

import (
	"testing"

	"github.com/pb33f/libopenapi"
	"github.com/stretchr/testify/require"
)

const keypairDoc = `
openapi: 3.0.3
info: { title: t, version: "1.0" }
paths:
  /os-keypairs:
    parameters:
      - name: X-Nova-Version
        in: header
        required: true
        schema: { type: string, default: "2.2" }
      - name: optional-with-default
        in: header
        schema: { type: string, default: "should-not-be-sent" }
      - name: required-no-default
        in: header
        required: true
        schema: { type: string }
      - name: structured
        in: header
        required: true
        schema:
          type: object
          default: { a: 1 }
      - name: explicit-null
        in: header
        required: true
        schema: { type: string, default: null }
    post:
      responses: { "200": { description: ok } }
  /os-keypairs/{name}:
    parameters:
      - name: name
        in: path
        required: true
        schema: { type: string, default: "from-the-document" }
      - name: page
        in: query
        required: true
        schema: { type: string, default: "1" }
    get:
      responses: { "200": { description: ok } }
`

func clientForDocument(t *testing.T, doc string) *UnstructuredClient {
	t.Helper()
	d, err := libopenapi.NewDocument([]byte(doc))
	require.NoError(t, err)
	m, errs := d.BuildV3Model()
	require.Empty(t, errs)
	return &UnstructuredClient{DocScheme: m}
}

// TestAVendorsRequiredDefaultIsSent is the behaviour Diego's principle asks for: the document is the
// authority, so a value the vendor declared is sent rather than withheld.
//
// A `default` on a request parameter is usually read as "what the server assumes when the value is
// absent" -- but that reading is INCOHERENT with `required: true`, because a required parameter is never
// absent. The only coherent reading of the pair is the vendor saying: this must be present, and here is
// the value. Before this change the same document produced "missing header: X-Nova-Version" on EVERY call.
func TestAVendorsRequiredDefaultIsSent(t *testing.T) {
	u := clientForDocument(t, keypairDoc)
	headers := map[string]string{}
	applied := u.ApplyVendorRequiredDefaults("post", "/os-keypairs",
		map[string]string{}, map[string]string{}, headers, map[string]string{})

	require.Equal(t, "2.2", headers["X-Nova-Version"],
		"the vendor declared this required WITH a value and it was not sent")
	require.Len(t, applied, 1, "exactly one parameter qualifies in this document: %v", applied)
	require.Equal(t, AppliedDefault{Name: "X-Nova-Version", In: "header", Value: "2.2"}, applied[0],
		"the report must name what was sent, or an operator finds it in a packet capture")

	// EVERY OTHER PARAMETER IN THE SAME DOCUMENT MUST BE UNTOUCHED, and each for its own reason. Without
	// these the assertion above passes for an implementation that fills everything it can reach.
	require.NotContains(t, headers, "optional-with-default",
		"OPTIONAL: here the usual reading holds -- the server really does supply the value -- so sending "+
			"it would assert a choice the author never made")
	require.NotContains(t, headers, "required-no-default",
		"no default exists, so there is nothing of the vendor's to reflect; the existing missing-header "+
			"error is the honest outcome")
	require.NotContains(t, headers, "structured",
		"an object default cannot go in a header without inventing a serialisation the vendor never "+
			"specified -- and inventing one would be the 0.53.0 mistake for real")
	require.NotContains(t, headers, "explicit-null",
		"an explicit null is a declaration that there is NO value, not a value of \"null\"")
}

// TestAnExplicitValueBeatsTheDocument pins the precedence the whole feature depends on: the author's own
// declaration always wins, so this only ever fills an absence.
func TestAnExplicitValueBeatsTheDocument(t *testing.T) {
	u := clientForDocument(t, keypairDoc)
	headers := map[string]string{"X-Nova-Version": "2.72"}
	applied := u.ApplyVendorRequiredDefaults("post", "/os-keypairs",
		map[string]string{}, map[string]string{}, headers, map[string]string{})

	require.Equal(t, "2.72", headers["X-Nova-Version"],
		"the document overwrote a value the author supplied through verbsDescription[].headers, a "+
			"fieldMapping or a Configuration CR -- which would silently downgrade a deliberate choice")
	require.Empty(t, applied, "nothing was filled, so nothing may be reported as filled")
}

// TestAPathParameterIsNEVERFilledFromTheDocument is the one place this feature does not follow the
// document, and the reason is measured rather than theoretical.
//
// A path parameter decides WHICH RESOURCE is addressed, so a value nobody supplied is not a detail of the
// request -- it is a different request. Two of one vendor's documents declare
//
//	projectId   required: true   default: '*'
//
// so filling it would address /projects/*/providers/... -- a read across every project the credential can
// see -- and the same default sits on an alertRules POST and DELETE. The consumer who found it is safe
// only because their own CRs supply projectId, which is a property of their corpus and not of this code.
func TestAPathParameterIsNEVERFilledFromTheDocument(t *testing.T) {
	u := clientForDocument(t, keypairDoc)
	params, query := map[string]string{}, map[string]string{}
	applied := u.ApplyVendorRequiredDefaults("get", "/os-keypairs/{name}",
		params, query, map[string]string{}, map[string]string{})

	// SETUP CONTROL: the same call DOES fill the query parameter on this path, so an empty `params`
	// below means "path is excluded" rather than "the call did nothing".
	require.Equal(t, "1", query["page"],
		"the query default was not applied, so the path assertion below proves nothing")

	require.Empty(t, params,
		"a path parameter was filled from the document: the vendor default for `name` here is "+
			"\"from-the-document\", and in a real corpus it is '*'")
	require.Len(t, applied, 1, "only the query fill may be reported: %v", applied)
	require.Equal(t, "query", applied[0].In)
}

// TestTheRequestIsValidAfterwards is the end-to-end point: the defaults are applied so that the
// validator, which is what produced "missing header", now passes on the same document.
//
// SETUP CONTROL FIRST: the validator must FAIL before the defaults are applied, or this proves nothing
// about the defaults.
func TestTheRequestIsValidAfterwards(t *testing.T) {
	u := clientForDocument(t, `
openapi: 3.0.3
info: { title: t, version: "1.0" }
paths:
  /things:
    parameters:
      - name: X-Nova-Version
        in: header
        required: true
        schema: { type: string, default: "2.2" }
    post:
      responses: { "200": { description: ok } }
`)
	before := u.ValidateRequest("post", "/things",
		map[string]string{}, map[string]string{}, map[string]string{}, map[string]string{})
	require.Error(t, before, "SETUP CONTROL: the validator must reject this before the defaults are "+
		"applied, or the assertion below is vacuous")
	require.Contains(t, before.Error(), "missing header: X-Nova-Version")

	headers := map[string]string{}
	u.ApplyVendorRequiredDefaults("post", "/things",
		map[string]string{}, map[string]string{}, headers, map[string]string{})
	require.NoError(t, u.ValidateRequest("post", "/things",
		map[string]string{}, map[string]string{}, headers, map[string]string{}),
		"the vendor's own document supplies this value, so the request it describes must be valid")
}
