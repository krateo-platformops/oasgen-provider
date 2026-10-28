package oas2jsonschema

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lookupReadWarnings(t *testing.T, rc *ResourceConfig) []string {
	t.Helper()
	doc, err := NewLibOASParser().Parse([]byte(noParamDoc))
	require.NoError(t, err)

	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), rc)
	_, warnings, err := g.BuildSpecSchema()
	require.NoError(t, err)

	var msgs []string
	for _, w := range warnings {
		msgs = append(msgs, w.Error())
	}
	return msgs
}

// lookupReading builds a lookup that is VALID against noParamDoc except for the CR path under test, so a
// warning from this fixture can only be the CR-read check firing.
//
// It used to declare `GET /things`, which the document defines only POST for, and bound a path parameter
// `id` that no operation declares. Harmless while nothing validated the lookup's own path -- and the
// moment checkLookupPaths was added it made every subtest here report a method warning, including the
// "no warning" one. A fixture that declares something the document cannot support is a latent failure in
// any test that later asserts on warnings.
func lookupReading(path string) DependentLookup {
	return DependentLookup{
		Name: "probe", Method: "GET", Path: "/things/{name}", Select: ".x",
		Parameters: []LookupParameter{{InPath: "name", InCustomResource: path}},
	}
}

var createGet = []Verb{
	{Action: ActionCreate, Method: "POST", Path: "/things"},
	{Action: ActionGet, Method: "GET", Path: "/things/{name}"},
}

// TestLookupReadingAnAbsentSpecFieldWarns covers #240.
//
// Without the warning the mistake is invisible until runtime and then surfaces in the wrong place: the
// controller reports `"spec.y" not found on the custom resource` on every reconcile of every CR of the
// kind, naming a RestDefinition field, for a CR the author wrote correctly.
func TestLookupReadingAnAbsentSpecFieldWarns(t *testing.T) {
	t.Run("reads a field the spec has: no warning", func(t *testing.T) {
		msgs := lookupReadWarnings(t, &ResourceConfig{
			Verbs:            createGet,
			Identifiers:      []string{"name"},
			DependentLookups: []DependentLookup{lookupReading("spec.name")},
		})
		for _, m := range msgs {
			assert.NotContains(t, m, "dependent lookup", "a readable field must not warn: %s", m)
		}
	})

	t.Run("reads a field the spec does not have: warns, naming the lookup and the path", func(t *testing.T) {
		msgs := lookupReadWarnings(t, &ResourceConfig{
			Verbs:            createGet,
			Identifiers:      []string{"name"},
			DependentLookups: []DependentLookup{lookupReading("spec.nowhere")},
		})
		var hit string
		for _, m := range msgs {
			if strings.Contains(m, "spec.nowhere") {
				hit = m
			}
		}
		require.NotEmpty(t, hit, "expected a warning naming the unreadable path, got %v", msgs)
		assert.Contains(t, hit, `"probe"`, "the message must name WHICH lookup")
		assert.Contains(t, hit, "every reconcile", "the message should say what the consequence is")
	})

	t.Run("a status-rooted read is not checked", func(t *testing.T) {
		// status. paths resolve from additionalStatusFields, which the spec schema says nothing about.
		// Warning on them would fire on correct declarations.
		msgs := lookupReadWarnings(t, &ResourceConfig{
			Verbs:            createGet,
			Identifiers:      []string{"name"},
			DependentLookups: []DependentLookup{lookupReading("status.metadata.id")},
		})
		for _, m := range msgs {
			assert.NotContains(t, m, "status.metadata.id", "a status-rooted read must not warn: %s", m)
		}
	})

	t.Run("match.equalsFromCustomResource is checked too", func(t *testing.T) {
		msgs := lookupReadWarnings(t, &ResourceConfig{
			Verbs:       createGet,
			Identifiers: []string{"name"},
			DependentLookups: []DependentLookup{{
				Name: "probe", Method: "GET", Path: "/things",
				Match: &LookupMatch{
					ItemsAt: ".", WhereField: "alias", SelectField: "id",
					EqualsFromCustomResource: "spec.absent",
				},
			}},
		})
		found := false
		for _, m := range msgs {
			if strings.Contains(m, "spec.absent") && strings.Contains(m, "match.equalsFromCustomResource") {
				found = true
			}
		}
		assert.True(t, found, "a match's CR-side read must be checked as well, got %v", msgs)
	})
}

// TestLookupReadCheckSeesTheFinishedSpec pins the placement.
//
// The check runs after excludedSpecFields and the includedSpecFields allowlist, because a path present in
// the base schema may have been taken out by either. A check running earlier would pass on a field the
// finished spec does not have — which is the exact failure it exists to prevent, just later.
func TestLookupReadCheckSeesTheFinishedSpec(t *testing.T) {
	msgs := lookupReadWarnings(t, &ResourceConfig{
		Verbs:              createGet,
		Identifiers:        []string{"name"},
		ExcludedSpecFields: []string{"token"},
		DependentLookups:   []DependentLookup{lookupReading("spec.token")},
	})

	found := false
	for _, m := range msgs {
		if strings.Contains(m, "spec.token") && strings.Contains(m, "dependent lookup") {
			found = true
		}
	}
	assert.True(t, found,
		"excludedSpecFields removed the field the lookup reads, so the check must warn; got %v", msgs)
}
