//go:build unit

package oas2jsonschema

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// #463. Every arm goes through Generate() rather than calling the check directly, because the defect
// class here is a check that exists and is never reached -- the trap #224's and #444's tests both
// document. A test on the predicate alone would pass for an unwired guard.

// shadowSpec declares `owner` as an OPTIONAL query parameter on two different verbs, and a response
// carrying owner.login so a dotted status field resolves. Optional is the point: a REQUIRED parameter
// becomes a required spec property, which the apiserver guarantees, so status could never fill it.
const shadowSpec = `
openapi: 3.0.0
info: {title: t, version: "1"}
paths:
  /repos:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name: {type: string}
      responses:
        "201":
          description: created
          content:
            application/json:
              schema:
                type: object
                properties:
                  name: {type: string}
                  owner:
                    type: object
                    properties:
                      login: {type: string}
  /repos/read:
    get:
      parameters:
        - name: owner
          in: query
          schema: {type: string}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  name: {type: string}
                  owner:
                    type: object
                    properties:
                      login: {type: string}
  /repos/write:
    put:
      parameters:
        - name: owner
          in: query
          schema: {type: string}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  name: {type: string}
`

// requiredPathParamSpec is the false-positive control's document: `owner` is a PATH parameter, which OAS
// forces to be required.
const requiredPathParamSpec = `
openapi: 3.0.0
info: {title: t, version: "1"}
paths:
  /repos:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name: {type: string}
      responses:
        "201":
          description: created
          content:
            application/json:
              schema:
                type: object
                properties:
                  name: {type: string}
                  owner:
                    type: object
                    properties:
                      login: {type: string}
  /repos/{owner}/read:
    get:
      parameters:
        - name: owner
          in: path
          required: true
          schema: {type: string}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  name: {type: string}
                  owner:
                    type: object
                    properties:
                      login: {type: string}
`

func shadowWarnings(t *testing.T, spec string, rc *ResourceConfig) []string {
	t.Helper()
	doc, err := NewLibOASParser().Parse([]byte(spec))
	require.NoError(t, err)
	res, err := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), rc).Generate()
	require.NoError(t, err)
	var out []string
	for _, w := range res.GenerationWarnings {
		if sge, ok := w.(SchemaGenerationError); ok && sge.Code == CodeStatusFieldShadowsParameter {
			out = append(out, sge.Message)
		}
	}
	return out
}

func shadowGetVerb(mapped bool) Verb {
	v := Verb{Action: ActionGet, Method: "GET", Path: "/repos/read"}
	if mapped {
		v.FieldMapping = []FieldMappingEntry{{InCustomResource: "status.owner.login", InQuery: "owner"}}
	}
	return v
}

func shadowUpdateVerb(mapped bool) Verb {
	v := Verb{Action: ActionUpdate, Method: "PUT", Path: "/repos/write"}
	if mapped {
		v.FieldMapping = []FieldMappingEntry{{InCustomResource: "status.owner.login", InQuery: "owner"}}
	}
	return v
}

func shadowBaseVerbs() []Verb {
	return []Verb{{Action: ActionCreate, Method: "POST", Path: "/repos"}}
}

// A dotted status field whose first segment names an optional query parameter nothing supplies.
func TestADottedStatusFieldShadowingAnOptionalParameterWarns(t *testing.T) {
	got := shadowWarnings(t, shadowSpec, &ResourceConfig{
		Identifiers:            []string{"name"},
		AdditionalStatusFields: []string{"owner.login"},
		Verbs:                  append(shadowBaseVerbs(), shadowGetVerb(false)),
	})
	require.Len(t, got, 1, "exactly one warning, for the get verb")
	require.Contains(t, got[0], "owner.login", "the message must quote what the author wrote")
	require.Contains(t, got[0], "query", "and name the destination")
	require.Contains(t, got[0], ActionGet, "and name the verb, since the check is per-verb")
}

// The control for the exemption: a request-direction mapping on that verb overwrites the bad value after
// processFields, so there is nothing to warn about.
func TestAMappingOnTheSameVerbSuppressesTheWarning(t *testing.T) {
	got := shadowWarnings(t, shadowSpec, &ResourceConfig{
		Identifiers:            []string{"name"},
		AdditionalStatusFields: []string{"owner.login"},
		Verbs:                  append(shadowBaseVerbs(), shadowGetVerb(true)),
	})
	require.Empty(t, got, "a mapping on this verb supplies the parameter, so the collision cannot reach the wire")
}

// THE DISCRIMINATOR, and the reason this check does not use mappingSuppliedParams.
//
// That function is computed once across ALL verbs into one flat map (#457). With it, a mapping on `get`
// would exempt `update` too -- and `update` would still put the rendered map on the wire. This arm fails
// if the resource-wide set is ever substituted for the per-verb one.
func TestTheWarningIsPerVerbNotResourceWide(t *testing.T) {
	got := shadowWarnings(t, shadowSpec, &ResourceConfig{
		Identifiers:            []string{"name"},
		AdditionalStatusFields: []string{"owner.login"},
		Verbs:                  append(shadowBaseVerbs(), shadowGetVerb(true), shadowUpdateVerb(false)),
	})
	require.Len(t, got, 1, "the mapped get must be exempt and the unmapped update must not be")
	require.Contains(t, got[0], ActionUpdate, "the surviving warning must be the UPDATE one")
	require.NotContains(t, got[0], "'"+ActionGet+"'", "the mapped get must not be warned about")
}

// THE FALSE-POSITIVE CONTROL. An OAS path parameter must be required, so it becomes a required spec
// property, which the apiserver guarantees is present -- and processFields writes only where the key is
// absent, so status can never fill it. Warning here would fire on every definition with a dotted status
// field and a path parameter of the same first segment, none of which can exhibit the defect.
func TestARequiredPathParameterIsNotWarnedAbout(t *testing.T) {
	got := shadowWarnings(t, requiredPathParamSpec, &ResourceConfig{
		Identifiers:            []string{"name"},
		AdditionalStatusFields: []string{"owner.login"},
		Verbs: append(shadowBaseVerbs(), Verb{
			Action: ActionGet, Method: "GET", Path: "/repos/{owner}/read",
		}),
	})
	require.Empty(t, got, "spec wins over status, and a required spec property is a guarantee: unreachable, so silent")
}

// A single-segment status field renders a leaf, not an object, so there is no composite to send.
func TestASingleSegmentStatusFieldIsNotWarnedAbout(t *testing.T) {
	got := shadowWarnings(t, shadowSpec, &ResourceConfig{
		Identifiers:            []string{"name"},
		AdditionalStatusFields: []string{"owner"},
		Verbs:                  append(shadowBaseVerbs(), shadowGetVerb(false)),
	})
	require.Empty(t, got, "an undotted entry is a leaf; only a dotted one renders an object")
}

// The message has to be actionable by someone holding a RestDefinition, so it names the stanza the entry
// came from rather than leaving them to guess between identifiers and additionalStatusFields.
func TestTheWarningNamesTheStanzaItCameFrom(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(shadowSpec))
	require.NoError(t, err)
	res, err := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Identifiers:            []string{"name"},
		AdditionalStatusFields: []string{"owner.login"},
		Verbs:                  append(shadowBaseVerbs(), shadowGetVerb(false)),
	}).Generate()
	require.NoError(t, err)
	var found bool
	for _, w := range res.GenerationWarnings {
		if sge, ok := w.(SchemaGenerationError); ok && sge.Code == CodeStatusFieldShadowsParameter {
			found = true
			require.True(t, strings.Contains(sge.Path, "additionalStatusFields"),
				"Path = %q, which does not name the stanza the entry was declared in", sge.Path)
		}
	}
	require.True(t, found, "no warning was produced, so this assertion covered nothing")
}

// THE ARM THAT SETTLES WHY A PATH PARAMETER IS SILENT, rather than asserting that it is.
//
// Two explanations were on the table for a generated spec having no `owner` key. The
// github-provider-kog session proposed that the spec is built from the CREATE verb alone, which would
// make every path parameter on a non-create verb absent from the spec and therefore reachable. The other
// is that a MAPPED parameter is omitted from the spec entirely by mappingSuppliedParams.
//
// This asserts the generated artifact directly: `owner` is a required path parameter on `get` ONLY --
// it appears on no create operation -- and nothing maps it. If the spec is create-only it will be absent;
// if addParametersToSpec collects across all verbs it will be present AND required, which is what makes
// the collision unreachable and the silence correct.
//
// Asserting the mechanism rather than the outcome is the point. TestARequiredPathParameterIsNotWarnedAbout
// already observes the silence, and a silence is consistent with both stories.
func TestAnUnmappedPathParameterOnANonCreateVerbIsRequiredInTheGeneratedSpec(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(requiredPathParamSpec))
	require.NoError(t, err)
	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Identifiers:            []string{"name"},
		AdditionalStatusFields: []string{"owner.login"},
		Verbs: append(shadowBaseVerbs(), Verb{
			Action: ActionGet, Method: "GET", Path: "/repos/{owner}/read",
		}),
	})
	specJSON, _, err := g.BuildSpecSchema()
	require.NoError(t, err)

	var parsed struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(specJSON, &parsed))

	require.Contains(t, parsed.Properties, "owner",
		"the generated spec has no 'owner' property, so the spec IS create-only and an unmapped path "+
			"parameter on a non-create verb is reachable -- the comment in status_field_parameter_shadow.go "+
			"needs correcting again. Properties: %v", rawKeysOf(parsed.Properties))
	require.Contains(t, parsed.Required, "owner",
		"'owner' is a spec property but not required, so the apiserver does not guarantee it and the "+
			"collision IS reachable. Required: %v", parsed.Required)
}

func rawKeysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
