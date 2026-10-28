package restdefinition

import (
	"reflect"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	rtv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreatedStatusCoversEveryStatusField is the guard for the bug this type was introduced to prevent.
//
// Create's phase-2 write used to be Status().Update(ctx, cr), which persisted cr's WHOLE status -- so any
// earlier step that set a field on cr.Status in place was carried along for free. Writing a re-read object
// instead, which is what makes the retry safe, ends that silently: a field an upstream step sets and
// applyTo does not carry is never persisted, and nothing fails at the point of the mistake.
//
// It happened immediately. syncAuthSecretRBAC sets AuthSecretDigest and AuthSecretRBACNamespaces in place
// a few lines above the write, and the first version of the refactor dropped both. The symptoms were a
// perpetual redeploy (Observe's auth-secret drift check comparing an empty digest against a real one) and
// an RBAC leak on Delete (teardownAuthSecretRBAC returns early when AuthSecretRBACNamespaces is empty).
// Every test in the package passed, because none of them exercise Create's second phase.
//
// Reflection rather than a hand-written list of fields to check: a hand-maintained list is the same
// construct that failed, and would need the same discipline to keep current. Same reasoning as the
// Schema.deepCopy guard added in #249, which found a pre-existing drop on its first run.
func TestCreatedStatusCoversEveryStatusField(t *testing.T) {
	// Every field set to a distinctive non-zero value, so a field applyTo fails to carry shows up as a
	// zero on the target.
	st := createdStatus{
		resource:                 definitionv1alpha1.KindApiVersion{Kind: "ActionsVariableRepo", APIVersion: "github.krateo.io/v1-0"},
		configuration:            &definitionv1alpha1.KindApiVersion{Kind: "ActionsVariableRepoConfiguration", APIVersion: "github.krateo.io/v1-0"},
		oasPath:                  "configmap://krateo-system/gh/openapi.yaml",
		digest:                   "sha256:digest",
		hasSecuritySchemes:       true,
		oasHash:                  "sha256:oas",
		resourceHash:             "sha256:resource",
		authSecretDigest:         "sha256:authsecret",
		authSecretRBACNamespaces: []string{"ns-a", "ns-b"},
	}

	// Fail loudly if createdStatus itself grows a field this fixture leaves unset — otherwise the check
	// below silently stops covering it.
	fixture := reflect.ValueOf(st)
	for i := 0; i < fixture.NumField(); i++ {
		if fixture.Field(i).IsZero() {
			t.Errorf("createdStatus field %q is zero in this fixture, so it is not covered; set it",
				fixture.Type().Field(i).Name)
		}
	}

	target := &definitionv1alpha1.RestDefinition{}
	st.applyTo(target)

	// The real invariant: no field of RestDefinitionStatus may be left at its zero value. ConditionedStatus
	// is the one exception, and it is not skipped -- applyTo sets a Creating condition, so it is asserted
	// separately below rather than exempted.
	status := reflect.ValueOf(target.Status)
	for i := 0; i < status.NumField(); i++ {
		name := status.Type().Field(i).Name
		assert.False(t, status.Field(i).IsZero(),
			"RestDefinitionStatus.%s was not set by createdStatus.applyTo. If this field is newly added, "+
				"carry it on createdStatus and set it in applyTo -- a field an upstream step writes onto "+
				"cr.Status in place is otherwise never persisted, with nothing failing where the mistake is",
			name)
	}

	assert.NotEmpty(t, target.Status.Conditions, "applyTo must set the Creating condition")
}

// TestCreatedStatusCarriesTheAuthSecretFields is the named regression for the two fields that were actually
// dropped, so a failure points at the cause rather than at a reflection loop.
func TestCreatedStatusCarriesTheAuthSecretFields(t *testing.T) {
	st := createdStatus{
		authSecretDigest:         "sha256:authsecret",
		authSecretRBACNamespaces: []string{"krateo-system", "team-a"},
	}

	target := &definitionv1alpha1.RestDefinition{}
	st.applyTo(target)

	assert.Equal(t, "sha256:authsecret", target.Status.AuthSecretDigest,
		"an empty authSecretDigest makes Observe's drift check fire on every reconcile, redeploying forever")
	assert.Equal(t, []string{"krateo-system", "team-a"}, target.Status.AuthSecretRBACNamespaces,
		"an empty authSecretRBACNamespaces makes teardownAuthSecretRBAC return early on Delete, leaking RBAC")
}

// TestCreatedStatusApplyToIsIdempotent -- persistStatusWithRetry runs applyTo once per write attempt and
// once more to mirror onto the caller's copy, so applying it twice must be indistinguishable from once.
// A HasSecuritySchemes pointer aliased across calls would be the easy way to get this wrong.
func TestCreatedStatusApplyToIsIdempotent(t *testing.T) {
	st := createdStatus{
		resource:                 definitionv1alpha1.KindApiVersion{Kind: "K", APIVersion: "g/v"},
		oasPath:                  "p",
		digest:                   "d",
		hasSecuritySchemes:       true,
		oasHash:                  "o",
		resourceHash:             "r",
		authSecretDigest:         "a",
		authSecretRBACNamespaces: []string{"ns"},
	}

	once := &definitionv1alpha1.RestDefinition{}
	st.applyTo(once)

	twice := &definitionv1alpha1.RestDefinition{}
	st.applyTo(twice)
	st.applyTo(twice)

	require.NotNil(t, once.Status.HasSecuritySchemes)
	require.NotNil(t, twice.Status.HasSecuritySchemes)
	assert.Equal(t, *once.Status.HasSecuritySchemes, *twice.Status.HasSecuritySchemes)

	// Compare everything except the conditions, whose timestamps legitimately differ between calls.
	o, w := once.Status, twice.Status
	o.ConditionedStatus, w.ConditionedStatus = rtv1.ConditionedStatus{}, rtv1.ConditionedStatus{}
	assert.Equal(t, o, w, "applying twice must be indistinguishable from applying once")
}

// TestCreatedStatusLeavesConfigurationAloneWhenAbsent -- a RestDefinition that had a Configuration and no
// longer does keeps the record of what was deployed, which Delete needs in order to tear it down.
func TestCreatedStatusLeavesConfigurationAloneWhenAbsent(t *testing.T) {
	target := &definitionv1alpha1.RestDefinition{}
	target.Status.Configuration = definitionv1alpha1.KindApiVersion{Kind: "WasDeployed", APIVersion: "g/v"}

	createdStatus{configuration: nil}.applyTo(target)

	assert.Equal(t, "WasDeployed", target.Status.Configuration.Kind,
		"a nil configuration must leave the recorded one in place, not zero it")
}
