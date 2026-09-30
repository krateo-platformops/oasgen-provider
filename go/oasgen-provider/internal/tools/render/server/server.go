// Package server is the HTTP surface of oasgen-render: POST /render turns RestDefinitions plus their OAS
// documents into the CRDs oasgen would apply, with the same code the controller runs (render.CRDs), and
// without reading from or writing to any cluster. The OAS documents travel in the request, keyed by the exact
// oasPath strings the RestDefinitions reference, so nothing is fetched either.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/crd"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/oas2jsonschema"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/render"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// DefaultMaxBodyBytes bounds a /render request. Real OAS documents range from a few KB (KOG specs) to a
// few MB (a vendor's full API description); the bound is there to refuse a runaway body, not to ration.
//
// 4 MiB, not the 32 MiB this started at, and the two numbers that matter are MEASURED rather than guessed.
// Parsing and rendering allocates ~97x the document size and holds ~29x of it live at peak:
//
//	input      allocated   live heap
//	  35 KiB     9.6 MiB     8.3 MiB
//	 349 KiB      37 MiB      15 MiB
//	 1.3 MiB     124 MiB      46 MiB
//	 3.1 MiB     300 MiB      90 MiB
//
// At 32 MiB that extrapolates to roughly 930 MiB of live heap for a SINGLE request -- so the old cap and
// any sane container memory limit were mutually inconsistent: the service accepted bodies it could not
// render without being OOM-killed, and on a shared node a pathological spec could take neighbours with it.
//
// 4 MiB covers every real document we have seen (GitHub's full public OAS is the largest at a few MB) and
// sits at ~116 MiB live heap, comfortably inside the chart's 512Mi default limit.
//
// The known consumer confirms the headroom is real rather than hopeful. The Controller Builder posts a
// draft.json built from a draft tree its frontend caps at 512 KiB -- oversized specs are trimmed to the
// mapped paths before being held -- so a live render body is about 0.6 MiB at most, roughly a sixth of
// this. (Its 8 MiB limit applies to the raw spec at import, before trimming, and never reaches /render.)
//
// THESE TWO NUMBERS ARE COUPLED. Raising this cap without raising helm/oasgen-provider/values.yaml's
// render.resources.limits.memory re-creates exactly the inconsistency it was lowered to remove.
const DefaultMaxBodyBytes int64 = 4 << 20

// Severities of a Problem. An "error" is something the controller refuses on: it would apply nothing for
// that RestDefinition. A "warning" is something it logs and applies anyway, surfaced here because an author
// previewing a draft is exactly the person who should see it.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Request is the POST /render body.
type Request struct {
	// RestDefinitions are RestDefinition objects (apiVersion/kind may be omitted).
	RestDefinitions []json.RawMessage `json:"restDefinitions"`
	// OAS maps each oasPath, spelled exactly as spec.oasPath spells it, to the document text.
	OAS map[string]string `json:"oas"`
}

// Response is the POST /render reply. Every list is present (possibly empty), never null.
type Response struct {
	CRDs                   []*apiextensionsv1.CustomResourceDefinition `json:"crds"`
	ConfigurationCRDs      []*apiextensionsv1.CustomResourceDefinition `json:"configurationCrds"`
	Errors                 []Problem                                   `json:"errors"`
	SkippedSecuritySchemes []SkippedSecurityScheme                     `json:"skippedSecuritySchemes"`
}

// Problem is one finding about one RestDefinition. RestDefinition is "<namespace>/<name>", or
// "restDefinitions[<i>]" when the object carries no name. Field is a RestDefinition field path when the
// finding is attributable to one, otherwise the schema location the generator reported, otherwise empty.
type Problem struct {
	RestDefinition string `json:"restDefinition"`
	Field          string `json:"field"`
	Message        string `json:"message"`
	Severity       string `json:"severity"`
}

// SkippedSecurityScheme is a security scheme the document declares that the Configuration CRD cannot express.
type SkippedSecurityScheme struct {
	RestDefinition string `json:"restDefinition"`
	Scheme         string `json:"scheme"`
}

// Handler serves /render and /healthz.
type Handler struct {
	Parser       oas2jsonschema.Parser
	MaxBodyBytes int64
	Log          *slog.Logger
}

// New returns a Handler using the parser the controller uses.
func New(log *slog.Logger) *Handler {
	return &Handler{Parser: oas2jsonschema.NewLibOASParser(), MaxBodyBytes: DefaultMaxBodyBytes, Log: log}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	case "/render":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "use POST")
			return
		}
		h.serveRender(w, r)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveRender(w http.ResponseWriter, r *http.Request) {
	limit := h.MaxBodyBytes
	if limit <= 0 {
		limit = DefaultMaxBodyBytes
	}
	var req Request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := dec.Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body exceeds %d bytes", limit))
			return
		}
		writeError(w, http.StatusBadRequest, "decoding request: "+err.Error())
		return
	}
	if len(req.RestDefinitions) == 0 {
		writeError(w, http.StatusBadRequest, "restDefinitions is empty")
		return
	}

	resp := h.Render(r.Context(), req)
	if h.Log != nil {
		h.Log.Info("rendered", "restDefinitions", len(req.RestDefinitions), "crds", len(resp.CRDs),
			"configurationCrds", len(resp.ConfigurationCRDs), "problems", len(resp.Errors))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// Render renders every RestDefinition in req independently: a failure in one is reported against it and
// the others still render. It is the whole of /render minus the HTTP framing.
func (h *Handler) Render(ctx context.Context, req Request) Response {
	resp := Response{
		CRDs:                   []*apiextensionsv1.CustomResourceDefinition{},
		ConfigurationCRDs:      []*apiextensionsv1.CustomResourceDefinition{},
		Errors:                 []Problem{},
		SkippedSecuritySchemes: []SkippedSecurityScheme{},
	}
	// Two RestDefinitions generating the same CRD is refused by the controller at apply time (one live
	// owner per CRD); a preview has no cluster to find that out from, so it checks the batch itself.
	producedBy := map[string]string{}

	for i, raw := range req.RestDefinitions {
		id := fmt.Sprintf("restDefinitions[%d]", i)
		cr := &definitionv1alpha1.RestDefinition{}
		if err := json.Unmarshal(raw, cr); err != nil {
			resp.Errors = append(resp.Errors, Problem{RestDefinition: id, Message: "decoding RestDefinition: " + err.Error(), Severity: SeverityError})
			continue
		}
		if cr.Name != "" {
			id = cr.Namespace + "/" + cr.Name
		}
		if cr.Kind != "" && cr.Kind != "RestDefinition" {
			resp.Errors = append(resp.Errors, Problem{RestDefinition: id, Field: "kind", Message: fmt.Sprintf("kind is %q, not RestDefinition", cr.Kind), Severity: SeverityError})
			continue
		}

		res, problems := h.renderOne(ctx, cr, req.OAS)
		for _, p := range problems {
			p.RestDefinition = id
			resp.Errors = append(resp.Errors, p)
		}
		if res == nil {
			continue
		}
		for _, s := range res.SkippedSecuritySchemes {
			resp.SkippedSecuritySchemes = append(resp.SkippedSecuritySchemes, SkippedSecurityScheme{RestDefinition: id, Scheme: s})
		}

		// The owner annotation the controller stamps is "<namespace>/<name>"; without both there is no owner
		// to stamp, and PrepareForApply leaves it off.
		owner := ""
		if cr.Namespace != "" && cr.Name != "" {
			owner = cr.Namespace + "/" + cr.Name
		}
		conflict := false
		for _, c := range []*apiextensionsv1.CustomResourceDefinition{res.CRD, res.ConfigurationCRD} {
			if c == nil {
				continue
			}
			if prev, ok := producedBy[c.Name]; ok {
				resp.Errors = append(resp.Errors, Problem{
					RestDefinition: id, Field: "spec.resource.kind",
					Message:  fmt.Sprintf("CRD %s is also generated by %s; the controller allows one owner per CRD and would refuse the second", c.Name, prev),
					Severity: SeverityError,
				})
				conflict = true
				continue
			}
			producedBy[c.Name] = id
		}
		if conflict {
			continue
		}
		crd.PrepareForApply(res.CRD, owner)
		resp.CRDs = append(resp.CRDs, res.CRD)
		if res.ConfigurationCRD != nil {
			crd.PrepareForApply(res.ConfigurationCRD, owner)
			resp.ConfigurationCRDs = append(resp.ConfigurationCRDs, res.ConfigurationCRD)
		}
	}
	return resp
}

// renderOne runs the controller's own sequence for one RestDefinition: parse the document its oasPath names,
// derive the target GVK, render. A nil result means the controller would have applied nothing.
func (h *Handler) renderOne(ctx context.Context, cr *definitionv1alpha1.RestDefinition, oas map[string]string) (res *render.Result, problems []Problem) {
	// The generator walks vendor documents of arbitrary shape; a panic on one of them must cost that one
	// RestDefinition, not the service.
	defer func() {
		if r := recover(); r != nil {
			res = nil
			problems = append(problems, Problem{Message: fmt.Sprintf("generation panicked: %v", r), Severity: SeverityError})
		}
	}()

	if cr.Spec.OASPath == "" {
		return nil, []Problem{{Field: "spec.oasPath", Message: "spec.oasPath is empty", Severity: SeverityError}}
	}
	document, ok := oas[cr.Spec.OASPath]
	if !ok {
		return nil, []Problem{{Field: "spec.oasPath", Message: fmt.Sprintf("no document supplied for oasPath %q: the request's oas map must carry it under exactly that key", cr.Spec.OASPath), Severity: SeverityError}}
	}
	doc, err := h.Parser.Parse([]byte(document))
	if err != nil {
		return nil, []Problem{{Field: "spec.oasPath", Message: fmt.Sprintf("getting document model from CR: %v", err), Severity: SeverityError}}
	}

	gvk := render.TargetGVK(cr, doc)
	res, err = render.CRDs(ctx, cr, gvk, doc, render.HasSecuritySchemes(doc))
	if err != nil {
		p := Problem{Message: err.Error(), Severity: SeverityError}
		var fe *render.FieldError
		if errors.As(err, &fe) {
			p.Field = fe.Field
		}
		return nil, []Problem{p}
	}

	for _, w := range res.ValidationWarnings {
		problems = append(problems, warning(w))
	}
	for _, w := range res.GenerationWarnings {
		problems = append(problems, warning(w))
	}
	if len(res.SkippedSecuritySchemes) > 0 && !res.AuthenticationGenerated {
		problems = append(problems, Problem{
			Field: "components.securitySchemes",
			Message: fmt.Sprintf("the OAS document declares only unsupported security scheme(s) (%s), so the generated Configuration CRD has no authentication field and every request will be unauthenticated",
				strings.Join(res.SkippedSecuritySchemes, "; ")),
			Severity: SeverityWarning,
		})
	}
	return res, problems
}

// warning turns a generator or ValidateSchemas finding into a Problem, keeping the schema location those
// errors carry.
func warning(err error) Problem {
	p := Problem{Message: err.Error(), Severity: SeverityWarning}
	var ge oas2jsonschema.SchemaGenerationError
	var ve oas2jsonschema.SchemaValidationError
	switch {
	case errors.As(err, &ge):
		p.Field = ge.Path
	case errors.As(err, &ve):
		p.Field = ve.Path
	}
	return p
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
