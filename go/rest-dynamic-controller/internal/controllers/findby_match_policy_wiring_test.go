package restResources

import (
	"testing"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/hostpolicy"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// identifiersMatchPolicy is PER-VERB -- spec.resource.verbsDescription[i].identifiersMatchPolicy -- and
// configure copies it onto the client only from the findby verb. The identifiers themselves are
// resource-level (spec.resource.identifiers).
//
// This boundary had no test, and it is the one that decides whether #444's opt-out works at all. The
// client-package arms prove that a client carrying "AND" produces no subset report
// (TestANDPolicyWithAllIdentifiersMatchingProducesNoReport); these prove the string actually ARRIVES
// there from the CR. Either half alone is satisfied by a wiring that silently never applies: an operator
// who declared AND would keep OR's behaviour AND keep getting the report, with nothing to show why.
//
// cli.IdentifiersMatchPolicy is the contract boundary the two halves meet on, so both sides assert on it
// by that name and on the same literal value.

// policyCR is the reconcile's object. configure reads meta.IsVerbose(c.mg) and assigns cli.Resource from
// it, so it must be a real object: every production caller passes the one the informer handed the
// reconcile (exists.go:102, restResources.go:288/891/1117/1336), so nil never occurs there.
func policyCR() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "ogen.krateo.io/v1alpha1",
		"kind":       "Node",
		"metadata":   map[string]interface{}{"name": "child-b", "namespace": "default"},
		"spec":       map[string]interface{}{"chassis": "rack-1", "name": "child-b"},
	}}
}

func policyInfo(verbs ...getter.VerbsDescription) *getter.Info {
	return &getter.Info{
		URL: "configmap://ks/a/a.json",
		Resource: getter.Resource{
			Identifiers:      []string{"chassis", "name"},
			VerbsDescription: verbs,
		},
	}
}

// The AND opt-out declared on the findby verb must reach the matcher.
func TestConfigureCopiesTheFindByVerbsIdentifiersMatchPolicyOntoTheClient(t *testing.T) {
	cs := newClientSet(nil, policyInfo(
		getter.VerbsDescription{Action: "get", Method: "GET", Path: "/x/{id}"},
		getter.VerbsDescription{Action: "findby", Method: "GET", Path: "/x", IdentifiersMatchPolicy: "AND"},
	), policyCR(), false, hostpolicy.Config{}, logging.NewNopLogger())

	cli := &restclient.UnstructuredClient{}
	cs.configure(cli)

	require.Equal(t, "AND", cli.IdentifiersMatchPolicy,
		"the per-verb opt-out must arrive on the field the matcher reads, or declaring AND does nothing")
	assert.Equal(t, []string{"chassis", "name"}, cli.IdentifierFields,
		"identifiers are resource-level, unlike the policy")
}

// Case-insensitively the same verb. "FindBy" is how the action is spelled in apiaction, and a definition
// may use any casing.
func TestConfigureMatchesTheFindByVerbCaseInsensitively(t *testing.T) {
	cs := newClientSet(nil, policyInfo(
		getter.VerbsDescription{Action: "FindBy", Method: "GET", Path: "/x", IdentifiersMatchPolicy: "and"},
	), policyCR(), false, hostpolicy.Config{}, logging.NewNopLogger())

	cli := &restclient.UnstructuredClient{}
	cs.configure(cli)

	assert.Equal(t, "and", cli.IdentifiersMatchPolicy,
		"the policy is lowercased by the matcher, not here, so it must be carried through verbatim")
}

// THE INVERSE CONTROL. The field's contract is "to be set only for findby actions", so a policy declared
// on some other verb must NOT reach the client -- it would otherwise silently change findby's matching
// from a verb that has nothing to do with it. An empty policy leaves the field empty, which the matcher
// defaults to OR.
func TestConfigureIgnoresAnIdentifiersMatchPolicyOnANonFindByVerb(t *testing.T) {
	cs := newClientSet(nil, policyInfo(
		getter.VerbsDescription{Action: "get", Method: "GET", Path: "/x/{id}", IdentifiersMatchPolicy: "AND"},
		getter.VerbsDescription{Action: "findby", Method: "GET", Path: "/x"},
	), policyCR(), false, hostpolicy.Config{}, logging.NewNopLogger())

	cli := &restclient.UnstructuredClient{}
	cs.configure(cli)

	assert.Empty(t, cli.IdentifiersMatchPolicy,
		"AND on the get verb must not opt findby in -- and, read the other way, must not be mistaken for "+
			"an opt-out that was never declared")
}

// A findby verb declaring no policy leaves the field empty, which is the DEFAULTED-TO-OR path: the one
// #444 is about, and the one that still reports. Pinned so that "no declaration" cannot drift into
// something the matcher reads as AND, which would silence the report for every kind that never opted in.
func TestConfigureLeavesThePolicyEmptyWhenFindByDeclaresNone(t *testing.T) {
	cs := newClientSet(nil, policyInfo(
		getter.VerbsDescription{Action: "findby", Method: "GET", Path: "/x"},
	), policyCR(), false, hostpolicy.Config{}, logging.NewNopLogger())

	cli := &restclient.UnstructuredClient{}
	cs.configure(cli)

	assert.Empty(t, cli.IdentifiersMatchPolicy,
		"omitted means OR by default, which is exactly the configuration that lost data in #444")
}
