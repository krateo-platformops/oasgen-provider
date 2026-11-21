//go:build unit

package restResources

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/text"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
)

// TestAPartialFieldIsReportedAsReplayedNotDropped is the defect in one assertion.
//
// Both halves came from issue 400: this warning, and the partialFields merge that exists so those keys
// are NOT lost. The detection never learned about the merge, so on a kind declaring partialFields the
// operator was told their keys might be deleted -- which the merge makes impossible -- and was NOT told
// the hazard that is real there: a value the vendor masks on read is replayed as the value (issue 739).
func TestAPartialFieldIsReportedAsReplayedNotDropped(t *testing.T) {
	mg := driftMG(1, nil)
	h, evs := latchedHandler(t, mg)

	found := map[string][]string{"config": {"clientSecret"}}
	partial := text.StringSet{"config": {}}

	// SETUP CONTROL: the fixture really marks the field partial. Without this, a StringSet built wrong
	// would send the test down the non-partial branch and every assertion below would be about the
	// sentence this test is not for.
	require.True(t, partial.Contains("config"), "the fixture does not mark config partial, so this asserts nothing")

	require.True(t, h.reportUnmodelledKeys(context.Background(), mg, found, partial, logging.NewNopLogger()),
		"nothing was reported, so this test asserts nothing")

	all := evs.all()
	require.Len(t, all, 1, "want exactly one event")
	msg := all[0].Message
	lower := strings.ToLower(msg)

	require.Contains(t, msg, "REPLAYED",
		"the message does not say the keys are replayed. For a partial field that is the whole mechanism: "+
			"the keys ARE sent, so nothing can be dropped")
	require.Contains(t, lower, "mask",
		"the message does not mention masking, which is the only hazard a partial field actually has here")
	require.Contains(t, msg, "config: clientSecret",
		"the message does not name the key being replayed, so an operator cannot act on it")
	require.Contains(t, lower, "declare",
		"the message does not say what to do; a declared key wins over the vendor's value, and that is "+
			"the whole remedy")

	// THE INVERTED CLAIM MUST BE ABSENT. This is the assertion that fails on main: the old single message
	// said an update "sends each of these fields WHOLE" and asked whether the vendor "drops the keys it
	// was not sent" -- both false for a field whose keys are sent by construction.
	for _, forbidden := range []string{"sends each of these fields whole", "drops the keys it was not sent"} {
		require.NotContains(t, lower, forbidden,
			"the message still carries the non-partial claim %q for a PARTIAL field, where the keys are "+
				"sent by construction and cannot be dropped", forbidden)
	}
}

// TestAMixOfPartialAndWholeFieldsGetsBothSentences pins the case a single message cannot serve.
//
// A verb can declare one field partial and put another in the same body. One sentence would have to be
// wrong about one of them, and the wrong one is not predictable from the declaration alone.
func TestAMixOfPartialAndWholeFieldsGetsBothSentences(t *testing.T) {
	mg := driftMG(1, nil)
	h, evs := latchedHandler(t, mg)

	found := map[string][]string{
		"config":   {"clientSecret"}, // partial -> replayed
		"settings": {"manualQueueOnly"},
	}
	partial := text.StringSet{"config": {}}

	require.True(t, h.reportUnmodelledKeys(context.Background(), mg, found, partial, logging.NewNopLogger()))
	all := evs.all()
	require.Len(t, all, 1)
	msg := all[0].Message
	lower := strings.ToLower(msg)

	require.Contains(t, lower, "sends each of these fields whole",
		"the non-partial field lost its own sentence, so settings is now described by the partial rule")
	require.Contains(t, msg, "REPLAYED",
		"the partial field lost its sentence")

	// AND EACH KEY MUST SIT UNDER THE RIGHT CLAIM. Both sentences being present is not enough: a message
	// that named settings under "REPLAYED" would pass every assertion above and still be wrong.
	whole := strings.Index(lower, "sends each of these fields whole")
	replay := strings.Index(msg, "REPLAYED")
	settings := strings.Index(msg, "settings: manualQueueOnly")
	config := strings.Index(msg, "config: clientSecret")
	require.True(t, whole >= 0 && replay >= 0 && settings >= 0 && config >= 0, "setup: all four anchors must be present")
	require.True(t, settings > whole && settings < replay,
		"settings is not under the whole-field sentence: a non-partial field described as replayed tells "+
			"an operator their keys are safe when an update may drop them")
	require.True(t, config > replay,
		"config is not under the replay sentence: a partial field described as sent-whole repeats the "+
			"defect this change fixes")
}

// TestThePartialSetIsReadFromTheUpdateVerb closes the gap the message tests cannot reach.
//
// The tests above pass `partial` directly, so they say nothing about WHICH verb the call site reads it
// from -- and a mutation swapping Update for Get survived them. partialFields is declared PER VERB, and
// this warning is about the UPDATE verb's body: reading the Get verb's set would describe a kind's update
// using a declaration that governs its reads.
//
// Two halves, because neither alone is enough: the function must distinguish the verbs, and the call site
// must ask for the right one.
func TestThePartialSetIsReadFromTheUpdateVerb(t *testing.T) {
	// A kind that declares DIFFERENT partial fields on get and update. If the two were the same, every
	// assertion here would pass whichever verb the call site named -- the inputs have to separate the axes.
	verbs := []getter.VerbsDescription{
		{Action: "get", PartialFields: []string{"readOnlyThing"}},
		{Action: "update", PartialFields: []string{"config"}},
	}

	upd := partialFieldsFor(verbs, string(apiaction.Update))
	get := partialFieldsFor(verbs, string(apiaction.Get))

	require.True(t, upd.Contains("config"), "the update verb's partial field was not read")
	require.False(t, upd.Contains("readOnlyThing"),
		"the update set contains the GET verb's field, so the per-verb filter is not filtering")
	require.True(t, get.Contains("readOnlyThing"), "setup control: the get verb's field is readable, so "+
		"the assertion above is about filtering and not about an empty fixture")
	require.False(t, get.Contains("config"), "the get set contains the UPDATE verb's field")
}

// TestTheCallSiteNamesTheUpdateVerb is STRUCTURAL, and says so.
//
// It reads the source rather than the behaviour, because reaching the call site needs a clientInfo with
// per-verb partial declarations threaded through a live Observe -- which is an integration fixture. This
// is the honest instrument for the gap: weaker than a behavioural test, and it does catch the swap that
// survived the message tests.
func TestTheCallSiteNamesTheUpdateVerb(t *testing.T) {
	b, err := os.ReadFile("restResources.go")
	require.NoError(t, err)
	src := string(b)

	// PARSE CONTROL: the right file. Without it a bad path makes the assertion below fail for the wrong
	// reason, or -- written as an absence check -- pass for the wrong one.
	require.Contains(t, src, "func NewHandler(", "the file read does not look like restResources.go")

	// KEYED ON THE REPORTER'S OWN CALL, NOT ON PRESENCE. There are TWO partialFieldsFor(...Update) call
	// sites -- this one in Observe and the merge's own in Update, in a different handler call, which is
	// why the value cannot be shared. A `Contains` assertion passed with one of the two mutated to Get,
	// because the other still matched: the wrong equivalence class, and the mutation proved it.
	//
	// So this anchors on the text BETWEEN the partial lookup and the report, which only the warning's
	// call site has.
	idx := strings.Index(src, "h.reportUnmodelledKeys(ctx, mg, found, partial, log)")
	require.GreaterOrEqual(t, idx, 0,
		"the reporter is not called with a partial set at all, so the feature is unwired")

	window := src[max(0, idx-600):idx]
	require.Contains(t, window, "string(apiaction.Update)",
		"the partial set handed to the unmodelled-key warning is not the UPDATE verb's. partialFields is "+
			"declared per verb and this warning is about the update's body, so another verb's declaration "+
			"would describe the wrong thing -- and the message tests cannot see which verb was asked")
	require.NotContains(t, window, "string(apiaction.Get)",
		"the warning's partial set is read from the GET verb, whose partialFields govern reads rather "+
			"than the update body this message describes")
}
