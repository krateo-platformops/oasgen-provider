//go:build unit

package deploy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// THE BUG THIS EXISTS TO PREVENT SHIPPED ONCE AND WAS FOUND BY A CONSUMER, NOT BY A TEST.
//
// The RDC RBAC objects are named with the FIXED rbacVersion so a CRD version bump does not orphan the
// previous set. The RDC Deployment is rendered with the OBSERVED version. A controller that derived its
// own Lease name from its -version flag therefore computed a string its Role does not grant: it could not
// acquire, and controller-runtime EXITS -- a crash loop across every generated controller of every kind
// whose CR version is not v1alpha1.
//
// github-provider-kog found it by reading deploy.go against their 32 kinds at v2022-11-28. The agreement
// check that shipped with the bug compared the Go format string to the YAML template's TEXT, and both
// read "<resource>-<apiGroup>-<apiVersion>" -- the same words bound to different values, which is exactly
// what a textual comparison cannot see.
func TestTheLeaseNameUsesTheFIXEDRBACVersionAndNotTheObservedOne(t *testing.T) {
	// A kind whose served version is NOT v1alpha1. This is the case that was broken, and the only one
	// where the two versions differ -- so a test using v1alpha1 would pass under the bug.
	observed := schema.GroupVersionResource{
		Group:    "github.krateo.io",
		Version:  "v2022-11-28",
		Resource: "pullrequests",
	}

	got := generatedRBACName(observed)

	assert.Equal(t, "pullrequests-github.krateo.io-v1alpha1", got,
		"the Lease name must be the generated Role's name, which uses rbacVersion")
	assert.NotContains(t, got, observed.Version,
		"if the OBSERVED version appears in the name, the controller will compute a Lease its Role does "+
			"not grant and will exit on startup -- the shipped bug, for every kind not at v1alpha1")
	assert.Contains(t, got, rbacVersion,
		"and the fixed version must appear, or the name does not match the Role at all")
}

// THE CONTROL. Without it, a generatedRBACName that ignored its argument entirely would pass the case
// above and produce the same wrong name for every resource in the cluster -- one Lease shared by all of
// them, which is the opposite of one writer per Kind x version.
func TestTheLeaseNameStillDistinguishesResources(t *testing.T) {
	a := generatedRBACName(schema.GroupVersionResource{Group: "g", Version: "v1", Resource: "alpha"})
	b := generatedRBACName(schema.GroupVersionResource{Group: "g", Version: "v1", Resource: "beta"})
	assert.NotEqual(t, a, b, "two resources must not share a Lease")

	c := generatedRBACName(schema.GroupVersionResource{Group: "other", Version: "v1", Resource: "alpha"})
	assert.NotEqual(t, a, c, "two groups must not share a Lease")

	// And the version argument must make NO difference, which is the inverse of the assertion above and
	// the property that makes the name stable across a CRD version bump.
	d := generatedRBACName(schema.GroupVersionResource{Group: "g", Version: "v99", Resource: "alpha"})
	assert.Equal(t, a, d, "the name must not move when the served version does: that is why the RBAC "+
		"objects are named with a fixed version in the first place")
}
