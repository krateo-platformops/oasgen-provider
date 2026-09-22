package getter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/auth"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/jqmodule"
	"github.com/krateo-platformops/unstructured-runtime/pkg/pluralizer"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// ErrDefinitionNotFound is returned by Get when no matching RestDefinition exists for the resource (a
// genuine absence), as distinct from a transient failure to list/read definitions. Callers (e.g. Delete)
// use it to decide whether it is safe to proceed without the definition or must retry.
var ErrDefinitionNotFound = errors.New("no matching RestDefinition found")

// configurationVersion is the fixed API version of every oasgen-generated <Kind>Configuration CRD,
// independent of the managed resource's own (OAS info.version-derived) version — mirrors oasgen's
// resourceVersion constant (internal/controllers/restdefinition/restdefinition.go). The managed resource
// and its Configuration sibling are separate kinds in the same group that can legitimately sit at
// different versions, so the Configuration GVK must never inherit the managed resource's version.
const configurationVersion = "v1alpha1"

// Pagination defines the pagination strategy for a "findby" action.
//
// MIRRORS oasgen-provider's apis/restdefinitions/v1alpha1/types.go. These two structs are the same
// contract decoded twice, and a field present in only one is silently dropped rather than rejected --
// which is how #51 shipped half-working. Change both or neither.
type Pagination struct {
	// Type specifies the pagination strategy: "continuationToken" or "pageNumber".
	Type string `json:"type"`
	// Configuration for 'continuationToken' pagination. Required if type is 'continuationToken'.
	ContinuationToken *ContinuationTokenConfig `json:"continuationToken,omitempty"`
	// Configuration for 'pageNumber' pagination. Required if type is 'pageNumber'.
	PageNumber *PageNumberConfig `json:"pageNumber,omitempty"`
	// (Future) Configuration for 'offset' pagination.
	//Offset *OffsetConfig `json:"offset,omitempty"`
}

// PageNumberConfig holds the settings for page-number pagination (?page=N&per_page=M).
type PageNumberConfig struct {
	// Request: how the page number (and optionally the page size) are sent.
	Request PageNumberRequest `json:"request"`
	// Response: how "another page exists" is recognised. Omitted selects the short-page rule.
	Response *PageNumberResponse `json:"response,omitempty"`
	// MaxPages bounds the walk. Exhausting it is its own outcome, never absence.
	MaxPages int `json:"maxPages"`
}

// PageNumberRequest declares how the page cursor is sent.
type PageNumberRequest struct {
	// Where the page number goes. Only "query" is supported.
	PageIn string `json:"pageIn"`
	// PagePath is the parameter name carrying the page number, e.g. "page".
	PagePath string `json:"pagePath"`
	// StartPage is the number of the first page -- 1 for most APIs, 0 for 0-based ones.
	StartPage int `json:"startPage"`
	// SizeIn / SizePath / PageSize optionally request a page size.
	SizeIn   string `json:"sizeIn,omitempty"`
	SizePath string `json:"sizePath,omitempty"`
	PageSize int    `json:"pageSize,omitempty"`
}

// PageNumberResponse declares how the last page is recognised. At most one mechanism.
type PageNumberResponse struct {
	// Header recognises "more pages exist" by matching a response header.
	Header *PageNumberHeaderSignal `json:"header,omitempty"`
	// Body recognises the end by comparing against a total the API reports.
	Body *PageNumberBodySignal `json:"body,omitempty"`
}

// PageNumberHeaderSignal matches a header to decide whether another page exists.
type PageNumberHeaderSignal struct {
	// Name of the header, e.g. "Link".
	Name string `json:"name"`
	// Matches is a substring whose presence means another page exists, e.g. `rel="next"`.
	Matches string `json:"matches"`
}

// PageNumberBodySignal reads a total from the body and compares it against progress so far.
type PageNumberBodySignal struct {
	// TotalPagesPath is a path to the total number of pages.
	TotalPagesPath string `json:"totalPagesPath,omitempty"`
	// TotalItemsPath is a path to the total number of items. Requires request.pageSize.
	TotalItemsPath string `json:"totalItemsPath,omitempty"`
}

// ContinuationTokenConfig holds the specific settings for token-based pagination.
type ContinuationTokenConfig struct {
	// Request: defines how to include the pagination token in the API request.
	Request ContinuationTokenRequest `json:"request"`
	// Response: defines how to extract the pagination token from the API response.
	Response ContinuationTokenResponse `json:"response"`
}

// ContinuationTokenRequest defines how to include the pagination token in the API request.
type ContinuationTokenRequest struct {
	// Where the token is located: "query", "header" or "body". Currently, only "query" is supported.
	TokenIn string `json:"tokenIn"`
	// The path or name of the query parameter, header, or body field.
	// For query parameters and headers, this is simply the name.
	// For body fields, this should be a JSON path.
	TokenPath string `json:"tokenPath"`
}

// ContinuationTokenResponse defines how to extract the pagination token from the API response.
type ContinuationTokenResponse struct {
	// Where the token is located: "header" or "body". Currently, only "header" is supported.
	TokenIn string `json:"tokenIn"`
	// The path or name of the header or body field.
	// For headers, this is simply the name.
	// For body fields, this should be a JSON path.
	TokenPath string `json:"tokenPath"`
}

// OffsetConfig is a placeholder for future offset pagination settings.
//type OffsetConfig struct{}

// RequestFieldMappingItem defines a single mapping from a path parameter, query parameter or body field
// to a field in the Custom Resource.
type RequestFieldMappingItem struct {
	// InPath defines the name of the path parameter to be mapped.
	// Only one of 'inPath', 'inQuery' or 'inBody' can be set.
	InPath string `json:"inPath,omitempty"`
	// InQuery defines the name of the query parameter to be mapped.
	// Only one of 'inPath', 'inQuery' or 'inBody' can be set.
	InQuery string `json:"inQuery,omitempty"`
	// InBody defines the name of the body parameter to be mapped.
	// Only one of 'inPath', 'inQuery' or 'inBody' can be set.
	InBody string `json:"inBody,omitempty"`
	// InCustomResource defines the JSONPath to the field within the Custom Resource that holds the value.
	// For example: 'spec.name' or 'status.metadata.id'.
	InCustomResource string `json:"inCustomResource"`
}

// FieldMappingItem is the runtime mirror of the RestDefinition FieldMappingItem (unified request/response
// mapping). Exactly one API-side anchor is set: inPath/inQuery/inBody select a request parameter,
// inResponse selects a response body field to be normalized into the CR-domain shape.
//
// Every path here is parsed by internal/tools/pathparsing, so besides dot and quoted-bracket notation it
// also addresses array elements — by position ("credentials[0].value") or, shape-independently, by content
// with a predicate ("credentials[?type=password].value").
type FieldMappingItem struct {
	InPath           string        `json:"inPath,omitempty"`
	InQuery          string        `json:"inQuery,omitempty"`
	InBody           string        `json:"inBody,omitempty"`
	InResponse       string        `json:"inResponse,omitempty"`
	InCustomResource string        `json:"inCustomResource,omitempty"`
	ValueMapping     *ValueMapping `json:"valueMapping,omitempty"`
	// Resolver, when set, sources the field's value from something other than a literal read of
	// InCustomResource. Valid only on request-direction entries (inPath/inQuery/inBody set). Applied
	// BEFORE ValueMapping (a resolved value may still be alias/jq-transformed afterward).
	Resolver *FieldResolver `json:"resolver,omitempty"`
	// DefaultIfAbsent (response entries only) is the JSON value injected at the CR-domain destination when
	// the API omits the inResponse source field entirely.
	DefaultIfAbsent json.RawMessage `json:"defaultIfAbsent,omitempty"`
}

// FieldResolver is the runtime mirror of the RestDefinition FieldResolver (issue #31). secretRef is the
// only kind; an apiLookup kind shipped in 0.12.0 and was removed in 0.15.0 (see oasgen's types.go).
type FieldResolver struct {
	Type      string             `json:"type"`
	SecretRef *SecretRefResolver `json:"secretRef,omitempty"`
}

// SecretRefResolver substitutes a Kubernetes Secret's value for the field. There is no namespace field:
// the Secret is always read from the Custom Resource instance's own namespace.
type SecretRefResolver struct {
	// NameFromCustomResource is a JSONPath into the Custom Resource yielding the Secret's name.
	NameFromCustomResource string `json:"nameFromCustomResource"`
	// KeyFromCustomResource is a JSONPath into the Custom Resource yielding the key within the Secret's data.
	KeyFromCustomResource string `json:"keyFromCustomResource"`
}

// ValueMapping is the runtime mirror of a value transform. Support depends on the tier and the DIRECTION:
// 'alias' applies both ways; 'jq' applies on the response direction only — a request entry carrying a jq
// mapping is skipped outright, so the field never reaches the body. Inline and ref: are equivalent here:
// resolveJQRefs materializes Ref into Inline before anything executes. See the fieldmapping package doc for
// the full matrix, including requestTransform, which is materialized and then never run.
type ValueMapping struct {
	Type    string       `json:"type"`
	Aliases []ValueAlias `json:"aliases,omitempty"`
	JQ      *JQProgram   `json:"jq,omitempty"`
}

// ValueAlias is a single bidirectional CR<->API value pair.
type ValueAlias struct {
	CustomResourceValue string `json:"customResourceValue"`
	APIValue            string `json:"apiValue"`
}

// JQProgram mirrors a gojq program supplied inline or as a module reference.
type JQProgram struct {
	Inline     string `json:"inline,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Entrypoint string `json:"entrypoint,omitempty"`
}

type VerbsDescription struct {
	// Name of the action to perform when this api is called
	Action string `json:"action"`
	// Method: the http method to use [GET, POST, PUT, DELETE, PATCH]
	Method string `json:"method"`
	// Path: the path to the api
	Path string `json:"path"`
	// RequestFieldMapping provides explicit mapping from API parameters (path, query, or body)
	// to fields in the Custom Resource. Deprecated: mirrored for backward compatibility; prefer FieldMapping.
	RequestFieldMapping []RequestFieldMappingItem `json:"requestFieldMapping,omitempty"`
	// FieldMapping is the unified request/response field mapping (see FieldMappingItem). Response-direction
	// entries (inResponse) are applied to the observed body before status population and drift comparison.
	FieldMapping []FieldMappingItem `json:"fieldMapping,omitempty"`
	// RequestTransform / ResponseTransform are whole-document jq programs, both applied by the jq engine:
	// ResponseTransform in fieldmapping.NormalizeResponseBody, RequestTransform in
	// fieldmapping.ApplyRequestTransform (Create/Update/Delete, after the body is assembled).
	RequestTransform  *JQProgram `json:"requestTransform,omitempty"`
	ResponseTransform *JQProgram `json:"responseTransform,omitempty"`
	// IdentifiersMatchPolicy defines how to match identifiers for the 'findby' action. To be set only for 'findby' actions.
	// If not set, defaults to 'OR'.
	// Possible values are 'AND' or 'OR'.
	// - 'AND': all identifiers must match.
	// - 'OR': at least one identifier must match (the default behavior).
	IdentifiersMatchPolicy string `json:"identifiersMatchPolicy,omitempty"`
	// Pagination defines the pagination strategy for 'findby' actions. To be set only for 'findby' actions.
	// If not set, no pagination will be used.
	Pagination *Pagination `json:"pagination,omitempty"`
	// SuccessCodes lists additional HTTP status codes treated as success for this verb, merged with the
	// OAS-declared 2xx codes.
	SuccessCodes []int `json:"successCodes,omitempty"`
	// Headers are static HTTP headers injected on every request for this verb.
	Headers []HeaderItem `json:"headers,omitempty"`
	// Queries are static query parameters injected on every request for this verb.
	Queries []QueryParam `json:"queries,omitempty"`
	// TolerateCodes are HTTP status codes treated as a successful empty response for this verb.
	TolerateCodes []int `json:"tolerateCodes,omitempty"`
	// NotFoundCodes are status codes remapped to a not-found result for this verb.
	NotFoundCodes []int `json:"notFoundCodes,omitempty"`
	// NotFoundBody is a gojq boolean predicate against the successful observe-response; when it returns true
	// the external resource is treated as not existing (body-based counterpart of NotFoundCodes). Its input
	// is the RAW body: for get, the whole GET body (e.g. .items|length==0); for findby, the single matched
	// item (a no-match already yields not-found), e.g. a tombstone .status == "deleted".
	NotFoundBody *JQProgram `json:"notFoundBody,omitempty"`
	// Async declares long-running-operation handling for this mutating verb.
	Async *AsyncConfig `json:"async,omitempty"`
}

// HeaderItem is a single static HTTP header injected on every request for a verb.
type HeaderItem struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// QueryParam is a single static query parameter injected on every request for a verb.
type QueryParam struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// AsyncConfig is the runtime mirror of the RestDefinition async/LRO configuration.
type AsyncConfig struct {
	// Mode is "blocking" (default, Model A: poll to completion inline) or "requeue" (Model B: fire, record
	// the handle, and poll once per reconcile until terminal). requeue applies to create/update; delete
	// always polls inline. See the oasgen CRD field for details.
	Mode         string       `json:"mode,omitempty"`
	OperationRef OperationRef `json:"operationRef"`
	Poll         PollConfig   `json:"poll"`
	PostGet      bool         `json:"postGet,omitempty"`
}

// OperationRef mirrors how to extract the async operation handle from the trigger response.
type OperationRef struct {
	In   string     `json:"in"`
	Path string     `json:"path"`
	JQ   *JQProgram `json:"jq,omitempty"`
}

// PollConfig mirrors the polling endpoint and its terminal semantics.
type PollConfig struct {
	Method string `json:"method,omitempty"`
	Path   string `json:"path"`
	// HandleParam is the NAME of the path parameter in Path that receives the extracted async operation
	// handle. Defaults to "operationId" when empty, which is what every RestDefinition written before this
	// field existed relies on. It has nothing to do with the OAS `operationId` keyword — that identifies an
	// operation definition; this names a path parameter.
	HandleParam     string   `json:"handleParam,omitempty"`
	StatusPath      string   `json:"statusPath"`
	SuccessValues   []string `json:"successValues"`
	FailureValues   []string `json:"failureValues,omitempty"`
	IntervalSeconds int      `json:"intervalSeconds,omitempty"`
	MaxAttempts     int      `json:"maxAttempts,omitempty"`
	TimeoutSeconds  int      `json:"timeoutSeconds,omitempty"`
}

type Resource struct {
	// Name: the name of the resource to manage
	Kind string `json:"kind"`
	// Identifiers: the list of fields to use as identifiers
	Identifiers []string `json:"identifiers"`
	// AdditionalStatusFields: the list of additional status fields to use
	AdditionalStatusFields []string `json:"additionalStatusFields"`
	// CompareScope selects which fields the drift comparison considers: "" / "fullSpec" compares every spec
	// field against the observed response; "identifiersAndStatus" compares ONLY identifiers +
	// additionalStatusFields. Mirrors the oasgen CRD field.
	CompareScope string `json:"compareScope,omitempty"`
	// ConfigurationFields: the list of fields to use as configuration fields
	ConfigurationFields []ConfigurationField `json:"configurationFields,omitempty"`
	// VerbsDescription: the list of verbs to use on this resource
	VerbsDescription []VerbsDescription `json:"verbsDescription"`
	// ObserveApiRef, when set, delegates observe to a Snowplow RESTAction (invoked via snowplow /call)
	// whose composed .status is projected into this resource's status. Mirrors the oasgen CRD field.
	ObserveApiRef *ApiRef `json:"observeApiRef,omitempty"`
	// CreateApiRef / DeleteApiRef / UpdateApiRef, when set, delegate create / delete / update to a Snowplow
	// RESTAction (an idempotent multi-call sequence) invoked via snowplow /call. Mirrors the oasgen CRD fields.
	CreateApiRef *ApiRef `json:"createApiRef,omitempty"`
	DeleteApiRef *ApiRef `json:"deleteApiRef,omitempty"`
	UpdateApiRef *ApiRef `json:"updateApiRef,omitempty"`
}

// ApiRef references a Snowplow RESTAction resolved via snowplow's /call endpoint.
type ApiRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// Extras are static key/values merged under the per-instance context passed to snowplow as request extras.
	Extras map[string]interface{} `json:"extras,omitempty"`
	// NotFoundExpr / UpToDateExpr (observeApiRef only) are gojq boolean predicates over {spec, status} - where
	// status is the RESTAction's composed result - that let a delegated observe report non-existence
	// (NotFoundExpr true => create) and drift (UpToDateExpr false => update), composing with create/update.
	NotFoundExpr *JQProgram `json:"notFoundExpr,omitempty"`
	UpToDateExpr *JQProgram `json:"upToDateExpr,omitempty"`
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
	Action string `json:"action"`
}

type Info struct {
	// URL of the OAS 3.0 JSON file that is being requested.
	URL string `json:"url"`

	// The resource to manage
	Resource Resource `json:"resources,omitempty"`

	// The spec of the configuration resource
	ConfigurationSpec map[string]interface{}

	// SetAuth function, when called, sets the authentication for the request.
	SetAuth func(req *http.Request)
}

type Getter interface {
	Get(un *unstructured.Unstructured) (*Info, error)
}

func Dynamic(cfg *rest.Config, pluralizer pluralizer.PluralizerInterface) (Getter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("rest config is nil")
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	return &dynamicGetter{
		pluralizer:    pluralizer,
		dynamicClient: dyn,
		jqResolver:    jqmodule.New(dyn, nil),
	}, nil
}

var _ Getter = (*dynamicGetter)(nil)

type dynamicGetter struct {
	dynamicClient dynamic.Interface
	pluralizer    pluralizer.PluralizerInterface
	// jqResolver materializes JQProgram.Ref (configmap:// / http(s):// jq modules) into JQProgram.Inline at
	// load time, so downstream jq consumers only ever see inline source.
	jqResolver *jqmodule.Resolver
}

// Get retrieves the related RestDefinition for the given unstructured object.
// The information is extracted from the RestDefinition and returned as an Info struct.
func (g *dynamicGetter) Get(un *unstructured.Unstructured) (*Info, error) {
	gvr, err := g.pluralizer.GVKtoGVR(un.GroupVersionKind())
	if err != nil {
		return nil, fmt.Errorf("getting GVR for '%v' in namespace: %s", un.GetKind(), un.GetNamespace())
	}

	gvrForDefinitions := schema.GroupVersionResource{
		Group:    "ogen.krateo.io",
		Version:  "v1alpha1",
		Resource: "restdefinitions",
	}

	all, err := g.dynamicClient.Resource(gvrForDefinitions).
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting definitions for '%v' in namespace: %s - %w", gvr.String(), un.GetNamespace(), err)
	}
	if len(all.Items) == 0 {
		return nil, fmt.Errorf("%w for '%v' in namespace %s", ErrDefinitionNotFound, gvr, un.GetNamespace())
	}

	for _, item := range all.Items {
		res, ok, err := unstructured.NestedFieldNoCopy(item.Object, "spec", "resource")
		if !ok {
			return nil, fmt.Errorf("missing spec.resource in definition for '%v' in namespace: %s", gvr, un.GetNamespace())
		}
		if err != nil {
			return nil, err
		}

		group, ok, err := unstructured.NestedString(item.Object, "spec", "resourceGroup")
		if !ok {
			return nil, fmt.Errorf("missing spec.resourceGroup in definition for '%v' in namespace: %s", gvr, un.GetNamespace())
		}
		if err != nil {
			return nil, err
		}

		kind, ok, err := unstructured.NestedString(item.Object, "spec", "resource", "kind")
		if !ok {
			return nil, fmt.Errorf("missing kind in definition for '%v' in namespace: %s", gvr, un.GetNamespace())
		}
		if err != nil {
			return nil, err
		}
		if kind != un.GetKind() {
			continue
		}

		oasPath, ok, err := unstructured.NestedString(item.Object, "spec", "oasPath")
		if !ok {
			return nil, fmt.Errorf("missing spec.oasPath in definition for '%v' in namespace: %s", gvr, un.GetNamespace())
		}
		if err != nil {
			return nil, err
		}

		if group == gvr.Group {
			// Convert the map to JSON
			jsonData, err := json.Marshal(res)
			if err != nil {
				return nil, err
			}
			// Convert the JSON to a struct
			var resource Resource
			err = json.Unmarshal(jsonData, &resource)
			if err != nil {
				return nil, err
			}

			info := &Info{
				URL:      oasPath,
				Resource: resource,
			}

			//log.Printf("Found definition for '%v' in namespace: %s", gvr, un.GetNamespace())
			// print info as json for debugging
			//infoJson, _ := json.MarshalIndent(info, "", "  ")
			//log.Printf("Definition info: %s", string(infoJson))

			err = g.processConfigurationRef(un, info)
			if err != nil {
				return nil, err
			}

			if err := g.resolveJQRefs(context.Background(), info, item.GetNamespace()); err != nil {
				return nil, err
			}

			return info, nil
		}
	}
	return nil, fmt.Errorf("%w for '%v' in namespace %s", ErrDefinitionNotFound, gvr, un.GetNamespace())
}

// resolveJQRefs walks every JQProgram carried by the resource's verbs and materializes any module reference
// (JQProgram.Ref) into JQProgram.Inline, so downstream jq consumers only deal with inline source.
func (g *dynamicGetter) resolveJQRefs(ctx context.Context, info *Info, ownNamespace string) error {
	if info == nil {
		return nil
	}
	for i := range info.Resource.VerbsDescription {
		v := &info.Resource.VerbsDescription[i]
		for _, p := range []*JQProgram{v.RequestTransform, v.ResponseTransform, v.NotFoundBody} {
			if err := g.materializeJQ(ctx, p, ownNamespace); err != nil {
				return err
			}
		}
		for j := range v.FieldMapping {
			if vm := v.FieldMapping[j].ValueMapping; vm != nil {
				if err := g.materializeJQ(ctx, vm.JQ, ownNamespace); err != nil {
					return err
				}
			}
		}
		if v.Async != nil {
			if err := g.materializeJQ(ctx, v.Async.OperationRef.JQ, ownNamespace); err != nil {
				return err
			}
		}
	}
	// The observeApiRef existence/drift predicates may also be module refs.
	if ar := info.Resource.ObserveApiRef; ar != nil {
		for _, p := range []*JQProgram{ar.NotFoundExpr, ar.UpToDateExpr} {
			if err := g.materializeJQ(ctx, p, ownNamespace); err != nil {
				return err
			}
		}
	}
	return nil
}

// materializeJQ resolves a single JQProgram's module reference into inline source. A module that names an
// Entrypoint has it appended as the trailing query (module defs + `<entrypoint>`); a module with no
// entrypoint must itself be a complete jq program. Inline programs and nil are left unchanged.
func (g *dynamicGetter) materializeJQ(ctx context.Context, p *JQProgram, ownNamespace string) error {
	if p == nil || p.Ref == "" {
		return nil
	}
	src, err := g.jqResolver.Fetch(ctx, p.Ref, ownNamespace)
	if err != nil {
		return fmt.Errorf("resolving jq module: %w", err)
	}
	program := string(src)
	if p.Entrypoint != "" {
		program = program + "\n" + p.Entrypoint
	}
	p.Inline = program
	p.Ref = ""
	p.Entrypoint = ""
	return nil
}

// processConfigurationRef processes the configuration reference for the given unstructured object.
// It retrieves the configuration spec and authentication methods from the Configuration CR.
// It returns an error if the configuration reference is not valid or if the retrieval fails.
func (g *dynamicGetter) processConfigurationRef(un *unstructured.Unstructured, info *Info) error {
	configRef, ok, err := unstructured.NestedStringMap(un.Object, "spec", "configurationRef")
	if err != nil {
		return fmt.Errorf("getting spec.configurationRef for resource of kind '%v' in namespace: %s", un.GetKind(), un.GetNamespace())
	}
	if !ok {
		return nil // No auth or configuration defined
	}

	// The default namespace used to search the Configuration CR is the same namespace as the unstructured object
	namespace := un.GetNamespace()
	if val, ok := configRef["namespace"]; ok { // if the namespace is specified in the configRef field, use it to search the Configuration CR
		namespace = val
	}

	gvk := un.GroupVersionKind()
	gvk.Kind = fmt.Sprintf("%sConfiguration", gvk.Kind) // e.g., "WorkflowConfiguration"
	gvk.Version = configurationVersion

	gvr, err := g.pluralizer.GVKtoGVR(gvk)
	if err != nil {
		return err
	}

	config, err := g.dynamicClient.Resource(gvr).
		Namespace(namespace).
		Get(context.Background(), configRef["name"], metav1.GetOptions{})
	if err != nil {
		return err
	}

	configSpec, ok, err := unstructured.NestedMap(config.Object, "spec", "configuration")
	if err != nil {
		return err
	}
	if ok {
		info.ConfigurationSpec = configSpec
	}

	authMethods, ok, err := unstructured.NestedMap(config.Object, "spec", "authentication")
	if err != nil {
		return err
	}
	if !ok {
		return nil // No auth methods defined
	}

	return parseAuthentication(authMethods, g.dynamicClient, info)
}

// parseAuthentication parses the authentication object and returns the appropriate AuthMethod for the given AuthType.
// It returns an error if the authentication object is not valid.
func parseAuthentication(authMethods map[string]interface{}, dyn dynamic.Interface, info *Info) error {
	for authTypeStr, authMethod := range authMethods {
		authType, err := auth.ToType(authTypeStr)
		if err != nil {
			return err
		}

		authMethodMap, ok := authMethod.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid auth method format for type: %s", authTypeStr)
		}

		switch authType {
		case auth.AuthTypeBasic:
			usernameRef, ok, err := unstructured.NestedStringMap(authMethodMap, "usernameRef")
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("missing usernameRef in basic auth")
			}

			username, err := GetSecret(context.Background(), dyn, SecretKeySelector{
				Name:      usernameRef["name"],
				Namespace: usernameRef["namespace"],
				Key:       usernameRef["key"],
			})
			if err != nil {
				return err
			}

			passwordRef, ok, err := unstructured.NestedStringMap(authMethodMap, "passwordRef")
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("missing passwordRef in basic auth")
			}

			password, err := GetSecret(context.Background(), dyn, SecretKeySelector{
				Name:      passwordRef["name"],
				Namespace: passwordRef["namespace"],
				Key:       passwordRef["key"],
			})
			if err != nil {
				return err
			}

			info.SetAuth = func(req *http.Request) {
				req.SetBasicAuth(username, password)
			}

			return nil
		case auth.AuthTypeBearer:
			tokenRef, ok, err := unstructured.NestedStringMap(authMethodMap, "tokenRef")
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("missing tokenRef in bearer auth")
			}
			token, err := GetSecret(context.Background(), dyn, SecretKeySelector{
				Name:      tokenRef["name"],
				Namespace: tokenRef["namespace"],
				Key:       tokenRef["key"],
			})
			if err != nil {
				return err
			}

			info.SetAuth = func(req *http.Request) {
				req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
			}
			return nil

		case auth.AuthTypeAPIKey:
			// An OAS `apiKey` scheme sends the credential VERBATIM in a header the document names. Neither
			// the header nor any prefix can be read from the document here — this package never parses the
			// OAS (DocScheme is built later, on the client) — so oasgen carries both onto the Configuration
			// CR and they are read back out of it.
			tokenRef, ok, err := unstructured.NestedStringMap(authMethodMap, "tokenRef")
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("missing tokenRef in apiKey auth")
			}
			header, _, err := unstructured.NestedString(authMethodMap, "header")
			if err != nil {
				return err
			}
			// An empty header is rejected rather than defaulted: http.Header.Set("") silently produces a
			// malformed request, so guessing "Authorization" here would turn a configuration mistake into a
			// wrong-but-plausible call. oasgen defaults this field when the document is unambiguous.
			if strings.TrimSpace(header) == "" {
				return fmt.Errorf("missing header in apiKey auth: the header name the credential is sent in must be set")
			}
			// Optional; empty means send the credential exactly as stored, which is what apiKey means.
			valuePrefix, _, err := unstructured.NestedString(authMethodMap, "valuePrefix")
			if err != nil {
				return err
			}
			token, err := GetSecret(context.Background(), dyn, SecretKeySelector{
				Name:      tokenRef["name"],
				Namespace: tokenRef["namespace"],
				Key:       tokenRef["key"],
			})
			if err != nil {
				return err
			}

			info.SetAuth = func(req *http.Request) {
				req.Header.Set(header, valuePrefix+token)
			}
			return nil
		}
	}
	return fmt.Errorf("no supported auth method found")
}

type SecretKeySelector struct {
	Name      string
	Namespace string
	Key       string
}

func GetSecret(ctx context.Context, client dynamic.Interface, secretKeySelector SecretKeySelector) (string, error) {
	gvr := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "secrets",
	}

	sec, err := client.Resource(gvr).Namespace(secretKeySelector.Namespace).Get(ctx, secretKeySelector.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	data, _, err := unstructured.NestedMap(sec.Object, "data")
	if err != nil {
		return "", err
	}
	// Check if the key exists in the data
	value, exists := data[secretKeySelector.Key]
	if !exists {
		return "", fmt.Errorf("key %s not found in secret %s/%s", secretKeySelector.Key, secretKeySelector.Namespace, secretKeySelector.Name)
	}

	// Check if the value is a string
	bsec, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("value for key %s in secret %s/%s is not a string", secretKeySelector.Key, secretKeySelector.Namespace, secretKeySelector.Name)
	}
	bkey, err := base64.StdEncoding.DecodeString(bsec)
	if err != nil {
		return "", fmt.Errorf("failed to decode secret key: %w", err)
	}
	// Trim surrounding whitespace. Every caller of GetSecret uses the value as a
	// credential -- a basic username/password, a bearer token or an apiKey -- and
	// leading or trailing whitespace is never part of one. It is, however, the
	// DEFAULT outcome of most ways of producing a Secret: `jq -r ... > file`,
	// `echo`, and virtually every text editor append a newline, and
	// `kubectl create secret --from-file` preserves it faithfully.
	//
	// Untrimmed, that newline reaches http.Header.Set and Go rejects the entire
	// request with an error that names neither the credential nor the cause:
	//
	//	net/http: invalid header field value for "Authorization"
	//
	// The Secret looks correct under every normal inspection -- the trailing 0a is
	// only visible under xxd -- so the natural first suspicion is the endpoint or
	// an expired token, not an invisible byte. Trimming cannot discard meaningful
	// input, whereas not trimming turns a near-universal authoring accident into
	// an opaque and permanent reconcile failure.
	return strings.TrimSpace(string(bkey)), nil
}
