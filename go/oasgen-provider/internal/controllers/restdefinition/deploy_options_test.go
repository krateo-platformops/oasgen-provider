package restdefinition

import (
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
)

// TestObserveAndWritePathsRenderIdenticalOptions is the regression test for the 0.28.0 readiness loop.
//
// Observe renders what WOULD be deployed and compares its digest against the one Create/Update stored.
// The two paths must therefore agree on every field that feeds the render -- dryRun is the only
// legitimate difference, because the read path must not mutate anything.
//
// When they disagreed (LabelSelector set on the write paths, missing on the read path) the digests could
// never match: Observe returned ResourceUpToDate=false on every pass, Update re-rendered and stored the
// value it already had, and every RestDefinition sat Ready=False/Creating indefinitely. Nothing was
// actually broken -- CRDs Established, controllers running, no object rewritten -- which is what made it
// a permanently false red rather than a visible outage.
func TestObserveAndWritePathsRenderIdenticalOptions(t *testing.T) {
	e := &external{log: logging.NewNopLogger(), rec: record.NewFakeRecorder(10)}
	cr := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "pullrequest", Namespace: "demo"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			ResourceGroup: "github.krateo.io",
			Resource:      definitionv1alpha1.Resource{Kind: "PullRequest"},
		},
	}
	gvk := schema.GroupVersionKind{Group: "github.krateo.io", Version: "v1-0-28", Kind: "PullRequest"}
	gvr := schema.GroupVersionResource{Group: gvk.Group, Version: gvk.Version, Resource: "pullrequests"}
	cfg := schema.GroupVersionResource{Group: gvk.Group, Version: gvk.Version, Resource: "pullrequestconfigurations"}

	read := e.deployOptions(cr, gvk, gvr, cfg, true)
	write := e.deployOptions(cr, gvk, gvr, cfg, false)

	assert.True(t, read.DryRunServer, "the read path must not mutate anything")
	assert.False(t, write.DryRunServer)

	// Normalise the one field that is allowed to differ, then require everything else to be equal.
	//
	// Log is nilled on both first: it holds a func value, and reflect.DeepEqual reports two non-nil funcs
	// as unequal regardless of what they point at, so leaving it in would make this assertion fail for a
	// reason that has nothing to do with what it is testing -- and a test that always fails gets deleted,
	// not fixed.
	read.DryRunServer = write.DryRunServer
	read.Log, write.Log = nil, nil
	assert.Equal(t, write, read,
		"Observe and Create/Update must render from identical options; any field set on one and not the "+
			"other makes their digests permanently disagree and the resource never reports Available")
}

// The selector is what the divergence was about, so pin that it reaches the options at all -- an empty
// one would mean the controller watches everything, silently undoing version scoping.
func TestDeployOptionsCarryTheVersionSelector(t *testing.T) {
	e := &external{log: logging.NewNopLogger(), rec: record.NewFakeRecorder(10)}
	cr := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "demo"},
		Spec:       definitionv1alpha1.RestDefinitionSpec{ResourceGroup: "github.krateo.io"},
	}
	gvk := schema.GroupVersionKind{Group: "github.krateo.io", Version: "v1-0-28", Kind: "X"}

	opts := e.deployOptions(cr, gvk, schema.GroupVersionResource{}, schema.GroupVersionResource{}, false)
	assert.Equal(t, "krateo.io/oas-version=v1-0-28", opts.LabelSelector)
}
