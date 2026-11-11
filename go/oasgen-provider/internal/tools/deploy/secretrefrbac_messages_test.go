//go:build unit

package deploy

import (
	"strings"
	"testing"

	"github.com/krateo-platformops/plumbing/fieldpath"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FINDING 3 of the call-site wiring audit that followed issue 599.
//
// `anchorOf` and the branch that uses it existed with NO test naming them anywhere, and five mutations
// survived the whole suite -- inverting its guard, never taking its branch, flipping the inner `&&`,
// walking segs forwards instead of backwards, and an off-by-one on the returned prefix.
//
// The severity is low and saying so is part of the finding: both branches at the call site `continue`, and
// the anchor feeds one `fieldpath.Get` whose result selects which of two `log.Debug` lines is emitted. No
// mutation there can change the Role that comes out.
//
// But the distinction is a FEATURE WITH A REQUESTER. The source comment records that a consumer asked for
// it after I wrongly claimed the leaf read alone could tell the two absences apart:
//
//	the ENTRY is absent        a password-only user has no OTP credential. Expected.
//	the entry is present and
//	  its REF is absent        the author declared the credential and left the ref off.
//
// An operator reading a Role that came out narrower than expected needs to know which. So these tests
// assert the messages, which is the only observable the mechanism has.

// capturingLogger records Debug messages. The deploy package had no recording logger; `restdefinition`
// has one, and this is the same shape kept local rather than exported across packages for a test.
type capturingLogger struct{ msgs *[]string }

func (l capturingLogger) record(msg string)                   { *l.msgs = append(*l.msgs, msg) }
func (l capturingLogger) Info(msg string, _ ...any)           { l.record(msg) }
func (l capturingLogger) Debug(msg string, _ ...any)          { l.record(msg) }
func (l capturingLogger) Warn(msg string, _ ...any)           { l.record(msg) }
func (l capturingLogger) Error(_ error, msg string, _ ...any) { l.record(msg) }
func (l capturingLogger) WithValues(_ ...any) logging.Logger  { return l }
func (l capturingLogger) WithName(_ string) logging.Logger    { return l }

// parseForAnchorTest parses through the SAME parser CollectSecretRefNames uses, rather than assembling
// segments by hand -- a hand-built fixture can disagree with the parser, and the test would then pin a
// shape the production code never sees.
func parseForAnchorTest(t *testing.T, path string) ([]fieldpath.Segment, error) {
	t.Helper()
	return fieldpath.Parse(path)
}

const (
	msgEntryAbsent = "secretRef collection: no entry matches this path on this resource, so the Role will not name it"
	msgRefAbsent   = "secretRef collection: the entry exists but its Secret reference is absent, so the Role will not name it"
)

func countMsg(msgs []string, want string) int {
	n := 0
	for _, m := range msgs {
		if m == want {
			n++
		}
	}
	return n
}

// The two absences must be REPORTED differently, which is the whole purpose of anchorOf.
//
// Both rows produce the same Role, deliberately — so a test asserting only the Role cannot tell them
// apart, which is exactly why five mutations survived.
func TestTheTwoAbsencesAreReportedDifferently(t *testing.T) {
	verbs := secretRefVerbs(
		"spec.credentials[?type=password].valueSecretRef.name",
		"spec.credentials[?type=otp].secretDataSecretRef.name",
	)

	cases := []struct {
		name      string
		creds     []any
		wantMsg   string
		forbidden string
		why       string
	}{
		{
			name: "the ENTRY is absent",
			creds: []any{
				map[string]any{"type": "password", "valueSecretRef": map[string]any{"name": "pw-secret"}},
			},
			wantMsg:   msgEntryAbsent,
			forbidden: msgRefAbsent,
			why: "a password-only user has no OTP credential at all. Reporting 'the entry exists but its " +
				"ref is absent' would send an operator looking for a missing ref on an entry they never declared",
		},
		{
			name: "the entry is PRESENT and its REF is absent",
			creds: []any{
				map[string]any{"type": "password", "valueSecretRef": map[string]any{"name": "pw-secret"}},
				map[string]any{"type": "otp"},
			},
			wantMsg:   msgRefAbsent,
			forbidden: msgEntryAbsent,
			why: "the author declared the credential and left the ref off. Reporting 'no entry matches' " +
				"would hide a definition defect behind a message that reads as an expected absence",
		},
	}

	// The suite must be able to fail in both directions, asserted rather than assumed: if both rows
	// expected the same message, every arm would pass against a collapsed single-message implementation.
	require.NotEqual(t, cases[0].wantMsg, cases[1].wantMsg,
		"both rows expect the same message, so this table cannot detect the two branches being collapsed")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var msgs []string
			got, err := CollectSecretRefNames(verbs, keycloakUser("kc", tc.creds), capturingLogger{msgs: &msgs})
			require.NoError(t, err, "the collector must never fail on CR data")

			// PREMISE: the Role is identical in both rows. That is what makes the message the only
			// observable, and what makes this test necessary rather than redundant.
			require.Equal(t, map[string][]string{"kc": {"pw-secret"}}, got,
				"premise: both shapes must produce the SAME Role, or this test is about the Role and not "+
					"about the diagnostic")

			assert.Equal(t, 1, countMsg(msgs, tc.wantMsg),
				"expected exactly one %q.\nWhy it matters: %s\nLogged: %v", tc.wantMsg, tc.why, msgs)
			assert.Zero(t, countMsg(msgs, tc.forbidden),
				"the WRONG absence was reported. %s\nLogged: %v", tc.why, msgs)
		})
	}
}

// anchorOf's own contract, asserted directly, because the call site can only observe it through a log
// line and three of the five surviving mutations were inside the function.
func TestAnchorOfReturnsThePrefixUpToTheLastSelector(t *testing.T) {
	// Built through the real parser rather than hand-assembled, so a change to segment shapes surfaces
	// here instead of being papered over by a fixture.
	for _, tc := range []struct {
		name      string
		path      string
		wantOK    bool
		wantDepth int
		why       string
	}{
		{
			name: "a predicate path anchors at the predicate", path: "spec.credentials[?type=otp].secretDataSecretRef.name",
			wantOK: true, wantDepth: 3,
			why: "spec, credentials, [?type=otp] -- the ENTRY. Walking forwards instead of backwards, or " +
				"returning segs[:i], would anchor somewhere that resolves for the wrong reason",
		},
		{
			name: "an index path anchors at the index", path: "spec.items[0].ref.name",
			wantOK: true, wantDepth: 3,
			why: "an index is a selector too",
		},
		{
			// THE ARM THAT SEPARATES THE AXES. Every other path here has exactly ONE selector, so
			// "the first selector" and "the last selector" are the same segment and a mutant that walks
			// segs forwards instead of backwards passes -- which is precisely what happened: it survived
			// the first version of this test. Two selectors is the minimum input that can tell them apart.
			name: "TWO selectors anchor at the LAST one", path: "spec.items[0].credentials[?type=otp].valueSecretRef.name",
			wantOK: true, wantDepth: 5,
			why: "spec, items, [0], credentials, [?type=otp] -- the innermost ENTRY. Anchoring at the " +
				"FIRST selector (depth 3, spec.items[0]) would ask whether the ITEM exists, which is a " +
				"different question and resolves even when the credential entry does not",
		},
		{
			name: "a plain dotted path has no anchor", path: "spec.ref.name",
			wantOK: false, wantDepth: 0,
			why: "nothing selects an entry, so there is no entry to ask about and the caller must fall " +
				"through to the plain message",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			segs, err := parseForAnchorTest(t, tc.path)
			require.NoError(t, err)

			anchor, ok := anchorOf(segs)
			assert.Equal(t, tc.wantOK, ok, tc.why)
			if !tc.wantOK {
				assert.Nil(t, anchor, "a path with no selector must return a nil anchor, not an empty slice "+
					"that would then be Get() as the whole object")
				return
			}
			assert.Len(t, anchor, tc.wantDepth,
				"the anchor must include the selector itself and stop there: %s", tc.why)
			assert.True(t, len(anchor) < len(segs),
				"the anchor must be a strict prefix; equal length means it is the leaf path and asking "+
					"about it answers the same question the leaf read already answered")
		})
	}
}

// A guard so the two messages cannot be quietly unified. If someone collapses them, the table above
// fails — but this says why in one line at the point of change.
func TestTheTwoMessagesRemainDistinct(t *testing.T) {
	require.NotEqual(t, msgEntryAbsent, msgRefAbsent)
	assert.True(t, strings.Contains(msgRefAbsent, "the entry exists"),
		"the ref-absent message no longer says the entry EXISTS, which is the only thing distinguishing it "+
			"from the other absence for an operator reading it")
	assert.True(t, strings.Contains(msgEntryAbsent, "no entry matches"),
		"the entry-absent message no longer says no entry MATCHES")
}
