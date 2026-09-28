package restResources

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/async"
	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/builder"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/pathparsing"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// asyncConfigForAction returns the Async config declared on the verb matching the given action, or nil.
func asyncConfigForAction(verbs []getter.VerbsDescription, action string) *getter.AsyncConfig {
	for _, v := range verbs {
		if strings.EqualFold(v.Action, action) {
			return v.Async
		}
	}
	return nil
}

// extractPollStatus reads the status value from a poll response body at the given JSONPath.
func extractPollStatus(body map[string]interface{}, statusPath string) (string, error) {
	segs, err := pathparsing.ParsePath(statusPath)
	if err != nil || len(segs) == 0 {
		return "", fmt.Errorf("invalid statusPath %q", statusPath)
	}
	v, found, err := unstructured.NestedFieldNoCopy(body, segs...)
	if err != nil || !found {
		return "", fmt.Errorf("statusPath %q not found in poll response", statusPath)
	}
	// Render scalars cleanly so a numeric status (e.g. 200) compares as "200", not fmt's "2e+02".
	return async.RenderScalar(v), nil
}

// driveAsync drives a long-running operation to completion (design #241, Model A blocking-poll): it
// extracts the operation handle from the trigger response, polls the operations endpoint until it reaches a
// terminal status, and — when PostGet is set — re-reads the resource so status is populated from its real
// state rather than the (transient) operation reference the trigger returned. It returns the response to
// use for subsequent status population.
//
// The poll reuses the trigger call's resolved path parameters (e.g. {organization}) plus the extracted
// {operationId}. The poll path must be declared in the OAS document (it goes through the same client Call).
func driveAsync(ctx context.Context, cli restclient.UnstructuredClientInterface, clientInfo *getter.Info, mg *unstructured.Unstructured, cfg *getter.AsyncConfig, action string, triggerResp restclient.Response, triggerReq *restclient.RequestConfiguration, log logging.Logger) (restclient.Response, error) {
	// The body is only required when the handle is read from the body; a header-based handle (e.g. a 202
	// Accepted with an Operation-Location header and no body) is valid with a nil/absent body.
	body, _ := triggerResp.ResponseBody.(map[string]interface{})
	if strings.EqualFold(cfg.OperationRef.In, "body") && body == nil {
		if triggerResp.ResponseBody == nil {
			return triggerResp, fmt.Errorf("async trigger returned an empty body (e.g. 204 No Content); operationRef.in=body cannot extract a handle — use operationRef.in=header for header-based handles")
		}
		return triggerResp, fmt.Errorf("async trigger response body is not an object (got %T)", triggerResp.ResponseBody)
	}
	opID, err := async.ExtractOperationHandle(ctx, body, triggerResp.Headers, cfg.OperationRef)
	if err != nil {
		return triggerResp, fmt.Errorf("extracting async operation handle: %w", err)
	}
	log.Debug("Async operation started, polling to completion", "operationId", opID, "pollPath", cfg.Poll.Path)

	// The poll re-uses the trigger call's resolved parameters, query, headers and cookies so a poll
	// endpoint that shares required inputs with the trigger (e.g. a required ?api-version query param, or
	// an auth header) still validates. The extracted {operationId} path param takes precedence.
	pollClient := &http.Client{Timeout: async.RequestTimeout}
	poll := func(ctx context.Context, id string) (string, error) {
		pollReq := buildPollRequest(cfg.Poll.Method, cfg.Poll.HandleParam, id, triggerReq)
		resp, perr := cli.Call(ctx, pollClient, cfg.Poll.Path, pollReq)
		if perr != nil {
			return "", perr
		}
		pb, ok := resp.ResponseBody.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("poll response body is not an object")
		}
		return extractPollStatus(pb, cfg.Poll.StatusPath)
	}

	if err := async.PollUntilTerminal(ctx, cfg.Poll, opID, poll); err != nil {
		return triggerResp, err
	}
	log.Debug("Async operation reached terminal success", "operationId", opID)

	// PostGet re-reads the resource so status reflects its real state. It is meaningless (and harmful)
	// for delete: a successful async delete has removed the resource, so a get would 404 and block the
	// delete/finalizer forever. Skip it unconditionally for the delete verb.
	if !cfg.PostGet || strings.EqualFold(action, "delete") {
		return triggerResp, nil
	}

	getCall, getInfo, gerr := builder.APICallBuilder(cli, clientInfo, apiaction.Get)
	if gerr != nil {
		return triggerResp, fmt.Errorf("async postGet: building get call: %w", gerr)
	}
	if getCall == nil || getInfo == nil {
		return triggerResp, fmt.Errorf("async postGet requested but no get action is defined")
	}
	getReq := builder.BuildCallConfig(ctx, getInfo, mg, clientInfo.ConfigurationSpec, nil)
	getResp, gerr := getCall(ctx, &http.Client{Timeout: async.RequestTimeout}, getInfo.Path, getReq)
	if gerr != nil {
		return triggerResp, fmt.Errorf("async postGet: get call: %w", gerr)
	}
	return getResp, nil
}

// buildPollRequest builds the RequestConfiguration for a single poll of the operations endpoint. It binds
// the extracted {operationId} path param and re-uses base's resolved path params, query, headers and cookies
// (nil-safe) so a poll endpoint sharing a required input with base still validates. base is the trigger
// request (Model A) or the get request (Model B) — both resolve the same shared params (e.g. {organization},
// ?api-version). The maps are copied so the poll request never mutates base's shared maps.
// defaultHandleParam is the path-parameter name the async operation handle binds to when a RestDefinition
// does not declare poll.handleParam. It is the name every configuration written before that field existed
// relies on, so it stays the default rather than becoming required.
//
// Note it is NOT the OAS `operationId` keyword, despite the spelling: that identifies an operation
// definition and is optional in OAS; this is the name of a PATH PARAMETER. Hardcoding it used to force the
// OAS document to spell the poll endpoint's parameter "operationId" — vendor specs name it whatever they
// like (Aruba's baremetal API uses .../monitor/{id}) and had to be patched to work.
const defaultHandleParam = "operationId"

func buildPollRequest(pollMethod, handleParam, operationID string, base *restclient.RequestConfiguration) *restclient.RequestConfiguration {
	if pollMethod == "" {
		pollMethod = "GET"
	}
	if handleParam == "" {
		handleParam = defaultHandleParam
	}
	params := map[string]string{handleParam: operationID}
	if base != nil {
		for k, v := range base.Parameters {
			if _, exists := params[k]; !exists {
				params[k] = v
			}
		}
	}
	return &restclient.RequestConfiguration{
		Method:     pollMethod,
		Parameters: params,
		Query:      copyStringMap(reqQuery(base)),
		Headers:    copyStringMap(reqHeaders(base)),
		Cookies:    copyStringMap(reqCookies(base)),
	}
}

// copyStringMap returns a shallow copy of m (nil-safe).
func copyStringMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func reqQuery(r *restclient.RequestConfiguration) map[string]string {
	if r == nil {
		return nil
	}
	return r.Query
}

func reqHeaders(r *restclient.RequestConfiguration) map[string]string {
	if r == nil {
		return nil
	}
	return r.Headers
}

func reqCookies(r *restclient.RequestConfiguration) map[string]string {
	if r == nil {
		return nil
	}
	return r.Cookies
}
