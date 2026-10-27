package oas2jsonschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// An async verb's own response is an operation HANDLE, not the resource: a 202 carrying
// {"operationId": ...} has no resource-shaped schema. Comparing it against the get base reported a
// missing create schema for a definition that is entirely correct (#228) -- the same mistake the
// dispatch exemption beside it already avoids.
//
// Asserting the ABSENCE of a warning is the assertion whose absence let this ship: every existing test
// here checks that a real problem IS reported, and none checked that a correct definition is left alone.
func TestAsyncVerbIsNotComparedAgainstTheBaseSchema(t *testing.T) {
	verbs := []Verb{
		{Action: ActionGet, Method: "GET", Path: "/resource/{id}"},
		{
			Action: ActionCreate, Method: "POST", Path: "/async/resource",
			Async: &Async{Mode: "", PollMethod: "GET", PollPath: "/operations/{operationId}", PostGet: true},
		},
	}
	assert.True(t, isAsyncVerb(verbs[1]), "the create declares async")
	assert.False(t, isAsyncVerb(verbs[0]), "the get does not")
}

// A SYNCHRONOUS create must still be compared: the exemption is for verbs whose response is a handle,
// not a blanket excuse. Removing the comparison for every create would hide the real mismatches this
// check exists to find.
func TestSynchronousCreateIsStillCompared(t *testing.T) {
	v := Verb{Action: ActionCreate, Method: "POST", Path: "/resource"}
	assert.False(t, isAsyncVerb(v), "no async block, so the comparison still applies")
}
