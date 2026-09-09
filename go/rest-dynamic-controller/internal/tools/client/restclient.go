package restclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"time"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/pagination"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/pathparsing"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	rawyaml "gopkg.in/yaml.v3"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TODO: maybe consider to wrap http.Response in our Response struct to have access to headers, status code, etc.
// In order to have isPending method attached to a complete response object.
type Response struct {
	ResponseBody any
	// Headers carries the HTTP response headers, so callers (e.g. the async engine) can read an operation
	// handle from a header such as Operation-Location on a 202 Accepted. May be nil.
	Headers    http.Header
	statusCode int
}

func (r *Response) IsPending() bool {
	if r == nil {
		return false
	}
	return r.statusCode == http.StatusProcessing || r.statusCode == http.StatusContinue || r.statusCode == http.StatusAccepted
}

func (u *UnstructuredClient) Call(ctx context.Context, cli *http.Client, path string, opts *RequestConfiguration) (Response, error) {
	if u.DocScheme == nil {
		return Response{}, fmt.Errorf("OpenAPI document scheme not initialized")
	}
	uri := buildPath(u.Server, path, opts.Parameters, opts.Query)
	if uri == nil {
		return Response{}, fmt.Errorf("failed to build URI using server %s and path %s", u.Server, path)
	}

	// We must check if there is a server override for this specific operation
	// so we look it up in the OpenAPI.
	// TODO: make helper function for this logic
	pathItem, ok := u.DocScheme.Model.Paths.PathItems.Get(path)
	if !ok {
		return Response{}, fmt.Errorf("path not found: %s", path)
	}
	httpMethod := string(opts.Method)
	ops := pathItem.GetOperations()
	if ops != nil {
		op, ok := ops.Get(strings.ToLower(httpMethod))
		if !ok {
			return Response{}, fmt.Errorf("operation not found for method %s at path %s", httpMethod, path)
		}

		if len(op.Servers) > 0 {
			server := op.Servers[0] // Use the first server defined for the operation (multiple servers per operation are not supported by Rest Dynamic Controller)
			// Changed the uri since we have a server override for this operation
			uri = buildPath(server.URL, path, opts.Parameters, opts.Query)
			if uri == nil {
				return Response{}, fmt.Errorf("failed to build URI using server %s and path %s", server.URL, path)
			}
		}
	}

	err := u.ValidateRequest(httpMethod, path, opts.Parameters, opts.Query, opts.Headers, opts.Cookies)
	if err != nil {
		return Response{}, err
	}

	var response any
	var payload []byte

	headers := make(http.Header)
	payload = nil
	m, ok := opts.Body.(map[string]any)
	if !ok && opts.Body != nil {
		return Response{}, fmt.Errorf("invalid body type: %T", opts.Body)
	}
	if len(m) != 0 {
		jsonBody, err := json.Marshal(opts.Body)
		if err != nil {
			return Response{}, err
		}
		payload = jsonBody
		headers.Set("Content-Type", "application/json")
	}

	for k, v := range opts.Headers {
		headers.Set(k, v)
	}

	req := &http.Request{
		Method: httpMethod,
		URL:    uri,
		Proto:  "HTTP/1.1",
		Body:   io.NopCloser(bytes.NewReader(payload)),
		Header: headers,
	}
	// Carry the reconcile span on the request so the otelhttp transport emits a
	// child client span and injects a W3C traceparent (continuing the trace).
	req = req.WithContext(ctx)

	for k, v := range opts.Cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}

	if u.Debug {
		cli.Transport = &debuggingRoundTripper{
			Transport:    cli.Transport,
			Out:          os.Stdout,
			PrettyJSON:   u.PrettyJSON,
			RedactValues: opts.SensitiveValues,
		}
	}
	// Wrap the (possibly debug) transport with otelhttp so outbound calls to the
	// managed external API are trace-instrumented. No-op when tracing is disabled.
	cli.Transport = instrumentTransport(cli.Transport)

	if u.SetAuth != nil {
		u.SetAuth(req)
	}

	// TODO: to be re-enabled when libopenapi-validator is stable
	//if u.Validator != nil {
	//	valid, validationErrors := u.Validator.ValidateRequest(req)
	//	if !valid {
	//		log.Println("Request is NOT valid according to OpenAPI specification:")
	//		for _, err := range validationErrors {
	//			log.Println(err.Error())
	//		}
	//		// Returning a generic error as the validation errors are already logged.
	//		return Response{}, fmt.Errorf("request validation failed")
	//	} else {
	//		log.Println("Request is valid according to OpenAPI specification")
	//	}
	//}

	start := time.Now()
	resp, err := cli.Do(req)
	if err != nil {
		recordOutboundCall(ctx, httpMethod, path, 0, start)
		return Response{}, fmt.Errorf("making request: %w", err)
	}
	recordOutboundCall(ctx, httpMethod, path, resp.StatusCode, start)
	defer resp.Body.Close()

	// If this verb declares the returned status code as meaning "not found", remap it to a not-found
	// result (StatusError 404) so the existence logic treats the external resource as absent, even when
	// the API signals absence with a non-standard code (e.g. 410 Gone or a 204).
	if opts != nil && HasValidStatusCode(resp.StatusCode, opts.NotFoundCodes...) {
		return Response{}, &StatusError{
			StatusCode: http.StatusNotFound,
			Inner:      fmt.Errorf("status %d remapped to not-found by notFoundCodes", resp.StatusCode),
		}
	}

	// If this verb tolerates the returned status code, short-circuit to a successful empty response
	// (e.g. an API that returns 404 for an optional sub-resource that is simply empty), skipping status
	// validation and body parsing. The caller's nil-body handling then treats the resource as up-to-date.
	if opts != nil && HasValidStatusCode(resp.StatusCode, opts.TolerateCodes...) {
		return Response{ResponseBody: nil, Headers: resp.Header, statusCode: resp.StatusCode}, nil
	}

	getDoc, ok := pathItem.GetOperations().Get(strings.ToLower(httpMethod))
	if !ok {
		return Response{}, fmt.Errorf("operation not found: %s", httpMethod)
	}
	validStatusCodes, err := getValidResponseCode(getDoc.Responses.Codes)
	if err != nil {
		return Response{}, err
	}
	// Merge any per-verb successCodes declared on the RestDefinition, so an API returning a non-standard
	// success code the OAS does not document is accepted rather than rejected as an invalid status.
	if opts != nil {
		validStatusCodes = append(validStatusCodes, opts.SuccessCodes...)
	}

	if !HasValidStatusCode(resp.StatusCode, validStatusCodes...) {
		return Response{}, &StatusError{
			StatusCode: resp.StatusCode,
			Inner:      fmt.Errorf("invalid status code: %d", resp.StatusCode),
		}
	}

	// Read the response body as we need to check its content length
	// Just checking if resp.Body is nil does not work, as it can be non-nil with a zero-length body.
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, fmt.Errorf("failed to read response body: %w", err)
	}

	defer resp.Body.Close()

	// Re-wrap body otherwise it will be closed
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// Allow empty body for success codes that may legitimately return no content, even if not common (no MUST NOT in RFCs, so we allow it):
	// - 200 OK: Common for DELETE, PUT, PATCH operations
	// - 201 Created: Some APIs return only Location header
	// - 202 Accepted: Common for async/fire-and-forget operations
	// - 203 Non-Authoritative Information: Transforming proxy responses
	// - 204 No Content: RFC mandates no body
	// - 205 Reset Content: RFC mandates no body
	// - 304 Not Modified: RFC mandates no body
	statusAllowsEmpty := resp.StatusCode == http.StatusOK ||
		resp.StatusCode == http.StatusCreated ||
		resp.StatusCode == http.StatusAccepted ||
		resp.StatusCode == http.StatusNonAuthoritativeInfo ||
		resp.StatusCode == http.StatusNoContent ||
		resp.StatusCode == http.StatusResetContent ||
		resp.StatusCode == http.StatusNotModified

	if len(bodyBytes) == 0 && !statusAllowsEmpty {
		return Response{}, fmt.Errorf("response body is empty for a status code which requires a body: %d", resp.StatusCode)
	}

	// For status codes that allow empty bodies, return nil directly, without going through handleResponse.
	// The headers are still surfaced so an async trigger returning 202 + Operation-Location with no body
	// can have its operation handle extracted from the header.
	if len(bodyBytes) == 0 && statusAllowsEmpty {
		return Response{
			ResponseBody: nil,
			Headers:      resp.Header,
			statusCode:   resp.StatusCode,
		}, nil
	}

	err = handleResponse(resp.Body, &response)
	if err != nil {
		return Response{}, fmt.Errorf("handling response: %w", err)
	}

	return Response{
		ResponseBody: response,
		Headers:      resp.Header,
		statusCode:   resp.StatusCode,
	}, nil
}

// FindBy locates a specific resource within an API response it retrieves.
// It serves as the primary orchestrator for the `FindBy` action of the Rest Dynamic Controller,
// delegating response parsing and item matching to helper functions: extractItemsFromResponse, findItemInList, and isItemMatch.
func (u *UnstructuredClient) FindBy(ctx context.Context, cli *http.Client, path string, opts *RequestConfiguration, findByAction *getter.VerbsDescription) (Response, error) {
	if findByAction == nil || findByAction.Pagination == nil {
		// No pagination configured, perform a single call.
		return u.CallFindBySingle(ctx, cli, path, opts)
	}

	// Set up debug transport once, before pagination starts
	if u.Debug {
		if _, ok := cli.Transport.(*debuggingRoundTripper); !ok {
			cli.Transport = &debuggingRoundTripper{
				Transport:    cli.Transport,
				Out:          os.Stdout,
				PrettyJSON:   u.PrettyJSON,
				RedactValues: opts.SensitiveValues,
			}
		}
	}

	// Create the paginator based on the configuration (e.g., continuation token).
	paginator, err := pagination.NewPaginator(findByAction.Pagination)
	if err != nil {
		return Response{}, fmt.Errorf("failed to create paginator: %w", err)
	}
	if paginator == nil {
		// Paginator factory returned nil, treat as no pagination.
		return u.CallFindBySingle(ctx, cli, path, opts)
	}

	paginator.Init()

	// Bound the walk. This loop had no cap at all: its only exits were a match, a transport error, or
	// the paginator reporting done, so a findby over a large collection with no match walked every page,
	// and a paginator that never reports done walked forever (#119).
	//
	// This is a SAFETY BACKSTOP, not a policy knob. It exists to make a runaway terminate, and is set
	// high enough that no legitimate search reaches it. The per-RestDefinition bound an author actually
	// wants -- "scan at most N pages for this resource" -- is the `maxPages` field proposed in #119, and
	// belongs with that design rather than being smuggled in as a constant here.
	pagesScanned := 0

	for {
		if pagesScanned >= maxFindByPages {
			// NOT a 404. IsNotFoundError keys on a 404 StatusError, and the reconciler acts on not-found
			// by CREATING the resource -- so returning one here would tell it "this does not exist" when
			// the truth is "I stopped looking", and it would create a duplicate of something it never
			// finished searching for. "I scanned N pages and did not conclude" is a different answer
			// from "it is not there", and must stay one.
			return Response{}, fmt.Errorf(
				"findby stopped after scanning %d pages without finding a match or reaching the end of the collection; "+
					"this is a safety limit, not a conclusion that the resource is absent", maxFindByPages)
		}
		pagesScanned++

		// Build and execute the request with the current paginator configuration (e.g., continuationToken).
		response, httpResp, err := u.CallForPagination(ctx, cli, path, opts, paginator)
		if err != nil {
			return Response{}, err
		}

		// Normalize the response to a list of items.
		itemList, err := ExtractItemsFromResponse(response.ResponseBody)
		if err != nil {
			// If extraction fails, we can't continue.
			return Response{}, err
		}

		// Search for a matching item in the current page's results.
		if matchedItem, found := u.findItemInList(itemList); found {
			// Found a match, return it immediately.
			// We do not continue pagination once a match is found.
			return Response{
				ResponseBody: matchedItem,
				statusCode:   response.statusCode,
			}, nil
		}

		// At this point, no match was found in the current page.
		// Ask the paginator if we should continue to the next page (in other words, if there is a next page).
		bodyBytes, _ := json.Marshal(response.ResponseBody) // Marshal body for analysis by paginator
		shouldContinue, err := paginator.ShouldContinue(httpResp, bodyBytes)
		if err != nil {
			return Response{}, fmt.Errorf("error checking pagination continuation: %w", err)
		}

		if !shouldContinue {
			// Paginator says we are done, break the loop.
			break
		}
	}

	// If the loop completes without finding a match, return a Not Found error.
	return Response{}, &StatusError{
		StatusCode: http.StatusNotFound,
		Inner:      fmt.Errorf("item not found after checking all pages"),
	}
}

// maxFindByPages caps the paginated findby walk -- a runaway backstop, deliberately far above any
// real search, and NOT the per-resource bound an author would configure (#119).
const maxFindByPages = 1000

// CallFindBySingle executes a non-paginated FindBy operation.
func (u *UnstructuredClient) CallFindBySingle(ctx context.Context, cli *http.Client, path string, opts *RequestConfiguration) (Response, error) {
	response, err := u.Call(ctx, cli, path, opts)
	if err != nil {
		return Response{}, err
	}
	if response.ResponseBody == nil {
		return Response{}, &StatusError{StatusCode: http.StatusNotFound, Inner: fmt.Errorf("item not found")}
	}

	// Extract the list of items from the response.
	itemList, err := ExtractItemsFromResponse(response.ResponseBody)
	if err != nil {
		return Response{}, err
	}

	// Delegate the search logic to a dedicated helper function.
	if matchedItem, found := u.findItemInList(itemList); found {
		return Response{ResponseBody: matchedItem, statusCode: response.statusCode}, nil
	}

	// If no match is found after checking all items, return a Not Found error.
	return Response{}, &StatusError{StatusCode: http.StatusNotFound, Inner: fmt.Errorf("item not found")}
}

// CallForPagination builds an `http.Request`, lets the paginator update it, executes it, and returns the response.
// TODO: to be refactored to avoid code duplication with Call method.
// Prerequisite for refactor is to change the Response struct to wrap http.Response directly.
// Differences with Call are mainly the paginator usage and the removal of debug transport setup (otherwise it would be set incrementally on each paginated call).
func (u *UnstructuredClient) CallForPagination(ctx context.Context, cli *http.Client, path string, opts *RequestConfiguration, paginator pagination.Paginator) (Response, *http.Response, error) {
	if u.DocScheme == nil {
		return Response{}, nil, fmt.Errorf("OpenAPI document scheme not initialized")
	}
	uri := buildPath(u.Server, path, opts.Parameters, opts.Query)
	if uri == nil {
		return Response{}, nil, fmt.Errorf("failed to build URI using server %s and path %s", u.Server, path)
	}

	// We must check if there is a server override for this specific operation
	// so we look it up in the OpenAPI.
	pathItem, ok := u.DocScheme.Model.Paths.PathItems.Get(path)
	if !ok {
		return Response{}, nil, fmt.Errorf("path not found: %s", path)
	}
	httpMethod := string(opts.Method)
	ops := pathItem.GetOperations()
	if ops != nil {
		op, ok := ops.Get(strings.ToLower(httpMethod))
		if !ok {
			return Response{}, nil, fmt.Errorf("operation not found for method %s at path %s", httpMethod, path)
		}

		if len(op.Servers) > 0 {
			server := op.Servers[0] // Use the first server defined for the operation (multiple servers per operation are not supported by Rest Dynamic Controller)
			// Changed the uri since we have a server override for this operation
			uri = buildPath(server.URL, path, opts.Parameters, opts.Query)
			if uri == nil {
				return Response{}, nil, fmt.Errorf("failed to build URI using server %s and path %s", server.URL, path)
			}
		}
	}

	err := u.ValidateRequest(httpMethod, path, opts.Parameters, opts.Query, opts.Headers, opts.Cookies)
	if err != nil {
		return Response{}, nil, err
	}

	var response any
	var payload []byte

	headers := make(http.Header)
	payload = nil
	m, ok := opts.Body.(map[string]any)
	if !ok && opts.Body != nil {
		return Response{}, nil, fmt.Errorf("invalid body type: %T", opts.Body)
	}
	if len(m) != 0 {
		jsonBody, err := json.Marshal(opts.Body)
		if err != nil {
			return Response{}, nil, err
		}
		payload = jsonBody
		headers.Set("Content-Type", "application/json")
	}

	for k, v := range opts.Headers {
		headers.Set(k, v)
	}

	req := &http.Request{
		Method: httpMethod,
		URL:    uri,
		Proto:  "HTTP/1.1",
		Body:   io.NopCloser(bytes.NewReader(payload)),
		Header: headers,
	}
	// Carry the reconcile span on the request so the otelhttp transport emits a
	// child client span and injects a W3C traceparent (continuing the trace).
	req = req.WithContext(ctx)

	for k, v := range opts.Cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}

	// Ensure outbound calls are trace-instrumented. FindBy installs the debug
	// transport once before the pagination loop; wrap with otelhttp idempotently
	// so paginated pages don't stack transports. No-op when tracing is disabled.
	if _, ok := cli.Transport.(*otelhttp.Transport); !ok {
		cli.Transport = instrumentTransport(cli.Transport)
	}

	if u.SetAuth != nil {
		u.SetAuth(req)
	}

	// Let the paginator modify the request (e.g., add token).
	if err := paginator.UpdateRequest(req); err != nil {
		return Response{}, nil, fmt.Errorf("paginator failed to update request: %w", err)
	}

	start := time.Now()
	resp, err := cli.Do(req)
	if err != nil {
		recordOutboundCall(ctx, httpMethod, path, 0, start)
		return Response{}, nil, fmt.Errorf("making request: %w", err)
	}
	recordOutboundCall(ctx, httpMethod, path, resp.StatusCode, start)
	defer resp.Body.Close()

	getDoc, ok := pathItem.GetOperations().Get(strings.ToLower(httpMethod))
	if !ok {
		return Response{}, nil, fmt.Errorf("operation not found: %s", httpMethod)
	}
	validStatusCodes, err := getValidResponseCode(getDoc.Responses.Codes)
	if err != nil {
		return Response{}, nil, err
	}
	// Merge any per-verb successCodes declared on the RestDefinition (see Call for rationale).
	if opts != nil {
		validStatusCodes = append(validStatusCodes, opts.SuccessCodes...)
	}

	if !HasValidStatusCode(resp.StatusCode, validStatusCodes...) {
		return Response{}, nil, &StatusError{
			StatusCode: resp.StatusCode,
			Inner:      fmt.Errorf("invalid status code: %d", resp.StatusCode),
		}
	}

	// Read the response body as we need to check its content length
	// Just checking if resp.Body is nil does not work, as it can be non-nil with a zero-length body.
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, nil, fmt.Errorf("failed to read response body: %w", err)
	}

	defer resp.Body.Close()

	// Re-wrap body otherwise it will be closed
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// Allow empty body for success codes that may legitimately return no content, even if not common (no MUST NOT in RFCs, so we allow it):
	// - 200 OK: Common for DELETE, PUT, PATCH operations
	// - 201 Created: Some APIs return only Location header
	// - 202 Accepted: Common for async/fire-and-forget operations
	// - 203 Non-Authoritative Information: Transforming proxy responses
	// - 204 No Content: RFC mandates no body
	// - 205 Reset Content: RFC mandates no body
	// - 304 Not Modified: RFC mandates no body
	statusAllowsEmpty := resp.StatusCode == http.StatusOK ||
		resp.StatusCode == http.StatusCreated ||
		resp.StatusCode == http.StatusAccepted ||
		resp.StatusCode == http.StatusNonAuthoritativeInfo ||
		resp.StatusCode == http.StatusNoContent ||
		resp.StatusCode == http.StatusResetContent ||
		resp.StatusCode == http.StatusNotModified

	if len(bodyBytes) == 0 && !statusAllowsEmpty {
		return Response{}, nil, fmt.Errorf("response body is empty for a status code which requires a body: %d", resp.StatusCode)
	}

	// For status codes that allow empty bodies, return nil directly, without going through handleResponse
	if len(bodyBytes) == 0 && statusAllowsEmpty {
		return Response{
			ResponseBody: nil,
			statusCode:   resp.StatusCode,
		}, resp, nil
	}

	err = handleResponse(resp.Body, &response)
	if err != nil {
		return Response{}, nil, fmt.Errorf("handling response: %w", err)
	}

	return Response{
		ResponseBody: response,
		statusCode:   resp.StatusCode,
	}, resp, nil
}

// ExtractItemsFromResponse parses the body of an API response and extracts a list of items.
// It is designed to handle three common API response patterns for list operations:
// 1. A standard JSON array: `[{"id": 1}, {"id": 2}]`. Note: we take the first array we find in the object as we don't know the property name in advance.
// 2. An object wrapping the array: `{"items": [{"id": 1}, {"id": 2}]}`
// 3. A single object, for endpoints that don't use an array for single-item results: `{"id": 1}` (e.g. when the collection only has one item at the moment)
//
// It is a free function (not a method) because it does not depend on any UnstructuredClient state — this
// lets other packages reuse the same list-normalization logic FindBy
// uses, without needing a client instance.
func ExtractItemsFromResponse(body interface{}) ([]interface{}, error) {
	// Case 1: The body is already a standard list (JSON array).
	if list, ok := body.([]interface{}); ok {
		return list, nil
	}

	// Case 2 and 3: The body is an object (map).
	if body == nil {
		return nil, fmt.Errorf("response body is nil")
	}
	if bodyMap, ok := body.(map[string]interface{}); ok {
		if len(bodyMap) == 0 {
			return []interface{}{}, nil
		}

		// Case 2: The body is an object, which may contain a list.
		// Iterate through its values to find the first one that is a list.
		for _, v := range bodyMap {
			if list, ok := v.([]interface{}); ok {
				return list, nil
			}
		}

		// Case 3: If no list was found inside the object, assume the object
		// itself is the single item we are looking for e.g. `{"id": 1}`.
		// Wrap it in a slice to create a list of one, e.g. `[{"id": 1}]`.
		return []interface{}{bodyMap}, nil
	}

	// If the body is not a list or an object, it's an unexpected type.
	return nil, fmt.Errorf("unexpected response type: %T", body)
}

// findItemInList iterates through a slice of items and checks if any of them
// match the identifiers of the local resource.
func (u *UnstructuredClient) findItemInList(items []interface{}) (map[string]interface{}, bool) {
	if len(items) == 0 {
		return nil, false
	}

	for _, item := range items {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			// Skip any elements in the list that are not JSON objects.
			continue
		}

		// Delegate the matching logic for a single item to a dedicated helper.
		isMatch, err := u.isItemMatch(itemMap)
		if err != nil {
			// If an error occurs during comparison, we cannot consider it a match.
			// For now, we log the error and continue searching.
			// log.Printf("error matching item: %v", err) // Optional: for debugging
			continue
		}

		// If a match is found, return the item immediately.
		if isMatch {
			return itemMap, true
		}
	}

	// Return false if no match was found in the entire list.
	return nil, false
}

// isItemMatch checks if a single item (from an API response) matches the local resource
// by comparing all configured identifier fields.
// The match logic can be either "AND" (all identifiers must match) or "OR" (any identifier matches).
// Default is "OR" if not specified.
func (u *UnstructuredClient) isItemMatch(itemMap map[string]interface{}) (bool, error) {
	policy := strings.ToLower(u.IdentifiersMatchPolicy)
	if policy == "" || (policy != "and" && policy != "or") {
		policy = "or" // Default to "or" if not specified or invalid
	}

	// If no identifiers are specified, no match is possible.
	if len(u.IdentifierFields) == 0 {
		// TODO: probably warning or error log
		return false, nil
	}

	switch policy {
	case "and":
		// AND Logic: Return false on the first failed match.
		for _, ide := range u.IdentifierFields {
			pathSegments, err := pathparsing.ParsePath(ide)
			if err != nil || len(pathSegments) == 0 {
				continue
			}

			val, found, err := unstructured.NestedFieldNoCopy(itemMap, pathSegments...)
			if err != nil || !found {
				// If any identifier is missing, it's not an AND match.
				return false, nil
			}

			ok, err := u.isInResource(val, pathSegments...)
			if err != nil {
				// A hard error during comparison should be propagated up.
				return false, err
			}
			if !ok {
				// If any identifier does not match, it's not an AND match.
				return false, nil
			}
		}

		// If the loop completes, it means all identifiers matched (AND logic succeeded).
		return true, nil
	case "or":
		// OR Logic (default): Return true on the first successful identifier match.
		for _, ide := range u.IdentifierFields {

			pathSegments, err := pathparsing.ParsePath(ide)
			if err != nil || len(pathSegments) == 0 {
				continue
			}

			val, found, err := unstructured.NestedFieldNoCopy(itemMap, pathSegments...)
			if err != nil || !found {
				// If field is not found or there is an error, it's not a match for this identifier, so we continue.
				continue
			}

			ok, err := u.isInResource(val, pathSegments...)
			if err != nil {
				// A hard error during comparison should be propagated up. // TODO: is this the desired behavior for OR logic?
				return false, err
			}

			if ok {
				// On the first match, we can return true.
				return true, nil
			}
		}

		// If the loop completes, no identifiers matched (OR logic failed).
		return false, nil
	default:
		return false, fmt.Errorf("unknown identifier match policy: %s", u.IdentifiersMatchPolicy)
	}
}

func jsonToYAML(jsonData []byte) ([]byte, error) {
	// First unmarshal JSON into a generic interface
	var obj interface{}
	if err := json.Unmarshal(jsonData, &obj); err != nil {
		return nil, err
	}

	// Then marshal to YAML
	yamlData, err := rawyaml.Marshal(obj)
	if err != nil {
		return nil, err
	}

	return yamlData, nil
}

// response should be a pointer to the object where the response will be unmarshalled.
func handleResponse(rc io.ReadCloser, response any) error {
	if rc == nil {
		return nil
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}

	yamlData, err := jsonToYAML(data)
	if err != nil {
		return fmt.Errorf("converting JSON to YAML: %w", err)
	}

	err = rawyaml.Unmarshal(yamlData, response)
	if err != nil {
		return fmt.Errorf("unmarshalling YAML response: %w", err)
	}
	return nil
}

type debuggingRoundTripper struct {
	Transport  http.RoundTripper
	Out        io.Writer
	PrettyJSON bool
	// RedactValues are resolved sensitive values (e.g. a secretRef-resolved secret) that must never reach
	// Out in cleartext. Every occurrence in the dumped request is replaced before writing.
	RedactValues []string
}

func (d *debuggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	b, err := httputil.DumpRequestOut(req, true)
	if err != nil {
		return nil, err
	}
	b = redact(b, d.RedactValues)

	d.Out.Write(b)
	d.Out.Write([]byte{'\n'})

	if d.Transport == nil {
		d.Transport = http.DefaultTransport
	}

	resp, err := d.Transport.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	// Dump the response: use pretty JSON if enabled, otherwise use httputil.DumpResponse
	if d.PrettyJSON {
		err = d.dumpResponseWithPrettyJSON(resp, req.URL.Query().Get("watch") != "true")
		if err != nil {
			return nil, err
		}
	} else {
		b, err := httputil.DumpResponse(resp, req.URL.Query().Get("watch") != "true")
		if err != nil {
			return nil, err
		}
		d.Out.Write(b)
		d.Out.Write([]byte{'\n'})
	}

	return resp, nil
}

// redact replaces every occurrence of each non-empty value in values with a fixed-width placeholder, so a
// redacted dump never leaks the original length. Used to keep secretRef-resolved values out of
// verbose/debug request dumps even though they are legitimately sent to the external API.
func redact(b []byte, values []string) []byte {
	for _, v := range values {
		if v == "" {
			continue
		}
		b = bytes.ReplaceAll(b, []byte(v), []byte("***REDACTED***"))
	}
	return b
}

// dumpResponseWithPrettyJSON dumps the HTTP response with pretty-printed JSON body if applicable
func (d *debuggingRoundTripper) dumpResponseWithPrettyJSON(resp *http.Response, dumpBody bool) error {
	if resp == nil {
		return fmt.Errorf("response is nil")
	}

	// Dump status line and headers first
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s %s\r\n", resp.Proto, resp.Status)
	resp.Header.Write(&b)
	b.WriteString("\r\n")

	if !dumpBody || resp.Body == nil {
		// No body to dump
		d.Out.Write(b.Bytes())
		d.Out.Write([]byte{'\n'})
		return nil
	}

	// Read the body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}

	// Close the original body and replace it with a new reader so the response can still be read
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	// Check if the body is JSON and pretty-print it
	prettyBody, isJSON := tryPrettyPrintJSON(bodyBytes)
	if isJSON {
		b.Write(prettyBody)
	} else { // Fallback
		// Not JSON or invalid JSON, write as-is
		b.Write(bodyBytes)
	}

	b.WriteString("\r\n")
	d.Out.Write(b.Bytes())
	d.Out.Write([]byte{'\n'})

	return nil
}

// tryPrettyPrintJSON attempts to pretty-print JSON. Returns the formatted bytes and true if successful.
func tryPrettyPrintJSON(data []byte) ([]byte, bool) {
	// Quick check: if empty or doesn't start with { or [, it's not JSON
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		// Empty body
		return data, false
	}

	firstChar := trimmed[0]
	if firstChar != '{' && firstChar != '[' {
		// Not JSON
		return data, false
	}

	// Try to unmarshal to verify it's valid JSON
	var jsonData interface{}
	if err := json.Unmarshal(data, &jsonData); err != nil {
		// Not valid JSON
		return data, false
	}

	// Pretty-print with 2-space indentation
	prettyJSON, err := json.MarshalIndent(jsonData, "", "  ")
	if err != nil {
		// Marshaling failed (shouldn't happen if unmarshal succeeded, but be safe)
		return data, false
	}

	return prettyJSON, true
}

// TODO: to be re-enabled when libopenapi-validator is stable
// Validate delegates the request validation to the underlying validator.
//func (u *UnstructuredClient) Validate(req *http.Request) (bool, []error) {
//	if u.Validator == nil {
//		// If no validator is configured, assume the request is valid.
//		return true, nil
//	}
//	return u.Validator.ValidateRequest(req)
//}
