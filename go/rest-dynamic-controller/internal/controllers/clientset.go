package restResources

import (
	"context"
	"fmt"
	"strings"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/unstructured-runtime/pkg/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// clientSet resolves, and caches, the client for the OAS document each VERB is executed against.
//
// A resource's lifecycle can span two documents: Aruba CloudServer reads from compute-provider 1.0 and
// creates against 1.1. The generator has resolved `oasPath` per verb since 0.25.0, but this side held a
// single Info.URL and executed every verb against it, so the verb needing the second document failed
// with "operation not found: POST" -- the feature worked at no released version (#220).
//
// Keyed by PATH rather than by action, for two reasons. Verbs that share a document then share one
// client, and BuildClient is not cheap: it downloads the document into a private temp directory and
// parses it, once per call. And a reconcile that touches three verbs in one document should pay for one
// fetch, not three.
//
// Not safe for concurrent use. One instance belongs to one reconcile, which is how it is used: the
// controller runs REST_CONTROLLER_WORKERS of them, each with its own set.
type clientSet struct {
	kube dynamic.Interface
	info *getter.Info
	// mg and prettyJSON are the per-reconcile configuration every client it hands out must carry. They
	// are held here rather than applied by callers because a client built for a SECOND document would
	// otherwise be returned unconfigured -- and the field that matters most is SetAuth, without which the
	// request simply goes out unauthenticated. A factory that can hand back an unauthenticated client is
	// the wrong factory.
	mg         *unstructured.Unstructured
	prettyJSON bool
	byPath     map[string]restclient.UnstructuredClientInterface
}

func newClientSet(kube dynamic.Interface, info *getter.Info, mg *unstructured.Unstructured, prettyJSON bool) *clientSet {
	return &clientSet{
		kube:       kube,
		info:       info,
		mg:         mg,
		prettyJSON: prettyJSON,
		byPath:     map[string]restclient.UnstructuredClientInterface{},
	}
}

// configure applies the per-reconcile settings to a freshly built client.
//
// This is the UNION of what the four reconcile entry points used to apply individually: Observe set
// Debug, PrettyJSON, Resource, SetAuth, IdentifierFields and IdentifiersMatchPolicy, while Create,
// Update and Delete set only the first two plus SetAuth. Nothing on a mutating path reaches findby
// today, so the three they omitted were unused there rather than wrong -- but four copies of a
// configuration block that disagree is how a second client ends up missing the one field it needed.
// One place, applied to every client, and the extra fields are inert where findby is not used.
func (c *clientSet) configure(cli *restclient.UnstructuredClient) {
	cli.Debug = meta.IsVerbose(c.mg)
	cli.PrettyJSON = c.prettyJSON
	cli.Resource = c.mg
	cli.SetAuth = c.info.SetAuth
	cli.IdentifierFields = c.info.Resource.Identifiers
	for _, verb := range c.info.Resource.VerbsDescription {
		if strings.EqualFold(verb.Action, string(apiaction.FindBy)) && verb.IdentifiersMatchPolicy != "" {
			cli.IdentifiersMatchPolicy = verb.IdentifiersMatchPolicy
		}
	}
}

// oasPathFor returns the document an action is executed against: the verb's own oasPath when it
// declares one, otherwise the definition-level spec.oasPath.
//
// A verb that declares no oasPath inherits the definition's, which is what keeps every existing
// RestDefinition working unchanged -- the overwhelmingly common case is one document for everything.
func (c *clientSet) oasPathFor(action string) string {
	if c.info == nil {
		return ""
	}
	for _, v := range c.info.Resource.VerbsDescription {
		if strings.EqualFold(v.Action, action) && v.OASPath != "" {
			return v.OASPath
		}
	}
	return c.info.URL
}

// For returns the client for the document this action executes against, building it on first use.
//
// Callers pass the action they are about to execute, NOT the action of the reconcile they are part of.
// That distinction is the bug this type exists to prevent: Delete verifies itself with the GET verb, and
// the async path re-reads with the GET verb, so both run an action their entry point did not name. A
// client resolved once per entry point would address the wrong document for exactly those reads.
func (c *clientSet) For(ctx context.Context, action string) (restclient.UnstructuredClientInterface, error) {
	path := c.oasPathFor(action)
	if path == "" {
		return nil, fmt.Errorf("no OAS document is declared for action %q and the definition carries no spec.oasPath", action)
	}
	if cli, ok := c.byPath[path]; ok {
		return cli, nil
	}
	cli, err := restclient.BuildClient(ctx, c.kube, path)
	if err != nil {
		return nil, fmt.Errorf("building the client for action %q against %q: %w", action, path, err)
	}
	c.configure(cli)
	c.byPath[path] = cli
	return cli, nil
}

// newClientSetWithClients builds a set backed by already-made clients, keyed by document path. It exists
// for tests: the production constructor reaches the cluster through BuildClient, which a unit test has no
// business doing. Keyed by path rather than action so one canned client can serve every verb sharing a
// document, exactly as the real cache does.
func newClientSetWithClients(info *getter.Info, byPath map[string]restclient.UnstructuredClientInterface) *clientSet {
	return &clientSet{info: info, byPath: byPath}
}
