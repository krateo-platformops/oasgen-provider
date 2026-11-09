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
	skipped := processFields(callInfo, specFields, reqConfiguration, mapBody)

	// 4. Apply values from the main resource's status
	skipped = append(skipped, processFields(callInfo, statusFields, reqConfiguration, mapBody)...)

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

	// 5b. THE VERDICT ON A COMPOSITE, taken here and not where it was found.
	//
	// A composite value cannot address a URL, a query string, a header or a cookie, so processFields
	// declines to write one. But declining is not yet a failure: the mappings above run AFTER
	// auto-population, by design and for stated reasons, and a request-direction mapping writes the
	// parameter unconditionally. So a kind whose mapped parameter reads from a nested status field --
	// `owner` from `status.owner.login` -- ends up with exactly the right value.
	//
	// Deciding at the point of discovery refused those requests. Their parameters were CORRECT and
	// BuildErr was set from a value that no longer existed, and the message told the author to add the
	// mapping they already had. Measured on a real definition by the github-provider-kog session:
	// Parameters=map[name:my-repo owner:octocat] with BuildErr non-nil, against a clean parent.
	//
	// So the verdict belongs to the ASSEMBLED configuration, after every writer. A skipped field that is
	// still absent is a real failure; one some later writer supplied is not. This also means a future
	// writer added below is covered automatically, where a check at the discovery site would have to be
	// taught about it.
	refuseUnwrittenComposites(reqConfiguration, skipped)

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
		pathSegments, err := pathparsing.ParseTyped(mapping.InCustomResource)
		if len(pathSegments) == 0 {
			continue
		}

		val, found, err := pathparsing.GetTyped(mg.Object, pathSegments)
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
			pathSegments, err := pathparsing.ParseTyped(mapping.InCustomResource)
			if err != nil || len(pathSegments) == 0 {
				continue
			}
			v, found, err := pathparsing.GetTyped(mg.Object, pathSegments)
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

	// The keys are the OpenAPI parameter LOCATIONS, which the specification defines as singular: "path",
	// "query", "header", "cookie". oasgen generates the configuration schema straight from them
	// (configuration_builder.go: `paramIn := fieldParam.In`), so those are the property names a generated
	// Configuration CRD actually declares.
	//
	// "headers" and "cookies" were plural here, so every configurationFields entry for a header or cookie
	// parameter was generated into the Configuration CRD and then NEVER APPLIED. path and query worked only
	// because OpenAPI's singular happens to match what this code asked for.
	//
	// Nothing could have depended on the plural form: the generated Configuration CRD declares `header`,
	// so a CR written with `headers:` is rejected by the apiserver as an unknown field under strict
	// decoding. The feature was not merely unused for headers and cookies -- it was unusable (#304).
	//
	// MEASURED BLAST RADIUS, which is why this is not a typo fix. The openstack-ironic-operator-kog session
	// declared their Ironic microversion as a configurationFields header. It was silently dropped, so
	// Ironic negotiated its MINIMUM (1.1), every port create answered 406, the node ended up with no port,
	// and the deploy failed with "does not have any port associated with it" -- three layers from the cause,
	// naming a missing port rather than a dropped header.
	process("path", req.Parameters)
	process("query", req.Query)
	process("header", req.Headers)
	process("cookie", req.Cookies)
}

// IsResourceKnown tries to build the `get` action API Call, with the given specFields and statusFields values.
// If it is able to build the `get` action request, returns true, false otherwise.
// Usually the `get` action is used to retrieve the resource by its unique identifier (usually server-side generated and assigned, e.g., ID, UUID).
// Therefore "known" in this case means that the resource can be retrieved by this kind of identifiers.
// This function is used during the reconciliation (in the Observe phase) to decide:
// - if the resource can be retrieved by its unique identifier (usually server-side generated and assigned) (e.g GET /resources/{id})
// - or if it needs to be found by its "findby" identifiers fields (e.g., unique name within a organization) in a list of resources (e.g GET /resources)
// IsResourceKnown reports whether the CR carries enough identity to issue the get verb.
//
// The error return is not decoration. "false" here means "the identity is not known yet", which routes
// observe to findby and, with no findby declared, to assume-needs-creating -- i.e. a CREATE. So anything
// that returns false had better actually mean the identity is absent.
//
// A BROKEN DECLARATION is not that. A request-direction jq that does not compile, fails, or returns a
// value with no URL form sets RequestConfiguration.BuildErr; reporting that as "not known" hands it to
// the same create path, which is precisely the silent-drop-then-create failure #117 set out to remove --
// relocated from the Call to this routing gate, where the Call's own guard never runs. Found on a live
// cluster: a get verb with a deliberately broken jq silently attempted a create instead of reporting the
// jq error.
//
// So the two answers are separated. err != nil means "this definition is wrong, surface it"; (false, nil)
// means "no identity yet", which is the only one that may lead to a create.
func IsResourceKnown(ctx context.Context, cli restclient.UnstructuredClientInterface, clientInfo *getter.Info, mg *unstructured.Unstructured) (bool, error) {
	if mg == nil || clientInfo == nil {
		return false, nil
	}

	apiCall, callInfo, err := APICallBuilder(cli, clientInfo, apiaction.Get)
	if apiCall == nil || err != nil {
		return false, nil
	}

	reqConfiguration := BuildCallConfig(ctx, callInfo, mg, clientInfo.ConfigurationSpec, nil)
	if reqConfiguration == nil {
		return false, nil
	}
	if reqConfiguration.BuildErr != nil {
		// A declaration that cannot produce a request. Never "not known".
		return false, reqConfiguration.BuildErr
	}

	return cli.ValidateRequest(callInfo.Method, callInfo.Path, reqConfiguration.Parameters, reqConfiguration.Query, reqConfiguration.Headers, reqConfiguration.Cookies) == nil, nil
}

// The four string-valued destinations processFields writes. Named rather than passed as strings so a
// typo cannot silently create a destination that refuseUnwrittenComposites never checks.
const (
	destPath   = "path parameter"
	destQuery  = "query parameter"
	destHeader = "header"
	destCookie = "cookie"
)

// skippedComposite records a field processFields declined to write because its value was a composite.
// It carries the value so the eventual message can show what arrived, and the destination so the final
// check looks in the right map.
type skippedComposite struct {
	dest  string
	field string
	value any
}

// refuseUnwrittenComposites sets BuildErr for any declined field that NO later writer supplied.
//
// Called once, after every writer in BuildCallConfig. See the call site for why the verdict cannot be
// taken where the composite is found. Leaves an existing BuildErr alone: the first failure to assemble
// a request is the one worth reporting, and the later ones are usually its consequences.
func refuseUnwrittenComposites(cfg *restclient.RequestConfiguration, skipped []skippedComposite) {
	if cfg == nil || cfg.BuildErr != nil {
		return
	}
	for _, s := range skipped {
		var present bool
		switch s.dest {
		case destPath:
			_, present = cfg.Parameters[s.field]
		case destQuery:
			_, present = cfg.Query[s.field]
		case destHeader:
			_, present = cfg.Headers[s.field]
		case destCookie:
			_, present = cfg.Cookies[s.field]
		default:
			// An unrecognised destination cannot be checked, so it cannot be cleared either. Refuse
			// rather than pass: a destination added without teaching this function about it must not
			// become a silent hole.
			cfg.BuildErr = fmt.Errorf("internal: a composite value was declined for %q with unknown "+
				"destination %q, so it cannot be confirmed resolved", s.field, s.dest)
			return
		}
		if !present {
			cfg.BuildErr = nonScalarParamErr(s.dest, s.field, s.value)
			return
		}
	}
}

// scalarParamValue renders value for use in a URL path segment, query string, header or cookie, and
// reports whether it can be rendered at all.
//
// Applied to FOUR of the five destinations processFields writes, and deliberately NOT to the fifth:
// ReqParams.Body takes the value as-is into the JSON request body, where a map or a slice is exactly
// what an author means. The other four are string-valued (Parameters, Query, Headers and Cookies are
// all map[string]string), so something has to render a composite as text and there is no rendering
// that is correct.
//
// Parameters and Query are map[string]string, so SOMETHING has to turn an arbitrary unstructured value
// into text. `fmt.Sprintf("%v", value)` did it unconditionally, which is fine for every scalar and
// silently wrong for a composite: a map renders as `map[login:octocat]` and a slice as `[a b]`. Those are
// not malformed URLs -- they are syntactically valid ones addressing something that does not exist, so
// the vendor answers 404 and the reconciler acts on not-found by CREATING, which is the #117 shape.
//
// The reachable route is a dotted additionalStatusFields entry (#463): `owner.login` renders status as a
// NESTED object, so the top-level status key is `owner`, which matches a parameter named `owner` -- and
// the value handed over is the whole map. A requestFieldMapping on that verb overwrites it afterwards and
// hides this, which is why it has not been seen in the field.
//
// Only the types unstructured data can actually hold are accepted; k8s decodes JSON numbers as int64 or
// float64, never int, so listing the other numeric kinds would be dead code. An explicit allowlist rather
// than "anything that is not a map or slice", so a type nobody considered fails closed.
func scalarParamValue(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case bool, int64, float64:
		return fmt.Sprintf("%v", v), true
	case nil:
		// An explicitly null field supplies no value. Empty string rather than "<nil>", which would
		// address a literal "<nil>" segment.
		return "", true
	default:
		return "", false
	}
}

// nonScalarParamErr explains the collision in the author's own vocabulary, because the value in the
// message is not something they wrote anywhere -- it is a rendering of a status object they declared one
// leaf of. Naming additionalStatusFields is what makes the message actionable.
//
// Only ever reached for a field NOTHING supplied, so the suggestion to add a mapping is always live
// advice. An earlier version could fire on a field a mapping had already supplied, which told the author
// to add the mapping they had.
func nonScalarParamErr(kind, field string, value any) error {
	return fmt.Errorf("%s %q would be set from a non-scalar value of type %T (rendered: %q): a composite "+
		"cannot address a URL. This happens when additionalStatusFields declares a DOTTED entry such as "+
		"%q, which renders status.%s as an object rather than a leaf, and %q also names a request "+
		"parameter. Either declare a request-direction fieldMapping that supplies %q from the leaf, or "+
		"rename the status field so it does not collide with the parameter",
		kind, field, value, fmt.Sprintf("%v", value), field+".<leaf>", field, field, field)
}

// processFields processes the given fields map (spec or status fields) and populates the request configuration accordingly.
func processFields(callInfo *CallInfo, fields map[string]interface{}, reqConfiguration *restclient.RequestConfiguration, mapBody map[string]interface{}) []skippedComposite {
	var skipped []skippedComposite
	for field, value := range fields {
		if field == "" {
			continue
		}

		if callInfo.ReqParams.Parameters.Contains(field) {
			if _, ok := reqConfiguration.Parameters[field]; !ok { // Avoid overwriting existing values
				if rendered, ok := scalarParamValue(value); ok {
					reqConfiguration.Parameters[field] = rendered
				} else {
					skipped = append(skipped, skippedComposite{dest: destPath, field: field, value: value})
				}
			}
		}

		if callInfo.ReqParams.Query.Contains(field) {
			if _, ok := reqConfiguration.Query[field]; !ok { // Avoid overwriting existing values
				if rendered, ok := scalarParamValue(value); ok {
					reqConfiguration.Query[field] = rendered
				} else {
					skipped = append(skipped, skippedComposite{dest: destQuery, field: field, value: value})
				}
			}
		}

		// HEADERS AND COOKIES, which this function used to skip with the note "probably better to be set
		// ONLY in the Configuration CR spec (and currently it is only possible there)".
		//
		// That skip was the whole of #305. An OAS header parameter declaring `required: true` AND a
		// `default` generates a correct CRD -- the generator DOES honour the default and the apiserver
		// materialises it onto the CR -- and then nothing ever populated it, so ValidateRequest demanded
		// a header the CR was carrying and the request failed with "missing header: <name>". The drop was
		// only ever population: opts.Headers reaches the wire at restclient.go:252/:794 and opts.Cookies
		// via AddCookie at :297/:839. Measured: ReqParams.Headers and .Cookies had ZERO reads anywhere in
		// either module, against a control of 3 for the sibling fields.
		//
		// Cookies are included because the defect is real there too, not for symmetry: ValidateRequest
		// demands a required cookie with no exemption of any kind, and oasgen's own skip tests
		// In == "header" so it never applies to one.
		//
		// A CREDENTIAL HEADER IS REFUSED. Measured with the refusal removed, ground truth from the test
		// server: `server saw X-Api-Key="s3cret" from the CR SPEC`. That puts a credential in the main CR
		// spec in cleartext -- wider read access than a Secret, which is what secretRef exists to avoid --
		// and RedactValues does not cover it, being populated solely from secretRef. `Authorization` is
		// already stopped twice over (the generator skips *authorization* params; SetAuth overwrites after
		// opts.Headers), but that skip matches only *authorization* and not the other eight names, and RDC
		// must not trust the CRD it is handed.
		//
		// Hop-by-hop names are NOT refused, and that is a measured decision rather than an omission. Of
		// {Host, Content-Length, Transfer-Encoding, Connection, Upgrade}, the first three never reach the
		// wire at all -- net/http's reqWriteExcludeHeader drops them from the header map at write time and
		// takes the Host line from req.URL.Host, which is also what gets dialled. So there is no
		// virtual-host-confusion channel here and no smuggling route. Connection and Upgrade do arrive and
		// change neither framing nor routing. Refusing them would be an availability argument, not a
		// security one.
		//
		// The !ok guard, rather than the body branch's nil-compare: Headers is map[string]string, so a
		// nil-compare would treat a legitimately empty header value as unset. It is also what makes
		// spec-beats-status work with no extra code -- status is processed second and finds the key taken.
		if callInfo.ReqParams.Headers.Contains(field) && !restclient.IsCredentialHeader(field) {
			if _, ok := reqConfiguration.Headers[field]; !ok {
				rendered, ok := scalarParamValue(value)
				if !ok {
					skipped = append(skipped, skippedComposite{dest: destHeader, field: field, value: value})
				} else {
					reqConfiguration.Headers[field] = rendered
				}
			}
		}

		if callInfo.ReqParams.Cookies.Contains(field) {
			if _, ok := reqConfiguration.Cookies[field]; !ok {
				rendered, ok := scalarParamValue(value)
				if !ok {
					skipped = append(skipped, skippedComposite{dest: destCookie, field: field, value: value})
				} else {
					reqConfiguration.Cookies[field] = rendered
				}
			}
		}

		if callInfo.ReqParams.Body.Contains(field) {
			// Nil-guard so spec (processed first) beats status. FieldMapping precedence is NOT handled
			// here — it comes from those mappings being applied after this, in BuildCallConfig.
			if mapBody[field] == nil {
				mapBody[field] = value
			}
		}
	}

	return skipped
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
