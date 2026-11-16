package restResources

import (
	"maps"
	"sort"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/text"
)

// mergePartialFields overlays the CR's keys onto the vendor's current value, for each top-level body
// field the verb declared PARTIAL, and returns the fields it merged.
//
// WHY A MERGE AND NOT A REPLACEMENT (issue 400). processFields assembles the request body by top-level
// spec key, so the whole value goes out under its key. Where the CR models only PART of what the vendor
// holds there, the remainder is deleted. Measured twice, on two vendors, with controls:
//
//	Ironic   spec.extra held 2 of 4 keys; etcd_ip and cp_endpoint were deleted. 82 seconds without a
//	         control-plane VIP, across 6 PATCHes. Blades with NO spec.extra were sent none and kept theirs.
//	GitHub   a webhook PATCH whose config omitted `secret` CLEARED the HMAC secret. A PATCH of `active`
//	         only kept it. Availability in the first case; INTEGRITY, silently, in the second.
//
// THE CR WINS ON EVERY KEY IT EXPRESSES. This is not a three-way merge and there is no attempt to detect
// an intentional deletion: a key the CR used to carry and no longer does is simply left at the vendor's
// value. That is the conservative direction -- the alternative is inferring deletion from absence, which
// is exactly the inference that produced this defect.
//
// ONE LEVEL, MATCHING THE WARNING. The overlay replaces whole values under the field's top-level keys; it
// does not recurse. #732 records the same limit on the detection side, and the two must agree: a merge
// that walked deeper than the warning would silently fix losses nobody was told about, and a warning that
// walked deeper than the merge would name losses the merge then caused anyway.
//
// Returns nil when nothing was merged, so a caller can tell "no declared field needed merging" from
// "a declared field was merged".
func mergePartialFields(body, remote map[string]interface{}, partial text.StringSet) []string {
	if len(body) == 0 || len(remote) == 0 || len(partial) == 0 {
		return nil
	}
	var merged []string
	for field := range partial {
		if field == "" {
			continue
		}
		crValue, inBody := body[field]
		if !inBody {
			// The CR does not express this field at all, so the request carries nothing for it and the
			// vendor replaces nothing. That is already the safe state -- it is the arm the live Ironic
			// control measured -- so there is nothing to merge.
			//
			// REDUNDANT WITH THE TYPE ASSERTION BELOW, and said so rather than implied: an absent key
			// yields a nil crValue, whose assertion to map[string]interface{} fails and continues anyway.
			// A mutation forcing inBody to true killed NO test, which is how that was established rather
			// than assumed. It is kept because "the CR does not express this field" and "the CR expresses
			// it as something other than an object" are different situations with different reasons, and
			// collapsing them into one unexplained `continue` loses the one that matters here.
			continue
		}
		crMap, ok := crValue.(map[string]interface{})
		if !ok {
			continue
		}
		remoteMap, ok := remote[field].(map[string]interface{})
		if !ok {
			// Nothing of the vendor's to preserve: absent, or a different kind of value. A type
			// disagreement is a separate defect that the drift comparison reports.
			continue
		}

		// Sized from the vendor's value alone, NOT len(remoteMap)+len(crMap). The sum was flagged by
		// CodeQL as go/allocation-size-overflow, and while two maps large enough to overflow an int are
		// not reachable, the arithmetic bought nothing: a capacity hint is an optimisation, and the map
		// grows by itself for whatever the CR adds on top. Removing the operation is clearer than
		// explaining why the overflow cannot happen.
		//
		// maps.Clone would read better still and is NOT used: a nil map stored in an interface satisfies
		// the type assertion above, Clone returns nil for it, and the first assignment into a nil map
		// panics. make + Copy is nil-safe at both ends.
		out := make(map[string]interface{}, len(remoteMap))
		maps.Copy(out, remoteMap) // the vendor's current value
		maps.Copy(out, crMap)     // then the CR, which wins on every key it expresses
		if len(out) == len(crMap) {
			// The CR already expressed everything the vendor holds, so the merge is a no-op. Reporting it
			// as merged would make the log claim a rescue that did not happen.
			continue
		}
		body[field] = out
		merged = append(merged, field)
	}
	sort.Strings(merged)
	return merged
}
