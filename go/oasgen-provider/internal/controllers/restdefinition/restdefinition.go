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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"

	"github.com/krateo-platformops/plumbing/env"
	"github.com/krateo-platformops/plumbing/kubeutil/dynamicwatch"
	"github.com/krateo-platformops/plumbing/kubeutil/event"
	"github.com/krateo-platformops/plumbing/kubeutil/eventrecorder"
	rtv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"

	"github.com/Masterminds/semver/v3"
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
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/crd"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/crd/collision"
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

func Setup(mgr ctrl.Manager, o controller.Options, metrics reconciler.MetricsRecorder, genCfg GeneratorConfig) error {
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
		genWatch: dynamicwatch.NewRegistry(mgr.GetCache()),
		genCfg:   genCfg,
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

// GeneratorConfig carries the process-level settings the controller cannot derive from the CR it is
// reconciling. Passed explicitly from main rather than read out of the environment here: main already
// exports Build as SERVICE_VERSION, and depending on a variable main happens to set would be a hidden
// coupling that no signature declares.
type GeneratorConfig struct {
	// Build identifies this binary (main.Build). See status.generatorBuild for why an empty value is
	// deliberately inert rather than treated as drift.
	Build string

	// RegenerateOnUpgrade re-renders every RestDefinition's CRD exactly once after an upgrade, which is
	// what carries a release's generation fixes to kinds that already exist. Default true. An operator
	// staging a large fleet sets OASGEN_PROVIDER_REGENERATE_ON_UPGRADE=false and accepts the trade the
	// default exists to avoid: generation fixes then do not apply until something else re-renders.
	RegenerateOnUpgrade bool
}

// generatorChanged reports whether this build is NEWER than the one that generated the CRD recorded by
// stored. Directional, deliberately, and that is the whole subtlety of this function.
//
// WHY NOT A PLAIN INEQUALITY, which is what this did first. A mismatch is symmetric: a DOWNGRADE looks
// exactly like an upgrade. Rolling the provider back would then re-render every CRD with the OLDER
// generator and silently revert the very fixes the newer one had applied -- and reverting a schema is not
// a cosmetic loss. A field that disappears from a CRD is PRUNED FROM EVERY LIVE CR at the next write, so
// a ten-minute rollback of a bad deploy would strip values the resources need, which is #419's failure
// mode arriving as a consequence of this feature.
//
// Raised by the openstack-ironic session from a real rollback: they went back to 0.50.0 while their CRDs
// had been written by a newer generator, leaving a cluster running an older generator against newer
// schemas. Benign as it stands -- nothing regenerates until something nudges it -- but with a symmetric
// trigger it would have reverted `spec.node_id`, which is the exact field whose absence had wedged a CR
// and cost them a day. Their words: "it is exactly the case nobody writes a test for."
//
// So a rollback leaves the newer schema in place. That is a stale-FORWARD schema, which is the safe
// direction: the generator is behind the artifact, no data is lost, and the next genuine upgrade
// re-renders it.
//
// ONLY MAJOR.MINOR.PATCH IS COMPARED. The stamp is `0.52.0-<sha>`, where the sha is a semver prerelease
// -- and prereleases sort BEFORE their release, so a literal comparison would make `0.52.0-abc` older
// than `0.52.0`, and two builds of one version would order by sha, which means nothing. Two stamps of
// the same release are the same source, so equal triples are no change.
//
// UNPARSEABLE FAILS CLOSED. If either side is not semver the direction cannot be established, so nothing
// is regenerated. That risks a silently inert feature if the stamp format ever changes, which is the
// lesser of the two risks: the other one reverts schemas.
func (c GeneratorConfig) generatorChanged(stored string) bool {
	if !c.RegenerateOnUpgrade || c.Build == "" {
		return false
	}
	if stored == "" {
		// Never recorded: the activation path every existing cluster takes when this ships. One
		// re-render, and such a CRD may genuinely be stale.
		return true
	}

	current, cerr := semver.NewVersion(c.Build)
	previous, perr := semver.NewVersion(stored)
	if cerr != nil || perr != nil {
		return false
	}
	return releaseTriple(current).GreaterThan(releaseTriple(previous))
}

// releaseTriple drops the prerelease and build metadata, leaving MAJOR.MINOR.PATCH. See
// generatorChanged for why the sha must not participate in the ordering.
func releaseTriple(v *semver.Version) *semver.Version {
	return semver.New(v.Major(), v.Minor(), v.Patch(), "", "")
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
	genWatch *dynamicwatch.Registry
	ctrl     crcontroller.Controller
	genCfg   GeneratorConfig
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
		genWatch: c.genWatch,
		ctrl:     c.ctrl,
		genCfg:   c.genCfg,
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
	genWatch *dynamicwatch.Registry
	ctrl     crcontroller.Controller
	genCfg   GeneratorConfig
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
// ensureGeneratedKindWatch registers a dynamic watch on the Kind this RestDefinition generates, so an
// instance appearing or being deleted enqueues the RestDefinition (issue 510).
//
// WITHOUT IT, BOTH DIRECTIONS WAIT FOR THE DRIFT POLL. Nothing else enqueues a RestDefinition when an
// instance of its generated Kind changes: For() watches the RestDefinition itself and
// ensureConfigurationWatch watches the Configuration Kind. So the secretRef grant was provisioned and
// REVOKED only on the next poll -- 3 minutes at the default, measured at 61-101 s on a consumer's cluster.
//
// THE DELETION DIRECTION IS THE SECURITY-RELEVANT ONE. A deleted resource's Secret stays readable by the
// controller's ServiceAccount until the poll comes round. Provisioning late is an availability delay;
// revoking late is a grant outliving the resource that justified it.
//
// A SEPARATE REGISTRY from cfgWatch. The registry dedupes by GVK and a Configuration Kind can never
// collide with a generated Kind, so sharing one would work -- but a deferred registration logs the same
// way for both, and the two have different consequences. Separate registries keep the log line attributable.
//
// Same "no unwatch" caveat as the Configuration watch: controller-runtime cannot remove a registered
// source from a running controller, so a watch on a generated Kind whose RestDefinition is later deleted
// idles for the life of the process.
func (e *external) ensureGeneratedKindWatch(cr *definitionv1alpha1.RestDefinition, gvr schema.GroupVersionResource) {
	if e.ctrl == nil || e.genWatch == nil {
		return // not wired for dynamic watches (e.g. under unit tests)
	}
	gvk := secretRefGeneratedGVK(cr, gvr)
	if gvk.Kind == "" {
		return
	}
	if err := e.genWatch.EnsureWatch(e.ctrl, gvk, enqueueRestDefinitionForGeneratedKind(e.kube)); err != nil {
		e.log.Debug("Dynamic watch registration on generated Kind deferred", "gvk", gvk.String(), "error", err)
	}
}

// enqueueRestDefinitionForGeneratedKind maps an instance event to the reconcile of the RestDefinition whose
// generated Kind it belongs to.
//
// THE GVK IS COMPUTED THE SAME WAY EVERYWHERE, via secretRefGeneratedGVK, which capitalises the
// author-written spec.resource.kind. A raw comparison here would silently match nothing for a definition
// whose kind is not capitalised -- exactly the defect a reviewer found in the collector, and the one
// rest-dynamic-controller's own definition lookup still carries (#530).
//
// The version comes from observedVersion, not from re-deriving it: Observe and the collector both use the
// DEPLOYED version, and a mid-flight bump would otherwise make the watch disagree with them about which
// Kind an instance belongs to.
func enqueueRestDefinitionForGeneratedKind(kube client.Client) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var rds definitionv1alpha1.RestDefinitionList
		if err := kube.List(ctx, &rds); err != nil {
			return nil
		}
		gvk := obj.GetObjectKind().GroupVersionKind()
		var reqs []reconcile.Request
		for i := range rds.Items {
			rd := &rds.Items[i]
			rdGVR := plurals.ToGroupVersionResource(schema.GroupVersionKind{
				Group:   rd.Spec.ResourceGroup,
				Version: observedVersion(rd),
				Kind:    rd.Spec.Resource.Kind,
			})
			if secretRefGeneratedGVK(rd, rdGVR) == gvk {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(rd)})
			}
		}
		return reqs
	}
}

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

		// Ready=Deleting. This path answers like any other, and provider-runtime's contract is that every
		// successful Observe sets Ready.
		//
		// Set BEFORE the two error returns below, though at the pinned version that no longer changes what
		// they report: v1.8.2's markNotObserved answers a resource carrying a deletion timestamp with
		// Deleting rather than NotObserved, whatever the condition on the object says. On v1.8.0 and v1.8.1
		// it checked only the condition, so setting it first was the one thing standing between a failed
		// uninstall and a Ready that read "we could not tell" about an object whose fate is not in doubt.
		//
		// Kept, not because the runtime needs it, but because Observe should state what it knows: that this
		// RestDefinition is being deleted is the provider's own answer, not something a caller should have
		// to see inferred from a timestamp. It also keeps this branch correct if the pin ever moves back.
		cr.SetConditions(rtv1.Deleting())

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
		// PERMANENT: objects.CreateK8sObject is os.ReadFile plus a template render, with no cluster
		// access, so the same inputs fail identically on every reconcile. Erroring here skips
		// provider-runtime's create-recovery guard, which runs only when Observe ANSWERS -- so a
		// RestDefinition whose create handshake is unresolved would be wedged forever on every runtime
		// version (#266). Answer, and carry the diagnosis on the object.
		return e.observePermanentFailure(cr, "the controller Deployment manifest", err)
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
	// EVERY document, not just spec.oasPath. A RestDefinition whose verbs span several documents would
	// otherwise have an invisible hole: editing the second document leaves the stored hash unchanged,
	// Observe sees no drift, and the CRD never regenerates -- the resource stays Ready while serving a
	// schema that no longer matches its own document.
	if byPath, ferr := e.fetchAllOASBytes(ctx, cr); ferr != nil {
		e.log.Debug("Could not fetch OAS for content-drift check; skipping", "error", ferr)
	} else if h := compositeOASDigest(byPath); cr.Status.OASHash != h {
		e.log.Debug("OAS document content changed", "status", cr.Status.OASHash, "current", h)
		return e.observeDrift(cr, "the OpenAPI document changed; regenerating the CRD")
	}

	// spec.resource can change the generated schema while the OAS is byte-identical -- additionalStatusFields
	// and includedSpecFields are append-only rather than immutable, precisely so a KOG chart adding a status
	// field is upgradable in place. status.resourceHash exists for this, Create stores it and Update compares
	// it and regenerates. Nothing reached that comparison: Update only runs when Observe reports drift, and
	// Observe checked the OAS content, the auth-secret references and the DEPLOYMENT digest (RBAC, ConfigMap,
	// Deployment -- never the CRD), so an append was accepted, reported "up to date", and did nothing. The CRD
	// kept its old status schema while its own RestDefinition described a different one, with no event and no
	// warning, and the only route to the new schema was deleting the RestDefinition -- which destroys the CRD
	// and every CR of that kind (#224).
	//
	// Deliberately hashes the whole of spec.resource rather than only the schema-shaping fields: including a
	// field that cannot change the CRD costs one redundant regenerate-and-redeploy, while omitting one that
	// can reintroduces exactly this silence. Same trade the OAS-content check above already makes.
	if rh := resourceHash(cr); cr.Status.ResourceHash != rh {
		e.log.Debug("spec.resource changed", "status", cr.Status.ResourceHash, "current", rh)
		return e.observeDrift(cr, "spec.resource changed; regenerating the CRD")
	}

	// The generator is the fifth input and the only one nothing hashed. The three checks above compare
	// what goes IN; this one compares what transforms it. Without it an upgrade that fixes the schema
	// generator left every existing CRD on the old schema while reporting Ready, and the operator's only
	// signal was the absence of the fix they had just upgraded for (#453).
	if e.genCfg.generatorChanged(cr.Status.GeneratorBuild) {
		e.log.Debug("Generator build changed", "status", cr.Status.GeneratorBuild, "current", e.genCfg.Build)
		return e.observeDrift(cr, "the provider that generated this CRD has been upgraded; regenerating the CRD")
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
			return e.observeDrift(cr, "the Configuration's authentication secret references changed; redeploying the controller")
		}

		// AND THE OBJECTS THEMSELVES, not only the digest of what they were built from (#473).
		//
		// The digest above answers "did the references change". It cannot express "does the Role exist", so
		// a Role deleted by hand, lost to a namespace rebuild, or removed by the pre-#440 legacy sweep of a
		// RestDefinition that collided on the old group-less name left this check satisfied and Update
		// unreachable -- and syncAuthSecretRBAC runs only from Create and Update. The repair arrived only
		// through an unrelated change to the spec, the OAS or the references, which may be never.
		//
		// Scoped to the namespaces the status says are provisioned, because that is exactly the set Sync
		// wrote and the only set whose absence is a regression rather than a namespace that never had RBAC.
		//
		// A FAILED EVALUATION IS NOT DRIFT, matching every other check in this function: a transient API
		// error must not flap a healthy resource into Creating.
		if len(cr.Status.AuthSecretRBACNamespaces) > 0 {
			roleName := deploy.AuthSecretRoleName(gvr)
			if missing, merr := deploy.MissingAuthSecretRBAC(ctx, e.kube, roleName, cr.Status.AuthSecretRBACNamespaces); merr != nil {
				e.log.Debug("Could not check auth secret RBAC for drift; skipping", "error", merr)
			} else if len(missing) > 0 {
				e.log.Debug("Auth secret RBAC is missing", "role", roleName, "namespaces", missing)
				return e.observeDrift(cr, fmt.Sprintf(
					"the auth-secret RBAC %q is missing in namespace(s) %s, so the controller cannot read its credential; reprovisioning",
					roleName, strings.Join(missing, ", ")))
			}
		}
	}

	// THE SAME TWO CHECKS FOR THE secretRef GRANT (#510), and deliberately NOT inside the configurationGVR
	// gate above: these references come from instances of the GENERATED Kind, which exists whether or not the
	// RestDefinition has a Configuration CRD at all.
	//
	// Without these, syncSecretRefRBAC is unreachable in the normal flow. It runs only from Create and
	// Update; the generated CRD is created INSIDE Create, so Create's collection always sees zero instances;
	// and nothing else triggers Update. Measured on a cluster before this existed: 8 CRs in 7 namespaces,
	// zero Roles, empty status, no log lines.
	//
	// A FAILED EVALUATION IS NOT DRIFT, matching every other check in this function.
	if deploy.DeclaresSecretRef(cr.Spec.Resource.VerbsDescription) {
		generatedGVK := secretRefGeneratedGVK(cr, gvr)
		if refs, cerr := deploy.CollectSecretRefNamesForAll(ctx, e.kube, generatedGVK, cr.Spec.Resource.VerbsDescription, e.log); cerr != nil {
			e.log.Debug("Could not collect secretRef secret names for drift check; skipping", "error", cerr)
		} else if h, herr := deploy.AuthSecretRefsDigest(refs); herr != nil {
			e.log.Debug("Could not hash secretRef secret names for drift check; skipping", "error", herr)
		} else if cr.Status.SecretRefDigest != h {
			e.log.Debug("secretRef secret names changed", "status", cr.Status.SecretRefDigest, "current", h)
			return e.observeDrift(cr, "the secretRef resolvers of this kind's instances reference a different "+
				"set of Secrets; reprovisioning their RBAC")
		}
	}

	// AND THE OBJECTS THEMSELVES, for the same reason as #473: the digest cannot express "does the Role
	// exist", so a Role deleted by hand or lost to a namespace rebuild would leave this check satisfied and
	// Update unreachable.
	if len(cr.Status.SecretRefRBACNamespaces) > 0 && deploy.DeclaresSecretRef(cr.Spec.Resource.VerbsDescription) {
		roleName := deploy.SecretRefRoleName(gvr)
		if missing, merr := deploy.MissingAuthSecretRBAC(ctx, e.kube, roleName, cr.Status.SecretRefRBACNamespaces); merr != nil {
			e.log.Debug("Could not check secretRef RBAC for drift; skipping", "error", merr)
		} else if len(missing) > 0 {
			e.log.Debug("secretRef RBAC is missing", "role", roleName, "namespaces", missing)
			return e.observeDrift(cr, fmt.Sprintf(
				"the secretRef RBAC %q is missing in namespace(s) %s, so the controller cannot read the "+
					"Secrets its resolvers name; reprovisioning", roleName, strings.Join(missing, ", ")))
		}
	}

	// Before a version-scoped controller starts watching, make sure the instances it is supposed to own
	// actually carry the label it selects on. Reported, not fatal: failing the reconcile here would stop a
	// working resource over a migration concern, and the next pass retries.
	e.backfillVersionLabelsReporting(ctx, cr, gvk, hasSecuritySchemes)

	// Drive the served-version prune to completion. Reporting not-up-to-date here is what makes Update
	// run, and Update prunes exactly what this counted -- the SAME predicate decides both, which is the
	// only reason this cannot ping-pong. Two predicates that disagree would have Observe ask forever for
	// work Update does not do, which is the shape of the 0.28.0 readiness loop.
	//
	// A failure to evaluate is NOT drift, matching the OAS-content and auth-secret checks above: a
	// transient List failure must not flap a healthy resource into Creating, and the next pass retries.
	if prunable, _, perr := e.prunableServedVersions(ctx, cr, gvk, gvr); perr != nil {
		e.log.Debug("Could not evaluate served-version prune; skipping", "error", perr)
	} else if len(prunable) > 0 {
		e.log.Debug("Served versions are prunable", "versions", prunable)
		return e.observeDrift(cr, fmt.Sprintf("retiring served CRD versions that are no longer declared: %v", prunable))
	}

	dig, err := deploy.Deploy(ctx, e.kube, opts)
	if err != nil {
		// Deploy renders manifests AND dry-run Applies them, and used to report both as the same bare
		// error. Only the render half is permanent; a dry-run Apply can fail because the apiserver is
		// briefly unavailable, which must still retry. deploy.PermanentRenderError distinguishes them at
		// the source rather than by inspecting the message here -- matching on a string would couple this
		// to wording and silently reopen the wedge when it is reworded (#266).
		var perr *deploy.PermanentRenderError
		if errors.As(err, &perr) {
			return e.observePermanentFailure(cr, perr.What, perr.Err)
		}
		return reconciler.ExternalObservation{}, err
	}

	if cr.Status.Digest != dig {
		e.log.Debug("Rendered resources digest changed", "status", cr.Status.Digest, "rendered", dig)
		return e.observeDrift(cr, "the rendered resources changed; redeploying")
	}

	dig, err = deploy.Lookup(ctx, e.kube, opts)
	if err != nil {
		return reconciler.ExternalObservation{}, err
	}
	if cr.Status.Digest != dig {
		e.log.Debug("Deployed resources digest changed", "status", cr.Status.Digest, "deployed", dig)
		return e.observeDrift(cr, "the deployed resources drifted from the render; redeploying")
	}

	err = manageFinalizers(ctx, e.kube, cr, e.log.Debug)
	if err != nil {
		return reconciler.ExternalObservation{}, fmt.Errorf("managing finalizers: %w", err)
	}

	// Ready must mean "you can apply a CR of this kind". Nothing above establishes that.
	//
	// Everything checked so far is about the RENDER and the controller: the Deployment is ready, the
	// ConfigMap digest matches, finalizers are managed. deploy.Lookup hashes the ConfigMap and fetches
	// nothing else -- no CRD is read anywhere on this path. So Available() used to be set while the
	// apiserver was not yet serving the generated kind, and "wait for Ready, then apply" failed with
	// `no matches for kind`, which is indistinguishable from a missing CRD (#314).
	//
	// Measured on a fresh cluster before this guard: five RestDefinitions reported Ready at t+148s while
	// five of the ten generated CRDs appeared at t+149s. Small window, reproducible, and the failure it
	// produces costs far more to diagnose than it does to hit.
	if served, pending, serr := e.generatedKindsServed(ctx, cr); serr != nil {
		// Could not determine servability. NOT the same as "not served": claiming Available here would be
		// the guess, and claiming Creating would hide a working install behind a transient API error.
		//
		// Returned UNWRAPPED, deliberately. Under provider-runtime v1.8.0 that makes the runtime set
		// Ready=Unknown/NotObserved, which is what this branch has always meant and previously could not
		// say: before v1.8.0 the error merely left the condition as it was, so a resource that had been
		// Available went on asserting it for as long as this check kept failing. Ignorance now has its own
		// answer, and reconciler.ReadyAssessed is precisely the wrong wrapper here -- it promises that
		// Observe assessed Ready, and the entire content of this branch is that it could not.
		//
		// The cost is real and accepted: one failed read of the served kinds drops a healthy resource from
		// Available to Unknown. That is the honest direction, and it is not merely pessimism -- this read
		// goes against the apiserver's own discovery of the generated kind, so a failure here correlates
		// with the consumer's `kubectl apply` of that kind failing too. The ignorance is about the very
		// thing Ready promises.
		return reconciler.ExternalObservation{}, fmt.Errorf("checking whether the generated kinds are served: %w", serr)
	} else if !served {
		e.log.Debug("Generated kind not yet served by the apiserver; holding Creating",
			"name", cr.Name, "pending", pending)
		cr.SetConditions(rtv1.Creating().WithMessage(
			fmt.Sprintf("the generated CRD is applied but the apiserver is not serving %s yet", pending)))
		// UpToDate so the runtime waits in the observe path: there is nothing for Update to fix, the
		// apiserver simply has not finished. Reporting drift here would send Update to re-render a CRD that
		// is already correct.
		return reconciler.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: true,
		}, nil
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

	docs, oasHash, err := e.getDocumentSetFromCR(ctx, cr)
	if err != nil {
		return fmt.Errorf("getting document model from CR: %w", err)
	}
	doc := docs.Default()

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
		// renderGeneratedCRDs, not generateAndApplyCRDs: the stamp has to travel with the render, and
		// persistCreatePhaseStatus below snapshots the whole status, so setting it here persists it.
		if err := e.renderGeneratedCRDs(ctx, cr, gvk, docs, hasSecuritySchemes, oasHash); err != nil {
			return err
		}

		if err := e.persistCreatePhaseStatus(ctx, cr, gvk, hasSecuritySchemes); err != nil {
			return fmt.Errorf("updating status: %w", err)
		}
		e.log.Debug("Applied CRD", "Kind:", cr.Spec.Resource.Kind, "Group:", cr.Spec.ResourceGroup)
		e.rec.Eventf(cr, corev1.EventTypeNormal, "RestDefinitionCreating",
			"RestDefinition '%s/%s' creating", cr.Spec.Resource.Kind, cr.Spec.ResourceGroup)

		return nil
	}

	// THE CRD EXISTS, AND THAT IS NOT THE SAME AS BEING CURRENT (#541).
	//
	// Observe's generator-changed drift check is unreachable on the reconcile that routes here: it
	// returns early while the generated Kind's controller Deployment is absent, so a provider upgrade
	// whose first reconciles beat that Deployment into existence arrives at Create instead -- and Create
	// used to skip generation because the CRD was present, while still recording the running build.
	//
	// Checked HERE rather than relied upon from Observe, because this path is reached precisely when
	// Observe did not get that far.
	if e.genCfg.generatorChanged(cr.Status.GeneratorBuild) {
		e.log.Debug("Generator build changed and the CRD already exists; regenerating before stamping",
			"status", cr.Status.GeneratorBuild, "current", e.genCfg.Build)
		if err := e.renderGeneratedCRDs(ctx, cr, gvk, docs, hasSecuritySchemes, oasHash); err != nil {
			return fmt.Errorf("regenerating the CRD after a provider upgrade: %w", err)
		}
	}

	configurationGVR := getConfigurationGVR(cr, hasSecuritySchemes)
	opts := e.deployOptions(cr, gvk, gvr, configurationGVR, false)
	// Before a version-scoped controller starts watching, make sure the instances it is supposed to own
	// actually carry the label it selects on. Reported, not fatal: failing the reconcile here would stop a
	// working resource over a migration concern, and the next pass retries.
	e.backfillVersionLabelsReporting(ctx, cr, gvk, hasSecuritySchemes)

	// BEFORE Deploy, not after (#473). The auth-secret Role is the ONLY grant that lets the generated
	// controller read its credential, now that the blanket cluster-wide `secrets` grant is gone from the
	// RDC ClusterRole. Deploying the Deployment first would start a pod whose very first credential read
	// races the Role that authorises it -- a 403 on every fresh install, inside one reconcile but real.
	//
	// Safe in this order: the sync lists Configuration instances and RENDERS the ServiceAccount name from
	// the same template Deploy uses (SelfServiceAccountName does not fetch it), so nothing here requires
	// Deploy to have run. A RoleBinding may name a ServiceAccount that does not exist yet; it simply
	// grants nothing until it does, which is the window Deploy then closes.
	if err := e.syncAuthSecretRBAC(ctx, cr, gvr); err != nil {
		return fmt.Errorf("syncing auth secret RBAC: %w", err)
	}

	// #510, and deliberately NOT folded into the call above. The two grants are collected from different
	// kinds -- auth refs from the Configuration Kind, secretRef targets from the GENERATED Kind -- and
	// SyncAuthSecretRBAC revokes any namespace in the caller's record that its collected set no longer
	// names. Sharing either the Role name or the status field between them makes each caller revoke the
	// other's namespaces on every reconcile.
	if err := e.syncSecretRefRBAC(ctx, cr, gvr); err != nil {
		return fmt.Errorf("syncing secretRef RBAC: %w", err)
	}

	dig, err := deploy.Deploy(ctx, e.kube, opts)
	if err != nil {
		return fmt.Errorf("installing controller: %w", err)
	}
	if configurationGVR != (schema.GroupVersionResource{}) {
		e.ensureConfigurationWatch(cr)
		e.ensureGeneratedKindWatch(cr, gvr)
	}

	// Prune served versions superseded by this one that are safe to drop without migrating any instance
	// (not current, not vacuum, not in storedVersions) — bounds version accumulation from repeated bumps.
	// Best-effort: a prune failure must not fail the reconcile.
	if pruned, perr := crd.PruneServedVersions(ctx, e.kube, gvr.GroupResource(), gvk.Version); perr != nil {
		e.log.Debug("Pruning superseded served versions failed (non-fatal)", "error", perr)
	} else if len(pruned) > 0 {
		e.log.Debug("Pruned superseded served versions", "versions", pruned)
	}

	// Everything this phase establishes is written onto cr.Status, and then SNAPSHOTTED (#263).
	//
	// Writing here rather than into a parallel struct is what makes the snapshot complete by
	// construction. syncAuthSecretRBAC above already set AuthSecretDigest and AuthSecretRBACNamespaces
	// on cr.Status in place, and a future step that sets another field will land in the same place --
	// with nothing to remember and no second list to keep in step. The previous shape, a hand-filled
	// struct, dropped exactly those two fields on its first outing.
	cr.Status.Resource = definitionv1alpha1.KindApiVersion{
		Kind:       gvk.Kind,
		APIVersion: gvk.GroupVersion().String(),
	}
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
	// NO HASH STAMPS HERE, AND NO GeneratorBuild STAMP (#541, #627). This phase does not render, and all
	// three fields mean "the inputs the CRD was rendered from". renderGeneratedCRDs writes them, wherever
	// the render happened -- including the branch above, which regenerates when the generator moved under
	// an existing CRD.
	//
	// status.oasHash and status.resourceHash used to be written here unconditionally. On the path where
	// the CRD already exists and the generator has NOT moved, nothing renders -- and advancing the hashes
	// anyway told the next Observe that the live document had already been generated from, permanently.
	// Leaving them alone is what makes that pass self-heal: Observe sees the drift it should see, and
	// Update regenerates.
	//
	// The old comment here said only "the build
	// that rendered this CRD". renderGeneratedCRDs writes it, wherever the render happened -- including
	// the branch above, which regenerates when the generator moved under an existing CRD.

	// Taken AFTER every in-place step above and immediately before the write. A field set after this
	// line is not in the snapshot and so is never persisted -- that is the one ordering rule this shape
	// still has, and it is a single line to check rather than a nine-member list.
	st := snapshotStatus(cr)

	// Retried on conflict for the same reason phase 1 is (#259). This write is the one that records
	// status.digest, status.oasHash and status.resourceHash -- the values Observe compares against to
	// decide whether anything drifted -- so losing it does not merely lose a status field: the next
	// Observe sees an empty digest, reports drift that does not exist, and Update redeploys on every
	// reconcile. A conflict here is as ordinary as in phase 1, and it was the only one of the two left
	// unprotected.
	if err := e.persistStatusWithRetry(ctx, cr, st.applyTo); err != nil {
		return fmt.Errorf("updating status: %w", err)
	}

	e.log.Debug("Created RestDefinition", "Kind:", cr.Spec.Resource.Kind, "Group:", cr.Spec.ResourceGroup)
	e.rec.Eventf(cr, corev1.EventTypeNormal, "RestDefinitionCreating",
		"RestDefinition '%s/%s' creating", cr.Spec.Resource.Kind, cr.Spec.ResourceGroup)
	return nil
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

// backfillVersionLabelsReporting runs the backfill and REPORTS A FAILURE, which it previously did only at
// Debug -- and `debug` defaults to false (main.go:44), so on a default install the failure was emitted
// nowhere at all (#425).
//
// WHY THAT SILENCE WAS WORSE THAN AN ORDINARY MISSING LOG LINE. The failure it hid is unobservable by any
// other route. Version-scoped watching filters SERVER-SIDE on both ListFunc and WatchFunc, so an
// unlabelled instance never enters the informer cache, is never enqueued, and cannot carry a condition or
// an event. The CR is created, looks healthy, and belongs to nobody. This log line is therefore not one
// diagnostic among several: on a default install it was the ONLY place the failure could ever have been
// reported, and it was switched off.
//
// THE ASYMMETRY IS THE ARGUMENT. ensureVersionPolicyBestEffort, directly below, reports its failure with
// Warn plus an event -- and that mechanism cannot reach the unlabelled state at all, because the policy it
// ensures DENIES an unstampable write rather than admitting one. So the loud mechanism was the one that
// could not fail into this state, and the quiet one was the only repair for it. Same backwards asymmetry
// as #409, where a stanza doing no work warned while a property leaving the schema did not.
//
// ONE HELPER FOR THREE CALL SITES, deliberately. The three sites were identical copies and all three were
// wrong in the same way, which is exactly how the third one would have been missed by a fix that edited
// them in place.
//
// Not a condition on the RestDefinition and not fatal: the RestDefinition itself is healthy, and failing
// the observation would stop reconciling a resource that is otherwise fine -- the same reasoning
// ensureVersionPolicyBestEffort already documents for its own non-fatality. The instances are what is
// affected, and they are precisely what cannot be annotated.
func (e *external) backfillVersionLabelsReporting(ctx context.Context, cr *definitionv1alpha1.RestDefinition,
	gvk schema.GroupVersionKind, hasSecuritySchemes bool) {
	if bferr := e.backfillVersionLabels(ctx, cr, gvk, hasSecuritySchemes); bferr != nil {
		e.log.Warn("Could not backfill the oas-version label; instances of this kind that carry no label "+
			"are matched by no controller's watch and cannot report that themselves",
			"kind", gvk.Kind, "group", gvk.Group, "error", bferr)
		e.rec.Eventf(cr, corev1.EventTypeWarning, "VersionLabelNotBackfilled",
			"could not backfill the %s label on instances of %s: %v; such instances are never reconciled "+
				"and carry no condition saying so", generation.VersionLabel, gvk.Kind, bferr)
	}
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
// IT DOES WARN ONCE PER RECONCILE ON A CLUSTER WITHOUT THE API, AND THAT IS NOW CORRECT. This comment
// used to say the opposite -- that EnsureVersionPolicy "already returns nil on a cluster that does not
// serve the API (< 1.36), so this stays quiet there". It does not: all three IsUnsupported branches
// return &ErrPolicyAPIUnsupported (policy.go:297, :313, :333), a non-nil error, so the warning and the
// VersionPolicyNotEnsured event below fire on every pass.
//
// The comment was true of a world that ended at 0.28.0, when the chart floor moved 1.33 -> 1.36 and an
// absent MutatingAdmissionPolicy API became an error "where it used to be tolerated". A cluster without
// the API can now only be reached by bypassing the chart, which refuses to install below 1.36 -- and in
// that configuration instances ARE being admitted unlabelled and matched by no controller's watch. That
// is a persistent, serious misconfiguration, so persistent noise is proportionate rather than spam, and
// the apiserver aggregates duplicate events rather than creating one per pass.
//
// The second stale claim, also corrected: this is NOT "hygiene today because rest-dynamic-controller
// stamps the label itself". That fallback existed in 0.27.0 and was REMOVED in 0.28.0, replaced by
// backfillVersionLabel, which stamps the same value by selecting on label absence. So the policy is
// load-bearing now rather than becoming so later: an unlabelled instance matches no controller's watch,
// is never observed, and -- because the listwatcher filters SERVER-SIDE on both ListFunc and WatchFunc --
// cannot carry a condition or an event saying so.
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

// renderGeneratedCRDs renders this RestDefinition's CRDs and then records the build that rendered them.
// It is the ONLY place Status.GeneratorBuild is written, and that is the whole point of its existing.
//
// # The defect it closes (#541)
//
// The stamp used to be written at the end of Create's second phase, unconditionally. On a provider
// upgrade whose first reconciles land while the generated Kind's controller Deployment is still absent,
// Observe returns early -- ResourceExists is deployOk, which is false on IsNotFound -- and so never
// reaches the generator-changed drift check. provider-runtime then calls Create, Create finds the CRD
// already present and SKIPS generation, falls through, and stamped the running build anyway.
//
// The result was a CRD still carrying the schema an older build rendered, under a status field asserting
// the current build had rendered it. Worse than the field being absent: absent is honest, and anything
// using it as a proxy for "this schema is current" got a confident wrong answer.
//
// AND IT SEALED ITSELF. generatorChanged returns true unconditionally when the stored value is empty --
// the activation branch every pre-existing cluster takes exactly once -- and the Create path consumed
// that branch without rendering, then wrote stored = current. No later reconcile, restart or re-upgrade
// to the same version retried it. Recovery needed a status patch clearing the field, and the operator had
// no signal that they needed one.
//
// Measured on a consumer's cluster: four rounds of "Dynamic Controller not deployed yet" across all six
// controllers, then six Creates, then the first "already deployed" AFTER them; 12 of 12 CRDs unchanged
// while status.generatorBuild moved to the new build on 6 of 6 RestDefinitions. A second consumer's
// upgrade of the same images took the Update branch and regenerated 32 of 32 correctly, which is what
// makes it a race rather than a version pair.
//
// # Why one function rather than a rule
//
// "Stamp only where you rendered" was already the rule, and the Update path's own comment stated it
// correctly: "the CRD was just rendered by THIS build whatever provoked the render, so recording a
// different build would be false." That reasoning is right, and it sat next to a sibling call site where
// the premise did not hold. A rule written in a comment is enforced by whoever reads it; a single
// function that renders and then stamps cannot be half-used by a third call site.
func (e *external) renderGeneratedCRDs(ctx context.Context, cr *definitionv1alpha1.RestDefinition, gvk schema.GroupVersionKind, docs *oas2jsonschema.DocumentSet, hasSecuritySchemes bool, oasHash string) error {
	if err := e.generateAndApplyCRDs(ctx, cr, gvk, docs, hasSecuritySchemes); err != nil {
		return err
	}
	// ALL THREE drift inputs are stamped here, and nowhere else (#627).
	//
	// The reasoning above is about GeneratorBuild and it was never specific to it: a bookkeeping value
	// that attests a render must not be writable by a caller that did not render. #541 applied it to one
	// input of three and the other two kept their unconditional writes at the end of Create's second
	// phase -- so an OAS content change or a spec.resource change that arrived via Create had its hash
	// advanced with nothing rendered.
	//
	// THAT LATCHED PERMANENTLY, which is why this is a worse defect than the one above rather than the
	// same one again. Observe compares these two hashes against the live document and spec; once the
	// stored value equals what it is compared against, no later reconcile sees drift, so the CRD keeps an
	// older schema forever while the RestDefinition reports Ready. Measured by the installer session: an
	// OAS-only change left the CRD byte-identical while status.oasHash held the digest of the NEW
	// document, still unchanged ten minutes later and unrecoverable by any generator move.
	//
	// resourceHash is recomputed here rather than passed in, because its input is `cr` -- which this
	// function already has, and which generateAndApplyCRDs does not modify. One source for the value
	// removes the question of whether a caller's copy was stale.
	cr.Status.GeneratorBuild = e.genCfg.Build
	cr.Status.OASHash = oasHash
	cr.Status.ResourceHash = resourceHash(cr)
	return nil
}

func (e *external) generateAndApplyCRDs(ctx context.Context, cr *definitionv1alpha1.RestDefinition, gvk schema.GroupVersionKind, docs *oas2jsonschema.DocumentSet, hasSecuritySchemes bool) (err error) {
	rendered, err := render.CRDs(ctx, cr, gvk, docs, hasSecuritySchemes)
	if err != nil {
		return err
	}

	// Refuse a generated name that a built-in resource already serves, BEFORE anything is applied (#200).
	if cerr := e.guardAgainstBuiltInCollision(ctx, cr, rendered); cerr != nil {
		return cerr
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

	// ONTO STATUS AS WELL AS THE EVENT (#462), and the assignment is UNCONDITIONAL.
	//
	// The Event is the right channel for "this just happened" and stays. It was wrong only as the SOLE
	// record of a standing condition: the apiserver's default --event-ttl is one hour, while a declaration
	// naming a field that does not exist stays wrong until someone edits it. Four definitions on a live
	// fleet carried eight generation errors between them, all emitted at install time and garbage-collected
	// long before anyone had a reason to look; they were found by rendering the definitions by hand.
	//
	// UNCONDITIONAL because the clearing case is the one that makes this readable. Assigning only when
	// warnings exist would leave a fixed declaration still complaining, and a surface that cannot go quiet
	// is one nobody trusts enough to check. This runs on create and whenever the OAS or spec.resource
	// changes -- which is exactly when the answer can have changed.
	//
	// Status, not a condition: these are N independent facts rather than one binary state, and a list
	// stays enumerable. `kubectl get restdefinition -A -o jsonpath=...{.status.schemaWarnings}` is a fleet
	// sweep, which is what someone otherwise has to write a bespoke renderer to get.
	//
	// It does NOT touch Ready or Synced. See the field's own comment for why.
	all := append(append([]error{}, rendered.GenerationWarnings...), rendered.ValidationWarnings...)
	cr.Status.SchemaWarnings = renderWarnings(all)
	if len(all) > 0 {
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

	// Same reasoning as the two warnings above -- a misconfiguration that is only discoverable at reconcile
	// time, said once at generation instead. See warnOnApiRefWithoutSnowplow for why it warns and never
	// refuses.
	e.warnOnApiRefWithoutSnowplow(cr, RDCtemplateConfigmapPath)

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

	docs, oasHash, err := e.getDocumentSetFromCR(ctx, cr)
	if err != nil {
		return fmt.Errorf("getting document model from CR: %w", err)
	}
	doc := docs.Default()

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
	generatorChanged := e.genCfg.generatorChanged(cr.Status.GeneratorBuild)
	if oasHash != cr.Status.OASHash || resHash != cr.Status.ResourceHash || generatorChanged {
		e.log.Debug("Regenerating CRD",
			"oasChanged", oasHash != cr.Status.OASHash,
			"resourceChanged", resHash != cr.Status.ResourceHash,
			"generatorChanged", generatorChanged)
		// The stamp travels with the render, in renderGeneratedCRDs. This site's own comment used to
		// state the rule correctly -- "the CRD was just rendered by THIS build whatever provoked the
		// render, so recording a different build would be false" -- while a sibling call site stamped on
		// a branch that had rendered nothing (#541). A rule in a comment is enforced by whoever reads it.
		if gerr := e.renderGeneratedCRDs(ctx, cr, gvk, docs, hasSecuritySchemes, oasHash); gerr != nil {
			return fmt.Errorf("regenerating CRD: %w", gerr)
		}
	}

	// Retire served versions nothing is using any more. OUTSIDE the regeneration gate above, because a
	// version becomes prunable when its last INSTANCE goes away -- which has nothing to do with whether
	// this RestDefinition's spec or OAS changed. Gating this on regeneration would mean a version stays
	// served until something unrelated happens to edit the definition, which on a stable install may be
	// never.
	//
	// Reported, not fatal. A CRD that still serves a retired version works; it is dead weight and a
	// stale endpoint, not an outage. Failing the reconcile over it would take a healthy resource down to
	// tidy up, and Observe will ask again on the next pass.
	if perr := e.pruneStaleServedVersions(ctx, cr, gvk, gvr); perr != nil {
		e.log.Warn("Could not prune stale served versions", "error", perr)
		e.rec.Eventf(cr, corev1.EventTypeWarning, "ServedVersionPruneFailed",
			"could not retire unused served version(s): %v", perr)
	}

	configurationGVR := getConfigurationGVR(cr, hasSecuritySchemes)
	opts := e.deployOptions(cr, gvk, gvr, configurationGVR, false)
	// Before a version-scoped controller starts watching, make sure the instances it is supposed to own
	// actually carry the label it selects on. Reported, not fatal: failing the reconcile here would stop a
	// working resource over a migration concern, and the next pass retries.
	e.backfillVersionLabelsReporting(ctx, cr, gvk, hasSecuritySchemes)

	// BEFORE Deploy, not after (#473). The auth-secret Role is the ONLY grant that lets the generated
	// controller read its credential, now that the blanket cluster-wide `secrets` grant is gone from the
	// RDC ClusterRole. Deploying the Deployment first would start a pod whose very first credential read
	// races the Role that authorises it -- a 403 on every fresh install, inside one reconcile but real.
	//
	// Safe in this order: the sync lists Configuration instances and RENDERS the ServiceAccount name from
	// the same template Deploy uses (SelfServiceAccountName does not fetch it), so nothing here requires
	// Deploy to have run. A RoleBinding may name a ServiceAccount that does not exist yet; it simply
	// grants nothing until it does, which is the window Deploy then closes.
	if err := e.syncAuthSecretRBAC(ctx, cr, gvr); err != nil {
		return fmt.Errorf("syncing auth secret RBAC: %w", err)
	}

	// #510, and deliberately NOT folded into the call above. The two grants are collected from different
	// kinds -- auth refs from the Configuration Kind, secretRef targets from the GENERATED Kind -- and
	// SyncAuthSecretRBAC revokes any namespace in the caller's record that its collected set no longer
	// names. Sharing either the Role name or the status field between them makes each caller revoke the
	// other's namespaces on every reconcile.
	if err := e.syncSecretRefRBAC(ctx, cr, gvr); err != nil {
		return fmt.Errorf("syncing secretRef RBAC: %w", err)
	}

	dig, err := deploy.Deploy(ctx, e.kube, opts)
	if err != nil {
		return fmt.Errorf("installing controller: %w", err)
	}
	if configurationGVR != (schema.GroupVersionResource{}) {
		e.ensureConfigurationWatch(cr)
		e.ensureGeneratedKindWatch(cr, gvr)
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
	// No status.oasHash write here either (#627): renderGeneratedCRDs stamps it inside the gate above.
	// This site was harmless -- the gate's own condition includes `oasHash != cr.Status.OASHash`, so
	// reaching here without a render means the value was already equal -- but "harmless because another
	// condition happens to cover it" is the property that stopped holding at Create's sibling site.

	// Taken AFTER every in-place step above and immediately before the write, same rule as Create's
	// phase 2. Update already sets the Creating condition a few lines up, which is what applyTo merges,
	// so the two call sites want identical behaviour.
	st := snapshotStatus(cr)

	// Retried on conflict, through the same helper both Create phases use (#264). This was the last of
	// the three status writes left as a bare Status().Update.
	//
	// It cannot WEDGE the resource the way a lost create-phase write can -- provider-runtime's refusal
	// to proceed applies to an unresolved CREATE handshake, and by the time Update runs that handshake is
	// long resolved, so a conflict here just fails the reconcile and the next one retries. What it costs
	// instead is a redeploy loop that resolves itself: a lost write loses status.digest, status.oasHash
	// and status.resourceHash, so the next Observe compares an empty or stale digest against a freshly
	// rendered one, reports drift that does not exist, and Update runs again. Wasteful rather than
	// terminal -- and on a bulk install it competes for exactly the API server attention that caused the
	// conflict in the first place.
	if err := e.persistStatusWithRetry(ctx, cr, st.applyTo); err != nil {
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
		// WARN, NOT DEBUG, AND THE COMPARISON IS THE ARGUMENT. Its counterpart syncAuthSecretRBAC is FATAL
		// (returns an error, so the runtime conditions it and emits an event). This one cannot be: it runs
		// in Delete, so by the time it executes there is no object left to carry a condition and an event
		// would be orphaned. That makes the log line the ONLY durable channel, and it was the quietest one.
		//
		// rest-dynamic-controller does the analogous teardown of its own self-granted secretRef RBAC at
		// Warn at all five of its call sites (restResources.go:1295, 1345, 1367, 1408, 1496) -- and RDC's
		// grant binds the SHARED, long-lived service account, so the genuinely dangerous one was already
		// instrumented correctly while the latent one was not.
		//
		// Deliberately no event: it would be attached to an object that is about to disappear. Warn is the
		// honest ceiling for a failure whose subject is leaving.
		//
		// The residue is a Role and RoleBinding left in a namespace, and NOTHING watches them --
		// status.AuthSecretRBACNamespaces, the only record of what needed cleaning, vanishes with the
		// object, so the list cannot be reconstructed afterwards. Impact is LATENT rather than live:
		// Undeploy deletes the bound ServiceAccount first (deploy.go:557), so the leftover binding has no
		// subject until a ServiceAccount of the same deterministic name reappears in that namespace.
		e.log.Warn("Tearing down auth secret RBAC failed; a Role and RoleBinding may be left behind, and "+
			"nothing records which namespaces needed cleaning once this object is gone",
			"kind", cr.Spec.Resource.Kind, "group", cr.Spec.ResourceGroup, "error", terr)
	}

	// #510. Separate record, separate Role name, so this removes only what syncSecretRefRBAC provisioned.
	// Delete is the last pass over this resource: a Role left here is permanent, and it grants read access
	// on real Secrets to a ServiceAccount that is about to stop existing.
	if terr := e.teardownSecretRefRBAC(ctx, cr, gvr); terr != nil {
		e.log.Warn("Removing secretRef RBAC failed (non-fatal); an orphaned Role may still grant secret "+
			"access to a deleted ServiceAccount", "error", terr, "namespaces", cr.Status.SecretRefRBACNamespaces)
	}

	e.log.Debug("Deleting RestDefinition", "Kind:", cr.Spec.Resource.Kind, "Group:", cr.Spec.ResourceGroup)
	e.rec.Eventf(cr, corev1.EventTypeNormal, "RestDefinitionDeleting",
		"RestDefinition '%s/%s' deleting", cr.Spec.Resource.Kind, cr.Spec.ResourceGroup)
	return err
}

// observeDrift is Observe's answer for "the cluster does not match this RestDefinition yet, and Update is
// what fixes it". Every drift branch goes through here so the condition cannot be forgotten at one of them.
//
// THE CONDITION IS THE POINT. Reporting drift without touching Ready left the previous Ready in place,
// and on a healthy resource that is Available -- so a RestDefinition went on reporting Ready=True for the
// whole window in which its CRD was being regenerated after an OAS edit. That is #314 in a different coat:
// the documented "kubectl wait --for condition=Ready=True, then apply a CR" returns immediately, the apply
// meets the OLD schema, the new field is pruned, and nothing says why. Ready has to mean "the kind this
// RestDefinition describes is the kind being served", not merely "some CRD is served".
//
// It also left the six branches depending on Update to set Creating. That does not happen when Update is
// blocked by policy or keeps failing, so under v1.8.0 a NotObserved left by an earlier outage would stick
// here -- the contract is what found this, not a symptom.
//
// Creating rather than Unavailable: Unavailable is documented as "expected to be available and known not
// to be", and this resource works, it just does not yet match its spec.
//
// Checked with the three consumers it reaches, before shipping rather than after:
//   - core-provider's cdc rollup maps reason Creating to childConverging, putting the owning composition at
//     Ready=False/Creating for the window. That verdict feeds the condition write and nothing else: no
//     rollback, no failure verdict, no timeout-then-abort. It is waited on, not acted on.
//   - the installer gates dependents on the PARENT's Ready, so dependents pause for the regeneration
//     window. A visible depsReady=false, not a failure -- and new pausing where a stale Available used to
//     sail through, which is the behaviour change to expect on upgrade.
//   - the one documented consumer check that reads a RestDefinition's Ready before applying CRs of its
//     kind becomes correct rather than racy.
func (e *external) observeDrift(cr *definitionv1alpha1.RestDefinition, what string) (reconciler.ExternalObservation, error) {
	cr.SetConditions(rtv1.Creating().WithMessage(what))
	return reconciler.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: false,
	}, nil
}

// observePermanentFailure is Observe's answer for a failure that will never clear on its own: a manifest
// that cannot be rendered. It reports the resource as absent-but-settled and records WHY on the object.
//
// Returning an answer rather than an error is the whole point. provider-runtime resolves an unresolved
// create handshake only AFTER Observe answers:
//
//	observation, err := external.Observe(externalCtx, managed)
//	if err != nil { return reconcile.Result{}, werr }   // returns HERE
//	if unresolvedCreate { ... }                         // never reached
//
// So an error return on a permanent failure strands a half-created RestDefinition on every runtime
// version, and no bump can fix it -- the guard is simply never given the chance to run (#266).
//
// ResourceExists:false with ResourceUpToDate:true matches the branch this file already takes when the
// CRD is absent. It is safe against duplication here because oasgen's Create APPLIES manifests rather
// than allocating anything: a repeated Create on an existing CRD is an apply, not a second resource.
// That is the test provider-runtime#17 sets -- "if I answer false and it did exist, would Create
// duplicate it?" -- and for this provider the answer is no.
//
// The condition is the deliverable as much as the answer. Without it the object shows only that it is
// not ready, which is indistinguishable from the expected two-phase intermediate state; with it, the
// RestDefinition names the template that cannot render.
func (e *external) observePermanentFailure(cr *definitionv1alpha1.RestDefinition, what string, cause error) (reconciler.ExternalObservation, error) {
	msg := fmt.Sprintf("cannot render %s and retrying will not help: %v", what, cause)
	e.log.Info("Permanent render failure; reporting it as an answer so the create handshake can resolve",
		"what", what, "error", cause)
	cr.SetConditions(rtv1.Unavailable().WithMessage(msg))
	e.rec.Eventf(cr, corev1.EventTypeWarning, "PermanentRenderFailure", "%s", msg)
	return reconciler.ExternalObservation{ResourceExists: false, ResourceUpToDate: true}, nil
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

	// A FAILED LIST IS NOT A COUNT OF ZERO (#576).
	//
	// This count's only consumer removes the `restresources-still-exist` finalizer when it reads 0, so a
	// list we could not perform must never reach it as 0 -- that deletes a RestDefinition while instances
	// of its generated Kind are still live, which is the one thing the finalizer exists to prevent.
	//
	// The previous version classified by error TEXT and produced two silent zeros. One text nilled the
	// items and cleared the error; the other skipped the whole block, so the error stayed non-nil, was
	// never returned or logged, and was then OVERWRITTEN by the Update calls below, which reused `err`.
	// Hence the distinct names from here on: a dropped error is now a compile error, not a silent one.
	if lerr := kubecli.List(ctx, &uli); lerr != nil {
		// A NO-MATCH IS NOT EVIDENCE THAT NO INSTANCES EXIST, but not for the reason it first appears.
		//
		// MEASURED AT THIS MODULE'S OWN PINS -- apimachinery v0.35.3, controller-runtime v0.23.3 -- by
		// openstack-nova-operator-kog, driving a discovery server whose knowledge of the kind is flipped
		// mid-test, offline, with the versions read back out of the built binary via `go version -m`
		// rather than from go.mod (the two can disagree). Identical arms at v0.20.0 and at v0.22.3, the
		// latter being what rest-dynamic-controller pins in this same repo.
		//
		// Corroborated independently: apiutil's `(*mapper).RESTMapping` is BYTE-IDENTICAL across
		// v0.20.0..v0.23.3, so the result is insensitive to the version over that range. That function is
		// the whole reload mechanism, quoted here so a reader can check it against their own vendored
		// copy rather than trust the attribution:
		//
		//	kind genuinely absent from discovery   *meta.NoKindMatchError, IsNoMatchError true
		//	CRD appears, mapper already stale      NO ERROR -- List SUCCEEDS after one discovery re-read
		//	discovery unreachable                  *fmt.wrapError, IsNoMatchError FALSE
		//
		//	func (m *mapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
		//		res, err := m.getMapper().RESTMapping(gk, versions...)
		//		if meta.IsNoMatchError(err) {
		//			if err := m.addKnownGroupAndReload(gk.Group, versions...); err != nil { return nil, err }
		//			res, err = m.getMapper().RESTMapping(gk, versions...)
		//		}
		//		return res, err
		//	}
		//
		// So a stale mapper never reaches this code: it tests its inner error with IsNoMatchError and, on
		// a miss, reloads discovery and retries. main.go passes no MapperProvider, so that is the mapper
		// this controller gets. A NoKindMatchError here has ALREADY survived a discovery reload.
		//
		// If that function changes, this reasoning needs re-checking -- the conclusion rests on it being
		// unchanged, not on the version the measurement was taken at.
		//
		// I HAVE PROPOSED TWO REACHABLE TRIGGERS FOR THIS AND BOTH WERE WRONG. Recording that, because a
		// third guess would be worse than an admission.
		//
		//	(1) the #314 RESTMapper-lag race    -- refuted: the dynamic mapper reloads and retries, so a
		//	                                       stale mapper never surfaces here (see above)
		//	(2) a PRUNED served version         -- refuted: prunableServedVersions skips `gvk.Version`,
		//	                                       which at its Observe call site IS observedVersion
		//	                                       (restdefinition.go:421), AND skips any version with
		//	                                       n > 0 instances. So neither the version we list at nor
		//	                                       a version holding instances can be pruned. The
		//	                                       invariant at the listGVK comment below -- "the observed
		//	                                       one is guaranteed to exist on the CRD" -- holds.
		//
		// WHAT IS ESTABLISHED, and it is enough to justify the shape:
		//
		//	discovery unreachable  -> *fmt.wrapError, IsNoMatchError FALSE -> the generic branch below
		//	                          returns it and the finalizer is HELD. Measured.
		//	the CRD is absent      -> no instances CAN exist, because deleting a CRD deletes its custom
		//	                          resources. Removal is correct and is the only path that permits it.
		//
		// So this guard is DEFENSIVE: no reachable path is known that delivers a no-match while the CRD
		// is present. That is not a reason to decide from the error alone -- the cost of asking the CRD is
		// one Get on a path that already failed, and the cost of being wrong is deleting a
		// RestDefinition whose instances are live. Asking is right even when the question is academic.
		//
		// If anyone finds a trigger, put it here and delete this paragraph.
		//
		// Note that generatedKindsServed treats an unknown kind as "definitely not served yet -- the one
		// case that is an answer rather than a failure", and that is correct FOR ITS QUESTION. For this
		// one it is not an answer at all: "not served" says nothing about whether instances exist. The
		// same error means different things to the two callers, which is why there is no shared
		// classifier here.
		//
		// And the generic branch below is where an UNREACHABLE DISCOVERY lands -- IsNoMatchError is false
		// on it -- which is the error most worth wording carefully, and is not a race at all.
		if !apimeta.IsNoMatchError(lerr) && !apierrors.IsNotFound(lerr) {
			return fmt.Errorf("listing RestResources: %w", lerr)
		}

		// The kind is unknown to the mapper or the apiserver. Ask the authority -- the CRD itself.
		crdName := plurals.ToGroupVersionResource(gvk).Resource + "." + gvk.Group
		crdPresent, cerr := crdExists(ctx, kubecli, crdName)
		if cerr != nil {
			// Could not determine. Refusing is the safe half: it cannot delete anything, and the next
			// pass re-decides with a working read.
			return fmt.Errorf("the generated kind %s could not be listed (%v), and checking whether its CRD %q exists also failed: %w",
				gvk.String(), lerr, crdName, cerr)
		}
		if crdPresent {
			return fmt.Errorf("the generated kind %s is not yet discoverable (%v) although its CRD %q exists, so its instances cannot be counted; not deciding the finalizer from an unknown count",
				gvk.String(), lerr, crdName)
		}

		// The CRD is genuinely absent, so no instances CAN exist -- deleting a CRD deletes its custom
		// resources. This is the only path on which an unlistable kind legitimately means zero.
		log("The generated Kind's CRD is absent, so no instances can exist",
			"Group", cr.Spec.ResourceGroup,
			"Kind", cr.Spec.Resource.Kind,
			"Version", observedVersion(cr),
			"crd", crdName,
			"listError", lerr.Error())
		uli.Items = nil
	}

	restResourceCount := len(uli.Items)

	// Manage restresources-still-exist finalizer
	if restResourceCount > 0 {
		switch {
		case meta.FinalizerExists(cr, restresourcesStillExistFinalizer):
			// Already held. Nothing to do, and in particular no Update: the common case must not
			// write on every reconcile.

		case cr.GetDeletionTimestamp() != nil:
			// AN OBJECT ALREADY BEING DELETED CANNOT GAIN A FINALIZER, and trying is not a harmless
			// no-op -- the apiserver rejects it with
			//
			//	metadata.finalizers: Forbidden: no new finalizers can be added if the object is
			//	being deleted, found new finalizers [composition.krateo.io/restresources-still-exist-finalizer]
			//
			// which this function used to return as an error. `observe` then failed on every pass, so
			// nothing downstream ever ran: not the delete path, and therefore not the live-instance
			// guard that exists to refuse the uninstall. The RestDefinition sat in Terminating forever
			// while the operator was shown an apiserver message about finalizers, which says nothing
			// about the instances that are the actual reason it cannot go away.
			//
			// The window is ordinary: a CR created after this function's previous pass and before the
			// RestDefinition was deleted. Measured by github-provider-kog on 0.60.0 (#612).
			//
			// Not adding it loses nothing. The finalizer's whole job is to stop the delete path from
			// proceeding, and the delete path has its own check against the live cluster at the moment
			// of the destructive act -- `liveInstanceCount` in deploy.Undeploy, which refuses with
			// ErrLiveInstances and names the count. Letting this pass succeed is what allows that
			// refusal to be reached and reported.
			log("Cannot add the restresources finalizer: the RestDefinition is already being deleted. "+
				"The uninstall will be refused by the live-instance check instead",
				"name", cr.Name,
				"Group", cr.Spec.ResourceGroup,
				"Kind", cr.Spec.Resource.Kind,
				"Count", restResourceCount,
				"deletionTimestamp", cr.GetDeletionTimestamp().String())

		default:
			log("Existing RestResources found", "Group", cr.Spec.ResourceGroup, "Kind", cr.Spec.Resource.Kind, "Count", restResourceCount)
			log("Adding finalizer to RestDefinition", "name", cr.Name, "finalizer", restresourcesStillExistFinalizer)
			meta.AddFinalizer(cr, restresourcesStillExistFinalizer)
			if uerr := kubecli.Update(ctx, cr); uerr != nil {
				return fmt.Errorf("adding restresources finalizer: %w", uerr)
			}
		}
	} else {
		if meta.FinalizerExists(cr, restresourcesStillExistFinalizer) {
			log("No RestResources found, removing finalizer", "name", cr.Name, "finalizer", restresourcesStillExistFinalizer)
			meta.RemoveFinalizer(cr, restresourcesStillExistFinalizer)
			if uerr := kubecli.Update(ctx, cr); uerr != nil {
				return fmt.Errorf("removing restresources finalizer: %w", uerr)
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
	return e.fetchOASBytesAt(ctx, cr, cr.Spec.OASPath)
}

// fetchOASBytesAt downloads one specific document. Split out from fetchOASBytes so a RestDefinition
// whose verbs span several documents can fetch each of them (#108); the single-document form above is
// the same call with spec.oasPath.
func (e *external) fetchOASBytesAt(ctx context.Context, cr *definitionv1alpha1.RestDefinition, oasPath string) (contents []byte, err error) {
	ctx, span := oteltelemetry.Tracer().Start(ctx, "restdefinition.fetch_oas")
	defer span.End()
	defer func() { oteltelemetry.RecordError(span, err) }()
	OASPath := oasPath
	span.SetAttributes(
		attribute.String("k8s.object.name", cr.Name),
		attribute.String("k8s.object.namespace", cr.Namespace),
		attribute.String("oas.source", OASPath),
	)
	basePath, err := os.MkdirTemp("", "ogen-oas-")
	if err != nil {
		// Name the SUBSYSTEM, because the frames wrapping this one name the ConfigMap. The outer frames
		// answer "what were we doing" -- fetching an OAS document from configmap://ns/name/key -- which is
		// the right thing for a wrapped error until the inner failure is in an unrelated subsystem. A node
		// with a full disk then produced a message whose only identifier was a ConfigMap path, and the
		// reader checked all twelve ConfigMaps before reading to the end of the line (#277).
		//
		// "node-local scratch space" says where to look. The chart mounts /tmp as an emptyDir backed by the
		// node filesystem, so this failing means the NODE is out of space (or /tmp is unwritable) -- nothing
		// about the ConfigMap, its contents or its availability is implicated.
		return nil, fmt.Errorf("creating a temporary directory for OAS parsing in node-local scratch space (%s): %w", os.TempDir(), err)
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

// getDocumentModelFromCR fetches and parses the CR's documents, returning the DEFAULT one and the
// composite hash across all of them.
//
// Implemented on top of getDocumentSetFromCR rather than beside it, deliberately. The hash returned here
// is stored in status.OASHash, and Observe compares that against compositeOASDigest over every document.
// If this computed a single-document digest while Observe compared a composite, the two could never
// agree: Observe would report drift on every pass, Update would store a value Observe never computes,
// and the resource would regenerate forever while reporting Creating. That is precisely the 0.28.0
// readiness loop, which came from two code paths computing the same thing differently. One function
// produces the hash; everything else asks it.
func (e *external) getDocumentModelFromCR(ctx context.Context, cr *definitionv1alpha1.RestDefinition) (doc oas2jsonschema.OASDocument, oasHash string, err error) {
	ctx, span := oteltelemetry.Tracer().Start(ctx, "restdefinition.parse_oas")
	defer span.End()
	defer func() { oteltelemetry.RecordError(span, err) }()

	docs, hash, err := e.getDocumentSetFromCR(ctx, cr)
	if err != nil {
		return nil, "", err
	}
	return docs.Default(), hash, nil
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

// guardAgainstBuiltInCollision refuses to mint a CRD whose plural or singular a built-in resource
// already serves (#200).
//
// Two asymmetries, both deliberate.
//
// FAIL OPEN when the built-in set cannot be read. Discovery being unreachable means the guard is
// absent, not satisfied. Refusing to generate on that basis would convert an apiserver hiccup into an
// outage for every RestDefinition on the cluster, to prevent a collision that is a tooling-ambiguity
// bug rather than data loss. The check is a safety net, so an unreachable safety net is reported and
// stepped over. Note a PARTIAL discovery result is different and is used: an incomplete set can only
// miss a collision, never invent one, so the refusals it does produce are still sound.
//
// REFUSE only while the CRD does not exist yet; WARN once it does. Refusing is what keeps the
// collision from being created, and before the apply that is exactly achievable. Afterwards it is not:
// the colliding CRD is already served, so refusing would not unbreak kubectl -- it would only stop the
// resource from ever being updated again, and the sole remedy, renaming the kind, destroys the CRs of
// the old one. That trades a usability bug for data loss. So an existing collision is reported loudly
// and repeatedly (it runs on every generation) and left for a human to decide about.
func (e *external) guardAgainstBuiltInCollision(ctx context.Context, cr *definitionv1alpha1.RestDefinition, rendered *render.Result) error {
	builtins, err := collision.Discover(ctx, e.disc, e.kube)
	if err != nil {
		e.log.Warn("Could not enumerate built-in resource names; the generated CRD was NOT checked for a collision with a built-in resource",
			"name", cr.Name, "namespace", cr.Namespace, "error", err)
		e.rec.Eventf(cr, corev1.EventTypeWarning, "BuiltInCollisionCheckSkipped",
			"could not enumerate the cluster's built-in resource names, so the generated CRD was not checked for a name collision: %v", err)
		return nil
	}
	if builtins.Partial {
		e.log.Warn("Built-in resource discovery was incomplete, usually an unhealthy aggregated APIService; the collision check is best-effort this pass",
			"name", cr.Name, "namespace", cr.Namespace)
	}

	for _, generated := range []*apiextensionsv1.CustomResourceDefinition{rendered.CRD, rendered.ConfigurationCRD} {
		if generated == nil {
			continue
		}
		cerr := builtins.Check(generated)
		if cerr == nil {
			continue
		}

		exists, lerr := crdExists(ctx, e.kube, generated.Name)
		if lerr != nil {
			// Unknown whether this is a new mint or an existing one. Refusing is the safe half: it
			// cannot destroy anything, and the next pass re-decides with a working read.
			return fmt.Errorf("checking whether CRD %q already exists, after detecting a built-in name collision (%w): %w", generated.Name, cerr, lerr)
		}

		if !exists {
			e.rec.Eventf(cr, corev1.EventTypeWarning, "BuiltInNameCollision", "%s", cerr.Error())
			return cerr
		}

		e.log.Warn("This RestDefinition's CRD shadows a built-in resource name; it is already served, so generation continues rather than blocking updates to it",
			"crd", generated.Name, "error", cerr)
		e.rec.Eventf(cr, corev1.EventTypeWarning, "BuiltInNameCollisionExisting",
			"%s. This CRD is ALREADY served, so generation continues: refusing now would block every future update without removing the collision, and renaming the kind destroys this CRD's existing resources. Resolve it deliberately.", cerr.Error())
	}
	return nil
}

// crdExists reports whether a CustomResourceDefinition of this name is already on the cluster.
func crdExists(ctx context.Context, kube client.Client, name string) (bool, error) {
	obj := &apiextensionsv1.CustomResourceDefinition{}
	if err := kube.Get(ctx, client.ObjectKey{Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// persistCreatePhaseStatus records the outcome of Create's FIRST phase -- the CRDs have been applied, the
// controller Deployment has not been created yet -- retrying on conflict.
//
// Create is deliberately two-phase: this phase applies the CRDs and returns, and the next reconcile, now
// seeing the CRD, creates the Deployment. That makes "CRDs present, no Deployment" an expected
// intermediate state rather than damage. What was not safe was how the phase ended (#252).
//
// The write it replaces had two defects, which produced two different permanent wedges from one line.
//
// It was UNRELIABLE. A plain Status().Update returns a resource-version conflict as an error, and Create
// returned it -- so provider-runtime, which cannot know how much of an errored Create took effect, left
// krateo.io/external-create-pending set and every later reconcile answered "cannot determine creation
// result". A conflict here is ordinary, not a failure: many RestDefinitions reconcile at once on a fresh
// install, which is exactly when it happens and exactly when it matters. Measured on a fresh GKE cluster:
// the CRDs applied at 14:41:37.25 and the status update conflicted 70ms later, stranding the resource
// permanently with no Deployment and no succeeded stamp.
//
// It was also INCOMPLETE. It recorded HasSecuritySchemes but not the version the CRD had just been applied
// at -- Status.Resource is assigned only in the second phase. So observedVersion fell back to
// resourceVersion, the next Observe looked the CRD up at the WRONG version, reported ResourceExists:
// false for a CRD that plainly existed, and the runtime re-entered the create handshake on an
// already-created resource. The re-check meant to absorb that is gated on `doc != nil`, and the
// saved-status branch leaves doc nil. Measured on a second cluster: external-create-pending re-set 97
// seconds AFTER external-create-succeeded.
//
// Recording the version here is what makes that re-check a safety net rather than the only guard. The
// version is not a guess: it is the gvk whose CRD was applied immediately above.
//
// Re-reads on every attempt rather than retrying a stale object, matching applyMergedWithRetry in
// internal/tools/crd: a concurrent change to the status is merged on top instead of clobbered by a stale
// write, which matters because the runtime writes annotations to this same object.
// It USED TO HAND-LIST THE FIELDS IT CARRIED, and that is the third field dropped this way (#492).
//
// The closure set exactly Creating, HasSecuritySchemes and Resource. generateAndApplyCRDs, called a few
// lines above the call site, sets cr.Status.SchemaWarnings IN PLACE -- and because this write targets a
// FRESHLY READ object, a field set on cr and absent from the closure is simply never persisted. Create's
// second phase does not render, so the snapshot it takes never holds the warnings either: on a FRESH
// INSTALL status.schemaWarnings was absent on every RestDefinition, including the ones with warnings.
// Measured by github-provider-kog on a kind cluster, 32 definitions, 5 warnings in the log and 0 on
// status; their control was a comment-only ConfigMap edit, which took the Update path and persisted the
// warning 64s later. So the feature worked everywhere except where a user first meets it.
//
// The comment on statusSnapshot below already said why a hand-listed set is unsafe here, and said it had
// already happened once (syncAuthSecretRBAC's two fields, #263). THREE TESTS COVERED THIS FEATURE --
// renderWarnings' own arms, the snapshot arm, and a wiring test written specifically to fail if the
// assignment at the render site were deleted -- and none of them exercised THIS function.
//
// So the fix is not to add SchemaWarnings to the list. It is to delete the list: the fields are set on cr
// and the whole status is snapshotted, exactly as Create's second phase and Update do. A snapshot has no
// collection site, which is the property that makes the class gone rather than patched, and it brings this
// path under TestCreatedStatusSnapshotCarriesEveryField for free.
func (e *external) persistCreatePhaseStatus(ctx context.Context, cr *definitionv1alpha1.RestDefinition, gvk schema.GroupVersionKind, hasSecuritySchemes bool) error {
	cr.SetConditions(rtv1.Creating())
	cr.Status.HasSecuritySchemes = &hasSecuritySchemes
	cr.Status.Resource = definitionv1alpha1.KindApiVersion{
		Kind:       gvk.Kind,
		APIVersion: gvk.GroupVersion().String(),
	}

	snap := snapshotStatus(cr)
	return e.persistStatusWithRetry(ctx, cr, snap.applyTo)
}

// statusSnapshot is the COMPLETE status a write persists, captured in one go. Used by Create's second
// phase AND by Update, both of which set the Creating condition and then write the whole status.
// value so it can be captured before the retry and applied to a re-read object.
//
// Why a named type rather than a closure over locals: this write used to be `Status().Update(ctx, cr)`,
// which persisted cr's WHOLE status -- so any earlier step that set a field on cr.Status in place was
// carried along for free. Writing a re-read object instead, which is what makes the retry safe, ends that
// silently: a field an upstream step sets and this type does not carry is never persisted, and nothing
// fails at the point of the mistake.
//
// That is not hypothetical -- it happened in the first version of this refactor. syncAuthSecretRBAC, a few
// lines above the write, sets AuthSecretDigest and AuthSecretRBACNamespaces on cr.Status in place, and
// both were dropped. An empty AuthSecretDigest makes Observe's auth-secret drift check fire on every
// reconcile, redeploying forever; an empty AuthSecretRBACNamespaces makes teardownAuthSecretRBAC return
// early on Delete, leaking RBAC in every namespace it had provisioned. Every test in the package passed,
// because none of them exercise Create's second phase.
//
// So the type exists to make the set enumerable and therefore checkable -- see
// TestCreatedStatusCoversEveryStatusField, which fails if RestDefinitionStatus grows a field applyTo does
// not set.
type statusSnapshot struct {
	status definitionv1alpha1.RestDefinitionStatus
}

// snapshotStatus copies cr's status as it stands.
//
// MUST be called after every step that sets a status field in place, and immediately before the write.
// A field set after the snapshot is taken is not in it and is therefore never persisted.
//
// A snapshot rather than an assembled value, because assembling one by hand is a list that has to be
// kept in step with both RestDefinitionStatus and every upstream step that writes to it -- and the
// previous shape proved the point by dropping two fields syncAuthSecretRBAC sets in place
// (AuthSecretDigest, AuthSecretRBACNamespaces). The symptoms were a redeploy on every reconcile, from
// Observe comparing an empty auth digest against a real one, and an RBAC leak on Delete, from
// teardownAuthSecretRBAC returning early on an empty namespace list. Every test in the package passed,
// because none exercise Create's second phase (#259, #263).
//
// A hand-written list is also only half-checkable: a guard can verify that the applier applies what it
// is given, but not that the collection site collected everything -- 7 of 7 field drops at the
// collection site passed the suite. A snapshot has no collection site to drop from.
func snapshotStatus(cr *definitionv1alpha1.RestDefinition) statusSnapshot {
	return statusSnapshot{status: *cr.Status.DeepCopy()}
}

// applyTo writes the snapshot's data fields onto target and merges a Creating condition.
//
// Idempotent and derives nothing from target except its conditions, because persistStatusWithRetry runs
// this once per write attempt and once more to mirror onto the caller's copy.
func (cs statusSnapshot) applyTo(target *definitionv1alpha1.RestDefinition) {
	next := *cs.status.DeepCopy()
	// Conditions come from the TARGET, not the snapshot. target is a freshly read object, so replacing
	// its condition list wholesale would discard one another writer set between our read and this write.
	// The data fields are this phase's to own; the condition list is shared, so it is merged into rather
	// than overwritten.
	next.ConditionedStatus = target.Status.ConditionedStatus
	target.Status = next
	target.SetConditions(rtv1.Creating())
}

// persistStatusWithRetry applies mutate to a FRESHLY READ copy of cr and writes the status subresource,
// retrying on conflict, then applies mutate to the caller's copy so the rest of the reconcile sees what
// was persisted rather than the pre-write state.
//
// Both of Create's phases end in a status write, and a conflict escaping either one is what wedges the
// resource (#252, #259). Sharing one mechanism rather than hand-rolling a second: phase 2 writes seven
// fields where phase 1 writes three, so a duplicated retry-and-mirror is exactly the construct that
// drifts, and a mirror that forgets a field is invisible until something downstream reads the stale value.
//
// Re-reads on every attempt rather than retrying a stale object, matching applyMergedWithRetry in
// internal/tools/crd: a concurrent change is merged on top instead of clobbered. That matters because
// provider-runtime writes its own annotations to this same object -- though not to its status, which has a
// subresource of its own, so these writes and the runtime's cannot collide on content.
//
// mutate must be idempotent and must not derive values from the object it is given: it runs twice, on two
// different copies. Anything computed from the spec is computed once by the caller and captured.
func (e *external) persistStatusWithRetry(ctx context.Context, cr *definitionv1alpha1.RestDefinition, mutate func(*definitionv1alpha1.RestDefinition)) error {
	key := client.ObjectKeyFromObject(cr)

	// written holds the resourceVersion the SUCCESSFUL write produced, so it can be mirrored onto the
	// caller's copy below. Assigned only after Status().Update returns nil: an attempt that read the
	// object and then failed to write it must not stamp the caller with a version that was never
	// persisted. A stale resourceVersion conflicts loudly; a wrong one succeeds against the wrong
	// generation, which is worse.
	var written string

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur := &definitionv1alpha1.RestDefinition{}
		if gerr := e.kube.Get(ctx, key, cur); gerr != nil {
			return gerr
		}
		mutate(cur)
		if uerr := e.kube.Status().Update(ctx, cur); uerr != nil {
			return uerr
		}
		written = cur.GetResourceVersion()
		return nil
	}); err != nil {
		return err
	}

	mutate(cr)

	// THE CALLER IS NOT THE ONLY WRITER, and the other one does not retry.
	//
	// provider-runtime reads the managed object once at the top of a reconcile, hands this same pointer to
	// Update as `mg`, and after we return writes its OWN status onto it (reconciler.go:745 calls its status
	// function once, with no RetryOnConflict). Because the write above went through a freshly read object,
	// the server moved on and the runtime was left holding the version it read -- so its write failed with
	// "cannot update managed resource status: the object has been modified", the reconcile returned an
	// error, and its condition write for that pass was lost. On a pass that had succeeded.
	//
	// Deterministic rather than racy, which is how it was found: one conflict per definition, every time.
	// Measured on the 0.57.0 upgrade as 25 of 25 kinds by one consumer and 4 of 4 by another (#579).
	//
	// Mirroring the status fields alone was not enough, and the existing mirror test still passed --
	// the fields were there. The metadata the next writer depends on was not.
	cr.SetResourceVersion(written)
	return nil
}

// generatedKindsServed reports whether the apiserver is actually serving the kinds this RestDefinition
// generated. It returns the first kind that is not yet served, for the condition message.
//
// It asks the RESTMapper, not the CRD object, deliberately. A CRD can exist and report Established while the
// specific VERSION a CR will be written through is not served -- the two generated halves are served at
// DIFFERENT versions (the resource half derives from the OAS info.version, the Configuration half is always
// DefaultVersion), so "the CRD is Established" is not the question. `no matches for kind X in version V`
// names a version, and the RESTMapper is what produces it; asking the same component that rejects the apply
// is the only check that cannot disagree with it.
//
// The mapper is controller-runtime's default dynamic mapper, which reloads on a miss rather than caching a
// negative forever, so a kind that becomes served is picked up on a subsequent pass.
//
// Both GVKs come from cr.Status, which Create/Update already populate from the same TargetGVK and
// getConfigurationGVK used to render the CRDs. Re-deriving them here would be a second source of truth for a
// value the object already carries -- and an empty Status.Configuration.APIVersion is MEANINGFUL: it means no
// Configuration CRD was generated (only configurationFields or security schemes produce one), so it must be
// skipped rather than treated as unknown.
func (e *external) generatedKindsServed(ctx context.Context, cr *definitionv1alpha1.RestDefinition) (served bool, pending string, err error) {
	type half struct {
		label string
		kav   definitionv1alpha1.KindApiVersion
	}
	halves := []half{
		{label: "resource", kav: cr.Status.Resource},
		{label: "configuration", kav: cr.Status.Configuration},
	}

	mapper := e.kube.RESTMapper()
	for _, h := range halves {
		// Not yet recorded (first Create has not stored it) or not generated at all. Either way there is
		// nothing to check for this half.
		if h.kav.APIVersion == "" || h.kav.Kind == "" {
			continue
		}
		gv, perr := schema.ParseGroupVersion(h.kav.APIVersion)
		if perr != nil {
			// A status we wrote ourselves should always parse. If it does not, say so rather than treat the
			// half as served.
			return false, "", fmt.Errorf("parsing status.%s.apiVersion %q: %w", h.label, h.kav.APIVersion, perr)
		}
		gvk := gv.WithKind(h.kav.Kind)

		if _, merr := mapper.RESTMapping(gvk.GroupKind(), gvk.Version); merr != nil {
			if apimeta.IsNoMatchError(merr) {
				// Definitely not served yet. This is the one case that is an answer rather than a failure.
				return false, gvk.String(), nil
			}
			// Anything else -- discovery unreachable, an unhealthy aggregated APIService, a timeout -- is
			// "could not look", not "not served". Returning false here would report Creating on a healthy
			// install every time discovery hiccuped.
			return false, "", fmt.Errorf("resolving %s: %w", gvk.String(), merr)
		}
	}
	return true, "", nil
}

// renderWarnings turns the render's warnings into the strings status carries, returning nil for none so
// the field is omitted entirely rather than serialised as an empty array (#462).
//
// nil rather than []string{} is deliberate: `schemaWarnings: []` on every healthy RestDefinition is noise
// in every `kubectl get -o yaml`, and omitempty only drops a nil slice.
func renderWarnings(warnings []error) []string {
	if len(warnings) == 0 {
		return nil
	}
	out := make([]string, 0, len(warnings))
	for _, w := range warnings {
		if w == nil {
			continue
		}
		out = append(out, w.Error())
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
