package oas2jsonschema

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The document: `get` takes {id} from the path, `create` posts a body. Two verbs so the per-verb property
// is testable, and `id` is a real path parameter so a mapping onto it is realistic.
const mappingSourceSpec = `
openapi: 3.0.0
info: {title: things, version: "1.0"}
components:
  schemas:
    Thing:
      type: object
      properties:
        name: {type: string}
        colour: {type: string}
      required: [name]
paths:
  /things/{id}:
    get:
      operationId: getThing
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
  /things:
    post:
      operationId: createThing
      requestBody:
        content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
`

func warningsFor(t *testing.T, rc *ResourceConfig) []string {
	t.Helper()
	doc, err := NewLibOASParser().Parse([]byte(mappingSourceSpec))
	require.NoError(t, err)
	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), rc)

	spec, _, err := g.BuildSpecSchema()
	require.NoError(t, err)
	status, _, err := g.BuildStatusSchema()
	require.NoError(t, err)

	var out []string
	for _, w := range g.warnUnresolvableMappingSource(spec, status) {
		out = append(out, w.Error())
	}
	return out
}

func baseVerbs(mapping []FieldMappingEntry) []Verb {
	return []Verb{
		{Action: ActionCreate, Method: "POST", Path: "/things"},
		{Action: ActionGet, Method: "GET", Path: "/things/{id}", FieldMapping: mapping},
	}
}

// THE SCREEN MUST BE ABLE TO FIRE. Without this arm every zero below is an unproven zero -- a screen that
// cannot tell "nothing to find" from "I looked nowhere" is the shape behind several defects in this repo,
// and the ironic session's own version of this check exits NON-ZERO when it finds none for that reason.
func TestUnresolvableMappingSourceIsReported(t *testing.T) {
	got := warningsFor(t, &ResourceConfig{
		Identifiers: []string{"name"},
		Verbs: baseVerbs([]FieldMappingEntry{
			{InPath: "id", InCustomResource: "status.nothing_here"},
		}),
	})

	require.Len(t, got, 1, "exactly one offending mapping, so exactly one warning")
	w := got[0]
	for _, want := range []string{`"get"`, "path", `"id"`, `"status.nothing_here"`, "generated status"} {
		require.Contains(t, w, want,
			"the message must name the verb, the destination, the parameter and the source -- those are what "+
				"an author edits. Got: %s", w)
	}
	require.Contains(t, w, "built with no value",
		"and must say what the request will DO; 'not found' alone does not convey that the parameter is "+
			"also absent from the spec")
}

// A source that DOES resolve must be silent. Paired with the arm above this is what makes the screen a
// measurement rather than an alarm.
func TestAResolvableMappingSourceIsSilent(t *testing.T) {
	require.Empty(t, warningsFor(t, &ResourceConfig{
		Identifiers:            []string{"name"},
		AdditionalStatusFields: []string{"colour"},
		Verbs: baseVerbs([]FieldMappingEntry{
			{InPath: "id", InCustomResource: "status.colour"},
		}),
	}), "status.colour is a declared status field; reporting it would be a false positive")
}

// A spec-rooted source resolves against the generated SPEC, which is a different document from status.
func TestASpecRootedSourceResolvesAgainstTheSpec(t *testing.T) {
	require.Empty(t, warningsFor(t, &ResourceConfig{
		Identifiers: []string{"name"},
		Verbs: baseVerbs([]FieldMappingEntry{
			{InPath: "id", InCustomResource: "spec.name"},
		}),
	}), "spec.name is a body property of create and therefore a spec field")

	got := warningsFor(t, &ResourceConfig{
		Identifiers: []string{"name"},
		Verbs: baseVerbs([]FieldMappingEntry{
			{InPath: "id", InCustomResource: "spec.not_a_field"},
		}),
	})
	require.Len(t, got, 1)
	require.Contains(t, got[0], "generated spec",
		"a spec-rooted miss must say SPEC, not status -- otherwise the author looks in the wrong stanza")
}

// A RESOLVER SUPPLIES ITS OWN VALUE, so its inCustomResource is not a read path and must not be screened.
//
// Measured in rest-dynamic-controller rather than inferred: ResolveRequestResolvers reads a secretRef's
// value from NameFromCustomResource/KeyFromCustomResource and a dependentLookup's from lookupValues[Name].
// m.InCustomResource appears there only inside error messages. Screening these would refuse working
// configurations, which is the over-reach #455 produced.
func TestAResolverEntryIsNotScreened(t *testing.T) {
	for _, kind := range []string{"secretRef", "dependentLookup"} {
		t.Run(kind, func(t *testing.T) {
			require.Empty(t, warningsFor(t, &ResourceConfig{
				Identifiers: []string{"name"},
				Verbs: baseVerbs([]FieldMappingEntry{
					// A source that would FAIL the screen if it were read. The point is that it is not.
					{InPath: "id", InCustomResource: "spec.definitely_absent", ResolverType: kind},
				}),
			}), "a %s entry's value comes from the resolver, not from inCustomResource", kind)
		})
	}
}

// metadata is a real stanza of a custom resource, so a source rooted there is not reported. Generation
// cannot enumerate its keys, so a complaint would be a guess.
func TestAMetadataRootedSourceIsNotReported(t *testing.T) {
	require.Empty(t, warningsFor(t, &ResourceConfig{
		Identifiers: []string{"name"},
		Verbs: baseVerbs([]FieldMappingEntry{
			{InPath: "id", InCustomResource: "metadata.name"},
		}),
	}))
}

// A root that is no stanza at all IS decidable and is reported, with the root named.
func TestASourceRootedOutsideTheResourceIsReported(t *testing.T) {
	got := warningsFor(t, &ResourceConfig{
		Identifiers: []string{"name"},
		Verbs: baseVerbs([]FieldMappingEntry{
			{InPath: "id", InCustomResource: "respons.id"}, // the realistic typo for `response`
		}),
	})
	require.Len(t, got, 1)
	require.Contains(t, got[0], `"respons"`, "name the offending root so a typo is visible")
	require.Contains(t, got[0], "not a stanza")
}

// PER-VERB, which is the opposite of what #458's implementation note advises -- the note's premise was
// resource-wide suppression, which #457 removed. Only the offending verb may be reported.
func TestOnlyTheOffendingVerbIsReported(t *testing.T) {
	got := warningsFor(t, &ResourceConfig{
		Identifiers:            []string{"name"},
		AdditionalStatusFields: []string{"colour"},
		Verbs: []Verb{
			{Action: ActionCreate, Method: "POST", Path: "/things",
				FieldMapping: []FieldMappingEntry{{InBody: "name", InCustomResource: "status.colour"}}},
			{Action: ActionGet, Method: "GET", Path: "/things/{id}",
				FieldMapping: []FieldMappingEntry{{InPath: "id", InCustomResource: "status.absent"}}},
		},
	})
	require.Len(t, got, 1, "create's mapping resolves; only get's must be reported")
	require.Contains(t, got[0], `"get"`)
	require.NotContains(t, got[0], `"create"`,
		"a resource-wide screen would implicate create too, which is the error #457 removed from the "+
			"suppression and which this screen must not reintroduce")
}

// DETERMINISTIC ORDER, asserted STRUCTURALLY rather than by repetition.
//
// THE FIRST VERSION OF THIS ARM WAS A FLAKY CONTROL, which is worse than a missing one: it compared five
// runs of a TWO-element result, and Go's randomised map iteration yields sorted order for two elements
// often enough that the mutation "drop the sort" was killed on one run and survived the next. I had
// already recorded it as killed. A control that passes sometimes reports a kill it did not earn.
//
// Fixed by making accidental agreement improbable AND by asserting the exact expected sequence: four
// offences whose keys sort distinctly, so an unsorted result matches the expected one in 1 of 4! orders,
// and the sequence itself is pinned rather than merely self-consistent.
func TestWarningsAreOrderedDeterministically(t *testing.T) {
	rc := &ResourceConfig{
		Identifiers: []string{"name"},
		Verbs: []Verb{
			{Action: ActionCreate, Method: "POST", Path: "/things", FieldMapping: []FieldMappingEntry{
				{InBody: "b_body", InCustomResource: "spec.absent_one"},
				{InQuery: "a_query", InCustomResource: "spec.absent_two"},
			}},
			{Action: ActionGet, Method: "GET", Path: "/things/{id}", FieldMapping: []FieldMappingEntry{
				{InPath: "id", InCustomResource: "spec.absent_three"},
				{InQuery: "z_query", InCustomResource: "spec.absent_four"},
			}},
		},
	}

	got := warningsFor(t, rc)
	require.Len(t, got, 4, "four offending mappings, four warnings. got: %v", got)

	// The key is action \x00 dest \x00 param \x00 source, so the expected sequence is fully determined:
	// create before get; within create, "body" before "query"; within get, "path" before "query".
	wantOrder := []struct{ action, param string }{
		{"create", "b_body"},
		{"create", "a_query"},
		{"get", "id"},
		{"get", "z_query"},
	}
	for i, want := range wantOrder {
		require.Contains(t, got[i], `"`+want.action+`"`,
			"position %d must be %s/%s; an unsorted result would place these by map iteration. got: %v",
			i, want.action, want.param, got)
		require.Contains(t, got[i], `"`+want.param+`"`,
			"position %d must be %s/%s. got: %v", i, want.action, want.param, got)
	}

	// And repeated runs must agree, which is the property the order exists to give a human comparing logs.
	for i := 0; i < 5; i++ {
		require.Equal(t, got, warningsFor(t, rc), "run %d disagreed", i)
	}
}

// AN UNREADABLE SCHEMA MUST NOT MAKE EVERY SOURCE LOOK ABSENT.
//
// The caller's complaint is "not present", and Go returns false for a missing key — so a nil property set
// would report every mapping rooted at that stanza. Found because the function's own comment claimed this
// contract while the code did not implement it: `if !specProps[seg]` on a nil map is `!false` → true.
//
// SCOPED PER STANZA, which my first version of this test got wrong. It asserted zero warnings whenever
// EITHER document was unreadable, and failed — correctly. An unreadable spec says nothing about whether a
// `status.` source resolves, and the readable half must keep working. The test was over-broad, not the code.
func TestAnUnreadableSchemaReportsNothingForThatStanzaOnly(t *testing.T) {
	// THE VERDICT PHRASES, not the bare stanza names. The message's own prose says "the parameter is
	// omitted from the generated spec because the mapping is expected to supply it", so asserting on
	// "generated spec" alone matches the explanation rather than the verdict -- which is exactly how the
	// first version of this assertion failed. The phrase has to be the one only a stanza miss produces.
	const specMiss = "is not a property of the generated spec"
	const statusMiss = "is not a property of the generated status"

	readableStatus := []byte(`{"properties":{"colour":{}}}`)
	readableSpec := []byte(`{"properties":{"name":{}}}`)
	broken := []byte("{not json")

	for name, tc := range map[string]struct {
		spec, status []byte
		wantStanzas  []string // the stanzas whose misses MUST still be reported
	}{
		"spec unreadable":   {spec: broken, status: readableStatus, wantStanzas: []string{statusMiss}},
		"status unreadable": {spec: readableSpec, status: broken, wantStanzas: []string{specMiss}},
		"both unreadable":   {spec: broken, status: broken, wantStanzas: nil},
		"both absent":       {spec: nil, status: nil, wantStanzas: nil},
	} {
		t.Run(name, func(t *testing.T) {
			doc, err := NewLibOASParser().Parse([]byte(mappingSourceSpec))
			require.NoError(t, err)
			g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
				Identifiers: []string{"name"},
				Verbs: baseVerbs([]FieldMappingEntry{
					{InPath: "id", InCustomResource: "spec.anything"},
					{InQuery: "q", InCustomResource: "status.anything"},
				}),
			})

			var got []string
			for _, w := range g.warnUnresolvableMappingSource(tc.spec, tc.status) {
				got = append(got, w.Error())
			}

			require.Len(t, got, len(tc.wantStanzas),
				"exactly the readable stanzas may report. got: %v", got)
			for i, stanza := range tc.wantStanzas {
				require.Contains(t, got[i], stanza)
			}
			// The unreadable stanza must never appear, whichever it is.
			if tc.spec == nil || string(tc.spec) == string(broken) {
				for _, w := range got {
					require.NotContains(t, w, specMiss,
						"the spec document could not be read, so a spec miss is the reader's failure, not the mapping's")
				}
			}
			if tc.status == nil || string(tc.status) == string(broken) {
				for _, w := range got {
					require.NotContains(t, w, statusMiss,
						"the status document could not be read, so a status miss is the reader's failure")
				}
			}
		})
	}
}

// TWO VERBS DECLARING THE SAME BROKEN MAPPING ARE REPORTED TWICE, once each.
//
// THE ARM THAT WAS MISSING. Dropping verb.Action from the dedup key survived every other arm: no test
// declared the same unresolvable mapping on two verbs, so nothing could see the difference between
// per-verb and per-resource reporting. The mutant survived, and in surviving it exposed a comment that
// claimed the collapsing behaviour while the code did the opposite.
//
// Reported twice is the correct side: each verb builds its own request, so each has its own broken
// request and the author has two places to fix. Collapsing would name one verb and leave the other
// silently broken — which is the resource-wide reporting #457 removed from the suppression.
func TestTheSameBrokenMappingOnTwoVerbsIsReportedForEach(t *testing.T) {
	broken := []FieldMappingEntry{{InPath: "id", InCustomResource: "status.absent"}}
	got := warningsFor(t, &ResourceConfig{
		Identifiers: []string{"name"},
		// create and get, because those are the two operations this document declares. Using a verb the
		// document lacks fails in BuildSpecSchema before the screen runs, which is how the first version
		// of this arm failed -- a PUT the fixture never had.
		Verbs: []Verb{
			{Action: ActionCreate, Method: "POST", Path: "/things", FieldMapping: broken},
			{Action: ActionGet, Method: "GET", Path: "/things/{id}", FieldMapping: broken},
		},
	})

	require.Len(t, got, 2,
		"each verb builds its own request, so each broken one must be named; collapsing to a single "+
			"warning leaves the unnamed verb silently broken. got: %v", got)

	var sawGet, sawCreate bool
	for _, w := range got {
		if strings.Contains(w, `"get"`) {
			sawGet = true
		}
		if strings.Contains(w, `"create"`) {
			sawCreate = true
		}
	}
	require.True(t, sawGet, "get must be named")
	require.True(t, sawCreate, "create must be named")
}

// And the key must still dedupe a mapping repeated WITHIN one verb, which is what it is for.
func TestADuplicateMappingWithinOneVerbIsReportedOnce(t *testing.T) {
	dup := FieldMappingEntry{InPath: "id", InCustomResource: "status.absent"}
	got := warningsFor(t, &ResourceConfig{
		Identifiers: []string{"name"},
		Verbs:       baseVerbs([]FieldMappingEntry{dup, dup}),
	})
	require.Len(t, got, 1, "one verb, one offending mapping declared twice: one warning. got: %v", got)
}

// DEPENDENT-LOOKUP PARAMETERS, the coverage gap a real corpus found.
//
// 4 of a consumer's 53 mapping sources sit in dependentLookups[].parameters, and nothing validated them:
// ValidateDependentLookups checks that a dependentLookup RESOLVER names a declared lookup — the lookup's
// identity, not its parameters' sources. The screen was blind to them by omission, not by design: the
// domain model already carries LookupParameter.InCustomResource, converted at render/helper.go:147.
func TestAnUnresolvableLookupParameterSourceIsReported(t *testing.T) {
	got := warningsFor(t, &ResourceConfig{
		Identifiers: []string{"name"},
		Verbs:       baseVerbs(nil),
		DependentLookups: []DependentLookup{{
			Name:   "defaultBranch",
			Method: "GET",
			Path:   "/things/{id}",
			Parameters: []LookupParameter{
				{InPath: "id", InCustomResource: "spec.not_a_field"},
			},
			Select: ".default_branch",
		}},
	})

	require.Len(t, got, 1)
	for _, want := range []string{`"defaultBranch"`, "lookup path", `"id"`, `"spec.not_a_field"`, "generated spec"} {
		require.Contains(t, got[0], want,
			"the message must name the LOOKUP, the destination, the parameter and the source. got: %s", got[0])
	}
	require.Contains(t, got[0], "lookup path",
		"a lookup's destination must be distinguishable from a verb's own parameter, or an author cannot "+
			"tell which request is broken")
}

// A resolvable lookup source is silent — the pair that makes the arm above a measurement.
func TestAResolvableLookupParameterSourceIsSilent(t *testing.T) {
	require.Empty(t, warningsFor(t, &ResourceConfig{
		Identifiers: []string{"name"},
		Verbs:       baseVerbs(nil),
		DependentLookups: []DependentLookup{{
			Name:       "defaultBranch",
			Method:     "GET",
			Path:       "/things/{id}",
			Parameters: []LookupParameter{{InPath: "id", InCustomResource: "spec.name"}},
			Select:     ".default_branch",
		}},
	}), "spec.name is a body property of create and therefore a spec field")
}

// A PARAMETER FED BY AN EARLIER LOOKUP READS NO CUSTOM RESOURCE, so it must not be screened.
//
// fromLookup is how the second of two chained lookups takes its input: the corpus's own gitref kind does
// exactly this (repo -> default_branch, then that branch's ref -> object.sha). Screening it would report
// every chained lookup in existence, which is the over-reach #455 produced in a different place.
func TestALookupParameterFedByAnotherLookupIsNotScreened(t *testing.T) {
	require.Empty(t, warningsFor(t, &ResourceConfig{
		Identifiers: []string{"name"},
		Verbs:       baseVerbs(nil),
		DependentLookups: []DependentLookup{{
			Name:   "branchRef",
			Method: "GET",
			Path:   "/things/{id}",
			// No InCustomResource at all: the value comes from the earlier lookup.
			Parameters: []LookupParameter{{InPath: "id", FromLookup: "defaultBranch"}},
			Select:     ".object.sha",
		}},
	}), "a fromLookup parameter takes its value from an earlier lookup, not from the CR")
}
