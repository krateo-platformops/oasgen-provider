// Package policy projects the oas-version MutatingAdmissionPolicy for a generated CRD's API group.
//
// oasgen-provider hosts no admission webhooks: the krateo.io/oas-version label is stamped onto generated
// instances by an in-apiserver MutatingAdmissionPolicy, which records the SERVED VERSION OF THE WRITE
// ENDPOINT the request came through. That label is what makes multi-version coexistence work — the vacuum
// storage version erases the apiVersion an object was written as, so without the label there is no record
// of which version an instance belongs to.
//
// Modelled on core-provider's composition-version policy (internal/tools/policy), with one structural
// difference: compositions all live in a single group, composition.krateo.io, so one cluster singleton
// covers them. oasgen generates CRDs into ARBITRARY groups declared per RestDefinition, so there is one
// policy per group. Matching apiGroups: ["*"] instead would stamp this label onto every object in the
// cluster, which is not ours to do.
package policy

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// policyAPIVersion is the GA MutatingAdmissionPolicy API, on by default since Kubernetes 1.36.
	//
	// The chart's floor is 1.33 (set for CRD validation ratcheting), so on 1.33-1.35 this API is absent
	// and no policy is created. That is tolerated rather than fatal: rest-dynamic-controller stamps the
	// label itself when it reconciles an instance that lacks one, so the model still works, just without
	// the apiserver doing it at admission. See EnsureVersionPolicy's NoMatch handling.
	policyAPIVersion = "admissionregistration.k8s.io/v1"

	// VersionLabel must match crd/generation.VersionLabel. The policy stamps the request's served version
	// onto it so per-version watching and pruning keep working after the vacuum storage version erases
	// the apiVersion an object was written as.
	VersionLabel = "krateo.io/oas-version"
)

// nonAlphaNum matches every run of characters a Kubernetes object name may not contain.
var nonAlphaNum = regexp.MustCompile(`[^a-z0-9]+`)

// PolicyName is the policy (and binding) name for an API group. One per group, derived from it so two
// RestDefinitions in the same group converge on the same object rather than fighting over it.
func PolicyName(group string) string {
	slug := strings.Trim(nonAlphaNum.ReplaceAllString(strings.ToLower(group), "-"), "-")
	name := "krateo-oas-version-" + slug
	if len(name) > 253 {
		name = name[:253]
	}
	return strings.TrimSuffix(name, "-")
}

// objects returns the MutatingAdmissionPolicy and its binding for group.
func objects(group string) (*unstructured.Unstructured, *unstructured.Unstructured) {
	name := PolicyName(group)

	p := &unstructured.Unstructured{}
	p.SetAPIVersion(policyAPIVersion)
	p.SetKind("MutatingAdmissionPolicy")
	p.SetName(name)
	p.Object["spec"] = map[string]any{
		"matchConstraints": map[string]any{
			"matchPolicy": "Exact",
			"resourceRules": []any{map[string]any{
				"apiGroups":   []any{group},
				"apiVersions": []any{"*"},
				"operations":  []any{"CREATE", "UPDATE"},
				"resources":   []any{"*"},
			}},
		},
		// Fail, not Ignore: an instance admitted WITHOUT the label is invisible to its version's
		// controller, which is a silent non-reconcile rather than a visible error. Refusing the write is
		// the better failure.
		"failurePolicy":      "Fail",
		"reinvocationPolicy": "Never",
		// JSONPatch, NOT ApplyConfiguration, and this is not a style preference -- core-provider paid for
		// it (#66 there).
		//
		// ApplyConfiguration performs a structured merge, which converts the WHOLE incoming object to its
		// typed form first. That conversion fails whenever any unrelated field holds a value its schema
		// does not accept -- including values Kubernetes itself considers valid. With failurePolicy: Fail
		// the write is then DENIED, so one object with an awkward field can be rejected by a mutation that
		// only ever wanted to add a label. There, a composition whose schema typed cpu as numeric but
		// whose value was the Quantity string "200m" wedged an entire install.
		//
		// A JSON Patch touches only the path it names. Nothing else is parsed, converted or validated, so
		// an unrelated field's representation cannot deny the request.
		//
		// The two branches are required: a JSON Patch `add` to /metadata/labels/<key> fails when
		// /metadata/labels does not exist, so an object with no labels needs the map created in one step.
		// jsonpatch.escapeKey handles RFC 6901 escaping of "/" in the label key (it becomes ~1) -- hand
		// writing that escape is exactly the thing that works until someone renames the label.
		"mutations": []any{map[string]any{
			"patchType": "JSONPatch",
			"jsonPatch": map[string]any{
				"expression": `has(object.metadata.labels)` +
					` ? [JSONPatch{op: "add", path: "/metadata/labels/" + jsonpatch.escapeKey("` + VersionLabel + `"), value: request.requestKind.version}]` +
					` : [JSONPatch{op: "add", path: "/metadata/labels", value: {"` + VersionLabel + `": request.requestKind.version}}]`,
			},
		}},
	}

	b := &unstructured.Unstructured{}
	b.SetAPIVersion(policyAPIVersion)
	b.SetKind("MutatingAdmissionPolicyBinding")
	b.SetName(name)
	b.Object["spec"] = map[string]any{"policyName": name}

	return p, b
}

// EnsureVersionPolicy guarantees the oas-version policy and its binding exist for group.
//
// Create-if-absent and idempotent: an existing policy is left untouched, so this never fights another
// field manager, and two RestDefinitions sharing a group converge rather than conflict.
//
// A cluster without the MutatingAdmissionPolicy API (Kubernetes < 1.36, which the chart's 1.33 floor
// permits) is NOT an error. The label is then stamped by rest-dynamic-controller on first reconcile
// instead. Returning an error here would make an otherwise working install fail on a capability the
// chart does not require.
//
// The policy is a per-group singleton shared by every RestDefinition in that group, so it is
// intentionally never removed on RestDefinition deletion: another definition in the same group may still
// depend on it, and an orphaned policy stamps a label nobody reads, which costs nothing.
func EnsureVersionPolicy(ctx context.Context, kube client.Client, group string) error {
	if group == "" {
		return fmt.Errorf("cannot ensure version policy: empty API group")
	}

	p, b := objects(group)
	// The policy before the binding: the binding references it by name.
	for _, o := range []*unstructured.Unstructured{p, b} {
		if err := kube.Create(ctx, o); err != nil {
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			if IsUnsupported(err) {
				// Pre-1.36 cluster. RDC's own stamping covers it.
				return nil
			}
			return fmt.Errorf("creating %s %q: %w", o.GetKind(), o.GetName(), err)
		}
	}
	return nil
}

// IsUnsupported reports whether err means the cluster does not serve the MutatingAdmissionPolicy API.
//
// The apiserver expresses this as a NoMatch from the RESTMapper, or a NotFound on the discovery path,
// depending on how the client resolved the GVK. Both mean the same thing here and neither is a failure
// of this install.
func IsUnsupported(err error) bool {
	if err == nil {
		return false
	}
	if meta := apierrors.IsNotFound(err); meta {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "no matches for kind") ||
		strings.Contains(msg, "could not find the requested resource") ||
		strings.Contains(msg, "no kind is registered")
}
