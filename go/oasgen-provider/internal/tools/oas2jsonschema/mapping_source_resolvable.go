package oas2jsonschema

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/pathparsing"
)

// warnUnresolvableMappingSource reports a request-direction fieldMapping whose inCustomResource names
// nothing the generated CR will carry (#458).
//
// THE FAILURE IT CATCHES IS SILENT BY CONSTRUCTION. The parameter is omitted from the spec ON THE STRENGTH
// of the mapping; the mapping then reads a field that is not there; and the request is built without the
// value. Nothing errors. For a path parameter that is a URL with an empty segment, which is a syntactically
// valid URL addressing something that does not exist -- so the vendor answers 404 and the reconciler acts
// on not-found by CREATING, the #117 shape.
//
// RESOLVED AGAINST THE GENERATED JSON, not against the *Schema. Two reasons, and the second is the one
// that decided it:
//
//   - the generated document is what becomes the CRD, so it is the authority on what a CR can carry. A
//     *Schema walk would be answering a question about an intermediate representation.
//   - the only existing walk, removeFieldAtPath, is DESTRUCTIVE: it removes what it finds. Reusing it is
//     not possible and reimplementing its traversal would be a second copy of the same logic to keep in
//     step -- the failure this repo keeps hitting.
//
// PER-VERB, which is the opposite of what #458's own implementation note advises. That note said the screen
// had to be resource-wide because mappingSuppliedParams suppressed a parameter for EVERY verb; #457 made
// suppression per-verb, so a resource-wide screen would now clear a mapping on the strength of a different
// verb's spec field. The note's premise is gone; the predicate it served is unchanged.
//
// A WARNING, NOT A REFUSAL, and that is a deliberate choice rather than the easy one. See the comment on
// the emission below.
func (g *OASSchemaGenerator) warnUnresolvableMappingSource(specSchemaJSON, statusSchemaJSON []byte) []error {
	if g.resourceConfig == nil {
		return nil
	}

	specProps := topLevelProperties(specSchemaJSON)
	statusProps := topLevelProperties(statusSchemaJSON)

	// KEYED PER (VERB, DESTINATION, PARAMETER, SOURCE), so two verbs declaring the SAME broken mapping are
	// reported twice -- once each. Each verb builds its own request, so each has its own broken request,
	// and the author has two places to fix. Collapsing them would name one verb and leave the other
	// silently broken, which is the resource-wide reporting #457 removed from the suppression.
	//
	// The key exists to dedupe a mapping repeated WITHIN one verb, not across verbs.
	//
	// An earlier version of this comment said the opposite -- "reported once even when several verbs
	// declare it identically" -- describing a key without the verb in it while the code had one. A
	// surviving mutant found it: dropping verb.Action from the key changed nothing any test could see,
	// because no arm declared the same broken mapping on two verbs. The comment was wrong and the gap in
	// the arms is what let it stand.
	//
	// Sorted on the way out so the warning order does not depend on map iteration.
	type offence struct {
		action, dest, param, source, reason string
	}
	seen := map[string]offence{}

	for _, verb := range g.resourceConfig.Verbs {
		for _, m := range verb.FieldMapping {
			dest, param := requestDestination(m)
			if dest == "" {
				continue // response-direction entry, or no request destination: nothing is built from it
			}
			if m.InCustomResource == "" {
				continue // reported, if at all, by whatever validates the entry's shape
			}

			// A RESOLVER SUPPLIES ITS OWN VALUE, so its inCustomResource is not a read path. Measured in
			// rest-dynamic-controller rather than inferred: ResolveRequestResolvers reads a secretRef's
			// value from r.NameFromCustomResource/r.KeyFromCustomResource, and a dependentLookup's from
			// lookupValues[r.Name]. m.InCustomResource appears there only inside error messages, as a
			// label for the field being filled. Screening these would refuse working configurations --
			// the over-reach that #455 produced and that this repo now keeps an arm against.
			//
			// NOTE FOR A FOLLOW-UP: a secretRef's NameFromCustomResource and KeyFromCustomResource ARE
			// read paths and would benefit from exactly this check. That is a different predicate from
			// #458's and belongs in its own issue rather than smuggled in here.
			if m.ResolverType != "" {
				continue
			}

			root, rest, reason := resolveCustomResourcePath(m.InCustomResource, specProps, statusProps)
			if reason == "" {
				continue
			}
			key := verb.Action + "\x00" + dest + "\x00" + param + "\x00" + m.InCustomResource
			if _, dup := seen[key]; dup {
				continue
			}
			_ = root
			_ = rest
			seen[key] = offence{action: verb.Action, dest: dest, param: param, source: m.InCustomResource, reason: reason}
		}
	}
	if len(seen) == 0 {
		return nil
	}

	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]error, 0, len(keys))
	for _, k := range keys {
		o := seen[k]
		// A WARNING RATHER THAN A REFUSAL. #458 offers either, and the repo's own history decides it:
		// 0.51.0 shipped a guard that refused configurations which worked, and four live RestDefinitions
		// with twelve CRs between them were reported broken by it. A false positive here -- a source this
		// screen cannot see but the controller can -- would do the same. A missed warning costs a silent
		// wrong request that is ALREADY happening today; a false refusal breaks a kind that is working.
		//
		// The message names the verb, the destination and the source, because those three are what an
		// author edits, and says what the request will do, because "not found" alone does not convey that
		// the parameter is also absent from the spec.
		out = append(out, SchemaGenerationError{
			Code: CodeFieldNotFound,
			Message: fmt.Sprintf(
				"verb %q maps %s parameter %q from %q, which %s: the parameter is omitted from the generated "+
					"spec because the mapping is expected to supply it, so the request will be built with no "+
					"value for it. Point inCustomResource at a field the CR carries, or remove the mapping so "+
					"the parameter returns to the spec",
				o.action, o.dest, o.param, o.source, o.reason),
		})
	}
	return out
}

// requestDestination returns the request location this entry writes and the name it writes there, or
// ("", "") when the entry has no request destination at all.
//
// inBody is included: a body mapping whose source is absent sends the body without that property, which is
// the same silent-wrong-request shape as a path or query. It is listed last because inPath and inQuery are
// the destinations mappingSuppliedParams acts on, so they are the ones that also cost a spec field.
func requestDestination(m FieldMappingEntry) (dest, name string) {
	switch {
	case m.InPath != "":
		return "path", m.InPath
	case m.InQuery != "":
		return "query", m.InQuery
	case m.InBody != "":
		return "body", m.InBody
	}
	return "", ""
}

// resolveCustomResourcePath reports why inCustomResource names nothing the CR will carry, or "" when it
// resolves.
//
// Only the ROOT segment is checked against the generated properties, and that is a deliberate bound rather
// than laziness: a nested source like `status.node.uuid` resolves through a response-shaped subtree that
// the status builder composes from the vendor's response, and this screen cannot know which nested keys
// that subtree will carry at runtime. Claiming a nested miss would produce false positives on exactly the
// shapes a consumer uses most. The root segment IS decidable: it is a top-level property of spec or status,
// both of which are fully generated here.
func resolveCustomResourcePath(inCustomResource string, specProps, statusProps map[string]bool) (root, rest, reason string) {
	segments, err := pathparsing.ParsePath(inCustomResource)
	if err != nil || len(segments) == 0 {
		return "", "", fmt.Sprintf("is not a parseable field path (%v)", err)
	}

	switch segments[0] {
	case "spec":
		if len(segments) < 2 {
			return "", "", "names the whole spec rather than a field in it"
		}
		// A NIL SET MEANS "COULD NOT READ", NOT "EMPTY". Go returns the zero value for a missing key, so
		// without this a nil map would make EVERY source look unresolvable and the screen would report the
		// whole resource on an unparseable document. My own comment on topLevelProperties claimed this
		// behaviour before the code had it.
		if specProps == nil {
			return "", "", ""
		}
		if !specProps[segments[1]] {
			return segments[1], strings.Join(segments[2:], "."), "is not a property of the generated spec"
		}
	case "status":
		if len(segments) < 2 {
			return "", "", "names the whole status rather than a field in it"
		}
		if statusProps == nil {
			return "", "", "" // see the spec case: nil is "could not read", not "empty"
		}
		if !statusProps[segments[1]] {
			return segments[1], strings.Join(segments[2:], "."), "is not a property of the generated status"
		}
	default:
		// Neither prefix. The controller resolves a mapping source against the CR, whose only two data
		// stanzas are spec and status, so a path rooted anywhere else cannot resolve -- but metadata is a
		// real stanza a consumer might legitimately read, so it is excluded from the complaint rather
		// than reported on a guess.
		if segments[0] == "metadata" {
			return "", "", ""
		}
		return segments[0], "", fmt.Sprintf("is rooted at %q, which is not a stanza of the custom resource", segments[0])
	}
	return "", "", ""
}

// topLevelProperties returns the property names of a generated schema document, or an empty set when the
// document is absent or unparseable.
//
// NIL ON FAILURE, AND THE CALLER MUST CHECK FOR IT. The caller's complaint is "not present", and Go
// returns false for a missing key, so a nil set would make every source look unresolvable and report the
// whole resource on one unparseable document. resolveCustomResourcePath therefore returns "resolves" when
// the relevant set is nil -- "could not read" is not "empty".
//
// This comment originally described that contract while the code did not implement it. It was written from
// the design rather than from the function, which is the way a comment asserting untested safety gets in.
func topLevelProperties(schemaJSON []byte) map[string]bool {
	if len(schemaJSON) == 0 {
		return nil
	}
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schemaJSON, &doc); err != nil {
		return nil
	}
	out := make(map[string]bool, len(doc.Properties))
	for name := range doc.Properties {
		out[name] = true
	}
	return out
}
