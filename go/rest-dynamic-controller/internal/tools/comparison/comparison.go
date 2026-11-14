package comparison

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/google/go-cmp/cmp"
)

type Reason struct {
	Reason      string
	FirstValue  any
	SecondValue any
	// Path is the field path the difference was found at, outermost segment first, with a slice element's
	// index as its own segment ("nodePools", "0", "instance").
	//
	// Without it a reported difference does not say WHICH field differs, which is most of what a reader
	// needs: a nested difference used to be reported with the enclosing container's values, so a
	// one-leaf mismatch inside an array of objects surfaced as two whole arrays and no field name.
	Path []string
}

// FieldPath renders Path in the dotted form a RestDefinition author writes, e.g. "nodePools[0].instance".
// Empty when the difference is not attributable to a field (a nil receiver, or a failure before any key
// was reached).
func (r *Reason) FieldPath() string {
	if r == nil || len(r.Path) == 0 {
		return ""
	}
	var b strings.Builder
	for _, seg := range r.Path {
		if _, err := strconv.Atoi(seg); err == nil {
			b.WriteString("[" + seg + "]")
			continue
		}
		if b.Len() > 0 {
			b.WriteString(".")
		}
		b.WriteString(seg)
	}
	return b.String()
}

type ComparisonResult struct {
	IsEqual bool
	Reason  *Reason
}

func (r ComparisonResult) String() string {
	if r.IsEqual {
		return "ComparisonResult: IsEqual=true"
	}
	if r.Reason == nil {
		return "ComparisonResult: IsEqual=false, Reason=nil"
	}
	if fp := r.Reason.FieldPath(); fp != "" {
		return fmt.Sprintf("ComparisonResult: IsEqual=false, Field=%s, Reason=%s, FirstValue=%v, SecondValue=%v", fp, r.Reason.Reason, r.Reason.FirstValue, r.Reason.SecondValue)
	}
	return fmt.Sprintf("ComparisonResult: IsEqual=false, Reason=%s, FirstValue=%v, SecondValue=%v", r.Reason.Reason, r.Reason.FirstValue, r.Reason.SecondValue)
}

// CompareExisting recursively compares fields between two maps and logs differences.
// If a field exists in the first map but not in the second, it is ignored.
// If a field exists in the second map but not in the first, it is ignored.
// If both maps have the same field, it compares their values.
// Slices order is considered, so if the order of elements in slices is different, they are considered unequal.
// If the values are maps or slices, it recursively compares them.
// childPath returns path + seg in a FRESH slice. `append(path, seg)` must not be used here: append may
// write into the caller's backing array, so a Path stored in a returned Reason could be rewritten by a
// later iteration of the loop that produced it, and two sibling recursions could share one array.
func childPath(path []string, seg string) []string {
	out := make([]string, len(path)+1)
	copy(out, path)
	out[len(path)] = seg
	return out
}

// Comparer compares a CR's spec against a remote body.
//
// ITS ZERO VALUE IS THE HISTORICAL BEHAVIOUR: every array compared element-by-element and in order. The
// only thing it can carry is a set of paths whose arrays are declared sets in the OAS document, and those
// are compared as multisets instead. Nothing else about the comparison changes, and a caller that does
// not supply any paths gets exactly what this package did before.
//
// WHY THIS TYPE EXISTS AT ALL. The schema is not available where the decision has to be made:
// `compareSlices` receives two `[]interface{}` and a path, and its entry point receives two maps. So
// "compare this array as a set" cannot be a branch inside the comparison -- it is a change to what the
// comparison is given, and the recursion has to carry it. `CompareExisting` ends in a variadic `path`,
// so nothing can be appended to its signature; a receiver is the one place left.
//
// Keycloak stores a realm's `enabledEventTypes` as a Java Set and returns it in hash order, so a
// positional comparison of 23 unchanged names reported drift on every resync and the controller re-PUT
// the realm every three minutes, forever (#617). With two elements the order happened to match, which is
// why it took a 23-element realm to find.
type Comparer struct {
	// setPaths holds dot-notation paths, with array indices removed, of arrays whose schema declares
	// `uniqueItems: true` with scalar items.
	setPaths map[string]struct{}
	// strippedPaths holds dot-notation paths, with array indices removed, that the REQUEST never
	// carries: the CR-side source of a resolver, which stripResolverSources removes from the body
	// before the call (#663). The vendor therefore cannot hold them, so comparing them is never
	// meaningful.
	strippedPaths map[string]struct{}
	// mapListKeys holds, for the same kind of path, the key names an array's OBJECT elements are
	// identified by -- from `x-kubernetes-list-type: map` plus `x-kubernetes-list-map-keys` (#650).
	//
	// A SEPARATE FIELD RATHER THAN A WIDER setPaths, because the two arms answer different questions
	// and a path can only be one of them: a set is matched BY VALUE and needs scalar elements, a
	// map-list is matched BY KEY and needs object elements. Merging them would make "which arm" a
	// property of the data rather than of the declaration, which is the guess this feature exists to
	// remove.
	mapListKeys map[string][]string
}

// NewComparer returns a Comparer that treats the given dot-notation paths as sets.
//
// The paths come from the OAS document via UnstructuredClient.SetTypedArrayPaths, which only emits
// arrays with SCALAR items. An empty or nil list yields a Comparer identical to the zero value.
func NewComparer(setTypedPaths []string) *Comparer {
	c := &Comparer{}
	if len(setTypedPaths) == 0 {
		return c
	}
	c.setPaths = make(map[string]struct{}, len(setTypedPaths))
	for _, p := range setTypedPaths {
		if p == "" {
			continue
		}
		c.setPaths[p] = struct{}{}
	}
	return c
}

// WithMapListKeys declares arrays whose object elements are identified by key, keyed by the same
// dot-notation path as setPaths. The map comes from UnstructuredClient.ListMapKeyPaths.
//
// A MODIFIER RATHER THAN A SECOND CONSTRUCTOR. NewComparer has existing callers and tests that must
// stay unaffected, and a NewComparerWithListSemantics beside it would be two ways to build one thing
// -- the shape where a future edit keeps the decorative one. Returns the receiver so the single call
// site reads as one expression.
func (c *Comparer) WithMapListKeys(keys map[string][]string) *Comparer {
	if len(keys) == 0 {
		return c
	}
	c.mapListKeys = make(map[string][]string, len(keys))
	for path, names := range keys {
		if path == "" || len(names) == 0 {
			continue
		}
		c.mapListKeys[path] = names
	}
	if len(c.mapListKeys) == 0 {
		c.mapListKeys = nil
	}
	return c
}

// WithStrippedPaths declares the CR-side paths the request never carries -- a resolver's
// inCustomResource, which stripResolverSources removes from the body (#663).
//
// NOT PART OF ListSemantics, deliberately. Those describe what a DOCUMENT says about the shape of its
// arrays and are read from the OAS; this is a fact about what this controller DOES to the request
// body, derived from the RestDefinition's own fieldMapping with no document read at all. Merging them
// would put two unrelated derivations behind one name.
func (c *Comparer) WithStrippedPaths(paths []string) *Comparer {
	if len(paths) == 0 {
		return c
	}
	c.strippedPaths = make(map[string]struct{}, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		c.strippedPaths[p] = struct{}{}
	}
	if len(c.strippedPaths) == 0 {
		c.strippedPaths = nil
	}
	return c
}

// isStrippedPath reports whether this path is a resolver source the request never carried.
func (c *Comparer) isStrippedPath(path []string) bool {
	if len(c.strippedPaths) == 0 || len(path) == 0 {
		return false
	}
	joined, ok := normalisePath(path)
	if !ok {
		return false
	}
	_, found := c.strippedPaths[joined]
	return found
}

// comparableKeyCount returns how many of mg's keys at this path the request could actually have
// carried -- that is, all of them except resolver sources.
func (c *Comparer) comparableKeyCount(mg map[string]interface{}, path []string) int {
	if len(c.strippedPaths) == 0 {
		return len(mg)
	}
	n := 0
	for key := range mg {
		if c.isStrippedPath(childPath(path, key)) {
			continue
		}
		n++
	}
	return n
}

// normalisePath strips array index segments and joins the rest, so a declaration-derived path and a
// runtime path meet. Shared by both arms so they cannot disagree about what a path is.
func normalisePath(path []string) (string, bool) {
	if len(path) == 0 {
		return "", false
	}
	kept := make([]string, 0, len(path))
	for _, seg := range path {
		if _, err := strconv.Atoi(seg); err == nil {
			continue
		}
		kept = append(kept, seg)
	}
	if len(kept) == 0 {
		return "", false
	}
	return strings.Join(kept, "."), true
}

// keyedListKeys returns the declared key names for this path, or nil.
func (c *Comparer) keyedListKeys(path []string) []string {
	if len(c.mapListKeys) == 0 {
		return nil
	}
	joined, ok := normalisePath(path)
	if !ok {
		return nil
	}
	return c.mapListKeys[joined]
}

// ListSemantics is everything a document declares about how its arrays should be compared, carried as
// one value because both halves are derived from the SAME operation's request body and must not be
// derived separately.
//
// The previous shape passed only the set paths, as a bare []string parameter. Adding the map-list
// keys beside it would have made a sixth parameter on isCRUpdated and, worse, allowed the two to be
// derived from different operations or different clients without anything noticing.
type ListSemantics struct {
	// SetPaths: arrays declared `uniqueItems: true` with scalar items, compared as multisets (#617).
	SetPaths []string
	// MapListKeys: arrays declared `x-kubernetes-list-type: map`, keyed by the named properties and
	// matched by key rather than by position (#650).
	MapListKeys map[string][]string
}

// Comparer builds the comparer these semantics describe. The zero value yields one identical to the
// historical behaviour, so a caller that derives nothing is unaffected.
func (ls ListSemantics) Comparer() *Comparer {
	return NewComparer(ls.SetPaths).WithMapListKeys(ls.MapListKeys)
}

// isSetPath reports whether the array at this path is a declared set.
//
// ARRAY INDICES ARE STRIPPED FIRST, and that is load-bearing rather than tidy. The walk appends the
// element index as its own path segment, so a set nested inside an array of objects arrives here as
// `rules.0.types` while the schema-derived path is `rules.types`. Looking up the unstripped path matches
// nothing, and the failure is silent: every array falls back to positional, which is exactly the
// behaviour this change exists to remove, with no error anywhere to say so.
func (c *Comparer) isSetPath(path []string) bool {
	if len(c.setPaths) == 0 || len(path) == 0 {
		return false
	}
	joined, ok := normalisePath(path)
	if !ok {
		return false
	}
	_, found := c.setPaths[joined]
	return found
}

// CompareExisting compares a CR's spec against a remote body, positionally. It is the historical entry
// point and is kept so that every existing caller and test is unaffected by #617: the set-aware
// behaviour is reachable only through NewComparer.
func CompareExisting(mg map[string]interface{}, rm map[string]interface{}, path ...string) (ComparisonResult, error) {
	return (&Comparer{}).CompareExisting(mg, rm, path...)
}

func (c *Comparer) CompareExisting(mg map[string]interface{}, rm map[string]interface{}, path ...string) (ComparisonResult, error) {
	// A populated CR object against an EMPTY remote object is a difference, not an absence.
	//
	// Without this, the loop below looks up every one of mg's keys in an empty rm, finds none of them,
	// skips them all by the absent-key rule, and returns equal. Measured against a real Ironic: the node
	// answered `"instance_info": {}` while the CR carried instance_info.image_source and image_checksum,
	// so no drift was reported, no PATCH was issued, and the resource went Synced=True having NEVER had
	// its desired state applied. It surfaced two layers away as a deploy rejected for
	// `Missing are: ['instance_info.image_source']` -- an application error that reads like a chart bug.
	//
	// This is the map-shaped twin of the slice defect fixed in #76, and compareSlices has compared lengths
	// ever since for the same reason: present-and-empty is the user declaring the thing empty, not
	// declining to have an opinion. The map branch never got the same treatment.
	//
	// AT THE FUNCTION ENTRY, deliberately, not before each recursive call. There are three call sites --
	// the external entry from the observe comparison, the nested-map branch, and the map-element-inside-a-
	// slice branch -- and a guard repeated at each one would reproduce the exact asymmetry that caused
	// this bug: a rule applied in one branch and forgotten in another. Here a fourth caller is safe by
	// construction.
	//
	// ONE-DIRECTIONAL. Empty-vs-empty is equal, and an EMPTY CR object against a populated remote stays
	// equal: a map key may legitimately be absent upstream, so CR-side `{}` is not the assertion that
	// CR-side `[]` is for a list.
	//
	// It does not disturb the absent-key rule below, which #309's remedy depends on: a responseTransform
	// that DELETES a masked field makes the key absent, not empty, and absent is still skipped. The two
	// compose -- deliberately unobservable stays silent, observably-empty now speaks.
	// A RESOLVER SOURCE IS NOT CONTENT THE VENDOR COULD HAVE HELD (#663), so it does not count here.
	//
	// stripResolverSources removes a resolver's inCustomResource from the request body, leaving the
	// containing object present-but-EMPTY. The vendor stores `{}` because that is what it was sent, and
	// the check below then correctly observed a CR-populated object against a remote-empty one. Two
	// individually-correct mechanisms: my strip created the asymmetry my comparison flagged. Measured
	// on a live Ironic node -- `extra` holding only `bmc_secret` burned five real PATCHes at 180s
	// intervals and latched at DriftNotConverging, while the SAME definition converged on a node whose
	// `extra` had other keys, so the fleet divided into permanently-broken and healthy as a function of
	// when the bridge script had run.
	//
	// SO ONLY THE KEYS THE REQUEST COULD HAVE CARRIED ARE COUNTED. That keeps this check at full
	// strength for the case it exists for -- which is the OTHER real Ironic measurement in the comment
	// above: `instance_info: {}` against a CR carrying image_source and image_checksum. Those are
	// ordinary API fields, the request DID carry them, no key is discounted, and it stays drift. The two
	// cases are the same shape with opposite correct answers, discriminated by exactly one thing:
	// whether the request ever sent the key.
	if c.comparableKeyCount(mg, path) > 0 && len(rm) == 0 {
		return ComparisonResult{
			IsEqual: false,
			Reason: &Reason{
				Reason:      "object is set in the custom resource but empty on the remote",
				FirstValue:  mg,
				SecondValue: rm,
				Path:        path,
			},
		}, nil
	}

	// Iterate over keys in the first map (mg, representing the CR on the cluster) in SORTED order.
	//
	// The order is load-bearing, not tidiness. This function returns on the FIRST difference it finds, so
	// the key order decides WHICH difference gets reported when more than one exists -- and Go randomizes
	// map iteration. Two sibling fields drifting (Aruba Kaas drifts on nodePools[0].instance AND
	// nodePools[0].dataCenter, both string-in-CR / object-in-response) therefore reported a different
	// field path on each observe, measured at a 173/27 split over 200 comparisons.
	//
	// That broke the non-convergence budget outright: driftSignature keys on the reported field path, and
	// driftRepeats resets whenever the signature changes, so a budget meant to halt after a fixed number
	// of futile updates instead halted when the coin landed the same way five times running -- luck, not
	// a bound, and worse the more fields drift at once. It also meant the Ready condition named a
	// different field to whoever was reading it each poll.
	//
	// Sorting makes the reported difference a deterministic function of the two documents. It does not
	// change WHAT counts as a difference, only which one is reported first.
	keys := make([]string, 0, len(mg))
	for key := range mg {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		value := mg[key]
		currentPath := childPath(path, key)
		pathStr := fmt.Sprintf("%v", currentPath)

		// A RESOLVER SOURCE IS SKIPPED OUTRIGHT (#663), before the absent-key rule rather than relying
		// on it. The absent-key rule already covers the usual case -- the vendor cannot return a key the
		// request never sent -- but a vendor that echoes unknown fields back WOULD return it, and
		// comparing the CR's `{name, key}` pointer against whatever it echoed is meaningless either way.
		// Skipping on the declaration rather than on the response makes the outcome independent of how
		// chatty the vendor is.
		if c.isStrippedPath(currentPath) {
			continue
		}

		rmValue, ok := rm[key]
		if !ok {
			// Key does not exist in rm, ignore and continue
			// TODO: to be understood if this is the desired behavior
			// Examples:
			// Key [configurationRef] not found in rm, ignoring and continuing (this is desired, but maybe can be whitelisted)
			continue
		}

		// Handle case where one or both values are nil
		if value == nil || rmValue == nil {
			if value == nil && rmValue == nil {
				continue // Both are nil, considered equal
			}
			// One is nil but the other isn't, so they are not equal.
			return ComparisonResult{
				IsEqual: false,
				Reason: &Reason{
					Reason:      "values differ (one is nil)",
					FirstValue:  value,
					SecondValue: rmValue,
					Path:        currentPath,
				},
			}, nil
		}

		switch reflect.TypeOf(value).Kind() {
		case reflect.Map:
			mgMap, ok1 := value.(map[string]interface{})
			if !ok1 {
				return ComparisonResult{
					IsEqual: false,
					Reason: &Reason{
						Reason:      "type assertion failed",
						FirstValue:  value,
						SecondValue: rmValue,
						Path:        currentPath,
					},
				}, fmt.Errorf("type assertion failed for map at %s", pathStr)
			}
			rmMap, ok2 := rmValue.(map[string]interface{})
			if !ok2 {
				// The CR holds an object and the response holds something else at the same key. This is
				// the scalar-vs-object shape divergence a vendor can ship in its own spec; it is reported
				// against the field so the author can reshape it with responseTransform.
				return ComparisonResult{
					IsEqual: false,
					Reason: &Reason{
						Reason:      "type assertion failed",
						FirstValue:  value,
						SecondValue: rmValue,
						Path:        currentPath,
					},
				}, fmt.Errorf("type assertion failed for map at %s", pathStr)
			}
			res, err := c.CompareExisting(mgMap, rmMap, currentPath...)
			if err != nil {
				return ComparisonResult{
					IsEqual: false,
					Reason: &Reason{
						Reason:      "error comparing maps",
						FirstValue:  value,
						SecondValue: rmValue,
						Path:        currentPath,
					},
				}, err
			}
			if !res.IsEqual {
				// The INNER result, not a fresh one built from the enclosing maps. Replacing it here was
				// what made a nested difference unreadable: the reason became "values differ" with both
				// whole containers as the values, discarding the leaf's name and its two scalars.
				return res, nil
			}
		case reflect.Slice:
			valueSlice, ok1 := value.([]interface{})
			if !ok1 || reflect.TypeOf(rmValue).Kind() != reflect.Slice {
				return ComparisonResult{
					IsEqual: false,
					Reason: &Reason{
						Reason:      "values are not both slices or type assertion failed",
						FirstValue:  value,
						SecondValue: rmValue,
						Path:        currentPath,
					},
				}, fmt.Errorf("values are not both slices or type assertion failed at %s", pathStr)
			}
			rmSlice, ok2 := rmValue.([]interface{})
			if !ok2 {
				return ComparisonResult{
					IsEqual: false,
					Reason: &Reason{
						Reason:      "values are not both slices or type assertion failed",
						FirstValue:  value,
						SecondValue: rmValue,
						Path:        currentPath,
					},
				}, fmt.Errorf("type assertion failed for slice at %s", pathStr)
			}
			res, err := c.compareSlices(valueSlice, rmSlice, currentPath)
			if err != nil {
				return res, err
			}
			if !res.IsEqual {
				return res, nil
			}
		default:
			// Here we compare primary types (string, bool, numbers)
			ok := CompareAny(value, rmValue)
			if !ok {
				return ComparisonResult{
					IsEqual: false,
					Reason: &Reason{
						Reason:      "values differ",
						FirstValue:  value,
						SecondValue: rmValue,
						Path:        currentPath,
					},
				}, nil
			}
		}
	}

	return ComparisonResult{IsEqual: true}, nil
}

// compareSlices compares two []interface{} element-by-element and in order. Nested maps recurse into
// CompareExisting and nested slices recurse into compareSlices; primitive elements are compared with the
// same type-normalizing CompareAny used for scalar fields.
//
// Unlike CompareExisting, which deliberately ignores map keys ABSENT from the CR (configurationRef and
// other cluster-only fields must not count as drift), a slice is compared by LENGTH as well as content.
// A key present in the CR holding an explicit [] is not an absent key: it is the user declaring the list
// empty. Treating present-and-empty as "no opinion" removed the only way to express that state, and did
// so while reporting success.
//
// This closes two latent defects in the previous inline implementation:
//   - primitive elements were compared with a raw `v != rmSlice[i]`, so an int64 CR value and the same
//     number decoded as float64 from an API response (or a "true" string vs a bool) drifted forever
//     inside arrays even though scalar fields already normalized them;
//   - a nested slice element panicked, because `!=` on []interface{} is a runtime panic, and a nil
//     element panicked on reflect.TypeOf(nil).Kind().
func (c *Comparer) compareSlices(valueSlice, rmSlice []interface{}, path []string) (ComparisonResult, error) {
	pathStr := fmt.Sprintf("%v", path)

	// Lengths must match. The previous rule only rejected a CR slice LONGER than the remote one and
	// ignored a longer remote as "extra elements", which made an empty CR slice compare equal to any
	// remote slice at all: 0 > n is false, the range body never runs, and the function returns equal.
	// Emptying a list could therefore never be enforced, and because the walk is positional, neither
	// could reordering against a shorter CR slice -- both reported Ready=True while diverged (#76).
	if len(valueSlice) != len(rmSlice) {
		return ComparisonResult{
			IsEqual: false,
			Reason:  &Reason{Reason: "slice lengths differ", FirstValue: valueSlice, SecondValue: rmSlice, Path: path},
		}, nil
	}

	// A DECLARED SET IS COMPARED AS A MULTISET, not in order.
	//
	// Only when the schema says so, and only for scalar elements. A multiset of objects needs a key to
	// match elements by, which is a different question that this repo already answers elsewhere with
	// identifiersMatchPolicy; inventing a second answer here would be worse than the ordering bug. If the
	// data disagrees with the schema -- objects where scalars were declared -- this falls back to the
	// positional walk rather than guessing.
	if c.isSetPath(path) && allScalar(valueSlice) && allScalar(rmSlice) {
		return compareAsMultiset(valueSlice, rmSlice, path), nil
	}

	// A DECLARED MAP-LIST IS MATCHED BY KEY, not by position (#650).
	//
	// The twin of the arm above, for the case its comment used to send readers elsewhere for: an array
	// of OBJECTS that a vendor stores as a set and returns in an arbitrary order. Position then means
	// nothing, and a positional walk reports a difference no update can retire -- the element it
	// rewrites comes back in a different slot next time.
	//
	// Only when the schema says so, via `x-kubernetes-list-type: map` plus
	// `x-kubernetes-list-map-keys`, and only when BOTH sides actually hold objects. If the data
	// disagrees with the declaration this falls through to the positional walk rather than guessing,
	// exactly as the set arm does for objects-where-scalars-were-declared.
	if keys := c.keyedListKeys(path); len(keys) > 0 && allObjects(valueSlice) && allObjects(rmSlice) {
		return c.compareAsKeyedList(valueSlice, rmSlice, keys, path)
	}

	for i, v := range valueSlice {
		rmv := rmSlice[i]
		// The element's index is its own path segment, so a difference inside an array of objects is
		// reported as nodePools[0].instance rather than as nodePools.
		elemPath := childPath(path, strconv.Itoa(i))

		// Nil handling: one nil and one non-nil differ; two nils are equal. Guards the
		// reflect.TypeOf(nil).Kind() panic below.
		if v == nil || rmv == nil {
			if v == nil && rmv == nil {
				continue
			}
			return ComparisonResult{
				IsEqual: false,
				Reason:  &Reason{Reason: "values differ (one is nil)", FirstValue: v, SecondValue: rmv, Path: elemPath},
			}, nil
		}

		switch reflect.TypeOf(v).Kind() {
		case reflect.Map:
			mgMap, okMg := v.(map[string]interface{})
			if !okMg {
				return ComparisonResult{
					IsEqual: false,
					Reason:  &Reason{Reason: "type assertion failed", FirstValue: v, SecondValue: rmv, Path: elemPath},
				}, fmt.Errorf("type assertion failed for map at %s", pathStr)
			}
			rmMap, okRm := rmv.(map[string]interface{})
			if !okRm {
				return ComparisonResult{
					IsEqual: false,
					Reason:  &Reason{Reason: "type assertion failed", FirstValue: v, SecondValue: rmv, Path: elemPath},
				}, fmt.Errorf("type assertion failed for map at %s", pathStr)
			}
			// elemPath, not path: without the index every element of the array reported the same
			// location, so two different elements differing produced the same message.
			res, err := c.CompareExisting(mgMap, rmMap, elemPath...)
			if err != nil {
				return ComparisonResult{
					IsEqual: false,
					Reason:  &Reason{Reason: "error comparing maps", FirstValue: v, SecondValue: rmv, Path: elemPath},
				}, err
			}
			if !res.IsEqual {
				return res, nil
			}
		case reflect.Slice:
			nestedV, okv := v.([]interface{})
			if !okv {
				return ComparisonResult{
					IsEqual: false,
					Reason:  &Reason{Reason: "type assertion failed", FirstValue: v, SecondValue: rmv, Path: elemPath},
				}, fmt.Errorf("type assertion failed for nested slice at %s", pathStr)
			}
			nestedRM, okr := rmv.([]interface{})
			if !okr {
				return ComparisonResult{
					IsEqual: false,
					Reason:  &Reason{Reason: "values are not both slices or type assertion failed", FirstValue: v, SecondValue: rmv, Path: elemPath},
				}, fmt.Errorf("type assertion failed for nested slice at %s", pathStr)
			}
			res, err := c.compareSlices(nestedV, nestedRM, elemPath)
			if err != nil {
				return res, err
			}
			if !res.IsEqual {
				return res, nil
			}
		default:
			if !CompareAny(v, rmv) {
				return ComparisonResult{
					IsEqual: false,
					Reason:  &Reason{Reason: "values differ", FirstValue: v, SecondValue: rmv, Path: elemPath},
				}, nil
			}
		}
	}

	return ComparisonResult{IsEqual: true}, nil
}

func CompareAny(a any, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	strA := fmt.Sprintf("%v", a)
	strB := fmt.Sprintf("%v", b)

	a = InferType(strA)
	b = InferType(strB)

	//log.Printf("Values to compare: '%v' and '%v'\n", a, b)
	//diff := cmp.Diff(a, b)
	//log.Printf("cmp diff:\n%s", diff)

	return cmp.Equal(a, b)
}

// DeepEqual performs a deep comparison between two values.
// This function is currently used in FindBy identifier comparisons (see isInResource in clienttools.go).
// It is suitable for comparing also complex structures like maps and slices.
// For maps (objects), key order does not matter.
// For slices (arrays), element order and content are strictly compared.
// Map and slice comparisons normalize nil values before comparison to avoid discrepancies due to nil entries.
func DeepEqual(a, b interface{}) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	aKind := reflect.TypeOf(a).Kind()
	bKind := reflect.TypeOf(b).Kind()
	// For complex types, a direct recursive comparison is correct and respects
	// the nuances of map and slice comparison.
	if aKind == reflect.Map || aKind == reflect.Slice || bKind == reflect.Map || bKind == reflect.Slice {
		//log.Printf("Using direct comparison for complex types: '%v' and '%v'\n", a, b)
		//diff := cmp.Diff(a, b)
		//log.Printf("cmp diff before normalization:\n%s", diff)
		// TODO: evaluate this to be configurable if needed
		normA := normalizeAny(a)
		normB := normalizeAny(b)
		//log.Printf("Normalized values for complex types: '%v' and '%v'\n", normA, normB)
		//diff = cmp.Diff(normA, normB)
		//log.Printf("cmp diff after normalization:\n%s", diff)
		return cmp.Equal(normA, normB)
	}

	// For primary types (string, bool, numbers), we use a normalization
	// step to handle type discrepancies, such as idifferent numeric types for integers and floats.
	strA := fmt.Sprintf("%v", a)
	strB := fmt.Sprintf("%v", b)

	normA := InferType(strA)
	normB := InferType(strB)

	// DEBUG
	//log.Printf("Comparing normalized values: '%v' and '%v'\n", normA, normB)
	//diff := cmp.Diff(normA, normB)
	//log.Printf("cmp diff:\n%s", diff)

	return cmp.Equal(normA, normB)

}

// Note: forked from plumbing library to solve UUID case and similar cases
// InferType attempts to infer and convert a string value to its most appropriate Go type.
// It supports primitive types (bool, int32, int64, float64, string), as well as
// structured types commonly found in Kubernetes configurations (map[string]any and []any).
// The function first tries to parse the input as JSON. If that fails, it falls back to
// custom parsing logic for booleans, nil/null, integers, and floats.
// If no conversion is possible, the original string is returned.
func InferType(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}

	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()

	var jsonVal any
	if err := decoder.Decode(&jsonVal); err == nil {
		// Check if there's more data after what was decoded
		// This ensures we only accept values where the entire string is valid JSON
		if decoder.More() {
			// There's more data, so this isn't a complete JSON value
			// E.g., UUID that starts with numbers like: "90f9629b-664b-4804-a560-dd79b0c628f8"
			// Decoder will parse "90" as a number and leave the rest which is not desired
			// Instead, we want to treat the whole string as a regular string and so to avoid the partial parsing in the switch below
		} else {
			switch v := jsonVal.(type) {
			case json.Number:
				if i, err := v.Int64(); err == nil {
					if i >= math.MinInt32 && i <= math.MaxInt32 {
						return int32(i)
					}
					return i
				}
				if f, err := v.Float64(); err == nil {
					// A whole-number float in int64 range must normalize to an
					// integer. Large integers (e.g. a byte-sized disk size or
					// memory) are printed by fmt "%v" in exponential form
					// ("2.147483648e+10"), which json.Number.Int64() rejects; without
					// this an int64 CR value and the same number decoded as float64
					// from an API response would compare unequal, causing perpetual
					// spurious drift on every int64 field.
					if f == math.Trunc(f) && f >= math.MinInt64 && f <= math.MaxInt64 {
						if i := int64(f); i >= math.MinInt32 && i <= math.MaxInt32 {
							return int32(i)
						} else {
							return i
						}
					}
					return f
				}
			default:
				return jsonVal
			}
		}
	}

	if strings.EqualFold(value, "true") {
		return true
	}
	if strings.EqualFold(value, "false") {
		return false
	}

	if strings.EqualFold(value, "null") || strings.EqualFold(value, "nil") {
		return nil
	}

	if intVal, err := strconv.ParseInt(value, 10, 64); err == nil {
		if intVal >= math.MinInt32 && intVal <= math.MaxInt32 {
			return int32(intVal)
		}
		return intVal
	}

	if floatVal, err := strconv.ParseFloat(value, 64); err == nil {
		if floatVal == math.Trunc(floatVal) {
			if floatVal >= math.MinInt64 && floatVal <= math.MaxInt64 {
				return int64(floatVal)
			}
		}
		return floatVal
	}

	return value
}

// normalizeMap recursively removes nil values from maps
func normalizeMap(m map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range m {
		if v == nil {
			continue // skip nil values
		}

		switch val := v.(type) {
		case map[string]interface{}:
			normalized := normalizeMap(val)
			if len(normalized) > 0 {
				result[k] = normalized
			}
		case []interface{}:
			normalized := normalizeSlice(val)
			if len(normalized) > 0 {
				result[k] = normalized
			}
		default:
			result[k] = v
		}
	}
	return result
}

// normalizeSlice recursively removes nil values from slices
func normalizeSlice(s []interface{}) []interface{} {
	result := make([]interface{}, 0, len(s))
	for _, item := range s {
		if item == nil {
			continue
		}
		if m, ok := item.(map[string]interface{}); ok {
			normalized := normalizeMap(m)
			if len(normalized) > 0 {
				result = append(result, normalized)
			}
		} else {
			result = append(result, item)
		}
	}
	return result
}

func normalizeAny(value any) any {
	switch v := value.(type) {
	case map[string]interface{}:
		return normalizeMap(v)
	case []interface{}:
		return normalizeSlice(v)
	default:
		return value
	}
}

// allScalar reports whether every element is a scalar, so a multiset comparison can match elements by
// value alone. nil counts as a scalar: two nils are equal and a nil against a value is a difference,
// which CompareAny already expresses.
func allScalar(s []interface{}) bool {
	for _, v := range s {
		if v == nil {
			continue
		}
		switch reflect.TypeOf(v).Kind() {
		case reflect.Map, reflect.Slice, reflect.Array:
			return false
		}
	}
	return true
}

// compareAsMultiset reports whether the two slices hold the same elements with the same multiplicities,
// ignoring order. Callers have already established that the lengths match.
//
// MULTIPLICITIES, NOT MEMBERSHIP. Each right-hand element is consumed by at most one match, so
// {a, a, b} and {a, b, b} are correctly different despite having equal lengths and identical member
// sets. A containment check would call them equal and is the obvious wrong implementation here.
//
// Elements are matched with CompareAny rather than ==, because the two sides arrive from different
// decoders: a JSON number from the vendor and an int64 from the unstructured CR are the same value with
// different dynamic types, which this package already normalises everywhere else.
func compareAsMultiset(valueSlice, rmSlice []interface{}, path []string) ComparisonResult {
	used := make([]bool, len(rmSlice))
	for _, v := range valueSlice {
		found := false
		for j, rmv := range rmSlice {
			if used[j] {
				continue
			}
			if CompareAny(v, rmv) {
				used[j] = true
				found = true
				break
			}
		}
		if !found {
			return ComparisonResult{
				IsEqual: false,
				Reason: &Reason{
					Reason:      "set member missing from the remote value",
					FirstValue:  valueSlice,
					SecondValue: rmSlice,
					Path:        path,
				},
			}
		}
	}
	return ComparisonResult{IsEqual: true}
}

// allObjects reports whether every element is a JSON object, which is what key matching needs.
//
// The mirror of allScalar, and the two are deliberately exclusive: an array cannot be matched both by
// value and by key, so a path declared as one kind whose data is the other falls back to positional
// rather than taking the arm that cannot work on it.
func allObjects(s []interface{}) bool {
	if len(s) == 0 {
		return false
	}
	for _, v := range s {
		if _, ok := v.(map[string]interface{}); !ok {
			return false
		}
	}
	return true
}

// keyTupleOf builds the identity of a map-list element from its declared key names.
//
// LENGTH-PREFIXED, not joined on a separator. `{a: "x|y", b: "z"}` and `{a: "x", b: "y|z"}` are
// different elements and a `|`-joined tuple makes them the same string -- the collision is silent and
// turns two elements into one match. Writing each value's length ahead of it makes the encoding
// injective, which is the same reason the resolver digest is built that way.
//
// A MISSING OR NON-SCALAR KEY IS NOT AN EMPTY KEY. It means this element cannot be identified by the
// declared keys at all, which is a declaration problem rather than a difference, so it is reported
// with its own reason instead of being silently grouped with every other element that also lacks one.
func keyTupleOf(elem map[string]interface{}, keys []string) (string, string) {
	var b strings.Builder
	for _, k := range keys {
		v, present := elem[k]
		if !present || v == nil {
			return "", k
		}
		switch v.(type) {
		case map[string]interface{}, []interface{}:
			return "", k
		}
		part := fmt.Sprintf("%v", v)
		fmt.Fprintf(&b, "%d:%s\n", len(part), part)
	}
	return b.String(), ""
}

// describeKeys renders an element's key values for a human reading a condition message. "name=foo" is
// actionable where "element 3" is not, which is the whole point of matching by key.
func describeKeys(elem map[string]interface{}, keys []string) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := elem[k]; ok {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		} else {
			parts = append(parts, k+"=<absent>")
		}
	}
	return strings.Join(parts, ",")
}

// compareAsKeyedList matches elements by their declared key tuple and compares each matched pair.
//
// The caller has already established that the lengths are equal and that both sides hold objects, so
// an unmatched CR element implies an unmatched remote element and neither side needs a second pass.
//
// A DUPLICATE TUPLE IS REPORTED, NOT RESOLVED. The apiserver refuses duplicate keys in a map-list, so
// the CR side cannot hold them once its CRD declares the type -- but nothing constrains the vendor,
// and an older CRD does not constrain the CR either. Picking one of two elements with the same
// identity would make the result depend on map iteration order, and falling back to positional would
// silently restore the bug. Reporting it names the declaration that needs fixing, and the
// non-convergence guard bounds the cost at a handful of writes.
func (c *Comparer) compareAsKeyedList(valueSlice, rmSlice []interface{}, keys []string, path []string) (ComparisonResult, error) {
	index := make(map[string]map[string]interface{}, len(rmSlice))
	for _, raw := range rmSlice {
		elem := raw.(map[string]interface{})
		tuple, missing := keyTupleOf(elem, keys)
		if missing != "" {
			return ComparisonResult{
				IsEqual: false,
				Reason: &Reason{
					Reason: fmt.Sprintf("the remote has an element with no usable value for declared list key %q, "+
						"so x-kubernetes-list-map-keys cannot identify it", missing),
					FirstValue: valueSlice, SecondValue: elem, Path: path,
				},
			}, nil
		}
		if _, clash := index[tuple]; clash {
			return ComparisonResult{
				IsEqual: false,
				Reason: &Reason{
					Reason: fmt.Sprintf("the remote has more than one element with %s, so the declared "+
						"x-kubernetes-list-map-keys do not identify elements at this path",
						describeKeys(elem, keys)),
					FirstValue: valueSlice, SecondValue: rmSlice, Path: path,
				},
			}, nil
		}
		index[tuple] = elem
	}

	seen := make(map[string]struct{}, len(valueSlice))
	for i, raw := range valueSlice {
		elem := raw.(map[string]interface{})
		tuple, missing := keyTupleOf(elem, keys)
		if missing != "" {
			return ComparisonResult{
				IsEqual: false,
				Reason: &Reason{
					Reason: fmt.Sprintf("the custom resource has an element with no usable value for declared "+
						"list key %q, so x-kubernetes-list-map-keys cannot identify it", missing),
					FirstValue: elem, SecondValue: rmSlice, Path: path,
				},
			}, nil
		}
		if _, dup := seen[tuple]; dup {
			return ComparisonResult{
				IsEqual: false,
				Reason: &Reason{
					Reason: fmt.Sprintf("the custom resource has more than one element with %s, so the declared "+
						"x-kubernetes-list-map-keys do not identify elements at this path",
						describeKeys(elem, keys)),
					FirstValue: valueSlice, SecondValue: rmSlice, Path: path,
				},
			}, nil
		}
		seen[tuple] = struct{}{}

		counterpart, ok := index[tuple]
		if !ok {
			return ComparisonResult{
				IsEqual: false,
				Reason: &Reason{
					Reason:     fmt.Sprintf("no element with %s on the remote", describeKeys(elem, keys)),
					FirstValue: elem, SecondValue: rmSlice, Path: path,
				},
			}, nil
		}

		// THE MATCHED PAIR IS COMPARED WITH THE ORDINARY RULES, through the RECEIVER, including the
		// absent-key skip -- so a cluster-only field inside an element is no more drift here than
		// anywhere else, and a set or map-list declared INSIDE an element keeps its semantics.
		//
		// `c.CompareExisting`, not the package-level function: that one builds a zero-value Comparer,
		// which would silently drop every declaration for everything nested inside a keyed element.
		//
		// THE PATH CARRIES THE CR-SIDE INDEX, not the key values, and this is the opposite of what it
		// first looked like it should be. Keys read better in a message -- but the path is also what
		// the DECLARATION lookup normalises, so a `name=foo` segment would make a nested declared
		// array arrive as `rules.name=foo.conditions` and match no schema path at all, losing its
		// semantics for exactly the documents this feature is for. The CR-side index is schema-shaped
		// AND stable between polls, because the CR is the side that does not reorder: the remote's
		// order is arbitrary, the CR's is whatever the author wrote. So the drift signature stays put
		// and the nested lookups still resolve.
		//
		// The key values go in the REASON instead, where "name=foo" is actionable and an index is not.
		elemPath := childPath(path, strconv.Itoa(i))
		inner, ierr := c.CompareExisting(elem, counterpart, elemPath...)
		if ierr != nil {
			return ComparisonResult{IsEqual: false}, ierr
		}
		if !inner.IsEqual {
			if inner.Reason != nil {
				inner.Reason.Reason = fmt.Sprintf("%s (element matched by %s)",
					inner.Reason.Reason, describeKeys(elem, keys))
			}
			return inner, nil
		}
	}
	return ComparisonResult{IsEqual: true}, nil
}
