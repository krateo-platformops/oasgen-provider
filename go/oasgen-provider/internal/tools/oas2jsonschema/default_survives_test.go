package oas2jsonschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A CONSUMER DEPENDENCY NOTHING ASSERTED, added at their request rather than because anything is broken.
//
// A consumer declares a header parameter `required: true` WITH a `default`. The generated CRD therefore
// carries it as a required property that has a default, so the apiserver supplies the value and no caller
// can omit it. That is why their charts needed no edit: every CR they render sets nothing and admission
// fills it in.
//
// Nothing in this repo asserted that. If a regeneration ever dropped defaults, their charts would keep
// rendering CRs that set nothing and EVERY ONE would start failing admission for a missing required
// property -- silently at render time, loudly at apply, and with the cause three layers away.
//
// Same shape as the unselectored MutatingAdmissionPolicy: a property someone depends on that nothing
// records. The treatment is the same, and the reasoning for a test rather than an issue is theirs:
// nothing is wrong today, the gap is that nothing would notice if it became wrong.
//
// THE required + default COMBINATION IS THE POINT, not `default` alone. `required` is enforced AFTER
// defaulting, so the default satisfies the requirement and the field can never be absent -- which is the
// property they want, and which also means the CRD can no longer distinguish "the operator chose this
// value" from "nobody said anything". For a microversion header that is the right trade; it is recorded
// here because it is obvious when chosen and invisible a year later.
const defaultOnRequiredParamSpec = `
openapi: 3.0.3
info: {title: ironic, version: "1.0"}
paths:
  /v1/nodes/{uuid}/states/provision:
    put:
      parameters:
        - name: uuid
          in: path
          required: true
          schema: {type: string}
        - name: X-OpenStack-Ironic-API-Version
          in: header
          required: true
          schema: {type: string, default: "1.109"}
        - name: optional-with-default
          in: header
          required: false
          schema: {type: string, default: "fallback"}
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [target]
              properties:
                target: {type: string}
      responses:
        '202': {description: accepted}
`

func TestADefaultOnARequiredParameterSurvivesIntoTheSpec(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(defaultOnRequiredParamSpec))
	require.NoError(t, err)
	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Verbs:       []Verb{{Action: "create", Method: "put", Path: "/v1/nodes/{uuid}/states/provision"}},
		Identifiers: []string{"uuid"},
	})
	raw, _, err := g.BuildSpecSchema()
	require.NoError(t, err)

	var spec map[string]any
	require.NoError(t, json.Unmarshal(raw, &spec))
	props, _ := spec["properties"].(map[string]any)

	hdr, ok := props["X-OpenStack-Ironic-API-Version"].(map[string]any)
	require.True(t, ok, "premise: the header parameter must reach the spec at all, or this test is vacuous")

	// THE DEPENDENCY: the default must be present, so the apiserver supplies it.
	assert.Equal(t, "1.109", hdr["default"],
		"a required property whose default is dropped makes every CR that omits the field fail admission; "+
			"the consumer's charts render exactly such CRs and needed no edit BECAUSE the default arrives")

	// And it must still be required -- a default on an OPTIONAL property is a weaker guarantee, because a
	// caller can then set it to something wrong rather than being unable to omit it.
	req, _ := spec["required"].([]any)
	assert.Contains(t, req, "X-OpenStack-Ironic-API-Version",
		"required AND defaulted is the combination depended on: required alone can be omitted at render "+
			"time, defaulted alone can be set wrongly")

	// The CONTROL, so this cannot pass merely because every property gets a default: an optional parameter
	// keeps its default too, and the path parameter -- which declares none -- must not acquire one.
	opt, ok := props["optional-with-default"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "fallback", opt["default"], "an optional parameter's default survives as well")
	assert.NotContains(t, req, "optional-with-default", "and it does not become required")

	uuid, ok := props["uuid"].(map[string]any)
	require.True(t, ok, "premise: the path parameter must be in the spec")
	_, hasDefault := uuid["default"]
	assert.False(t, hasDefault,
		"a parameter declaring no default must not acquire one -- without this arm the test would pass "+
			"against a generator that stamped a default onto everything")
}
