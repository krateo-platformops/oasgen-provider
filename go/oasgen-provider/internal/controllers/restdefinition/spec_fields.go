package restdefinition

import (
	"context"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/crd"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/plurals"
)

// specPropertyNames returns the top-level spec property names of this RestDefinition's GENERATED kind,
// read from the CRD that has already been applied.
//
// NIL ON ANY UNCERTAINTY, and the direction is deliberate. A nil result means "unknown", and the
// existence predicate treats unknown conservatively -- it flags as it did before this function
// existed. The failure modes that produce nil are all ordinary: status has not recorded the generated
// kind yet (reconcile 1), the CRD is not applied yet, the apiserver read failed, or the schema is
// shaped unexpectedly. Returning an EMPTY set for any of those would assert "this kind has no spec
// properties", which would flag every status-mapped parameter including the ones that do resolve --
// the false positive this function exists to remove.
//
// THE CRD IS THE SCHEMA, which is the point. The alternative instruments were the live CR's spec keys
// (rejected: a kind-level condition must not change verdict per object, and an optional-but-unset
// property would read as absent) and re-parsing the OAS on every Observe (rejected: cost, and it would
// answer a different question than the one the runtime asks). The runtime fills path parameters from
// the object's spec by NAME, and the CRD's spec properties are exactly the names that can be there.
func specPropertyNames(ctx context.Context, kube client.Client, cr *definitionv1alpha1.RestDefinition) map[string]bool {
	if cr.Status.Resource.Kind == "" || cr.Status.Resource.APIVersion == "" {
		return nil
	}
	gv, err := schema.ParseGroupVersion(cr.Status.Resource.APIVersion)
	if err != nil {
		return nil
	}
	gvr := plurals.ToGroupVersionResource(schema.GroupVersionKind{
		Group:   gv.Group,
		Version: gv.Version,
		Kind:    cr.Status.Resource.Kind,
	})

	live, err := crd.Get(ctx, kube, gvr.GroupResource())
	if err != nil || live == nil {
		return nil
	}
	return specPropertiesOf(live, gvr.Version)
}

// specPropertiesOf is split out so a test can hold a CRD without an apiserver.
//
// THE VERSION MATTERS. A CRD may serve several versions whose spec schemas differ, and the parameter
// we are judging belongs to the version this RestDefinition generated. Falling back to "any version"
// would mix schemas and could suppress a correct flag.
func specPropertiesOf(live *apiextensionsv1.CustomResourceDefinition, version string) map[string]bool {
	for i := range live.Spec.Versions {
		v := &live.Spec.Versions[i]
		if v.Name != version {
			continue
		}
		if v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			return nil
		}
		spec, ok := v.Schema.OpenAPIV3Schema.Properties["spec"]
		if !ok {
			return nil
		}
		if len(spec.Properties) == 0 {
			// A spec with no properties is a real answer only if the schema genuinely declares none.
			// It is indistinguishable here from a pruned or unexpected shape, so stay conservative.
			return nil
		}
		out := make(map[string]bool, len(spec.Properties))
		for name, prop := range spec.Properties {
			// ONLY A SCALAR CAN FILL A PATH PARAMETER, so only a scalar counts as "resolves from the
			// spec". The runtime renders a spec value into a parameter through scalarParamValue, which
			// accepts string, bool, int64, float64 and nil and SKIPS anything else:
			//
			//	default:
			//		return "", false   // object or array: not a parameter value
			//
			// Counting an object- or array-typed property here would suppress a CORRECT flag, because
			// the runtime would never use it and the GET really would not validate. That is the one
			// direction this predicate must not get wrong: a missed flag leaves 0.86.0's behaviour, a
			// spurious suppression hides a kind that genuinely cannot answer existence.
			//
			// FOUND BY ANSWERING A CONSUMER'S QUESTION, not by a test. azuredevops-provider-kog asked
			// what exactly "resolves from the spec" tests before pre-registering a row against it; the
			// honest answer was "the name, and not the type", which is how this gap surfaced. 0.87.0
			// ships without this refinement.
			//
			// AN EMPTY TYPE IS NOT A SCALAR. A property with no declared type could be anything,
			// including an object, so it does not count -- unknown stays conservative here exactly as
			// a nil set does at the function level.
			if !scalarSchemaType(prop.Type) {
				continue
			}
			out[name] = true
		}
		return out
	}
	return nil
}

// scalarSchemaType reports whether an OpenAPI type can be rendered into a path parameter.
//
// THE LIST MIRRORS scalarParamValue IN THE RUNTIME, deliberately and not by coincidence: "integer"
// and "number" are its int64/float64, "boolean" its bool. If that function ever accepts more, this
// must follow, and the comment there should say so.
func scalarSchemaType(t string) bool {
	switch t {
	case "string", "integer", "number", "boolean":
		return true
	default:
		return false
	}
}
