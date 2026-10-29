package restclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	pathutil "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	stringset "github.com/krateo-platformops/rest-dynamic-controller/internal/text"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/comparison"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	fgetter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/filegetter"
	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	orderedmap "github.com/pb33f/libopenapi/orderedmap"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// TODO: to be re-enabled when libopenapi-validator is stable
// Validator defines the interface for request validation.
//type Validator interface {
//	ValidateRequest(req *http.Request) (bool, []error)
//}

type APICallType string

// buildPath assembles the request URL from the server base URL, the OAS path template, the path
// parameters and the query parameters. Every caller (see builder.BuildCallConfig) supplies RAW values —
// straight out of the CR spec/status, a fieldMapping or a static per-verb query — so this function owns
// percent-encoding, and must apply it EXACTLY ONCE.
//
// Path parameters are escaped here, with url.PathEscape; the later url.Parse(parsed.String())
// round-trip preserves that escaped form in RawPath rather than re-encoding it, so one pass is all
// they get. Query parameters are NOT escaped here: url.Values.Encode() below escapes both key and
// value. Escaping them here as well would percent-encode the '%' of the first pass, so a git ref
// "builder/sock-shop" would leave as "builder%252Fsock-shop" and the upstream API would look up a
// ref literally named "builder%2Fsock-shop" and 404.
func buildPath(baseUrl string, path string, parameters map[string]string, query map[string]string) *url.URL {
	for key, param := range parameters {
		param = url.PathEscape(param)
		path = strings.Replace(path, fmt.Sprintf("{%s}", key), param, 1)
	}

	params := url.Values{}
	for key, param := range query {
		params.Add(key, param) // raw: params.Encode() below does the single escaping pass
	}

	parsed, err := url.Parse(baseUrl)
	if err != nil {
		return nil
	}

	// Remove trailing slash from base path if present
	parsed.Opaque = "//" + pathutil.Join(parsed.Host, parsed.Path, path)
	parsed.RawQuery = params.Encode()
	parsed, err = url.Parse(parsed.String())
	if err != nil {
		return nil
	}
	return parsed
}

func getValidResponseCode(codes *orderedmap.Map[string, *v3.Response]) ([]int, error) {
	var validCodes []int
	for code := codes.First(); code != nil; code = code.Next() {
		icode, err := strconv.Atoi(code.Key())
		if err != nil {
			return nil, fmt.Errorf("invalid response code: %s", code.Key())
		}
		if icode >= 200 && icode < 300 {
			validCodes = append(validCodes, icode)
			// return icode, nil
		}
	}
	return validCodes, nil
}

type UnstructuredClientInterface interface {
	ValidateRequest(httpMethod string, path string, parameters map[string]string, query map[string]string, headers map[string]string, cookies map[string]string) error
	RequestedBody(httpMethod string, path string) (bodys stringset.StringSet, err error)
	RequestedParams(httpMethod string, path string) (parameters, query, headers, cookies stringset.StringSet, err error)
	// UpdatableBodyPaths belongs here with its siblings above: it is document introspection, and a caller
	// holding the interface needs it for compareScope "updatable". It was reachable only through the
	// concrete type, which forced Observe to hold one, which is what stood in the way of resolving a
	// client per verb (#220).
	UpdatableBodyPaths(httpMethod string, path string) ([]string, error)
	FindBy(ctx context.Context, cli *http.Client, path string, conf *RequestConfiguration, findByAction *getter.VerbsDescription) (Response, error)
	Call(ctx context.Context, cli *http.Client, path string, conf *RequestConfiguration) (Response, error)
	// ServerURL is the EXTERNAL endpoint this client calls -- the document's servers[0].url. Here for the
	// same reason UpdatableBodyPaths is: it was reachable only through the concrete type, and the C8
	// audit emitter needs it from a caller holding the interface (#253).
	//
	// It must identify the VENDOR, not the document: clientInfo.URL is a configmap:// path and names
	// nothing reachable, so it would be the wrong value for server.address even though it is the one
	// closest to hand.
	//
	// Returns the full URL. audit.Emit reduces it to host[:port], dropping scheme, userinfo, path, query
	// and fragment -- centrally, so no caller has to remember that a vendor path routinely carries an
	// identifier and at least one carries a token-shaped segment.
	ServerURL() string
	//Validate(req *http.Request) (bool, []error) // TODO: to be re-enabled when libopenapi-validator is stable (to be renamed to ValidateRequest)
}

type UnstructuredClient struct {
	IdentifierFields       []string
	IdentifiersMatchPolicy string
	Resource               *unstructured.Unstructured
	//Doc                   libopenapi.Document         // Parsed OpenAPI document by libopenapi, needed for http request validation. TODO: to be re-enabled when libopenapi-validator is stable
	DocScheme  *libopenapi.DocumentModel[v3.Document] // OpenAPI document model (high-level)
	Server     string
	Debug      bool
	PrettyJSON bool
	SetAuth    func(req *http.Request)
	//Validator             Validator 				    // Validator for request validation. TODO: to be re-enabled when libopenapi-validator is stable
}

type RequestConfiguration struct {
	// BuildErr records a failure that occurred while ASSEMBLING this configuration -- e.g. a
	// request-direction jq valueMapping that did not compile, failed, or returned something that cannot
	// address a URL.
	//
	// It is carried here rather than returned from BuildCallConfig because that function has nine callers,
	// and an error return is nine chances to ignore it. Every request instead passes through Call /
	// CallForPagination, which refuse outright when this is set -- one choke point that cannot be
	// forgotten. Silently sending a request whose path parameter was dropped is the failure mode this
	// exists to prevent: the URL is still syntactically valid, so the API answers 404, and the reconciler
	// acts on not-found by CREATING (#117).
	BuildErr error

	Parameters map[string]string // Path parameters
	Query      map[string]string
	Headers    map[string]string
	Cookies    map[string]string
	Body       any
	Method     string
	// SuccessCodes are additional HTTP status codes to accept as success for this call, merged with the
	// OAS-derived 2xx codes during response validation.
	SuccessCodes []int
	// TolerateCodes are HTTP status codes to treat as a successful empty response instead of an error.
	TolerateCodes []int
	// NotFoundCodes are HTTP status codes remapped to a not-found result (StatusError 404).
	NotFoundCodes []int
	// SensitiveValues are resolved values (e.g. a secretRef-resolved secret) that must never appear in
	// verbose/debug request-dump output, even though they are sent to the external API. Call/FindBy redact
	// every occurrence of these strings before writing a debug dump.
	SensitiveValues []string
}

// isInResource is a method used during a "FindBy" operation.
// It compares a value from an API response with the corresponding value in the local Unstructured resource.
// It checks for the identifier's presence and correctness in 'spec' first, then falls back to checking 'status'.
// TODO: to be re-evaluated and possibly modified for potential addition of `ResponseFieldMapping` (possibly in future versions).
func (u *UnstructuredClient) isInResource(responseValue interface{}, fieldPath ...string) (bool, error) {
	if u.Resource == nil {
		return false, fmt.Errorf("resource is nil")
	}

	// Check 1: Look for the identifier in 'spec'.
	specPath := append([]string{"spec"}, fieldPath...)
	localValue, found, err := unstructured.NestedFieldNoCopy(u.Resource.Object, specPath...)
	if err != nil {
		return false, fmt.Errorf("error searching for identifier in spec: %w", err)
	}
	// If the field is found in the spec, we compare it.
	// If it matches, we have a definitive match and can return true.
	if found && comparison.DeepEqual(localValue, responseValue) {
		return true, nil
	}

	// Check 2: If the identifier was not found in 'spec', or if it was found but did not match,
	// we proceed to check the 'status'. This is common for server-assigned identifiers
	// and not with identifiers normally used for a findby operation.
	// Last resort check, even if it makes less sense to search for findby identifiers in 'status'.
	statusPath := append([]string{"status"}, fieldPath...)
	localValue, found, err = unstructured.NestedFieldNoCopy(u.Resource.Object, statusPath...)
	if err != nil {
		return false, fmt.Errorf("error searching for identifier in status: %w", err)
	}
	// If found in status, we compare it. This is the last chance for a match.
	if found && comparison.DeepEqual(localValue, responseValue) {
		return true, nil
	}

	// No match.
	return false, nil
}

// ValidateRequest is a method that validates the request parameters, query, headers, and cookies against the OpenAPI document.
// It checks if the required parameters are present and returns an error if any required parameter is missing.
// ServerURL implements UnstructuredClientInterface.
func (u *UnstructuredClient) ServerURL() string { return u.Server }

func (u *UnstructuredClient) ValidateRequest(httpMethod string, path string, parameters map[string]string, query map[string]string, headers map[string]string, cookies map[string]string) error {
	pathItem, ok := u.DocScheme.Model.Paths.PathItems.Get(path)
	if !ok {
		return fmt.Errorf("path not found: %s", path)
	}
	getDoc, ok := pathItem.GetOperations().Get(strings.ToLower(httpMethod))
	if !ok {
		return fmt.Errorf("operation not found: %s", httpMethod)
	}
	for _, param := range getDoc.Parameters {
		if param.Required != nil && *param.Required {
			if param.In == "path" {
				if _, ok := parameters[param.Name]; !ok {
					return fmt.Errorf("missing path parameter: %s", param.Name)
				}
			}
			if param.In == "query" {
				if _, ok := query[param.Name]; !ok {
					return fmt.Errorf("missing query parameter: %s", param.Name)
				}
			}
			if param.In == "header" {
				if _, ok := headers[param.Name]; !ok && !isAuthorizationHeader(param.Name) {
					return fmt.Errorf("missing header: %s", param.Name)
				}
			}
			if param.In == "cookie" {
				if _, ok := cookies[param.Name]; !ok {
					return fmt.Errorf("missing cookie: %s", param.Name)
				}
			}
		}
	}
	return nil
}

// RequestedBody is a method that returns the body parameters for a given HTTP method and path.
// resolveRequestBodyMedia picks the media type whose schema describes an operation's request body, and
// reports when it cannot.
//
// `application/json` was hardcoded at both call sites, and a miss returned a zero value with a nil error
// (#288). A document declaring `application/json-patch+json`, `application/merge-patch+json` or anything
// else therefore lost its body schema SILENTLY: no body fields were reported, the builder composed
// against nothing, compareScope "updatable" degraded -- and the request was still sent. The symptom is a
// write that succeeds while carrying a subset of what the CR asked for, which is worse than a 415.
//
// Preference order, and the reason for it:
//
//  1. `application/json` exactly -- overwhelmingly the common case, and preferring it keeps every existing
//     document resolving to precisely the schema it did before.
//  2. the SINGLE declared entry, whatever it is -- a document declaring one media type has stated
//     unambiguously which schema describes its body, so honouring it requires no guess.
//
// Deliberately NOT third: picking one of several non-JSON entries. With two or more and no JSON, which
// schema applies depends on what the caller will actually send, which this layer does not know. Guessing
// there would reintroduce exactly the silent-wrong-schema failure this function exists to remove, so it
// returns an error and lets the caller say so out loud.
func resolveRequestBodyMedia(body *v3.RequestBody) (*v3.MediaType, string, error) {
	if body == nil || body.Content == nil {
		return nil, "", nil
	}
	// A media type entry with no schema is NOT a usable answer. The previous code reached
	// bodySchema.Schema.BuildSchema() directly, and a nil Schema panics inside libopenapi -- so a
	// document declaring a body with no schema was only ever safe because the hardcoded
	// Get("application/json") happened to miss it. Surfacing that as a panic would be worse than the
	// silent return it replaced, so a schema-less entry is treated as unresolved and reported.
	hasSchema := func(mt *v3.MediaType) bool { return mt != nil && mt.Schema != nil }

	if mt, ok := body.Content.Get("application/json"); ok && hasSchema(mt) {
		return mt, "application/json", nil
	}

	var only *v3.MediaType
	var onlyName string
	n := 0
	for pair := body.Content.First(); pair != nil; pair = pair.Next() {
		n++
		only, onlyName = pair.Value(), pair.Key()
	}
	switch n {
	case 0:
		return nil, "", nil
	case 1:
		if !hasSchema(only) {
			return nil, "", nil
		}
		return only, onlyName, nil
	default:
		names := make([]string, 0, n)
		for pair := body.Content.First(); pair != nil; pair = pair.Next() {
			names = append(names, pair.Key())
		}
		sort.Strings(names)
		return nil, "", fmt.Errorf("request body declares %d media types and none is application/json (%s): "+
			"cannot determine which schema describes the body", n, strings.Join(names, ", "))
	}
}

func (u *UnstructuredClient) RequestedBody(httpMethod string, path string) (bodyParams stringset.StringSet, err error) {
	if u.DocScheme == nil || u.DocScheme.Model.Paths == nil {
		return nil, fmt.Errorf("document scheme or model is nil")
	}

	pathItem, ok := u.DocScheme.Model.Paths.PathItems.Get(path)
	if !ok {
		return nil, fmt.Errorf("path not found: %s", path)
	}
	getDoc, ok := pathItem.GetOperations().Get(strings.ToLower(httpMethod))
	if !ok {
		return nil, fmt.Errorf("operation not found: %s", httpMethod)
	}
	bodyParams = stringset.NewStringSet()
	if getDoc.RequestBody == nil {
		return nil, nil
	}
	bodySchema, _, mErr := resolveRequestBodyMedia(getDoc.RequestBody)
	if mErr != nil {
		return nil, fmt.Errorf("resolving request body media type for %s %s: %w", httpMethod, path, mErr)
	}
	if bodySchema == nil {
		// No media type carries a schema -- e.g. a text/plain body. There are genuinely no JSON fields to
		// enumerate, so an empty set is the right answer and not a silent drop. The #288 defect was a
		// schema that EXISTS under a non-JSON media type, which resolveRequestBodyMedia now returns; real
		// ambiguity comes back as an error above.
		return bodyParams, nil
	}
	schema, err := bodySchema.Schema.BuildSchema()
	if err != nil {
		return nil, fmt.Errorf("building schema for %s: %w", path, err)
	}
	populateFromAllOf(schema)

	for sch := schema.Properties.First(); sch != nil; sch = sch.Next() {
		bodyParams.Add(sch.Key())
	}

	return bodyParams, nil
}

// UpdatableBodyPaths returns the dot-notation LEAF paths that the given operation's
// application/json request body can express, e.g. ["metadata.name", "properties.default"].
//
// It exists for drift comparison. A spec field is only FIXABLE if the update verb's body
// can carry it, so comparing a field outside this set can only produce noise: the
// controller sees a difference, issues an update that cannot possibly contain the field,
// nothing changes, and the same difference is found again on the next reconcile — forever.
//
// Real example (Aruba Cloud Subnet), where the two schemas are deliberately asymmetric:
//
//	create  SubnetPropertiesDto        type, default, network, dhcp
//	update  SubnetUpdatePropertiesDto  default ONLY
//
// The server also assigns the CIDR itself for a Basic subnet, so spec.properties.network
// virtually always differs from the response — and no update can ever reconcile it.
//
// Leaf paths (not intermediate ones) are returned so the projection is precise:
// "properties.default" must be comparable while its sibling "properties.network" is not.
// Returns nil when the operation declares no JSON body, which callers must treat as
// "nothing is updatable" rather than "everything is".
func (u *UnstructuredClient) UpdatableBodyPaths(httpMethod string, path string) ([]string, error) {
	if u.DocScheme == nil || u.DocScheme.Model.Paths == nil {
		return nil, fmt.Errorf("document scheme or model is nil")
	}
	pathItem, ok := u.DocScheme.Model.Paths.PathItems.Get(path)
	if !ok {
		return nil, fmt.Errorf("path not found: %s", path)
	}
	op, ok := pathItem.GetOperations().Get(strings.ToLower(httpMethod))
	if !ok {
		return nil, fmt.Errorf("operation not found: %s", httpMethod)
	}
	if op.RequestBody == nil || op.RequestBody.Content == nil {
		return nil, nil
	}
	bodySchema, _, mErr := resolveRequestBodyMedia(op.RequestBody)
	if mErr != nil {
		return nil, fmt.Errorf("resolving request body media type for %s %s: %w", httpMethod, path, mErr)
	}
	if bodySchema == nil {
		// As in RequestedBody: no schema under any media type means there are no updatable JSON paths to
		// collect, which is a true answer rather than a dropped one.
		return nil, nil
	}
	schema, err := bodySchema.Schema.BuildSchema()
	if err != nil {
		return nil, fmt.Errorf("building schema for %s: %w", path, err)
	}
	var out []string
	collectLeafPaths(schema, "", &out, 0)
	return out, nil
}

// collectLeafPaths walks a request-body schema and appends the dot-notation path of every
// leaf property to out. allOf is flattened at each level via populateFromAllOf, matching how
// the rest of this package reads bodies. depth bounds the walk so a self-referential schema
// cannot loop.
func collectLeafPaths(schema *base.Schema, prefix string, out *[]string, depth int) {
	if schema == nil || depth > 20 {
		return
	}
	populateFromAllOf(schema)
	if schema.Properties == nil || schema.Properties.Len() == 0 {
		// A leaf: a scalar, an array, or an object with no declared properties (a free-form
		// map). Free-form maps are compared wholesale, which is correct — the update body
		// can carry them in their entirety.
		if prefix != "" {
			*out = append(*out, prefix)
		}
		return
	}
	for prop := schema.Properties.First(); prop != nil; prop = prop.Next() {
		child, err := prop.Value().BuildSchema()
		if err != nil {
			continue
		}
		next := prop.Key()
		if prefix != "" {
			next = prefix + "." + prop.Key()
		}
		collectLeafPaths(child, next, out, depth+1)
	}
}

// func PopulateFromAllOf() is a method that populates the schema with the properties from the allOf field.
// the recursive function to populate the schema with the properties from the allOf field.
func populateFromAllOf(schema *base.Schema) {
	if len(schema.Type) > 0 && schema.Type[0] == "array" {
		if schema.Items != nil {
			if schema.Items.N == 0 {
				sch, err := schema.Items.A.BuildSchema()
				if err != nil {
					return
				}

				populateFromAllOf(sch)
			}
		}
		return
	}
	for prop := schema.Properties.First(); prop != nil; prop = prop.Next() {
		populateFromAllOf(prop.Value().Schema())
	}
	for _, proxy := range schema.AllOf {
		propSchema, err := proxy.BuildSchema()
		populateFromAllOf(propSchema)
		if err != nil {
			return
		}
		// Iterate over the properties of the schema with First() and Next()
		for prop := propSchema.Properties.First(); prop != nil; prop = prop.Next() {
			if schema.Properties == nil {
				schema.Properties = orderedmap.New[string, *base.SchemaProxy]()
			}
			// Add the property to the schema
			schema.Properties.Set(prop.Key(), prop.Value())
		}
	}
}

// RequestedParams is a method that returns the parameters and query parameters for a given HTTP method and path.
func (u *UnstructuredClient) RequestedParams(httpMethod string, path string) (parameters, query, headers, cookies stringset.StringSet, err error) {
	pathItem, ok := u.DocScheme.Model.Paths.PathItems.Get(path)
	if !ok {
		return nil, nil, nil, nil, fmt.Errorf("path not found: %s", path)
	}
	getDoc, ok := pathItem.GetOperations().Get(strings.ToLower(httpMethod))
	if !ok {
		return nil, nil, nil, nil, fmt.Errorf("operation not found: %s", httpMethod)
	}
	parameters = stringset.NewStringSet()
	query = stringset.NewStringSet()
	headers = stringset.NewStringSet()
	cookies = stringset.NewStringSet()
	for _, param := range getDoc.Parameters {
		switch param.In {
		case "path":
			parameters.Add(param.Name)
		case "query":
			query.Add(param.Name)
		case "header":
			headers.Add(param.Name)
		case "cookie":
			cookies.Add(param.Name)
		default:
			return nil, nil, nil, nil, fmt.Errorf("unknown parameter location: %s", param.In)
		}
	}
	//fmt.Printf("RequestedParams: parameters=%v, query=%v, headers=%v, cookies=%v\n", parameters, query, headers, cookies)
	return
}

// BuildClient is a function that builds partial client from a swagger file.
func BuildClient(ctx context.Context, kubeclient dynamic.Interface, swaggerPath string) (*UnstructuredClient, error) {
	// A PRIVATE directory per call. This used to be the fixed, shared path
	// "/tmp/rest-dynamic-controller", created here and removed by `defer os.RemoveAll(basePath)` -- which
	// deletes the WHOLE directory, including files other in-flight calls are still using.
	//
	// BuildClient runs once per reconcile, from four sites in restResources.go, and the controller runs
	// REST_CONTROLLER_WORKERS (default 5) of them concurrently. So worker A returning deleted the directory
	// out from under worker B between its MkdirAll and its write, and B failed with
	//
	//	failed to download file: creating destination file:
	//	open /tmp/rest-dynamic-controller/<doc>.yaml: no such file or directory
	//
	// Being a race, its rate tracked concurrency rather than correctness: staggered by the 3-minute resync
	// it almost never fired, and after a pod restart -- when every CR of the kind reconciles at once -- it
	// fired constantly. That is how it survived unnoticed and then looked like a regression in a release
	// that had not touched this module at all.
	//
	// MkdirTemp gives each call its own directory, so a concurrent call has nothing to delete that is ours.
	// Keep it that way: any shared parent that gets RemoveAll'd reintroduces exactly this.
	basePath, err := os.MkdirTemp("", "rest-dynamic-controller-")
	if err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}
	defer os.RemoveAll(basePath)

	fgetter := &fgetter.Filegetter{
		Client:     &http.Client{},
		KubeClient: kubeclient,
	}

	err = fgetter.GetFile(ctx, filepath.Join(basePath, filepath.Base(swaggerPath)), swaggerPath, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to download file: %w", err)
	}

	contents, _ := os.ReadFile(filepath.Join(basePath, pathutil.Base(swaggerPath)))
	d, err := libopenapi.NewDocument(contents)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	doc, modelErrors := d.BuildV3Model()
	if len(modelErrors) > 0 {
		return nil, fmt.Errorf("failed to build model: %w", errors.Join(modelErrors...))
	}
	if doc == nil {
		return nil, fmt.Errorf("failed to build model")
	}

	// Resolve model references
	resolvingErrors := doc.Index.GetResolver().Resolve()
	errs := []error{}
	for i := range resolvingErrors {
		errs = append(errs, resolvingErrors[i].ErrorRef)
	}
	if len(resolvingErrors) > 0 {
		return nil, fmt.Errorf("failed to resolve model references: %w", errors.Join(errs...))
	}
	if len(doc.Model.Servers) == 0 {
		return nil, fmt.Errorf("no servers found in the document")
	}

	// TODO: to be re-enabled when libopenapi-validator is stable
	//validator, err := NewOpenAPIValidator(d)
	//if err != nil {
	//	return nil, fmt.Errorf("failed to create validator: %w", err)
	//}

	return &UnstructuredClient{
		Server: doc.Model.Servers[0].URL,
		//Doc:       d,
		DocScheme: doc,
		//Validator: validator,
	}, nil
}

// isAuthorizationHeader checks if the given header is an authorization header or contains "authorization" (case-insensitive).
func isAuthorizationHeader(header string) bool {
	return strings.Contains(strings.ToLower(header), "authorization")
}
