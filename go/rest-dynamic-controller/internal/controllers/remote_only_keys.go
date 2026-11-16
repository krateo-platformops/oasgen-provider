package restResources

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/krateo-platformops/plumbing/kubeutil/event"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/text"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/builder"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

// remoteOnlyBodyKeys reports, per top-level spec field that the UPDATE verb sends in its body, the keys
// the vendor currently holds and the CR does not express.
//
// WHY THIS IS THE FIRST HALF OF #400. processFields assembles the request body by top-level spec key:
//
//	if callInfo.ReqParams.Body.Contains(field) {
//	    if mapBody[field] == nil { mapBody[field] = value }
//	}
//
// The whole value goes in under its key. There is no per-key merge anywhere in the path and nothing reads
// what the vendor currently holds -- so a CR modelling PART of a map enforces that partial view as the
// WHOLE object, and the unmodelled remainder is deleted with no warning, no condition and no log line.
//
// MEASURED, by openstack-ironic-operator-kog on a live cluster. A sidecar mirrored two of the four keys a
// bare-metal node publishes into Ironic's node.extra; RDC sent those two as the whole `extra`, deleting
// `etcd_ip` and `cp_endpoint`. Lifetime of the deleted values: 82 seconds, across 6 PATCHes -- a sustained
// enforcement loop, not a race. `cp_endpoint` was a control-plane VIP: every replica then rendered
// `curl -k https:///healthz`, a URL with no host, polled it 120 times and exited 1, while Ironic reported
// `active` for all six machines. The controlled arm is what makes it attributable: blade01 had a non-empty
// spec.extra and lost keys, blades 02 and 03 had NO spec.extra, were sent no `extra` at all, and kept
// theirs -- same cluster, same minute.
//
// #400's own framing of the defect is the reason THIS function exists rather than only a merge: "nothing
// distinguishes 'this map is the complete desired state' from 'this map is a partial view', and nothing
// says anything when a key present on the server is about to be removed." The merge fixes the first half
// for an author who DECLARES it. This fixes the second half for everyone, including the author who does
// not yet know the problem exists -- which is every author today, and was the Ironic author.
//
// MAPS ONLY, and the omission is deliberate rather than partial work. An array is replayed whole too, but
// saying WHICH element would be lost needs a notion of element identity, and this repo already has three
// different ones in flight (positional comparison, set-typed arrays, map-list keys). Picking one here, in
// a warning, would quietly establish it for the merge as well. Arrays get their own decision.
//
// SCOPED TO THE UPDATE BODY, because a field the update does not send cannot be deleted by it. Warning
// about a field that never leaves the cluster would be a false positive, and a false positive in a warning
// about data loss is worse than silence: it teaches the reader to ignore the real one.
//
// Returns nil when there is nothing to report, so the caller's zero-write path stays zero-write.
func remoteOnlyBodyKeys(spec, remote map[string]interface{}, bodyFields text.StringSet) map[string][]string {
	if len(spec) == 0 || len(remote) == 0 {
		return nil
	}
	var out map[string][]string
	for field, specValue := range spec {
		if field == "" || !bodyFields.Contains(field) {
			continue
		}
		specMap, ok := specValue.(map[string]interface{})
		if !ok {
			continue
		}
		remoteMap, ok := remote[field].(map[string]interface{})
		if !ok {
			// The vendor does not hold a map there -- either absent, or a different kind of value. Either
			// way there are no keys of its own to lose, and a type disagreement is a different defect that
			// the drift comparison already reports.
			continue
		}
		var lost []string
		for k := range remoteMap {
			if _, present := specMap[k]; !present {
				lost = append(lost, k)
			}
		}
		if len(lost) == 0 {
			continue
		}
		// Sorted so the warning text is stable across reconciles. An unstable message would make the
		// once-per-generation signature below useless -- and a condition or event whose text churns is a
		// write surface, which is the reason this repo keeps counts out of condition messages.
		sort.Strings(lost)
		if out == nil {
			out = make(map[string][]string, 1)
		}
		out[field] = lost
	}
	return out
}

// specFields returns the CR's top-level spec map, or nil.
func specFields(mg *unstructured.Unstructured) map[string]interface{} {
	if mg == nil {
		return nil
	}
	m, found, err := unstructured.NestedMap(mg.Object, "spec")
	if err != nil || !found {
		return nil
	}
	return m
}

// annotationUnmodelledKeysReported records the signature of the last unmodelled-key report, so the
// warning fires once per (generation, finding) rather than once per poll.
//
// THE SAME LESSON AS #717, WHICH IS WHY IT IS HERE FROM THE START. A permanent condition re-emitted every
// pass is not a diagnostic: a missing RBAC rule produced 28 Events whose count climbed ~6/min for six days
// and one telemetry record per increment, and the only thing an operator could read from it was that
// something had been wrong for a while. This finding is permanent in exactly the same way -- it clears
// only when a human changes the CR or the vendor -- so it is reported on transition.
//
// The generation is in the signature for the reason driftSignature gives: a human who edits the CR in
// response to the warning must be told again if they did not fix it, and generation is precisely the
// apiserver's record of "the spec changed".
//
// The FINDING is in the signature too, not just the generation: a vendor that grows a new unmodelled key
// without anyone touching the CR is a new fact and must be reported, and that is the case where a
// generation-only key would stay silent forever.
const annotationUnmodelledKeysReported = "krateo.io/unmodelled-keys-reported"

// unmodelledKeysSignature identifies one finding for the purpose of "is this the same one again".
func unmodelledKeysSignature(mg *unstructured.Unstructured, found map[string][]string) string {
	if mg == nil || len(found) == 0 {
		return ""
	}
	fields := make([]string, 0, len(found))
	for f := range found {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	var b strings.Builder
	fmt.Fprintf(&b, "gen=%d", mg.GetGeneration())
	for _, f := range fields {
		fmt.Fprintf(&b, ";%s=%s", f, strings.Join(found[f], ","))
	}
	return b.String()
}

// reportUnmodelledKeys emits one Warning naming the fields and the keys an update would remove, and
// records the finding so the next identical pass is silent. It reports whether it emitted.
//
// AN EVENT, AND DELIBERATELY NOT A Ready CONDITION. A Ready reason this repo publishes and a consumer does
// not map falls to core-provider's `default: childConverging`, which holds every parent composition at
// Ready=False/Creating for as long as the state lasts -- and this state lasts until a human acts. That is
// how AuthorizationDenied and DriftNotConverging each wedged parents before their arms were added, and
// those were states that at least WERE unhealthy. This one is not: the resource is reconciling correctly
// and the DECLARATION is incomplete. Wedging a composition over a declaration gap would be a worse defect
// than the one being reported, and it would arrive on clusters whose core-provider predates any mapping.
//
// Not fatal and not a drift signal either: reporting drift is what makes the reconciler call Update, and
// an Update is the thing that performs the deletion.
func (h *handler) reportUnmodelledKeys(ctx context.Context, mg *unstructured.Unstructured, found map[string][]string, log logging.Logger) bool {
	sig := unmodelledKeysSignature(mg, found)
	if sig == "" {
		return false
	}
	if mg.GetAnnotations()[annotationUnmodelledKeysReported] == sig {
		return false
	}

	fields := make([]string, 0, len(found))
	for f := range found {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		parts = append(parts, fmt.Sprintf("spec.%s: %s", f, strings.Join(found[f], ", ")))
	}
	detail := strings.Join(parts, "; ")

	h.eventRecorder.Event(mg, event.Warning(reasonUnmodelledKeys, "Observe", fmt.Errorf(
		"the vendor holds keys this CR does not express, and an update sends each of these fields WHOLE, "+
			"so issuing one would DELETE them -- %s. Nothing is failing: the resource reconciles correctly "+
			"and the declaration is incomplete. Either model the missing keys in the CR, or confirm the CR "+
			"is meant to own the whole field", detail)))
	log.Info("An update would remove keys the vendor holds and the CR does not express",
		"kind", mg.GetKind(), "name", mg.GetName(), "fields", detail)

	if perr := h.patchAnnotations(ctx, mg, map[string]interface{}{
		annotationUnmodelledKeysReported: sig,
	}); perr != nil {
		// A failed record must not fail the reconcile. The warning has already been emitted; losing the
		// bookkeeping costs a repeat next pass, which is strictly better than wedging the resource it is
		// warning about.
		log.Debug("Could not record the unmodelled-keys report", "error", perr)
	}
	return true
}

// updateBodyFields resolves which top-level CR fields the UPDATE verb puts in its request body.
//
// Built from the update verb's own document, via the same clientSet lookup the drift comparison already
// uses for list semantics -- a second client for a second document would otherwise go out
// unauthenticated, which is the reason clientSet exists (#220).
//
// APICallBuilder returns (nil, nil, nil) when the verb is absent, so a kind declaring no update returns
// an empty set and no error: there is no update to delete anything, and nothing to warn about.
func (h *handler) updateBodyFields(ctx context.Context, cs *clientSet, clientInfo *getter.Info) (text.StringSet, error) {
	if cs == nil || clientInfo == nil {
		return nil, nil
	}
	ucli, err := cs.For(ctx, string(apiaction.Update))
	if err != nil {
		return nil, err
	}
	_, callInfo, err := builder.APICallBuilder(ucli, clientInfo, apiaction.Update)
	if err != nil {
		return nil, err
	}
	if callInfo == nil || callInfo.ReqParams == nil {
		return nil, nil
	}
	return callInfo.ReqParams.Body, nil
}
