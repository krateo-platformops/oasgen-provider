package restResources

import (
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/stretchr/testify/assert"
)

func infoWithVerbs(defaultPath string, verbs ...getter.VerbsDescription) *getter.Info {
	return &getter.Info{URL: defaultPath, Resource: getter.Resource{VerbsDescription: verbs}}
}

// The motivating case from #220: Aruba CloudServer reads from compute-provider 1.0 and creates against
// 1.1, declared as a per-verb oasPath on the create verb only.
func TestOASPathForSelectsThePerVerbDocument(t *testing.T) {
	cs := newClientSet(nil, infoWithVerbs("configmap://ks/compute/compute.json",
		getter.VerbsDescription{Action: "findby", Method: "GET", Path: "/cloudServers"},
		getter.VerbsDescription{Action: "get", Method: "GET", Path: "/cloudServers/{id}"},
		getter.VerbsDescription{Action: "delete", Method: "DELETE", Path: "/cloudServers/{id}"},
		getter.VerbsDescription{Action: "create", Method: "POST", Path: "/cloudServers",
			OASPath: "configmap://ks/compute11/compute11.json"},
	), nil, false)

	assert.Equal(t, "configmap://ks/compute11/compute11.json", cs.oasPathFor("create"),
		"a verb declaring oasPath executes against ITS document")
	for _, a := range []string{"get", "findby", "delete"} {
		assert.Equal(t, "configmap://ks/compute/compute.json", cs.oasPathFor(a),
			"%s declares none, so it inherits spec.oasPath", a)
	}
}

// Delete verifies itself with the GET verb, so the action asked for is not the action of the entry
// point. This is the pairing a single client-per-entry-point gets wrong, and the reason For() takes the
// action being executed rather than the reconcile's action.
func TestOASPathForResolvesDeleteAndItsVerificationGetSeparately(t *testing.T) {
	cs := newClientSet(nil, infoWithVerbs("configmap://ks/a/a.json",
		getter.VerbsDescription{Action: "get", Method: "GET", Path: "/x/{id}"},
		getter.VerbsDescription{Action: "delete", Method: "DELETE", Path: "/x/{id}",
			OASPath: "configmap://ks/b/b.json"},
	), nil, false)

	assert.Equal(t, "configmap://ks/b/b.json", cs.oasPathFor("delete"))
	assert.Equal(t, "configmap://ks/a/a.json", cs.oasPathFor("get"),
		"the verification GET must address the GET verb's document, not the delete verb's")
}

func TestOASPathForIsCaseInsensitiveAndFallsBack(t *testing.T) {
	cs := newClientSet(nil, infoWithVerbs("configmap://ks/a/a.json",
		getter.VerbsDescription{Action: "GET", Method: "GET", Path: "/x/{id}", OASPath: "configmap://ks/c/c.json"},
	), nil, false)
	assert.Equal(t, "configmap://ks/c/c.json", cs.oasPathFor("get"), "action matching is case-insensitive")
	assert.Equal(t, "configmap://ks/a/a.json", cs.oasPathFor("update"), "an undeclared verb inherits spec.oasPath")

	// An empty per-verb oasPath is not a declaration; it must not shadow the definition's.
	cs2 := newClientSet(nil, infoWithVerbs("configmap://ks/a/a.json",
		getter.VerbsDescription{Action: "get", Method: "GET", Path: "/x/{id}", OASPath: ""},
	), nil, false)
	assert.Equal(t, "configmap://ks/a/a.json", cs2.oasPathFor("get"))
}

func TestOASPathForNilInfo(t *testing.T) {
	assert.Equal(t, "", newClientSet(nil, nil, nil, false).oasPathFor("get"))
}
