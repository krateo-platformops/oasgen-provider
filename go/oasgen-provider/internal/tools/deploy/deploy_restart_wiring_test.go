//go:build unit || integration

package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// THE WIRING, which is the thing nobody had tested and the thing #599 actually broke.
//
// restartNeeded was tested in isolation (restart_gate_test.go, 6 input shapes, both outcomes) and the
// live read is tested in isolation (live_configmap_read_test.go). Both were correct. #599 lived in the
// WIRING between them: Deploy handed restartNeeded a cmExisted that was always false, because the live
// read was keyed from the Deployment's name.
//
// Mutation testing proved the gap was real rather than theoretical. With only the two isolated suites,
// `restartNeeded(cm.Data, cm.Data, cmExisted)` and `restartNeeded(liveData, cm.Data, false)` BOTH
// SURVIVED -- two mutations that disable the restart entirely, neither caught by any test. This file
// exists to kill them, by driving the real Deploy against a real renderer and asking the apiserver what
// happened to the Deployment.
//
// It changes the ConfigMap's data the way reality does: by REWRITING THE TEMPLATE FILE. That is exactly
// what `helm upgrade --set-string rdc.env.X=Y` does -- Helm ranges .Values.rdc.env into the asset at
// chart-render time, so the provider's input is a file on disk and a change to it is a file change.

// writeTemplates lays down the seven templates Deploy renders, using the REAL name expressions from
// helm/oasgen-provider/assets/rdc/ -- in particular `{{ .name }}-configmap` for the ConfigMap and
// `{{ .name }}` for the Deployment, which is the difference #599 turned on.
func writeTemplates(t *testing.T, dir, debugValue string) {
	t.Helper()
	rbac := filepath.Join(dir, "rbac")
	require.NoError(t, os.MkdirAll(rbac, 0o755))

	files := map[string]string{
		filepath.Join(rbac, "serviceaccount.yaml"): `apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ .name }}
  namespace: {{ .namespace }}
`,
		filepath.Join(rbac, "clusterrole.yaml"): `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{ .resource }}-{{ .apiVersion }}
rules:
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create"]
`,
		filepath.Join(rbac, "clusterrolebinding.yaml"): `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{ .resource }}-{{ .apiVersion }}
subjects:
  - kind: ServiceAccount
    name: {{ .serviceAccount }}
    namespace: {{ .saNamespace }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ .resource }}-{{ .apiVersion }}
`,
		filepath.Join(rbac, "role.yaml"): `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ .resource }}-{{ .apiVersion }}
  namespace: {{ .namespace }}
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get"]
`,
		filepath.Join(rbac, "rolebinding.yaml"): `apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ .resource }}-{{ .apiVersion }}
  namespace: {{ .namespace }}
subjects:
  - kind: ServiceAccount
    name: {{ .serviceAccount }}
    namespace: {{ .saNamespace }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ .resource }}-{{ .apiVersion }}
`,
		// THE SUFFIX. Copied from the real asset, and the reason the live read must be keyed from the
		// rendered object rather than from the NamespacedName the caller holds.
		filepath.Join(dir, "configmap.yaml"): fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .name }}-configmap
  namespace: {{ .namespace }}
data:
  REST_CONTROLLER_SERVICEACCOUNT_NAME: {{ .composition_controller_sa_name }}
  HOME: /tmp
  REST_CONTROLLER_DEBUG: %q
`, debugValue),
		// NO annotations, exactly like the real asset -- which is what makes the apply strip restartedAt
		// (issue 595, fixed separately in #597).
		filepath.Join(dir, "deployment.yaml"): `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .name }}
  namespace: {{ .namespace }}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: {{ .name }}
  template:
    metadata:
      labels:
        app: {{ .name }}
    spec:
      serviceAccountName: {{ .serviceAccountName }}
      containers:
        - name: rdc
          image: rest-dynamic-controller:test
          envFrom:
            - configMapRef:
                name: {{ .name }}-configmap
`,
	}
	for p, body := range files {
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
}

func wiringScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))
	require.NoError(t, rbacv1.AddToScheme(s))
	return s
}

func TestDeployRestartsOnlyWhenTheConfigMapDataActuallyChanged(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeTemplates(t, dir, "false")

	kube := fake.NewClientBuilder().WithScheme(wiringScheme(t)).Build()
	gvr := schema.GroupVersionResource{Group: "petstore.krateo.io", Version: "v1alpha1", Resource: "pets"}

	opts := DeployOptions{
		KubeClient:             kube,
		GVR:                    gvr,
		NamespacedName:         types.NamespacedName{Namespace: "krateo-system", Name: "petstore"},
		RBACFolderPath:         filepath.Join(dir, "rbac"),
		ConfigmapTemplatePath:  filepath.Join(dir, "configmap.yaml"),
		DeploymentTemplatePath: filepath.Join(dir, "deployment.yaml"),
		Log:                    func(msg string, kv ...any) {},
	}

	depKey := client.ObjectKey{Namespace: "krateo-system", Name: "petstore" + ControllerResourceSuffix}
	cmKey := client.ObjectKey{Namespace: "krateo-system", Name: "petstore" + ControllerResourceSuffix + "-configmap"}

	restartedAt := func(t *testing.T) (string, bool) {
		t.Helper()
		dep := appsv1.Deployment{}
		require.NoError(t, kube.Get(ctx, depKey, &dep))
		v, ok := dep.Spec.Template.Annotations[deployment.RestartAnnotation]
		return v, ok
	}

	// ---- pass 1: the first deploy. Nothing exists, so there is no pod to roll.
	_, err := Deploy(ctx, kube, opts)
	require.NoError(t, err)

	cm := corev1.ConfigMap{}
	require.NoError(t, kube.Get(ctx, cmKey, &cm),
		"the ConfigMap is not at <rd>-controller-configmap, so this test's template no longer matches the "+
			"real asset and the names #599 turned on are not being exercised")
	require.Equal(t, "false", cm.Data["REST_CONTROLLER_DEBUG"], "premise: pass 1 renders the old value")

	_, ok := restartedAt(t)
	assert.False(t, ok,
		"the first deploy restarted a controller that had no running pod. Creating the Deployment and "+
			"rolling it in the same pass is a wasted rollout on every fresh install")

	// ---- pass 2: the template changes, exactly as a `helm upgrade` of rdc.env changes it.
	writeTemplates(t, dir, "true")
	_, err = Deploy(ctx, kube, opts)
	require.NoError(t, err)

	require.NoError(t, kube.Get(ctx, cmKey, &cm))
	require.Equal(t, "true", cm.Data["REST_CONTROLLER_DEBUG"],
		"premise: the apply must have persisted the new data, or there is nothing for the gate to react to")

	stamp, ok := restartedAt(t)
	require.True(t, ok,
		"THE ConfigMap's DATA CHANGED AND THE CONTROLLER WAS NOT RESTARTED. This is issue 599: envFrom is "+
			"read once at container start, so the running controller keeps its old environment forever, "+
			"silently and with no error. 0.59.0 shipped in this state for every kind.")
	assert.NotEmpty(t, stamp)

	// ---- pass 3: nothing changes. The stamp must not move.
	_, err = Deploy(ctx, kube, opts)
	require.NoError(t, err)

	again, ok := restartedAt(t)
	require.True(t, ok,
		"the restart annotation disappeared on a reconcile that changed nothing. That costs a second, "+
			"pointless rollout (issue 595) because removing the annotation changes the pod template as "+
			"much as adding it does")
	assert.Equal(t, stamp, again,
		"the restart annotation changed value on a reconcile that changed nothing, so the controller rolls "+
			"on every poll (issue 588)")
}

// The gate must not fire on a change that is NOT the ConfigMap's data.
//
// This is the arm that #588 was filed for: a secretRef Secret-name change rewrites a Role's
// resourceNames and nothing the pod consumes, and authorization is evaluated per request with no cached
// decision -- so rolling the controller for it loses its logs and achieves nothing.
func TestDeployDoesNotRestartWhenOnlyRBACChanged(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeTemplates(t, dir, "false")

	kube := fake.NewClientBuilder().WithScheme(wiringScheme(t)).Build()
	opts := DeployOptions{
		KubeClient:             kube,
		GVR:                    schema.GroupVersionResource{Group: "petstore.krateo.io", Version: "v1alpha1", Resource: "pets"},
		NamespacedName:         types.NamespacedName{Namespace: "krateo-system", Name: "petstore"},
		RBACFolderPath:         filepath.Join(dir, "rbac"),
		ConfigmapTemplatePath:  filepath.Join(dir, "configmap.yaml"),
		DeploymentTemplatePath: filepath.Join(dir, "deployment.yaml"),
		Log:                    func(msg string, kv ...any) {},
	}
	depKey := client.ObjectKey{Namespace: "krateo-system", Name: "petstore" + ControllerResourceSuffix}

	_, err := Deploy(ctx, kube, opts)
	require.NoError(t, err)

	// Change a Role's rules only -- the ConfigMap template is untouched.
	rolePath := filepath.Join(dir, "rbac", "role.yaml")
	src, err := os.ReadFile(rolePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(rolePath,
		[]byte(string(src)+"  - apiGroups: [\"\"]\n    resources: [\"secrets\"]\n    verbs: [\"list\"]\n"), 0o644))

	_, err = Deploy(ctx, kube, opts)
	require.NoError(t, err)

	dep := appsv1.Deployment{}
	require.NoError(t, kube.Get(ctx, depKey, &dep))
	assert.NotContains(t, dep.Spec.Template.Annotations, deployment.RestartAnnotation,
		"an RBAC-only change restarted the generated controller. Authorization is evaluated by the "+
			"apiserver per request and the controller caches no decision, so the restart achieves nothing "+
			"and loses the pod's logs (issue 588)")
}
