package restResources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/text"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/builder"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// errNoGetVerb is returned when a kind declares partialFields but no `get` verb to read with.
var errNoGetVerb = errors.New("partialFields needs a get verb to read the current value with")

// partialFieldsFor returns the top-level body fields the named action declared PARTIAL.
func partialFieldsFor(verbs []getter.VerbsDescription, action string) text.StringSet {
	out := text.StringSet{}
	for _, v := range verbs {
		if !strings.EqualFold(v.Action, action) {
			continue
		}
		for _, f := range v.PartialFields {
			if f != "" {
				out[f] = struct{}{}
			}
		}
	}
	return out
}

// readRemoteForMerge reads the resource's CURRENT representation, for overlaying the CR's keys onto.
//
// WHY THE `get` VERB AND NEVER findby. A merge needs the resource's own full representation. A findby
// item comes out of a LIST, and a list item is routinely a projection -- HyperDX's
// `GET /api/v2/dashboards` returns `data: []` of summaries, and Ironic's node list omits `extra`
// entirely. Merging from a projection would overlay the CR's keys onto a subset and then send that
// subset as the whole object, which is the very defect partialFields exists to prevent, with an extra
// request paid for the privilege. So a kind that declares partialFields and has no `get` verb gets
// errNoGetVerb, not a quiet fallback.
//
// THE BODY IS DELIBERATELY NOT NORMALIZED, and this is the one place where the merge and the detection
// side (#725) read the same thing differently:
//
//	detection   compares against the body AFTER NormalizeResponseBody, because it is reporting on the
//	            CR's own field names, which is what an author reads.
//	merge       uses the RAW response, because the value it is overlaid into is a REQUEST body: it has
//	            already been through processFields and requestTransform, so it speaks the VENDOR's field
//	            names. Normalizing first would overlay CR-named keys into a vendor-named object, and a
//	            kind whose fieldMapping renames a key would send the renamed key to the vendor.
//
// Where no fieldMapping renames anything -- both measured cases -- the two are identical. Where one
// does, they differ by construction, and that is stated here rather than left for someone to find.
func (h *handler) readRemoteForMerge(
	ctx context.Context,
	cs *clientSet,
	clientInfo *getter.Info,
	mg *unstructured.Unstructured,
	log logging.Logger,
) (map[string]interface{}, error) {
	gcli, err := cs.For(ctx, string(apiaction.Get))
	if err != nil {
		return nil, fmt.Errorf("building the get verb's client: %w", err)
	}
	apiCall, callInfo, err := builder.APICallBuilder(gcli, clientInfo, apiaction.Get)
	if err != nil {
		return nil, fmt.Errorf("building the get request: %w", err)
	}
	if apiCall == nil || callInfo == nil {
		return nil, errNoGetVerb
	}
	reqConfiguration := builder.BuildCallConfig(ctx, callInfo, mg, clientInfo.ConfigurationSpec, nil)
	if reqConfiguration == nil {
		return nil, fmt.Errorf("building the get call configuration")
	}
	response, err := apiCall(ctx, &http.Client{}, callInfo.Path, reqConfiguration)
	if err != nil {
		return nil, fmt.Errorf("reading the current value: %w", err)
	}
	b, ok := response.ResponseBody.(map[string]interface{})
	if !ok {
		// A non-object body cannot be merged from. Distinguished from a transport failure because the
		// caller's decision is the same either way (omit the field) but the operator's is not.
		return nil, fmt.Errorf("the get verb returned a %T, not an object", response.ResponseBody)
	}
	log.Debug("Read the current value for a partial-field merge", "kind", mg.GetKind(), "path", callInfo.Path)
	return b, nil
}

// dropPartialFields removes every declared partial field from the request body, for use when the read
// that would have made the merge safe did not succeed.
//
// WHY OMIT RATHER THAN SEND IT WHOLE. Sending the field whole is exactly the defect: the vendor replaces
// the object with the CR's partial view and the keys the CR does not model are deleted. Omitting it means
// the request carries nothing under that key, so the vendor replaces nothing -- the state the live Ironic
// control measured as safe, where blades with no spec.extra kept theirs across every PATCH.
//
// THE COST, STATED: the CR's own changes to that field do not reach the vendor on this pass. A field that
// is both partial and changed therefore WAITS for a successful read. That is the right trade -- a delayed
// write converges on the next pass, a deleted key does not come back -- but it is a real delay and the
// resource says so rather than reporting a clean update.
func dropPartialFields(body map[string]interface{}, partial text.StringSet) []string {
	var dropped []string
	for field := range partial {
		if _, present := body[field]; present {
			delete(body, field)
			dropped = append(dropped, field)
		}
	}
	return dropped
}

const reasonPartialMergeUnavailable = "PartialFieldMergeUnavailable"
