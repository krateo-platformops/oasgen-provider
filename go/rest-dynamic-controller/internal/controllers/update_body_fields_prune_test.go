package restResources

import (
	"context"
	"testing"

	"github.com/pb33f/libopenapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

// ISSUE 960 REGRESSION GUARD, and the one that matters for issue 400.
//
// The recursive prune of the update body is a SEPARATE structure on CallInfo (BodyShape). updateBodyFields
// returns callInfo.ReqParams.Body to the unmodelled-key detection, which compares TOP-LEVEL keys against the
// vendor's top-level response keys. If the prune had reshaped that set -- recursed it, or dropped a key --
// the detection would change with it. Reasoning that it does not is not the test: this runs the real
// function against a real document whose update schema has nested structure the prune acts on, and pins the
// exact set.
func TestUpdateBodyFieldsIsUnchangedByTheBodyPrune(t *testing.T) {
	const spec = `
openapi: 3.0.3
info: {title: nets, version: "1.0"}
paths:
  /nets:
    put:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                name: {type: string}
                provider:
                  type: object
                  required: [network_type]
                  properties:
                    network_type: {type: string}
                config: {type: object, additionalProperties: true}
      responses: {"200": {description: ok}}
`
	doc, err := libopenapi.NewDocument([]byte(spec))
	require.NoError(t, err)
	model, errs := doc.BuildV3Model()
	require.Empty(t, errs)

	info := &getter.Info{URL: "doc", Resource: getter.Resource{VerbsDescription: []getter.VerbsDescription{
		{Action: "update", Method: "PUT", Path: "/nets"},
	}}}
	cs := newClientSetWithClients(info, map[string]restclient.UnstructuredClientInterface{
		"doc": &restclient.UnstructuredClient{DocScheme: model},
	})

	got, err := (&handler{}).updateBodyFields(context.Background(), cs, info)
	require.NoError(t, err)

	assert.Equal(t, bodySet("name", "provider", "config"), got,
		"updateBodyFields must still return exactly the flat TOP-LEVEL key set")
	assert.NotContains(t, got, "network_type", "a nested key must never appear in the detection's set")
}
