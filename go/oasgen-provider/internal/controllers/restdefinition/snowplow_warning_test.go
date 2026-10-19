package restdefinition

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
)

func ref(ns, name string) *definitionv1alpha1.ApiRef {
	return &definitionv1alpha1.ApiRef{Namespace: ns, Name: name}
}

func restDefinitionWith(res definitionv1alpha1.Resource) *definitionv1alpha1.RestDefinition {
	return &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "gitref", Namespace: "krateo-system"},
		Spec:       definitionv1alpha1.RestDefinitionSpec{Resource: res},
	}
}

// configmapTemplate writes the post-helm-render RDC configmap template, optionally carrying URL_SNOWPLOW.
func configmapTemplate(t *testing.T, snowplowURL string) string {
	t.Helper()
	body := `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .resource }}-{{ .apiVersion }}-configmap
  namespace: {{ .namespace }}
data:
  REST_CONTROLLER_SERVICEACCOUNT_NAME: {{ .composition_controller_sa_name }}
  REST_CONTROLLER_SERVICEACCOUNT_NAMESPACE: {{ .composition_controller_sa_namespace }}
  HOME: /tmp
`
	if snowplowURL != "" {
		body += "  URL_SNOWPLOW: \"" + snowplowURL + "\"\n"
	}
	p := filepath.Join(t.TempDir(), "configmap.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func newExternalWithRecorder() (*external, *record.FakeRecorder) {
	rec := record.NewFakeRecorder(10)
	return &external{log: logging.NewNopLogger(), rec: rec}, rec
}

// drain collects the events written so far without blocking on an empty channel.
func drain(rec *record.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// All four delegating fields, not observeApiRef alone: RDC's nil-client check sits in BOTH
// observeViaRestAction and mutateViaRestAction, so a resource that delegates only its create is just as
// inoperative as one that delegates its observe. Listing three and missing one would warn on three
// resources out of four and leave the fourth to be discovered the old way.
func TestDeclaredApiRefsNamesEveryDelegatedVerb(t *testing.T) {
	got := declaredApiRefs(&definitionv1alpha1.Resource{
		ObserveApiRef: ref("krateo-system", "observe-git-ref"),
		CreateApiRef:  ref("krateo-system", "create-git-ref"),
		UpdateApiRef:  ref("krateo-system", "update-git-ref"),
		DeleteApiRef:  ref("krateo-system", "delete-git-ref"),
	})

	assert.Equal(t, []string{
		"createApiRef krateo-system/create-git-ref",
		"deleteApiRef krateo-system/delete-git-ref",
		"observeApiRef krateo-system/observe-git-ref",
		"updateApiRef krateo-system/update-git-ref",
	}, got, "sorted, so the same misconfiguration produces the same message on every reconcile")
}

func TestDeclaredApiRefsIsEmptyWhenNothingIsDelegated(t *testing.T) {
	assert.Empty(t, declaredApiRefs(&definitionv1alpha1.Resource{Kind: "Repo"}))
}

// The case the whole change exists for: #120's createApiRef workaround on a default install, where
// rdc.env is {}.
func TestWarnsWhenAnApiRefIsDeclaredAndSnowplowIsNotConfigured(t *testing.T) {
	e, rec := newExternalWithRecorder()
	cr := restDefinitionWith(definitionv1alpha1.Resource{
		Kind:         "GitRef",
		CreateApiRef: ref("krateo-system", "create-git-ref"),
	})

	e.warnOnApiRefWithoutSnowplow(cr, configmapTemplate(t, ""))

	events := drain(rec)
	require.Len(t, events, 1)
	assert.Contains(t, events[0], "Warning")
	assert.Contains(t, events[0], "ApiRefWithoutSnowplow")
	assert.Contains(t, events[0], "createApiRef krateo-system/create-git-ref",
		"the message has to name the field and the RESTAction, or it sends nobody anywhere")
	assert.Contains(t, events[0], "rdc.env.URL_SNOWPLOW",
		"and the remedy, which is a chart value and not a RestDefinition edit")
}

func TestSilentWhenSnowplowIsConfigured(t *testing.T) {
	e, rec := newExternalWithRecorder()
	cr := restDefinitionWith(definitionv1alpha1.Resource{
		Kind:         "GitRef",
		CreateApiRef: ref("krateo-system", "create-git-ref"),
	})

	e.warnOnApiRefWithoutSnowplow(cr, configmapTemplate(t, "http://snowplow.krateo-system:8081"))

	assert.Empty(t, drain(rec), "a correctly configured install must produce no event at all")
}

// By far the most important negative: this runs on EVERY generation, and the overwhelming majority of
// RestDefinitions delegate nothing. An event here would be noise on every resource in every KOG.
func TestSilentWhenNoApiRefIsDeclaredEvenWithoutSnowplow(t *testing.T) {
	e, rec := newExternalWithRecorder()
	cr := restDefinitionWith(definitionv1alpha1.Resource{Kind: "Repo"})

	e.warnOnApiRefWithoutSnowplow(cr, configmapTemplate(t, ""))

	assert.Empty(t, drain(rec))
}

// Fail open. A RestDefinition that generates correctly must not acquire a warning because the diagnostic
// could not be computed -- and must not have generation disturbed either. Deploy renders the same
// template moments later and reports a genuine template failure where it is actionable.
func TestFailsOpenWhenTheTemplateCannotBeRead(t *testing.T) {
	e, rec := newExternalWithRecorder()
	cr := restDefinitionWith(definitionv1alpha1.Resource{
		Kind:         "GitRef",
		CreateApiRef: ref("krateo-system", "create-git-ref"),
	})

	e.warnOnApiRefWithoutSnowplow(cr, filepath.Join(t.TempDir(), "absent.yaml"))

	assert.Empty(t, drain(rec),
		"an unreadable template is no evidence that snowplow is unconfigured")
}

// A resource delegating several verbs is one misconfiguration, not three. One event, naming all of them.
func TestOneEventForSeveralRefs(t *testing.T) {
	e, rec := newExternalWithRecorder()
	cr := restDefinitionWith(definitionv1alpha1.Resource{
		Kind:          "GitRef",
		CreateApiRef:  ref("krateo-system", "create-git-ref"),
		ObserveApiRef: ref("krateo-system", "observe-git-ref"),
	})

	e.warnOnApiRefWithoutSnowplow(cr, configmapTemplate(t, ""))

	events := drain(rec)
	require.Len(t, events, 1)
	assert.Equal(t, 1, strings.Count(events[0], "ApiRefWithoutSnowplow"))
	assert.Contains(t, events[0], "createApiRef krateo-system/create-git-ref")
	assert.Contains(t, events[0], "observeApiRef krateo-system/observe-git-ref")
}

// A whitespace-only URL_SNOWPLOW is broken configuration, but it is not THIS warning's problem, and the
// distinction is the one place this check could quietly start lying. RDC builds its client whenever the
// value is != "", so "   " yields a client that fails on a malformed request URL. Trimming before the
// comparison would emit an event asserting "no snowplow client is configured" about an install where one
// demonstrably is, and send the reader to set a value that is already set.
func TestSilentWhenSnowplowURLIsWhitespace(t *testing.T) {
	e, rec := newExternalWithRecorder()
	cr := restDefinitionWith(definitionv1alpha1.Resource{
		Kind:         "GitRef",
		CreateApiRef: ref("krateo-system", "create-git-ref"),
	})

	e.warnOnApiRefWithoutSnowplow(cr, configmapTemplate(t, "   "))

	assert.Empty(t, drain(rec),
		"RDC would build a client from this, so the no-client event would be false")
}
