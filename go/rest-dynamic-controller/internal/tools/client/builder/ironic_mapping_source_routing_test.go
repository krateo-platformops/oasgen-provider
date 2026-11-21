package builder

// Does findby's reachability depend on WHERE the get's path params resolve from?
//
// Observe consults `findby` only when IsResourceKnown is false. IsResourceKnown builds
// the GET and asks whether it is addressable, and it has NO observation counter -- it is
// a pure function of the CURRENT spec. So there is no "first reconcile" during which
// findby gets a turn: if the get's path params all resolve at reconcile 1, findby is
// structurally unreachable and its `queries`, `responseTransform` and
// `identifiersMatchPolicy` are configuration that can never execute.
//
// BuildCallConfig's writer order, numbered in its own comments:
//
//	3. processFields(specFields)    <- auto-populates path params from spec BY NAME
//	4. processFields(statusFields)
//	5. applyRequestFieldMapping     <- wins where its own source resolves
//
// So a param can be filled by TWO different routes, and which one fired is invisible in
// the outcome. These arms separate them.
//
// WHAT THE EXISTING COVERAGE DOES AND DOES NOT REACH. `TestIsResourceKnown` in
// builder_test.go has five cases: one happy path and four nil/error guards. Its get
// declares `Path: "/test"` -- no `{param}` template at all -- no case carries a
// `requestFieldMapping`, and the happy path runs through `mockUnstructuredClient`, whose
// ValidateRequest returns `validateError` unconditionally and never inspects `params`.
// So that table covers IsResourceKnown's error handling and nil guards, and cannot
// distinguish how a path parameter came to be filled.
// `TestIsResourceKnownSeparatesBrokenFromUnknown` in request_jq_test.go does draw a real
// distinction, between a broken JQ expression and an unknown identity, which is a
// different axis from this one.
//
// THE EXPERIMENT IS ARM M vs ARM S-PRE. Identical verb sets, identical single path
// parameter, no spec field named `uuid` in either arm -- so step 3 cannot be what fills
// it -- differing ONLY in whether the mapping's `inCustomResource` is a `spec.*` or a
// `status.*` path. If routing did not depend on that, the two arms would agree.
//
//	ARM M       mapping=spec.name                   status=none   param="blade01"      known=TRUE
//	ARM S-PRE   mapping=status.uuid                 status=none   param=""             known=false
//	ARM S-POST  mapping=status.uuid                 status=SET    param="6b3f1c2e-..." known=TRUE
//	DISCRIM     mapping=spec.nothing_supplies_this                param=""             known=false
//
// ARM S-POST is what stops ARM S-PRE being over-read: without it, S-PRE's `false` is
// indistinguishable from "a `status.*` mapping source never works". With it, a
// status-mapped get is reachable only on reconciles BEFORE its status carries the value
// -- so findby-reachability is not a property of the kind, and a screen that latches it
// is wrong half the time.
//
// WHY A CUSTOM CLIENT. mockUnstructuredClient.ValidateRequest ignores `params`, so
// IsResourceKnown through it is a CONSTANT with respect to this question. The arms would
// all pass whether or not the mechanism worked. mappingSourceClient checks that every
// declared path parameter is present and non-empty, which is what makes the arms able to
// disagree.
//
// MEASURED DOWNSTREAM BEFORE BEING PROPOSED HERE, identically at oasgen 0.61.0 and
// 0.80.0, on the seven Ironic RestDefinitions in
// github.com/krateo-blueprints/openstack-ironic-operator-kog. Five of those seven have a
// findby that cannot fire; the two that can do so for different reasons -- one has no
// `get` verb at all, and one (Port) resolves its get from `status.uuid`. The
// generalisation "a path param matching a spec field BY NAME" accounted for only two of
// the five, which is why step 5 is tested here separately rather than assumed equivalent.
//
// `-count=1` is not decoration on arms like these: a fixture a test READS is not a build
// input, so two arms that genuinely differ can both replay one cached result.
//
// THE ARMS MUST NOT ALL AGREE. ARM M and ARM S-POST are true; ARM S-PRE and the
// discriminator are false, each for a different stated reason. A run in which they agree
// is vacuous and reports nothing.

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/text"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// mappingSourceClient checks params the way the real client does for this question:
// every path parameter the operation declares must be present AND non-empty.
//
// Named distinctly from powerParamCheckingClient and the provision file's
// paramCheckingClient on purpose: all three files get copied into the SAME package, and
// a shared helper would make any one of them un-runnable alone.
//
// Why a custom client at all: mockUnstructuredClient.ValidateRequest returns
// m.validateError unconditionally and never looks at `params`, so IsResourceKnown
// through it is a CONSTANT. The provision file was fooled by exactly that and reported
// every arm and its control as true.
type mappingSourceClient struct {
	mockUnstructuredClient
	required []string
}

func (c *mappingSourceClient) ValidateRequest(method, path string, params, query, headers, cookies map[string]string) error {
	for _, r := range c.required {
		if v, ok := params[r]; !ok || v == "" {
			return fmt.Errorf("path parameter %q is missing or empty", r)
		}
	}
	return nil
}

func (c *mappingSourceClient) Validate(req *http.Request) (bool, []error) { return true, nil }

// uuidKeyedInfo is the shape the four spec-mapped kinds and Port share: a findby on the
// collection, plus a get on /{uuid} whose only path param is resolved by a mapping.
// `source` is the single thing under test -- "spec.name" for the four, "status.uuid" for
// Port. Nothing declares a spec field NAMED uuid, so step 3 can never help here and the
// mapping is the only route to a filled param.
//
// Built fresh per call rather than shared: BuildCallConfig may rewrite the mapping it is
// handed (stripResolverSources removes inCustomResource), so a reused Info would make
// each arm depend on which ran first.
func uuidKeyedInfo(collection, item, source string) *getter.Info {
	mapping := []getter.RequestFieldMappingItem{
		{InPath: "uuid", InCustomResource: source},
	}
	return &getter.Info{Resource: getter.Resource{
		Identifiers: []string{"name"},
		VerbsDescription: []getter.VerbsDescription{
			{Action: "findby", Method: "GET", Path: collection},
			{Action: "get", Method: "GET", Path: item, RequestFieldMapping: mapping},
			{Action: "create", Method: "POST", Path: collection},
			{Action: "delete", Method: "DELETE", Path: item, RequestFieldMapping: mapping},
		},
	}}
}

func uuidKeyedClient(item string) *mappingSourceClient {
	return &mappingSourceClient{
		mockUnstructuredClient: mockUnstructuredClient{
			requestedParams: map[string]text.StringSet{
				item: text.NewStringSet("uuid"),
			},
		},
		required: []string{"uuid"},
	}
}

func TestIronicMappingSourceRouting(t *testing.T) {
	const nodesColl, nodesItem = "/v1/nodes", "/v1/nodes/{uuid}"
	const portsColl, portsItem = "/v1/ports", "/v1/ports/{uuid}"

	// resolve returns the path parameter BuildCallConfig actually produced -- the fact
	// every verdict below follows from. Reported alongside `known` in every arm so a
	// reader can see the mechanism, not just the routing decision.
	resolve := func(cli *mappingSourceClient, info *getter.Info, mg *unstructured.Unstructured) (string, bool) {
		apiCall, callInfo, err := APICallBuilder(cli, info, apiaction.Get)
		if apiCall == nil || err != nil {
			t.Fatalf("APICallBuilder refused the get verb: %v", err)
		}
		cfg := BuildCallConfig(context.Background(), callInfo, mg, info.ConfigurationSpec, nil)
		if cfg == nil {
			t.Fatalf("BuildCallConfig returned nil")
		}
		if cfg.BuildErr != nil {
			return "", false
		}
		v, ok := cfg.Parameters["uuid"]
		return v, ok && v != ""
	}

	t.Run("ARM M: mapped from spec.name, fresh CR -- get is buildable, findby BYPASSED", func(t *testing.T) {
		// BaremetalNode as charts/baremetal-host writes it on the first reconcile:
		// spec.name set, NO status. Note there is no spec field named `uuid`, so step 3
		// contributes nothing and step 5 is doing all the work.
		fresh := &unstructured.Unstructured{Object: map[string]interface{}{
			"spec": map[string]interface{}{"name": "blade01"},
		}}
		cli := uuidKeyedClient(nodesItem)

		gotParam, filled := resolve(cli, uuidKeyedInfo(nodesColl, nodesItem, "spec.name"), fresh)
		assert.Equal(t, "blade01", gotParam,
			"step 5 resolves {uuid} from spec.name; nothing is named uuid in the spec, so "+
				"step 3's by-name auto-populate cannot be what filled it")
		assert.True(t, filled)

		known, err := IsResourceKnown(context.Background(), cli,
			uuidKeyedInfo(nodesColl, nodesItem, "spec.name"), fresh)
		assert.NoError(t, err)
		assert.True(t, known,
			"THE CORRECTION, and it is the reason this file exists: KNOWN on the FIRST "+
				"reconcile through the MAPPING, with no same-named spec field anywhere. So "+
				"'findby is unreachable when a path param matches a spec field by name' "+
				"was too narrow -- it misses this, which is four of this repo's seven "+
				"kinds. The condition is that the get's params are RESOLVABLE AT RECONCILE "+
				"1, by name at step 3 OR by a mapping whose source is a spec.* path at "+
				"step 5."+
				"\n\nWHAT FOLLOWS BUT IS NOT MEASURED HERE: with the get answering, "+
				"findby's responseTransform and identifiersMatchPolicy are on a path that "+
				"cannot execute, and create is reached only when the GET 404s. Those are "+
				"inferences from the routing. This harness never performs the GET, so do "+
				"not cite this arm as evidence about what the server answers.")
		t.Logf("ARM M       mapping=spec.name    status=none   param=%q  known=%v", gotParam, known)
	})

	t.Run("ARM S-PRE: mapped from status.uuid, fresh CR -- NOT known, findby IS the path", func(t *testing.T) {
		// Port on its first reconcile: spec.address set, status absent entirely.
		fresh := &unstructured.Unstructured{Object: map[string]interface{}{
			"spec": map[string]interface{}{"address": "aa:bb:cc:dd:ee:01"},
		}}
		cli := uuidKeyedClient(portsItem)

		gotParam, filled := resolve(cli, uuidKeyedInfo(portsColl, portsItem, "status.uuid"), fresh)
		assert.Empty(t, gotParam,
			"the mapping's source status.uuid does not resolve on a CR with no status, "+
				"and no spec field is named uuid either")
		assert.False(t, filled)

		known, err := IsResourceKnown(context.Background(), cli,
			uuidKeyedInfo(portsColl, portsItem, "status.uuid"), fresh)
		assert.NoError(t, err)
		assert.False(t, known,
			"DISCRIMINATES ARM M, and this pair IS the experiment. Same verb set, same "+
				"single path param, no same-named spec field in either arm -- the ONLY "+
				"difference is whether the mapping reads spec.* or status.*. So findby's "+
				"reachability is a function of WHERE the param resolves from, not of "+
				"whether a mapping exists. Port is the one kind of this repo's seven whose "+
				"findby genuinely fires, and this is why.")
		t.Logf("ARM S-PRE   mapping=status.uuid  status=none   param=%q  known=%v", gotParam, known)
	})

	t.Run("ARM S-POST: mapped from status.uuid, after create -- known, findby retired", func(t *testing.T) {
		// The same Port once rdc has published status.uuid. This arm is what makes
		// ARM S-PRE a statement about the ABSENT status rather than about status.*
		// mappings being unusable in general -- without it, S-PRE's false is
		// indistinguishable from a mapping source that never works.
		observed := &unstructured.Unstructured{Object: map[string]interface{}{
			"spec":   map[string]interface{}{"address": "aa:bb:cc:dd:ee:01"},
			"status": map[string]interface{}{"uuid": "6b3f1c2e-0000-4a11-9f00-aaaaaaaaaaaa"},
		}}
		cli := uuidKeyedClient(portsItem)

		gotParam, filled := resolve(cli, uuidKeyedInfo(portsColl, portsItem, "status.uuid"), observed)
		assert.Equal(t, "6b3f1c2e-0000-4a11-9f00-aaaaaaaaaaaa", gotParam,
			"step 4/5 resolve it once status.uuid exists")
		assert.True(t, filled)

		known, err := IsResourceKnown(context.Background(), cli,
			uuidKeyedInfo(portsColl, portsItem, "status.uuid"), observed)
		assert.NoError(t, err)
		assert.True(t, known,
			"So Port's findby is reachable only on reconciles BEFORE its status carries a "+
				"uuid -- not permanently live. 'findby fires for Port' is true of the "+
				"first reconcile and false afterwards, and a screen that reports it as a "+
				"property of the kind is wrong half the time.")
		t.Logf("ARM S-POST  mapping=status.uuid  status=SET    param=%q  known=%v", gotParam, known)
	})
}

// THE DISCRIMINATOR. A path param that NOTHING can supply -- no same-named spec field,
// and a mapping source that resolves nowhere on this CR.
//
// Without it, ARM S-PRE's `false` means only "something was missing". With it, S-PRE
// means "this specific source did not resolve", because here is what a genuinely
// unsuppliable param looks like through the same code path. If this ever comes back
// true, ValidateRequest is not checking params and every arm above is vacuous.
//
// This is the arm a future reader will be tempted to delete as redundant. Keep it.
func TestIronicMappingSourceDiscriminator(t *testing.T) {
	const coll, item = "/v1/nodes", "/v1/nodes/{uuid}"
	mg := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{"name": "blade01"},
	}}
	cli := uuidKeyedClient(item)

	info := uuidKeyedInfo(coll, item, "spec.nothing_supplies_this")
	apiCall, callInfo, err := APICallBuilder(cli, info, apiaction.Get)
	if apiCall == nil || err != nil {
		t.Fatalf("APICallBuilder refused the get verb: %v", err)
	}
	cfg := BuildCallConfig(context.Background(), callInfo, mg, info.ConfigurationSpec, nil)
	if cfg != nil && cfg.BuildErr == nil {
		assert.Empty(t, cfg.Parameters["uuid"],
			"a mapping whose source resolves nowhere must leave the param empty")
	}

	known, err := IsResourceKnown(context.Background(), cli,
		uuidKeyedInfo(coll, item, "spec.nothing_supplies_this"), mg)
	assert.NoError(t, err)
	assert.False(t, known,
		"CONTROL: an unsuppliable param must read NOT known. If this is true, the client "+
			"is not checking params and ARM M's `true` means nothing.")
	t.Logf("DISCRIM     mapping=spec.nothing_supplies_this  known=%v", known)
}
