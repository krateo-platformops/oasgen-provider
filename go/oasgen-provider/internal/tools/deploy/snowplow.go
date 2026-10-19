package deploy

import (
	"fmt"

	templates "github.com/krateo-platformops/oasgen-provider/internal/tools/objects"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// SnowplowURLKey is the rest-dynamic-controller environment variable carrying snowplow's base URL.
//
// It is the single switch that decides whether a delegated verb can work at all: RDC constructs its
// snowplow client only when this is non-empty, and every *ApiRef path (observeViaRestAction,
// mutateViaRestAction) returns "no snowplow client is configured" when that client is nil. So a
// RestDefinition declaring any *ApiRef against a controller rendered without this key is not degraded,
// it is inoperative -- every reconcile of every instance fails.
const SnowplowURLKey = "URL_SNOWPLOW"

// RenderedSnowplowURL reports the URL_SNOWPLOW value the generated rest-dynamic-controller WOULD be
// given, by rendering the same ConfigMap template Deploy renders and reading that one key.
//
// The value originates in the oasgen-provider chart as .Values.rdc.env, which the chart expands into
// this template at INSTALL time (assets/rdc/configmap.yaml ranges over it), so by the time the
// controller reads the file the entry is either a literal line or absent. rdc.env defaults to {}, which
// is why the absent case is the common one rather than the exotic one.
//
// The ConfigMap's own identity is deliberately not a parameter: only data[URL_SNOWPLOW] is read and the
// object is discarded, so the name, namespace and labels the template would compute are irrelevant here.
// The two serviceaccount keys get inert placeholders for the same reason -- they must be non-empty only
// so the rendered YAML still decodes into a ConfigMap, whose data is map[string]string.
//
// Reading the template a second time rather than threading cm.Data out of Deploy is intentional: the
// answer is wanted at GENERATION, which runs before Deploy and is the point of the whole check. The
// render is a file read plus a template expansion with no cluster access, so repeating it is cheap.
func RenderedSnowplowURL(configmapTemplatePath string) (string, error) {
	cm := corev1.ConfigMap{}
	err := templates.CreateK8sObject(&cm, schema.GroupVersionResource{}, types.NamespacedName{Name: "snowplow-check"}, configmapTemplatePath,
		"composition_controller_sa_name", "snowplow-check",
		"composition_controller_sa_namespace", "snowplow-check")
	if err != nil {
		return "", fmt.Errorf("rendering the rest-dynamic-controller configmap template to read %s: %w", SnowplowURLKey, err)
	}
	return cm.Data[SnowplowURLKey], nil
}
