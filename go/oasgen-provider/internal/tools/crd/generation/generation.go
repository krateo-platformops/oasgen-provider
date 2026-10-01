// Package generation holds the pure, version-lifecycle primitives that operate on an already-generated
// CustomResourceDefinition: appending a served version alongside a non-served "vacuum" storage version so
// multiple schemas coexist under conversion=None, flipping served/storage flags, pruning stale versions,
// propagating the status schema, and comparing schemas. They are ported/aligned from core-provider's
// internal/tools/crd/generation so the two providers share the same multi-version CRD model.
//
// This package is intentionally free of any crdgen/toolchain invocation: callers generate the single-version
// CRD (via crdgen + crd.Unmarshal) and then compose it into the live multi-version CRD with these helpers.
package generation

import (
	"fmt"

	hash "github.com/krateo-platformops/oasgen-provider/internal/tools/hash"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// VacuumVersionName is the never-served storage version that carries all stored objects across served
// versions. Its presence lets multiple served versions coexist under conversion strategy None without a
// conversion webhook, and the apiserver forbids deleting it while objects are stored — so it is never pruned.
const VacuumVersionName = "vacuum"

// VersionLabel is the label a served instance carries to record which version it was written through. The
// version printer column reads it, and (from Stage 5) owner-scoped migration stamps it. Single source of
// truth so the column JSONPath and the migration writer never disagree.
const VersionLabel = "krateo.io/oas-version"

// SetServedStorage sets the served/storage flags on the named version (no-op if absent).
func SetServedStorage(crd *apiextensionsv1.CustomResourceDefinition, version string, served, storage bool) {
	for i := range crd.Spec.Versions {
		if crd.Spec.Versions[i].Name == version {
			crd.Spec.Versions[i].Served = served
			crd.Spec.Versions[i].Storage = storage
		}
	}
}

// AppendVersion merges each version of toadd into crd. A version already present is left untouched
// (idempotent). When a genuinely new version is added, a non-served "vacuum" storage version is injected
// once (if absent) to hold stored objects, and every non-vacuum version is flipped to served=true /
// storage=false so the vacuum remains the sole storage version. Returns the merged CRD. crd and toadd are
// NOT mutated: it works on a deep copy (a shallow value copy would share the Spec.Versions backing array, so
// the flip below could leak served/storage into the caller's slice), and appended versions are deep-copied
// so their Schema pointers are not aliased with toadd.
func AppendVersion(crd apiextensionsv1.CustomResourceDefinition, toadd apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, error) {
	out := crd.DeepCopy()
	for _, el2 := range toadd.Spec.Versions {
		exist := false
		vacuum := false
		for _, el1 := range out.Spec.Versions {
			if el1.Name == el2.Name {
				exist = true
				break
			}
		}
		for _, el1 := range out.Spec.Versions {
			if el1.Name == VacuumVersionName {
				vacuum = true
				break
			}
		}

		if !exist {
			out.Spec.Versions = append(out.Spec.Versions, *el2.DeepCopy())
			if !vacuum {
				out.Spec.Versions = append(out.Spec.Versions, apiextensionsv1.CustomResourceDefinitionVersion{
					Name:    VacuumVersionName,
					Served:  false,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type:        "object",
							Description: "This is a vacuum version to storage different versions",
							Properties: map[string]apiextensionsv1.JSONSchemaProps{
								"apiVersion": {
									Type:        "string",
									Description: "APIVersion defines the versioned schema of this representation of an object. Servers should convert recognized schemas to the latest internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources",
								},
								"kind": {
									Type: "string",
								},
								"metadata": {
									Type: "object",
								},
								"spec": {
									Type:                   "object",
									XPreserveUnknownFields: &[]bool{true}[0],
								},
								"status": {
									Type:                   "object",
									XPreserveUnknownFields: &[]bool{true}[0],
								},
							},
						},
					},
				})
			}
			for i := range out.Spec.Versions {
				if out.Spec.Versions[i].Name != VacuumVersionName {
					out.Spec.Versions[i].Served = true
					out.Spec.Versions[i].Storage = false
				}
			}
		}
	}

	return out, nil
}

// RemoveStaleVersions removes the named non-vacuum versions from crd.Spec.Versions. The vacuum storage
// version is NEVER removed (the apiserver forbids deleting the storage version, and it preserves all stored
// instances). The caller owns the prunability predicate (no definition/instance references the version, and
// it is not the current served version); this is the pure mutation. Returns whether anything was removed.
func RemoveStaleVersions(crd *apiextensionsv1.CustomResourceDefinition, prune map[string]bool) bool {
	removed := false
	kept := make([]apiextensionsv1.CustomResourceDefinitionVersion, 0, len(crd.Spec.Versions))
	for _, v := range crd.Spec.Versions {
		if v.Name != VacuumVersionName && prune[v.Name] {
			removed = true
			continue
		}
		kept = append(kept, v)
	}
	crd.Spec.Versions = kept
	return removed
}

// versionColumn surfaces the served version an instance was written through (the VersionLabel) in
// `kubectl get`. It reads empty on clusters where nothing stamps the label yet.
var versionColumn = apiextensionsv1.CustomResourceColumnDefinition{
	Name:     "VERSION",
	Type:     "string",
	JSONPath: `.metadata.labels.krateo\.io/oas-version`,
}

// AddVersionColumn adds the VERSION printer column to every served version that does not already carry it.
// The non-served vacuum storage version is skipped (kubectl never lists it). Idempotent.
func AddVersionColumn(crd *apiextensionsv1.CustomResourceDefinition) {
	for i := range crd.Spec.Versions {
		v := &crd.Spec.Versions[i]
		if v.Name == VacuumVersionName {
			continue
		}
		has := false
		for _, c := range v.AdditionalPrinterColumns {
			if c.Name == versionColumn.Name {
				has = true
				break
			}
		}
		if !has {
			v.AdditionalPrinterColumns = append(v.AdditionalPrinterColumns, versionColumn)
		}
	}
}

// UpdateStatus propagates the status subschema of the given version to every version in crd, so all versions
// expose a uniform status. Errors if the given version has no schema.
func UpdateStatus(crd *apiextensionsv1.CustomResourceDefinition, version apiextensionsv1.CustomResourceDefinitionVersion) error {
	if version.Schema == nil || version.Schema.OpenAPIV3Schema == nil {
		return fmt.Errorf("CRD %s version %s schema is nil", crd.Name, version.Name)
	}
	newStatus := version.Schema.OpenAPIV3Schema.Properties["status"]
	for i := range crd.Spec.Versions {
		if crd.Spec.Versions[i].Schema != nil && crd.Spec.Versions[i].Schema.OpenAPIV3Schema != nil {
			crd.Spec.Versions[i].Schema.OpenAPIV3Schema.Properties["status"] = newStatus
		}
	}
	return nil
}

// StatusEqual reports whether the status subschema of the first status-bearing version of crd1 equals that of
// crd2 (by FNV hash). It errors when either CRD has no version exposing a status property. NOTE: this only
// compares the status subtree — it must not be the sole drift gate for a spec-schema change (oasgen keys
// spec drift off the OAS content hash).
func StatusEqual(crd1, crd2 *apiextensionsv1.CustomResourceDefinition) (bool, error) {
	searchFirstStatus := func(crd *apiextensionsv1.CustomResourceDefinition) int {
		for i, v := range crd.Spec.Versions {
			if v.Schema != nil && v.Schema.OpenAPIV3Schema != nil {
				if _, ok := v.Schema.OpenAPIV3Schema.Properties["status"]; ok {
					return i
				}
			}
		}
		return -1
	}

	i1 := searchFirstStatus(crd1)
	if i1 == -1 {
		return false, fmt.Errorf("CRD %s has no version with status property", crd1.Name)
	}
	i2 := searchFirstStatus(crd2)
	if i2 == -1 {
		return false, fmt.Errorf("CRD %s has no version with status property", crd2.Name)
	}

	h1 := hash.NewFNVObjectHash()
	if err := h1.SumHash(crd1.Spec.Versions[i1].Schema.OpenAPIV3Schema.Properties["status"]); err != nil {
		return false, fmt.Errorf("error hashing CRD status: %w", err)
	}
	h2 := hash.NewFNVObjectHash()
	if err := h2.SumHash(crd2.Spec.Versions[i2].Schema.OpenAPIV3Schema.Properties["status"]); err != nil {
		return false, fmt.Errorf("error hashing generated CRD status: %w", err)
	}
	return h1.GetHash() == h2.GetHash(), nil
}

// GVKExists reports whether crd already serves the given GVK (group + kind match and a version of that name
// is present).
func GVKExists(crd *apiextensionsv1.CustomResourceDefinition, gvk schema.GroupVersionKind) bool {
	if crd.Spec.Group != gvk.Group {
		return false
	}
	if crd.Spec.Names.Kind != gvk.Kind {
		return false
	}
	for _, v := range crd.Spec.Versions {
		if v.Name == gvk.Version {
			return true
		}
	}
	return false
}

// ServedVersions returns the served version names of crd, excluding the non-served vacuum store.
func ServedVersions(crd *apiextensionsv1.CustomResourceDefinition) []string {
	if crd == nil {
		return nil
	}
	out := make([]string, 0, len(crd.Spec.Versions))
	for _, v := range crd.Spec.Versions {
		if v.Name == VacuumVersionName || !v.Served {
			continue
		}
		out = append(out, v.Name)
	}
	return out
}

// VersionSelector is the label selector the controller for `version` watches with.
//
// EXACT equality, for every version including the newest -- symmetric with composition-dynamic-controller,
// which builds the same requirement with selection.Equals and has no notion of a "current" version at all.
//
// The symmetry is load-bearing, not cosmetic. An exact selector is a constant function of the controller's
// OWN version, so it can never go stale: whatever else happens to the CRD, a controller deployed for v27
// watches v27's instances and nothing else. An earlier attempt here gave the newest version a set-based
// `notin (every other served version)` so it would also pick up unlabelled instances -- and that selector
// silently went WRONG the moment another version was appended, because the older Deployment kept the
// selector it was created with and started claiming the newer version's instances too. Measured:
//
//	v27 deployed with "notin (v26)"; v28 appended; a v28-labelled instance then matched BOTH controllers.
//
// Exactly the double-reconciliation the per-version model exists to prevent, reintroduced by the thing
// meant to improve it.
//
// Instances that carry no label are not this function's problem. They are labelled by the oas-version
// MutatingAdmissionPolicy at admission, and swept by the provider for anything that predates it -- the
// same division of labour core-provider uses, where the manager stamps and the per-version controller
// only ever selects.
func VersionSelector(version string) string {
	if version == "" {
		return ""
	}
	return fmt.Sprintf("%s=%s", VersionLabel, version)
}
