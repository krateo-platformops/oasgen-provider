// Package render turns a RestDefinition plus its parsed OAS document into the CRDs oasgen would apply,
// without touching a cluster. It is the generation half of the RestDefinition controller's
// generateAndApplyCRDs, lifted out so the oasgen-render service can run exactly the same code for a
// preview: the controller renders and then applies, the service renders and returns.
package render

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/crd"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/oas2jsonschema"
	oteltelemetry "github.com/krateo-platformops/oasgen-provider/internal/tools/telemetry"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/text"
	"github.com/krateo-platformops/plumbing/crdgen"
	"go.opentelemetry.io/otel/attribute"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// DefaultVersion is the CRD API version used when the OAS document declares none, and the fixed version of
// every Configuration CRD.
const DefaultVersion = "v1alpha1"

// FieldError is an error attributable to one field of the RestDefinition, so a caller that shows it to an
// author can point at the line to fix. Its message is the underlying error's, unchanged.
type FieldError struct {
	Field string
	Err   error
}

func (e *FieldError) Error() string { return e.Err.Error() }
func (e *FieldError) Unwrap() error { return e.Err }

func fieldErrorf(field, format string, args ...any) error {
	return &FieldError{Field: field, Err: fmt.Errorf(format, args...)}
}

// TargetVersion is the CRD API version derived from the OAS document's info.version (normalized to a legal
// k8s version name via crdgen), falling back to DefaultVersion when the OAS is unavailable or declares no
// version.
func TargetVersion(doc oas2jsonschema.OASDocument) string {
	if doc != nil {
		if v := crdgen.NormalizeVersionName(doc.Version()); v != "" {
			return v
		}
	}
	return DefaultVersion
}

// TargetGVK is the GroupVersionKind of the resource CRD a RestDefinition generates from doc.
func TargetGVK(cr *definitionv1alpha1.RestDefinition, doc oas2jsonschema.OASDocument) schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   cr.Spec.ResourceGroup,
		Version: TargetVersion(doc),
		Kind:    text.CapitaliseFirstLetter(cr.Spec.Resource.Kind),
	}
}

// HasSecuritySchemes reports whether doc declares any security scheme, which is one of the two reasons a
// Configuration CRD is generated (the other is declared configurationFields).
func HasSecuritySchemes(doc oas2jsonschema.OASDocument) bool {
	return doc.SecuritySchemes() != nil && len(doc.SecuritySchemes()) > 0
}

// Result is everything one RestDefinition renders to.
type Result struct {
	// CRD is the resource CRD, exactly as generated (before crd.PrepareForApply).
	CRD *apiextensionsv1.CustomResourceDefinition
	// ConfigurationCRD is nil when the RestDefinition declares no configurationFields and the OAS document
	// declares no security schemes.
	ConfigurationCRD *apiextensionsv1.CustomResourceDefinition

	// GenerationWarnings and ValidationWarnings (the latter from oas2jsonschema.ValidateSchemas) are
	// non-fatal: the controller logs them and applies the CRDs anyway.
	GenerationWarnings []error
	ValidationWarnings []error

	// SkippedSecuritySchemes names security schemes the Configuration CRD cannot express.
	SkippedSecuritySchemes []string
	// AuthenticationGenerated is true when the Configuration schema carries an authentication field. With
	// skipped schemes and no authentication, the resource has no way to supply a credential at all.
	AuthenticationGenerated bool
}

// CRDs renders cr against docs into the resource CRD and, when needed, the Configuration CRD. gvk and
// hasSecuritySchemes are the caller's (see TargetGVK and HasSecuritySchemes). An error means the controller
// would refuse to apply anything; a *FieldError among them names the offending RestDefinition field.
//
// Takes the DOCUMENT SET rather than one document (#108). Until this change the controller built a set
// with every per-verb override in it and then handed generation docs.Default(), so the overrides were
// discarded one call before the only code that reads them. A verb's own document reaches the generator
// through here and nowhere else.
func CRDs(ctx context.Context, cr *definitionv1alpha1.RestDefinition, gvk schema.GroupVersionKind, docs *oas2jsonschema.DocumentSet, hasSecuritySchemes bool) (_ *Result, err error) {
	// Fail here rather than at poll time: an async poll path that breaks rest-dynamic-controller's runtime
	// contract is otherwise accepted by admission AND by this reconcile, and only surfaces once a create has
	// already fired (issue #46).
	if verr := validateAsyncPollPaths(cr, docs); verr != nil {
		return nil, verr
	}
	// Same reasoning, one verb along: a dispatch whose correlation cannot close the loop re-fires on
	// every reconcile, so it is refused before anything is applied rather than discovered as duplicate
	// runs (#199).
	if verr := validateDispatchCorrelation(cr, docs); verr != nil {
		return nil, verr
	}

	verbs := toDomainVerbs(cr.Spec.Resource.VerbsDescription)

	resourceConfig, rerr := toDomainResourceConfig(cr, verbs)
	if rerr != nil {
		return nil, rerr
	}
	generator := oas2jsonschema.NewOASSchemaGeneratorFromSet(docs, oas2jsonschema.DefaultGeneratorConfig(), resourceConfig)

	// The echo path is checked here, against the collection's item schema, because this is the first point
	// that schema exists. It is the last way to reach a dispatch that fires on every reconcile: a mistyped
	// echoedAt never matches, so the invocation reads as absent forever.
	if verr := generator.ValidateCorrelationEcho(); verr != nil {
		for i, v := range cr.Spec.Resource.VerbsDescription {
			if strings.EqualFold(v.Action, oas2jsonschema.ActionDispatch) {
				return nil, &FieldError{
					Field: fmt.Sprintf("spec.resource.verbsDescription[%d].correlation.echoedAt", i),
					Err:   verr,
				}
			}
		}
		return nil, verr
	}

	// Checked here for the same reason as the echo path above: it rejects a DECLARATION, but it cannot be
	// CRD validation -- as CEL it exceeds the apiserver's rule cost budget and would refuse the whole CRD
	// at install (#169). See ValidateDependentLookups.
	if lerr := generator.ValidateDependentLookups(); lerr != nil {
		for i, v := range cr.Spec.Resource.VerbsDescription {
			for j, m := range v.FieldMapping {
				if m.Resolver != nil && m.Resolver.Type == "dependentLookup" {
					return nil, &FieldError{
						Field: fmt.Sprintf("spec.resource.verbsDescription[%d].fieldMapping[%d].resolver.dependentLookup.name", i, j),
						Err:   lerr,
					}
				}
			}
		}
		return nil, lerr
	}

	_, genSpan := oteltelemetry.Tracer().Start(ctx, "restdefinition.generate_crd")
	defer genSpan.End()
	defer func() { oteltelemetry.RecordError(genSpan, err) }()
	genSpan.SetAttributes(
		attribute.String("k8s.object.name", cr.Name),
		attribute.String("k8s.object.namespace", cr.Namespace),
		attribute.String("crd.group", gvk.Group),
		attribute.String("crd.kind", gvk.Kind),
	)

	result, err := generator.Generate()
	if err != nil {
		return nil, fmt.Errorf("generating schemas: %w", err)
	}

	// The token must be a field the CR can actually carry, and only now is there a spec schema to ask.
	// A token naming nothing in the spec can never match an echoed value, so the dispatch would fire on
	// every reconcile -- the one outcome this verb exists to prevent.
	if verr := validateCorrelationTokenInSpec(cr, result.SpecSchema); verr != nil {
		return nil, verr
	}

	out := &Result{
		GenerationWarnings:      result.GenerationWarnings,
		ValidationWarnings:      result.ValidationWarnings,
		SkippedSecuritySchemes:  result.SkippedSecuritySchemes,
		AuthenticationGenerated: len(result.ConfigurationSchema) > 0 && bytes.Contains(result.ConfigurationSchema, []byte(`"authentication"`)),
	}

	res, crdWarnings, err := crdgen.GenerateWithWarnings(crdgen.Options{
		Group:        gvk.Group,
		Version:      gvk.Version,
		Kind:         gvk.Kind,
		Categories:   []string{strings.ToLower(cr.Spec.Resource.Kind), "restresources", "rr"},
		SpecSchema:   result.SpecSchema,
		StatusSchema: result.StatusSchema,
		Managed:      true,
	})
	if err != nil {
		return nil, fmt.Errorf("generating CRD: %w", err)
	}
	out.GenerationWarnings = append(out.GenerationWarnings, crdgenWarnings("the generated CRD", crdWarnings)...)
	out.CRD, err = crd.Unmarshal(res)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling CRD: %w", err)
	}

	if len(resourceConfig.ConfigurationFields) > 0 || hasSecuritySchemes {
		cfgGVK := schema.GroupVersionKind{
			Group:   cr.Spec.ResourceGroup,
			Version: DefaultVersion,
			Kind:    text.CapitaliseFirstLetter(cr.Spec.Resource.Kind) + "Configuration",
		}
		cfgRes, cfgWarnings, cerr := crdgen.GenerateWithWarnings(crdgen.Options{
			Group:      cfgGVK.Group,
			Version:    cfgGVK.Version,
			Kind:       cfgGVK.Kind,
			Categories: []string{strings.ToLower(cr.Spec.Resource.Kind), "restconfigs", "rc"},
			SpecSchema: result.ConfigurationSchema,
			Managed:    false,
		})
		if cerr != nil {
			return nil, fmt.Errorf("generating configuration CRD: %w", cerr)
		}
		out.GenerationWarnings = append(out.GenerationWarnings, crdgenWarnings("the generated Configuration CRD", cfgWarnings)...)
		out.ConfigurationCRD, cerr = crd.Unmarshal(cfgRes)
		if cerr != nil {
			return nil, fmt.Errorf("unmarshalling configuration CRD: %w", cerr)
		}
	}
	return out, nil
}

// crdgenWarnings adapts crdgen's []string warnings onto this package's []error channel, which is what
// reaches the log and the RestDefinition's event (see the controller's logWarnings call).
//
// WHY THIS EXISTS AT ALL (#452): until plumbing v1.26.1 the only entry point was crdgen.Generate, which
// computed the transpiler's warnings and then dropped them -- nothing read, returned or logged them, so
// "crdgen will warn about it" meant nothing. GenerateWithWarnings gives them back, but a channel is not a
// signal until a consumer puts them somewhere durable, so they are appended to GenerationWarnings here
// rather than returned in a new field no caller reads.
//
// THE SUBJECT IS PREFIXED because the two call sites in this file generate DIFFERENT CRDs from different
// schemas. An unprefixed `spec.properties: given default {}` cannot be attributed to the managed CRD or
// the Configuration CRD, and the Configuration CRD is the one a user cannot see in spec.resource.
func crdgenWarnings(subject string, warnings []string) []error {
	if len(warnings) == 0 {
		return nil
	}
	out := make([]error, 0, len(warnings))
	for _, w := range warnings {
		out = append(out, fmt.Errorf("%s: %s", subject, w))
	}
	return out
}
