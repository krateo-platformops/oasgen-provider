//go:build unit

package restdefinition

import (
	"context"
	"os"
	"strings"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/deploy"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/objects"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WHY THIS FILE EXISTS (#541). Every other generator-build test asserts on the PREDICATE or on the Debug
// message Observe logs, and the defect was neither: the predicate was correct and Observe never reached
// it. Create was entered with the CRD already present, skipped generation because of that, and recorded
// the running build anyway -- so the schema stayed as an older build had rendered it under a status field
// asserting otherwise. Then, because the stamp now equalled the running build, the drift check could
// never fire again.
//
// So these tests drive the entry points provider-runtime calls, with the controller Deployment ABSENT,
// which is the only state that routes an upgrade into Create. They assert on the CRD IN THE CLUSTER and
// on the PERSISTED status -- never on whether a helper was called, because "generateAndApplyCRDs was not
// invoked" is a claim about the code and "the schema is stale" is a claim about the cluster.

// markTheGeneratedCRD writes a property into the generated CRD's served schema that no render can
// produce, and returns its name. A re-render replaces that schema from the OAS documents, so the marker's
// ABSENCE afterwards is the observable for "the CRD was regenerated".
//
// AN INJECTED MARKER RATHER THAN A REMOVED PROPERTY, and the first two attempts are why. Removing a real
// property needs the fixture's CRD to have been rendered in the first place -- and the harness seeds a
// hand-built, schema-less stub, so on the pre-fix code nothing ever renders it and the SETUP failed
// instead of the assertion. Routing the first Create down the !crdOk branch to get a real render then
// broke the harness differently: that branch returns before deploying the controller, so rollOut found no
// Deployment. The marker needs no render to exist, so the fixture is identical under both codepaths and
// the arms isolate the branch under test.
func (l *secretRefLoop) markTheGeneratedCRD() string {
	l.t.Helper()
	const marker = "zzNotFromTheOasDocument"
	crd := l.fetchGeneratedCRD()
	require.NotEmpty(l.t, crd.Spec.Versions, "the fixture CRD must serve a version")

	v := &crd.Spec.Versions[0]
	if v.Schema == nil {
		v.Schema = &apiextensionsv1.CustomResourceValidation{}
	}
	if v.Schema.OpenAPIV3Schema == nil {
		v.Schema.OpenAPIV3Schema = &apiextensionsv1.JSONSchemaProps{Type: "object"}
	}
	if v.Schema.OpenAPIV3Schema.Properties == nil {
		v.Schema.OpenAPIV3Schema.Properties = map[string]apiextensionsv1.JSONSchemaProps{}
	}
	spec := v.Schema.OpenAPIV3Schema.Properties["spec"]
	if spec.Properties == nil {
		spec.Type = "object"
		spec.Properties = map[string]apiextensionsv1.JSONSchemaProps{}
	}
	spec.Properties[marker] = apiextensionsv1.JSONSchemaProps{Type: "string"}
	v.Schema.OpenAPIV3Schema.Properties["spec"] = spec
	require.NoError(l.t, l.e.kube.Update(context.Background(), crd))

	// CONTROL: the marker really is there. Without this, a silently failed Update would make "the marker
	// is gone" true for the wrong reason -- it would never have been written.
	require.True(l.t, l.generatedCRDHasSpecProperty(marker),
		"the fixture must start WITH %q present, or this test cannot observe a re-render", marker)
	return marker
}

func (l *secretRefLoop) fetchGeneratedCRD() *apiextensionsv1.CustomResourceDefinition {
	l.t.Helper()
	gvr := targetGVRForTest(l.cr)
	crd := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(l.t, l.e.kube.Get(context.Background(),
		client.ObjectKey{Name: gvr.Resource + "." + gvr.Group}, crd),
		"the generated CRD must exist before this test can say anything about its schema")
	return crd
}

func (l *secretRefLoop) generatedCRDHasSpecProperty(name string) bool {
	l.t.Helper()
	crd := l.fetchGeneratedCRD()
	for i := range crd.Spec.Versions {
		if _, ok := specProperties(crd.Spec.Versions[i])[name]; ok {
			return true
		}
	}
	return false
}

// specProperties returns a served version's spec properties, or nil when the version carries no schema at
// all -- which the harness's seeded stub does, and which a never-rendered CRD therefore looks like.
func specProperties(v apiextensionsv1.CustomResourceDefinitionVersion) map[string]apiextensionsv1.JSONSchemaProps {
	if v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
		return nil
	}
	spec, ok := v.Schema.OpenAPIV3Schema.Properties["spec"]
	if !ok {
		return nil
	}
	return spec.Properties
}

// removeTheControllerDeployment puts the cluster into the state a provider upgrade creates: the new
// provider's first reconcile finds the generated Kind's controller Deployment missing. Observe then
// returns early -- ResourceExists is deployOk, false on IsNotFound -- and provider-runtime calls Create.
func (l *secretRefLoop) removeTheControllerDeployment() {
	l.t.Helper()
	// Located with the SAME constructor rollOut and Observe use, so this deletes exactly the object
	// Observe will fail to find. Building the key by hand got the namespace from the template wrong and
	// deleted nothing, which would have left the test asserting against a healthy cluster.
	dep := &appsv1.Deployment{}
	require.NoError(l.t, objects.CreateK8sObject(dep, targetGVRForTest(l.cr), types.NamespacedName{
		Namespace: l.cr.Namespace, Name: l.cr.Name + deploy.ControllerResourceSuffix}, RDCtemplateDeploymentPath))
	require.NoError(l.t, l.e.kube.Delete(context.Background(), dep),
		"the Deployment must exist to be removed, or the test is not exercising the race")

	// CONTROL: it is really gone, so the Observe below genuinely takes the !deployOk branch.
	err := l.e.kube.Get(context.Background(), client.ObjectKeyFromObject(dep), &appsv1.Deployment{})
	require.True(l.t, apierrors.IsNotFound(err), "expected the Deployment to be absent, got %v", err)
}

// settleAt brings the loop to steady state with a given build stamped, which is every arm's starting
// point: a cluster that is converged and consistent before the upgrade.
//
// IT ALSO HAS TO MAKE THE FIXTURE GENERATABLE, and that is worth saying rather than hiding in a setup
// line. The shared RestDefinition fixture declares a verb with no `path` and no `method`, because no test
// in this harness has ever needed the generator to RUN -- the CRD is hand-seeded so Observe can get past
// the CRD gate. These tests are the first here that make Create render, so the verb is pointed at the
// only operation the harness OAS declares.
//
// The edit goes through the CLUSTER, not just the in-memory copy, because reconcile() re-reads the object
// at the top of every pass exactly as provider-runtime does.
func settleAt(t *testing.T, build string, regenerate bool) *secretRefLoop {
	t.Helper()
	ctx := context.Background()
	l := newSecretRefLoop(t)

	live := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, l.e.kube.Get(ctx, client.ObjectKeyFromObject(l.cr), live))
	require.Len(t, live.Spec.Resource.VerbsDescription, 1, "the fixture is expected to declare one verb")
	live.Spec.Resource.VerbsDescription[0].Path = "/prs"
	live.Spec.Resource.VerbsDescription[0].Method = "post"
	require.NoError(t, l.e.kube.Update(ctx, live))
	l.cr = live

	l.e.genCfg = GeneratorConfig{Build: build, RegenerateOnUpgrade: regenerate}
	l.settle()
	require.Equal(t, build, l.persisted().GeneratorBuild,
		"the fixture must stamp the build it rendered with before the upgrade is simulated")
	return l
}

// TestCreateRegeneratesWhenTheGeneratorMovedUnderAnExistingCRD is #541 as a test.
//
// The sequence is the one a consumer measured: the provider's build moves, its first reconciles land
// while the controller Deployment is absent, Observe returns early, and Create is entered with the CRD
// already present. Before the fix, Create skipped generation and stamped the new build -- leaving a
// stale schema that no later reconcile would repair, because the stamp had made the drift check false.
func TestCreateRegeneratesWhenTheGeneratorMovedUnderAnExistingCRD(t *testing.T) {
	l := settleAt(t, "0.56.0-aaaaaaa", true)
	marker := l.markTheGeneratedCRD()

	l.e.genCfg.Build = "0.57.0-bbbbbbb"
	l.removeTheControllerDeployment()

	drift, observed := l.reconcile()
	require.True(t, drift, "Observe must report the resource as absent, which is what routes to Create")
	require.True(t, containsSubstring(observed, "Dynamic Controller not deployed yet"),
		"this test is only about the Create path, and that message is how we know Observe took it: %v", observed)

	require.False(t, l.generatedCRDHasSpecProperty(marker),
		"Create found the CRD present and must have REGENERATED it: marker %q survives, so the schema is "+
			"still whatever was there before this build ran (#541)", marker)
	require.Equal(t, "0.57.0-bbbbbbb", l.persisted().GeneratorBuild,
		"having rendered, the stamp must advance")
}

// TestCreateDoesNotStampABuildItDidNotRenderWith is the arm that fails on the OLD code, and it is the
// defect stated directly: the stamp is a claim about the artifact, so it must not move without a render.
//
// The operator here has opted out of regeneration, so Create must leave BOTH the schema and the stamp
// alone. Before the fix it left the schema alone and advanced the stamp -- which is strictly worse than
// doing nothing, because the field then asserts the new build rendered a schema it never touched, and
// `generatorChanged` goes false so nothing will revisit it if they opt back in.
func TestCreateDoesNotStampABuildItDidNotRenderWith(t *testing.T) {
	l := settleAt(t, "0.56.0-aaaaaaa", true)
	marker := l.markTheGeneratedCRD()

	l.e.genCfg = GeneratorConfig{Build: "0.57.0-bbbbbbb", RegenerateOnUpgrade: false}
	l.removeTheControllerDeployment()

	drift, observed := l.reconcile()
	require.True(t, drift)
	require.True(t, containsSubstring(observed, "Dynamic Controller not deployed yet"),
		"this arm must take the CREATE path, or it says nothing about Create's stamp: %v", observed)

	require.True(t, l.generatedCRDHasSpecProperty(marker),
		"an opted-out operator must not get a re-render; marker %q was removed", marker)
	require.Equal(t, "0.56.0-aaaaaaa", l.persisted().GeneratorBuild,
		"nothing rendered, so the stamp must still name the build that DID render this schema")
}

// TestCreateOnAnUnchangedGeneratorRendersNothing is the control that stops the two arms above from
// passing for the wrong reason.
//
// If Create regenerated unconditionally whenever the CRD exists, the first test would pass while
// measuring nothing about the generator check. Here the build has NOT moved, so the tamper must survive
// -- which also pins that the fix did not turn every Create into a re-render of every CRD it finds.
func TestCreateOnAnUnchangedGeneratorRendersNothing(t *testing.T) {
	l := settleAt(t, "0.56.0-aaaaaaa", true)
	marker := l.markTheGeneratedCRD()

	l.removeTheControllerDeployment() // same race, same Create path, same build

	drift, observed := l.reconcile()
	require.True(t, drift)
	require.True(t, containsSubstring(observed, "Dynamic Controller not deployed yet"),
		"this arm must take the CREATE path for its conclusion to hold: %v", observed)

	require.True(t, l.generatedCRDHasSpecProperty(marker),
		"the generator did not move, so Create must not re-render: marker %q was removed, which means the "+
			"first arm would pass without measuring the generator check at all", marker)
	require.Equal(t, "0.56.0-aaaaaaa", l.persisted().GeneratorBuild)
}

// TestTheGeneratorStampIsWrittenInExactlyOnePlace is a structural assertion, and it is here because the
// behaviour tests above cannot express it.
//
// The rule "stamp only where you rendered" was already written down, correctly, in the Update path's own
// comment -- next to a sibling call site where its premise did not hold. A rule in a comment is enforced
// by whoever reads it. This asserts the shape that enforces it instead: Status.GeneratorBuild is assigned
// by one function, and that function renders first.
//
// It fails if someone adds a fourth call site that stamps without rendering, which is precisely how #541
// was introduced and the one thing no fixture can catch.
func TestTheGeneratorStampIsWrittenInExactlyOnePlace(t *testing.T) {
	src := readControllerSource(t)

	const stamp = "cr.Status.GeneratorBuild = e.genCfg.Build"
	require.Equal(t, 1, countOccurrences(src, stamp),
		"Status.GeneratorBuild must be assigned in exactly one place -- renderGeneratedCRDs, which renders "+
			"first. A second assignment is how #541 happened.")

	// And that one place must be the helper: the assignment has to sit after a render, not beside one.
	helper := helperBody(t, src, "func (e *external) renderGeneratedCRDs(")
	require.Contains(t, helper, "e.generateAndApplyCRDs(",
		"renderGeneratedCRDs must be the function that renders")
	require.Contains(t, helper, stamp,
		"the single assignment must live inside renderGeneratedCRDs, after the render")
	require.Less(t, indexOf(helper, "e.generateAndApplyCRDs("), indexOf(helper, stamp),
		"the render must come BEFORE the stamp, or the stamp can be written for a render that failed")

	// CONTROL: the reader can see a string that is definitely there, so a zero above means absence
	// rather than a file that was not read.
	require.Equal(t, 1, countOccurrences(src, "func (e *external) renderGeneratedCRDs("),
		"control: the source really was read")
}

// readControllerSource reads the controller file this package compiles from, so the structural assertion
// below is about the code that ships rather than about a copy. Relative to the test's own directory,
// which `go test` sets as the working directory.
func readControllerSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("restdefinition.go")
	require.NoError(t, err, "the structural test must be able to read the file it reasons about")
	require.Greater(t, len(b), 1000, "control: restdefinition.go is not a stub")
	return string(b)
}

func countOccurrences(hay, needle string) int { return strings.Count(hay, needle) }

func indexOf(hay, needle string) int { return strings.Index(hay, needle) }

// helperBody returns the text of the function whose declaration starts with decl, up to the first line
// that is exactly a closing brace -- enough to tell the order of two statements inside one short
// function, which is all this is used for.
func helperBody(t *testing.T, src, decl string) string {
	t.Helper()
	i := strings.Index(src, decl)
	require.GreaterOrEqual(t, i, 0, "function %q not found", decl)
	rest := src[i:]
	end := strings.Index(rest, "\n}\n")
	require.GreaterOrEqual(t, end, 0, "could not find the end of %q", decl)
	return rest[:end]
}
