package oas2jsonschema

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/safety"
	"github.com/pb33f/libopenapi"

	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"
	"github.com/pb33f/libopenapi/utils"
)

// --- Parser Implementation ---

type libOASParser struct{}

// NewLibOASParser creates a new parser that uses the libopenapi library.
func NewLibOASParser() Parser {
	return &libOASParser{}
}

// Parse takes raw OpenAPI specification content and returns a document
// that conforms to the OASDocument interface.
func (p *libOASParser) Parse(content []byte) (OASDocument, error) {

	d, err := libopenapi.NewDocument(content)
	if err != nil {
		return nil, ParserError{
			Code:    CodeDocumentCreationError,
			Message: "failed to create new libopenapi document",
			Err:     err,
		}
	}

	// Sniff the version BEFORE building a model: BuildV3Model rejects a Swagger 2.0 document outright
	// ("supplied spec is a different version (oas2)"), which is how 2.0 documents used to be refused at
	// the front door. ArgoCD, Grafana, Argo Workflows and Harbor all publish 2.0 (#196).
	if info := d.GetSpecInfo(); info != nil && info.SpecType == utils.OpenApi2 {
		return parseV2(d)
	}

	doc, modelErrors := d.BuildV3Model()
	if len(modelErrors) > 0 {
		return nil, ParserError{
			Code:    CodeModelBuildError,
			Message: "failed to build V3 model",
			Err:     errors.Join(modelErrors...),
		}
	}
	if doc == nil {
		return nil, ParserError{
			Code:    CodeModelBuildError,
			Message: "resulting document was nil after building model",
		}
	}

	// Resolve model references
	// Ensures that all $ref pointers are properly resolved before further processing.
	// This is just a validation step by `libopenapi`, there is no replacement of references in the model
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

	return NewLibOASDocumentAdapter(doc), nil
}

// --- Adapter Implementation ---

type libOASDocumentAdapter struct {
	doc *libopenapi.DocumentModel[v3.Document]
}

// We implement the OASDocument interface for the libopenapi DocumentModel
func NewLibOASDocumentAdapter(doc *libopenapi.DocumentModel[v3.Document]) OASDocument {
	return &libOASDocumentAdapter{doc: doc}
}

func (a *libOASDocumentAdapter) FindPath(path string) (PathItem, bool) {
	p, ok := a.doc.Model.Paths.PathItems.Get(path)
	if !ok {
		return nil, false
	}
	return &libOASPathItemAdapter{path: p}, true
}

func (a *libOASDocumentAdapter) Version() string {
	if a.doc.Model.Info == nil {
		return ""
	}
	return a.doc.Model.Info.Version
}

func (a *libOASDocumentAdapter) SecuritySchemes() []SecuritySchemeInfo {
	if a.doc.Model.Components == nil || a.doc.Model.Components.SecuritySchemes == nil {
		return nil
	}
	var schemes []SecuritySchemeInfo
	for pair := a.doc.Model.Components.SecuritySchemes.First(); pair != nil; pair = pair.Next() {
		v := pair.Value()
		schemes = append(schemes, SecuritySchemeInfo{
			Name:      pair.Key(),
			Type:      SecuritySchemeType(v.Type),
			Scheme:    v.Scheme,
			In:        v.In,
			ParamName: v.Name,
		})
	}
	return schemes
}

type libOASPathItemAdapter struct {
	path *v3.PathItem
}

func (a *libOASPathItemAdapter) GetOperations() map[string]Operation {
	ops := make(map[string]Operation)
	rawOps := a.path.GetOperations()
	for pair := rawOps.First(); pair != nil; pair = pair.Next() {
		// Every operation on this path inherits the path item's shared parameters. Handing them to
		// the operation adapter here is what makes them visible to both callers at once (#197).
		ops[pair.Key()] = &libOASOperationAdapter{op: pair.Value(), pathParams: a.path.Parameters}
	}
	return ops
}

type libOASOperationAdapter struct {
	op *v3.Operation
	// pathParams are the parameters the enclosing path item declares for all of its operations.
	// They are merged UNDER op.Parameters by GetParameters.
	pathParams []*v3.Parameter
}

// GetParameters merges the path item's shared parameters under the operation's own.
//
// OAS 3 lets a path item declare `parameters` that apply to each of its operations, and an operation
// may override one by redeclaring the same name and location. Keycloak leans on this heavily -- it
// declares path-item parameters on 273 of its 274 paths -- so reading only op.Parameters made a
// REQUIRED path parameter like `realm` invisible, and the generated kind had no spec field with which
// to build its own URL.
func (a *libOASOperationAdapter) GetParameters() []ParameterInfo {
	// Operation-level first, so the override rule is a plain "already seen" check below rather than
	// an append-then-overwrite that depends on map iteration order.
	var params []ParameterInfo
	seen := make(map[string]struct{}, len(a.op.Parameters))
	for _, p := range a.op.Parameters {
		if p == nil {
			continue
		}
		seen[parameterKey(p.Name, p.In)] = struct{}{}
		params = append(params, convertLibopenapiParameter(p))
	}
	for _, p := range a.pathParams {
		if p == nil {
			continue
		}
		if _, overridden := seen[parameterKey(p.Name, p.In)]; overridden {
			continue
		}
		params = append(params, convertLibopenapiParameter(p))
	}
	return params
}

// parameterKey identifies a parameter the way OAS does: by name AND location. A `realm` path
// parameter and a `realm` query parameter are two different parameters, and only the matching pair
// overrides.
func parameterKey(name, in string) string {
	return in + "\x00" + name
}

func convertLibopenapiParameter(p *v3.Parameter) ParameterInfo {
	return ParameterInfo{
		Name:        p.Name,
		In:          p.In,
		Description: p.Description,
		Required:    p.Required != nil && *p.Required,
		Schema:      convertLibopenapiSchema(p.Schema),
	}
}

func (a *libOASOperationAdapter) GetRequestBody() RequestBodyInfo {
	if a.op.RequestBody == nil || a.op.RequestBody.Content == nil {
		return RequestBodyInfo{}
	}
	content := make(map[string]*Schema)
	for pair := a.op.RequestBody.Content.First(); pair != nil; pair = pair.Next() {
		content[pair.Key()] = convertLibopenapiSchema(pair.Value().Schema)
	}
	return RequestBodyInfo{Content: content}
}

func (a *libOASOperationAdapter) GetResponses() map[int]ResponseInfo {
	if a.op.Responses == nil || a.op.Responses.Codes == nil {
		return nil
	}
	responses := make(map[int]ResponseInfo)
	for pair := a.op.Responses.Codes.First(); pair != nil; pair = pair.Next() {
		code, err := strconv.Atoi(pair.Key())
		if err != nil {
			continue
		}
		content := make(map[string]*Schema)
		if pair.Value().Content != nil {
			for contentPair := pair.Value().Content.First(); contentPair != nil; contentPair = contentPair.Next() {
				content[contentPair.Key()] = convertLibopenapiSchema(contentPair.Value().Schema)
			}
		}
		responses[code] = ResponseInfo{Content: content}
	}
	return responses
}

// --- Conversion Utilities ---

// convertLibopenapiSchema is the entry point for schema conversion. It sets up a
// map to track visited schema proxies to prevent infinite recursion in the case
// of circular references.
func convertLibopenapiSchema(proxy *base.SchemaProxy) *Schema {

	guard := safety.NewRecursionGuard(50, 5000, 30*time.Second)
	ctx, cancel := guard.WithContext()
	defer cancel()

	return convertLibopenapiSchemaWithVisited(ctx, proxy, guard, make(map[string]*Schema), 0)
}

// convertLibopenapiSchemaWithVisited is the recursive implementation of the schema conversion.
// It uses the 'visited' map to detect and handle circular references.
func convertLibopenapiSchemaWithVisited(
	ctx context.Context,
	proxy *base.SchemaProxy,
	guard *safety.RecursionGuard,
	visited map[string]*Schema,
	depth int,
) *Schema {

	if proxy == nil {
		return nil
	}

	if err := guard.Check(ctx, depth); err != nil {
		log.Printf("schema recursion aborted: %v", err)
		return &Schema{Type: []string{"object"}}
	}

	// Use the reference pointer string as the unique key for cycle detection,
	// but only if the proxy is actually a reference.
	// Otherwise, use the pointer address as a unique key.
	// This ensures that we can handle both referenced and inline schemas correctly.
	// If the schema is a reference, we use its $ref value as the key.
	// If it's not a reference, we use its memory address to uniquely identify it.
	if proxy.IsReference() {
		key := proxy.GetReference()
		if key == "" {
			key = fmt.Sprintf("%p", proxy)
		}
		if existing, ok := visited[key]; ok {
			//log.Printf("Encountered already visited schema reference: %s", key)
			//log.Printf("Schema info: Type=%v, Description=%q, Properties=%d", existing.Type, existing.Description, len(existing.Properties))
			return existing
		}
	} else {
		// For non-references, use the pointer address as a unique key.
		key := fmt.Sprintf("%p", proxy)
		if existing, ok := visited[key]; ok {
			//log.Printf("Encountered already visited inline schema: %s", key)
			//log.Printf("Schema info: Type=%v, Description=%q, Properties=%d", existing.Type, existing.Description, len(existing.Properties))
			return existing
		}
	}

	// Create a new schema placeholder and add it to the visited map before building or processing the schema.
	// This is used to break the recursion.
	// domainSchema is the schema defined in this domain (oas2jsonschema package)
	domainSchema := &Schema{}

	if proxy.IsReference() {
		ref := proxy.GetReference()
		if ref != "" {
			visited[ref] = domainSchema
		}
	}

	// Gracefully handle panics from the underlying library, which can occur with invalid schemas (e.g., dangling references).
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Schema conversion panic for ref=%s proxy=%p: %v", proxy.GetReference(), proxy, r)
		}
	}()

	s, err := proxy.BuildSchema() // This step resolves the reference if it's a $ref
	if err != nil {
		log.Printf("Schema build error: %v", err)
		return domainSchema // Return the placeholder to avoid breaking parent
	}

	if s == nil {
		log.Printf("Schema build returned nil for proxy: %v", proxy)
		return domainSchema
	}

	// Default handling
	var defaultVal interface{}
	if s.Default != nil {
		if err := s.Default.Decode(&defaultVal); err != nil {
			log.Printf("Failed to decode default value: %v", err)
		}
	}

	// Populate the placeholder schema we created earlier by modifying its fields directly.
	// Do not reassign domainSchema as that would break the circular reference handling
	domainSchema.Type = s.Type
	domainSchema.Description = s.Description
	domainSchema.Required = s.Required
	domainSchema.Default = defaultVal

	// Enum handling
	var enumValues []interface{}
	if len(s.Enum) > 0 {
		for _, enumProxy := range s.Enum {
			var enumVal interface{}
			if err := enumProxy.Decode(&enumVal); err != nil {
				log.Printf("Failed to decode enum value: %v (raw: %#v)", err, enumProxy)
				continue
			}
			enumValues = append(enumValues, enumVal)
		}
		domainSchema.Enum = enumValues
	}

	// AdditionalProperties handling
	if s.AdditionalProperties != nil {
		switch {
		case s.AdditionalProperties.IsB():
			// Boolean form: allows or disallows any additional properties.
			domainSchema.AdditionalProperties = &AdditionalProperties{Bool: s.AdditionalProperties.B}
		case s.AdditionalProperties.IsA():
			// Object form: a typed free-form map (additionalProperties: {type: string}). Recursed through
			// the same guard/visited/depth machinery as Items, so a self-referential value schema is
			// handled identically. Dropping this used to cost the CRD both the value type and its
			// validation — see the AdditionalProperties doc comment in types.go.
			if valueSchema := convertLibopenapiSchemaWithVisited(ctx, s.AdditionalProperties.A, guard, visited, depth+1); valueSchema != nil {
				domainSchema.AdditionalProperties = &AdditionalProperties{Schema: valueSchema}
			}
		default:
			log.Print("Warning: unknown additionalProperties form; ignored")
		}
	}

	// Validation keywords (#249). Copied verbatim: every one of these is representable in a CRD's
	// OpenAPI v3 schema under the same name, so there is nothing to translate. Before #249 they were
	// read off the document and dropped here, which made a declared constraint silently unenforced.
	domainSchema.Pattern = s.Pattern
	domainSchema.MinLength = s.MinLength
	domainSchema.MaxLength = s.MaxLength
	domainSchema.Minimum = s.Minimum
	domainSchema.Maximum = s.Maximum
	domainSchema.MultipleOf = s.MultipleOf
	domainSchema.MinItems = s.MinItems
	domainSchema.MaxItems = s.MaxItems
	if s.UniqueItems != nil {
		domainSchema.UniqueItems = *s.UniqueItems
	}
	// exclusiveMinimum/Maximum changed meaning between OAS 3.0 (a boolean modifier on minimum/maximum)
	// and 3.1 (a number that is itself the bound). libopenapi models that as a DynamicValue, and only the
	// boolean form has a CRD equivalent -- apiextensionsv1.JSONSchemaProps declares them as plain bools.
	//
	// The numeric form is dropped rather than coerced. Treating `exclusiveMinimum: 5` as
	// `exclusiveMinimum: true` would silently attach the bound to whatever `minimum` happens to say, or to
	// nothing at all, which changes which values the API server accepts. Logged rather than silently
	// ignored, since an unenforced constraint that reads as declared is the whole subject of #249.
	if s.ExclusiveMinimum != nil {
		if s.ExclusiveMinimum.IsA() {
			domainSchema.ExclusiveMinimum = s.ExclusiveMinimum.A
		} else {
			log.Printf("exclusiveMinimum is declared in the OAS 3.1 numeric form, which a CRD cannot express; it is not enforced")
		}
	}
	if s.ExclusiveMaximum != nil {
		if s.ExclusiveMaximum.IsA() {
			domainSchema.ExclusiveMaximum = s.ExclusiveMaximum.A
		} else {
			log.Printf("exclusiveMaximum is declared in the OAS 3.1 numeric form, which a CRD cannot express; it is not enforced")
		}
	}

	// MaxProperties handling
	// as per JSON Schema spec (any non-negative integer).
	if s.MaxProperties != nil {
		domainSchema.MaxProperties = int(*s.MaxProperties)
	}

	// Format handling
	// Preserve the format on the domain schema so numeric widths (notably int64)
	// survive into the generated JSON schema / CRD instead of being downgraded to
	// int32. The format is also appended to the description for additional context
	// (e.g. for string formats that are not emitted as a JSON Schema "format").
	if s.Format != "" {
		domainSchema.Format = s.Format
		domainSchema.Description = appendFormatToDescription(domainSchema.Description, s.Format)
	}

	// Properties handling
	if s.Properties != nil {
		domainSchema.Properties = make([]Property, 0, s.Properties.Len())
		for pair := s.Properties.First(); pair != nil; pair = pair.Next() {
			domainSchema.Properties = append(domainSchema.Properties, Property{
				Name:   pair.Key(),
				Schema: convertLibopenapiSchemaWithVisited(ctx, pair.Value(), guard, visited, depth+1),
			})
		}
	}

	// Items handling (OAS 3.0 / 3.1)
	if s.Items != nil {
		switch {
		case s.Items.IsA():
			// Single schema (OAS 3.0 style or OAS 3.1 single schema)
			domainSchema.Items = convertLibopenapiSchemaWithVisited(ctx, s.Items.A, guard, visited, depth+1)

		case s.Items.IsB():
			// OAS 3.1 tuple validation: array of schemas
			// Currently not handled in `oasgen-provider`
			log.Print("Warning: array of schemas in 'items' is not currently handled")
		default:
			log.Print("Warning: Unknown 'items' type")
		}
	}

	// AllOf handling
	// At this time we only handle the recursive conversion of allOf schemas.
	// We do not merge properties or other attributes from allOf into the parent schema.
	// That will be handled later in the processing pipeline (in helpers.go).
	if len(s.AllOf) > 0 {
		domainSchema.AllOf = make([]*Schema, 0, len(s.AllOf))
		for _, allOfProxy := range s.AllOf {
			domainSchema.AllOf = append(domainSchema.AllOf, convertLibopenapiSchemaWithVisited(ctx, allOfProxy, guard, visited, depth+1))
		}
	}

	return domainSchema
}

// appendFormatToDescription appends the format information to the description string.
// If the format is empty, it returns the original description unchanged.
// This is a simple utility to enhance schema descriptions with format details.
func appendFormatToDescription(description, format string) string {
	if format == "" {
		return description
	}
	return fmt.Sprintf("%s (format: %s)", description, format)
}

// Note: function currently not used
func convertToLibopenapiSchema(schema *Schema) *base.Schema {
	if schema == nil {
		return nil
	}

	libSchema := &base.Schema{
		Type:        schema.Type,
		Description: schema.Description,
		Required:    schema.Required,
	}

	if len(schema.Properties) > 0 {
		libSchema.Properties = orderedmap.New[string, *base.SchemaProxy]()
		for _, prop := range schema.Properties {
			libSchema.Properties.Set(prop.Name, base.CreateSchemaProxy(convertToLibopenapiSchema(prop.Schema)))
		}
	}

	if schema.Items != nil {
		libSchema.Items = &base.DynamicValue[*base.SchemaProxy, bool]{
			A: base.CreateSchemaProxy(convertToLibopenapiSchema(schema.Items)),
		}
	}

	for _, allOfSchema := range schema.AllOf {
		libSchema.AllOf = append(libSchema.AllOf, base.CreateSchemaProxy(convertToLibopenapiSchema(allOfSchema)))
	}

	return libSchema
}
