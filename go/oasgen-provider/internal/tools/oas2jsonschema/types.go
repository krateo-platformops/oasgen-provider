package oas2jsonschema

import (
	"time"

	rtv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
)

// GeneratorConfig holds configuration options for the schema generator.
type GeneratorConfig struct {
	AcceptedMIMETypes        []string
	SuccessCodes             []int
	IncludeIdentifiersInSpec bool
	MaxRecursionDepth        int
	MaxRecursionNodes        int32
	RecursionTimeout         time.Duration
}

// DefaultGeneratorConfig returns a new GeneratorConfig with default values.
func DefaultGeneratorConfig() *GeneratorConfig {
	return &GeneratorConfig{
		AcceptedMIMETypes:        []string{"application/json"},
		SuccessCodes:             []int{200, 201},
		IncludeIdentifiersInSpec: false,
		MaxRecursionDepth:        50,
		MaxRecursionNodes:        5000,
		RecursionTimeout:         30 * time.Second,
	}
}

// ResourceConfig holds the necessary configuration extracted from a
// source (like a RestDefinition) to guide the schema generation process along with
// the OAS specification.
type ResourceConfig struct {
	Verbs                  []Verb
	Identifiers            []string
	AdditionalStatusFields []string
	ConfigurationFields    []ConfigurationField
	ExcludedSpecFields     []string
}

type ConfigurationField struct {
	FromOpenAPI        FromOpenAPI
	FromRestDefinition FromRestDefinition
}

type FromOpenAPI struct {
	Name string
	In   string // "query", "path", "header", "cookie" (TODO: add validation for this)
}

type FromRestDefinition struct {
	Actions []string
}

// Verb defines a specific API operation (action, method, path).
type Verb struct {
	Action string
	Method string
	Path   string
	// ItemsPath names the envelope property holding the collection, for a findby whose response is not a
	// bare array. Empty means "infer", which succeeds only when the inference is unambiguous.
	ItemsPath string
	// FieldMapping carries the unified request/response field mappings declared for this verb.
	// The generation layer consumes the response-direction entries (InResponse set) to resolve the type
	// of relocated status fields against the response schema; request-direction entries are carried for
	// completeness but are not used during generation.
	FieldMapping []FieldMappingEntry
}

// FieldMappingEntry is a library-agnostic representation of a single RestDefinition fieldMapping entry,
// carrying only what the generation layer needs: the API-side anchor, the CR-side destination, and the
// kind of value transform (if any). It intentionally omits the transform payload (alias pairs or jq
// source), which is runtime-only and irrelevant to schema generation.
type FieldMappingEntry struct {
	InPath           string
	InQuery          string
	InBody           string
	InResponse       string
	InCustomResource string
	// ValueMappingType is "" (no transform, type resolvable from the source), "alias" (a statically-typed
	// string/enum remap), or "jq" (an opaque program whose output type is not statically analyzable).
	ValueMappingType string
}

// --- Library-Agnostic Domain Models ---

// Schema is a library-agnostic representation of a JSON Schema Object, which is used
// within the OpenAPI specification to define the structure of data payloads.
// It is not a representation of the entire OpenAPI document itself.
// Potentially, this struct could be modified to include more fields in the future.
// It is the domainSchema defined in this domain (oas2jsonschema).
type Schema struct {
	Type                 []string // OAS 3.1 allows multiple types (e.g., ["string", "null"])
	Description          string
	Properties           []Property // Using a slice to preserve order of properties (TODO: consider using a map)
	Items                *Schema    // For array types, this defines the schema of items in the array
	AllOf                []*Schema
	Required             []string
	Default              interface{} // Default value for the schema
	Enum                 []interface{}
	AdditionalProperties *AdditionalProperties
	MaxProperties        int
	Format               string                 // JSON Schema "format"; numeric formats (int32/int64/float/double) are emitted into the generated schema, all formats are also added to the description
	Extensions           map[string]interface{} // Currently not used but can hold custom extensions
}

// AdditionalProperties is JSON Schema §5.18 in EITHER of its two forms; nil means the keyword is absent.
//
// Only the boolean form used to survive the OAS conversion, so a typed free-form map
// (additionalProperties: {type: string}, i.e. map[string]string) reached crdgen as nothing at all and the
// generated CRD lost both the type and its validation. crdgen has always been able to represent the object
// form — its own AdditionalProperties unmarshals from either a bool or a nested schema — so the value was
// being discarded on this side, before it ever got there.
//
// Schema wins when set: the two forms are mutually exclusive in the source document, and a value schema is
// strictly more informative than "true".
type AdditionalProperties struct {
	// Bool is the boolean form: true permits any additional property, false forbids them.
	Bool bool
	// Schema is the object form: the schema every additional value must satisfy.
	Schema *Schema
}

// IsSchema reports whether this is the object (typed-map) form.
func (a *AdditionalProperties) IsSchema() bool { return a != nil && a.Schema != nil }

// Property represents a single key-value pair in a schema's properties.
// Using a slice of these preserves order.
type Property struct {
	Name   string
	Schema *Schema
}

// SecuritySchemeType defines the type of a security scheme (e.g., http, apiKey).
type SecuritySchemeType string

// Source: https://swagger.io/docs/specification/v3_0/authentication/
const (
	SchemeTypeHTTP          SecuritySchemeType = "http"
	SchemeTypeAPIKey        SecuritySchemeType = "apiKey"        // Currently not supported
	SchemeTypeOAuth2        SecuritySchemeType = "oauth2"        // Currently not supported
	SchemeTypeOpenIDConnect SecuritySchemeType = "openIdConnect" // Currently not supported
)

// SecuritySchemeInfo is a library-agnostic representation of a security scheme.
// It mirrors the structure of an OpenAPI security scheme.
// In this Go code, it is a "sum type" that captures different security scheme types.
// The 'Type' field is the high-level category (e.g., 'http', 'apiKey', 'oauth2', 'openIdConnect').
// The 'Scheme' field is a sub-detail used only when Type is 'http' (e.g., 'basic', 'bearer').
// Other fields like 'In' and 'ParamName' are used for other types (e.g., 'apiKey').
type SecuritySchemeInfo struct {
	Name      string
	Type      SecuritySchemeType
	Scheme    string // e.g., "basic", "bearer"
	In        string // e.g., "header", "query"
	ParamName string // The name of the header or query parameter (for apiKey)
}

// ParameterInfo is a library-agnostic representation of an API parameter.
type ParameterInfo struct {
	Name        string
	In          string
	Description string
	Required    bool
	Schema      *Schema
}

// RequestBodyInfo is a library-agnostic representation of a request body.
// The Go type name reflects the OpenAPI spec's 'requestBody' object
type RequestBodyInfo struct {
	Content map[string]*Schema
}

// ResponseInfo is a library-agnostic representation of a response.
// The Go type name reflects the OpenAPI spec's single response object under the 'responses' map.
type ResponseInfo struct {
	Content map[string]*Schema
}

// GenerationResult holds the output of the schema generation process.
type GenerationResult struct {
	SpecSchema          []byte
	StatusSchema        []byte
	ConfigurationSchema []byte
	GenerationWarnings  []error
	ValidationWarnings  []error
	// SkippedSecuritySchemes names security schemes that could not be expressed, as
	// "<name> (type: <type>, in: <in>)". Separate from GenerationWarnings because it needs acting on rather
	// than logging: when it is non-empty and no auth method was generated, the resource has no way to
	// authenticate at all.
	SkippedSecuritySchemes []string
}

type BasicAuth struct {
	UsernameRef rtv1.SecretKeySelector `json:"usernameRef"`
	PasswordRef rtv1.SecretKeySelector `json:"passwordRef"`
}

type BearerAuth struct {
	TokenRef rtv1.SecretKeySelector `json:"tokenRef"`
}

// APIKeyAuth is the credential shape for an OAS `type: apiKey, in: header` security scheme: a value sent
// verbatim in a header the document names.
//
// Header and ValuePrefix live here, on the Configuration CR, rather than being derived at request time,
// because rest-dynamic-controller resolves authentication BEFORE the OAS is parsed — its definitiongetter
// imports no OpenAPI library and the document model is built later, on the client. The declared header name
// therefore has to travel oasgen -> CRD -> CR -> RDC like every other OAS-derived fact in this stack.
type APIKeyAuth struct {
	// TokenRef points at the Secret holding the credential VALUE ONLY, with no prefix. Keeping the wire
	// format out of the Secret means a rotation path (ESO, a token endpoint) writes the raw credential and
	// never has to know about HTTP framing.
	TokenRef rtv1.SecretKeySelector `json:"tokenRef"`
	// Header is the header the credential is sent in, defaulted from the security scheme's declared name
	// when the document is unambiguous. It is a real field rather than a hidden constant so the value the
	// generator derived is visible in the CR — which is what someone debugging a 401 needs.
	Header string `json:"header"`
	// ValuePrefix is prepended to the credential on the wire. Empty by default: OAS `apiKey` means "send
	// this value", and there is no prefix concept in the specification.
	//
	// Set it to "Bearer " — INCLUDING THE TRAILING SPACE — for APIs that declare apiKey with an
	// Authorization header but expect bearer framing. Without the space the wire value is "Bearerxyz",
	// which fails identically to a wrong credential.
	//
	// It is deliberately NOT defaulted to "Bearer " on an Authorization header: a Secret already holding
	// the full header value would then silently become "Bearer Bearer ...".
	// +optional
	ValuePrefix string `json:"valuePrefix,omitempty"`
}

// deepCopy creates a deep copy of the Schema.
func (s *Schema) deepCopy() *Schema {
	// Initialize a map to track visited schemas to handle circular references.
	visited := make(map[*Schema]*Schema)
	return s.deepCopyRec(visited)
}

func (s *Schema) deepCopyRec(visited map[*Schema]*Schema) *Schema {
	if s == nil {
		return nil
	}

	// If we have already copied this schema, return the existing copy to break the cycle.
	if copied, ok := visited[s]; ok {
		return copied
	}

	// Create a new schema and register it in the visited map before recursing.
	newSchema := &Schema{}
	visited[s] = newSchema

	// Copy scalar fields and slices of basic types.
	if s.Type != nil {
		newSchema.Type = append([]string{}, s.Type...)
	}
	newSchema.Description = s.Description
	if s.Required != nil {
		newSchema.Required = append([]string{}, s.Required...)
	}

	// Note: Default and Enum are shallow-copied. This is an accepted limitation
	// as they are expected to contain primitive types.
	newSchema.Default = s.Default
	if s.AdditionalProperties != nil {
		ap := &AdditionalProperties{Bool: s.AdditionalProperties.Bool}
		if s.AdditionalProperties.Schema != nil {
			ap.Schema = s.AdditionalProperties.Schema.deepCopyRec(visited)
		}
		newSchema.AdditionalProperties = ap
	}
	newSchema.MaxProperties = s.MaxProperties
	newSchema.Format = s.Format

	if s.Enum != nil {
		newSchema.Enum = make([]interface{}, len(s.Enum))
		copy(newSchema.Enum, s.Enum)
	}

	// Recursively copy nested schemas, passing the visited map along.
	if s.Items != nil {
		newSchema.Items = s.Items.deepCopyRec(visited)
	}

	if s.Properties != nil {
		newSchema.Properties = make([]Property, len(s.Properties))
		for i, p := range s.Properties {
			var copiedSchema *Schema
			if p.Schema != nil {
				copiedSchema = p.Schema.deepCopyRec(visited)
			}
			newSchema.Properties[i] = Property{
				Name:   p.Name,
				Schema: copiedSchema,
			}
		}
	}

	if s.AllOf != nil {
		newSchema.AllOf = make([]*Schema, len(s.AllOf))
		for i, allOfSchema := range s.AllOf {
			newSchema.AllOf[i] = allOfSchema.deepCopyRec(visited)
		}
	}

	return newSchema
}
