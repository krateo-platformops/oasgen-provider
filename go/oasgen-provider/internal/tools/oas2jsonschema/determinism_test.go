package oas2jsonschema

import (
	"fmt"
	"reflect"
	"testing"
)

// #397. A generated CRD has to be byte-reproducible, and it was not: four sites built emitted output by
// ranging a map, and Go randomises map iteration.
//
// WHY N AND NOT 2. The original determinism check was two renders, byte-identical, read as proof the
// harness was not a variable. It was a FALSE PASS: with a random order over two values, agreeing is the
// likely outcome. The real measurement took six renders and split 1-in-5. So every case here runs the
// operation many times and fails if ANY run differs from the first -- and the count is high enough that a
// single flaking site is caught with margin rather than by luck.
//
// The probability a 2-element random order agrees across N runs is 2^-(N-1): at N=2 that is 1 in 2, at
// N=64 it is 1 in 2^63. The cost is microseconds, so there is no reason to economise here.
const determinismRuns = 64

// first-seen dedup over two lists, which is the required/enum case from the allOf merge.
func TestDedupeStringsFirstSeenIsDeterministicAndOrdered(t *testing.T) {
	a := []string{"id", "name", "id"}
	b := []string{"name", "tenant", "id", "zone"}
	want := []string{"id", "name", "tenant", "zone"} // first appearance across a then b

	first := dedupeStringsFirstSeen(a, b)
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("order = %v, want %v (first appearance across the inputs, not sorted and not random)", first, want)
	}
	for i := 0; i < determinismRuns; i++ {
		got := dedupeStringsFirstSeen(a, b)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs: %v vs %v. The dedup is order-dependent on map iteration again, which "+
				"makes a generated CRD non-reproducible and every consumer's version diff unreliable", i, got, first)
		}
	}
}

func TestDedupeValuesFirstSeenIsDeterministicAndOrdered(t *testing.T) {
	// The exact shape that was measured flaking: an enum merged from two allOf members.
	a := []interface{}{"Basic", "Advanced", "Basic"}
	b := []interface{}{"Advanced", "Premium"}
	want := []interface{}{"Basic", "Advanced", "Premium"}

	first := dedupeValuesFirstSeen(a, b)
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("order = %v, want %v. sameEnum compares enums as ORDERED lists because that is how a "+
			"document writes them, so first-seen order is the meaning-preserving answer and sorting is not",
			first, want)
	}
	for i := 0; i < determinismRuns; i++ {
		if got := dedupeValuesFirstSeen(a, b); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs: %v vs %v", i, got, first)
		}
	}
}

// THE CONTROL, and it is the case that makes the two above mean something.
//
// A test that only checks the new helpers would pass against an implementation that happened to be
// deterministic for small inputs. This reproduces the OLD implementation -- range the dedup map -- and
// asserts it IS observably non-deterministic at this input size. If this case ever stops failing to vary,
// the map-iteration randomisation Go guarantees has changed, and the two cases above are no longer
// testing anything.
func TestTheOldMapRangeApproachIsObservablyNonDeterministic(t *testing.T) {
	values := []string{"Basic", "Advanced", "Premium", "Enterprise", "Trial", "Legacy"}

	oldWay := func() []string {
		set := make(map[string]struct{}, len(values))
		for _, v := range values {
			set[v] = struct{}{}
		}
		out := make([]string, 0, len(set))
		for v := range set { // the defect
			out = append(out, v)
		}
		return out
	}

	first := oldWay()
	varied := false
	for i := 0; i < determinismRuns; i++ {
		if !reflect.DeepEqual(oldWay(), first) {
			varied = true
			break
		}
	}
	if !varied {
		t.Errorf("CONTROL FAILED: ranging a %d-entry map produced the same order in %d runs. Either Go has "+
			"stopped randomising map iteration or this input is too small to show it -- either way the "+
			"determinism cases above are no longer proving anything and this control needs rethinking",
			len(values), determinismRuns)
	}
}

// Dedup must still DEDUPE, or first-seen order would be trivially stable over a list that never
// collapses -- and the cases above would pass without the deduplication they exist alongside.
func TestFirstSeenStillDeduplicates(t *testing.T) {
	got := dedupeStringsFirstSeen([]string{"a", "a", "b"}, []string{"b", "a", "c"})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	gotV := dedupeValuesFirstSeen([]interface{}{1, 1, 2}, []interface{}{2, 3})
	wantV := []interface{}{1, 2, 3}
	if !reflect.DeepEqual(gotV, wantV) {
		t.Errorf("got %v, want %v", gotV, wantV)
	}
	// Empty and nil inputs must not produce a non-nil empty slice where nil was returned before, because
	// a CRD renders `required: []` differently from an absent `required`.
	if got := dedupeStringsFirstSeen(nil, nil); got != nil {
		t.Errorf("nil inputs must yield nil, not %#v: an empty `required: []` in a CRD is not the same as "+
			"an absent one, and emitting the wrong one is a schema change", got)
	}
	if got := dedupeValuesFirstSeen(); got != nil {
		t.Errorf("no inputs must yield nil, not %#v", got)
	}
	_ = fmt.Sprint()
}
