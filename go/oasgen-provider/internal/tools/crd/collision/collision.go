// Package collision refuses to mint a CRD whose resource names would shadow a built-in resource.
//
// A generated plural is derived from the OpenAPI document's own resource name, and vendor APIs are full
// of names Kubernetes already serves: "deployment", "service", "secret", "job", "event", "node", "role",
// "ingress". GitHub's document contains "deployment", so generation minted
// `deployments.github.krateo.io` and two API groups then claimed the plural `deployments`. kubectl
// refuses the bare two-argument form rather than pick one:
//
//	$ kubectl get deployment krateo-clickstack -n krateo-system
//	error: you must specify only one resource
//
// which breaks tooling that has nothing to do with this provider, a long way from where the collision
// was created (#200).
//
// The check deliberately covers EVERY non-CRD group rather than the core group. `deployments` lives in
// `apps/v1`, not `v1`, so a guard written against "core Kubernetes kinds" would let the very case that
// prompted this straight through. The set is enumerated from the discovery API at generation time, not
// hardcoded: it varies by cluster version and by which aggregated APIServices are installed, and a
// hardcoded list is a list that goes stale.
package collision

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// coreGroupDisplay is what the empty group is called when a message has to name it.
const coreGroupDisplay = "core"

// BuiltInNames is the set of resource names -- plurals AND singulars -- served by API groups that are
// NOT backed by a CustomResourceDefinition.
//
// Singulars matter as much as plurals, and arguably more. The sibling case in authn
// (krateo-platformops/authn#34) shadows a singular and returns a WRONG ANSWER instead of an error:
// `kubectl get serviceaccount` lists 6 objects where 133 exist. A loud ambiguity error is the better
// of the two failures, so the guard covers both names rather than only the one that errors.
type BuiltInNames struct {
	// byName maps a resource name to the groups serving it, so a refusal can name the conflict.
	byName map[string][]string

	// Partial records that discovery returned an incomplete picture, usually because an aggregated
	// APIService is unhealthy. An incomplete set can only produce FALSE NEGATIVES -- a name absent from
	// it is simply not checked -- so every refusal it does produce is still sound. Reported so the
	// caller can say the check was best-effort rather than silently claim a clean bill of health.
	Partial bool
}

// Len is the number of distinct names in the set. Deliberately no expected value is asserted anywhere
// against it: two sessions enumerating the built-ins of one cluster got different totals purely from
// how they deduplicated multi-group and aggregated-APIService entries, so a count is a hostage. The set
// is what matters.
func (b *BuiltInNames) Len() int {
	if b == nil {
		return 0
	}
	return len(b.byName)
}

// Discover enumerates the resource names served by every non-CRD API group on the cluster.
//
// "Non-CRD" is established by subtracting the resources that CustomResourceDefinitions serve, rather
// than by guessing from the group name. That subtraction is what keeps the guard from firing on a
// resource THIS provider already generated -- on a re-reconcile its own CRD is in discovery, and
// without the subtraction every generated resource would collide with itself.
//
// It also means a collision between two CRDs is not this guard's business: `services.serviceusage.
// cnrm.cloud.google.com` shares a plural with core `services`, but a third-party CRD is not something
// generation can refuse its way out of.
func Discover(ctx context.Context, disc discovery.DiscoveryInterface, kube client.Client) (*BuiltInNames, error) {
	// A missing client is reported, never dereferenced. The guard runs inside the reconcile hot path,
	// where a nil-interface panic would be a far worse outcome than the collision it exists to catch --
	// and the caller already treats "cannot read the built-ins" as fail-open.
	if disc == nil || kube == nil {
		return nil, fmt.Errorf("no discovery or kube client available to enumerate built-in resource names")
	}

	crdBacked, err := crdBackedResources(ctx, kube)
	if err != nil {
		return nil, fmt.Errorf("listing CustomResourceDefinitions: %w", err)
	}

	out := &BuiltInNames{byName: map[string][]string{}}

	_, lists, err := disc.ServerGroupsAndResources()
	if err != nil {
		// Partial results are the normal shape of this error: one unhealthy aggregated APIService fails
		// its group and the rest still come back. Returning nothing there would turn an unrelated
		// APIService outage into a generation outage, so the partial set is used and flagged.
		if !discovery.IsGroupDiscoveryFailedError(err) {
			return nil, fmt.Errorf("discovering server resources: %w", err)
		}
		out.Partial = true
	}

	for _, list := range lists {
		if list == nil {
			continue
		}
		gv, perr := schema.ParseGroupVersion(list.GroupVersion)
		if perr != nil {
			// A groupVersion this client cannot parse is one it cannot attribute either. Skipping it
			// costs a possible false negative, which is the same cost as Partial above.
			out.Partial = true
			continue
		}
		for _, r := range list.APIResources {
			// Subresources ("deployments/status") are not addressable as `kubectl get <name>`, so they
			// cannot be collided with.
			if strings.Contains(r.Name, "/") {
				continue
			}
			if _, isCRD := crdBacked[gv.Group+"/"+r.Name]; isCRD {
				continue
			}
			out.add(r.Name, gv.Group)
			out.add(r.SingularName, gv.Group)
		}
	}

	return out, nil
}

func (b *BuiltInNames) add(name, group string) {
	if name == "" {
		return
	}
	if group == "" {
		group = coreGroupDisplay
	}
	for _, g := range b.byName[name] {
		if g == group {
			return
		}
	}
	b.byName[name] = append(b.byName[name], group)
}

// crdBackedResources returns the `group/plural` of every resource served by a CustomResourceDefinition.
func crdBackedResources(ctx context.Context, kube client.Client) (map[string]struct{}, error) {
	list := &apiextensionsv1.CustomResourceDefinitionList{}
	if err := kube.List(ctx, list); err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		out[item.Spec.Group+"/"+item.Spec.Names.Plural] = struct{}{}
	}
	return out, nil
}

// Error reports a generated name that a built-in resource already serves.
type Error struct {
	// Name is the generated resource name that collides.
	Name string
	// Kind is the generated CRD's kind, which is what an author would have to change.
	Kind string
	// CRDName is the full name of the CRD that would have been minted.
	CRDName string
	// Groups are the non-CRD API groups that already serve Name.
	Groups []string
}

func (e *Error) Error() string {
	return fmt.Sprintf(
		"generated resource name %q (kind %q, CRD %q) is already served by the built-in API group(s) %s; "+
			"two groups claiming one name makes `kubectl get %s <name>` ambiguous for the BUILT-IN resource, "+
			"breaking tooling unrelated to this provider, so generation refuses it. Set spec.resource.kind to a "+
			"name that does not collide (for example a vendor prefix such as %q)",
		e.Name, e.Kind, e.CRDName, strings.Join(e.Groups, ", "), e.Name, suggestPrefix(e.Kind, e.CRDName))
}

// suggestPrefix proposes a deterministic non-colliding kind, using the CRD's own group as the prefix:
// `Deployment` in `github.krateo.io` becomes `GithubDeployment`.
//
// A suggestion rather than something applied automatically. A silent rename changes the resource name
// every Composition author has to type, and would be discovered exactly the way this collision was --
// much later, somewhere else.
func suggestPrefix(kind, crdName string) string {
	parts := strings.SplitN(crdName, ".", 3)
	if len(parts) < 2 || parts[1] == "" {
		return kind
	}
	prefix := parts[1]
	return strings.ToUpper(prefix[:1]) + prefix[1:] + kind
}

// Check reports whether crd's plural or singular is already served by a built-in resource.
//
// A nil or empty set checks nothing and returns nil: when discovery could not be reached the guard is
// absent, not satisfied, and the caller is expected to say so.
func (b *BuiltInNames) Check(crd *apiextensionsv1.CustomResourceDefinition) error {
	if b == nil || crd == nil || len(b.byName) == 0 {
		return nil
	}

	// Plural first: it is the name kubectl resolves, so it is the one that produces the reported error.
	for _, name := range []string{crd.Spec.Names.Plural, crd.Spec.Names.Singular} {
		if name == "" {
			continue
		}
		groups, collides := b.byName[name]
		if !collides {
			continue
		}
		sorted := append([]string(nil), groups...)
		sort.Strings(sorted)
		return &Error{
			Name:    name,
			Kind:    crd.Spec.Names.Kind,
			CRDName: crd.Name,
			Groups:  sorted,
		}
	}
	return nil
}
