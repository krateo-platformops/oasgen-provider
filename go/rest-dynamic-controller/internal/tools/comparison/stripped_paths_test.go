//go:build unit

package comparison

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE DISCRIMINATING PAIR, and it is why this fix is a key count rather than a weakening of the
// remote-empty check. Both cases were measured on real Ironic nodes by the same consumer, both are a
// CR-populated object against `{}` on the remote, and they have OPPOSITE correct answers.
//
//	extra: {bmc_secret}        remote {}   the request sent {} -- the strip removed the pointer   EQUAL
//	instance_info: {image_*}   remote {}   the request sent the fields; the vendor dropped them   DRIFT
//
// One thing separates them: whether the request could ever have carried the key. Anything that cannot
// tell these apart is either reinstating #663 or breaking the check #663's neighbour exists for.
func TestTheRemoteEmptyCheckDiscriminatesByWhetherTheRequestCarriedTheKey(t *testing.T) {
	stripped := NewComparer(nil).WithStrippedPaths([]string{"extra.bmc_secret"})

	t.Run("a container holding only a resolver source is not drift", func(t *testing.T) {
		mg := map[string]interface{}{"extra": map[string]interface{}{
			"bmc_secret": map[string]interface{}{"name": "bmc", "key": "password"},
		}}
		rm := map[string]interface{}{"extra": map[string]interface{}{}}

		res, err := stripped.CompareExisting(mg, rm)
		require.NoError(t, err)
		assert.True(t, res.IsEqual,
			"the strip emptied this container itself, so the remote `{}` is what we sent; got %s", res)
	})

	t.Run("CONTROL: the same shape with ordinary API fields stays drift", func(t *testing.T) {
		mg := map[string]interface{}{"instance_info": map[string]interface{}{
			"image_source":   "http://example/img",
			"image_checksum": "sha256:abc",
		}}
		rm := map[string]interface{}{"instance_info": map[string]interface{}{}}

		res, err := stripped.CompareExisting(mg, rm)
		require.NoError(t, err)
		require.False(t, res.IsEqual,
			"a vendor that dropped fields the request DID send is real drift -- this is the measured "+
				"case the remote-empty check exists for, which went Synced=True with no PATCH before it")
		require.NotNil(t, res.Reason)
		assert.Equal(t, "object is set in the custom resource but empty on the remote", res.Reason.Reason)
	})

	t.Run("CONTROL: without the declaration the resolver case is still drift", func(t *testing.T) {
		mg := map[string]interface{}{"extra": map[string]interface{}{
			"bmc_secret": map[string]interface{}{"name": "bmc", "key": "password"},
		}}
		rm := map[string]interface{}{"extra": map[string]interface{}{}}

		res, err := NewComparer(nil).CompareExisting(mg, rm)
		require.NoError(t, err)
		assert.False(t, res.IsEqual,
			"the declaration must be what changes the verdict, or the first case passes for some other reason")
	})
}

// THE SURGICAL CASE. A container holding a resolver source AND a real field, with the remote empty,
// must still be drift -- the real field was sent and is missing. An all-or-nothing "this container has
// a stripped key, skip it" would get this wrong, and it is the shape an Ironic node actually has:
// `extra` carries bmc_secret plus operational keys written at runtime.
func TestAMixedContainerIsJudgedOnTheKeysTheRequestCarried(t *testing.T) {
	c := NewComparer(nil).WithStrippedPaths([]string{"extra.bmc_secret"})
	ptr := map[string]interface{}{"name": "bmc", "key": "password"}

	t.Run("remote empty, CR has a real sibling: DRIFT", func(t *testing.T) {
		mg := map[string]interface{}{"extra": map[string]interface{}{"bmc_secret": ptr, "etcd_ip": "10.0.0.1"}}
		rm := map[string]interface{}{"extra": map[string]interface{}{}}

		res, err := c.CompareExisting(mg, rm)
		require.NoError(t, err)
		assert.False(t, res.IsEqual, "etcd_ip was sent and the remote has nothing; that is drift")
	})

	t.Run("remote has the real sibling: EQUAL", func(t *testing.T) {
		mg := map[string]interface{}{"extra": map[string]interface{}{"bmc_secret": ptr, "etcd_ip": "10.0.0.1"}}
		rm := map[string]interface{}{"extra": map[string]interface{}{"etcd_ip": "10.0.0.1"}}

		res, err := c.CompareExisting(mg, rm)
		require.NoError(t, err)
		assert.True(t, res.IsEqual, "got %s", res)
	})

	t.Run("remote has the real sibling CHANGED: DRIFT on that field", func(t *testing.T) {
		mg := map[string]interface{}{"extra": map[string]interface{}{"bmc_secret": ptr, "etcd_ip": "10.0.0.1"}}
		rm := map[string]interface{}{"extra": map[string]interface{}{"etcd_ip": "10.0.0.9"}}

		res, err := c.CompareExisting(mg, rm)
		require.NoError(t, err)
		require.False(t, res.IsEqual)
		require.NotNil(t, res.Reason)
		assert.Equal(t, "extra.etcd_ip", res.Reason.FieldPath(),
			"the reported field must be the real one, not the pointer")
	})
}

// A resolver source is skipped on the DECLARATION, not on the response being silent about it. A vendor
// that echoes unknown fields back would otherwise have its echo compared against the CR's {name,key}
// pointer, which is meaningless -- and would make the verdict depend on how chatty the vendor is.
func TestAResolverSourceIsSkippedEvenWhenTheVendorEchoesItBack(t *testing.T) {
	c := NewComparer(nil).WithStrippedPaths([]string{"extra.bmc_secret"})

	mg := map[string]interface{}{"extra": map[string]interface{}{
		"bmc_secret": map[string]interface{}{"name": "bmc", "key": "password"},
		"etcd_ip":    "10.0.0.1",
	}}
	rm := map[string]interface{}{"extra": map[string]interface{}{
		"bmc_secret": "something-the-vendor-made-up",
		"etcd_ip":    "10.0.0.1",
	}}

	res, err := c.CompareExisting(mg, rm)
	require.NoError(t, err)
	assert.True(t, res.IsEqual,
		"the pointer must not be compared against the vendor's echo of it; got %s", res)
}

// A top-level resolver source needs no container handling at all, and must not be compared either.
func TestATopLevelResolverSourceIsSkipped(t *testing.T) {
	c := NewComparer(nil).WithStrippedPaths([]string{"clientSecretRef"})

	mg := map[string]interface{}{
		"clientSecretRef": map[string]interface{}{"name": "s", "key": "k"},
		"name":            "app",
	}
	rm := map[string]interface{}{"name": "app"}

	res, err := c.CompareExisting(mg, rm)
	require.NoError(t, err)
	assert.True(t, res.IsEqual, "got %s", res)
}

// Index segments are stripped before lookup, so a resolver source inside an array element resolves
// against the declaration-derived path, which carries no index.
func TestAResolverSourceInsideAnArrayElementResolves(t *testing.T) {
	c := NewComparer(nil).WithStrippedPaths([]string{"credentials.valueRef"})

	mg := map[string]interface{}{"credentials": []interface{}{
		map[string]interface{}{"type": "password", "valueRef": map[string]interface{}{"name": "s", "key": "k"}},
	}}
	rm := map[string]interface{}{"credentials": []interface{}{
		map[string]interface{}{"type": "password"},
	}}

	res, err := c.CompareExisting(mg, rm)
	require.NoError(t, err)
	assert.True(t, res.IsEqual, "got %s", res)

	// CONTROL: the element's real field is still compared.
	rm2 := map[string]interface{}{"credentials": []interface{}{
		map[string]interface{}{"type": "otp"},
	}}
	res2, err := c.CompareExisting(mg, rm2)
	require.NoError(t, err)
	assert.False(t, res2.IsEqual, "a changed real field inside the element must still be drift")
}

// An element whose ONLY key is a resolver source, against an empty remote element, is the array-shaped
// twin of the headline case.
func TestAnArrayElementHoldingOnlyAResolverSourceIsNotDrift(t *testing.T) {
	c := NewComparer(nil).WithStrippedPaths([]string{"credentials.valueRef"})

	mg := map[string]interface{}{"credentials": []interface{}{
		map[string]interface{}{"valueRef": map[string]interface{}{"name": "s", "key": "k"}},
	}}
	rm := map[string]interface{}{"credentials": []interface{}{
		map[string]interface{}{},
	}}

	res, err := c.CompareExisting(mg, rm)
	require.NoError(t, err)
	assert.True(t, res.IsEqual, "got %s", res)
}

func TestWithStrippedPathsIsANoOpWhenEmpty(t *testing.T) {
	assert.Nil(t, NewComparer(nil).WithStrippedPaths(nil).strippedPaths)
	assert.Nil(t, NewComparer(nil).WithStrippedPaths([]string{}).strippedPaths)
	assert.Nil(t, NewComparer(nil).WithStrippedPaths([]string{""}).strippedPaths,
		"an empty path is not a declaration")
}
