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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	// SpecHashAnnotation records the hash of the spec a policy was created from.
	//
	// The policy is created-if-absent and never updated, so without this there is no way to tell that the
	// policy on a cluster is not the one the running build would write -- no generation to compare, no
	// event when it diverges, and the divergence grows silently as versions ship. A policy created before
	// this annotation existed has none, which reads as stale, which is correct: those are exactly the ones
	// that cannot be corrected except by deleting them.
	SpecHashAnnotation = "krateo.io/oas-version-policy-hash"
)

// Outcome is what EnsureVersionPolicy found or did, for a caller that wants to report drift without
// treating it as a failure.
type Outcome int

const (
	// OutcomeUnknown is the zero value, returned alongside an error.
	OutcomeUnknown Outcome = iota
	// OutcomeCreated means the policy was absent and has been created.
	OutcomeCreated
	// OutcomeCurrent means a policy for this group exists and matches what this build would create.
	OutcomeCurrent
	// OutcomeStale means a policy for this group exists but was created from a different spec, or from a
	// build predating SpecHashAnnotation. It still stamps, so it is not an error -- but it is not what
	// this build would write, and it will never be updated in place.
	OutcomeStale
	// OutcomeUnsupported is retained so the String() switch stays total, but EnsureVersionPolicy no
	// longer returns it: an absent API is now ErrPolicyAPIUnsupported. See that type.
	OutcomeUnsupported
)

func (o Outcome) String() string {
	switch o {
	case OutcomeCreated:
		return "created"
	case OutcomeCurrent:
		return "current"
	case OutcomeStale:
		return "stale"
	case OutcomeUnsupported:
		return "unsupported"
	default:
		return "unknown"
	}
}

// ErrPolicyGroupMismatch is returned when the policy name for this group is already taken by a policy
// matching a DIFFERENT group.
//
// PolicyName slugifies with [^a-z0-9]+ -> "-", so every non-alphanumeric character collapses to the same
// separator and distinct API groups can share a name: github.krateo.io, github-krateo.io and
// github.krateo-io all produce krateo-oas-version-github-krateo-io.
//
// Treating that collision as success -- which an unexamined AlreadyExists does -- means the second group's
// instances match no policy and are never stamped. No error, no event, and the RestDefinition reports
// healthy. That is the silent non-reconcile failurePolicy: Fail was chosen to prevent; Fail only engages
// when a policy MATCHES the request, and here none does.
type ErrPolicyGroupMismatch struct {
	Policy string
	Want   string
	Got    []string
}

func (e *ErrPolicyGroupMismatch) Error() string {
	return fmt.Sprintf(
		"policy %q already exists for API group(s) %v, not %q: these groups differ only by characters the policy name collapses, so they cannot both be stamped; rename one group",
		e.Policy, e.Got, e.Want)
}

// ErrPolicyAPIUnsupported is returned when the cluster does not serve MutatingAdmissionPolicy.
//
// This USED to be tolerated: the chart's floor was 1.33, the API arrives in 1.36, and
// rest-dynamic-controller stamped the label itself on first reconcile, so an absent API only meant the
// label arrived later than admission.
//
// Version-scoped watching removes that fallback. A controller watching with exact equality on
// krateo.io/oas-version never observes an unlabelled instance, so it never stamps one either -- the
// instance is created, looks healthy, and is reconciled by nobody. There is no longer any mechanism that
// covers an absent policy, which is why this is an error rather than a tolerated state, and why the chart
// floor moved to 1.36 in the same change.
//
// Symmetric with core-provider, which states the same requirement outright: the composition-version label
// is stamped by a MutatingAdmissionPolicy that must exist in every cluster a composition CRD lives in.
type ErrPolicyAPIUnsupported struct {
	Group string
	Err   error
}

func (e *ErrPolicyAPIUnsupported) Error() string {
	return fmt.Sprintf(
		"this cluster does not serve MutatingAdmissionPolicy, which Kubernetes 1.36+ provides and version-scoped watching requires: instances in API group %q would be admitted without the %s label and then matched by no controller's watch (%v)",
		e.Group, VersionLabel, e.Err)
}

func (e *ErrPolicyAPIUnsupported) Unwrap() error { return e.Err }

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

	// Stamp the spec hash on create, so a later version can TELL that a policy on the cluster is not the
	// one it would write. This is the only part of #172 that cannot be added retroactively: the policy is
	// never updated, so a policy created without the annotation can never acquire one.
	h := specHash(p)
	p.SetAnnotations(map[string]string{SpecHashAnnotation: h})
	b.SetAnnotations(map[string]string{SpecHashAnnotation: h})

	return p, b
}

// specHash is a content hash of the policy spec, used to notice that an existing policy differs from the
// one this build would create.
//
// Hashing the spec rather than stamping a version number means any change to the mutation expression, the
// match constraints or the failure policy is detectable, without anyone having to remember to bump
// something. encoding/json sorts map keys, so the encoding is stable across runs.
func specHash(p *unstructured.Unstructured) string {
	b, err := json.Marshal(p.Object["spec"])
	if err != nil {
		// Cannot happen for a map we just built, but returning a constant would make every policy compare
		// equal, which is the one answer that hides drift.
		return "unhashable"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// policyGroups returns every apiGroup the policy's match constraints name.
func policyGroups(p *unstructured.Unstructured) []string {
	rules, found, err := unstructured.NestedSlice(p.Object, "spec", "matchConstraints", "resourceRules")
	if err != nil || !found {
		return nil
	}
	var out []string
	for _, r := range rules {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		groups, found, err := unstructured.NestedStringSlice(m, "apiGroups")
		if err != nil || !found {
			continue
		}
		out = append(out, groups...)
	}
	return out
}

// matchesOnly reports whether groups is exactly [group].
func matchesOnly(groups []string, group string) bool {
	return len(groups) == 1 && groups[0] == group
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
func EnsureVersionPolicy(ctx context.Context, kube client.Client, group string) (Outcome, error) {
	if group == "" {
		return OutcomeUnknown, fmt.Errorf("cannot ensure version policy: empty API group")
	}

	p, b := objects(group)

	// The policy before the binding: the binding references it by name.
	created := false
	switch err := kube.Create(ctx, p); {
	case err == nil:
		created = true
	case IsUnsupported(err):
		return OutcomeUnknown, &ErrPolicyAPIUnsupported{Group: group, Err: err}
	case !apierrors.IsAlreadyExists(err):
		return OutcomeUnknown, fmt.Errorf("creating %s %q: %w", p.GetKind(), p.GetName(), err)
	}

	// An existing policy is still left untouched -- the create-if-absent contract is unchanged, and this
	// never fights another field manager. What changed is that AlreadyExists is no longer accepted without
	// looking: it cannot otherwise distinguish two RestDefinitions converging on their shared group's
	// policy (intended) from two DIFFERENT groups colliding on one slugified name (silent non-stamping).
	outcome := OutcomeCreated
	if !created {
		cur := &unstructured.Unstructured{}
		cur.SetAPIVersion(policyAPIVersion)
		cur.SetKind("MutatingAdmissionPolicy")
		if err := kube.Get(ctx, client.ObjectKey{Name: p.GetName()}, cur); err != nil {
			if IsUnsupported(err) {
				return OutcomeUnknown, &ErrPolicyAPIUnsupported{Group: group, Err: err}
			}
			return OutcomeUnknown, fmt.Errorf("reading the existing policy %q: %w", p.GetName(), err)
		}

		if groups := policyGroups(cur); !matchesOnly(groups, group) {
			return OutcomeUnknown, &ErrPolicyGroupMismatch{Policy: p.GetName(), Want: group, Got: groups}
		}

		outcome = OutcomeCurrent
		if cur.GetAnnotations()[SpecHashAnnotation] != p.GetAnnotations()[SpecHashAnnotation] {
			outcome = OutcomeStale
		}
	}

	// Always attempt the binding, even when the policy already existed: the two are separate objects and
	// either can be deleted without the other. A policy with no binding is inert, so it would stamp
	// nothing while looking present.
	if err := kube.Create(ctx, b); err != nil {
		if IsUnsupported(err) {
			return OutcomeUnknown, &ErrPolicyAPIUnsupported{Group: group, Err: err}
		}
		if !apierrors.IsAlreadyExists(err) {
			return OutcomeUnknown, fmt.Errorf("creating %s %q: %w", b.GetKind(), b.GetName(), err)
		}
	}

	return outcome, nil
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
