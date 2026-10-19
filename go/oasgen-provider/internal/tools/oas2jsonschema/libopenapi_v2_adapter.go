package oas2jsonschema

import (
	"errors"
	"log"
	"strconv"

	"github.com/pb33f/libopenapi"
	v2 "github.com/pb33f/libopenapi/datamodel/high/v2"
	"gopkg.in/yaml.v3"
)

// --- Swagger / OpenAPI 2.0 ---
//
// A SECOND ADAPTER behind the same OASDocument interface, not a 2.0 -> 3.0 conversion on ingest.
//
// Converting on ingest is cheaper to write and breaks the guarantee this repo enforces everywhere else:
// status.oasHash would hash a document the vendor never published, and #108's cross-document
// disagreement logic would be reasoning about artefacts nobody can go and read. VerbsDescription.OASPath
// is explicit that documents are read as published and never modified.
//
// It is also much cheaper than it looks, because the bulk of this package is reusable as-is:
// v2.Parameter.Schema and v2.Response.Schema are both *base.SchemaProxy -- the SAME shared type the v3
// path uses -- so convertLibopenapiSchema takes them unchanged. Only two things are genuinely new, and
// both are bounded: non-body parameters carry their type inline instead of in a schema, and responses
// have no MIME map.
//
// Four high-value targets publish 2.0: ArgoCD, Grafana, Argo Workflows and Harbor (#196).

type libOASv2DocumentAdapter struct {
	doc *libopenapi.DocumentModel[v2.Swagger]
	// produces is the document-level `produces`, used as the MIME type of a response that has only a
	// schema. See synthesiseResponseContent.
	produces []string
}

// NewLibOASv2DocumentAdapter adapts a Swagger 2.0 model to the OASDocument interface.
func NewLibOASv2DocumentAdapter(doc *libopenapi.DocumentModel[v2.Swagger]) OASDocument {
	return &libOASv2DocumentAdapter{doc: doc, produces: doc.Model.Produces}
}

func (a *libOASv2DocumentAdapter) FindPath(path string) (PathItem, bool) {
	if a.doc.Model.Paths == nil || a.doc.Model.Paths.PathItems == nil {
		return nil, false
	}
	p, ok := a.doc.Model.Paths.PathItems.Get(path)
	if !ok {
		return nil, false
	}
	return &libOASv2PathItemAdapter{path: p, produces: a.produces}, true
}

func (a *libOASv2DocumentAdapter) Version() string {
	if a.doc.Model.Info == nil {
		return ""
	}
	return a.doc.Model.Info.Version
}

// SecuritySchemes translates securityDefinitions into the v3-shaped vocabulary the rest of this package
// speaks, because the configuration builder matches on (type, scheme, in) as OAS 3 names them.
//
// Swagger 2.0's `type: basic` is OAS 3's `type: http, scheme: basic`; without that mapping a 2.0
// document declaring basic auth would generate NO authentication field and every request would go out
// unauthenticated. 2.0 has no bearer type at all -- a bearer token there is conventionally an apiKey in
// the Authorization header, which the configuration builder already handles.
func (a *libOASv2DocumentAdapter) SecuritySchemes() []SecuritySchemeInfo {
	if a.doc.Model.SecurityDefinitions == nil || a.doc.Model.SecurityDefinitions.Definitions == nil {
		return nil
	}
	var schemes []SecuritySchemeInfo
	for pair := a.doc.Model.SecurityDefinitions.Definitions.First(); pair != nil; pair = pair.Next() {
		v := pair.Value()
		info := SecuritySchemeInfo{
			Name:      pair.Key(),
			Type:      SecuritySchemeType(v.Type),
			In:        v.In,
			ParamName: v.Name,
		}
		if v.Type == "basic" {
			info.Type = SchemeTypeHTTP
			info.Scheme = "basic"
		}
		schemes = append(schemes, info)
	}
	return schemes
}

type libOASv2PathItemAdapter struct {
	path     *v2.PathItem
	produces []string
}

// GetOperations hands each operation the path item's shared parameters, exactly as the v3 adapter does.
// v2.PathItem carries Parameters too, and a path-item parameter is as invisible here as it was there
// (#197) if the operation adapter does not merge it.
func (a *libOASv2PathItemAdapter) GetOperations() map[string]Operation {
	ops := make(map[string]Operation)
	rawOps := a.path.GetOperations()
	if rawOps == nil {
		return ops
	}
	for pair := rawOps.First(); pair != nil; pair = pair.Next() {
		ops[pair.Key()] = &libOASv2OperationAdapter{
			op:         pair.Value(),
			pathParams: a.path.Parameters,
			produces:   a.produces,
		}
	}
	return ops
}

type libOASv2OperationAdapter struct {
	op         *v2.Operation
	pathParams []*v2.Parameter
	produces   []string
}

// GetParameters merges the path item's shared parameters under the operation's own, by name AND
// location, matching the v3 contract.
//
// The `in: body` parameter is deliberately EXCLUDED: in Swagger 2.0 the request body is modelled as a
// parameter, and GetRequestBody below is what surfaces it. Leaving it in would also make it a spec
// field in its own right, named "body", alongside the properties it already contributes.
func (a *libOASv2OperationAdapter) GetParameters() []ParameterInfo {
	var params []ParameterInfo
	seen := make(map[string]struct{}, len(a.op.Parameters))
	for _, p := range a.op.Parameters {
		if p == nil || p.In == "body" {
			continue
		}
		seen[parameterKey(p.Name, p.In)] = struct{}{}
		params = append(params, convertV2Parameter(p))
	}
	for _, p := range a.pathParams {
		if p == nil || p.In == "body" {
			continue
		}
		if _, overridden := seen[parameterKey(p.Name, p.In)]; overridden {
			continue
		}
		params = append(params, convertV2Parameter(p))
	}
	return params
}

// GetRequestBody returns the `in: body` parameter's schema.
//
// Swagger 2.0 has no requestBody object and no MIME map on the request: at most one parameter may be
// `in: body`, and the media types it is sent as are the document- or operation-level `consumes`. The
// schema is a *base.SchemaProxy, so the existing v3 converter takes it unchanged.
func (a *libOASv2OperationAdapter) GetRequestBody() RequestBodyInfo {
	body := a.bodyParameter()
	if body == nil || body.Schema == nil {
		return RequestBodyInfo{}
	}

	schema := convertLibopenapiSchema(body.Schema)
	mimes := a.op.Consumes
	if len(mimes) == 0 {
		// Nothing says otherwise, so assume JSON rather than returning a body under no media type at
		// all, which would read downstream as "this operation has no request body".
		mimes = []string{defaultV2MIMEType}
	}

	content := make(map[string]*Schema, len(mimes))
	for _, mime := range mimes {
		content[mime] = schema
	}
	return RequestBodyInfo{Content: content}
}

// bodyParameter returns the operation's `in: body` parameter, looking at the path item's shared
// parameters too, since 2.0 permits declaring it there.
func (a *libOASv2OperationAdapter) bodyParameter() *v2.Parameter {
	for _, p := range a.op.Parameters {
		if p != nil && p.In == "body" {
			return p
		}
	}
	for _, p := range a.pathParams {
		if p != nil && p.In == "body" {
			return p
		}
	}
	return nil
}

// defaultV2MIMEType is what a 2.0 document means when it says nothing. 2.0 responses carry a bare
// schema with no media type, and the rest of this package keys content by MIME.
const defaultV2MIMEType = "application/json"

func (a *libOASv2OperationAdapter) GetResponses() map[int]ResponseInfo {
	if a.op.Responses == nil || a.op.Responses.Codes == nil {
		return nil
	}
	responses := make(map[int]ResponseInfo)
	for pair := a.op.Responses.Codes.First(); pair != nil; pair = pair.Next() {
		code, err := strconv.Atoi(pair.Key())
		if err != nil {
			continue
		}
		responses[code] = ResponseInfo{Content: a.synthesiseResponseContent(pair.Value())}
	}
	return responses
}

// synthesiseResponseContent turns a 2.0 response's single schema into the MIME-keyed map the rest of
// this package expects.
//
// The media types come from the operation's `produces`, then the document's, then JSON. Narrowing by
// `produces` rather than always claiming application/json matters because GeneratorConfig.
// AcceptedMIMETypes filters on these: a document that produces only `application/xml` should not have
// its responses presented as JSON, which would make the generator pick up a body it cannot actually
// parse at runtime.
func (a *libOASv2OperationAdapter) synthesiseResponseContent(resp *v2.Response) map[string]*Schema {
	if resp == nil || resp.Schema == nil {
		return map[string]*Schema{}
	}
	schema := convertLibopenapiSchema(resp.Schema)

	mimes := a.op.Produces
	if len(mimes) == 0 {
		mimes = a.produces
	}
	if len(mimes) == 0 {
		mimes = []string{defaultV2MIMEType}
	}

	content := make(map[string]*Schema, len(mimes))
	for _, mime := range mimes {
		content[mime] = schema
	}
	return content
}

// convertV2Parameter builds a ParameterInfo from a Swagger 2.0 parameter.
//
// This is the one real piece of conversion work in the v2 path. A non-body 2.0 parameter has no
// `schema`: it carries type/format/enum/items/default inline on the parameter itself, so a Schema has
// to be synthesised from those fields. It is bounded -- there is no recursion to worry about, since 2.0
// forbids object and nested-object non-body parameters; the only nesting is `items`, for arrays.
func convertV2Parameter(p *v2.Parameter) ParameterInfo {
	info := ParameterInfo{
		Name:        p.Name,
		In:          p.In,
		Description: p.Description,
		Required:    p.Required != nil && *p.Required,
	}

	// A body parameter does carry a real schema; everything else is inline. Handled here as well as in
	// the callers so this function is correct standing alone.
	if p.Schema != nil {
		info.Schema = convertLibopenapiSchema(p.Schema)
		return info
	}

	info.Schema = synthesiseV2ParameterSchema(p)
	return info
}

// synthesiseV2ParameterSchema builds a Schema from the fields a 2.0 non-body parameter carries inline.
//
// Pattern/minimum/maximum and the other numeric facets are deliberately dropped: the domain Schema has
// nowhere to put them, and inventing a description that restates them would put prose where validation
// belongs. They were never expressible for v3 parameters either, so this is parity, not a v2 gap.
func synthesiseV2ParameterSchema(p *v2.Parameter) *Schema {
	s := &Schema{Format: p.Format}
	if p.Type != "" {
		s.Type = []string{p.Type}
	}
	s.Default = decodeYAMLNode(p.Default)
	for _, e := range p.Enum {
		if v := decodeYAMLNode(e); v != nil {
			s.Enum = append(s.Enum, v)
		}
	}
	if p.Items != nil {
		s.Items = synthesiseV2ItemsSchema(p.Items)
	}
	return s
}

// synthesiseV2ItemsSchema converts an `items` object, which is 2.0's way of typing array elements for
// non-body parameters. It nests, so this recurses -- bounded by the document's own nesting.
func synthesiseV2ItemsSchema(items *v2.Items) *Schema {
	if items == nil {
		return nil
	}
	s := &Schema{Format: items.Format}
	if items.Type != "" {
		s.Type = []string{items.Type}
	}
	s.Default = decodeYAMLNode(items.Default)
	for _, e := range items.Enum {
		if v := decodeYAMLNode(e); v != nil {
			s.Enum = append(s.Enum, v)
		}
	}
	if items.Items != nil {
		s.Items = synthesiseV2ItemsSchema(items.Items)
	}
	return s
}

func decodeYAMLNode(n *yaml.Node) interface{} {
	if n == nil {
		return nil
	}
	var out interface{}
	if err := n.Decode(&out); err != nil {
		log.Printf("Failed to decode Swagger 2.0 node: %v", err)
		return nil
	}
	return out
}

// parseV2 builds the Swagger 2.0 model, mirroring the V3 path step for step so the two cannot drift in
// what they consider a usable document: build, reject a nil model, then resolve references.
func parseV2(d libopenapi.Document) (OASDocument, error) {
	doc, modelErrors := d.BuildV2Model()
	if len(modelErrors) > 0 {
		return nil, ParserError{
			Code:    CodeModelBuildError,
			Message: "failed to build V2 (Swagger 2.0) model",
			Err:     errors.Join(modelErrors...),
		}
	}
	if doc == nil {
		return nil, ParserError{
			Code:    CodeModelBuildError,
			Message: "resulting document was nil after building the Swagger 2.0 model",
		}
	}

	resolvingErrors := doc.Index.GetResolver().Resolve()
	if len(resolvingErrors) > 0 {
		var errs []error
		for i := range resolvingErrors {
			errs = append(errs, resolvingErrors[i].ErrorRef)
		}
		return nil, ParserError{
			Code:    CodeModelResolutionError,
			Message: "failed to resolve model references",
			Err:     errors.Join(errs...),
		}
	}

	return NewLibOASv2DocumentAdapter(doc), nil
}
