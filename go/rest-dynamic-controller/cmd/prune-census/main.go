// Command prune-census reports, per update operation of an OpenAPI document, the nested request-body
// paths the update prune (issue 960) removes: the paths the CREATE schema declares and the UPDATE schema
// does not.
//
// It does not reason about schemas. It asks the real code: it builds the real restclient.BodyShape for
// the create and the update operation, assembles a body holding every path the create shape lists, runs
// the real (*BodyShape).Prune of the update shape over it, and reports whatever is missing from the
// result. Every allOf merge, oneOf/anyOf union, free-form and required rule is therefore the one the
// controller applies, and the answer follows those rules if they change.
//
// Only OpenAPI 3.x documents are accepted: that is what the controller's own loader builds.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/pb33f/libopenapi"
	"sigs.k8s.io/yaml"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
)

// Operation is one update operation's result.
type Operation struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	// Create is the "METHOD path" whose schema was the baseline; empty when none was found.
	Create string `json:"create,omitempty"`
	// Inspected is false when there was nothing to compare, with Skipped saying why. An uninspected
	// operation is NOT an operation with zero prunable paths.
	Inspected bool   `json:"inspected"`
	Skipped   string `json:"skipped,omitempty"`
	// Pruned lists each removed path in dotted form; "[]" marks an array element.
	Pruned []string `json:"pruned"`
	// PrunedSegments is the same list unambiguously, for keys that themselves contain a dot.
	PrunedSegments [][]string `json:"prunedSegments"`
}

// Input shape labels. Every run states which one produced its numbers.
const (
	InputCRD   = "crd-spec-schema"
	InputProxy = "create-body-schema-PROXY"
)

// Report is the whole census.
type Report struct {
	// InputShape says which input produced the numbers: InputCRD is the real answer, InputProxy is
	// narrower than a real CR spec (it lacks parameters merged from every verb) and so UNDERCOUNTS.
	InputShape string `json:"inputShape"`
	// InputSource is the CRD file for InputCRD, empty for the proxy.
	InputSource string      `json:"inputSource,omitempty"`
	Operations  []Operation `json:"operations"`
	// Inspected is the denominator: operations that had both schemas to compare.
	Inspected int `json:"inspected"`
	// WithPrunable is how many of those lose at least one path.
	WithPrunable int `json:"withPrunable"`
	Skipped      int `json:"skipped"`
	TotalPaths   int `json:"totalPaths"`
}

var updateMethods = []string{"put", "patch"}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("prune-census", flag.ContinueOnError)
	fs.SetOutput(stderr)
	oas := fs.String("oas", "", "path to an OpenAPI 3.x document (required)")
	method := fs.String("method", "", "restrict to one update method (put or patch)")
	path := fs.String("path", "", "restrict to one path")
	input := fs.String("input", "", "path to the generated CRD (YAML or JSON); its spec schema is the input shape (primary mode)")
	crdVersion := fs.String("crd-version", "", "CRD version to read (default: the storage version)")
	asJSON := fs.Bool("json", false, "print JSON instead of text")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *oas == "" {
		fmt.Fprintln(stderr, "prune-census: --oas is required")
		return 2
	}
	raw, err := os.ReadFile(*oas)
	if err != nil {
		fmt.Fprintf(stderr, "prune-census: %v\n", err)
		return 1
	}
	var crd []byte
	if *input != "" {
		if crd, err = os.ReadFile(*input); err != nil {
			fmt.Fprintf(stderr, "prune-census: %v\n", err)
			return 1
		}
	}
	rep, err := Census(raw, crd, *crdVersion, *method, *path)
	if err != nil {
		fmt.Fprintf(stderr, "prune-census: %v\n", err)
		return 1
	}
	if *input != "" {
		rep.InputSource = *input
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(stderr, "prune-census: %v\n", err)
			return 1
		}
		return 0
	}
	WriteText(stdout, rep)
	return 0
}

// Census runs the census over an OpenAPI 3.x document. method and path, when non-empty, restrict it.
// crd, when non-nil, is a generated CRD whose spec schema is the input shape; otherwise the create body
// schema is used as a labelled PROXY.
func Census(raw, crd []byte, crdVersion, method, path string) (*Report, error) {
	var inputShape *restclient.BodyShape
	if crd != nil {
		var err error
		if inputShape, err = shapeFromCRD(crd, crdVersion); err != nil {
			return nil, err
		}
	}
	doc, err := libopenapi.NewDocument(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing document: %w", err)
	}
	model, errs := doc.BuildV3Model()
	if model == nil {
		return nil, fmt.Errorf("document is not a buildable OpenAPI 3.x document: %v", errs)
	}
	cli := &restclient.UnstructuredClient{DocScheme: model}

	method = strings.ToLower(method)
	if method != "" && method != "put" && method != "patch" {
		return nil, fmt.Errorf("--method %q is not an update verb (put or patch)", method)
	}
	rep := &Report{Operations: []Operation{}, InputShape: InputProxy}
	if inputShape != nil {
		rep.InputShape = InputCRD
	}
	if model.Model.Paths == nil || model.Model.Paths.PathItems == nil {
		return rep, nil
	}
	var paths []string
	for p := model.Model.Paths.PathItems.First(); p != nil; p = p.Next() {
		if path == "" || p.Key() == path {
			paths = append(paths, p.Key())
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		item, _ := model.Model.Paths.PathItems.Get(p)
		ops := item.GetOperations()
		for _, m := range updateMethods {
			if method != "" && m != method {
				continue
			}
			if _, ok := ops.Get(m); !ok {
				continue
			}
			rep.Operations = append(rep.Operations, inspect(cli, inputShape, m, p))
		}
	}
	for _, o := range rep.Operations {
		if !o.Inspected {
			rep.Skipped++
			continue
		}
		rep.Inspected++
		if len(o.Pruned) > 0 {
			rep.WithPrunable++
			rep.TotalPaths += len(o.Pruned)
		}
	}
	return rep, nil
}

// shapeFromCRD builds the input shape from a generated CRD's spec schema. It does not interpret the
// schema: the spec schema is placed as the request body of a one-operation OpenAPI document and handed to
// the real BodyShape, so the same merge, union, free-form and required rules apply to it as to the
// update operation's own schema.
func shapeFromCRD(crd []byte, version string) (*restclient.BodyShape, error) {
	var c struct {
		Spec struct {
			Versions []struct {
				Name    string `json:"name"`
				Storage bool   `json:"storage"`
				Schema  struct {
					OpenAPIV3Schema struct {
						Properties map[string]interface{} `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(crd, &c); err != nil {
		return nil, fmt.Errorf("parsing CRD: %w", err)
	}
	if len(c.Spec.Versions) == 0 {
		return nil, fmt.Errorf("CRD has no spec.versions")
	}
	pick := -1
	for i, v := range c.Spec.Versions {
		if (version != "" && v.Name == version) || (version == "" && v.Storage) {
			pick = i
			break
		}
	}
	if pick < 0 {
		return nil, fmt.Errorf("CRD has no version %q (or no storage version when none was named)", version)
	}
	specSchema, ok := c.Spec.Versions[pick].Schema.OpenAPIV3Schema.Properties["spec"]
	if !ok {
		return nil, fmt.Errorf("CRD version %q has no spec schema", c.Spec.Versions[pick].Name)
	}
	doc := map[string]interface{}{
		"openapi": "3.0.3",
		"info":    map[string]interface{}{"title": "crd-input", "version": "1"},
		"paths": map[string]interface{}{"/input": map[string]interface{}{"post": map[string]interface{}{
			"requestBody": map[string]interface{}{"content": map[string]interface{}{
				"application/json": map[string]interface{}{"schema": specSchema}}},
			"responses": map[string]interface{}{"200": map[string]interface{}{"description": "ok"}},
		}}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	d, err := libopenapi.NewDocument(raw)
	if err != nil {
		return nil, fmt.Errorf("wrapping CRD spec schema: %w", err)
	}
	m, errs := d.BuildV3Model()
	if m == nil {
		return nil, fmt.Errorf("wrapping CRD spec schema: %v", errs)
	}
	shape, err := (&restclient.UnstructuredClient{DocScheme: m}).BodyShape("post", "/input")
	if err != nil {
		return nil, err
	}
	if shape == nil {
		return nil, fmt.Errorf("CRD spec schema produced no shape")
	}
	return shape, nil
}

// createCandidates lists where the create schema may live: POST on the same path, else POST on the
// collection path (the update path with a trailing {param} segment dropped).
func createCandidates(path string) []string {
	out := []string{path}
	if i := strings.LastIndex(path, "/"); i > 0 {
		if last := path[i+1:]; strings.HasPrefix(last, "{") && strings.HasSuffix(last, "}") {
			out = append(out, path[:i])
		}
	}
	return out
}

func inspect(cli *restclient.UnstructuredClient, inputShape *restclient.BodyShape, method, path string) Operation {
	op := Operation{Method: strings.ToUpper(method), Path: path, Pruned: []string{}, PrunedSegments: [][]string{}}

	updShape, err := cli.BodyShape(method, path)
	if err != nil {
		op.Skipped = "update schema: " + err.Error()
		return op
	}
	if updShape == nil {
		op.Skipped = "update operation declares no request body"
		return op
	}
	createShape := inputShape
	if inputShape != nil {
		op.Create = "CRD spec schema"
	}
	for _, cp := range createCandidates(path) {
		if inputShape != nil {
			break
		}
		s, err := cli.BodyShape("post", cp)
		if err != nil || s == nil {
			continue
		}
		createShape, op.Create = s, "POST "+cp
		break
	}
	if createShape == nil {
		op.Skipped = "no POST operation with a request body for this path or its collection path"
		return op
	}

	// The body holds every path the CREATE shape lists; the real Prune of the UPDATE shape then decides.
	body, _ := fillFromShape(createShape).(map[string]interface{})
	if body == nil {
		body = map[string]interface{}{}
	}
	out := updShape.Prune(body)
	op.Inspected = true
	op.PrunedSegments = missing(body, out, nil)
	sort.Slice(op.PrunedSegments, func(i, j int) bool {
		return strings.Join(op.PrunedSegments[i], "\x00") < strings.Join(op.PrunedSegments[j], "\x00")
	})
	if op.PrunedSegments == nil {
		op.PrunedSegments = [][]string{}
	}
	for _, segs := range op.PrunedSegments {
		op.Pruned = append(op.Pruned, strings.Join(segs, "."))
	}
	return op
}

// fillFromShape assembles a value holding every key the shape lists. It reads the shape's own fields; it
// does not interpret any schema. An open (unenumerated) sub-tree is a leaf, since nothing in it is listed.
func fillFromShape(s *restclient.BodyShape) interface{} {
	if s == nil || s.Open {
		return "x"
	}
	if s.Items != nil {
		return []interface{}{fillFromShape(s.Items)}
	}
	if s.Properties == nil {
		return "x"
	}
	m := make(map[string]interface{}, len(s.Properties))
	for k, child := range s.Properties {
		m[k] = fillFromShape(child)
	}
	return m
}

// missing returns the paths present in in but absent from out.
func missing(in, out interface{}, prefix []string) [][]string {
	var res [][]string
	switch iv := in.(type) {
	case map[string]interface{}:
		ov, _ := out.(map[string]interface{})
		for k, v := range iv {
			p := append(append([]string{}, prefix...), k)
			o, ok := ov[k]
			if !ok {
				res = append(res, p)
				continue
			}
			res = append(res, missing(v, o, p)...)
		}
	case []interface{}:
		ov, _ := out.([]interface{})
		p := append(append([]string{}, prefix...), "[]")
		for i, v := range iv {
			if i >= len(ov) {
				break
			}
			res = append(res, missing(v, ov[i], p)...)
		}
	}
	return res
}

// WriteText prints the stable text form: one header per operation, one path per line beneath it, then
// the denominator. The summary line is printed even when every count is zero.
func WriteText(w io.Writer, r *Report) {
	switch r.InputShape {
	case InputCRD:
		fmt.Fprintf(w, "input shape: generated CRD spec schema (%s)\n", r.InputSource)
	default:
		fmt.Fprintln(w, "input shape: create body schema (PROXY -- narrower than a real CR spec, so this UNDERCOUNTS)")
	}
	for _, o := range r.Operations {
		if !o.Inspected {
			fmt.Fprintf(w, "operation %s %s skipped: %s\n", o.Method, o.Path, o.Skipped)
			continue
		}
		fmt.Fprintf(w, "operation %s %s (create: %s) prunable=%d\n", o.Method, o.Path, o.Create, len(o.Pruned))
		for _, p := range o.Pruned {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}
	fmt.Fprintf(w, "%d of %d operations had prunable paths (%d paths; %d operations skipped as not inspectable)\n",
		r.WithPrunable, r.Inspected, r.TotalPaths, r.Skipped)
}
