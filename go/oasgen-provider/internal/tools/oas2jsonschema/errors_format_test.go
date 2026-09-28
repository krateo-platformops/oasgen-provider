package oas2jsonschema

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSchemaGenerationErrorOmitsAnEmptyLocation covers a defect found by reading the output on a live
// cluster, not by reading the code.
//
// Path is optional, and in practice usually absent: most emitters in this package set only Code and
// Message. The unconditional "generation error at %s: %s" then rendered "generation error at : ...",
// which reads as a missing format argument. That is a poor way to introduce a warning whose entire job
// is to be believed — a reader's first conclusion is that the warning is broken, not the RestDefinition.
func TestSchemaGenerationErrorOmitsAnEmptyLocation(t *testing.T) {
	withoutPath := SchemaGenerationError{
		Code:    CodeStatusFieldNotFound,
		Message: "status field 'metadata.tags' not found in response, defaulting to string",
	}
	got := withoutPath.Error()

	assert.NotContains(t, got, "at :", "an empty location must be omitted, not printed")
	assert.Equal(t, "generation error: status field 'metadata.tags' not found in response, defaulting to string", got)

	withPath := SchemaGenerationError{
		Path:    "identifiers.metadata.name",
		Code:    CodeIdentifierNotResolvable,
		Message: "identifier is not present in the response schema",
	}
	assert.Equal(t,
		"generation error at identifiers.metadata.name: identifier is not present in the response schema",
		withPath.Error(),
		"a location that exists must still be shown")
}

// The status-field warnings now carry a location, because the person reading them is holding a
// RestDefinition and needs the coordinate they can act on.
func TestStatusFieldWarningsCarryALocation(t *testing.T) {
	e := SchemaGenerationError{
		Path:    statusFieldPath("metadata.tags"),
		Code:    CodeStatusFieldNotFound,
		Message: "status field 'metadata.tags' not found in response, defaulting to string",
	}
	assert.True(t, strings.HasPrefix(e.Error(), "generation error at additionalStatusFields.metadata.tags:"),
		"got %q", e.Error())
}
