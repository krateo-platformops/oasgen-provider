package v1alpha1

import (
	rtv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Pagination defines the pagination strategy for a "findby" action.
// Currently, only 'continuationToken' is supported.
// +kubebuilder:validation:XValidation:rule="self.type == 'continuationToken' ? has(self.continuationToken) : true",message="continuationToken configuration must be provided when type is 'continuationToken'"
type Pagination struct {
	// Type specifies the pagination strategy. Currently, only 'continuationToken' is supported.
	// +kubebuilder:validation:Enum=continuationToken
	// +required
	Type string `json:"type"`
	// Configuration for 'continuationToken' pagination. Required if type is 'continuationToken'.
	// +optional
	ContinuationToken *ContinuationTokenConfig `json:"continuationToken,omitempty"`

	// (Future) Configuration for 'pageNumber' pagination.
	// +optional
	//PageNumber *PageNumberConfig `json:"pageNumber,omitempty"`

	// (Future) Configuration for 'offset' pagination.
	// +optional
	//Offset *OffsetConfig `json:"offset,omitempty"`
}

// ContinuationTokenConfig holds the specific settings for token-based pagination.
type ContinuationTokenConfig struct {
	// Request: defines how to include the pagination token in the API request.
	// +required
	Request ContinuationTokenRequest `json:"request"`
	// Response: defines how to extract the pagination token from the API response.
	// +required
	Response ContinuationTokenResponse `json:"response"`
}

// ContinuationTokenRequest defines how to include the pagination token in the API request.
type ContinuationTokenRequest struct {
	// Where the token is located: "query", "header" or "body". Currently, only "query" is supported.
	// +kubebuilder:validation:Enum=query
	// +required
	TokenIn string `json:"tokenIn"`
	// The path or name of the query parameter, header, or body field.
	// For query parameters and headers, this is simply the name.
	// For body fields, this should be a JSON path.
	// +required
	TokenPath string `json:"tokenPath"`
}

// ContinuationTokenResponse defines how to extract the pagination token from the API response.
type ContinuationTokenResponse struct {
	// Where the token is located: "header" or "body". Currently, only "header" is supported.
	// +kubebuilder:validation:Enum=header
	// +required
	TokenIn string `json:"tokenIn"`
	// The path or name of the header or body field.
	// For headers, this is simply the name.
	// For body fields, this should be a JSON path.
	// +required
	TokenPath string `json:"tokenPath"`
}

// PageNumberConfig is a placeholder for future page number pagination settings.
//type PageNumberConfig struct{}

// OffsetConfig is a placeholder for future offset pagination settings.
//type OffsetConfig struct{}

// RequestFieldMappingItem defines a single mapping from a path parameter, query parameter or body field
// to a field in the Custom Resource.
// +kubebuilder:validation:XValidation:rule="(has(self.inPath) ? 1 : 0) + (has(self.inQuery) ? 1 : 0) + (has(self.inBody) ? 1 : 0) == 1",message="Either inPath, inQuery or inBody must be set, but not more than one"
type RequestFieldMappingItem struct {
	// InPath defines the name of the path parameter to be mapped.
	// Only one of 'inPath', 'inQuery' or 'inBody' can be set.
	// +optional
	InPath string `json:"inPath,omitempty"`
	// InQuery defines the name of the query parameter to be mapped.
	// Only one of 'inPath', 'inQuery' or 'inBody' can be set.
	// +optional
	InQuery string `json:"inQuery,omitempty"`
	// InBody defines the name of the body parameter to be mapped.
	// Only one of 'inPath', 'inQuery' or 'inBody' can be set.
	// +optional
	InBody string `json:"inBody,omitempty"`
	// InCustomResource defines the JSONPath to the field within the Custom Resource that holds the value.
	// For example: 'spec.name' or 'status.metadata.id'.
	// Note: potentially we could add validation to ensure this is a valid path (e.g., starts with 'spec.' or 'status.').
	// Currently, no validation is enforced on the content of this field.
	// +kubebuilder:validation:Required
	InCustomResource string `json:"inCustomResource"`
}

// FieldMappingItem defines a single unified mapping entry: it relocates a value between the Custom
// Resource and the external API (request OR response direction) and optionally transforms the value as
// it crosses that boundary. It generalizes RequestFieldMappingItem (which is retained, deprecated, for
// backward compatibility): the request anchors inPath/inQuery/inBody keep their existing meaning, and a
// new inResponse anchor selects a field of the API response body to be normalized into the CR-facing
// shape before status population and drift comparison.
//
// Exactly one API-side anchor must be set. The anchor kind implies the direction:
// inPath/inQuery/inBody => request, inResponse => response.
// +kubebuilder:validation:XValidation:rule="(has(self.inPath)?1:0)+(has(self.inQuery)?1:0)+(has(self.inBody)?1:0)+(has(self.inResponse)?1:0) == 1",message="exactly one of inPath, inQuery, inBody or inResponse must be set"
// +kubebuilder:validation:XValidation:rule="!has(self.resolver) || has(self.inPath) || has(self.inQuery) || has(self.inBody)",message="resolver is only valid on a request-direction entry (inPath, inQuery or inBody)"
type FieldMappingItem struct {
	// InPath selects a REQUEST path parameter (request direction).
	// Only one of 'inPath', 'inQuery', 'inBody' or 'inResponse' can be set.
	// +optional
	InPath string `json:"inPath,omitempty"`
	// InQuery selects a REQUEST query parameter (request direction).
	// Only one of 'inPath', 'inQuery', 'inBody' or 'inResponse' can be set.
	// +optional
	InQuery string `json:"inQuery,omitempty"`
	// InBody selects a REQUEST body field (request direction).
	// Only one of 'inPath', 'inQuery', 'inBody' or 'inResponse' can be set.
	// +optional
	InBody string `json:"inBody,omitempty"`
	// InResponse selects a RESPONSE body field by JSONPath (response direction).
	// The value found here is transformed (if valueMapping is set) and relocated to the CR-domain
	// destination given by inCustomResource, so that status population and drift comparison operate on the
	// CR-domain shape.
	// Only one of 'inPath', 'inQuery', 'inBody' or 'inResponse' can be set.
	// +optional
	InResponse string `json:"inResponse,omitempty"`
	// InCustomResource is the JSONPath to the field within the Custom Resource, e.g. 'spec.permission' or
	// 'status.metadata.id'. For request entries it is the SOURCE of the value; for response entries it is
	// the CR-domain DESTINATION whose leaf name and parent path determine where the value lands.
	//
	// Array elements can be addressed either by position ('spec.credentials[0].value') or, preferably, by
	// content with a predicate ('spec.credentials[?type=password].value'), which selects the single element
	// whose given field equals the given value. A predicate is shape-independent: it keeps targeting the
	// right element when the user lists the array in a different order, where a positional index would
	// silently address a different one. The predicate must match exactly one element — matching several is
	// an error rather than a silent first-wins, and on a request body it can only address an element that
	// already exists (it never invents one). Same syntax applies to inBody and to the resolver's
	// nameFromCustomResource/keyFromCustomResource.
	// +optional
	InCustomResource string `json:"inCustomResource,omitempty"`
	// ValueMapping optionally transforms the value as it crosses the CR<->API boundary.
	// +optional
	ValueMapping *ValueMapping `json:"valueMapping,omitempty"`
	// Resolver, when set, sources the field's value from something other than a literal read of
	// inCustomResource — currently a secretRef, substituting a Kubernetes Secret's value. Applied
	// BEFORE valueMapping, so a resolved value may still be alias/jq-transformed afterward. Valid
	// only on request-direction entries (inPath/inQuery/inBody set).
	// +optional
	Resolver *FieldResolver `json:"resolver,omitempty"`
	// DefaultIfAbsent, for a response entry (inResponse), supplies the value to inject at the CR-domain
	// destination when the API omits the source field entirely. This canonicalizes an absent field into a
	// known default so status population and drift comparison converge — e.g. an API that omits a boolean
	// object when it is false. It is an arbitrary JSON value (scalar, object or array). Ignored when the
	// source field is present, and ignored for request-direction entries.
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	DefaultIfAbsent *apiextensionsv1.JSON `json:"defaultIfAbsent,omitempty"`
}

// FieldResolver is the primitive behind issue #31 (secretRef). It is deliberately kept as an extensible
// discriminated union (type + a per-kind struct) rather than collapsed into SecretRef alone, so a future
// resolver kind is an additive change.
//
// An apiLookup kind shipped briefly in 0.12.0 and was removed in 0.15.0 without ever having a user. It
// resolved an alias to an id by calling another action in the SAME RestDefinition, but its Action had to
// name one of the five CRUD verbs (the VerbsDescription.Action enum), so an auxiliary lookup endpoint could
// not be declared and cross-resource lookup — the reason it was built, see
// krateo-rest-dynamic-controller#30 — was inexpressible. The case it appeared to serve (resolving a
// server-generated id) is already covered by additionalStatusFields + inCustomResource: status.id. Do not
// reintroduce it in that shape: #30 carries a design for named lookups with client-side matching.
// +kubebuilder:validation:XValidation:rule="self.type == 'secretRef' ? has(self.secretRef) : true",message="secretRef must be set when type is 'secretRef'"
type FieldResolver struct {
	// Type selects the resolver kind.
	// +kubebuilder:validation:Enum=secretRef
	// +required
	Type string `json:"type"`
	// SecretRef substitutes a Kubernetes Secret's value for the field (used when type is 'secretRef'). The
	// Secret is always read from the Custom Resource instance's own namespace — there is no field to name a
	// different one.
	// +optional
	SecretRef *SecretRefResolver `json:"secretRef,omitempty"`
}

// SecretRefResolver substitutes a Kubernetes Secret's value for the field. The Secret is always read from
// the Custom Resource instance's own namespace (no namespace field is exposed, by design: this forecloses
// a cross-namespace secret reference at the type level rather than relying on a runtime check).
type SecretRefResolver struct {
	// NameFromCustomResource is a JSONPath into the Custom Resource yielding the Secret's name.
	// +required
	NameFromCustomResource string `json:"nameFromCustomResource"`
	// KeyFromCustomResource is a JSONPath into the Custom Resource yielding the key within the Secret's data.
	// +required
	KeyFromCustomResource string `json:"keyFromCustomResource"`
}

// ValueMapping declares a value transform applied to a FieldMappingItem. Exactly one tier is configured:
// Tier 1 ('alias') is a finite, self-documenting set of bidirectional CR<->API value pairs; Tier 2 ('jq')
// is a gojq program for the transforms the alias primitive cannot express (structural unwrap, conditional
// or non-bijective mapping, null<->sentinel, arithmetic, string surgery). Bitwise/multi-call logic stays
// out of scope (plugin territory).
// +kubebuilder:validation:XValidation:rule="self.type == 'alias' ? has(self.aliases) : true",message="aliases must be set when type is 'alias'"
// +kubebuilder:validation:XValidation:rule="self.type == 'jq' ? has(self.jq) : true",message="jq must be set when type is 'jq'"
type ValueMapping struct {
	// Type selects the transform tier.
	// +kubebuilder:validation:Enum=alias;jq
	// +required
	Type string `json:"type"`
	// Aliases is an explicit set of bidirectional CR<->API value pairs (used when type is 'alias').
	// On the request the CR value is rewritten to its apiValue; on the response the apiValue is rewritten
	// back to the CR value; any value without a matching pair passes through unchanged.
	// +optional
	Aliases []ValueAlias `json:"aliases,omitempty"`
	// JQ is a gojq program (used when type is 'jq'), supplied inline or as a referenced .jq module.
	// The program is one-directional: for a round-tripping field, write the inverse program in the
	// opposite-direction entry.
	// +optional
	JQ *JQProgram `json:"jq,omitempty"`
}

// ValueAlias is a single bidirectional CR<->API value pair, e.g. {customResourceValue: read, apiValue: pull}.
type ValueAlias struct {
	// CustomResourceValue is the value as expressed in the Custom Resource (CR domain).
	// +required
	CustomResourceValue string `json:"customResourceValue"`
	// APIValue is the corresponding value as expressed by the external API (API domain).
	// +required
	APIValue string `json:"apiValue"`
}

// JQProgram is a gojq program supplied EITHER inline OR as a reference to a self-contained .jq module
// asset. Exactly one of Inline or Ref is set. It is the single type used everywhere jq is accepted:
// per-field (ValueMapping.JQ) and document-level (VerbsDescription.RequestTransform/ResponseTransform).
// +kubebuilder:validation:XValidation:rule="has(self.inline) != has(self.ref)",message="exactly one of inline or ref must be set"
// +kubebuilder:validation:XValidation:rule="!has(self.entrypoint) || has(self.ref)",message="entrypoint is only valid together with ref"
type JQProgram struct {
	// Inline is a gojq source literal. Best for short, single-use expressions.
	// +optional
	Inline string `json:"inline,omitempty"`
	// Ref references a self-contained .jq module asset, using the SAME URI scheme as spec.oasPath:
	//   configmap://<namespace>/<name>/<key>   |   http(s)://<url>
	// +optional
	// +kubebuilder:validation:Pattern=`^(configmap:\/\/([a-z0-9-]+)\/([a-z0-9-]+)\/([a-zA-Z0-9._-]+)|https?:\/\/\S+)$`
	Ref string `json:"ref,omitempty"`
	// Entrypoint is the jq function defined in the referenced module to invoke, e.g. "normalize".
	// If empty, the whole module body is executed as the program. Only meaningful together with ref.
	// +optional
	Entrypoint string `json:"entrypoint,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self.action == 'findby' || !has(self.identifiersMatchPolicy)",message="identifiersMatchPolicy can only be set for 'findby' actions"
// +kubebuilder:validation:XValidation:rule="self.action == 'findby' || !has(self.pagination)",message="pagination can only be set for 'findby' actions"
type VerbsDescription struct {
	// Name of the action to perform when this api is called [create, update, get, delete, findby]
	// +kubebuilder:validation:Enum=create;update;get;delete;findby
	// +required
	Action string `json:"action"`
	// Method: the http method to use [GET, POST, PUT, DELETE, PATCH]
	// +kubebuilder:validation:Enum=GET;POST;PUT;DELETE;PATCH
	// +required
	Method string `json:"method"`
	// Path: the path to the api - has to be the same path as the one in the OAS file you are referencing
	// +required
	Path string `json:"path"`
	// RequestFieldMapping provides explicit mapping from API parameters (path, query, or body)
	// to fields in the Custom Resource.
	//
	// Deprecated: use FieldMapping instead. RequestFieldMapping is request-direction only and carries no
	// value transform; it is retained for backward compatibility and each entry is treated as an
	// equivalent request-direction FieldMappingItem at load time. It will be removed after a migration window.
	// +optional
	RequestFieldMapping []RequestFieldMappingItem `json:"requestFieldMapping,omitempty"`
	// FieldMapping provides unified request/response value relocation and optional per-field value
	// transforms (alias or jq). It supersedes RequestFieldMapping: request entries (inPath/inQuery/inBody)
	// behave as before, and response entries (inResponse) normalize the observed body into the CR-domain
	// shape at the reconcile chokepoint, before status population and drift comparison. Being reconcile
	// behavior rather than CRD shape, it is intentionally mutable.
	// +optional
	FieldMapping []FieldMappingItem `json:"fieldMapping,omitempty"`
	// RequestTransform is a whole-document gojq program applied to the assembled request body immediately
	// before it is sent — the document-scoped sibling of a per-field jq valueMapping, mirroring
	// ResponseTransform. Input '.' is the entire body and the single output replaces it.
	//
	// It runs AFTER the per-field fieldMapping entries have composed the body, so the program sees the
	// finished article. That is the inverse of ResponseTransform, which runs BEFORE per-field mapping —
	// both orderings exist so the document program always gets the API-shaped view.
	//
	// Applies only where a body is sent (create/update/delete); on a bodyless verb it is a no-op rather
	// than an invented body. A program that fails to compile or run fails the reconcile rather than
	// sending a partially transformed body.
	//
	// Requires rest-dynamic-controller >= 0.17.0. Between oasgen 0.16.0 and this release the field was
	// REJECTED at admission: it was accepted by the schema and executed by nothing, and rejecting it was
	// preferable to letting it be a silent no-op.
	// +optional
	RequestTransform *JQProgram `json:"requestTransform,omitempty"`
	// ResponseTransform is a whole-document gojq program applied to the raw response body once, at the
	// reconcile chokepoint, before per-field fieldMapping, status population and drift comparison. Input
	// '.' is the entire response body and the single output replaces it. It is the declarative form of a
	// plugin's whole-body response normalizer.
	// +optional
	ResponseTransform *JQProgram `json:"responseTransform,omitempty"`
	// IdentifiersMatchPolicy defines how to match identifiers for the 'findby' action. To be set only for 'findby' actions.
	// If not set, defaults to 'OR'.
	// Possible values are 'AND' or 'OR'.
	// - 'AND': all identifiers must match.
	// - 'OR': at least one identifier must match (the default behavior).
	// +kubebuilder:validation:Enum=AND;OR
	// +optional
	IdentifiersMatchPolicy string `json:"identifiersMatchPolicy,omitempty"`
	// Pagination defines the pagination strategy for 'findby' actions. To be set only for 'findby' actions.
	// If not set, no pagination will be used.
	// +optional
	Pagination *Pagination `json:"pagination,omitempty"`
	// SuccessCodes lists additional HTTP status codes to treat as success for this verb, beyond the 2xx
	// codes declared for the operation in the OAS document. Use it when an API returns a non-standard
	// success code the OAS does not document (e.g. a 201 or 202 that the reconciler would otherwise reject
	// as an invalid status). Values are merged with the OAS-derived success codes, never replacing them.
	// +optional
	SuccessCodes []int `json:"successCodes,omitempty"`
	// Headers is a list of static HTTP headers to inject on every request for this verb, e.g. an API that
	// requires a specific 'Accept' media-type or 'Content-Type' the OAS does not otherwise enforce. Header
	// values are sent verbatim and are not validated against the OAS.
	// +optional
	Headers []HeaderItem `json:"headers,omitempty"`
	// Queries is a list of static query parameters to inject on every request for this verb, e.g. an
	// API that requires a specific 'api-version' the CRD-generating path does not carry. Combined with the
	// per-verb path/method and request fieldMapping, this expresses an alternative endpoint for a verb
	// (e.g. an update routed to a different sub-API with its own api-version). Values are sent verbatim.
	// +optional
	Queries []QueryParam `json:"queries,omitempty"`
	// TolerateCodes lists HTTP status codes that, for this verb, are treated as a successful EMPTY response
	// instead of an error. Use it when a code that would otherwise fail the call actually means "the
	// (sub-)resource is simply empty / not present yet" rather than a real failure — e.g. an API returning
	// 404 for an optional collection with no entries. Use with care: tolerating 404 on a verb whose code
	// genuinely signals a deleted resource would mask that deletion.
	// +optional
	TolerateCodes []int `json:"tolerateCodes,omitempty"`
	// NotFoundCodes lists HTTP status codes that, for this verb, mean the external resource does NOT exist
	// (i.e. are remapped to a not-found result) even though they are not 404. Use it for APIs that signal
	// absence with a non-standard code the reconciler would otherwise treat as an error or as existing —
	// e.g. an existence check that returns 410 Gone or 204 for a missing resource. Intended for get/findby.
	// +optional
	NotFoundCodes []int `json:"notFoundCodes,omitempty"`
	// NotFoundBody is the body-based counterpart of NotFoundCodes: a gojq predicate evaluated against the
	// successful (2xx) observe-response. When it yields true the external resource is treated as NOT
	// existing, so the reconciler creates it. Use it for APIs that signal absence with a 200 body rather
	// than a status code. The program must return a boolean, and its input '.' is the RAW body, whose shape
	// differs by verb:
	//   - get:    the whole GET body — e.g. `.items | length == 0`, `.deleted == true`, `.status == "NOT_FOUND"`.
	//   - findby: the SINGLE matched item (a findby no-match already yields not-found on its own), so write
	//             it against the item — e.g. a tombstone `.status == "deleted"`; a list-shaped predicate is
	//             meaningless here.
	// Intended for get/findby only. It is not evaluated while the resource is Pending (mid async create), and
	// a non-boolean result is a reconcile error.
	// +optional
	NotFoundBody *JQProgram `json:"notFoundBody,omitempty"`
	// Async declares long-running-operation (async) handling for this mutating verb: after the trigger call
	// returns an operation handle, the controller polls an operations endpoint until it reaches a terminal
	// status, turning an asynchronous API into a synchronous reconcile. Set only on create/update/delete.
	// +optional
	Async *AsyncConfig `json:"async,omitempty"`
}

// AsyncConfig declares how a mutating verb's long-running operation is driven to completion.
type AsyncConfig struct {
	// Mode selects how the long-running operation is driven:
	//   - "blocking" (default): the trigger reconcile polls the operation to completion inline (Model A).
	//     Simplest, but occupies a reconcile worker for the duration of the operation.
	//   - "requeue": the trigger reconcile fires the operation, records its handle, and returns; each
	//     subsequent reconcile polls the operation once and requeues until it reaches a terminal status
	//     (Model B). Non-blocking — it never pins a worker — and adds terminal-failure detection to the
	//     otherwise blind "wait for the resource to appear" pending flow. Because the operation is triggered
	//     before its handle is recorded, the trigger must be idempotent (as for blocking mode). requeue
	//     applies to create/update; delete always polls inline, since the finalizer must be held until the
	//     delete operation completes.
	// +kubebuilder:validation:Enum=blocking;requeue
	// +optional
	Mode string `json:"mode,omitempty"`
	// OperationRef: how to extract the operation handle from the trigger response.
	// +required
	OperationRef OperationRef `json:"operationRef"`
	// Poll: the polling endpoint and its terminal semantics.
	// +required
	Poll PollConfig `json:"poll"`
	// PostGet: after terminal success, re-run the resource's get/findby to fetch the final state.
	// +optional
	PostGet bool `json:"postGet,omitempty"`
}

// OperationRef declares how to extract the async operation handle from the trigger response.
type OperationRef struct {
	// In: where the handle is located: "body" (a JSONPath into the trigger response body) or "header".
	// +kubebuilder:validation:Enum=body;header
	// +required
	In string `json:"in"`
	// Path: the JSONPath (for in=body) or header name (for in=header) that carries the handle.
	// +required
	Path string `json:"path"`
	// JQ: optional gojq program to derive the handle from the raw value at Path (e.g. an id from a URL).
	// +optional
	JQ *JQProgram `json:"jq,omitempty"`
}

// PollConfig declares the polling endpoint and the values that mark a terminal outcome.
type PollConfig struct {
	// Method: the HTTP method for the poll call (GET in practice).
	// +kubebuilder:validation:Enum=GET
	// +optional
	Method string `json:"method,omitempty"`
	// Path: the poll endpoint template. Two things must hold together, and both are checked when the
	// RestDefinition is processed rather than left to fail on the first poll:
	//
	//  1. it must contain the literal {operationId} token — that is the parameter name the extracted handle
	//     is bound to in rest-dynamic-controller;
	//  2. it must be an EXACT key of the OAS paths object, because paths are resolved by exact string
	//     lookup.
	//
	// Together they mean the poll endpoint must be declared in the OAS document with a path parameter whose
	// name matches handleParam below.
	// +required
	Path string `json:"path"`
	// HandleParam is the NAME of the path parameter in Path that receives the extracted operation handle.
	// Defaults to "operationId".
	//
	// Despite that default's spelling this is NOT the OAS `operationId` keyword: that identifies an
	// operation definition and is optional, whereas this names a path parameter. An async API is not
	// required to expose anything called operationId — it only has to return a handle somewhere in the
	// trigger response (declared by operationRef) and offer a poll endpoint that accepts it.
	//
	// Set this when the OAS document names the parameter something else, which vendor specs routinely do:
	// Aruba's baremetal API declares .../monitor/{id}, so path: .../monitor/{id} with handleParam: id uses
	// that document unmodified. Before this field existed the document had to be patched to rename the
	// parameter to operationId.
	//
	// Requires rest-dynamic-controller >= 0.18.0; an older controller ignores it and binds to operationId.
	// +optional
	HandleParam string `json:"handleParam,omitempty"`
	// StatusPath: JSONPath to the status field in the poll response.
	// +required
	StatusPath string `json:"statusPath"`
	// SuccessValues: status values that mark terminal success.
	// +kubebuilder:validation:MinItems=1
	// +required
	SuccessValues []string `json:"successValues"`
	// FailureValues: status values that mark terminal failure.
	// +optional
	FailureValues []string `json:"failureValues,omitempty"`
	// IntervalSeconds: delay between polls. Defaults to 1.
	// +optional
	IntervalSeconds int `json:"intervalSeconds,omitempty"`
	// MaxAttempts: maximum number of poll attempts. Defaults to 10.
	// +optional
	MaxAttempts int `json:"maxAttempts,omitempty"`
	// TimeoutSeconds: overall cap on the polling loop. 0 means no explicit cap (bounded by MaxAttempts).
	// +optional
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
}

// HeaderItem is a single static HTTP header injected on every request for a verb.
type HeaderItem struct {
	// Name is the HTTP header name, e.g. 'Accept' or 'Content-Type'.
	// +required
	Name string `json:"name"`
	// Value is the HTTP header value, sent verbatim.
	// +required
	Value string `json:"value"`
}

// QueryParam is a single static query parameter injected on every request for a verb.
type QueryParam struct {
	// Name is the query parameter name, e.g. 'api-version'.
	// +required
	Name string `json:"name"`
	// Value is the query parameter value, sent verbatim.
	// +required
	Value string `json:"value"`
}

// +kubebuilder:validation:XValidation:rule="!(has(self.createApiRef) && has(self.observeApiRef) && !has(self.observeApiRef.notFoundExpr))",message="createApiRef with observeApiRef requires observeApiRef.notFoundExpr, so a create can be triggered when the delegated observe reports the resource absent"
// +kubebuilder:validation:XValidation:rule="!has(self.createApiRef) || self.verbsDescription.exists(v, v.action == 'get' || v.action == 'findby')",message="createApiRef requires a get or findby verb so the controller can verify the create converged (level-based convergence)"
// +kubebuilder:validation:XValidation:rule="!has(self.compareScope) || self.compareScope != 'identifiersAndStatus' || (has(self.identifiers) && size(self.identifiers) > 0) || (has(self.additionalStatusFields) && size(self.additionalStatusFields) > 0)",message="compareScope 'identifiersAndStatus' requires at least one identifier or additionalStatusField to compare against"
type Resource struct {
	// Name: the name of the resource to manage
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Kind is immutable, you cannot change that once the CRD has been generated"
	// +required
	Kind string `json:"kind"`
	// VerbsDescription: the list of verbs to use on this resource
	// +required
	VerbsDescription []VerbsDescription `json:"verbsDescription"`
	// Identifiers: the list of fields to use as identifiers - used to populate the status of the resource
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Identifiers are immutable, you cannot change them once the CRD has been generated"
	// +optional
	Identifiers []string `json:"identifiers,omitempty"`
	// AdditionalStatusFields: the list of fields to use as additional status fields - used to populate the status of the resource
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="AdditionalStatusFields are immutable, you cannot change them once the CRD has been generated"
	// +optional
	AdditionalStatusFields []string `json:"additionalStatusFields,omitempty"`
	// CompareScope selects which fields the drift comparison (Observe) considers when deciding whether the
	// external resource is up to date.
	//   - "fullSpec" (default, also when unset): every spec field is compared against the observed response.
	//   - "identifiersAndStatus": only the fields listed in identifiers + additionalStatusFields are compared.
	//     Fields outside that set no longer trigger updates, so use this ONLY when those fields capture
	//     everything worth reconciling (e.g. all other spec fields are create-only / server-managed). It trades
	//     precision for ergonomics: no per-field responseTransform/fieldMapping is needed to stop false drift on
	//     divergently-shaped response fields. Being reconcile behavior rather than CRD shape, it is mutable.
	// +kubebuilder:validation:Enum=fullSpec;identifiersAndStatus
	// +optional
	CompareScope string `json:"compareScope,omitempty"`
	// ConfigurationFields: the list of fields to use as configuration fields
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="ConfigurationFields are immutable, you cannot change them once the CRD has been generated"
	// +optional
	ConfigurationFields []ConfigurationField `json:"configurationFields,omitempty"`
	// ExcludedSpecFields: the list of fields to exclude from the spec of the generated CRD (for example server-generated technical IDs could be excluded)
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="ExcludedSpecFields are immutable, you cannot change them once the CRD has been generated"
	// +optional
	ExcludedSpecFields []string `json:"excludedSpecFields,omitempty"`
	// ObserveApiRef, when set, delegates the OBSERVE of this resource to a Snowplow RESTAction instead of the
	// get/findby verbs: the rest-dynamic-controller invokes the referenced RESTAction (via snowplow /call,
	// under its own identity) each reconcile — passing the resource's name/namespace/uid and its identifier
	// values (not the whole spec) as request extras — and projects the RESTAction's composed .status into
	// this resource's status (leaving the runtime-managed conditions untouched). It dissolves proxies whose
	// only job is to compose a multi-call observation (read several sub-resources and shape one status). The
	// referenced RESTAction is trusted platform configuration. Being reconcile behavior rather than CRD
	// shape, it is intentionally mutable.
	// +optional
	ObserveApiRef *ApiRef `json:"observeApiRef,omitempty"`
	// CreateApiRef, when set, delegates CREATE of this resource to a Snowplow RESTAction instead of the
	// create verb: the controller invokes the referenced RESTAction (passing the resource's name/namespace/
	// uid and its whole spec — the desired state — as request extras) to run the multi-call provisioning
	// sequence, and projects any composed .status it returns into this resource's status. The RESTAction
	// MUST be idempotent: the controller does not verify per-call success — it re-invokes create every
	// reconcile until Observe reports the resource exists (level-based convergence). This therefore REQUIRES
	// a get/findby verb — OR an observeApiRef whose notFoundExpr reports non-existence — the observe that
	// reports non-existence; with none, the resource is marked Available after a single unverified
	// invocation. Dissolves proxies whose only job is to chain create calls (e.g. create instance -> attach
	// disk -> start).
	// +optional
	CreateApiRef *ApiRef `json:"createApiRef,omitempty"`
	// DeleteApiRef, when set, delegates DELETE of this resource to a Snowplow RESTAction instead of the
	// delete verb: on deletion the controller invokes the referenced RESTAction (the teardown sequence) and
	// holds the finalizer until it succeeds. The RESTAction MUST be idempotent and tolerate an already-gone
	// sub-resource.
	// +optional
	DeleteApiRef *ApiRef `json:"deleteApiRef,omitempty"`
	// UpdateApiRef, when set, delegates UPDATE of this resource to a Snowplow RESTAction instead of the
	// update verb: when Observe reports drift the controller invokes the referenced RESTAction (passing the
	// whole spec — the desired state) to re-apply it. Like create, the RESTAction MUST be idempotent.
	// +optional
	UpdateApiRef *ApiRef `json:"updateApiRef,omitempty"`
}

// ApiRef references a Snowplow RESTAction (templates.krateo.io/v1) that the controller resolves via
// snowplow's /call endpoint under its own authn identity.
type ApiRef struct {
	// Name of the RESTAction to resolve.
	// +required
	Name string `json:"name"`
	// Namespace of the RESTAction.
	// +required
	Namespace string `json:"namespace"`
	// Extras are static key/values merged UNDER the per-instance context (this resource's name, namespace,
	// uid and spec) that the controller passes to snowplow as request extras; the per-instance context wins
	// on conflict. Use them to parameterize the RESTAction (e.g. a fixed endpoint or api-version).
	// +optional
	Extras *apiextensionsv1.JSON `json:"extras,omitempty"`
	// NotFoundExpr (observeApiRef only) is a gojq boolean predicate evaluated against {spec, status}, where
	// status is the RESTAction's composed result. When it returns true the resource is reported as NOT
	// existing, so the controller creates it — this is what lets observeApiRef compose with createApiRef
	// (which otherwise it cannot, because a delegated observe reports existence unconditionally).
	// +optional
	NotFoundExpr *JQProgram `json:"notFoundExpr,omitempty"`
	// UpToDateExpr (observeApiRef only) is a gojq boolean predicate over {spec, status}. When it returns
	// false the resource is reported as drifted, so the controller updates it (composing with updateApiRef).
	// Absent => the resource is assumed up-to-date.
	// +optional
	UpToDateExpr *JQProgram `json:"upToDateExpr,omitempty"`
}

// RestDefinitionSpec is the specification of a RestDefinition.
type RestDefinitionSpec struct {
	// Path to the OpenAPI specification. This value can change over time, for example if the OAS file is updated but be sure to not change the requestbody of the `create` verb.
	// +required
	// - configmap://<namespace>/<name>/<key>
	// - http(s)://<url>
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^(configmap:\/\/([a-z0-9-]+)\/([a-z0-9-]+)\/([a-zA-Z0-9._-]+)|https?:\/\/\S+)$`
	OASPath string `json:"oasPath"`
	// Group: the group of the resource to manage
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="ResourceGroup is immutable, you cannot change that once the CRD has been generated"
	// +required
	ResourceGroup string `json:"resourceGroup"`
	// The resource to manage
	// +required
	Resource Resource `json:"resource"`
}

type ConfigurationField struct {
	FromOpenAPI        FromOpenAPI        `json:"fromOpenAPI"`
	FromRestDefinition FromRestDefinition `json:"fromRestDefinition"`
}

type FromOpenAPI struct {
	Name string `json:"name"`
	In   string `json:"in"` // "query", "path", "header", "cookie"
}

type FromRestDefinition struct {
	// Actions: the list of actions this configuration applies to. Use ["*"] to apply to all actions.
	// +kubebuilder:validation:MinItems=1
	// +required
	Actions []string `json:"actions"`
}

type KindApiVersion struct {
	// APIVersion: the api version of the resource
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`

	// Kind: the kind of the resource
	// +optional
	Kind string `json:"kind,omitempty"`
}

// RestDefinitionStatus is the status of a RestDefinition.
type RestDefinitionStatus struct {
	rtv1.ConditionedStatus `json:",inline"`

	// OASPath: the path to the OAS Specification file.
	OASPath string `json:"oasPath"`

	// Resource: the resource to manage
	// +optional
	Resource KindApiVersion `json:"resource"`

	// Configuration: the configuration of the resource
	// +optional
	Configuration KindApiVersion `json:"configuration"`

	// Digest: the digest of the managed resources
	// +optional
	Digest string `json:"digest,omitempty"`

	// HasSecuritySchemes: whether the OAS document defines security schemes.
	// Cached here so Observe does not need to re-fetch the OAS document on every reconcile.
	// +optional
	HasSecuritySchemes *bool `json:"hasSecuritySchemes,omitempty"`

	// OASHash: a content hash of the resolved OAS document at the last successful Create/Update. Observe
	// re-hashes the referenced OAS each reconcile and treats a change as drift, so an edit to the OAS source
	// (e.g. the referenced ConfigMap) is picked up even when oasPath itself is unchanged.
	// +optional
	OASHash string `json:"oasHash,omitempty"`

	// AuthSecretDigest: a hash of the Secret names (grouped by namespace) currently referenced, across every
	// Configuration CR instance of this RestDefinition's Configuration Kind, by usernameRef/passwordRef/
	// tokenRef. Observe re-collects and re-hashes these references each reconcile and treats a change as
	// drift, so RDC's per-namespace auth-secret RBAC (see AuthSecretRBACNamespaces) is refreshed whenever a
	// Configuration instance starts, stops, or changes which Secret it authenticates with.
	// +optional
	AuthSecretDigest string `json:"authSecretDigest,omitempty"`

	// AuthSecretRBACNamespaces: the namespaces in which a namespace-scoped Role+RoleBinding currently grants
	// RDC's ServiceAccount read access to the auth Secrets collected above. Tracked so a namespace that no
	// longer has any referencing Configuration instance has its now-unneeded RBAC removed, and so RestDefinition
	// delete can tear down every namespace's RBAC without re-listing (possibly already-gone) Configuration instances.
	// +optional
	AuthSecretRBACNamespaces []string `json:"authSecretRBACNamespaces,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,categories={krateo,restdefinition,core}
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="API VERSION",type="string",JSONPath=".status.resource.apiVersion",priority=10
// +kubebuilder:printcolumn:name="KIND",type="string",JSONPath=".status.resource.kind",priority=10
// +kubebuilder:printcolumn:name="OAS PATH",type="string",JSONPath=".status.oasPath",priority=10
// RestDefinition is a RestDefinition type with a spec and a status.
type RestDefinition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RestDefinitionSpec   `json:"spec,omitempty"`
	Status RestDefinitionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// RestDefinitionList is a list of RestDefinition objects.
type RestDefinitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []RestDefinition `json:"items"`
}

// GetCondition of this RestDefinition.
func (mg *RestDefinition) GetCondition(ct rtv1.ConditionType) rtv1.Condition {
	return mg.Status.GetCondition(ct)
}

// SetConditions of this RestDefinition.
func (mg *RestDefinition) SetConditions(c ...rtv1.Condition) {
	mg.Status.SetConditions(c...)
}
