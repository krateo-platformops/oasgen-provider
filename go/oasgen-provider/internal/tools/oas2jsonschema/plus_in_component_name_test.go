package oas2jsonschema

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plusInComponentNameDoc is shaped like the Aruba CMP profile document: a request body $ref'ing a wrapper
// schema whose own property $refs a component whose NAME contains a "+".
//
// .NET-generated documents produce these names routinely -- a nested type `ApiKeyQuotasDto.QuotaValue`
// serialises as `ApiKeyQuotasDto+QuotaValue`, because "+" is how the CLR spells a nested type. The name is
// legal: OAS constrains component keys to ^[a-zA-Z0-9\.\-_]+$ for the KEY, and every real .NET emitter
// writes the "+" anyway, with the $ref either carrying it literally or percent-encoding it as %2B.
//
// Both spellings used to fail, for different reasons, and both are asserted here. The resolver unescaped
// refs with url.QueryUnescape, which decodes "+" to a SPACE: a literal "+" was decoded once and became
// " ", while "%2B" was decoded twice (the second pass is guarded on the presence of "%") and so also
// ended as " ". Either way the lookup asked for a component named with a space, which does not exist, and
// the whole document failed to build with ModelBuildError -- not a degraded schema, no CRD at all.
//
// The fix is url.PathUnescape, which leaves "+" alone and still decodes "%2B" to "+".
//
// This test asserts the nested property RESOLVES, not merely that Parse returned no error. A ref that
// silently resolved to an empty schema would satisfy "no error" while losing every field behind it, which
// is the failure this is meant to catch on the next parser bump.
const plusInComponentNameDoc = `
openapi: 3.0.0
info: {title: profile, version: "1.0"}
paths:
  /aiKeys/{id}:
    put:
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      requestBody:
        content:
          application/json:
            schema: {$ref: '#/components/schemas/AiKeyDto'}
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: {$ref: '#/components/schemas/AiKeyDto'}
components:
  schemas:
    AiKeyDto:
      type: object
      properties:
        quotas: {$ref: '%s'}
    ApiKeyQuotasDto+QuotaValue:
      type: object
      properties:
        limit: {type: integer}
`

func TestParsePlusInComponentName(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  string
	}{
		{"literal plus", "#/components/schemas/ApiKeyQuotasDto+QuotaValue"},
		{"percent-encoded plus", "#/components/schemas/ApiKeyQuotasDto%2BQuotaValue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := NewLibOASParser().Parse([]byte(fmt.Sprintf(plusInComponentNameDoc, tc.ref)))
			require.NoError(t, err, "a component name containing + must not fail the model build")
			require.NotNil(t, doc)

			item, ok := doc.FindPath("/aiKeys/{id}")
			require.True(t, ok)
			op, ok := item.GetOperations()["put"]
			require.True(t, ok)

			body := op.GetRequestBody().Content["application/json"]
			require.NotNil(t, body)

			var quotas *Schema
			for _, p := range body.Properties {
				if p.Name == "quotas" {
					quotas = p.Schema
				}
			}
			require.NotNil(t, quotas, "the property referencing the +-named component is missing")

			// The ref resolved only if the component's own fields came through.
			require.Len(t, quotas.Properties, 1, "the +-named component resolved to an empty schema")
			assert.Equal(t, "limit", quotas.Properties[0].Name)
			assert.Equal(t, []string{"integer"}, quotas.Properties[0].Schema.Type)
		})
	}
}
