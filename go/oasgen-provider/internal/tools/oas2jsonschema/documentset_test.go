package oas2jsonschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// stubDoc is an identifiable OASDocument; only identity matters to these tests.
type stubDoc struct {
	OASDocument
	id string
}

func TestDocumentSetResolvesEveryVerbToTheDefault(t *testing.T) {
	def := &stubDoc{id: "default"}
	s := NewDocumentSet(def)

	assert.Same(t, def, s.Default())
	for _, action := range []string{"create", "get", "findby", "update", "delete", "anything"} {
		assert.Same(t, def, s.For(action),
			"while per-verb oasPath is unimplemented every verb must resolve to spec.oasPath's document")
	}
}

// The override path is exercised now even though nothing constructs it yet, because the comparison it
// depends on is the thing most likely to be got wrong later: a set keyed "Create" against a verb
// declared "create" must not silently fall through to the default and build the CRD from the wrong
// document. ExtractSchemaForAction compares actions case-insensitively; this must agree with it.
func TestDocumentSetMatchesTheActionCaseInsensitively(t *testing.T) {
	def := &stubDoc{id: "default"}
	override := &stubDoc{id: "v1.1"}
	s := &DocumentSet{def: def, byVerb: map[string]OASDocument{"Create": override}}

	assert.Same(t, override, s.For("create"), "declared lower-case, keyed capitalised")
	assert.Same(t, override, s.For("CREATE"))
	assert.Same(t, override, s.For("Create"))
	assert.Same(t, def, s.For("get"), "a verb with no override still resolves to the default")
}

// A nil set must not panic: callers thread it through paths that can legitimately hold nothing yet.
func TestDocumentSetIsNilSafe(t *testing.T) {
	var s *DocumentSet
	assert.Nil(t, s.Default())
	assert.Nil(t, s.For("create"))
}
