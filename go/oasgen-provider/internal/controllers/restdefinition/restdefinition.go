package restdefinition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"os"
	"path"
	"strings"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/krateo-platformops/plumbing/env"
	"github.com/krateo-platformops/plumbing/kubeutil/dynamicwatch"
	"github.com/krateo-platformops/plumbing/kubeutil/event"
	"github.com/krateo-platformops/plumbing/kubeutil/eventrecorder"
	rtv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/provider-runtime/pkg/controller"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/krateo-platformops/provider-runtime/pkg/meta"
	"github.com/krateo-platformops/provider-runtime/pkg/ratelimiter"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/crd"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/crd/generation"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/deploy"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/deployment"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/filegetter"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/oas2jsonschema"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/objects"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/plurals"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/policy"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/render"
	"github.com/krateo-platformops/provider-runtime/pkg/reconciler"
	"github.com/krateo-platformops/provider-runtime/pkg/resource"

	oteltelemetry "github.com/krateo-platformops/oasgen-provider/internal/tools/telemetry"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/text"
	"go.opentelemetry.io/otel/attribute"
)

const (
	reconcileGracePeriod = 1 * time.Minute
	reconcileTimeout     = 4 * time.Minute
)

const (
	errNotRestDefinition = "managed resource is not a RestDefinition"
	resourceVersion      = render.DefaultVersion

	restresourcesStillExistFinalizer = "composition.krateo.io/restresources-still-exist-finalizer"
)

var (
	RDCtemplateDeploymentPath = path.Join(os.TempDir(), "assets/rdc-deployment/deployment.yaml")
	RDCtemplateConfigmapPath  = path.Join(os.TempDir(), "assets/rdc-configmap/configmap.yaml")
	RDCrbacConfigFolder       = path.Join(os.TempDir(), "assets/rdc-rbac/")
)

func Setup(mgr ctrl.Manager, o controller.Options, metrics reconciler.MetricsRecorder) error {
	name := reconciler.ControllerName(definitionv1alpha1.RestDefinitionGroupKind)

	log := o.Logger.WithValues("controller", name)

	// recorder (legacy k8s.io/client-go/tools/record) is used directly by the
	// external client's Eventf calls.
	recorder := mgr.GetEventRecorderFor(name)

	cfg := mgr.GetConfig()

	// apiRecorder (k8s.io/client-go/tools/events) backs the reconciler's
	// event.NewAPIRecorder, mirroring core-provider's eventrecorder.Create wiring;
	// provider-runtime v1.2's event.Recorder moved to plumbing/kubeutil/event which
	// expects the newer events.EventRecorder.
	apiRecorder, err := eventrecorder.Create(context.Background(), cfg, name, nil)
	if err != nil {
		return fmt.Errorf("failed to create event recorder: %w", err)
	}

	cli, err := client.New(cfg, client.Options{})
	if err != nil {
		return fmt.Errorf("failed to create kube client: %w", err)
	}

	discovery, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to create discovery client: %w", err)
	}

	conn := &connector{
		kube:     cli,
		log:      log,
		recorder: recorder,
		disc:     discovery,
		parser:   oas2jsonschema.NewLibOASParser(),
		cfgWatch: dynamicwatch.NewRegistry(mgr.GetCache()),
	}

	r := reconciler.NewReconciler(mgr,
		resource.ManagedKind(definitionv1alpha1.RestDefinitionGroupVersionKind),
		reconciler.WithExternalConnecter(conn),
		reconciler.WithTimeout(reconcileTimeout),
		reconciler.WithCreationGracePeriod(reconcileGracePeriod),
		reconciler.WithPollInterval(o.PollInterval),
		reconciler.WithLogger(log),
		reconciler.WithMetrics(metrics),
		reconciler.WithRecorder(event.NewAPIRecorder(apiRecorder)))

	// Build (not Complete) so the controller handle is captured: a Configuration Kind is generated at
	// runtime by THIS controller (generateAndApplyCRDs), so a dynamic watch on it can only be registered
	// once a RestDefinition has reconciled at least once — see ensureConfigurationWatch, called from
	// Create/Update. Watch is documented safe to call after the manager/controller has started.
	c, err := ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		For(&definitionv1alpha1.RestDefinition{}).
		Build(ratelimiter.New(name, r, o.GlobalRateLimiter))
	if err != nil {
		return err
	}
	conn.ctrl = c

	return nil
}

type connector struct {
	kube     client.Client
	log      logging.Logger
	recorder record.EventRecorder
	disc     discovery.DiscoveryInterface
	parser   oas2jsonschema.Parser
	// cfgWatch and ctrl back the dynamic per-Configuration-Kind watch (see ensureConfigurationWatch):
	// cfgWatch dedupes registration across reconciles (of possibly many different RestDefinitions, each
	// with its own generated Configuration Kind); ctrl is the handle Watch is called on, captured from
	// Build in Setup once the controller exists.
	cfgWatch *dynamicwatch.Registry
	ctrl     crcontroller.Controller
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (reconciler.ExternalClient, error) {
	cr, ok := mg.(*definitionv1alpha1.RestDefinition)
	if !ok {
		return nil, errors.New(errNotRestDefinition)
	}
	RDCtemplateDeploymentPath = env.String("RDC_TEMPLATE_DEPLOYMENT_PATH", RDCtemplateDeploymentPath)
	RDCtemplateConfigmapPath = env.String("RDC_TEMPLATE_CONFIGMAP_PATH", RDCtemplateConfigmapPath)
	RDCrbacConfigFolder = env.String("RDC_RBAC_CONFIG_FOLDER", RDCrbacConfigFolder)

	log := c.log.WithValues("name", cr.Name, "namespace", cr.Namespace)

	return &external{
		kube:     c.kube,
		log:      log,
		rec:      c.recorder,
		disc:     c.disc,
		parser:   c.parser,
		cfgWatch: c.cfgWatch,
		ctrl:     c.ctrl,
	}, nil
}

// An ExternalClient observes, then either creates, updates, or deletes an
// external resource to ensure it reflects the managed resource's desired state.
type external struct {
	kube     client.Client
	log      logging.Logger
	rec      record.EventRecorder
	disc     discovery.DiscoveryInterface
	parser   oas2jsonschema.Parser
	cfgWatch *dynamicwatch.Registry
	ctrl     crcontroller.Controller
}

// ensureConfigurationWatch registers a watch on cr's generated Configuration Kind the first time it is
// seen (cfgWatch dedupes), so a Configuration instance change (a new/changed usernameRef/passwordRef/
// tokenRef, or an instance appearing/disappearing) enqueues cr immediately instead of waiting for the next
// resync — the auth-secret RBAC drift check in Observe then does the actual re-sync. Best-effort: if the
// Configuration Kind is not yet discoverable (its CRD was only just applied), the periodic resync still
// drives the resync-based baseline, and the next Create/Update reconcile retries registration.
//
// There is no corresponding "unwatch": controller-runtime has no primitive to remove a registered source
// from a running controller (the same limitation and tradeoff documented on core-provider's
// compositionmirror.go, which this mechanism is modeled on). A watch on a Configuration Kind whose
// RestDefinition is later deleted is harmless (its informer idles) but is never released for the life of
// the process.
func (e *external) ensureConfigurationWatch(cr *definitionv1alpha1.RestDefinition) {
	if e.ctrl == nil || e.cfgWatch == nil {
		return // not wired for dynamic watches (e.g. under unit tests)
	}
	cfgGVK := getConfigurationGVK(cr)
	if err := e.cfgWatch.EnsureWatch(e.ctrl, cfgGVK, enqueueRestDefinitionForConfiguration(e.kube)); err != nil {
		e.log.Debug("Dynamic watch registration on Configuration Kind deferred", "gvk", cfgGVK.String(), "error", err)
	}
}

// enqueueRestDefinitionForConfiguration maps a Configuration-instance event to the reconcile of its owning
// RestDefinition: the one whose generated Configuration Kind (getConfigurationGVK) matches the event
// object's own GVK. RestDefinitions are listed cluster-wide (Configuration instances are not scoped to
// their owning RestDefinition's namespace — SecretKeySelector documents "a reference to a secret key in an
// arbitrary namespace" as the intended design, so neither is the RestDefinition/Configuration relationship
// itself assumed same-namespace here), mirroring core-provider's compositionmirror.go
// enqueueCDForComposition, the precedent this mechanism is modeled on.
func enqueueRestDefinitionForConfiguration(kube client.Client) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var rds definitionv1alpha1.RestDefinitionList
		if err := kube.List(ctx, &rds); err != nil {
			return nil
		}
		gvk := obj.GetObjectKind().GroupVersionKind()
		var reqs []reconcile.Request
		for i := range rds.Items {
			if getConfigurationGVK(&rds.Items[i]) == gvk {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&rds.Items[i])})
			}
		}
		return reqs
	}
}

// targetVersion is the CRD API version derived from the OAS document's info.version (normalized to a legal
// k8s version name via crdgen), falling back to resourceVersion ("v1alpha1") when the OAS is unavailable or
// declares no version. Used by Create/Update/Delete, which have the parsed OAS in hand.
func targetVersion(doc oas2jsonschema.OASDocument) string {
	return render.TargetVersion(doc)
}

// observedVersion is the currently-deployed CRD API version, read from status (set at the last Create/Update),
// falling back to resourceVersion before the first Create. Used by Observe and finalizer handling, which must
// look at the version that actually exists on the cluster rather than re-deriving from the (possibly bumped)
// OAS — a version bump is detected via the OAS content hash and reconciled (append + redeploy) by Update.
func observedVersion(cr *definitionv1alpha1.RestDefinition) string {
	if cr.Status.Resource.APIVersion != "" {
		if gv, gerr := schema.ParseGroupVersion(cr.Status.Resource.APIVersion); gerr == nil && gv.Version != "" {
			return gv.Version
		}
	}
	return resourceVersion
}

func (e *external) Observe(ctx context.Context, mg resource.Managed) (obs reconciler.ExternalObservation, err error) {
	cr, ok := mg.(*definitionv1alpha1.RestDefinition)
	if !ok {
		return reconciler.ExternalObservation{}, errors.New(errNotRestDefinition)
	}

	ctx, span := oteltelemetry.Tracer().Start(ctx, "restdefinition.observe")
	defer span.End()
	defer func() { oteltelemetry.RecordError(span, err) }()
	span.SetAttributes(
		attribute.String("k8s.object.name", cr.Name),
		attribute.String("k8s.object.namespace", cr.Namespace),
		attribute.String("oas.source", cr.Spec.OASPath),
	)

	gvk := schema.GroupVersionKind{
		Group:   cr.Spec.ResourceGroup,
		Version: observedVersion(cr),
		Kind:    text.CapitaliseFirstLetter(cr.Spec.Resource.Kind),
	}

	if meta.WasDeleted(cr) {
		e.log.Debug("RestDefinition was deleted, skipping observation")
		err := manageFinalizers(ctx, e.kube, cr, e.log.Debug)
		if err != nil {
			return reconciler.ExternalObservation{}, fmt.Errorf("managing finalizers: %w", err)
		}
		return reconciler.ExternalObservation{
			ResourceExists:   false,
			ResourceUpToDate: true,
		}, e.Delete(ctx, cr)
	}

	// Keep the group's oas-version policy present for as long as its CRDs are served, not merely at the
	// moment they are generated. Placed after the WasDeleted branch above so a RestDefinition on its way
	// out does not recreate what Delete may be tearing down.
	e.ensureVersionPolicyBestEffort(ctx, cr)

	// Read hasSecuritySchemes from status (saved by Create/Update) to avoid
	// re-fetching and parsing the OAS document on every Observe cycle.
	// If the status field is not yet set (nil), fetch the OAS document once to
	// determine the correct value. This only happens on the first Observe before
	// Create has had a chance to populate the status field.
	var hasSecuritySchemes bool
	var doc oas2jsonschema.OASDocument
	if cr.Status.HasSecuritySchemes != nil {
		hasSecuritySchemes = *cr.Status.HasSecuritySchemes
		e.log.Debug("Using saved HasSecuritySchemes from status", "HasSecuritySchemes", hasSecuritySchemes)
	} else {
		hasSecuritySchemes = true // Safe default to true
		var derr error
		doc, _, derr = e.getDocumentModelFromCR(ctx, cr)
		if derr != nil {
			e.log.Debug("Failed to get document model from CR, defaulting HasSecuritySchemes to true", "error", derr)
		} else {
			hasSecuritySchemes = doc.SecuritySchemes() != nil && len(doc.SecuritySchemes()) > 0
		}
		e.log.Debug("HasSecuritySchemes not yet saved in status, resolved from OAS document (or defaulted to true if failed to get document model from CR)", "HasSecuritySchemes", hasSecuritySchemes)
	}

	configurationGVR := getConfigurationGVR(cr, hasSecuritySchemes)
	if configurationGVR != (schema.GroupVersionResource{}) {
		e.log.Debug("Configuration GVR", "configurationGVR", configurationGVR.String())
	} else {
		// empty GVR, means no configuration fields or security schemes, so we can skip looking for the configuration CRD
		e.log.Debug("No Configuration GVR, skipping configuration CRD lookup")
	}

	gvr := plurals.ToGroupVersionResource(gvk)
	e.log.Debug("Observing RestDefinition", "gvr", gvr.String())

	crdOk, err := crd.Lookup(ctx, e.kube, gvr)
	if err != nil {
		return reconciler.ExternalObservation{}, err
	}

	// observedVersion falls back to resourceVersion ("v1alpha1") when status.Resource.APIVersion hasn't
	// been set yet. That is normally only true before this RestDefinition's very first Create — but it can
	// also be transiently true on the reconcile immediately following Create, if this Get raced the cache
	// behind e.kube and read the CR before its just-written status was visible: Create derives the CRD
	// version from the OAS (targetVersion) and applies it there, not at the fallback version, so the
	// fallback-based lookup above spuriously reports "not found" even though the CRD exists. Since doc (the
	// parsed OAS) is already in hand whenever status hasn't been observed yet, re-check at the
	// OAS-derived version before concluding the CRD is genuinely missing.
	if !crdOk && cr.Status.Resource.APIVersion == "" && doc != nil {
		if derivedGVR := (schema.GroupVersionResource{
			Group:    cr.Spec.ResourceGroup,
			Version:  targetVersion(doc),
			Resource: gvr.Resource,
		}); derivedGVR != gvr {
			if derivedOk, derr := crd.Lookup(ctx, e.kube, derivedGVR); derr == nil && derivedOk {
				e.log.Debug("CRD found at OAS-derived version, not the status fallback version — status likely hasn't caught up yet",
					"fallbackGVR", gvr.String(), "derivedGVR", derivedGVR.String())
				gvr = derivedGVR
				gvk.Version = derivedGVR.Version
				crdOk = true
			}
		}
	}

	if !crdOk {
		e.log.Debug("CRD not found", "gvr", gvr.String())

		cr.SetConditions(rtv1.Unavailable().
			WithMessage(fmt.Sprintf("CRD for '%s' does not exists yet", gvr.String())))
		return reconciler.ExternalObservation{
			ResourceExists:   false,
			ResourceUpToDate: true,
		}, nil
	}
	e.log.Debug("Searching for Dynamic Controller", "gvr", gvr.String())

	deploymentNSName := types.NamespacedName{
		Namespace: cr.Namespace,
		Name:      cr.Name + deploy.ControllerResourceSuffix,
	}
	obj := appsv1.Deployment{}
	err = objects.CreateK8sObject(&obj, gvr, deploymentNSName, RDCtemplateDeploymentPath)
	if err != nil {
		return reconciler.ExternalObservation{}, err
	}
	deployOk, deployReady, err := deployment.LookupDeployment(ctx, e.kube, &obj)
	if err != nil {
		return reconciler.ExternalObservation{}, err
	}
	if !deployOk {
		e.log.Debug("Dynamic Controller not deployed yet",
			"name", obj.Name, "namespace", obj.Namespace, "gvr", gvr.String())

		cr.SetConditions(rtv1.Unavailable().
			WithMessage(fmt.Sprintf("Dynamic Controller '%s' not deployed yet", obj.Name)))

		return reconciler.ExternalObservation{
			ResourceExists:   resourceExistsForController(deployOk, deployReady),
			ResourceUpToDate: true,
		}, nil
	}

	e.log.Debug("Dynamic Controller already deployed",
		"name", obj.Name, "namespace", obj.Namespace,
		"gvr", gvr.String(), "ready", deployReady,
		"replicas", *obj.Spec.Replicas, "readyReplicas", obj.Status.ReadyReplicas)

	if !deployReady {
		e.log.Debug("Dynamic Controller not ready yet",
			"name", obj.Name, "namespace", obj.Namespace,
		)

		cr.SetConditions(rtv1.Unavailable().
			WithMessage(fmt.Sprintf("Dynamic Controller '%s' not ready yet", obj.Name)))

		// EXISTS BUT NOT READY IS NOT "DOES NOT EXIST". The Deployment is right there -- deployOk is
		// true -- it simply has not finished rolling out.
		//
		// Reporting ResourceExists: false here told provider-runtime the external resource was GONE, so
		// it re-entered the create handshake on a resource that had already been created: it re-set
		// krateo.io/external-create-pending, and because Observe kept answering "not exists" past the
		// creation grace period, the reconcile wedged on errCreateIncomplete ("cannot determine creation
		// result"). The RestDefinition then sat Ready=False forever with a CRD that was in fact served
		// and functional, and took its owning composition down with it.
		//
		// Observed on a 34-RestDefinition install where exactly one wedged -- the annotations are the
		// proof: external-create-succeeded at 20:34:44, external-create-pending re-set at 20:35:59, 75
		// seconds AFTER the create had succeeded. It is timing-dependent, so it recurs intermittently on
		// any large multi-kind install and not at all on small ones.
		//
		// Readiness is surfaced through the Unavailable condition above, which is what conditions are
		// for. ResourceUpToDate stays true so the runtime waits in the observe path rather than
		// attempting an Update on a controller that is still starting.
		//
		// NOTE the deliberate asymmetry with the !deployOk branch above, which keeps returning false:
		// there the Deployment genuinely does NOT exist, and Create is what deploys it. Returning true
		// there would be a real bug -- the controller would never be created at all.
		return reconciler.ExternalObservation{
			ResourceExists:   resourceExistsForController(deployOk, deployReady),
			ResourceUpToDate: true,
		}, nil
	}
	opts := e.deployOptions(cr, gvk, gvr, configurationGVR, true)

	// An OAS content change (e.g. the referenced ConfigMap was edited) does not alter the render digest below
	// — that digest depends only on the RD spec, not the OAS document — so detect it explicitly: hash the
	// resolved OAS and treat a change vs the hash stored at the last Create/Update as drift, so Update
	// regenerates the CRD. A transient fetch failure is NOT treated as drift, to avoid flapping when the OAS
	// source is briefly unreachable.
	if contents, ferr := e.fetchOASBytes(ctx, cr); ferr != nil {
		e.log.Debug("Could not fetch OAS for content-drift check; skipping", "error", ferr)
	} else if h := oasContentDigest(contents); cr.Status.OASHash != h {
		e.log.Debug("OAS document content changed", "status", cr.Status.OASHash, "current", h)
		return reconciler.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: false,
		}, nil
	}

	// A Configuration instance's usernameRef/passwordRef/tokenRef can change (or an instance can appear/
	// disappear) independently of the RestDefinition's own spec, so re-collect and re-hash on every
	// reconcile and treat a change as drift, same pattern as the OAS-content check above. Gated on
	// configurationGVR (already computed above) so a RestDefinition with no Configuration CRD at all skips
	// straight past this, rather than attempting and swallowing a guaranteed List failure every reconcile.
	// A transient collection failure is NOT treated as drift, for the same reason as the OAS check.
	if configurationGVR != (schema.GroupVersionResource{}) {
		if refs, cerr := deploy.CollectAuthSecretRefs(ctx, e.kube, getConfigurationGVK(cr)); cerr != nil {
			e.log.Debug("Could not collect auth secret references for drift check; skipping", "error", cerr)
		} else if h, herr := deploy.AuthSecretRefsDigest(refs); herr != nil {
			e.log.Debug("Could not hash auth secret references for drift check; skipping", "error", herr)
		} else if cr.Status.AuthSecretDigest != h {
			e.log.Debug("Auth secret references changed", "status", cr.Status.AuthSecretDigest, "current", h)
			return reconciler.ExternalObservation{
				ResourceExists:   true,
				ResourceUpToDate: false,
			}, nil
		}
	}

	// Before a version-scoped controller starts watching, make sure the instances it is supposed to own
	// actually carry the label it selects on. Reported, not fatal: failing the reconcile here would stop a
	// working resource over a migration concern, and the next pass retries.
	if bferr := e.backfillVersionLabels(ctx, cr, gvk, hasSecuritySchemes); bferr != nil {
		e.log.Debug("Backfilling the oas-version label", "error", bferr)
	}

	dig, err := deploy.Deploy(ctx, e.kube, opts)
	if err != nil {
		return reconciler.ExternalObservation{}, err
	}

	if cr.Status.Digest != dig {
		e.log.Debug("Rendered resources digest changed", "status", cr.Status.Digest, "rendered", dig)
		return reconciler.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: false,
		}, nil
	}

	dig, err = deploy.Lookup(ctx, e.kube, opts)
	if err != nil {
		return reconciler.ExternalObservation{}, err
	}
	if cr.Status.Digest != dig {
		e.log.Debug("Deployed resources digest changed", "status", cr.Status.Digest, "deployed", dig)
		return reconciler.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: false,
		}, nil
	}

	err = manageFinalizers(ctx, e.kube, cr, e.log.Debug)
	if err != nil {
		return reconciler.ExternalObservation{}, fmt.Errorf("managing finalizers: %w", err)
	}

	cr.SetConditions(rtv1.Available())
	return reconciler.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true,
	}, nil
}

// resourceExistsForController answers the single question Observe must get right about the dynamic
// controller: does the external resource EXIST? Readiness is a separate axis, surfaced through
// conditions, and conflating the two is what wedged RestDefinitions on large installs (#122).
//
//	deployOk  deployReady  ->  exists
//	false     -            ->  false   the Deployment is genuinely absent; Create is what deploys it,
//	                                   so reporting true here would mean it is never created at all
//	true      false        ->  TRUE    it exists, it is merely still rolling out. Reporting false told
//	                                   provider-runtime the resource was GONE, so it re-entered the
//	                                   create handshake on an already-created resource, re-set
//	                                   krateo.io/external-create-pending, and wedged on
//	                                   errCreateIncomplete once the creation grace period lapsed
//	true      true         ->  true    the ordinary case
//
// The asymmetry between the first two rows is deliberate and load-bearing; see #122 for the live
// reproduction, where external-create-pending was re-set 75 seconds AFTER create had succeeded.
func resourceExistsForController(deployOk, deployReady bool) bool {
	_ = deployReady // readiness never affects existence; named to make that explicit rather than implicit
	return deployOk
}

func (e *external) Create(ctx context.Context, mg resource.Managed) (err error) {
	cr, ok := mg.(*definitionv1alpha1.RestDefinition)
	if !ok {
		return errors.New(errNotRestDefinition)
	}

	ctx, span := oteltelemetry.Tracer().Start(ctx, "restdefinition.create")
	defer span.End()
	defer func() { oteltelemetry.RecordError(span, err) }()
	span.SetAttributes(
		attribute.String("k8s.object.name", cr.Name),
		attribute.String("k8s.object.namespace", cr.Namespace),
		attribute.String("oas.source", cr.Spec.OASPath),
	)

	if !meta.IsActionAllowed(cr, meta.ActionCreate) {
		e.log.Debug("External resource should not be created by provider, skip creating.")
		return nil
	}

	e.log.Info("Creating RestDefinition", "Kind:", cr.Spec.Resource.Kind, "Group:", cr.Spec.ResourceGroup)

	doc, oasHash, err := e.getDocumentModelFromCR(ctx, cr)
	if err != nil {
		return fmt.Errorf("getting document model from CR: %w", err)
	}

	// check if doc has authentication defined, if so log it
	hasSecuritySchemes := render.HasSecuritySchemes(doc)
	e.log.Debug("Checking for security schemes in OAS document", "HasSecuritySchemes: ", hasSecuritySchemes)
	if hasSecuritySchemes {
		e.log.Debug("Security schemes found in OAS document", "Count", len(doc.SecuritySchemes()))
		for _, ss := range doc.SecuritySchemes() {
			e.log.Debug("Security scheme found in OAS document", "Name", ss.Name, "Type", ss.Type)
		}
	} else {
		e.log.Debug("No security schemes found in OAS document")
	}

	gvk := schema.GroupVersionKind{
		Group:   cr.Spec.ResourceGroup,
		Version: targetVersion(doc),
		Kind:    text.CapitaliseFirstLetter(cr.Spec.Resource.Kind),
	}
	gvr := plurals.ToGroupVersionResource(gvk)

	crdOk, err := crd.Lookup(ctx, e.kube, gvr)
	if err != nil {
		return err
	}

	if !crdOk {
		if err := e.generateAndApplyCRDs(ctx, cr, gvk, doc, hasSecuritySchemes); err != nil {
			return err
		}

		cr.SetConditions(rtv1.Creating())
		cr.Status.HasSecuritySchemes = &hasSecuritySchemes
		err = e.kube.Status().Update(ctx, cr)
		if err != nil {
			return fmt.Errorf("updating status: %w", err)
		}
		e.log.Debug("Applied CRD", "Kind:", cr.Spec.Resource.Kind, "Group:", cr.Spec.ResourceGroup)
		e.rec.Eventf(cr, corev1.EventTypeNormal, "RestDefinitionCreating",
			"RestDefinition '%s/%s' creating", cr.Spec.Resource.Kind, cr.Spec.ResourceGroup)

		return nil
	}

	configurationGVR := getConfigurationGVR(cr, hasSecuritySchemes)
	opts := e.deployOptions(cr, gvk, gvr, configurationGVR, false)
	// Before a version-scoped controller starts watching, make sure the instances it is supposed to own
	// actually carry the label it selects on. Reported, not fatal: failing the reconcile here would stop a
	// working resource over a migration concern, and the next pass retries.
	if bferr := e.backfillVersionLabels(ctx, cr, gvk, hasSecuritySchemes); bferr != nil {
		e.log.Debug("Backfilling the oas-version label", "error", bferr)
	}

	dig, err := deploy.Deploy(ctx, e.kube, opts)
	if err != nil {
		return fmt.Errorf("installing controller: %w", err)
	}

	if err := e.syncAuthSecretRBAC(ctx, cr, gvr); err != nil {
		return fmt.Errorf("syncing auth secret RBAC: %w", err)
	}
	if configurationGVR != (schema.GroupVersionResource{}) {
		e.ensureConfigurationWatch(cr)
	}

	// Prune served versions superseded by this one that are safe to drop without migrating any instance
	// (not current, not vacuum, not in storedVersions) — bounds version accumulation from repeated bumps.
	// Best-effort: a prune failure must not fail the reconcile.
	if pruned, perr := crd.PruneServedVersions(ctx, e.kube, gvr.GroupResource(), gvk.Version); perr != nil {
		e.log.Debug("Pruning superseded served versions failed (non-fatal)", "error", perr)
	} else if len(pruned) > 0 {
		e.log.Debug("Pruned superseded served versions", "versions", pruned)
	}

	cr.SetConditions(rtv1.Creating())
	cr.Status.Resource = definitionv1alpha1.KindApiVersion{
		Kind:       gvk.Kind,
		APIVersion: gvk.GroupVersion().String(),
	}
	// Only set Configuration status if configuration fields or security schemes are defined
	if len(cr.Spec.Resource.ConfigurationFields) > 0 || hasSecuritySchemes {
		e.log.Debug("Configuration fields or security schemes defined, setting Configuration status in RestDefinition status")
		cfgGVK := getConfigurationGVK(cr)
		cr.Status.Configuration = definitionv1alpha1.KindApiVersion{
			Kind:       cfgGVK.Kind,
			APIVersion: cfgGVK.GroupVersion().String(),
		}
	}
	cr.Status.OASPath = cr.Spec.OASPath
	cr.Status.Digest = dig
	cr.Status.HasSecuritySchemes = &hasSecuritySchemes
	cr.Status.OASHash = oasHash
	cr.Status.ResourceHash = resourceHash(cr)

	err = e.kube.Status().Update(ctx, cr)
	if err != nil {
		return fmt.Errorf("updating status: %w", err)
	}

	e.log.Debug("Created RestDefinition", "Kind:", cr.Spec.Resource.Kind, "Group:", cr.Spec.ResourceGroup)
	e.rec.Eventf(cr, corev1.EventTypeNormal, "RestDefinitionCreating",
		"RestDefinition '%s/%s' creating", cr.Spec.Resource.Kind, cr.Spec.ResourceGroup)
	return err
}

// generateAndApplyCRDs renders the target CRD (and the Configuration CRD when configuration fields or
// security schemes are present) from the OAS document via render.CRDs, and applies each version-awarely via
// ApplyOrUpdateCRD (create when absent, or in-place schema replace of the current version). It is shared by
// Create (first install) and Update (regenerate when the OAS content changed), so an edited OAS is reflected
// in the CRD. The rendering half is the same code the oasgen-render preview service runs.
// resourceHash is a content hash of spec.resource, used to notice a change that alters the generated CRD
// while the OAS document is byte-identical.
//
// Needed because additionalStatusFields is append-only rather than immutable: it is the one input to
// generation a user can still change. Gating regeneration on the OAS hash alone would accept such an
// apply and silently not regenerate, which is worse than refusing it -- the upgrade would report success
// and the new status fields would simply never appear.
func resourceHash(cr *definitionv1alpha1.RestDefinition) string {
	b, err := json.Marshal(cr.Spec.Resource)
	if err != nil {
		// A spec that cannot be marshalled cannot be hashed; returning a constant would make every
		// reconcile look unchanged, so return something that never matches instead.
		return "unhashable"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// deployOptions builds the DeployOptions for every path that renders the rest-dynamic-controller.
//
// ONE constructor, and that is the whole point. Observe renders what WOULD be deployed and compares its
// digest against the digest Create/Update stored. If the two paths build their options differently, the
// digests can never agree: Observe reports ResourceUpToDate=false forever, Update re-renders and stores
// the same value it already had, and the resource never reaches Available.
//
// That is not hypothetical. 0.28.0 shipped with LabelSelector set in Create and Update but missing here,
// because the three literals were edited by matching on their text and Observe's differed by alignment
// alone. Every RestDefinition on the affected cluster sat Ready=False/Creating indefinitely, with a
// stable status.digest and a rendered digest that "changed" on every pass -- a compute-only loop that
// rewrote nothing, so it looked like churn rather than a stuck gate.
//
// dryRun is the ONLY legitimate difference between the read path and the write paths: Observe must not
// mutate anything to find out what the digest would be. Any other field added here is shared by
// construction, which is what stops this recurring.
func (e *external) deployOptions(
	cr *definitionv1alpha1.RestDefinition,
	gvk schema.GroupVersionKind,
	gvr schema.GroupVersionResource,
	configurationGVR schema.GroupVersionResource,
	dryRun bool,
) deploy.DeployOptions {
	return deploy.DeployOptions{
		ConfigurationGVR:       configurationGVR,
		RBACFolderPath:         RDCrbacConfigFolder,
		DeploymentTemplatePath: RDCtemplateDeploymentPath,
		ConfigmapTemplatePath:  RDCtemplateConfigmapPath,
		KubeClient:             e.kube,
		NamespacedName: types.NamespacedName{
			Namespace: cr.Namespace,
			Name:      cr.Name,
		},
		GVR:           gvr,
		Log:           e.log.Debug,
		LabelSelector: e.versionSelectorFor(gvk.Version),
		DryRunServer:  dryRun,
	}
}

// versionSelectorFor is the label selector the rest-dynamic-controller for this CRD version must watch
// with: exact equality on krateo.io/oas-version, matching composition-dynamic-controller's
// selection.Equals.
//
// Exact rather than "everything except the other versions". An exact selector is a constant function of
// the controller's OWN version, so it cannot go stale. The notin scheme it replaces did: a controller
// deployed with notin (v26) claims v28-labelled instances the moment a v28 appears, because the set it
// excludes was fixed when it was rendered. That held under one snapshot and not across time, which is
// the failure mode a label selector must not have.
//
// Returning "" would mean "watch everything", which is the pre-coexistence behaviour and is why the
// version must never be empty here. Callers pass gvk.Version, which generation.VersionSelector guards.
func (e *external) versionSelectorFor(version string) string {
	return generation.VersionSelector(version)
}

// ensureVersionPolicyBestEffort keeps the group's oas-version policy present on every reconcile.
//
// Until #173 this was reachable only from generateAndApplyCRDs, whose two callers are gated: Create runs
// it when the CRD is absent, Update when the OAS or resource hash changed. On a stable install neither
// fires, so a policy deleted out of band — by a cleanup script, by policy tooling, or by an operator
// trying to force a refresh — was never restored, and nothing reported its absence.
//
// Observe runs unconditionally and precedes Create and Update in the reconciler loop, so ensuring here
// also preserves the ordering the generation path relies on: the policy exists before the CRD that makes
// instances of this group writable.
//
// Deliberately NOT fatal, unlike the call in generateAndApplyCRDs. That one runs immediately before the
// CRD is applied — before instances can be written at all — so failing closed there prevents the window
// the policy exists to close. By the time Observe runs the CRD is already served and instances may
// already be live, so failing the observation would close no window; it would only stop reconciling a
// resource that is otherwise healthy, and on a cluster whose RBAC lagged the chart it would break every
// RestDefinition at once. A warning plus an event makes the absence visible without that blast radius.
//
// EnsureVersionPolicy already returns nil on a cluster that does not serve the API (< 1.36), so this
// stays quiet there rather than warning once per reconcile.
//
// This is hygiene today, because rest-dynamic-controller stamps the label itself when it observes an
// instance without one. It becomes load-bearing once watching is version-scoped: an unlabelled instance
// then matches no controller's watch, so it is never observed, so that fallback never runs.
func (e *external) ensureVersionPolicyBestEffort(ctx context.Context, cr *definitionv1alpha1.RestDefinition) {
	outcome, err := policy.EnsureVersionPolicy(ctx, e.kube, cr.Spec.ResourceGroup)
	if err != nil {
		e.log.Warn("Could not ensure the oas-version policy; instances of this group may be admitted without the version label",
			"group", cr.Spec.ResourceGroup, "error", err)
		e.rec.Eventf(cr, corev1.EventTypeWarning, "VersionPolicyNotEnsured",
			"could not ensure the oas-version MutatingAdmissionPolicy for group %q: %v", cr.Spec.ResourceGroup, err)
		return
	}

	// A stale policy is not a failure -- it still stamps, just not with the spec this build would write --
	// so it is reported rather than raised. It is actionable precisely because the ensure now runs every
	// reconcile: deleting the policy is enough, and the next pass recreates it from the running code.
	// Before that, saying "this is stale" would have been a complaint with no remedy short of editing the
	// RestDefinition to force regeneration.
	if outcome == policy.OutcomeStale {
		e.log.Warn("The oas-version policy on this cluster was created from a different spec and is never updated in place; delete it to have it recreated",
			"group", cr.Spec.ResourceGroup, "policy", policy.PolicyName(cr.Spec.ResourceGroup))
		e.rec.Eventf(cr, corev1.EventTypeWarning, "VersionPolicyStale",
			"MutatingAdmissionPolicy %q was created from a different spec than this build writes; it is never updated in place, so delete it and the next reconcile will recreate it",
			policy.PolicyName(cr.Spec.ResourceGroup))
	}
}

func (e *external) generateAndApplyCRDs(ctx context.Context, cr *definitionv1alpha1.RestDefinition, gvk schema.GroupVersionKind, doc oas2jsonschema.OASDocument, hasSecuritySchemes bool) (err error) {
	rendered, err := render.CRDs(ctx, cr, gvk, doc, hasSecuritySchemes)
	if err != nil {
		return err
	}
	// Schema warnings are WARN plus an event, not DEBUG.
	//
	// They were DEBUG+3 and on nothing else, which made them unreachable on a default install (#151). The
	// failure they exist to catch is silent by construction: a RestDefinition whose identifier can never
	// match reports Ready, findby never matches, nothing errors, and the resource simply never converges.
	// That is exactly the case where nobody has a reason to go tailing debug logs -- everything visible
	// says the system is fine. An accurate warning nobody can reach is close to worthless.
	//
	// An EVENT rather than a new status field, deliberately. The issue proposes status.generationWarnings,
	// which is an API change on a published CRD and brings staleness with it: a warning describing a fixed
	// problem is worse than none, because it teaches people to ignore the field. Events expire on their
	// own, are already where an operator looks (`kubectl describe restdefinition`), and follow the
	// SkippedSecuritySchemes precedent immediately below. Status surface remains open in #151 if these
	// turn out not to be enough.
	//
	// ONE aggregated event rather than one per warning: this runs on every CRD generation, and a document
	// with a dozen unresolvable fields should not write a dozen events. The log keeps the full detail.
	logWarnings := func(kind string, warnings []error) {
		for _, w := range warnings {
			e.log.Warn("Schema "+kind+" warning", "warning", w)
		}
	}
	logWarnings("generation", rendered.GenerationWarnings)
	logWarnings("validation", rendered.ValidationWarnings)

	if len(rendered.GenerationWarnings)+len(rendered.ValidationWarnings) > 0 {
		all := append(append([]error{}, rendered.GenerationWarnings...), rendered.ValidationWarnings...)
		e.rec.Eventf(cr, corev1.EventTypeWarning, "SchemaWarnings", "%s", warningEventMessage(all))
	}

	// A skipped security scheme is not a debug-level fact. The document advertises a way to authenticate
	// that the generated Configuration CRD cannot express, so the user has no field to supply a credential
	// through and discovers it as 401s at reconcile time. Warn always; when NOTHING could be generated the
	// resource cannot authenticate at all, so say so distinctly and emit an event.
	//
	// Deliberately not fatal: unlike a rejected requestTransform or poll path, the user declared nothing
	// here — this is the vendor's document — and we cannot know whether the endpoint actually enforces the
	// scheme it advertises. Failing generation would break anyone running an oauth2-declaring document
	// against an endpoint that does not enforce it.
	if len(rendered.SkippedSecuritySchemes) > 0 {
		schemes := strings.Join(rendered.SkippedSecuritySchemes, "; ")
		if rendered.AuthenticationGenerated {
			e.log.Warn("Unsupported security schemes skipped; other authentication methods are still available",
				"schemes", schemes)
			e.rec.Eventf(cr, corev1.EventTypeWarning, "UnsupportedSecuritySchemes",
				"skipped security scheme(s) not expressible in the Configuration CRD: %s", schemes)
		} else {
			e.log.Warn("No authentication method could be generated: this resource has NO way to supply a credential",
				"schemes", schemes)
			e.rec.Eventf(cr, corev1.EventTypeWarning, "NoAuthenticationGenerated",
				"the OAS document declares only unsupported security scheme(s) (%s), so the generated Configuration CRD has no authentication field and every request will be unauthenticated", schemes)
		}
	}

	// Ensure the version-stamping policy BEFORE the CRD exists, because the CRD is what makes instances
	// writable: once it is served, an instance can be created, and one admitted without the
	// krateo.io/oas-version label is invisible to its version's controller. Ordering this after the apply
	// would leave a window where that is possible.
	//
	// Fatal here, and only here. Observe calls ensureVersionPolicyBestEffort on every reconcile, which is
	// what actually keeps the policy present (#173); this call is what makes the policy a PRECONDITION of
	// first serving the CRD. Failing closed on this path refuses to open that window; failing closed in
	// Observe would merely stop reconciling a group whose CRD is already served. Hence the asymmetry.
	//
	// An absent MutatingAdmissionPolicy API is now fatal too, where it used to be tolerated. The chart's
	// floor moved to 1.36 in the same change that made watching version-scoped: with an exact selector
	// there is no longer anything that covers a missing policy, because rest-dynamic-controller's own
	// fallback stamping could never be reached by an instance that matches no watch.
	// A stale policy is NOT fatal even here: it still stamps, and refusing to generate the CRD over a spec
	// difference would turn a reportable condition into an outage. Observe reports it. A collision is
	// fatal, because the colliding group's instances would never be stamped at all.
	if _, perr := policy.EnsureVersionPolicy(ctx, e.kube, cr.Spec.ResourceGroup); perr != nil {
		return fmt.Errorf("ensuring the oas-version policy for group %q: %w", cr.Spec.ResourceGroup, perr)
	}

	e.log.Debug("Applying CRD", "Kind:", cr.Spec.Resource.Kind, "Group:", cr.Spec.ResourceGroup)
	owner := cr.Namespace + "/" + cr.Name
	outcome, err := crd.ApplyOrUpdateCRD(ctx, e.kube, rendered.CRD, owner)
	if err != nil {
		return fmt.Errorf("installing CRD: %w", err)
	}
	e.noteAdoption(cr, rendered.CRD.Name, outcome)

	if cfgCRDU := rendered.ConfigurationCRD; cfgCRDU != nil {
		e.log.Debug("Applying Configuration CRD", "Kind", cfgCRDU.Spec.Names.Kind, "Group", cfgCRDU.Spec.Group)
		cfgOutcome, cerr := crd.ApplyOrUpdateCRD(ctx, e.kube, cfgCRDU, owner)
		if cerr != nil {
			return fmt.Errorf("installing configuration CRD: %w", cerr)
		}
		e.noteAdoption(cr, cfgCRDU.Name, cfgOutcome)
	}
	return nil
}

func (e *external) Update(ctx context.Context, mg resource.Managed) (err error) {
	cr, ok := mg.(*definitionv1alpha1.RestDefinition)
	if !ok {
		return errors.New(errNotRestDefinition)
	}

	ctx, span := oteltelemetry.Tracer().Start(ctx, "restdefinition.update")
	defer span.End()
	defer func() { oteltelemetry.RecordError(span, err) }()
	span.SetAttributes(
		attribute.String("k8s.object.name", cr.Name),
		attribute.String("k8s.object.namespace", cr.Namespace),
		attribute.String("oas.source", cr.Spec.OASPath),
	)

	doc, oasHash, err := e.getDocumentModelFromCR(ctx, cr)
	if err != nil {
		return fmt.Errorf("getting document model from CR: %w", err)
	}

	hasSecuritySchemes := render.HasSecuritySchemes(doc)

	if !meta.IsActionAllowed(cr, meta.ActionUpdate) {
		e.log.Debug("External resource should not be updated by provider, skip updating.")
		return nil
	}

	e.log.Info("Updating RestDefinition", "Kind:", cr.Spec.Resource.Kind, "Group:", cr.Spec.ResourceGroup)

	gvk := schema.GroupVersionKind{
		Group:   cr.Spec.ResourceGroup,
		Version: targetVersion(doc),
		Kind:    text.CapitaliseFirstLetter(cr.Spec.Resource.Kind),
	}
	gvr := plurals.ToGroupVersionResource(gvk)

	// Regenerate and re-apply the target CRD when the OAS content changed (same version → in-place schema
	// replace, breaking allowed). This is what makes an edited OAS ConfigMap reach the CRD — previously Update
	// only redeployed the RDC. Gated on the OAS content hash so a drift triggered by anything else (e.g. the
	// deployment digest) does not needlessly re-run the crd generator.
	resHash := resourceHash(cr)
	if oasHash != cr.Status.OASHash || resHash != cr.Status.ResourceHash {
		e.log.Debug("Regenerating CRD",
			"oasChanged", oasHash != cr.Status.OASHash,
			"resourceChanged", resHash != cr.Status.ResourceHash)
		if gerr := e.generateAndApplyCRDs(ctx, cr, gvk, doc, hasSecuritySchemes); gerr != nil {
			return fmt.Errorf("regenerating CRD: %w", gerr)
		}
		cr.Status.ResourceHash = resHash
	}

	configurationGVR := getConfigurationGVR(cr, hasSecuritySchemes)
	opts := e.deployOptions(cr, gvk, gvr, configurationGVR, false)
	// Before a version-scoped controller starts watching, make sure the instances it is supposed to own
	// actually carry the label it selects on. Reported, not fatal: failing the reconcile here would stop a
	// working resource over a migration concern, and the next pass retries.
	if bferr := e.backfillVersionLabels(ctx, cr, gvk, hasSecuritySchemes); bferr != nil {
		e.log.Debug("Backfilling the oas-version label", "error", bferr)
	}

	dig, err := deploy.Deploy(ctx, e.kube, opts)
	if err != nil {
		return fmt.Errorf("installing controller: %w", err)
	}

	if err := e.syncAuthSecretRBAC(ctx, cr, gvr); err != nil {
		return fmt.Errorf("syncing auth secret RBAC: %w", err)
	}
	if configurationGVR != (schema.GroupVersionResource{}) {
		e.ensureConfigurationWatch(cr)
	}

	// Prune served versions superseded by this one that are safe to drop without migrating any instance
	// (not current, not vacuum, not in storedVersions) — bounds version accumulation from repeated bumps.
	// Best-effort: a prune failure must not fail the reconcile.
	if pruned, perr := crd.PruneServedVersions(ctx, e.kube, gvr.GroupResource(), gvk.Version); perr != nil {
		e.log.Debug("Pruning superseded served versions failed (non-fatal)", "error", perr)
	} else if len(pruned) > 0 {
		e.log.Debug("Pruned superseded served versions", "versions", pruned)
	}

	cr.SetConditions(rtv1.Creating())
	cr.Status.Resource = definitionv1alpha1.KindApiVersion{
		Kind:       gvk.Kind,
		APIVersion: gvk.GroupVersion().String(),
	}
	// Only set Configuration status if configuration fields or security schemes are defined
	if len(cr.Spec.Resource.ConfigurationFields) > 0 || hasSecuritySchemes {
		e.log.Debug("Configuration fields or security schemes defined, setting Configuration status in RestDefinition status")
		cfgGVK := getConfigurationGVK(cr)
		cr.Status.Configuration = definitionv1alpha1.KindApiVersion{
			Kind:       cfgGVK.Kind,
			APIVersion: cfgGVK.GroupVersion().String(),
		}
	}
	cr.Status.OASPath = cr.Spec.OASPath
	cr.Status.Digest = dig
	cr.Status.HasSecuritySchemes = &hasSecuritySchemes
	cr.Status.OASHash = oasHash

	err = e.kube.Status().Update(ctx, cr)
	if err != nil {
		return fmt.Errorf("updating status: %w", err)
	}

	e.log.Debug("Updated RestDefinition", "Kind:", cr.Spec.Resource.Kind, "Group:", cr.Spec.ResourceGroup)
	e.rec.Eventf(cr, corev1.EventTypeNormal, "RestDefinitionUpdating",
		"RestDefinition '%s/%s' updating", cr.Spec.Resource.Kind, cr.Spec.ResourceGroup)
	return nil
}

func (e *external) Delete(ctx context.Context, mg resource.Managed) (err error) {
	cr, ok := mg.(*definitionv1alpha1.RestDefinition)
	if !ok {
		return errors.New(errNotRestDefinition)
	}

	ctx, span := oteltelemetry.Tracer().Start(ctx, "restdefinition.delete")
	defer span.End()
	defer func() { oteltelemetry.RecordError(span, err) }()
	span.SetAttributes(
		attribute.String("k8s.object.name", cr.Name),
		attribute.String("k8s.object.namespace", cr.Namespace),
		attribute.String("oas.source", cr.Spec.OASPath),
	)

	// Set to true by default to avoid issues in case of errors when getting the document model from the CR.
	// During a helm uninstall of a provider, the ConfigMap containing the OAS document might already be deleted
	// when the RestDefinition is being deleted, causing an error when trying to get the document model from the CR.
	hasSecuritySchemes := true
	doc, _, err := e.getDocumentModelFromCR(ctx, cr)
	if err != nil {
		e.log.Debug("Failed to get document model from CR", "error", err)
		// Probably ConfigMap with OAS document is already deleted during a helm uninstall
	}
	if doc != nil {
		hasSecuritySchemes = doc.SecuritySchemes() != nil && len(doc.SecuritySchemes()) > 0
		e.log.Debug("Checked for security schemes in OAS document", "HasSecuritySchemes", hasSecuritySchemes)
	}

	if !meta.IsActionAllowed(cr, meta.ActionDelete) {
		e.log.Debug("External resource should not be deleted by provider, skip deleting.")
		return nil
	}

	e.log.Info("Deleting RestDefinition", "Kind:", cr.Spec.Resource.Kind, "Group:", cr.Spec.ResourceGroup)

	gvr := plurals.ToGroupVersionResource(schema.GroupVersionKind{
		Group:   cr.Spec.ResourceGroup,
		Version: targetVersion(doc),
		Kind:    text.CapitaliseFirstLetter(cr.Spec.Resource.Kind),
	})

	// The GVK used to LIST instances before the CRD is dropped (#125) deliberately uses
	// observedVersion, not targetVersion, for the same reason manageFinalizers does: the apiserver
	// serves every instance through any served version, and the observed one is guaranteed to exist on
	// the CRD. targetVersion may be a bump that has not been deployed yet, and listing against a version
	// the CRD does not serve fails -- which this guard treats as "cannot verify" and refuses on, blocking
	// the delete forever for a reason that has nothing to do with live instances.
	listGVK := schema.GroupVersionKind{
		Group:   cr.Spec.ResourceGroup,
		Version: observedVersion(cr),
		Kind:    text.CapitaliseFirstLetter(cr.Spec.Resource.Kind),
	}

	skipDeploy := meta.FinalizerExists(cr, restresourcesStillExistFinalizer)

	configurationGVR := getConfigurationGVR(cr, hasSecuritySchemes)
	opts := deploy.UndeployOptions{
		ConfigurationGVR: configurationGVR,
		// GVK lets Undeploy LIST live instances before dropping the CRD (#125). Without it the count
		// cannot be taken, and Undeploy refuses rather than assuming zero.
		GVK:            listGVK,
		SkipCRD:        false,
		SkipDeploy:     skipDeploy,
		RBACFolderPath: RDCrbacConfigFolder,
		KubeClient:     e.kube,
		NamespacedName: types.NamespacedName{
			Namespace: cr.Namespace,
			Name:      cr.Name,
		},
		GVR:                    gvr,
		Log:                    e.log.Debug,
		DeploymentTemplatePath: RDCtemplateDeploymentPath,
		ConfigmapTemplatePath:  RDCtemplateConfigmapPath,
	}

	err = deploy.Undeploy(ctx, e.kube, opts)
	if err != nil {
		return fmt.Errorf("uninstalling controller: %w", err)
	}

	if skipDeploy || opts.SkipCRD {
		e.log.Debug(" RestResources still exist",
			"Group", gvr.Group, "Resource", gvr.Resource)
		return fmt.Errorf("restResources still exist")
	}

	// Only torn down once we're past the skipDeploy check above: RestResources still existing means RDC is
	// still actively reconciling them and may still need to authenticate to the external API, so revoking
	// its auth-secret access any earlier could break an in-flight reconcile of a resource that isn't
	// actually going away yet.
	if terr := e.teardownAuthSecretRBAC(ctx, cr, gvr); terr != nil {
		e.log.Debug("Tearing down auth secret RBAC failed (non-fatal)", "error", terr)
	}

	e.log.Debug("Deleting RestDefinition", "Kind:", cr.Spec.Resource.Kind, "Group:", cr.Spec.ResourceGroup)
	e.rec.Eventf(cr, corev1.EventTypeNormal, "RestDefinitionDeleting",
		"RestDefinition '%s/%s' deleting", cr.Spec.Resource.Kind, cr.Spec.ResourceGroup)
	return err
}

func getConfigurationGVR(cr *definitionv1alpha1.RestDefinition, hasSecuritySchemes bool) schema.GroupVersionResource {
	// Return empty GVR if no configuration fields or security schemes are defined, as we don't want to create a configuration CRD in this case
	if len(cr.Spec.Resource.ConfigurationFields) == 0 && !hasSecuritySchemes {
		return schema.GroupVersionResource{}
	}
	cfgGVK := getConfigurationGVK(cr)
	return plurals.ToGroupVersionResource(cfgGVK)
}

func getConfigurationGVK(cr *definitionv1alpha1.RestDefinition) schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   cr.Spec.ResourceGroup,
		Version: resourceVersion,
		Kind:    text.CapitaliseFirstLetter(cr.Spec.Resource.Kind) + "Configuration",
	}
}

func manageFinalizers(ctx context.Context, kubecli client.Client, cr *definitionv1alpha1.RestDefinition, log func(msg string, keysAndValues ...any)) error {
	log("Managing finalizers for RestDefinition", "name", cr.Name, "namespace", cr.Namespace)

	// Check if RestResources still exist for this RestDefinition. Use the deployed version (from status): the
	// apiserver serves all instances through any served version, and this one is guaranteed to exist on the CRD.
	gvk := schema.GroupVersionKind{
		Group:   cr.Spec.ResourceGroup,
		Version: observedVersion(cr),
		Kind:    text.CapitaliseFirstLetter(cr.Spec.Resource.Kind),
	}

	uli := unstructured.UnstructuredList{}
	uli.SetGroupVersionKind(gvk)
	err := kubecli.List(ctx, &uli)
	if err != nil && !strings.Contains(err.Error(), "no matches for") {

		// If the CRD is missing, we assume no resources exist
		if strings.Contains(err.Error(), "the server could not find the requested resource") {
			log("CRD not found, treating as no resources exist",
				"Group", cr.Spec.ResourceGroup,
				"Kind", cr.Spec.Resource.Kind,
				"Version", observedVersion(cr),
				"error", err.Error())
			uli.Items = nil
			err = nil
		} else {
			return fmt.Errorf("listing RestResources: %w", err)
		}
	}

	restResourceCount := len(uli.Items)

	// Manage restresources-still-exist finalizer
	if restResourceCount > 0 {
		if !meta.FinalizerExists(cr, restresourcesStillExistFinalizer) {
			log("Existing RestResources found", "Group", cr.Spec.ResourceGroup, "Kind", cr.Spec.Resource.Kind, "Count", restResourceCount)
			log("Adding finalizer to RestDefinition", "name", cr.Name, "finalizer", restresourcesStillExistFinalizer)
			meta.AddFinalizer(cr, restresourcesStillExistFinalizer)
			err = kubecli.Update(ctx, cr)
			if err != nil {
				return fmt.Errorf("adding restresources finalizer: %w", err)
			}
		}
	} else {
		if meta.FinalizerExists(cr, restresourcesStillExistFinalizer) {
			log("No RestResources found, removing finalizer", "name", cr.Name, "finalizer", restresourcesStillExistFinalizer)
			meta.RemoveFinalizer(cr, restresourcesStillExistFinalizer)
			err = kubecli.Update(ctx, cr)
			if err != nil {
				return fmt.Errorf("removing restresources finalizer: %w", err)
			}
		}
	}

	return nil
}

// fetchOASBytes downloads the OAS document referenced by the CR's oasPath (configmap:// or http(s)://) and
// returns its raw bytes. It uses a unique temp dir per call so concurrent reconciles never clobber each
// other's download (the previous shared /tmp/ogen-provider dir was racy under >1 concurrent reconcile, and
// Observe now fetches on every cycle for drift detection).
func (e *external) fetchOASBytes(ctx context.Context, cr *definitionv1alpha1.RestDefinition) (contents []byte, err error) {
	ctx, span := oteltelemetry.Tracer().Start(ctx, "restdefinition.fetch_oas")
	defer span.End()
	defer func() { oteltelemetry.RecordError(span, err) }()
	OASPath := cr.Spec.OASPath
	span.SetAttributes(
		attribute.String("k8s.object.name", cr.Name),
		attribute.String("k8s.object.namespace", cr.Namespace),
		attribute.String("oas.source", OASPath),
	)
	basePath, err := os.MkdirTemp("", "ogen-oas-")
	if err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}
	defer os.RemoveAll(basePath)

	fg := &filegetter.Filegetter{
		Client:     http.DefaultClient,
		KubeClient: e.kube,
	}
	dst := path.Join(basePath, path.Base(OASPath))
	if err = fg.GetFile(ctx, dst, OASPath, nil); err != nil {
		return nil, fmt.Errorf("failed to download file: %w", err)
	}
	contents, err = os.ReadFile(dst)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	return contents, nil
}

// oasContentDigest is a stable content hash of the OAS document bytes. Observe compares it against
// cr.Status.OASHash (set at the last Create/Update) so a change to the OAS source is detected as drift even
// when oasPath itself is unchanged.
func oasContentDigest(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

// getDocumentModelFromCR fetches and parses the CR's OAS document, also returning the content hash of the
// exact bytes that were parsed (so callers store a hash consistent with what they generated from).
func (e *external) getDocumentModelFromCR(ctx context.Context, cr *definitionv1alpha1.RestDefinition) (doc oas2jsonschema.OASDocument, oasHash string, err error) {
	ctx, span := oteltelemetry.Tracer().Start(ctx, "restdefinition.parse_oas")
	defer span.End()
	defer func() { oteltelemetry.RecordError(span, err) }()

	contents, err := e.fetchOASBytes(ctx, cr)
	if err != nil {
		return nil, "", err
	}
	doc, err = e.parser.Parse(contents)
	if err != nil {
		return nil, "", err
	}
	return doc, oasContentDigest(contents), nil
}

// noteAdoption raises an Event when a CRD was taken over from a RestDefinition that no longer exists.
//
// Adoption is a change of ownership and it happens without anyone asking, so it must be visible. Before
// #137 the same situation produced a hard failure an operator had to trace through provider source; the
// fix makes it self-healing, and the risk of a self-healing fix is that it heals silently and nobody ever
// learns the husk was there. The Event is what keeps "it just started working" from being the whole story.
func (e *external) noteAdoption(cr *definitionv1alpha1.RestDefinition, crdName string, outcome crd.ApplyOutcome) {
	if outcome.AdoptedFrom == "" {
		return
	}
	e.rec.Eventf(cr, corev1.EventTypeNormal, "CRDOwnershipAdopted",
		"took ownership of CRD %q from RestDefinition %q, which no longer exists",
		crdName, outcome.AdoptedFrom)
	e.log.Info("adopted CRD from a deleted RestDefinition",
		"crd", crdName, "previousOwner", outcome.AdoptedFrom, "newOwner", cr.Namespace+"/"+cr.Name)
}
