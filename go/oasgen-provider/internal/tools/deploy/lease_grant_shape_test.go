//go:build unit

package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// RBAC CANNOT AUTHORIZE `create` BY resourceNames, AND A RULE THAT TRIES GRANTS NO CREATE AT ALL.
//
// The name of an object being created is in the REQUEST BODY, not in the authorization attributes, so the
// authorizer has nothing to match a resourceNames list against. The rule stays valid, admission accepts it,
// the apiserver stores it, and the create is denied at request time. There is no error anywhere in the
// install to read.
//
// THAT SHIPPED. #800 wrote one rule with resourceNames and verbs [get, watch, create, update], whose own
// comment said "`create` is needed for first acquisition". Both halves true; together they do not work.
// On a fresh install the Lease does not exist, controller-runtime's LeaseLock does Get-then-CREATE
// (client-go tools/leaderelection/resourcelock/leaselock.go:42 and :57), the create is forbidden, and
// controller-runtime EXITS -- every generated controller in a crash loop, N failures rather than one.
//
// WHY A SHAPE TEST AND NOT THE PARITY TEST. TestRBACFixturesMatchTheShippedAssets asserts that the three
// copies AGREE. All three agreed on the broken shape, and would agree on it again: parity cannot see a
// defect that is present everywhere. This test asserts the shape itself, which is the property parity
// structurally cannot express.
//
// WHAT IT CANNOT DO. It reads a template, so it proves the manifest asks for the right thing -- not that
// the apiserver grants it. Only a cluster can say that. The two are different claims and this is the
// cheaper one.
func TestTheLeaseCreateGrantIsNotNameScoped(t *testing.T) {
	// The shipped asset is the one that reaches a cluster; the fixtures are held to it by the parity test.
	path := filepath.Join("..", "..", "..", "..", "..", "helm", "oasgen-provider", "assets", "rdc", "rbac", "role.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the shipped Role asset must be readable at %s", path)

	// Two template passes stand between this file and a cluster: helm's, then the provider's. Both leave
	// `{{ ... }}` actions that are not YAML. Neutralising them is enough to read the STRUCTURE, which is
	// all this test asserts -- the values are the parity test's and the cluster's business.
	neutralised := regexp.MustCompile(`\{\{[^{}]*\}\}`).ReplaceAll(raw, []byte("PLACEHOLDER"))
	for i := 0; i < 3 && regexp.MustCompile(`[{}]`).Match(neutralised); i++ {
		neutralised = regexp.MustCompile(`\{\{[^{}]*\}\}`).ReplaceAll(neutralised, []byte("PLACEHOLDER"))
		neutralised = regexp.MustCompile("`").ReplaceAll(neutralised, []byte(""))
	}

	var role struct {
		Kind  string `json:"kind"`
		Rules []struct {
			APIGroups     []string `json:"apiGroups"`
			Resources     []string `json:"resources"`
			ResourceNames []string `json:"resourceNames"`
			Verbs         []string `json:"verbs"`
		} `json:"rules"`
	}
	require.NoError(t, yaml.Unmarshal(neutralised, &role), "the neutralised Role must parse; got:\n%s", neutralised)
	require.Equal(t, "Role", role.Kind)

	// A CONTROL ON THE INSTRUMENT. If the parse silently produced nothing, every assertion below would
	// pass over an empty slice and this test would report success about a file it never read.
	require.NotEmpty(t, role.Rules, "no rules parsed -- the instrument failed, not the asset")

	var sawLeaseCreate, sawLeasePin bool
	for _, r := range role.Rules {
		isLeases := false
		for _, res := range r.Resources {
			if res == "leases" {
				isLeases = true
			}
		}
		if !isLeases {
			continue
		}
		for _, v := range r.Verbs {
			if v == "create" {
				sawLeaseCreate = true
				require.Empty(t, r.ResourceNames,
					"a leases rule grants `create` while carrying resourceNames %v -- RBAC cannot authorize "+
						"create by name, so this grants NO create and every generated controller will fail "+
						"first acquisition and exit. Put `create` in its own rule with no resourceNames.",
					r.ResourceNames)
			}
			// `watch` with resourceNames needs a metadata.name field selector to work at all, and
			// LeaseLock does get/create/update only -- so granting it would imply a mechanism that is
			// not in use.
			require.NotEqual(t, "watch", v, "leases `watch` is not used by LeaseLock; granting it implies a mechanism that does not exist")
			require.NotEqual(t, "delete", v, "controller-runtime releases a Lease by clearing its holder, never by deleting it")
		}
		if len(r.ResourceNames) > 0 {
			sawLeasePin = true
			require.ElementsMatch(t, []string{"get", "update"}, r.Verbs,
				"the name-pinned leases rule must be exactly get+update: get/update are renewal and are "+
					"the pair that could take over another controller's Lease, so they are what pinning "+
					"must cover")
		}
	}

	// THE GRANT MUST EXIST AT ALL. Without these two the loop above is vacuous: a Role with no leases
	// rules passes every assertion inside it. This is the difference between "the shape is right" and
	// "there is a shape".
	require.True(t, sawLeaseCreate, "no leases rule grants `create` -- first acquisition on a fresh install would be denied")
	require.True(t, sawLeasePin, "no leases rule is name-pinned -- any controller could take any other's Lease")
}
