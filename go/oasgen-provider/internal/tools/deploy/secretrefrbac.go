package deploy

import (
	"fmt"
	"sort"

	"github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/plumbing/fieldpath"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// anchorOf returns the prefix of segs up to and including its last selector or index -- the ENTRY a leaf
// path hangs off. It reports false when there is none, which means the path addresses a fixed location
// and "the entry is absent" is not a distinct case from "the leaf is absent".
func anchorOf(segs []fieldpath.Segment) ([]fieldpath.Segment, bool) {
	for i := len(segs) - 1; i >= 0; i-- {
		if segs[i].Kind == fieldpath.KindPredicate || segs[i].Kind == fieldpath.KindIndex {
			return segs[:i+1], true
		}
	}
	return nil, false
}

// secretRefResolverType is the resolver kind whose Secret this collects. Matched exactly, so a future
// resolver type cannot be swept in by a prefix or a contains check.
const secretRefResolverType = "secretRef"

// CollectSecretRefNames returns the Secret names a CR's secretRef resolvers reference, grouped by the
// namespace they will be read from, ready for SyncAuthSecretRBAC.
//
// # Why the PROVIDER collects this at all
//
// rest-dynamic-controller used to provision its own secretRef Role. It cannot: a subject cannot grant
// itself less than nothing, and self-imposed least privilege needs a broader grant to narrow FROM -- which
// is exactly what 0.53.0 removed. So the provider, whose ServiceAccount legitimately holds the broader
// permission, provisions on the controller's behalf (#510).
//
// # THE COLLECTOR IS LENIENT AND THE RESOLVER IS STRICT
//
// This is the rule that makes the leniency below safe, and it was articulated by a consumer rather than
// by me:
//
//	the collector   decides what the Role may NAME. It must be lenient, because an unresolvable entry on
//	                one credential must not block the Role for another on the same CR -- a password-only
//	                user must not fail because the OTP path it never used is unresolvable.
//	the resolver    decides whether the REQUEST is valid. It must be strict, because a user who declared
//	                an OTP credential must never be created password-only.
//
// So an entry that does not resolve contributes no name and no error. That is not tolerance of a mistake:
// the same path is resolved again, strictly, by the controller when it builds the request, and THAT is
// where a mistyped path or an unusable ref becomes a field-specific failure the author sees.
//
// Lenient collection plus strict resolution is a different system from lenient-everywhere. Without the
// second half, this function would silently grant nothing and nothing would ever say why.
//
// # What "does not resolve" covers, deliberately
//
// fieldpath.Get returns three outcomes and TWO of them mean "no name here":
//
//	found=false, err=nil   absent. The entry is not on this CR -- a password-only user's OTP path.
//	err != nil             the path cannot APPLY: a null intermediate (`secretDataSecretRef: null`), a
//	                       predicate matching more than one element, a scalar where a map was expected.
//	found=true             a name, if it is a non-empty string.
//
// The null-intermediate case is why this is spelled out rather than left to generic error handling. It
// was pinned by plumbing's owner while porting the walker, with the warning that a collector must decide
// it explicitly. At a real apiserver a structural CRD PRUNES a null on a field that is not marked
// nullable, so the stored object has the key absent and this case collapses into the first -- but a unit
// test, or a preserve-unknown-fields subtree, still produces it.
//
// A PARSE failure is different in kind and is returned as an error: the path comes from the
// RestDefinition, not from the CR, so it is a DEFINITION defect that every CR of the kind shares. Being
// lenient about it would hide a broken definition behind "no Secrets to grant".
func CollectSecretRefNames(verbs []v1alpha1.VerbsDescription, cr *unstructured.Unstructured, log logging.Logger) (map[string][]string, error) {
	if cr == nil {
		return nil, nil
	}
	// The Secret is read from the CR's own namespace: the controller's own provisioning did the same
	// (EnsureSecretRole(..., mg.GetNamespace(), ...)), and a secretRef names a Secret beside its CR.
	ns := cr.GetNamespace()
	if ns == "" {
		return nil, fmt.Errorf("collecting secretRef names: the custom resource has no namespace")
	}

	seen := map[string]struct{}{}
	for _, verb := range verbs {
		for _, m := range verb.FieldMapping {
			if m.Resolver == nil || m.Resolver.Type != secretRefResolverType || m.Resolver.SecretRef == nil {
				continue
			}
			path := m.Resolver.SecretRef.NameFromCustomResource
			if path == "" {
				continue
			}
			segs, err := fieldpath.Parse(path)
			if err != nil {
				return nil, fmt.Errorf(
					"collecting secretRef names: verb %q declares nameFromCustomResource %q, which is not a "+
						"valid field path (%w); every CR of this kind is affected, so this is a definition "+
						"defect rather than missing data", verb.Action, path, err)
			}
			v, found, err := fieldpath.Get(cr.Object, segs)
			if !found && err == nil {
				// ABSENT, and WHICH absence matters to an operator reading a Role that came out narrower
				// than expected. Both of these return found=false with a nil error, so the leaf read alone
				// cannot tell them apart -- a consumer pointed this out after I claimed it could:
				//
				//	the ENTRY is absent        a password-only user has no OTP credential. Expected.
				//	the entry is present and
				//	  its REF is absent        the author declared the credential and left the ref off.
				//	                           The resolver will refuse the request loudly, so the CR
				//	                           already says so -- this line says why the ROLE is narrow.
				//
				// The anchor is the path up to and including its last selector or index: if THAT resolves,
				// the entry exists and only the ref is missing.
				if anchor, ok := anchorOf(segs); ok {
					if _, anchorFound, anchorErr := fieldpath.Get(cr.Object, anchor); anchorFound && anchorErr == nil {
						log.Debug("secretRef collection: the entry exists but its Secret reference is absent, so the Role will not name it",
							"verb", verb.Action, "path", path, "resource", cr.GetName())
						continue
					}
				}
				log.Debug("secretRef collection: no entry matches this path on this resource, so the Role will not name it",
					"verb", verb.Action, "path", path, "resource", cr.GetName())
				continue
			}
			if err != nil {
				// EXPLICIT, and a mutation test is why it is written this way.
				//
				// fieldpath.Get returns found=false alongside EVERY error, so `if err != nil || !found`
				// was dead code: dropping the err arm changed nothing, and a mutant that did so survived.
				// The code read as though it handled the null-intermediate case deliberately while in fact
				// handling it by accident of `found` being false -- which is exactly what plumbing's owner
				// warned against ("don't let it fall into whatever err handling is generic").
				//
				// AND THE SEPARATION IS NOT MUTATION-VERIFIABLE, which is worth stating rather than
				// implying otherwise. Replacing this condition with `if false` STILL passes every test,
				// because an error always arrives with found=false and the next arm catches it. No input
				// can produce err != nil with found == true, so the two arms are not independent and no
				// test can tell them apart.
				//
				// So this arm is legibility, not behaviour: it says which case is being handled, and it
				// does not prove it. Making it observable would need a logger here -- an absent entry and
				// an unusable ref are different facts an operator would want distinguished when a Role
				// comes out narrower than expected -- and that is a signature change and a decision,
				// not a tidy-up. Recorded rather than done.
				//
				// LENIENT, per the contract above: a null ref, a scalar where a map was expected, or a
				// selector matching more than one element contributes no name. The controller resolves the
				// same path strictly when it builds the request, and reports a field-specific error there.
				log.Debug("secretRef collection: this path cannot be read on this resource, so the Role will not name it",
					"verb", verb.Action, "path", path, "resource", cr.GetName(), "reason", err.Error())
				continue
			}
			name, ok := v.(string)
			if !ok || name == "" {
				log.Debug("secretRef collection: this path resolved to no usable Secret name, so the Role will not name it",
					"verb", verb.Action, "path", path, "resource", cr.GetName())
				continue
			}
			seen[name] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return map[string][]string{ns: names}, nil
}
