package oas2jsonschema

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// actionOnlyVerbs is the shape issues 625 and 629 are both about: create is the only write verb, so
// unstructured-runtime can queue a Create only while findby reports the resource absent.
func actionOnlyVerbs() []Verb {
	return []Verb{
		{Action: ActionCreate, Method: "POST", Path: "/v1/nodes"},
		findByVerb("/v1/nodes", "", ""),
	}
}

// THE ESCAPES ARE ASSERTED ONE AT A TIME, because a predicate with five clauses and one test is a
// predicate with four untested clauses. Each row removes the action-only property by a DIFFERENT
// route, and each must independently stop issue 629's warning.
func TestEachEscapeFromTheActionOnlyShapeIsHonouredIndependently(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(g *OASSchemaGenerator)
		why    string
	}{
		{
			name: "a get verb",
			mutate: func(g *OASSchemaGenerator) {
				g.resourceConfig.Verbs = append(g.resourceConfig.Verbs,
					Verb{Action: ActionGet, Method: "GET", Path: "/v1/nodes/{uuid}"})
			},
			why: "Observe no longer decides existence from findby alone",
		},
		{
			name: "an update verb",
			mutate: func(g *OASSchemaGenerator) {
				g.resourceConfig.Verbs = append(g.resourceConfig.Verbs,
					Verb{Action: ActionUpdate, Method: "PATCH", Path: "/v1/nodes/{uuid}"})
			},
			why: "drift can drive a write, so a matching findby does not make the kind inert",
		},
		{
			name: "a dispatch verb",
			mutate: func(g *OASSchemaGenerator) {
				g.resourceConfig.Verbs = append(g.resourceConfig.Verbs,
					Verb{Action: ActionDispatch, Method: "POST", Path: "/v1/nodes"})
			},
			why: "the action has a reachable path that does not depend on absence",
		},
		{
			// THE ESCAPE THE GENERATOR COULD NOT SEE until CreateDelegated was plumbed. The runtime
			// handles createApiRef before building any create call, so such a kind creates perfectly
			// well -- and warning it would be a confident false positive on a legitimate shape, which
			// is the exact failure issue 625 exists to stop.
			name:   "createApiRef (creation delegated to a RESTAction)",
			mutate: func(g *OASSchemaGenerator) { g.resourceConfig.CreateDelegated = true },
			why:    "the create is reachable through the delegated action, with no create verb needed",
		},
		{
			name:   "observeApiRef (observation delegated)",
			mutate: func(g *OASSchemaGenerator) { g.resourceConfig.ObserveDelegated = true },
			why:    "the findby is declared but never executed, so it cannot make anything unreachable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// CONTROL FIRST: without the mutation the warning MUST fire, or this row proves nothing.
			base := matchGen([]string{"uuid"}, actionOnlyVerbs(), ironicShapedDoc())
			require.NotEmpty(t, base.warnCreateUnreachableForActionOnlyKind(base.matchability()),
				"the unmutated fixture does not warn, so this row cannot show that %s suppresses it", tc.name)

			g := matchGen([]string{"uuid"}, actionOnlyVerbs(), ironicShapedDoc())
			tc.mutate(g)
			assert.False(t, g.isActionOnly(), "%s must remove the action-only property: %s", tc.name, tc.why)
			assert.Empty(t, g.warnCreateUnreachableForActionOnlyKind(g.matchability()),
				"issue 629's warning fired for a kind whose create is reachable via %s. %s", tc.name, tc.why)
		})
	}
}

// THESE ARMS ARE PERMANENTLY THE ONLY COVERAGE FOR THE POSITIVE DIRECTION. DO NOT DELETE THEM AS
// UNEXERCISED.
//
// The diagnostic they test fires only for an action-only kind whose findby CAN match -- which is, by
// definition, a kind whose only effect never fires. There is no healthy configuration in which it is
// correct and the resource works. So:
//
//   - no consumer can ever exercise this path, now or later: adoption means WORKING definitions;
//   - the only live route is deliberately breaking a kind, which has been declined;
//   - therefore these arms are not interim coverage pending field evidence. Waiting cannot help.
//
// A consumer census found ONE action-only definition across 38 consumer kinds, and even that one
// cannot reach the condition site (its findby cannot match, so Observe returns at the not-found
// branch). The natural cleanup instinct -- "remove the case no real manifest exercises" -- would
// delete exactly the only thing testing this.
//
// The general test, worth applying to any diagnostic: ASK WHETHER A HEALTHY SYSTEM CAN PRODUCE THE
// SIGNAL. If it cannot, "we will see it in the field" is not a plan.
//
// ISSUE 629 FIRES when the action-only kind's findby CAN match.
//
// `uuid` IS carried by the collection items in the Ironic-shaped document, so findby matches, Observe
// returns {ResourceExists: true, ResourceUpToDate: true}, and the create -- the kind's only effect --
// never fires again.
func TestAnActionOnlyKindWhoseFindByCanMatchIsWarned(t *testing.T) {
	g := matchGen([]string{"uuid"}, actionOnlyVerbs(), ironicShapedDoc())

	require.True(t, g.isActionOnly(), "the fixture must be the action-only shape")
	require.Equal(t, MatchabilityCanMatch, g.matchability().Verdict,
		"`uuid` is carried by the findby items, so the verdict must be CanMatch or this arm tests the wrong thing")

	errs := g.warnCreateUnreachableForActionOnlyKind(g.matchability())
	require.Len(t, errs, 1)
	var sge SchemaGenerationError
	require.True(t, errors.As(errs[0], &sge))
	assert.Equal(t, CodeCreateUnreachableForActionOnlyKind, sge.Code)
	assert.Equal(t, "spec.resource.verbsDescription", sge.Path)
	assert.Contains(t, sge.Message, "only write verb")
	assert.Contains(t, sge.Message, "dispatch",
		"the message must offer the alternative, not just name the problem")
}

// ISSUE 625'S SUPPRESSION, and issue 629 staying silent, from the SAME fixture.
//
// `name` is absent from the collection items, so findby can never match -- which for this shape is
// not a defect but the only thing that makes the resource work. Neither diagnostic may fire: the old
// one because its advice would disable the kind, the new one because the create IS reachable here.
func TestAnActionOnlyKindWhoseFindByCannotMatchGetsNeitherDiagnostic(t *testing.T) {
	g := matchGen([]string{"name"}, actionOnlyVerbs(), ironicShapedDoc())

	require.True(t, g.isActionOnly())
	mr := g.matchability()
	require.Equal(t, MatchabilityCannotMatch, mr.Verdict,
		"`name` is absent from the findby items, so the verdict must be CannotMatch")

	assert.Empty(t, g.warnIdentifiersMissingFromFindByResponse(mr),
		"issue 625: this is the warning that told an author to make findby match, which disabled "+
			"every provision transition for 24 hours. It must not fire for this shape")
	assert.Empty(t, g.warnCreateUnreachableForActionOnlyKind(mr),
		"issue 629's warning must not fire when findby CANNOT match: the create is reachable, which "+
			"is the whole design of this shape")

	// CONTROL: the same identifier on a NON-action-only kind MUST still warn, or the suppression
	// above could be hiding a warning that simply stopped working.
	withGet := matchGen([]string{"name"}, append(actionOnlyVerbs(),
		Verb{Action: ActionGet, Method: "GET", Path: "/v1/nodes/{uuid}"}), ironicShapedDoc())
	assert.NotEmpty(t, withGet.warnIdentifiersMissingFromFindByResponse(withGet.matchability()),
		"the original warning no longer fires even for a kind it is correct for, so the suppression "+
			"is not a suppression -- it is a removal")
}

// THE POLICY-DEPENDENT ROW, which a naive predicate gets wrong.
//
// Under OR with SOME identifiers present, matching narrows to the present ones and STILL SUCCEEDS.
// So the verdict is CanMatch and issue 629 must fire, even though an identifier is missing. A
// predicate reading "any missing means it cannot match" reports CannotMatch here and misses the
// defect entirely.
func TestUnderORWithSomeIdentifiersPresentTheVerdictIsCanMatch(t *testing.T) {
	// `uuid` IS in the collection items, `name` is NOT. Policy defaults to OR.
	g := matchGen([]string{"uuid", "name"}, actionOnlyVerbs(), ironicShapedDoc())

	mr := g.matchability()
	require.NotEmpty(t, mr.Missing, "the fixture must have a missing identifier or this row is the all-present case")
	require.Len(t, mr.Missing, 1, "exactly one identifier must be missing: got %v", mr.Missing)
	assert.Equal(t, MatchabilityCanMatch, mr.Verdict,
		"under OR, one present identifier is enough for findby to match; reporting CannotMatch here "+
			"would hide issue 629's defect on the most common policy")

	errs := g.warnCreateUnreachableForActionOnlyKind(mr)
	require.Len(t, errs, 1, "issue 629 must fire: findby can still match on the present identifier")
	var sge SchemaGenerationError
	require.True(t, errors.As(errs[0], &sge))
	assert.Contains(t, sge.Message, "OR",
		"the message must say WHY it can still match when an identifier is missing")

	// And under AND the same inputs flip the verdict, asserted here so the two policies are shown to
	// differ rather than assumed to.
	andVerbs := []Verb{
		{Action: ActionCreate, Method: "POST", Path: "/v1/nodes"},
		// findByVerb's signature is (path, POLICY, itemsPath) -- the policy is the SECOND
		// argument. Passing "AND" third sets itemsPath instead, which makes unwrapFindByItems
		// fail and the verdict Indeterminate, and the arm then fails for a reason that has
		// nothing to do with policy.
		findByVerb("/v1/nodes", "AND", ""),
	}
	gAnd := matchGen([]string{"uuid", "name"}, andVerbs, ironicShapedDoc())
	andResult := gAnd.matchability()
	require.Equal(t, "AND", andResult.Policy,
		"the fixture does not actually carry the AND policy, so this comparison would be between "+
			"two OR cases. findByVerb takes (path, policy, itemsPath)")
	assert.Equal(t, MatchabilityCannotMatch, andResult.Verdict,
		"under AND one absent identifier makes every comparison fail, so the verdict must differ "+
			"from the OR case on identical inputs")
	assert.NotEqual(t, mr.Verdict, andResult.Verdict,
		"the two policies produced the SAME verdict on identical identifiers and document, so this "+
			"test cannot show the verdict is policy-dependent at all")
}

// EVERY INDETERMINATE REASON PRODUCES NEITHER DIAGNOSTIC, and each row names which reason it
// exercised.
//
// Before the verdict type, all of these returned the same nil as "all identifiers are present", so
// "can match" and "cannot tell" were indistinguishable. Without the Reason assertion a passing row
// cannot show WHICH cause it covered, and the table could silently exercise one cause five times.
func TestEveryIndeterminateReasonSilencesBothDirections(t *testing.T) {
	for _, tc := range []struct {
		name   string
		build  func() *OASSchemaGenerator
		reason string
	}{
		{
			name:   "no identifiers declared",
			build:  func() *OASSchemaGenerator { return matchGen(nil, actionOnlyVerbs(), ironicShapedDoc()) },
			reason: IndeterminateNoIdentifiers,
		},
		{
			name: "no findby verb",
			build: func() *OASSchemaGenerator {
				return matchGen([]string{"uuid"},
					[]Verb{{Action: ActionCreate, Method: "POST", Path: "/v1/nodes"}}, ironicShapedDoc())
			},
			reason: IndeterminateNoFindBy,
		},
		{
			name: "observation delegated",
			build: func() *OASSchemaGenerator {
				g := matchGen([]string{"uuid"}, actionOnlyVerbs(), ironicShapedDoc())
				g.resourceConfig.ObserveDelegated = true
				return g
			},
			reason: IndeterminateObserveDelegated,
		},
		{
			name: "the findby response cannot be read",
			build: func() *OASSchemaGenerator {
				// A findby pointing at a path the document does not describe.
				return matchGen([]string{"uuid"}, []Verb{
					{Action: ActionCreate, Method: "POST", Path: "/v1/nodes"},
					findByVerb("/v1/does-not-exist", "", ""),
				}, ironicShapedDoc())
			},
			reason: IndeterminateResponseUnreadable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := tc.build()
			mr := g.matchability()

			require.Equal(t, MatchabilityIndeterminate, mr.Verdict,
				"this row must produce Indeterminate, got %s", mr.Verdict)
			assert.Equal(t, tc.reason, mr.Reason,
				"the row exercised a DIFFERENT indeterminate cause than it names, so the table does "+
					"not cover what it claims to")
			assert.Empty(t, mr.Missing, "an Indeterminate verdict must not carry a missing list")

			assert.Empty(t, g.warnIdentifiersMissingFromFindByResponse(mr),
				"a positive absence claim cannot rest on a verdict that could not be reached")
			assert.Empty(t, g.warnCreateUnreachableForActionOnlyKind(mr),
				"issue 629's claim is positive too: 'your only verb can never run' must not be emitted "+
					"because something could not be determined")
		})
	}
}

// THE ACCEPTANCE CRITERION, ASSERTED ON THE ARTIFACT rather than on a grep of the source.
//
// Issue 625's criterion is "zero diagnostics assume a create is unwanted" for this shape. A grep over
// the source cannot establish that -- the framing phrase is line-wrapped across comment lines (12
// files, 16 occurrences, measured) and most of those occurrences are comments that are perfectly
// accurate for the kinds they describe. What matters is what a consumer RECEIVES.
//
// So this generates the action-only shape and asserts that nothing in the warnings tells its author
// their create is a problem to be avoided.
func TestNoDiagnosticTellsAnActionOnlyKindItsCreateIsUnwanted(t *testing.T) {
	resourceConfig := &ResourceConfig{Identifiers: []string{"name"}, Verbs: actionOnlyVerbs()}
	result, err := NewOASSchemaGenerator(ironicShapedDoc(), DefaultGeneratorConfig(), resourceConfig).Generate()
	require.NoError(t, err)

	// CONTROL: the same document on a non-action-only kind MUST produce at least one warning, or an
	// empty warning list here would prove nothing about suppression.
	ctrlConfig := &ResourceConfig{
		Identifiers: []string{"name"},
		Verbs:       append(actionOnlyVerbs(), Verb{Action: ActionGet, Method: "GET", Path: "/v1/nodes/{uuid}"}),
	}
	ctrl, err := NewOASSchemaGenerator(ironicShapedDoc(), DefaultGeneratorConfig(), ctrlConfig).Generate()
	require.NoError(t, err)
	require.NotEmpty(t, ctrl.GenerationWarnings,
		"the control shape produced no warnings at all, so this test cannot tell suppression from silence")

	for _, w := range result.GenerationWarnings {
		msg := strings.ToLower(w.Error())
		assert.NotContains(t, msg, "acts on not-found by creating",
			"a diagnostic still frames this shape's create as an unwanted consequence: %s", w.Error())
		assert.NotContains(t, msg, "remove",
			"a diagnostic still advises REMOVING something from an action-only kind's identifiers, "+
				"which is the advice that disabled a consumer's transitions for 24 hours: %s", w.Error())
	}
}

// THE PLUMBING for the new direction: issue 629's warning must reach GenerationWarnings, not merely
// be returnable from its own function. This mirrors
// TestTheMatchabilityWarningReachesGenerationWarnings for the opposite shape.
func TestTheUnreachableCreateWarningReachesGenerationWarnings(t *testing.T) {
	resourceConfig := &ResourceConfig{Identifiers: []string{"uuid"}, Verbs: actionOnlyVerbs()}
	result, err := NewOASSchemaGenerator(ironicShapedDoc(), DefaultGeneratorConfig(), resourceConfig).Generate()
	require.NoError(t, err)

	var found bool
	for _, w := range result.GenerationWarnings {
		var sge SchemaGenerationError
		if errors.As(w, &sge) && sge.Code == CodeCreateUnreachableForActionOnlyKind {
			found = true
		}
	}
	assert.True(t, found,
		"issue 629's warning did not reach GenerationWarnings; got %v", result.GenerationWarnings)
}

// BOTH DIRECTIONS MUST READ ONE VERDICT, asserted on the source.
//
// If the two emitters each called g.matchability() themselves they could drift apart -- and the whole
// argument for one predicate is that a suppression and its inverse cannot disagree about the shape.
// Issue 627 was the cost of exactly this: one function advancing three values while comparing one.
func TestBothDirectionsAreDrivenByASingleVerdict(t *testing.T) {
	src := readSourceFile(t, "generator.go")

	assert.Equal(t, 1, strings.Count(src, "g.matchability()"),
		"generator.go must compute the matchability verdict exactly once and hand it to both "+
			"emitters; found %d calls. Two calls can diverge, which is what one predicate exists to "+
			"prevent.", strings.Count(src, "g.matchability()"))
	assert.Contains(t, src, "g.warnIdentifiersMissingFromFindByResponse(matchabilityVerdict)")
	assert.Contains(t, src, "g.warnCreateUnreachableForActionOnlyKind(matchabilityVerdict)")
}

// readSourceFile reads a file from this package's own directory, for the structural assertions above.
// Following the precedent in the deploy and restclient packages: a source assertion makes a future
// author who changes the shape come here and say why, which a behavioural test cannot do for a
// property that is about how the code is wired rather than what it computes.
func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	require.NoError(t, err, "reading %s for a structural assertion", name)
	require.NotEmpty(t, b, "%s is empty, so any Count() over it would be a vacuous 0", name)
	return string(b)
}

// THE REAL REGRESSION, kept as a fixture in preference to a constructed one.
//
// This is not a shape invented to exercise the predicate. It is the change a consumer actually
// shipped: a `responseTransform` that made the findby items carry `node_id`, applied to make an
// "unmatchable findby" warning go away. It disabled every provision transition for 24 hours, and the
// same change had been reverted in their repo four days earlier under the subject "the
// responseTransform I added today would have silently stopped every transition" -- so the warning
// this code suppresses had out-argued the author's own correct revert note.
//
// A historical bad state is better evidence than a constructed one: it is known to be reachable by a
// real author acting on a real message, which is exactly what a synthetic fixture cannot establish.
func TestTheRealIronicRegressionIsCaught(t *testing.T) {
	doc := ironicShapedDoc()
	// What the transform added: `node_id` present in the collection items.
	doc.Paths["/v1/nodes"].Ops["get"] = &mockOperation{Responses: map[int]ResponseInfo{200: {Content: map[string]*Schema{
		"application/json": obj(prop("nodes", arr(obj(
			prop("node_id", scalar("string")),
			prop("uuid", scalar("string")),
		)))),
	}}}}
	// ironic-node-provision, verbatim: findby + create, one identifier, OR policy.
	g := matchGen([]string{"node_id"}, []Verb{
		findByVerb("/v1/nodes", "OR", ""),
		{Action: ActionCreate, Method: "PUT", Path: "/v1/nodes/{node_id}/states/provision"},
	}, doc)

	require.True(t, g.isActionOnly(), "the real definition must be recognised as action-only")
	mr := g.matchability()
	require.Equal(t, MatchabilityCanMatch, mr.Verdict,
		"with the transform applied the findby CAN match, which is what made the create unreachable")

	errs := g.warnCreateUnreachableForActionOnlyKind(mr)
	require.Len(t, errs, 1,
		"the change that cost a consumer 24 hours of stopped transitions must produce a diagnostic")

	// CONTROL: without the transform -- the configuration they shipped as the fix -- it must be SILENT.
	clean := matchGen([]string{"node_id"}, []Verb{
		findByVerb("/v1/nodes", "OR", ""),
		{Action: ActionCreate, Method: "PUT", Path: "/v1/nodes/{node_id}/states/provision"},
	}, ironicShapedDoc())
	assert.Empty(t, clean.warnCreateUnreachableForActionOnlyKind(clean.matchability()),
		"the CORRECT configuration must not be warned about -- an unmatchable findby is what makes "+
			"this shape work, and warning it is the defect issue 625 exists to stop")
}
