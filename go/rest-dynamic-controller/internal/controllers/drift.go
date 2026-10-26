// Drift-loop bookkeeping (#213).
//
// Observe answers "does the CR match the external resource"; the reconciler answers "no" by calling Update.
// That is correct as long as the update CAN fix the difference. When it cannot, the pair becomes a loop: the
// same difference is found, the same update is issued, nothing changes, once per poll interval, forever.
//
// The loop is not hypothetical and not cheap. A vendor that describes one concept differently in each
// direction -- request `instance: "K2A4"`, response `instance: {name, cpu, ram}` -- produces a difference no
// update can resolve, and `compareScope` cannot always exclude it, because the field a vendor reshapes is
// often exactly the one its update verb accepts. Against an API that refuses writes while a resource is
// mid-transition, each accepted write also drives the resource out of its ready state, which is then what
// refuses the next one: a healthy production resource oscillating on the poll interval for the life of the CR.
//
// So this records WHICH difference was last reported and how many times running, and stops issuing the
// update once that count says the update is not working. Stopping is not pretending: the resource stays NOT
// Ready, with the field named, because it genuinely does not match its spec.
package restResources

import (
	"context"
	"fmt"
	"strconv"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/comparison"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	// annotationDriftSignature identifies the difference last reported, so "the same difference again" can
	// be told from "a different difference". It holds the generation and the field path, never the VALUES:
	// a value belongs in the log, not in an annotation that is readable by anyone who can get the resource
	// and that would grow without bound.
	annotationDriftSignature = "krateo.io/drift-signature"
	// annotationDriftRepeats counts consecutive observes that reported that same signature.
	annotationDriftRepeats = "krateo.io/drift-repeats"
	// maxDriftRepeats is how many identical reports are allowed before the update stops being issued.
	//
	// Matches the dispatch observe-miss budget, and for the same reason: it is a deadline for concluding
	// "this is not going to work", not a retry budget. At the default 3m poll it is 15 minutes, which is
	// long enough for an eventually-consistent API to settle and short enough to bound the damage of a
	// write loop against an API that charges for the resource being written.
	maxDriftRepeats = 5
)

// driftSignature identifies a reported difference for the purpose of "is this the same one again".
//
// Generation is part of it so that editing the spec always re-enables the update: a human who changes the
// CR in response to the condition must get a fresh attempt, and generation is exactly the apiserver's
// record of "the spec changed". The field path is the rest of it. The values are deliberately excluded --
// including them would defeat the counter for any field whose remote value moves on its own (a timestamp,
// a counter), which is precisely a case where the update is not converging and must be stopped.
func driftSignature(mg *unstructured.Unstructured, res comparison.ComparisonResult) string {
	if mg == nil {
		return ""
	}
	field := res.Reason.FieldPath()
	if field == "" && res.Reason != nil {
		// No attributable field (a whole-document mismatch). The reason text still distinguishes one
		// cause from another, which is all the counter needs.
		field = res.Reason.Reason
	}
	return fmt.Sprintf("gen=%d;field=%s", mg.GetGeneration(), field)
}

// driftRepeats reads the consecutive-report count for sig, returning 0 when the recorded signature is a
// different one (a different difference starts its own count) or when the value is unusable. Anything
// unparsable reads as 0 rather than failing a reconcile: this is a brake, and a corrupt brake must fail
// open, not wedge the controller.
func driftRepeats(mg *unstructured.Unstructured, sig string) int {
	if mg == nil || sig == "" {
		return 0
	}
	ann := mg.GetAnnotations()
	if ann[annotationDriftSignature] != sig {
		return 0
	}
	n, err := strconv.Atoi(ann[annotationDriftRepeats])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// recordDriftRepeat persists sig and the incremented count, returning the new count.
func (h *handler) recordDriftRepeat(ctx context.Context, mg *unstructured.Unstructured, sig string) (int, error) {
	n := driftRepeats(mg, sig) + 1
	if err := h.patchAnnotations(ctx, mg, map[string]interface{}{
		annotationDriftSignature: sig,
		annotationDriftRepeats:   strconv.Itoa(n),
	}); err != nil {
		return 0, err
	}
	return n, nil
}

// clearDriftRepeats removes the bookkeeping once the comparison agrees, so a resource that converges after
// a transient difference starts from zero the next time one appears. A no-op when there is nothing
// recorded, which is the common case and must not cost a write per reconcile.
func (h *handler) clearDriftRepeats(ctx context.Context, mg *unstructured.Unstructured) error {
	if mg == nil {
		return nil
	}
	ann := mg.GetAnnotations()
	if ann[annotationDriftSignature] == "" && ann[annotationDriftRepeats] == "" {
		return nil
	}
	return h.patchAnnotations(ctx, mg, map[string]interface{}{
		annotationDriftSignature: nil,
		annotationDriftRepeats:   nil,
	})
}
