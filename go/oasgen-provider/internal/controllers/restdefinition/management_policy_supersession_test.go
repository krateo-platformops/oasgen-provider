//go:build unit || integration

package restdefinition

import (
	"testing"

	"github.com/krateo-platformops/plumbing/managementpolicy"
	"github.com/krateo-platformops/provider-runtime/pkg/meta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestInvalidManagementPolicyWithholdsEveryMutatingVerb replaces the check this commit deletes.
//
// WHY A TEST AND NOT A NOTE. management_policy_invalid.go is removed because provider-runtime reports
// the same fault itself from v1.21.0, on Synced, with the same reason string. That supersession is
// only complete because an INVALID policy withholds every mutating verb: the runtime's three
// post-Observe policy gates (before Delete, Create and Update) are reached only when the policy
// ALLOWS that action, so if an invalid value ever permitted one, those paths would run and the
// runtime's report would not cover them.
//
// The provider-runtime session, asked directly, verified the gates and then named exactly this as the
// thing they had NOT read: "I read May's behaviour for an invalid value from that message and its
// callers, not from plumbing's managementpolicy source; your own assertion test is the place to pin
// it." This is that place.
//
// MEASURED in plumbing: invalid() calls of() with NO verbs, so Policy.allowed is empty and Allows is
// false for every verb. The deleted check is what would have caught a change to that, so the test
// takes over the job rather than documenting it.
func TestInvalidManagementPolicyWithholdsEveryMutatingVerb(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{}}
	obj.SetAnnotations(map[string]string{managementpolicy.AnnotationKey: "defualt"}) // a real typo shape

	d := meta.ManagementDecision(obj)
	require.NotEmpty(t, d.Policy.Invalid,
		"the annotation must be reported as unparseable, or this test is asserting nothing about an "+
			"invalid policy -- it would be exercising whatever valid policy the value happened to parse as")

	for _, v := range []managementpolicy.Verb{managementpolicy.Create, managementpolicy.Update, managementpolicy.Delete} {
		assert.False(t, d.May(v),
			"an invalid management policy must withhold %q: the runtime's gate before that action is "+
				"reached only when the policy allows it, so permitting it here would run a path the "+
				"runtime's ManagementPolicyInvalid report does not cover", v)
	}

	// CONTROL: a VALID policy must permit what it names, or the assertions above would pass on a
	// decision that withholds everything unconditionally -- which would make them vacuous.
	ok := &unstructured.Unstructured{Object: map[string]any{}}
	ok.SetAnnotations(map[string]string{managementpolicy.AnnotationKey: "observe,create"})
	dok := meta.ManagementDecision(ok)
	require.Empty(t, dok.Policy.Invalid, "the control's policy must parse")
	assert.True(t, dok.May(managementpolicy.Create), "control: a policy naming create must permit it")
	assert.False(t, dok.May(managementpolicy.Delete), "control: a policy not naming delete must withhold it")
}
