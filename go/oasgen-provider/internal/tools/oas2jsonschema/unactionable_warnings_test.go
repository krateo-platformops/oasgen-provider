package oas2jsonschema

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A document whose `get` returns a body and whose `update` declares 204 with no content -- GitHub's
// documented shape for several operations, and the one that produced 7 of a consumer's 12 validation
// warnings.
const bodylessUpdateSpec = `
openapi: 3.0.0
info: {title: things, version: "1.0"}
components:
  schemas:
    Thing:
      type: object
      properties:
        name: {type: string}
        size: {type: string}
      required: [name]
paths:
  /things:
    post:
      operationId: createThing
      requestBody: {content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
  /things/{name}:
    get:
      operationId: getThing
      parameters: [{name: name, in: path, required: true, schema: {type: string}}]
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
    put:
      operationId: updateThing
      parameters: [{name: name, in: path, required: true, schema: {type: string}}]
      requestBody: {content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
      responses:
        "204": {description: no content}
`

// Same document, but `update`'s success DOES carry content -- in a MIME type the generator does not
// accept. That is a different fact and must still be reported: something was returned and we could not
// read it.
const unreadableUpdateSpec = `
openapi: 3.0.0
info: {title: things, version: "1.0"}
components:
  schemas:
    Thing:
      type: object
      properties:
        name: {type: string}
        size: {type: string}
      required: [name]
paths:
  /things:
    post:
      operationId: createThing
      requestBody: {content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
  /things/{name}:
    get:
      operationId: getThing
      parameters: [{name: name, in: path, required: true, schema: {type: string}}]
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
    put:
      operationId: updateThing
      parameters: [{name: name, in: path, required: true, schema: {type: string}}]
      requestBody: {content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
      responses:
        "200": {description: ok, content: {application/xml: {schema: {$ref: '#/components/schemas/Thing'}}}}
        # A BODYLESS ERROR RESPONSE, which nearly every real document declares. It is here to make the
        # 2xx bound on the bodyless detection testable: without that bound this 404 would set the
        # bodyless flag and suppress a warning about a SUCCESS response we genuinely could not read.
        "404": {description: not found}
`

// A document whose `owner` is a PATH PARAMETER ONLY -- deliberately absent from every request body.
//
// THE FIRST VERSION OF THE #484 ARM USED A FIELD THAT WAS ALSO A CREATE-BODY PROPERTY, and it was
// vacuous: the body contributes the property, removeFieldAtPath finds it, and no warning fires whether
// the suppression is present or not. The mutation "remove the #484 suppression" SURVIVED, which is how
// that surfaced. The repo's own #455 comment names this exact trap -- "A name can be both a path
// parameter and a create-BODY property" -- and I walked into it while testing a different fix.
const pathOnlyParamSpec = `
openapi: 3.0.0
info: {title: things, version: "1.0"}
components:
  schemas:
    Thing:
      type: object
      properties:
        label: {type: string}
      required: [label]
paths:
  /owners/{owner}/things:
    post:
      operationId: createThing
      parameters: [{name: owner, in: path, required: true, schema: {type: string}}]
      requestBody: {content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
  /owners/{owner}/things/{label}:
    get:
      operationId: getThing
      parameters:
        - {name: owner, in: path, required: true, schema: {type: string}}
        - {name: label, in: path, required: true, schema: {type: string}}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
`

func validationWarningsFor(t *testing.T, spec string) []string {
	t.Helper()
	doc, err := NewLibOASParser().Parse([]byte(spec))
	require.NoError(t, err)
	verbs := []Verb{
		{Action: ActionCreate, Method: "POST", Path: "/things"},
		{Action: ActionGet, Method: "GET", Path: "/things/{name}"},
		{Action: ActionUpdate, Method: "PUT", Path: "/things/{name}"},
	}
	var out []string
	for _, w := range ValidateSchemas(NewDocumentSet(doc), verbs, DefaultGeneratorConfig()) {
		out = append(out, w.Error())
	}
	return out
}

// A DOCUMENTED 204 IS NOT AN AUTHORING ERROR, so there is nothing to compare and nothing to report.
//
// Measured on a consumer's 32 kinds: 7 of their 12 validation warnings were this shape, every one
// permanent and unactionable. The vendor documents success with no body; the author cannot add one, and
// rewriting the asset to silence the warning would make the document false.
func TestABodylessSuccessResponseIsNotReported(t *testing.T) {
	got := validationWarningsFor(t, bodylessUpdateSpec)
	for _, w := range got {
		require.NotContains(t, w, "could not extract schema for action 'update'",
			"a 204 has no schema to compare; reporting it is a permanent message with no action behind it. got: %v", got)
		require.NotContains(t, w, "no suitable response schema",
			"got: %v", got)
	}
}

// THE CONTROL THAT KEEPS THE SUPPRESSION NARROW. Content present in a MIME type we do not accept is a
// REAL diagnostic: something was returned and the generator could not use it. If this stops warning, the
// suppression has swallowed a class it was never meant to touch.
func TestUnreadableSuccessContentIsStillReported(t *testing.T) {
	got := validationWarningsFor(t, unreadableUpdateSpec)
	require.NotEmpty(t, got,
		"an update whose success carries application/xml must still be reported: the body exists and we "+
			"cannot read it, which is the opposite of a 204")
	joined := strings.Join(got, "\n")
	require.Contains(t, joined, "update",
		"and the message must name the action. got: %v", got)
}

// #484: a field that is BOTH excludedSpecFields and mapping-supplied must not warn.
func TestAnExcludedFieldThatAMappingSuppliesIsNotReported(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(pathOnlyParamSpec))
	require.NoError(t, err)
	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Identifiers:        []string{"label"},
		ExcludedSpecFields: []string{"owner"},
		Verbs: []Verb{
			{Action: ActionCreate, Method: "POST", Path: "/owners/{owner}/things",
				FieldMapping: []FieldMappingEntry{{InPath: "owner", InCustomResource: "status.owner"}}},
			{Action: ActionGet, Method: "GET", Path: "/owners/{owner}/things/{label}",
				FieldMapping: []FieldMappingEntry{{InPath: "owner", InCustomResource: "status.owner"}}},
		},
	})
	_, warnings, err := g.BuildSpecSchema()
	require.NoError(t, err)
	for _, w := range warnings {
		require.NotContains(t, w.Error(), "set in excludedSpecFields not found in schema",
			"the mapping removed the field, so the exclusion is inert BECAUSE the mapping did the job -- and "+
				"ExcludedSpecFields is immutable, so the author cannot remove the entry. got: %v", w.Error())
	}
}

// THE CONTROL. An exclusion that names nothing and is NOT mapping-supplied must still warn, or #484 has
// disabled the check rather than narrowing it.
func TestAnExcludedFieldNothingSuppliesIsStillReported(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(bodylessUpdateSpec))
	require.NoError(t, err)
	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Identifiers:        []string{"name"},
		ExcludedSpecFields: []string{"no_such_field_anywhere"},
		Verbs: []Verb{
			{Action: ActionCreate, Method: "POST", Path: "/things"},
			{Action: ActionGet, Method: "GET", Path: "/things/{name}"},
		},
	})
	_, warnings, err := g.BuildSpecSchema()
	require.NoError(t, err)
	var found bool
	for _, w := range warnings {
		if strings.Contains(w.Error(), "no_such_field_anywhere") {
			found = true
		}
	}
	require.True(t, found,
		"an exclusion naming nothing, with no mapping behind it, is a real authoring error and must still "+
			"be reported. got: %v", warnings)
}
