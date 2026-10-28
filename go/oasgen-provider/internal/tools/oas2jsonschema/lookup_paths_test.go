package oas2jsonschema

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lookupPathWarnings generates against noParamDoc, which declares POST /things and
// GET /things/{name} with one path parameter, `name`.
func lookupPathWarnings(t *testing.T, lookups ...DependentLookup) []string {
	t.Helper()
	doc, err := NewLibOASParser().Parse([]byte(noParamDoc))
	require.NoError(t, err)

	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Verbs:            createGet,
		Identifiers:      []string{"name"},
		DependentLookups: lookups,
	})
	_, warnings, err := g.BuildSpecSchema()
	require.NoError(t, err, "an undeclared lookup path is a warning, never a fatal error")

	var msgs []string
	for _, w := range warnings {
		msgs = append(msgs, w.Error())
	}
	return msgs
}

// findWarning returns the single warning containing needle, failing if there is none.
func findWarning(t *testing.T, msgs []string, needle string) string {
	t.Helper()
	var hit string
	for _, m := range msgs {
		if strings.Contains(m, needle) {
			hit = m
		}
	}
	require.NotEmpty(t, hit, "expected a warning containing %q, got %v", needle, msgs)
	return hit
}

// validLookup is declared entirely within what noParamDoc supports.
func validLookup() DependentLookup {
	return DependentLookup{
		Name: "parent", Method: "GET", Path: "/things/{name}", Select: ".x",
		Parameters: []LookupParameter{{InPath: "name", InCustomResource: "spec.name"}},
	}
}

// TestLookupDeclaringAnUndeclaredPathWarns covers the second half of #240.
//
// A lookup resolves its parameters against its document exactly as a verb does, so a path the document
// does not declare has no parameter schema and cannot be bound. Nothing says so until the lookup runs,
// and then it fails on every reconcile of every CR of the kind.
func TestLookupDeclaringAnUndeclaredPathWarns(t *testing.T) {
	t.Run("a fully declared lookup warns about nothing", func(t *testing.T) {
		for _, m := range lookupPathWarnings(t, validLookup()) {
			assert.NotContains(t, m, "dependent lookup",
				"a lookup the document fully supports must be silent: %s", m)
		}
	})

	t.Run("a path the document does not contain", func(t *testing.T) {
		l := validLookup()
		l.Path = "/repos/{owner}/{repo}/git/ref/heads/{default_branch}"
		hit := findWarning(t, lookupPathWarnings(t, l), "dependentLookups[parent].path")

		assert.Contains(t, hit, `"parent"`, "the message must name WHICH lookup")
		assert.Contains(t, hit, l.Path, "the message must quote the path that was not found")
		assert.Contains(t, hit, "every reconcile",
			"the message should say the consequence, since the symptom appears nowhere near the cause")
	})

	t.Run("a path that exists, with a method it does not define", func(t *testing.T) {
		l := validLookup()
		l.Path, l.Method = "/things", "GET" // the document declares only POST here
		hit := findWarning(t, lookupPathWarnings(t, l), "dependentLookups[parent].method")

		assert.Contains(t, hit, "GET")
		assert.Contains(t, hit, "/things")
		// Must be reported as a METHOD problem, not a path problem: the path is right and saying
		// otherwise sends the author to change the wrong field.
		for _, m := range lookupPathWarnings(t, l) {
			assert.NotContains(t, m, "dependentLookups[parent].path",
				"the path exists, so nothing may report it as missing: %s", m)
		}
	})

	t.Run("a parameter the operation does not declare", func(t *testing.T) {
		l := validLookup()
		l.Parameters = []LookupParameter{{InPath: "id", InCustomResource: "spec.name"}}
		hit := findWarning(t, lookupPathWarnings(t, l), "dependentLookups[parent].parameters")

		assert.Contains(t, hit, `"id"`, "the message must name the parameter that cannot be bound")
		assert.Contains(t, hit, "/things/{name}")
	})

	t.Run("a query parameter is checked the same way", func(t *testing.T) {
		l := validLookup()
		l.Parameters = []LookupParameter{{InQuery: "filter", InCustomResource: "spec.name"}}
		hit := findWarning(t, lookupPathWarnings(t, l), "dependentLookups[parent].parameters")
		assert.Contains(t, hit, `"filter"`)
	})

	// An unresolvable path must short-circuit, and the reason is stronger than tidiness: FindPath returns
	// a ZERO pathItem when it fails, and calling GetOperations() on that panics with a nil dereference.
	// Removing the `continue` after the path warning does not produce extra warnings -- it crashes
	// generation for the whole RestDefinition. Verified by mutation: the panic is raised from
	// checkLookupPaths and takes the test binary down.
	t.Run("an undeclared path short-circuits instead of touching the missing operation", func(t *testing.T) {
		l := validLookup()
		l.Path = "/nowhere"
		l.Parameters = []LookupParameter{
			{InPath: "a", InCustomResource: "spec.name"},
			{InPath: "b", InCustomResource: "spec.name"},
		}
		msgs := lookupPathWarnings(t, l)
		findWarning(t, msgs, "dependentLookups[parent].path")
		for _, m := range msgs {
			assert.NotContains(t, m, "dependentLookups[parent].parameters",
				"parameters must not be checked against an operation that was not found: %s", m)
		}
	})

	t.Run("several lookups are each reported", func(t *testing.T) {
		a, b := validLookup(), validLookup()
		a.Name, a.Path = "first", "/nowhere"
		b.Name, b.Method = "second", "DELETE"
		msgs := lookupPathWarnings(t, a, b)
		findWarning(t, msgs, "dependentLookups[first].path")
		findWarning(t, msgs, "dependentLookups[second].method")
	})

	// The method is matched case-insensitively, as a verb's is. An author writing `get` rather than `GET`
	// has made no mistake, and warning about it would train them to ignore these warnings.
	t.Run("a lowercase method is not a mistake", func(t *testing.T) {
		l := validLookup()
		l.Method = "get"
		for _, m := range lookupPathWarnings(t, l) {
			assert.NotContains(t, m, "dependent lookup", "method case must not matter: %s", m)
		}
	})

	// No lookups at all must not cost a document walk or produce anything.
	t.Run("no lookups, nothing reported", func(t *testing.T) {
		for _, m := range lookupPathWarnings(t) {
			assert.NotContains(t, m, "dependent lookup")
		}
	})
}
