package policy

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// A cluster that does not serve MutatingAdmissionPolicy used to be tolerated: EnsureVersionPolicy
// returned (OutcomeUnsupported, nil) because rest-dynamic-controller stamped the label itself.
//
// Version-scoped watching removed that fallback, so there is no longer anything covering an absent
// policy. Tolerating it would produce an install that comes up green and reconciles no instance at all,
// which is the single worst outcome available here.
func TestAbsentPolicyAPIIsAnErrorNotATolerance(t *testing.T) {
	kube := fake.NewClientBuilder().
		WithScheme(policyScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
				return fmt.Errorf(`no matches for kind "MutatingAdmissionPolicy" in version "admissionregistration.k8s.io/v1"`)
			},
		}).Build()

	outcome, err := EnsureVersionPolicy(context.Background(), kube, "github.krateo.io")

	require.Error(t, err, "an absent policy API must not be reported as success")
	assert.Equal(t, OutcomeUnknown, outcome)

	var unsupported *ErrPolicyAPIUnsupported
	require.True(t, errors.As(err, &unsupported), "callers must be able to tell this from any other failure")
	assert.Equal(t, "github.krateo.io", unsupported.Group)

	msg := unsupported.Error()
	assert.Contains(t, msg, "1.36", "the message must name the version that provides the API")
	assert.Contains(t, msg, VersionLabel, "and the label that would go unwritten")
	assert.Contains(t, msg, "matched by no controller's watch", "and the consequence, which is the part that is not obvious")
}

// IsUnsupported still has to recognise the shapes, since it is what builds the typed error above.
func TestUnsupportedDetectionStillFeedsTheTypedError(t *testing.T) {
	for _, msg := range []string{
		`no matches for kind "MutatingAdmissionPolicy"`,
		"could not find the requested resource",
		"no kind is registered for the type",
	} {
		kube := fake.NewClientBuilder().
			WithScheme(policyScheme(t)).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
					return fmt.Errorf("%s", msg)
				},
			}).Build()

		_, err := EnsureVersionPolicy(context.Background(), kube, "g.example.io")
		var unsupported *ErrPolicyAPIUnsupported
		assert.True(t, errors.As(err, &unsupported), "should classify %q as an absent API", msg)
	}
}
