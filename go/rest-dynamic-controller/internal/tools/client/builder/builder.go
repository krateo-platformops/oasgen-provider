package builder

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	unstructuredtools "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/deepcopy"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/fieldmapping"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/pathparsing"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/text"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

type RequestedParams struct {
	Parameters text.StringSet // `Parameters` are the path parameters
	Query      text.StringSet
	Headers    text.StringSet
	Cookies    text.StringSet
	Body       text.StringSet
}

type CallInfo struct {
	Path                string
	ReqParams           *RequestedParams
	IdentifierFields    []string
	RequestFieldMapping []getter.RequestFieldMappingItem // Deprecated: mirrored for backward compatibility; prefer FieldMapping.
	FieldMapping        []getter.FieldMappingItem        // FieldMapping is the unified request/response mapping; only request-direction entries (inPath/inQuery/inBody) apply here.
	Method              string
	Action              apiaction.APIAction
	SuccessCodes        []int               // extra status codes accepted as success for this verb (merged with OAS 2xx)
	Headers             []getter.HeaderItem // static per-verb headers to inject on the request
	Queries             []getter.QueryParam // static per-verb query params to inject on the request
	TolerateCodes       []int               // status codes treated as a successful empty response for this verb
	NotFoundCodes       []int               // status codes remapped to a not-found result for this verb
	// RequestTransform is this verb's whole-document jq program for the outgoing body. It is NOT applied by
	// BuildCallConfig — that has no context to run jq with — but by the caller, via
	// fieldmapping.ApplyRequestTransform, after the body is assembled and immediately before the call.
	RequestTransform *getter.JQProgram
}

type APIFuncDef func(ctx context.Context, cli *http.Client, path string, conf *restclient.RequestConfiguration) (restclient.Response, error)

// APICallBuilder builds the API call based on the action and the info from the RestDefinition
func APICallBuilder(cli restclient.UnstructuredClientInterface, info *getter.Info, action apiaction.APIAction) (apifunc APIFuncDef, callInfo *CallInfo, err error) {
	identifierFields := info.Resource.Identifiers
	for _, descr := range info.Resource.VerbsDescription {
		if strings.EqualFold(descr.Action, action.String()) {
			params, query, headers, cookies, err := cli.RequestedParams(descr.Method, descr.Path)
			if err != nil {
				return nil, nil, fmt.Errorf("retrieving requested params: %s", err)
			}
			var body text.StringSet
			if descr.Method == "POST" || descr.Method == "PUT" || descr.Method == "PATCH" {
				body, err = cli.RequestedBody(descr.Method, descr.Path)
				if err != nil {
					return nil, nil, fmt.Errorf("retrieving requested body params: %s", err)
				}
				if body == nil {
					body = text.StringSet{}
				}
			}

			callInfo := &CallInfo{
				Path:   descr.Path,
				Method: descr.Method,
				Action: action,
				ReqParams: &RequestedParams{
					Parameters: params, // Path parameters
					Query:      query,
					Headers:    headers,
					Cookies:    cookies,
					Body:       body,
				},
				IdentifierFields:    identifierFields,
				RequestFieldMapping: descr.RequestFieldMapping,
				FieldMapping:        descr.FieldMapping,
				SuccessCodes:        descr.SuccessCodes,
				Headers:             descr.Headers,
				Queries:             descr.Queries,
				TolerateCodes:       descr.TolerateCodes,
				NotFoundCodes:       descr.NotFoundCodes,
				RequestTransform:    descr.RequestTransform,
			}

			switch action {
			case apiaction.FindBy:
				// Specialized FindBy function, we need to pass also the description in this case so we use a closure.
				// We return a function that captures the `descr` variable from the surrounding scope
				// and uses it when the returned function is called but still conforms to the APIFuncDef signature.
				return func(ctx context.Context, httpClient *http.Client, path string, conf *restclient.RequestConfiguration) (restclient.Response, error) {
					return cli.FindBy(ctx, httpClient, path, conf, &descr)
				}, callInfo, nil
			default:
				return cli.Call, callInfo, nil // Generic Call function
			}
		}
	}
	return nil, nil, nil
}

// UnresolvedPathParams returns, in order of appearance, the names of the {placeholder} segments in a path
// template that params does not populate — treating an empty value as unpopulated, since substituting it
// would silently address a different URL (".../users/" rather than ".../users/{id}").
//
// A non-empty result means the external resource is NOT ADDRESSABLE: the request cannot be aimed at
// anything, so sending it is pointless. Callers use this to distinguish "the call would fail" from "the
// call failed", which matters most on delete — a CR whose create never succeeded has no identifier in its
// status, and blocking its teardown on a request that can never be built strands the CR forever.
func UnresolvedPathParams(path string, params map[string]string) []string {
	var missing []string
	for i := 0; i < len(path); i++ {
		if path[i] != '{' {
			continue
		}
		end := strings.IndexByte(path[i:], '}')
		if end < 0 {
			break // unterminated placeholder: nothing addressable to report beyond this point
		}
		name := path[i+1 : i+end]
		if name != "" && params[name] == "" {
			missing = append(missing, name)
		}
		i += end
	}
	return missing
}

// BuildCallConfig builds the request configuration based on the callInfo and the fields from the spec and
// status of the main resource, the spec of the Configuration CR and also the request field mappings.
//
// resolved carries the values already produced by fieldmapping.ResolveRequestResolvers for this same
// callInfo.FieldMapping slice (keyed by fieldmapping.ResolverKey), for FieldMapping entries whose Resolver
// (secretRef) needs I/O this synchronous function cannot perform itself. Pass nil at
// call sites that don't resolve resolvers (e.g. the async/observe paths) — a resolver-bearing entry is
// then simply skipped rather than sent unresolved.
//
// It does NOT apply callInfo.RequestTransform: running jq needs a context this function does not take.
// Callers that send a body apply it afterwards via fieldmapping.ApplyRequestTransform.
func BuildCallConfig(ctx context.Context, callInfo *CallInfo, mg *unstructured.Unstructured, configSpec map[string]interface{}, resolved map[string]interface{}) *restclient.RequestConfiguration {
	if callInfo == nil || mg == nil {
		return nil
	}

	// Initialize the request configuration with empty maps for each parameter type.
	reqConfiguration := &restclient.RequestConfiguration{}
	reqConfiguration.Parameters = make(map[string]string) // Path parameters
	reqConfiguration.Query = make(map[string]string)
	reqConfiguration.Headers = make(map[string]string)
	reqConfiguration.Cookies = make(map[string]string)
	reqConfiguration.Method = callInfo.Method
	reqConfiguration.SuccessCodes = callInfo.SuccessCodes
	reqConfiguration.TolerateCodes = callInfo.TolerateCodes
	reqConfiguration.NotFoundCodes = callInfo.NotFoundCodes
	mapBody := make(map[string]interface{})

	// 1. Apply fields from the Configuration CR.
	applyConfigSpec(reqConfiguration, configSpec, callInfo.Action.String())

	// 1b. Inject static per-verb headers (explicit RD config wins over any config-derived header).
	for _, h := range callInfo.Headers {
		if h.Name != "" {
			reqConfiguration.Headers[h.Name] = h.Value
		}
	}

	// 1c. Inject static per-verb query parameters (set before spec/status processing so they win).
	for _, q := range callInfo.Queries {
		if q.Name != "" {
			reqConfiguration.Query[q.Name] = q.Value
		}
	}

	specFields, err := unstructuredtools.GetFieldsFromUnstructured(mg, "spec")
	if err != nil {
		specFields = make(map[string]interface{}) // Initialize as empty map if error when retrieving spec
	}

	// TODO: debug prints
	//log.Printf("Spec fields retrieved from unstructured:\n")
	//for k, v := range specFields {
	//	log.Printf("Spec field key: %s, value: %v\n", k, v)
	//}

	statusFields, err := unstructuredtools.GetFieldsFromUnstructured(mg, "status")
	if err != nil {
		statusFields = make(map[string]interface{}) // Initialize as empty map if error when retrieving status
	}

	// 3. Apply values from the main resource's spec (spec takes precedence over status in case of duplicates).
	processFields(callInfo, specFields, reqConfiguration, mapBody)

	// 4. Apply values from the main resource's status
	processFields(callInfo, statusFields, reqConfiguration, mapBody)

	// 5. Apply explicit field mappings LAST, so they win by write order.
	//
	// These used to run before auto-population, with processFields underlaying the spec beneath what was
	// written. Ordering them last is equivalent for precedence and strictly better for addressing: a
	// [?key=value] predicate can only resolve against an array that already exists, and before
	// auto-population the body is empty, so every predicate write would fail. Populating first also
	// removes the need to merge, since a later write simply overwrites the specific path it targets and
	// leaves every sibling that processFields put there untouched.
	applyRequestFieldMapping(callInfo, mg, reqConfiguration, mapBody) // deprecated RequestFieldMapping
	applyFieldMapping(ctx, callInfo, mg, reqConfiguration, mapBody, resolved)

	// 6. Drop the CR-side source of every Resolver entry from the body. A resolver's
	// inCustomResource is a POINTER the controller dereferences (a secretRef's {name,key}, an
	// a secretRef's {name,key}) — the dereferenced result is already written at the entry's inBody, so the
	// pointer itself is CR-domain plumbing that no API expects. Auto-population (step 3) cannot know
	// that and would forward it verbatim, which strict APIs reject outright: Keycloak answers 400 to a
	// CredentialRepresentation carrying an unknown `valueSecretRef`. Only Resolver entries are
	// stripped — a plain relocation's source stays, since it is ordinary API-bound data.
	stripResolverSources(callInfo, mapBody)

	// 5. Set the body in the request configuration
	reqConfiguration.Body = mapBody

	return reqConfiguration
}

// applyRequestFieldMapping populates the request configuration from the request field mappings.
func applyRequestFieldMapping(callInfo *CallInfo, mg *unstructured.Unstructured, reqConfiguration *restclient.RequestConfiguration, mapBody map[string]interface{}) {
	if callInfo.RequestFieldMapping == nil {
		return
	}

	// Iterate over the request field mappings
	for _, mapping := range callInfo.RequestFieldMapping {
		pathSegments, err := pathparsing.ParsePath(mapping.InCustomResource)
		if len(pathSegments) == 0 {
			continue
		}

		val, found, err := pathparsing.GetNestedField(mg.Object, pathSegments)
		if err != nil || !found {
			continue
		}

		if mapping.InPath != "" {
			// Parse InPath with pathparsing to be consistent with dot notation handling
			inPathSegments, err := pathparsing.ParsePath(mapping.InPath)
			if err != nil || len(inPathSegments) == 0 {
				continue
			}

			// It should be a single segment for path parameters since path parameters are flat
			if len(inPathSegments) != 1 {
				continue
			}

			mapping.InPath = inPathSegments[0]
			strVal := fmt.Sprintf("%v", val)
			reqConfiguration.Parameters[mapping.InPath] = strVal

		} else if mapping.InQuery != "" {
			// Parse InQuery with pathparsing to be consistent with dot notation handling
			inQuerySegments, err := pathparsing.ParsePath(mapping.InQuery)
			if err != nil || len(inQuerySegments) == 0 {
				continue
			}

			// It should be a single segment for query parameters since query parameters are flat
			if len(inQuerySegments) != 1 {
				continue
			}

			mapping.InQuery = inQuerySegments[0]
			strVal := fmt.Sprintf("%v", val)
			reqConfiguration.Query[mapping.InQuery] = strVal

		} else if mapping.InBody != "" {
			// Parse InBody with pathparsing to be consistent with dot notation handling
			inBodySegments, err := pathparsing.ParsePath(mapping.InBody)
			if err != nil || len(inBodySegments) == 0 {
				continue
			}

			// Perform deep copy and type conversions (e.g., float64 to int64).
			// This is needed since we will set the value in the body map and therefore we need to ensure the types are correct.
			// On the other hand, for path and query parameters we convert everything to string.
			convertedValue := deepcopy.DeepCopyJSONValue(val)

			// Debug prints
			//for k, v := range mapBody {
			//	log.Printf("Before setting, mapBody key: %s, value: %v\n", k, v)
			//}

			// Set the value in the body map at the correct nested path
			err = pathparsing.SetNestedField(mapBody, convertedValue, inBodySegments)
			if err != nil {
				continue
			}

			// Debug prints
			//for k, v := range mapBody {
			//	log.Printf("mapBody key: %s, value: %v\n", k, v)
			//}
		}
	}
}

// describeMapping names a mapping entry the way an operator would recognise it, for error messages.
func describeMapping(m getter.FieldMappingItem) string {
	switch {
	case m.InPath != "":
		return fmt.Sprintf("path parameter %q", m.InPath)
	case m.InQuery != "":
		return fmt.Sprintf("query parameter %q", m.InQuery)
	case m.InBody != "":
		return fmt.Sprintf("body field %q", m.InBody)
	default:
		return "field mapping entry"
	}
}

// applyFieldMapping populates the request configuration from the unified FieldMapping's request-direction
// entries (inPath/inQuery/inBody set; inResponse-only entries are for the response side and are ignored
// here). It mirrors applyRequestFieldMapping's write targets and validation exactly, adding the Tier-1
// alias value transform, Tier-2 jq, and (when resolved is populated) Resolver (secretRef) values.
//
// A jq program that fails to compile, fails to run, or returns a value that cannot address a URL sets
// reqConfiguration.BuildErr and stops, rather than leaving the field unwritten. Skipping was the previous
// behaviour and it was wrong in the most expensive way available: a missing path parameter still yields a
// URL that parses, so the API answers 404, and the reconciler acts on not-found by creating (#117).
//
// A Resolver entry with no matching resolved value (resolved is nil, or the caller's resolve pass didn't
// cover this entry) is still skipped -- that is an ordinary "not applicable here", not a broken
// declaration.
func applyFieldMapping(ctx context.Context, callInfo *CallInfo, mg *unstructured.Unstructured, reqConfiguration *restclient.RequestConfiguration, mapBody map[string]interface{}, resolved map[string]interface{}) {
	for _, mapping := range callInfo.FieldMapping {
		if mapping.InPath == "" && mapping.InQuery == "" && mapping.InBody == "" {
			continue // response-direction (inResponse) entry, handled elsewhere
		}

		var val interface{}
		// fromSecret drives redaction of the value AFTER any transform, not only before it. See the
		// note below the valueMapping switch.
		fromSecret := mapping.Resolver != nil && mapping.Resolver.Type == "secretRef"
		if mapping.Resolver != nil {
			v, ok := resolved[fieldmapping.ResolverKey(mapping)]
			if !ok {
				continue
			}
			val = v
			if fromSecret {
				if s, ok := val.(string); ok {
					reqConfiguration.SensitiveValues = append(reqConfiguration.SensitiveValues, s)
				}
			}
		} else {
			pathSegments, err := pathparsing.ParsePath(mapping.InCustomResource)
			if err != nil || len(pathSegments) == 0 {
				continue
			}
			v, found, err := pathparsing.GetNestedField(mg.Object, pathSegments)
			if err != nil || !found {
				continue
			}
			val = v
		}

		if mapping.ValueMapping != nil {
			switch mapping.ValueMapping.Type {
			case "alias":
				val = fieldmapping.ApplyAlias(val, mapping.ValueMapping.Aliases, fieldmapping.RequestCRToAPI)
			case "jq":
				// Request-direction jq. This used to be skipped outright, which meant a declared transform
				// silently produced NO field at all: a path parameter simply absent, a URL that still
				// parses, a 404, and a reconciler that creates on 404. The one part of the request surface
				// with no transformation hook was path and query parameters, so "strip a prefix before
				// calling" needed a Go plugin to run strings.TrimPrefix (#117).
				out, jerr := fieldmapping.ApplyValueJQ(ctx, mapping.ValueMapping.JQ, val, describeMapping(mapping))
				if jerr != nil {
					reqConfiguration.BuildErr = jerr
					return
				}
				val = out
			default:
				// Unreachable through the CRD (the enum admits alias and jq only), so this is a definition
				// that bypassed admission. Refuse rather than send a value that should have been
				// transformed and was not.
				reqConfiguration.BuildErr = fmt.Errorf("%s declares an unsupported valueMapping type %q",
					describeMapping(mapping), mapping.ValueMapping.Type)
				return
			}

			// A transform of a secret produces ANOTHER secret, and it is the transformed value that goes
			// on the wire. Registering only the pre-transform value would redact the raw credential from
			// request logging while printing, say, its base64 encoding -- redaction that looks like it is
			// working and is not.
			//
			// This became reachable only when request-direction jq started running (#117): before that a
			// resolver+valueMapping entry was skipped entirely, so the combination never executed. The
			// transform is exactly what someone reaches for to encode a credential the API wants wrapped,
			// so it is the likely use rather than an exotic one.
			//
			// Appending both is deliberate: redaction matches by value, the pre-transform secret may still
			// appear elsewhere, and a duplicate costs nothing.
			if fromSecret {
				if s, ok := val.(string); ok && s != "" {
					reqConfiguration.SensitiveValues = append(reqConfiguration.SensitiveValues, s)
				}
			}
		}

		switch {
		case mapping.InPath != "":
			inPathSegments, err := pathparsing.ParsePath(mapping.InPath)
			if err != nil || len(inPathSegments) != 1 {
				continue
			}
			str, serr := fieldmapping.ScalarForURL(val, describeMapping(mapping))
			if serr != nil {
				reqConfiguration.BuildErr = serr
				return
			}
			reqConfiguration.Parameters[inPathSegments[0]] = str

		case mapping.InQuery != "":
			inQuerySegments, err := pathparsing.ParsePath(mapping.InQuery)
			if err != nil || len(inQuerySegments) != 1 {
				continue
			}
			str, serr := fieldmapping.ScalarForURL(val, describeMapping(mapping))
			if serr != nil {
				reqConfiguration.BuildErr = serr
				return
			}
			reqConfiguration.Query[inQuerySegments[0]] = str

		case mapping.InBody != "":
			inBodySegments, err := pathparsing.ParsePath(mapping.InBody)
			if err != nil || len(inBodySegments) == 0 {
				continue
			}
			convertedValue := deepcopy.DeepCopyJSONValue(val)
			if err := pathparsing.SetNestedField(mapBody, convertedValue, inBodySegments); err != nil {
				continue
			}
		}
	}
}

// applyConfigSpec populates the request configuration from a configuration spec map (coming from the Configuration CR)
func applyConfigSpec(req *restclient.RequestConfiguration, configSpec map[string]interface{}, action string) {
	if configSpec == nil {
		return
	}

	// Internal helper
	process := func(key string, dest map[string]string) {
		if actionConfig, found, err := unstructured.NestedMap(configSpec, key, action); err == nil && found && actionConfig != nil {
			for k, v := range actionConfig {
				stringVal := fmt.Sprintf("%v", v) // Convert any type to string
				dest[k] = stringVal
			}
		}
	}

	process("path", req.Parameters)
	process("query", req.Query)
	process("headers", req.Headers)
	process("cookies", req.Cookies)
}

// IsResourceKnown tries to build the `get` action API Call, with the given specFields and statusFields values.
// If it is able to build the `get` action request, returns true, false otherwise.
// Usually the `get` action is used to retrieve the resource by its unique identifier (usually server-side generated and assigned, e.g., ID, UUID).
// Therefore "known" in this case means that the resource can be retrieved by this kind of identifiers.
// This function is used during the reconciliation (in the Observe phase) to decide:
// - if the resource can be retrieved by its unique identifier (usually server-side generated and assigned) (e.g GET /resources/{id})
// - or if it needs to be found by its "findby" identifiers fields (e.g., unique name within a organization) in a list of resources (e.g GET /resources)
func IsResourceKnown(ctx context.Context, cli restclient.UnstructuredClientInterface, clientInfo *getter.Info, mg *unstructured.Unstructured) bool {
	if mg == nil || clientInfo == nil {
		return false
	}

	apiCall, callInfo, err := APICallBuilder(cli, clientInfo, apiaction.Get)
	if apiCall == nil || err != nil {
		return false
	}

	reqConfiguration := BuildCallConfig(ctx, callInfo, mg, clientInfo.ConfigurationSpec, nil)
	if reqConfiguration == nil || reqConfiguration.BuildErr != nil {
		return false
	}

	return cli.ValidateRequest(callInfo.Method, callInfo.Path, reqConfiguration.Parameters, reqConfiguration.Query, reqConfiguration.Headers, reqConfiguration.Cookies) == nil
}

// processFields processes the given fields map (spec or status fields) and populates the request configuration accordingly.
func processFields(callInfo *CallInfo, fields map[string]interface{}, reqConfiguration *restclient.RequestConfiguration, mapBody map[string]interface{}) {
	for field, value := range fields {
		if field == "" {
			continue
		}

		if callInfo.ReqParams.Parameters.Contains(field) {
			if _, ok := reqConfiguration.Parameters[field]; !ok { // Avoid overwriting existing values
				reqConfiguration.Parameters[field] = fmt.Sprintf("%v", value)
				//log.Printf("Setting path parameter field %s to value %v from resource\n", field, value)
			}
		}

		if callInfo.ReqParams.Query.Contains(field) {
			if _, ok := reqConfiguration.Query[field]; !ok { // Avoid overwriting existing values
				reqConfiguration.Query[field] = fmt.Sprintf("%v", value)
			}
		}

		// Note: probably headers and cookies are better to be set ONLY in the Configuration CR spec
		// (and currently it is only possible there)
		// Therefore, we do not set them here since we are processing only the main resource fields (spec/status) with this function.

		if callInfo.ReqParams.Body.Contains(field) {
			// Nil-guard so spec (processed first) beats status. FieldMapping precedence is NOT handled
			// here — it comes from those mappings being applied after this, in BuildCallConfig.
			if mapBody[field] == nil {
				mapBody[field] = value
			}
		}
	}
}

// stripResolverSources removes, from the request body, the CR-side path that each Resolver entry reads
// from. Paths are spec/status-relative once in the body (auto-population lifts spec fields to the top
// level), so a leading "spec."/"status." segment is trimmed before removal. Entries without a Resolver
// are left alone.
func stripResolverSources(callInfo *CallInfo, mapBody map[string]interface{}) {
	for _, mapping := range callInfo.FieldMapping {
		if mapping.Resolver == nil || mapping.InCustomResource == "" {
			continue
		}
		trimmed := strings.TrimPrefix(strings.TrimPrefix(mapping.InCustomResource, "spec."), "status.")
		segs, err := pathparsing.ParsePath(trimmed)
		if err != nil || len(segs) == 0 {
			continue
		}
		pathparsing.RemoveNestedField(mapBody, segs)
	}
}
