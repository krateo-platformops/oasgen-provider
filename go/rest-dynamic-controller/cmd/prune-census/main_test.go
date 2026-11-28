//go:build unit || integration

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The issue 960 shape: the create schema declares three colon-named keys under `network` and the update
// schema does not.
const networksDoc = `
openapi: 3.0.3
info: {title: nets, version: "1.0"}
paths:
  /networks:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                network:
                  type: object
                  properties:
                    name: {type: string}
                    description: {type: string}
                    "provider:network_type": {type: string}
                    "provider:physical_network": {type: string}
                    "provider:segmentation_id": {type: integer}
                    "router:external": {type: boolean}
      responses: {"201": {description: ok}}
    put:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                network:
                  type: object
                  properties:
                    name: {type: string}
                    description: {type: string}
                    "router:external": {type: boolean}
      responses: {"200": {description: ok}}
`

// Negative control: the update schema declares everything the create schema does.
const symmetricDoc = `
openapi: 3.0.3
info: {title: sym, version: "1.0"}
paths:
  /things:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                thing:
                  type: object
                  properties:
                    name: {type: string}
                    "provider:network_type": {type: string}
      responses: {"201": {description: ok}}
    put:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                thing:
                  type: object
                  properties:
                    name: {type: string}
                    "provider:network_type": {type: string}
      responses: {"200": {description: ok}}
`

// An update operation with no create operation anywhere to compare it with.
const noCreateDoc = `
openapi: 3.0.3
info: {title: nocreate, version: "1.0"}
paths:
  /lonely:
    put:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                x:
                  type: object
                  properties:
                    name: {type: string}
      responses: {"200": {description: ok}}
`

// Deep nesting (4 levels), an array of objects, a free-form object, and the update living on the {id}
// path with the create on the collection path.
const deepDoc = `
openapi: 3.0.3
info: {title: deep, version: "1.0"}
paths:
  /widgets:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                a:
                  type: object
                  properties:
                    b:
                      type: object
                      properties:
                        c:
                          type: object
                          properties:
                            keep: {type: string}
                            drop: {type: string}
                rules:
                  type: array
                  items:
                    type: object
                    properties:
                      id: {type: string}
                      extra: {type: string}
                free:
                  type: object
                  additionalProperties: true
      responses: {"201": {description: ok}}
  /widgets/{id}:
    patch:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                a:
                  type: object
                  properties:
                    b:
                      type: object
                      properties:
                        c:
                          type: object
                          properties:
                            keep: {type: string}
                rules:
                  type: array
                  items:
                    type: object
                    properties:
                      id: {type: string}
                free:
                  type: object
                  properties:
                    other: {type: string}
      responses: {"200": {description: ok}}
`

// A CRD spec schema WIDER than the create body: project_id is a parameter-derived key the create body
// does not declare and the update schema lacks.
const networksCRD = `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: networks.example.io}
spec:
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                network:
                  type: object
                  properties:
                    name: {type: string}
                    description: {type: string}
                    "provider:network_type": {type: string}
                    "provider:physical_network": {type: string}
                    "provider:segmentation_id": {type: integer}
                    "router:external": {type: boolean}
                    project_id: {type: string}
`

func census(t *testing.T, doc string, crd string, method, path string) *Report {
	t.Helper()
	var c []byte
	if crd != "" {
		c = []byte(crd)
	}
	rep, err := Census([]byte(doc), c, "", method, path)
	require.NoError(t, err)
	return rep
}

func only(t *testing.T, r *Report, method, path string) Operation {
	t.Helper()
	for _, o := range r.Operations {
		if o.Method == method && o.Path == path {
			return o
		}
	}
	t.Fatalf("operation %s %s not in report", method, path)
	return Operation{}
}

func TestCensusPositiveControlListsTheThreeColonNamedKeys(t *testing.T) {
	r := census(t, networksDoc, "", "", "")
	o := only(t, r, "PUT", "/networks")
	require.True(t, o.Inspected)
	assert.Equal(t, []string{
		"network.provider:network_type",
		"network.provider:physical_network",
		"network.provider:segmentation_id",
	}, o.Pruned)
	assert.Equal(t, 1, r.WithPrunable)
	assert.Equal(t, 1, r.Inspected)
	assert.Equal(t, 3, r.TotalPaths)
}

func TestCensusNegativeControlIsExactlyZeroWithItsDenominator(t *testing.T) {
	r := census(t, symmetricDoc, "", "", "")
	o := only(t, r, "PUT", "/things")
	require.True(t, o.Inspected, "the zero must come from an inspected operation")
	assert.Len(t, o.Pruned, 0)
	assert.Equal(t, 0, r.WithPrunable)
	assert.Equal(t, 1, r.Inspected)

	var buf bytes.Buffer
	WriteText(&buf, r)
	out := buf.String()
	assert.Contains(t, out, "0 of 1 operations had prunable paths")
	assert.Contains(t, out, "prunable=0")
}

func TestCensusZeroPrunableAndNothingInspectedAreDistinguishable(t *testing.T) {
	zero := census(t, symmetricDoc, "", "", "")
	// Restricted to a path with no update operation, nothing is inspected at all.
	none := census(t, symmetricDoc, "", "", "/nowhere")
	require.Equal(t, 0, none.Inspected)

	var a, b bytes.Buffer
	WriteText(&a, zero)
	WriteText(&b, none)
	assert.Contains(t, a.String(), "0 of 1 operations had prunable paths")
	assert.Contains(t, b.String(), "0 of 0 operations had prunable paths")
	assert.NotEqual(t, a.String(), b.String())

	// An update with no create to compare against is skipped, never counted as zero.
	r := census(t, noCreateDoc, "", "", "")
	o := only(t, r, "PUT", "/lonely")
	assert.False(t, o.Inspected)
	assert.NotEmpty(t, o.Skipped)
	assert.Equal(t, 1, r.Skipped)
	assert.Equal(t, 0, r.Inspected)
	var c bytes.Buffer
	WriteText(&c, r)
	assert.Contains(t, c.String(), "skipped:")
	assert.Contains(t, c.String(), "0 of 0 operations had prunable paths")
}

func TestCensusDeepNestingAndArrayElementsAndFreeForm(t *testing.T) {
	r := census(t, deepDoc, "", "", "")
	o := only(t, r, "PATCH", "/widgets/{id}")
	require.True(t, o.Inspected)
	assert.Equal(t, "POST /widgets", o.Create)
	assert.Equal(t, []string{"a.b.c.drop", "rules.[].extra"}, o.Pruned)
	assert.Equal(t, [][]string{{"a", "b", "c", "drop"}, {"rules", "[]", "extra"}}, o.PrunedSegments)
	// `free` is open in the create schema, so nothing under it is enumerated and nothing is reported.
	for _, p := range o.Pruned {
		assert.False(t, strings.HasPrefix(p, "free"), p)
	}
}

// The amendment: a CR spec is wider than the create body. The CRD mode must list project_id; the create
// body fallback cannot see it. Collapsing the two modes fails this test.
func TestCRDInputSeesWhatTheCreateBodyProxyMisses(t *testing.T) {
	crd := census(t, networksDoc, networksCRD, "", "")
	proxy := census(t, networksDoc, "", "", "")

	crdOp := only(t, crd, "PUT", "/networks")
	proxyOp := only(t, proxy, "PUT", "/networks")
	assert.Contains(t, crdOp.Pruned, "network.project_id")
	assert.NotContains(t, proxyOp.Pruned, "network.project_id")
	assert.Len(t, crdOp.Pruned, 4)
	assert.Len(t, proxyOp.Pruned, 3)

	assert.Equal(t, InputCRD, crd.InputShape)
	assert.Equal(t, InputProxy, proxy.InputShape)
}

func TestEveryRunStatesItsInputShape(t *testing.T) {
	var a, b bytes.Buffer
	WriteText(&a, census(t, networksDoc, networksCRD, "", ""))
	WriteText(&b, census(t, networksDoc, "", "", ""))
	assert.True(t, strings.HasPrefix(a.String(), "input shape: generated CRD spec schema"))
	assert.True(t, strings.HasPrefix(b.String(), "input shape: create body schema (PROXY -- narrower than a real CR spec, so this UNDERCOUNTS)"))
}

func TestRunEndToEndTextAndJSON(t *testing.T) {
	dir := t.TempDir()
	oas := filepath.Join(dir, "oas.yaml")
	crd := filepath.Join(dir, "crd.yaml")
	require.NoError(t, os.WriteFile(oas, []byte(networksDoc), 0o600))
	require.NoError(t, os.WriteFile(crd, []byte(networksCRD), 0o600))

	var out, errb bytes.Buffer
	require.Equal(t, 0, run([]string{"--oas", oas, "--input", crd, "--method", "put", "--path", "/networks"}, &out, &errb), errb.String())
	assert.Contains(t, out.String(), "  network.provider:segmentation_id\n")
	assert.Contains(t, out.String(), "1 of 1 operations had prunable paths")

	out.Reset()
	require.Equal(t, 0, run([]string{"--oas", oas, "--json"}, &out, &errb), errb.String())
	var rep Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &rep))
	assert.Equal(t, InputProxy, rep.InputShape)
	assert.Equal(t, 3, rep.TotalPaths)

	assert.Equal(t, 2, run([]string{}, &out, &errb))
}
