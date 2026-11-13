package restResources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/fieldmapping"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// statusResolvedResolversDigest is the status key this controller writes.
//
// oasgen-provider DECLARES it (oas2jsonschema.StatusResolvedResolversDigest). It is a separate Go
// module in this same repository and that constant lives under internal/, so the two cannot share a
// definition -- `testdata/resolver-digest-contract.json` pins them against each other instead. If
// they ever disagree, the apiserver prunes every write this controller makes, the write exits 0, and
// the rotation stays undetected: issue 598's own defect arriving through its fix. See
// shared_resolver_digest_test.go.
const statusResolvedResolversDigest = "resolvedResolversDigest"

var secretGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// updateScopedSecretRefMapping returns the UPDATE verb's field-mapping entries, which are the only
// ones whose secretRef resolvers may contribute to the digest.
//
// THE SCOPE RULE, and it is a correctness constraint rather than a narrowing for economy. A digest
// difference's only remedy is an update. A resolver the update verb does not send is one whose
// rotation an update cannot push, so a difference in it can never be retired: Observe would report
// drift, Update would issue a request that does not carry the value, the next Observe would see the
// same difference, and so on -- one vendor write per poll, per CR, forever.
//
// This controller already applies exactly this principle to ordinary drift a few hundred lines up
// ("No update verb, or one without a JSON body, means nothing about this resource is fixable by an
// update -- so nothing should be reported as drift"). A digest is drift arriving by another channel
// and it takes the same rule.
//
// Returning nothing when there is no update verb is therefore the right answer and not a degenerate
// case: such a resource has no remedy for anything, so it reports no drift of any kind.
func updateScopedSecretRefMapping(r *getter.Resource) []getter.FieldMappingItem {
	if r == nil {
		return nil
	}
	for _, v := range r.VerbsDescription {
		if !strings.EqualFold(v.Action, string(apiaction.Update)) {
			continue
		}
		return v.FieldMapping
	}
	return nil
}

// resolverDigestSources collects the update-scoped secretRef sources for this CR instance. No I/O.
func resolverDigestSources(r *getter.Resource, mg *unstructured.Unstructured) ([]fieldmapping.SecretRefSource, error) {
	mapping := updateScopedSecretRefMapping(r)
	if len(mapping) == 0 {
		return nil, nil
	}
	return fieldmapping.CollectSecretRefSources(mapping, mg)
}

// resolverDigest computes the digest over a set of already-fingerprinted sources.
//
// The input strings are pre-sorted by CollectSecretRefSources and each is "<source>\x00<fingerprint>",
// so the digest is stable under reordering of the mapping and changes if any source is added,
// removed, repointed or refingerprinted.
func resolverDigest(parts []string) string {
	h := sha256.New()
	for _, p := range parts {
		// Length-prefixed, so no combination of source and fingerprint can be re-split differently
		// and collide with another set (a "a\x00bc" / "ab\x00c" pair digests identically otherwise).
		fmt.Fprintf(h, "%d:%s\n", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// fingerprintSource returns the per-source value fed into the digest.
//
// THIS IS THE ONE CHOICE-DEPENDENT FUNCTION in the feature, and it is isolated here deliberately.
//
// It returns the referenced Secret's RESOURCE VERSION -- not the secret's value. Every write to a
// Secret changes its resourceVersion, so this never misses a content change; and it reveals nothing
// about the content, so the digest is safe to publish in a status field that anyone with `get` on
// the CR can read.
//
// WHY NOT A DIGEST OF THE VALUE, which is what issue 598 proposed. A value digest in a readable
// field is a password-cracking oracle. Salting it with the resource's UID does not help, because the
// UID is readable by the same `get` that reads the digest. Measured, single-threaded, on this
// machine: a 6-digit PIN falls out of a UID-salted SHA-256 value digest in 0.25 seconds, and eight
// lowercase letters in about ten seconds on one mid-range GPU. The secret values a secretRef points
// at are, by construction, ones the CR's readers were NOT granted access to.
//
// THE COST of using resourceVersion instead: a write to the Secret that does not change the value it
// holds still moves the resourceVersion, so an update is issued that re-sends an unchanged value.
// That is one idempotent vendor write, it is visible and attributable, and it cannot corrupt
// anything. The value-digest alternative trades that for a permanent disclosure surface on every CR.
func (h *handler) fingerprintSource(ctx context.Context, src fieldmapping.SecretRefSource) (string, error) {
	sec, err := h.dynamicClient.Resource(secretGVR).Namespace(src.Namespace).
		Get(ctx, src.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading secret %s/%s for the resolver digest: %w", src.Namespace, src.Name, err)
	}
	// The KEY's presence is part of the fingerprint: a Secret that stops carrying the key is a change
	// the vendor needs to hear about, and reporting it as "unchanged" would hide it.
	data, _, derr := unstructured.NestedMap(sec.Object, "data")
	if derr != nil {
		return "", fmt.Errorf("reading data of secret %s/%s: %w", src.Namespace, src.Name, derr)
	}
	present := "absent"
	if _, ok := data[src.Key]; ok {
		present = "present"
	}
	return sec.GetResourceVersion() + "\x00" + present, nil
}

// resolverDigestNow computes this instance's current digest.
//
// inScope is false when the resource has no update-scoped secretRef resolver at all, which is the
// common case: the caller must then do nothing, not compare against an empty digest.
func (h *handler) resolverDigestNow(ctx context.Context, r *getter.Resource, mg *unstructured.Unstructured) (digest string, inScope bool, err error) {
	sources, err := resolverDigestSources(r, mg)
	if err != nil {
		return "", true, err
	}
	if len(sources) == 0 {
		return "", false, nil
	}
	parts := make([]string, 0, len(sources))
	for _, src := range sources {
		fp, ferr := h.fingerprintSource(ctx, src)
		if ferr != nil {
			return "", true, ferr
		}
		parts = append(parts, src.String()+"\x00"+fp)
	}
	return resolverDigest(parts), true, nil
}

// recordedResolverDigest reads the digest last recorded on the CR. An absent, null or non-string
// value reads as "" -- which the caller must treat as "never recorded", NOT as "differs from
// everything". See the bootstrap branch at the call site for why that distinction is what keeps a
// pruned field from becoming an update loop.
func recordedResolverDigest(mg *unstructured.Unstructured) string {
	v, found, err := unstructured.NestedString(mg.Object, "status", statusResolvedResolversDigest)
	if err != nil || !found {
		return ""
	}
	return v
}

// setResolverDigest sets the digest on the in-memory object. It does NOT write to the apiserver; the
// caller's existing status write carries it, which is also what makes the read-back free.
func setResolverDigest(mg *unstructured.Unstructured, digest string) error {
	return unstructured.SetNestedField(mg.Object, digest, "status", statusResolvedResolversDigest)
}

// resolverDigestWasStored reports whether a status write actually persisted the digest.
//
// THE READ-BACK, and it is the only thing that distinguishes "written" from "accepted and
// discarded". The generated status is a CLOSED schema: its properties are declared by
// oasgen-provider and there is no x-kubernetes-preserve-unknown-fields. A CRD generated before the
// field existed therefore PRUNES it -- the UpdateStatus call succeeds, returns no error, and the
// digest simply is not there. Two consumers hit that exact trap on other fields in one day, both
// exiting 0.
//
// `stored` must be the object the apiserver RETURNED from the write, not the one that was sent.
// tools.UpdateStatus returns the server's copy, so the caller already has it.
func resolverDigestWasStored(stored *unstructured.Unstructured, want string) bool {
	if stored == nil {
		return false
	}
	return recordedResolverDigest(stored) == want && want != ""
}
