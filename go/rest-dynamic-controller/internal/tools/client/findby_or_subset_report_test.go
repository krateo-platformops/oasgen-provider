package restclient

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// An OR identifier policy -- the DEFAULT when identifiersMatchPolicy is omitted -- returns on the FIRST
// identifier whose value agrees, and findItemInList returns the first item for which that happens. With two
// or more identifiers declared, an adoption therefore rests on exactly ONE of them while the identifiers
// after it were never compared at all.
//
// That is not a theoretical hazard: a CR naming child-b adopted a different object, enclosure-a, because
// the two share a non-unique `chassis`, reported Synced=True against it, and then deleted it (#444).
//
// These tests cover the RUNTIME REPORT only. The default is deliberately unchanged, so every arm below
// also pins the adoption that OR already makes -- see
// TestTheReportDoesNotMoveWhichItemIsAdopted, which is the control for that.

// or444CR is the CR from the measured case: it names child-b and shares `chassis` with its enclosure.
func or444CR() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"chassis": "rack-1",
			"name":    "child-b",
			"serial":  "SN-CHILD-B",
			"uuid":    "u-child-b",
		},
	}}
}

// sameMapObject reports whether two maps are the SAME object, not merely equal ones. Maps are reference
// types, so this is a stronger statement than deep equality: it proves the matcher handed back the very
// element of the input slice it always did, rather than an equal copy or a different item.
func sameMapObject(a, b map[string]interface{}) bool {
	if a == nil || b == nil {
		return false
	}
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// ARM A -- a match on the FIRST declared identifier. Everything after it is unconsulted, which is the
// widest possible blind spot and the shape of the #444 incident.
func TestORMatchOnTheFirstIdentifierReportsTheRestAsNotConsulted(t *testing.T) {
	u := &UnstructuredClient{
		// Policy left EMPTY on purpose: this is the defaulted-to-OR path, which is the one that bites.
		IdentifierFields: []string{"chassis", "name", "serial"},
		Resource:         or444CR(),
	}

	// The WRONG object. It shares chassis with the CR and differs in both name and serial.
	wrongObject := map[string]interface{}{
		"chassis": "rack-1",
		"name":    "enclosure-a",
		"serial":  "SN-ENCLOSURE-A",
	}

	item, found, report, err := u.findItemInList([]interface{}{wrongObject})

	require.NoError(t, err)
	require.True(t, found, "unchanged: chassis agrees, so OR adopts -- the report does not prevent that")
	require.True(t, sameMapObject(wrongObject, item),
		"the adoption is exactly what it was before the report existed")

	require.NotNil(t, report, "an OR match decided on 1 of 3 identifiers must be reported")
	assert.Equal(t, "chassis", report.MatchedIdentifier,
		"the report must name the identifier the adoption actually rests on")
	assert.Equal(t, []string{"name", "serial"}, report.NotConsulted,
		"OR returned at chassis, so name and serial were never compared -- and name is the one that would "+
			"have rejected this object")
	assert.Equal(t, 3, report.Declared())
	assert.Empty(t, report.AbsentFromItem)
	assert.Empty(t, report.PresentButDiffering)
	assert.Empty(t, report.Unparseable)
}

// ARM B -- a match on a LATER identifier. The identifiers BEFORE it were consulted and did not agree, so
// claiming they went unconsulted would be a false report. This arm is what separates "not consulted" from
// "did not match"; a report that merely listed every non-matching identifier would pass arm A and fail here.
func TestORMatchOnALaterIdentifierDoesNotClaimTheEarlierOnesWereUnconsulted(t *testing.T) {
	u := &UnstructuredClient{
		IdentifiersMatchPolicy: "OR", // explicit, and case-insensitively the same path
		IdentifierFields:       []string{"uuid", "serial", "chassis", "name"},
		Resource:               or444CR(),
	}

	wrongObject := map[string]interface{}{
		// no uuid at all -- consulted, absent from the item
		"serial":  "SN-ENCLOSURE-A", // consulted, present, disagrees with the CR
		"chassis": "rack-1",         // agrees -> this is where OR returns
		"name":    "enclosure-a",    // never reached
	}

	item, found, report, err := u.findItemInList([]interface{}{wrongObject})

	require.NoError(t, err)
	require.True(t, found)
	require.True(t, sameMapObject(wrongObject, item))

	require.NotNil(t, report)
	assert.Equal(t, "chassis", report.MatchedIdentifier)

	// THE LOAD-BEARING ASSERTION: only what came after the match is unconsulted.
	assert.Equal(t, []string{"name"}, report.NotConsulted,
		"only `name` follows the matching identifier")
	assert.NotContains(t, report.NotConsulted, "uuid",
		"uuid WAS consulted -- it was looked for on the item and not found. Reporting it as unconsulted "+
			"would misdescribe the blind spot an operator has to act on")
	assert.NotContains(t, report.NotConsulted, "serial",
		"serial WAS consulted and actively disagreed")

	// Free from the branches the OR loop already takes, so recorded rather than collapsed.
	assert.Equal(t, []string{"uuid"}, report.AbsentFromItem)
	assert.Equal(t, []string{"serial"}, report.PresentButDiffering)
	assert.Equal(t, 4, report.Declared(), "every declared identifier is accounted for in exactly one bucket")
}

// ARM C -- THE CONTROL, and it must go red if the report is wired to anything other than "an OR match over
// a strict subset". An AND match consulted every declared identifier and every one of them agreed, so there
// is no subset and nothing to report. A report built unconditionally, or on "any successful match", or in
// findItemInList rather than in the OR branch, fails here while still passing arms A and B.
func TestANDPolicyWithAllIdentifiersMatchingProducesNoReport(t *testing.T) {
	u := &UnstructuredClient{
		IdentifiersMatchPolicy: "AND",
		IdentifierFields:       []string{"chassis", "name"},
		Resource:               or444CR(),
	}

	// The RIGHT object: both identifiers agree, which is what AND requires.
	rightObject := map[string]interface{}{"chassis": "rack-1", "name": "child-b"}

	item, found, report, err := u.findItemInList([]interface{}{rightObject})

	require.NoError(t, err)
	require.True(t, found, "AND behaviour is untouched: all identifiers agree, so it matches")
	require.True(t, sameMapObject(rightObject, item))
	assert.Nil(t, report,
		"an AND match rests on the FULL declared set. Reporting a subset here would be false, and would "+
			"train operators to ignore the line that matters")
}

// And an AND match that fails is unchanged too: no match, no report, no error.
func TestANDPolicyThatDoesNotMatchProducesNoReport(t *testing.T) {
	u := &UnstructuredClient{
		IdentifiersMatchPolicy: "and",
		IdentifierFields:       []string{"chassis", "name"},
		Resource:               or444CR(),
	}

	item, found, report, err := u.findItemInList([]interface{}{
		map[string]interface{}{"chassis": "rack-1", "name": "enclosure-a"},
	})

	require.NoError(t, err)
	assert.False(t, found, "AND rejects on `name`, which is the behaviour #444 asks operators to opt into")
	assert.Nil(t, item)
	assert.Nil(t, report)
}

// ARM D -- THE CONTROL-FLOW CONTROL. #444's fix is diagnostic: the same item must be adopted as before, in
// the same order, in every case. A previous attempt at a three-valued match outcome DID move control flow
// and was reverted after breaking a live kind, so this is asserted rather than assumed.
//
// Both sub-cases put a SECOND item in the list that would also match, so adopting the wrong one is
// detectable: the pre-change code returns the first item for which isItemMatch is true, and nothing else.
func TestTheReportDoesNotMoveWhichItemIsAdopted(t *testing.T) {
	t.Run("first-identifier match", func(t *testing.T) {
		u := &UnstructuredClient{
			IdentifierFields: []string{"chassis", "name"},
			Resource:         or444CR(),
		}

		first := map[string]interface{}{"chassis": "rack-1", "name": "enclosure-a"}
		// Agrees on BOTH identifiers -- the object the CR actually names. The pre-change code never
		// reaches it, and neither may this one.
		second := map[string]interface{}{"chassis": "rack-1", "name": "child-b"}

		item, found, report, err := u.findItemInList([]interface{}{first, second})

		require.NoError(t, err)
		require.True(t, found)
		assert.True(t, sameMapObject(first, item),
			"still the FIRST matching item, not the better one. Preferring `second` would be a fix to the "+
				"default, which #444 explicitly does not make here")

		// Byte-identical to what the pre-change code adopted.
		got, merr := json.Marshal(item)
		require.NoError(t, merr)
		assert.JSONEq(t, `{"chassis":"rack-1","name":"enclosure-a"}`, string(got))

		assert.NotNil(t, report, "the report is the only thing that is new")
	})

	t.Run("later-identifier match", func(t *testing.T) {
		u := &UnstructuredClient{
			IdentifierFields: []string{"serial", "chassis"},
			Resource:         or444CR(),
		}

		// serial disagrees, chassis agrees -> adopted on the second identifier.
		first := map[string]interface{}{"serial": "SN-ENCLOSURE-A", "chassis": "rack-1"}
		second := map[string]interface{}{"serial": "SN-CHILD-B", "chassis": "rack-1"}

		item, found, report, err := u.findItemInList([]interface{}{first, second})

		require.NoError(t, err)
		require.True(t, found)
		assert.True(t, sameMapObject(first, item),
			"iteration order is unchanged: a later identifier matching does not make the matcher keep "+
				"looking for a better item")

		got, merr := json.Marshal(item)
		require.NoError(t, merr)
		assert.JSONEq(t, `{"serial":"SN-ENCLOSURE-A","chassis":"rack-1"}`, string(got))

		require.NotNil(t, report)
		assert.Equal(t, "chassis", report.MatchedIdentifier)
		assert.Empty(t, report.NotConsulted,
			"the match landed on the LAST declared identifier, so nothing went unconsulted -- yet the "+
				"basis is still 1 of 2, which is why the report is still produced")
		assert.Equal(t, []string{"serial"}, report.PresentButDiffering)
	})
}

// ARM E -- a single declared identifier. Nothing can have gone unconsulted and the basis WAS the full
// declared set, so there is no subset and no report. This is the other half of the "strict subset" rule,
// and the common case for most kinds: it must stay silent or the line becomes noise.
func TestASingleIdentifierUnderORProducesNoReport(t *testing.T) {
	u := &UnstructuredClient{
		IdentifierFields: []string{"chassis"},
		Resource:         or444CR(),
	}

	item, found, report, err := u.findItemInList([]interface{}{
		map[string]interface{}{"chassis": "rack-1", "name": "enclosure-a"},
	})

	require.NoError(t, err)
	require.True(t, found)
	assert.NotNil(t, item)
	assert.Nil(t, report,
		"one declared identifier is matched in full. A report here would fire on every findby of every "+
			"single-identifier kind, which is most of them")
}

// No match means no adoption, so there is no basis to describe.
func TestNoORMatchProducesNoReport(t *testing.T) {
	u := &UnstructuredClient{
		IdentifierFields: []string{"chassis", "name"},
		Resource:         or444CR(),
	}

	item, found, report, err := u.findItemInList([]interface{}{
		map[string]interface{}{"chassis": "rack-9", "name": "enclosure-a"},
	})

	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, item)
	assert.Nil(t, report, "nothing was adopted, so nothing was adopted on a subset")
}

// The report must describe the ADOPTED item, not an item rejected earlier in the list. isItemMatch builds a
// report per item, and keeping the wrong one would name an identifier that had nothing to do with the
// adoption -- a plausible-looking line pointing at the wrong field.
func TestTheReportDescribesTheAdoptedItemNotAnEarlierRejectedOne(t *testing.T) {
	u := &UnstructuredClient{
		IdentifierFields: []string{"chassis", "name"},
		Resource:         or444CR(),
	}

	rejected := map[string]interface{}{"chassis": "rack-9", "name": "other"}
	adopted := map[string]interface{}{"chassis": "rack-1", "name": "enclosure-a"}

	item, found, report, err := u.findItemInList([]interface{}{rejected, adopted})

	require.NoError(t, err)
	require.True(t, found)
	require.True(t, sameMapObject(adopted, item))

	require.NotNil(t, report)
	assert.Equal(t, "chassis", report.MatchedIdentifier)
	assert.Equal(t, []string{"name"}, report.NotConsulted)
	assert.Empty(t, report.PresentButDiffering,
		"the rejected item's two disagreeing identifiers must not leak into the adopted item's report")
}

// An identifier whose declared path does not parse is skipped without comparing anything, so it is neither
// absent from the item nor present-and-differing. Filing it as either would make that field's claim false.
func TestAnUnparseableIdentifierPathIsReportedSeparately(t *testing.T) {
	u := &UnstructuredClient{
		IdentifierFields: []string{"not a path", "chassis", "name"},
		Resource:         or444CR(),
	}

	item, found, report, err := u.findItemInList([]interface{}{
		map[string]interface{}{"chassis": "rack-1", "name": "enclosure-a"},
	})

	require.NoError(t, err)
	require.True(t, found, "unchanged: an unparseable path is skipped, it does not fail the search")
	require.NotNil(t, item)

	require.NotNil(t, report)
	assert.Equal(t, "chassis", report.MatchedIdentifier)
	assert.Equal(t, []string{"name"}, report.NotConsulted)
	assert.Equal(t, []string{"not a path"}, report.Unparseable)
	assert.Empty(t, report.AbsentFromItem)
	assert.Empty(t, report.PresentButDiffering)
	assert.Equal(t, 3, report.Declared())
}

// isItemMatch is where the subset is decided, so assert it there too rather than only through
// findItemInList: a report produced for the right item but on the wrong basis is invisible one layer up.
func TestIsItemMatchReportsTheSubsetBasisDirectly(t *testing.T) {
	u := &UnstructuredClient{
		IdentifierFields: []string{"chassis", "name"},
		Resource:         or444CR(),
	}

	matched, report, err := u.isItemMatch(map[string]interface{}{
		"chassis": "rack-1",
		"name":    "enclosure-a",
	})

	require.NoError(t, err)
	assert.True(t, matched, "the bool is exactly what it was")
	require.NotNil(t, report)
	assert.Equal(t, "chassis", report.MatchedIdentifier)
	assert.Equal(t, []string{"name"}, report.NotConsulted)
}

// A structurally failed comparison still propagates and carries NO report: the match was never decided, so
// there is no basis to describe. Guards the interaction with #442, which this change is stacked on.
func TestAComparisonErrorCarriesNoReport(t *testing.T) {
	u := &UnstructuredClient{
		IdentifierFields: []string{"chassis", "name"},
		Resource:         nil, // isInResource fails structurally
	}

	item, found, report, err := u.findItemInList([]interface{}{
		map[string]interface{}{"chassis": "rack-1"},
	})

	require.Error(t, err, "unchanged from #442: the error reaches the caller")
	assert.Nil(t, item)
	assert.False(t, found)
	assert.Nil(t, report, "no match was established, so nothing was adopted on a subset")
}
