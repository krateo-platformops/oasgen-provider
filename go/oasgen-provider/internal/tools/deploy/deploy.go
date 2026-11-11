package deploy

import (
	"context"
	"fmt"
	"maps"
	"path"
	"strings"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/deployment"
	hasher "github.com/krateo-platformops/oasgen-provider/internal/tools/hash"
	kubecli "github.com/krateo-platformops/oasgen-provider/internal/tools/kube"
	templates "github.com/krateo-platformops/oasgen-provider/internal/tools/objects"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	crd "github.com/krateo-platformops/oasgen-provider/internal/tools/crd"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ControllerResourceSuffix = "-controller"
	// rbacVersion is the fixed version used to NAME the RDC RBAC objects, decoupling them from the target CRD
	// version so a version bump neither orphans nor duplicates the RBAC set. Rules are version-independent.
	rbacVersion             = "v1alpha1"
	ConfigmapResourceSuffix = "-configmap"
)

type UndeployOptions struct {
	ConfigurationGVR schema.GroupVersionResource
	KubeClient       client.Client
	NamespacedName   types.NamespacedName
	GVR              schema.GroupVersionResource
	// GVK is the managed resource's kind, needed to LIST its instances before uninstalling the CRD.
	// A GVR alone cannot be listed without a RESTMapper, and guessing the Kind from the plural would
	// silently under-count -- which, for this particular check, means deleting production resources.
	// Callers already hold it: they derive GVR from it.
	GVK                    schema.GroupVersionKind
	RBACFolderPath         string
	Log                    func(msg string, keysAndValues ...any)
	SkipCRD                bool
	SkipDeploy             bool
	DeploymentTemplatePath string
	ConfigmapTemplatePath  string
}

type DeployOptions struct {
	GVR                    schema.GroupVersionResource
	ConfigurationGVR       schema.GroupVersionResource
	KubeClient             client.Client
	NamespacedName         types.NamespacedName
	RBACFolderPath         string
	DeploymentTemplatePath string
	ConfigmapTemplatePath  string
	Log                    func(msg string, keysAndValues ...any)
	// LabelSelector scopes the generated controller's watch to the instances THIS version owns. Every
	// served version runs its own controller and every served endpoint returns every object, so the
	// apiVersion cannot separate them -- krateo.io/oas-version does. Empty means watch everything, which
	// is correct when only one version is served. Computed by crd/generation.VersionSelector.
	LabelSelector string
	// DryRunServer is used to determine if the deployment should be applied in dry-run mode. This is ignored in lookup mode
	DryRunServer bool
}

// absenceOrError classifies a failed Get for Lookup, which must be able to say "not deployed yet" WITHOUT
// saying "I could not tell". Returns nil when err means the object genuinely does not exist, and otherwise
// logs and returns err.
//
// THE THREE CALL SITES USED TO RETURN ("", nil) FOR EVERY FAILURE, which reported a failed lookup as a
// successful EMPTY one (#427). Observe then compared status.Digest against "" , concluded drift, and called
// Update -- forever, for as long as the underlying error persisted, re-applying identical manifests.
//
// That contradicted the rule the other three drift inputs state explicitly in their own comments: a
// transient failure is NOT drift, "to avoid flapping when the OAS source is briefly unreachable"
// (restdefinition.go:449), "for the same reason as the OAS check" (:496), and "a transient List failure
// must not flap a healthy resource into Creating" (:516). This input is the fourth and it did the
// opposite, invisibly, because its caller cannot distinguish an empty digest from a failed one.
//
// And since the drift branches began reporting Ready=False/Creating with a message naming the drift, the
// consequence stopped being a wasteful loop and became a PUBLISHED WRONG DIAGNOSIS: "the deployed
// resources drifted from the render; redeploying", propagated by core-provider's rollup to the owning
// composition, when nothing drifted and nothing needs redeploying.
//
// ONE CLASSIFIER FOR THREE SITES rather than three copies of the branch. The three were identical in
// shape and all three were wrong the same way, which is how a fix editing them in place misses one.
//
// The model is crd.Lookup (internal/tools/crd/crd.go:49-56), which has always made exactly this split for
// exactly this question.
func absenceOrError(log func(msg string, keysAndValues ...any), msg string, err error) error {
	if apierrors.IsNotFound(err) {
		// A true answer, not a dropped one: a first install has nothing deployed, and Observe relies on
		// an empty digest to mean that.
		return nil
	}
	logError(log, msg, err)
	return err
}

func logError(log func(msg string, keysAndValues ...any), msg string, err error) {
	if log != nil {
		log(msg, "error", err)
	}
}

func createRBACResources(gvr schema.GroupVersionResource, rbacNSName types.NamespacedName, ConfigurationGVR schema.GroupVersionResource, rbacFolderPath string) (corev1.ServiceAccount, rbacv1.ClusterRole, rbacv1.ClusterRoleBinding, rbacv1.Role, rbacv1.RoleBinding, error) {
	rbacNSName = types.NamespacedName{
		Namespace: rbacNSName.Namespace,
		Name:      rbacNSName.Name + ControllerResourceSuffix,
	}

	// RBAC objects are named "<resource>-<version>" by the templates, but their RULES grant on group+resource
	// (not on a specific version). Render them with a STABLE version so the names do not change when the target
	// CRD version is bumped — otherwise each bump would orphan the previous version's RBAC set (including the
	// cluster-scoped ClusterRole/ClusterRoleBinding), and a teardown with the OAS already gone (version falls
	// back) would silently fail to delete the real, derived-version-named RBAC. rbacVersion keeps names
	// backward-compatible with RBAC created before version derivation.
	rbacGVR := schema.GroupVersionResource{Group: gvr.Group, Version: rbacVersion, Resource: gvr.Resource}

	sa := corev1.ServiceAccount{}
	err := templates.CreateK8sObject(&sa, rbacGVR, rbacNSName, path.Join(rbacFolderPath, "serviceaccount.yaml"))
	if err != nil {
		return corev1.ServiceAccount{}, rbacv1.ClusterRole{}, rbacv1.ClusterRoleBinding{}, rbacv1.Role{}, rbacv1.RoleBinding{}, err
	}

	configuration := ""
	if ConfigurationGVR.Resource != "" {
		configuration = ConfigurationGVR.Resource
		//fmt.Printf("Configuration GVR found: %s\n", configuration)
	}
	clusterrole := rbacv1.ClusterRole{}
	err = templates.CreateK8sObject(&clusterrole, rbacGVR, rbacNSName, path.Join(rbacFolderPath, "clusterrole.yaml"), "configuration", configuration)
	if err != nil {
		return corev1.ServiceAccount{}, rbacv1.ClusterRole{}, rbacv1.ClusterRoleBinding{}, rbacv1.Role{}, rbacv1.RoleBinding{}, err
	}
	//fmt.Printf("ClusterRole created with name: %s, namespace: %s, rules: %+v\n", clusterrole.Name, clusterrole.Namespace, clusterrole.Rules)

	clusterrolebinding := rbacv1.ClusterRoleBinding{}
	err = templates.CreateK8sObject(&clusterrolebinding, rbacGVR, rbacNSName, path.Join(rbacFolderPath, "clusterrolebinding.yaml"), "serviceAccount", sa.Name, "saNamespace", sa.Namespace)
	if err != nil {
		return corev1.ServiceAccount{}, rbacv1.ClusterRole{}, rbacv1.ClusterRoleBinding{}, rbacv1.Role{}, rbacv1.RoleBinding{}, err
	}
	//fmt.Printf("ClusterRoleBinding created with name: %s, namespace: %s, subjects: %+v, roleRef: %+v\n", clusterrolebinding.Name, clusterrolebinding.Namespace, clusterrolebinding.Subjects, clusterrolebinding.RoleRef)

	role := rbacv1.Role{}
	err = templates.CreateK8sObject(&role, rbacGVR, rbacNSName, path.Join(rbacFolderPath, "role.yaml"))
	if err != nil {
		return corev1.ServiceAccount{}, rbacv1.ClusterRole{}, rbacv1.ClusterRoleBinding{}, rbacv1.Role{}, rbacv1.RoleBinding{}, err
	}
	//fmt.Printf("Role created with name: %s, namespace: %s, rules: %+v\n", role.Name, role.Namespace, role.Rules)

	rolebinding := rbacv1.RoleBinding{}
	err = templates.CreateK8sObject(&rolebinding, rbacGVR, rbacNSName, path.Join(rbacFolderPath, "rolebinding.yaml"), "serviceAccount", sa.Name, "saNamespace", sa.Namespace)
	if err != nil {
		return corev1.ServiceAccount{}, rbacv1.ClusterRole{}, rbacv1.ClusterRoleBinding{}, rbacv1.Role{}, rbacv1.RoleBinding{}, err
	}
	//fmt.Printf("RoleBinding created with name: %s, namespace: %s, subjects: %+v, roleRef: %+v\n", rolebinding.Name, rolebinding.Namespace, rolebinding.Subjects, rolebinding.RoleRef)

	return sa, clusterrole, clusterrolebinding, role, rolebinding, nil
}

func installRBACResources(ctx context.Context, kubeClient client.Client, clusterrole rbacv1.ClusterRole, clusterrolebinding rbacv1.ClusterRoleBinding, role rbacv1.Role, rolebinding rbacv1.RoleBinding, sa corev1.ServiceAccount, log func(msg string, keysAndValues ...any), hsh *hasher.ObjectHash, applyOpts kubecli.ApplyOptions) error {
	if hsh == nil {
		return fmt.Errorf("hasher is required")
	}
	err := kubecli.Apply(ctx, kubeClient, &clusterrole, applyOpts)
	if err != nil {
		logError(log, "Error installing clusterrole", err)
		return err
	}
	err = hsh.SumHash(clusterrole.ObjectMeta.Name, clusterrole.ObjectMeta.Namespace, clusterrole.Rules)
	if err != nil {
		return fmt.Errorf("error hashing clusterrole: %v", err)
	}
	log("ClusterRole successfully installed", "name", clusterrole.Name, "namespace", clusterrole.Namespace, "digest", hsh.GetHash())

	err = kubecli.Apply(ctx, kubeClient, &clusterrolebinding, applyOpts)
	if err != nil {
		logError(log, "Error installing clusterrolebinding", err)
		return err
	}
	err = hsh.SumHash(clusterrolebinding.ObjectMeta.Name, clusterrolebinding.ObjectMeta.Namespace, clusterrolebinding.Subjects, clusterrolebinding.RoleRef)
	if err != nil {
		return fmt.Errorf("error hashing clusterrolebinding: %v", err)
	}
	log("ClusterRoleBinding successfully installed", "name", clusterrolebinding.Name, "namespace", clusterrolebinding.Namespace, "digest", hsh.GetHash())

	err = kubecli.Apply(ctx, kubeClient, &role, applyOpts)
	if err != nil {
		logError(log, "Error installing role", err)
		return err
	}
	err = hsh.SumHash(role.ObjectMeta.Name, role.ObjectMeta.Namespace, role.Rules)
	if err != nil {
		return fmt.Errorf("error hashing role: %v", err)
	}
	log("Role successfully installed", "name", role.Name, "namespace", role.Namespace, "digest", hsh.GetHash())

	err = kubecli.Apply(ctx, kubeClient, &rolebinding, applyOpts)
	if err != nil {
		logError(log, "Error installing rolebinding", err)
		return err
	}
	err = hsh.SumHash(rolebinding.ObjectMeta.Name, rolebinding.ObjectMeta.Namespace, rolebinding.Subjects, rolebinding.RoleRef)
	if err != nil {
		return fmt.Errorf("error hashing rolebinding: %v", err)
	}
	log("RoleBinding successfully installed", "name", rolebinding.Name, "namespace", rolebinding.Namespace, "digest", hsh.GetHash())

	err = kubecli.Apply(ctx, kubeClient, &sa, applyOpts)
	if err != nil {
		logError(log, "Error installing serviceaccount", err)
		return err
	}
	err = hsh.SumHash(sa.ObjectMeta.Name, sa.ObjectMeta.Namespace)
	if err != nil {
		return fmt.Errorf("error hashing serviceaccount: %v", err)
	}
	log("ServiceAccount successfully installed", "name", sa.Name, "namespace", sa.Namespace, "digest", hsh.GetHash())

	return nil
}

func uninstallRBACResources(ctx context.Context, kubeClient client.Client, clusterrole rbacv1.ClusterRole, clusterrolebinding rbacv1.ClusterRoleBinding, role rbacv1.Role, rolebinding rbacv1.RoleBinding, sa corev1.ServiceAccount, log func(msg string, keysAndValues ...any)) error {
	err := kubecli.Uninstall(ctx, kubeClient, &clusterrole, kubecli.UninstallOptions{})
	if err != nil {
		logError(log, "Error uninstalling clusterrole", err)
		return err
	}
	log("ClusterRole successfully uninstalled", "name", clusterrole.Name, "namespace", clusterrole.Namespace)

	err = kubecli.Uninstall(ctx, kubeClient, &clusterrolebinding, kubecli.UninstallOptions{})
	if err != nil {
		logError(log, "Error uninstalling clusterrolebinding", err)
		return err
	}
	log("ClusterRoleBinding successfully uninstalled", "name", clusterrolebinding.Name, "namespace", clusterrolebinding.Namespace)

	err = kubecli.Uninstall(ctx, kubeClient, &role, kubecli.UninstallOptions{})
	if err != nil {
		logError(log, "Error uninstalling role", err)
		return err
	}
	log("Role successfully uninstalled", "name", role.Name, "namespace", role.Namespace)

	err = kubecli.Uninstall(ctx, kubeClient, &rolebinding, kubecli.UninstallOptions{})
	if err != nil {
		logError(log, "Error uninstalling rolebinding", err)
		return err
	}
	log("RoleBinding successfully uninstalled", "name", rolebinding.Name, "namespace", rolebinding.Namespace)

	err = kubecli.Uninstall(ctx, kubeClient, &sa, kubecli.UninstallOptions{})
	if err != nil {
		logError(log, "Error uninstalling serviceaccount", err)
		return err
	}
	log("ServiceAccount successfully uninstalled", "name", sa.Name, "namespace", sa.Namespace)

	return nil
}

func lookupRBACResources(ctx context.Context, kubeClient client.Client, clusterrole rbacv1.ClusterRole, clusterrolebinding rbacv1.ClusterRoleBinding, role rbacv1.Role, rolebinding rbacv1.RoleBinding, sa corev1.ServiceAccount, log func(msg string, keysAndValues ...any), hsh *hasher.ObjectHash) error {
	if hsh == nil {
		return fmt.Errorf("hasher is required")
	}
	err := kubecli.Get(ctx, kubeClient, &clusterrole)
	if err != nil {
		logError(log, "Error getting clusterrole", err)
		return err
	}
	err = hsh.SumHash(clusterrole.ObjectMeta.Name, clusterrole.ObjectMeta.Namespace, clusterrole.Rules)
	if err != nil {
		return fmt.Errorf("error hashing clusterrole: %v", err)
	}
	log("ClusterRole successfully fetched", "name", clusterrole.Name, "namespace", clusterrole.Namespace, "digest", hsh.GetHash())

	err = kubecli.Get(ctx, kubeClient, &clusterrolebinding)
	if err != nil {
		logError(log, "Error getting clusterrolebinding", err)
		return err
	}
	err = hsh.SumHash(clusterrolebinding.ObjectMeta.Name, clusterrolebinding.ObjectMeta.Namespace, clusterrolebinding.Subjects, clusterrolebinding.RoleRef)
	if err != nil {
		return fmt.Errorf("error hashing clusterrolebinding: %v", err)
	}
	log("ClusterRoleBinding successfully fetched", "name", clusterrolebinding.Name, "namespace", clusterrolebinding.Namespace, "digest", hsh.GetHash())

	err = kubecli.Get(ctx, kubeClient, &role)
	if err != nil {
		logError(log, "Error getting role", err)
		return err
	}
	err = hsh.SumHash(role.ObjectMeta.Name, role.ObjectMeta.Namespace, role.Rules)
	if err != nil {
		return fmt.Errorf("error hashing role: %v", err)
	}
	log("Role successfully fetched", "name", role.Name, "namespace", role.Namespace, "digest", hsh.GetHash())

	err = kubecli.Get(ctx, kubeClient, &rolebinding)
	if err != nil {
		logError(log, "Error getting rolebinding", err)
		return err
	}
	err = hsh.SumHash(rolebinding.ObjectMeta.Name, rolebinding.ObjectMeta.Namespace, rolebinding.Subjects, rolebinding.RoleRef)
	if err != nil {
		return fmt.Errorf("error hashing rolebinding: %v", err)
	}
	log("RoleBinding successfully fetched", "name", rolebinding.Name, "namespace", rolebinding.Namespace, "digest", hsh.GetHash())

	err = kubecli.Get(ctx, kubeClient, &sa)
	if err != nil {
		logError(log, "Error getting serviceaccount", err)
		return err
	}
	err = hsh.SumHash(sa.ObjectMeta.Name, sa.ObjectMeta.Namespace)
	if err != nil {
		return fmt.Errorf("error hashing serviceaccount: %v", err)
	}
	log("ServiceAccount successfully fetched", "name", sa.Name, "namespace", sa.Namespace, "digest", hsh.GetHash())

	return nil
}

func Deploy(ctx context.Context, kube client.Client, opts DeployOptions) (digest string, err error) {
	if opts.Log == nil {
		return "", fmt.Errorf("log function is required")
	}

	hsh := hasher.NewFNVObjectHash()

	sa, clusterrole, clusterrolebinding, role, rolebinding, err := createRBACResources(opts.GVR, opts.NamespacedName, opts.ConfigurationGVR, opts.RBACFolderPath)
	if err != nil {
		opts.Log("Error creating RBAC resources", "error", err)
		// createRBACResources is five templates.CreateK8sObject calls and nothing else -- no cluster
		// access -- so every failure it can report is permanent.
		return "", &PermanentRenderError{What: "RBAC manifests", Err: err}
	}
	applyOpts := kubecli.ApplyOptions{}

	if opts.DryRunServer {
		applyOpts.DryRun = []string{"All"}
	}
	err = installRBACResources(ctx, opts.KubeClient, clusterrole, clusterrolebinding, role, rolebinding, sa, opts.Log, &hsh, applyOpts)
	if err != nil {
		opts.Log("Error installing RBAC resources", "error", err)
		return "", err
	}

	cmNSName := types.NamespacedName{
		Namespace: opts.NamespacedName.Namespace,
		Name:      opts.NamespacedName.Name + ControllerResourceSuffix,
	}
	// RENDER FIRST, then read the live object AT THE RENDERED OBJECT'S OWN KEY.
	//
	// The order is the fix for #599 and it is not cosmetic. cmNSName.Name is "<rd>-controller", but the
	// ConfigMap asset renders its name as `{{ .name }}-configmap` while the Deployment asset renders
	// `{{ .name }}` -- so the live read used to ask for a ConfigMap at the DEPLOYMENT's name, got
	// NotFound on every reconcile of every kind, and set cmExisted=false forever. restartNeeded then
	// returned false unconditionally and the generated controller was never restarted: an rdc.env
	// change reached its ConfigMap and never reached the running process, silently, because envFrom is
	// read once at container start.
	//
	// Keying from `cm` makes the two names ONE VALUE rather than two that must agree. Re-deriving the
	// suffix in Go would restore the same defect the next time an asset's name expression changes.
	cm := corev1.ConfigMap{}
	err = templates.CreateK8sObject(&cm, opts.GVR, cmNSName, opts.ConfigmapTemplatePath,
		"composition_controller_sa_name", sa.Name,
		"composition_controller_sa_namespace", sa.Namespace)
	if err != nil {
		opts.Log("Error creating configmap object", "error", err)
		return "", &PermanentRenderError{What: "the controller ConfigMap", Err: err}
	}

	liveData, cmExisted, err := liveConfigMapData(ctx, opts.KubeClient, &cm)
	if err != nil {
		logError(opts.Log, "Error reading the existing configmap", err)
		return "", err
	}

	mustRestart := restartNeeded(liveData, cm.Data, cmExisted)

	err = kubecli.Apply(ctx, opts.KubeClient, &cm, applyOpts)
	if err != nil {
		opts.Log("Error installing configmap", "name", cm.Name, "namespace", cm.Namespace, "error", err)
		return "", fmt.Errorf("error installing configmap: %v", err)
	}
	err = hsh.SumHash(cm.ObjectMeta.Name, cm.ObjectMeta.Namespace, cm.Data)
	if err != nil {
		return "", fmt.Errorf("error hashing configmap: %v", err)
	}
	opts.Log("Configmap successfully installed", "gvr", opts.GVR.String(), "name", cm.Name, "namespace", cm.Namespace, "digest", hsh.GetHash())

	deploymentNSName := types.NamespacedName{
		Namespace: opts.NamespacedName.Namespace,
		Name:      opts.NamespacedName.Name + ControllerResourceSuffix,
	}
	dep := appsv1.Deployment{}
	err = templates.CreateK8sObject(
		&dep,
		opts.GVR,
		deploymentNSName,
		opts.DeploymentTemplatePath,
		"serviceAccountName", sa.Name,
		"labelSelector", opts.LabelSelector)
	if err != nil {
		opts.Log("Error creating deployment object", "error", err)
		return "", &PermanentRenderError{What: "the controller Deployment", Err: err}
	}

	// The Deployment, and ONLY the Deployment, carries the restart annotation over from the live object.
	// applyOpts is shared with the ClusterRole, Role, bindings, ServiceAccount and ConfigMap applies
	// above, and this hook asserts its argument is a *appsv1.Deployment -- putting it on the shared
	// options would make every one of those applies fail. So this is a copy, not a mutation of the
	// shared value.
	//
	// Issue 595: without it, Apply's full Update removes the annotation the restart below writes, which
	// costs a second, pointless rollout on the next reconcile.
	depApplyOpts := applyOpts
	depApplyOpts.FromCurrent = deployment.PreserveRestartAnnotation

	err = kubecli.Apply(ctx, opts.KubeClient, &dep, depApplyOpts)
	if err != nil {
		opts.Log("Error installing deployment", "name", dep.Name, "namespace", dep.Namespace, "error", err)
		return "", fmt.Errorf("error installing deployment: %v", err)
	}

	if !opts.DryRunServer {
		dep := appsv1.Deployment{}
		err = templates.CreateK8sObject(
			&dep,
			opts.GVR,
			deploymentNSName,
			opts.DeploymentTemplatePath,
			"serviceAccountName", sa.Name,
			"labelSelector", opts.LabelSelector)
		if err != nil {
			opts.Log("Error creating deployment object", "error", err)
			return "", err
		}
		// RESTART ONLY WHEN THE ConfigMap's DATA CHANGED, which is the only change the pod cannot pick
		// up by itself (#588).
		//
		// The ConfigMap reaches the container through `envFrom: configMapRef`, so its values are fixed at
		// container start AND the reference is by NAME -- the pod template does not change when the data
		// does. That is the one case needing an explicit roll. Everything else the pod consumes (the
		// image, env, volumes, resources) lives in the pod template, and a Deployment whose pod template
		// changes rolls its pods on its own.
		//
		// This block used to restart UNCONDITIONALLY. Its comment said "if the hash changes" and no hash
		// was ever consulted, so every reconcile that reached here rolled the controller. Once #510/#527
		// made a secretRef Secret-name change a reason to redeploy, a consumer measured 8 Deployment
		// revisions in 15 seconds where the previous release had 2 in a day -- with the ConfigMap's
		// resourceVersion and data hash provably identical across the restarts, which is what refuted the
		// comment's own justification.
		//
		// THE READINESS CHECK IS GONE DELIBERATELY. `if dep.Status.ReadyReplicas == dep.Status.Replicas`
		// was written as a guard against restarting something already broken, but with an unconditional
		// restart above it it was the only brake on a restart storm -- and it braked LESS the healthier
		// the controller was, since a mid-roll pod is not ready. Keeping it now would be worse than
		// removing it: a ConfigMap change arriving while a pod is still rolling is exactly when a restart
		// is REQUIRED, because that pod may have started before the change.
		if mustRestart {
			err = kubecli.Get(ctx, opts.KubeClient, &dep)
			if err != nil {
				logError(opts.Log, "Error getting deployment", err)
				return "", err
			}
			opts.Log("Restarting the generated controller: its ConfigMap data changed",
				"name", dep.Name, "namespace", dep.Namespace)
			err = deployment.RestartDeployment(ctx, opts.KubeClient, &dep)
			if err != nil {
				logError(opts.Log, "Error restarting deployment", err)
				return "", err
			}
		}
	}

	deployment.CleanFromRestartAnnotation(&dep)

	err = hsh.SumHash(dep.ObjectMeta.Name, dep.ObjectMeta.Namespace, dep.Spec)
	if err != nil {
		return "", fmt.Errorf("error hashing deployment spec: %v", err)
	}
	opts.Log("Deployment successfully installed", "gvr", opts.GVR.String(), "name", dep.Name, "namespace", dep.Namespace, "digest", hsh.GetHash())

	return hsh.GetHash(), nil
}

// PermanentRenderError wraps a failure to RENDER a manifest -- reading a template off disk and
// substituting values into it. It exists so Observe can tell a permanent local failure from a transient
// cluster one, which it otherwise cannot: Deploy's render steps and its dry-run Apply steps returned the
// same bare error, and Observe returned it upward either way.
//
// That conflation wedges a half-created RestDefinition (#266). provider-runtime's create-recovery guard
// runs only when Observe ANSWERS -- an error return skips it -- so a resource whose create handshake is
// unresolved and whose template will never render is stuck on every runtime version, forever, because
// the guard never gets the chance to reconcile the handshake against reality.
//
// An error is the right answer only for a TRANSIENT cannot-tell. A permanent one must be reported as an
// answer plus a condition saying why, so the object carries its own diagnosis instead of retrying a
// render that cannot succeed.
//
// Rendering touches no cluster: templates.CreateK8sObject is os.ReadFile plus substitution. So a failure
// is permanent by construction -- the same inputs will fail identically on every reconcile until a human
// changes the template or the chart.
type PermanentRenderError struct {
	// What was being rendered, for the condition message: "configmap", "deployment", "RBAC".
	What string
	Err  error
}

func (e *PermanentRenderError) Error() string {
	return fmt.Sprintf("rendering %s: %v (this will not succeed on retry)", e.What, e.Err)
}

func (e *PermanentRenderError) Unwrap() error { return e.Err }

// ErrLiveInstances is returned when a CRD cannot be uninstalled because instances of it still exist.
// Callers should treat it as "not yet", not as a failure to retry differently: the RestDefinition keeps
// its finalizer and the CRD, its CRs and their external resources all survive.
var ErrLiveInstances = fmt.Errorf("CRD still has live instances")

// liveInstanceCount reports how many instances of gvr exist cluster-wide.
//
// A missing CRD is not an error: it means the kind is already gone, so there is nothing to protect.
// Any OTHER failure is propagated, because a count we could not take must never read as zero.
func liveInstanceCount(ctx context.Context, kube client.Client, gvk schema.GroupVersionKind) (int, error) {
	if gvk.Kind == "" {
		// Without a Kind nothing can be listed, and a zero here would authorise the delete. Refuse.
		return 0, fmt.Errorf("cannot list instances: no Kind supplied")
	}
	var list unstructured.UnstructuredList
	list.SetGroupVersionKind(gvk)
	if err := kube.List(ctx, &list); err != nil {
		if apimeta.IsNoMatchError(err) || apierrors.IsNotFound(err) ||
			strings.Contains(err.Error(), "the server could not find the requested resource") ||
			strings.Contains(err.Error(), "no matches for") {
			return 0, nil
		}
		return 0, err
	}
	return len(list.Items), nil
}

func Undeploy(ctx context.Context, kube client.Client, opts UndeployOptions) error {
	if opts.Log == nil {
		return fmt.Errorf("log function is required")
	}

	if !opts.SkipCRD {
		// REFUSE TO DROP A CRD THAT STILL HAS INSTANCES.
		//
		// Uninstalling a CRD cascade-deletes every CR of that kind, and those CRs carry finalizers that
		// delete the EXTERNAL resource. So deleting a RestDefinition could destroy real infrastructure --
		// a GitHub repository, a cloud server -- with no per-CR opt-in and nothing to undo it (#125).
		//
		// A guard already existed via the restresources-still-exist finalizer, but it was consulted only
		// for SkipDeploy, never for SkipCRD, and it reflects what a PREVIOUS reconcile observed. A CR
		// created since then would not be seen. This check is taken here, at the moment of the
		// destructive act, against the live cluster.
		//
		// A LIST failure refuses too. "I could not count" must never read as "there were none" -- that is
		// the same conflation that produced the delete-path bugs in #77/#98/#101, and here the cost of
		// getting it wrong is unrecoverable rather than merely wrong.
		n, cerr := liveInstanceCount(ctx, opts.KubeClient, opts.GVK)
		if cerr != nil {
			opts.Log("Refusing to uninstall CRD: cannot determine whether instances exist",
				"name", opts.GVR.GroupResource().String(), "error", cerr.Error())
			return fmt.Errorf("verifying %s has no live instances before uninstalling its CRD: %w",
				opts.GVR.GroupResource().String(), cerr)
		}
		if n > 0 {
			opts.Log("Refusing to uninstall CRD: live instances exist",
				"name", opts.GVR.GroupResource().String(), "instances", n)
			return fmt.Errorf("%w: %s has %d live instance(s); uninstalling the CRD would cascade-delete "+
				"them and their finalizers would delete the external resources they manage. Delete the "+
				"instances first if that is intended", ErrLiveInstances, opts.GVR.GroupResource().String(), n)
		}

		err := crd.Uninstall(ctx, opts.KubeClient, opts.GVR.GroupResource())
		if err == nil && opts.Log != nil {
			opts.Log("CRD successfully uninstalled", "name", opts.GVR.GroupResource().String())
		}
		if err != nil {
			opts.Log("Error uninstalling CRD", "name", opts.GVR.GroupResource().String(), "error", err)
			return err
		}
	}

	if opts.SkipDeploy {
		opts.Log("Skipping deploy deletion")
		return nil
	}

	sa, clusterrole, clusterrolebinding, role, rolebinding, err := createRBACResources(opts.GVR, opts.NamespacedName, opts.ConfigurationGVR, opts.RBACFolderPath)
	if err != nil {
		opts.Log("Error creating RBAC resources", "error", err)
		return err
	}
	deploymentNSName := types.NamespacedName{
		Namespace: opts.NamespacedName.Namespace,
		Name:      opts.NamespacedName.Name + ControllerResourceSuffix,
	}
	dep := appsv1.Deployment{}
	err = templates.CreateK8sObject(
		&dep,
		opts.GVR,
		deploymentNSName,
		opts.DeploymentTemplatePath,
		"serviceAccountName", sa.Name)
	if err != nil {
		opts.Log("Error creating deployment object", "error", err)
		return err
	}
	err = kubecli.Uninstall(ctx, opts.KubeClient, &dep, kubecli.UninstallOptions{})
	if err != nil {
		opts.Log("Error uninstalling deployment", "name", dep.Name, "namespace", dep.Namespace, "error", err)
		return fmt.Errorf("error uninstalling deployment: %v", err)
	}
	cmNSName := types.NamespacedName{
		Namespace: opts.NamespacedName.Namespace,
		Name:      opts.NamespacedName.Name + ControllerResourceSuffix,
	}
	cm := corev1.ConfigMap{}
	err = templates.CreateK8sObject(&cm, opts.GVR, cmNSName, opts.ConfigmapTemplatePath,
		"composition_controller_sa_name", sa.Name,
		"composition_controller_sa_namespace", sa.Namespace)
	if err != nil {
		opts.Log("Error creating configmap object", "error", err)
		return err
	}

	err = kubecli.Uninstall(ctx, opts.KubeClient, &cm, kubecli.UninstallOptions{})
	if err != nil {
		opts.Log("Error uninstalling configmap", "name", cm.Name, "namespace", cm.Namespace, "error", err)
		return err
	}

	err = uninstallRBACResources(ctx, opts.KubeClient, clusterrole, clusterrolebinding, role, rolebinding, sa, opts.Log)
	if err != nil {
		opts.Log("Error uninstalling RBAC resources", "error", err)
		return err
	}

	opts.Log("RBAC resources successfully uninstalled", "gvr", opts.GVR.String(), "name", opts.NamespacedName.Name, "namespace", opts.NamespacedName.Namespace)

	return err
}

// This function is used to lookup the current state of the deployment and return the hash of the current state
// This is used to determine if the deployment needs to be updated or not
func Lookup(ctx context.Context, kube client.Client, opts DeployOptions) (digest string, err error) {
	if opts.Log == nil {
		return "", fmt.Errorf("log function is required")
	}

	sa, clusterrole, clusterrolebinding, role, rolebinding, err := createRBACResources(opts.GVR, opts.NamespacedName, opts.ConfigurationGVR, opts.RBACFolderPath)
	if err != nil {
		return "", err
	}

	hsh := hasher.NewFNVObjectHash()

	err = lookupRBACResources(ctx, opts.KubeClient, clusterrole, clusterrolebinding, role, rolebinding, sa, opts.Log, &hsh)
	if err != nil {
		// This site also gains its own log line. It was the only one of the three that logged nothing
		// itself, so the sole reason a failure here was visible at all is that lookupRBACResources logged
		// on its way out -- via opts.Log, which is e.log.Debug.
		return "", absenceOrError(opts.Log, "Error looking up RBAC resources", err)
	}

	cmNSName := types.NamespacedName{
		Namespace: opts.NamespacedName.Namespace,
		Name:      opts.NamespacedName.Name + ControllerResourceSuffix,
	}
	cm := corev1.ConfigMap{}
	err = templates.CreateK8sObject(&cm, opts.GVR, cmNSName, opts.ConfigmapTemplatePath,
		"composition_controller_sa_name", sa.Name,
		"composition_controller_sa_namespace", sa.Namespace)
	if err != nil {
		return "", err
	}

	err = kubecli.Get(ctx, opts.KubeClient, &cm)
	if err != nil {
		return "", absenceOrError(opts.Log, "Error fetching configmap", err)
	}
	err = hsh.SumHash(cm.ObjectMeta.Name, cm.ObjectMeta.Namespace, cm.Data)
	if err != nil {
		return "", fmt.Errorf("error hashing configmap: %v", err)
	}
	opts.Log("Configmap successfully fetched", "gvr", opts.GVR.String(), "name", cm.Name, "namespace", cm.Namespace, "digest", hsh.GetHash())

	deploymentNSName := types.NamespacedName{
		Namespace: opts.NamespacedName.Namespace,
		Name:      opts.NamespacedName.Name + ControllerResourceSuffix,
	}
	dep := appsv1.Deployment{}
	err = templates.CreateK8sObject(
		&dep,
		opts.GVR,
		deploymentNSName,
		opts.DeploymentTemplatePath,
		"serviceAccountName", sa.Name)
	if err != nil {
		return "", err
	}

	err = kubecli.Get(ctx, opts.KubeClient, &dep)
	if err != nil {
		return "", absenceOrError(opts.Log, "Error fetching deployment", err)
	}

	deployment.CleanFromRestartAnnotation(&dep)

	err = hsh.SumHash(dep.ObjectMeta.Name, dep.ObjectMeta.Namespace, dep.Spec)
	if err != nil {
		return "", fmt.Errorf("error hashing deployment spec: %v", err)
	}
	opts.Log("Deployment successfully fetched", "gvr", opts.GVR.String(), "name", dep.Name, "namespace", dep.Namespace, "digest", hsh.GetHash())

	return hsh.GetHash(), nil
}

// liveConfigMapData reads the data of the ConfigMap that `rendered` is about to replace.
//
// IT IS KEYED FROM `rendered` ON PURPOSE. The rendered object is the only authority on the name the
// apply will write, because that name comes from the asset's own `metadata.name` expression and not from
// any NamespacedName the caller holds. #599 was exactly this: the caller passed "<rd>-controller" while
// the asset renders "<rd>-controller-configmap", so the read missed by a suffix and the miss was
// indistinguishable from a first deploy.
//
// An absent ConfigMap really is not an error -- it means this is the first deploy, and a pod that has not
// started yet does not need restarting. That tolerance is why #599 was silent for a whole release, so the
// one thing this must never do is treat a lookup it could not perform as an absence: a rendered object
// with no name is a bug in the asset, not an absent object, and it is refused.
func liveConfigMapData(ctx context.Context, kube client.Client, rendered *corev1.ConfigMap) (map[string]string, bool, error) {
	if rendered.GetName() == "" {
		return nil, false, fmt.Errorf("the rendered ConfigMap has no name, so the live one cannot be located")
	}

	live := corev1.ConfigMap{}
	live.SetName(rendered.GetName())
	live.SetNamespace(rendered.GetNamespace())

	if gerr := kubecli.Get(ctx, kube, &live); gerr != nil {
		if apierrors.IsNotFound(gerr) {
			return nil, false, nil
		}
		return nil, false, gerr
	}
	return live.Data, true, nil
}

// restartNeeded reports whether the generated controller must be rolled to pick up a change.
//
// ONLY the ConfigMap's data can require this, and the reason is the mount style: the generated
// controller reads it through `envFrom: configMapRef`, so the values are fixed at container start and
// the reference is by NAME. A data change therefore leaves the pod template untouched, which means the
// Deployment has nothing to roll on and the running container keeps its old environment indefinitely.
//
// Everything else the pod consumes -- the image, explicit `env:` entries, volumes, resources -- is part
// of the pod template, and a Deployment whose pod template changes rolls its pods without being asked.
// Restarting for those would be redundant; restarting for RBAC would be meaningless, since authorization
// is evaluated by the apiserver on every request and the controller caches no decision.
//
// `cmExisted` false means this is the first deploy: there is no running pod with stale environment, and
// the Deployment being created alongside starts with the new ConfigMap already in place.
func restartNeeded(liveData, renderedData map[string]string, cmExisted bool) bool {
	if !cmExisted {
		return false
	}
	return !maps.Equal(liveData, renderedData)
}
