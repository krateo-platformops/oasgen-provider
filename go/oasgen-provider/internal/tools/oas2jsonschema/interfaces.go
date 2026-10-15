package oas2jsonschema

// Parser defines the interface for parsing an OpenAPI specification.
type Parser interface {
	Parse(content []byte) (OASDocument, error)
}

// OASDocument defines the contract for accessing an OpenAPI specification.
type OASDocument interface {
	FindPath(path string) (PathItem, bool)
	SecuritySchemes() []SecuritySchemeInfo
	// Version returns the OAS document's info.version, or "" when absent. It is the source of the
	// target CRD's API version (normalized to a legal k8s version name before use).
	Version() string
}

// PathItem defines the contract for a single API path.
type PathItem interface {
	// GetOperations returns the path's operations, each already carrying the path item's own
	// shared parameters. See Operation.GetParameters for why the merge happens here and not at
	// the call sites.
	GetOperations() map[string]Operation
}

// Operation defines the contract for a single API operation.
type Operation interface {
	// GetParameters returns EVERY parameter that applies to this operation: the ones it declares
	// itself, plus the ones its path item declares for all of its operations (#197). An
	// operation-level parameter overrides a path-item one with the same name and location, per the
	// OAS override rule.
	//
	// The merge is the adapter's job rather than each caller's because the callers -- the spec
	// builder and the configuration-field extractor -- both want the applicable set, and a merge
	// repeated at each call site is a merge that can drift. A caller that reads only what the
	// operation literally declares generates a CRD that cannot bind its own URL.
	GetParameters() []ParameterInfo
	GetRequestBody() RequestBodyInfo    // There is only one request body per operation.
	GetResponses() map[int]ResponseInfo // The keys are HTTP status codes and therefore there could be multiple responses.
}
