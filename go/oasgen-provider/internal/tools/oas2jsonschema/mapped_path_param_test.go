package oas2jsonschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mappedParamDoc has a create at a collection path and a get/delete at an item path, which is the ordinary
// shape for a resource whose identifier the vendor mints: POST /flavors returns the id, and every later
// call addresses /flavors/{id}.
const mappedParamDoc = `
openapi: 3.0.0
info: {title: t, version: "1.0"}
paths:
  /flavors:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [flavor]
              properties:
                flavor: {type: object, properties: {name: {type: string}}}
      responses:
        '201': {description: ok, content: {application/json: {schema: {type: object, properties: {id: {type: string}}}}}}
  /flavors/{id}:
    get:
      parameters: [{name: id, in: path, required: true, schema: {type: string}}]
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: object, properties: {id: {type: string}}}}}}
`

// specFor returns the generated spec's required list and property names for the given verbs and identifiers.
func specFor(t *testing.T, doc OASDocument, identifiers []string, verbs []Verb) ([]string, map[string]bool) {
	t.Helper()
	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{Verbs: verbs, Identifiers: identifiers})
	base, _, err := g.BuildSpecSchema()
	require.NoError(t, err)

	var parsed struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(base, &parsed))
	props := map[string]bool{}
	for k := range parsed.Properties {
		props[k] = true
	}
	return parsed.Required, props
}

var flavorCreate = Verb{Action: ActionCreate, Method: "POST", Path: "/flavors"}

func flavorGetWith(entries ...FieldMappingEntry) Verb {
	return Verb{Action: ActionGet, Method: "GET", Path: "/flavors/{id}", FieldMapping: entries}
}

// TestMappedPathParamIsNotDemandedOfTheAuthor covers #325.
//
// Before this, every path parameter became a required top-level spec field named after the PARAMETER,
// regardless of what the mapping said. For a parameter mapped from status.* that is not redundant but
// IMPOSSIBLE: the vendor assigns the id at create and the controller reads it back, so the author cannot
// supply a value that does not exist yet, and `spec.id: Required value` blocks the create path entirely.
// Measured on five generated CRDs, five for five.
func TestMappedPathParamIsNotDemandedOfTheAuthor(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(mappedParamDoc))
	require.NoError(t, err)

	t.Run("mapped from status: absent from spec entirely", func(t *testing.T) {
		req, props := specFor(t, doc, nil, []Verb{
			flavorCreate,
			flavorGetWith(FieldMappingEntry{InPath: "id", InCustomResource: "status.flavor.id"}),
		})
		assert.NotContains(t, req, "id",
			"a parameter the controller supplies from status cannot be required: the value does not exist until after create")
		assert.False(t, props["id"],
			"and it must not be present as an OPTIONAL field either -- nothing reads it, so an author who sets it is silently ignored")
		assert.Contains(t, req, "flavor", "an unrelated required body field must stay required")
	})

	t.Run("mapped from spec at another path: also absent", func(t *testing.T) {
		// The keypair shape: inCustomResource points INTO spec, so the value is author-supplied -- just not
		// at the flat name. The flat duplicate is redundant rather than unsatisfiable, but still wrong.
		req, props := specFor(t, doc, nil, []Verb{
			flavorCreate,
			flavorGetWith(FieldMappingEntry{InPath: "id", InCustomResource: "spec.flavor.id"}),
		})
		assert.NotContains(t, req, "id")
		assert.False(t, props["id"], "the author already supplies this at spec.flavor.id; a second flat field is a duplicate")
	})

	t.Run("NOT mapped: the parameter stays required and present", func(t *testing.T) {
		// The control. If this ever fails, the change has unrequired every path parameter rather than the
		// mapped ones, and a kind that genuinely needs an author-supplied id can no longer ask for it.
		req, props := specFor(t, doc, nil, []Verb{flavorCreate, flavorGetWith()})
		assert.Contains(t, req, "id",
			"a path parameter with no mapping IS the author's to provide, and must remain required")
		assert.True(t, props["id"])
	})

	t.Run("a RESPONSE-direction mapping does not suppress", func(t *testing.T) {
		// inResponse -> inCustomResource populates status. It says nothing about where a request parameter
		// comes from, so it must not be mistaken for a request-direction entry.
		req, props := specFor(t, doc, nil, []Verb{
			flavorCreate,
			flavorGetWith(FieldMappingEntry{InResponse: "id", InCustomResource: "status.flavor.id"}),
		})
		assert.Contains(t, req, "id",
			"a response mapping supplies STATUS, not the request parameter; suppressing on it would unrequire a field nothing provides")
		assert.True(t, props["id"])
	})

	t.Run("an entry with no inCustomResource does not suppress", func(t *testing.T) {
		req, _ := specFor(t, doc, nil, []Verb{
			flavorCreate,
			flavorGetWith(FieldMappingEntry{InPath: "id"}),
		})
		assert.Contains(t, req, "id",
			"an entry naming a destination but no source supplies nothing")
	})

	// The identifier interaction, which is the one thing that could have made omitting the property unsafe.
	//
	// I expected addIdentifiersToSpec to re-add the field and make omission safe that way. It does -- but
	// only behind `IncludeIdentifiersInSpec`, which DefaultGeneratorConfig sets false and which NOTHING in
	// the generation path ever sets true. So in production an identifier never reaches the spec at all, and
	// the real reason omission is safe is simpler than the one I assumed. Both states are pinned: the first
	// is what ships, the second is the guarantee for whoever turns the flag on.
	t.Run("identifiers off (the shipped config): no spec field, and that is correct", func(t *testing.T) {
		req, props := specFor(t, doc, []string{"id"}, []Verb{
			flavorCreate,
			flavorGetWith(FieldMappingEntry{InPath: "id", InCustomResource: "status.flavor.id"}),
		})
		assert.False(t, props["id"],
			"with IncludeIdentifiersInSpec false -- the only configuration the generator ships -- declaring id as an identifier adds nothing to spec")
		assert.NotContains(t, req, "id")
	})

	t.Run("identifiers ON: the field is re-added, and NOT required", func(t *testing.T) {
		// addIdentifiersToSpec runs AFTER addParametersToSpec and appends any identifier it does not find,
		// as an optional string. If that ordering is ever inverted, a mapped parameter that is also an
		// identifier would silently lose its selector and findby could never match.
		cfg := DefaultGeneratorConfig()
		cfg.IncludeIdentifiersInSpec = true
		g := NewOASSchemaGenerator(doc, cfg, &ResourceConfig{
			Identifiers: []string{"id"},
			Verbs: []Verb{
				flavorCreate,
				flavorGetWith(FieldMappingEntry{InPath: "id", InCustomResource: "status.flavor.id"}),
			},
		})
		base, _, err := g.BuildSpecSchema()
		require.NoError(t, err)

		var parsed struct {
			Required   []string                  `json:"required"`
			Properties map[string]map[string]any `json:"properties"`
		}
		require.NoError(t, json.Unmarshal(base, &parsed))

		_, present := parsed.Properties["id"]
		assert.True(t, present, "an identifier must be re-added when the flag is on, or findby loses its selector")
		assert.NotContains(t, parsed.Required, "id", "re-adding it as an identifier must not make it required again")
	})
}
