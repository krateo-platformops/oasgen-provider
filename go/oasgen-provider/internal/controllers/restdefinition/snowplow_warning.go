package restdefinition

import (
	"fmt"
	"sort"
	"strings"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/deploy"
	corev1 "k8s.io/api/core/v1"
)

// declaredApiRefs names every *ApiRef the resource delegates a verb to, as "<field> <namespace>/<name>",
// sorted so the warning message is stable across reconciles (an unstable message makes two events out of
// one problem).
//
// All four are listed rather than observeApiRef alone: RDC's nil-snowplow-client check sits in
// observeViaRestAction AND mutateViaRestAction, so a resource delegating only its create is just as
// inoperative as one delegating its observe.
func declaredApiRefs(res *definitionv1alpha1.Resource) []string {
	byField := map[string]*definitionv1alpha1.ApiRef{
		"observeApiRef": res.ObserveApiRef,
		"createApiRef":  res.CreateApiRef,
		"updateApiRef":  res.UpdateApiRef,
		"deleteApiRef":  res.DeleteApiRef,
	}
	out := make([]string, 0, len(byField))
	for field, ref := range byField {
		if ref == nil {
			continue
		}
		out = append(out, fmt.Sprintf("%s %s/%s", field, ref.Namespace, ref.Name))
	}
	sort.Strings(out)
	return out
}

// warnOnApiRefWithoutSnowplow warns, at GENERATION, when the RestDefinition delegates a verb to snowplow
// but the controller this release renders has no URL_SNOWPLOW to reach it with.
//
// The gap this closes is one of timing, not of silence. Nothing here is silent at runtime: RDC already
// fails loudly, with the field and the RESTAction named ("resource declares createApiRef X/Y but no
// snowplow client is configured"). But that error needs an instance to reconcile before anyone sees it,
// so the mistake is discovered by whoever first writes a CR -- typically not whoever set the chart
// values, and typically some time later. Generation is where both facts are already in hand: the
// RestDefinition declares the ref, and the ConfigMap about to be rendered for its controller does not
// carry the key. Saying it here costs one event and moves the discovery before the first CR exists.
//
// A WARNING, never fatal, and that is the whole decision in this change. Refusing generation would be
// wrong in both directions: the credential and the snowplow URL are platform-level configuration that
// the RestDefinition author frequently does not own, so refusing punishes the wrong person; and
// URL_SNOWPLOW can be supplied after the fact by a helm upgrade, which restarts the generated controller
// without the RestDefinition changing at all. A refusal would have to be reversed by an edit nobody
// needs to make. (Contrast the built-in name collision guard, which refuses: there the only remedy IS a
// RestDefinition edit, and the author owns it.)
//
// Fail open on a render error, deliberately. This check exists to add a diagnostic; a RestDefinition
// that generates correctly must not stop generating because the diagnostic could not be computed. Deploy
// renders the same template moments later and reports that failure properly, where it is actionable.
func (e *external) warnOnApiRefWithoutSnowplow(cr *definitionv1alpha1.RestDefinition, configmapTemplatePath string) {
	refs := declaredApiRefs(&cr.Spec.Resource)
	if len(refs) == 0 {
		return
	}

	url, err := deploy.RenderedSnowplowURL(configmapTemplatePath)
	if err != nil {
		e.log.Debug("Could not read the rendered snowplow URL; skipping the apiRef/snowplow check",
			"path", configmapTemplatePath, "error", err)
		return
	}
	// Exactly empty, not whitespace-trimmed. RDC builds its snowplow client whenever URL_SNOWPLOW is
	// != "" (main.go), so a whitespace-only value DOES produce a client -- a broken one that fails on a
	// malformed request URL, which is a different problem with a different error. Trimming here would make
	// this event assert "no snowplow client is configured" about an install where one is.
	if url != "" {
		return
	}

	declared := strings.Join(refs, "; ")
	e.log.Warn("Resource delegates a verb to snowplow but the generated controller has no "+deploy.SnowplowURLKey+": every reconcile of every instance will fail",
		"apiRefs", declared, "key", deploy.SnowplowURLKey)
	e.rec.Eventf(cr, corev1.EventTypeWarning, "ApiRefWithoutSnowplow",
		"resource declares %s, but the generated rest-dynamic-controller has no %s, so every reconcile of every instance will fail with \"no snowplow client is configured\": set rdc.env.%s in the oasgen-provider chart values",
		declared, deploy.SnowplowURLKey, deploy.SnowplowURLKey)
}
