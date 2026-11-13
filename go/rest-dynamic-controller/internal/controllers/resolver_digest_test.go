//go:build unit || integration

package restResources

import (
	"os"
	"regexp"
	"strings"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/fieldmapping"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func secretRefEntry(inBody, nameFrom, keyFrom string) getter.FieldMappingItem {
	return getter.FieldMappingItem{
		InBody: inBody,
		Resolver: &getter.FieldResolver{
			Type: "secretRef",
			SecretRef: &getter.SecretRefResolver{
				NameFromCustomResource: nameFrom,
				KeyFromCustomResource:  keyFrom,
			},
		},
	}
}

func crWith(obj map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: obj}
	u.SetNamespace("ns1")
	return u
}

// ---------------------------------------------------------------- the digest itself

// A DIGEST THAT CANNOT DISTINGUISH TWO SOURCE SETS IS NOT A CHANGE DETECTOR.
//
// The length prefix exists for this: without it "a\x00bc" and "ab\x00c" concatenate identically, so
// a resolver repointed from secret "a" key "bc" to secret "ab" key "c" would digest the same and the
// rotation would be invisible. Asserting the two differ is the only way that prefix is load-bearing
// rather than decorative.
func TestTheDigestSeparatesSetsThatConcatenateIdentically(t *testing.T) {
	a := resolverDigest([]string{"a\x00bc"})
	b := resolverDigest([]string{"ab\x00c"})
	assert.NotEqual(t, a, b,
		"two source sets whose concatenations are equal digest the same, so the length prefix is not "+
			"doing its job and a repointed secretRef is undetectable")

	// CONTROL: the function must agree with itself, or the inequality above proves nothing.
	assert.Equal(t, a, resolverDigest([]string{"a\x00bc"}), "the digest is not deterministic")
}

func TestTheDigestRespondsToEveryInput(t *testing.T) {
	base := []string{"k1\x00ns1/s1\x00key1\x00rv100\x00present"}
	d := resolverDigest(base)

	for _, tc := range []struct {
		name  string
		parts []string
	}{
		{"a different resourceVersion", []string{"k1\x00ns1/s1\x00key1\x00rv101\x00present"}},
		{"the key became absent", []string{"k1\x00ns1/s1\x00key1\x00rv100\x00absent"}},
		{"repointed to another secret", []string{"k1\x00ns1/s2\x00key1\x00rv100\x00present"}},
		{"repointed to another key", []string{"k1\x00ns1/s1\x00key2\x00rv100\x00present"}},
		{"a second source added", append(append([]string{}, base...), "k2\x00ns1/s9\x00k\x00rv1\x00present")},
		{"no sources at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotEqual(t, d, resolverDigest(tc.parts),
				"the digest did not move: %s is undetectable", tc.name)
		})
	}
}

// ---------------------------------------------------------------- the scope rule

func TestUpdateScopedMappingTakesTheUpdateVerbOnly(t *testing.T) {
	onCreate := secretRefEntry("password", "spec.ref.name", "spec.ref.key")
	onUpdate := secretRefEntry("token", "spec.other.name", "spec.other.key")

	r := &getter.Resource{VerbsDescription: []getter.VerbsDescription{
		{Action: "create", FieldMapping: []getter.FieldMappingItem{onCreate}},
		{Action: "get"},
		{Action: "update", FieldMapping: []getter.FieldMappingItem{onUpdate}},
	}}
	got := updateScopedSecretRefMapping(r)
	require.Len(t, got, 1, "the update verb's mapping has one entry")
	assert.Equal(t, "token", got[0].InBody,
		"the create verb's resolver was selected; an update cannot push it, so a difference in it "+
			"would be drift no update can retire")

	// Both halves of the conjunction: no update verb must select NOTHING, not fall back to another.
	noUpdate := &getter.Resource{VerbsDescription: []getter.VerbsDescription{
		{Action: "create", FieldMapping: []getter.FieldMappingItem{onCreate}},
		{Action: "get"},
	}}
	assert.Empty(t, updateScopedSecretRefMapping(noUpdate),
		"a resource with no update verb has no remedy for any difference, so it must report none")
	assert.Empty(t, updateScopedSecretRefMapping(nil), "a nil resource must not panic or select anything")
}

// ---------------------------------------------------------------- source collection

func TestCollectSourcesSeparatesAnUnsetRefFromABadPath(t *testing.T) {
	mapping := []getter.FieldMappingItem{secretRefEntry("password", "spec.ref.name", "spec.ref.key")}

	t.Run("set on this instance", func(t *testing.T) {
		got, err := fieldmapping.CollectSecretRefSources(mapping, crWith(map[string]any{
			"spec": map[string]any{"ref": map[string]any{"name": "s1", "key": "k1"}},
		}))
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "s1", got[0].Name)
		assert.Equal(t, "k1", got[0].Key)
		assert.Equal(t, "ns1", got[0].Namespace)
	})

	t.Run("not set on this instance is a skip, not an error", func(t *testing.T) {
		got, err := fieldmapping.CollectSecretRefSources(mapping, crWith(map[string]any{
			"spec": map[string]any{},
		}))
		require.NoError(t, err, "an optional ref this CR does not carry must not fail the collection")
		assert.Empty(t, got)
	})

	t.Run("an explicit null is an absence", func(t *testing.T) {
		got, err := fieldmapping.CollectSecretRefSources(mapping, crWith(map[string]any{
			"spec": map[string]any{"ref": map[string]any{"name": nil, "key": "k1"}},
		}))
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("a non-string ref is an ERROR, not an absence", func(t *testing.T) {
		_, err := fieldmapping.CollectSecretRefSources(mapping, crWith(map[string]any{
			"spec": map[string]any{"ref": map[string]any{"name": int64(7), "key": "k1"}},
		}))
		require.Error(t, err,
			"a ref whose name is a number read as 'unset' would drop the resolver from the digest "+
				"silently, for as long as it stayed wrong")
		assert.Contains(t, err.Error(), "not a string")
	})
}

func TestCollectSourcesSkipsWhatTheRequestWouldNotCarry(t *testing.T) {
	cr := crWith(map[string]any{
		"spec": map[string]any{"ref": map[string]any{"name": "s1", "key": "k1"}},
	})

	t.Run("a dependentLookup contributes nothing", func(t *testing.T) {
		got, err := fieldmapping.CollectSecretRefSources([]getter.FieldMappingItem{{
			InBody: "userId",
			Resolver: &getter.FieldResolver{
				Type:            "dependentLookup",
				DependentLookup: &getter.DependentLookupRef{Name: "l"},
			},
		}}, cr)
		require.NoError(t, err)
		assert.Empty(t, got,
			"resolving a dependentLookup reaches the VENDOR; including one here would cost a round-trip "+
				"per CR per poll to compute a digest")
	})

	t.Run("a when guard that does not hold skips the entry", func(t *testing.T) {
		e := secretRefEntry("password", "spec.ref.name", "spec.ref.key")
		e.Resolver.When = &getter.ResolverWhen{CustomResourceFieldPresent: "spec.enableIt"}
		got, err := fieldmapping.CollectSecretRefSources([]getter.FieldMappingItem{e}, cr)
		require.NoError(t, err)
		assert.Empty(t, got, "the guard does not hold, so the request will not carry this value either")

		// POSITIVE CONTROL: the same entry with the guard satisfied MUST be collected, or the
		// assertion above would pass against a collector that skips every guarded entry.
		on := crWith(map[string]any{"spec": map[string]any{
			"ref": map[string]any{"name": "s1", "key": "k1"}, "enableIt": "yes",
		}})
		got, err = fieldmapping.CollectSecretRefSources([]getter.FieldMappingItem{e}, on)
		require.NoError(t, err)
		assert.Len(t, got, 1, "a guarded entry whose guard HOLDS must be collected")
	})

	t.Run("a response-direction entry addresses no request destination", func(t *testing.T) {
		e := secretRefEntry("", "spec.ref.name", "spec.ref.key")
		e.InResponse = "something"
		got, err := fieldmapping.CollectSecretRefSources([]getter.FieldMappingItem{e}, cr)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestCollectSourcesIsStableAndDeduplicated(t *testing.T) {
	cr := crWith(map[string]any{
		"spec": map[string]any{"ref": map[string]any{"name": "s1", "key": "k1"}},
	})
	// Two entries pointing at the same secret+key: one Secret, so one source.
	a := secretRefEntry("password", "spec.ref.name", "spec.ref.key")
	b := secretRefEntry("password2", "spec.ref.name", "spec.ref.key")

	fwd, err := fieldmapping.CollectSecretRefSources([]getter.FieldMappingItem{a, b}, cr)
	require.NoError(t, err)
	rev, err := fieldmapping.CollectSecretRefSources([]getter.FieldMappingItem{b, a}, cr)
	require.NoError(t, err)

	require.Len(t, fwd, 2, "two distinct resolver keys are two sources even against one Secret")
	assert.Equal(t, fwd, rev, "the order of the mapping slice changed the collected set")
	assert.Equal(t,
		resolverDigest([]string{fwd[0].String(), fwd[1].String()}),
		resolverDigest([]string{rev[0].String(), rev[1].String()}),
		"reordering the mapping changes the digest, which would issue an update on a no-op edit")
}

// ---------------------------------------------------------------- read / write / read-back

func TestRecordedDigestTreatsEveryUnusableShapeAsNeverRecorded(t *testing.T) {
	for _, tc := range []struct {
		name string
		obj  map[string]any
	}{
		{"no status at all", map[string]any{}},
		{"status without the field", map[string]any{"status": map[string]any{"id": "x"}}},
		{"the field is null", map[string]any{"status": map[string]any{statusResolvedResolversDigest: nil}}},
		{"the field is not a string", map[string]any{"status": map[string]any{statusResolvedResolversDigest: int64(1)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, "", recordedResolverDigest(crWith(tc.obj)),
				"must read as never-recorded: the call site treats \"\" as bootstrap, and anything else "+
					"here would make a malformed field an update loop")
		})
	}

	// POSITIVE CONTROL: a real value must come back, or every row above passes against a function
	// that returns "" unconditionally.
	ok := crWith(map[string]any{"status": map[string]any{statusResolvedResolversDigest: "abc123"}})
	assert.Equal(t, "abc123", recordedResolverDigest(ok))
}

// THE READ-BACK IS THE ONLY THING THAT SEES A PRUNED WRITE.
//
// A status write that the apiserver strips returns nil and an object without the field. This is what
// tells those two cases apart, so it is tested against the shape the apiserver actually returns.
func TestTheReadBackDistinguishesWrittenFromPruned(t *testing.T) {
	const want = "deadbeef"

	written := crWith(map[string]any{"status": map[string]any{statusResolvedResolversDigest: want}})
	assert.True(t, resolverDigestWasStored(written, want), "the field came back with the value we wrote")

	pruned := crWith(map[string]any{"status": map[string]any{"id": "x"}})
	assert.False(t, resolverDigestWasStored(pruned, want),
		"the apiserver returned a status without the field, which is exactly what a pruned write "+
			"looks like, and it must not read as success")

	other := crWith(map[string]any{"status": map[string]any{statusResolvedResolversDigest: "something-else"}})
	assert.False(t, resolverDigestWasStored(other, want), "a different value is not the value we wrote")

	assert.False(t, resolverDigestWasStored(nil, want), "a nil object is not a successful write")
	assert.False(t, resolverDigestWasStored(written, ""),
		"an empty want must never read as stored, or a resource with no sources would report Recorded")
}

func TestSetDigestRoundTrips(t *testing.T) {
	mg := crWith(map[string]any{})
	require.NoError(t, setResolverDigest(mg, "abc"))
	assert.Equal(t, "abc", recordedResolverDigest(mg))
	require.NoError(t, setResolverDigest(mg, "def"))
	assert.Equal(t, "def", recordedResolverDigest(mg), "the digest must be replaced, not appended")
}

// ---------------------------------------------------------------- call-site wiring

// stripGoComments removes // and /* */ comments so a source assertion cannot be satisfied by a
// comment that merely QUOTES the code it is describing.
//
// This is not hypothetical caution. These files carry long rationale comments that name the exact
// calls they are about, and an earlier version of a test like this one passed against a comment
// quoting `SetConditions(...)` while asserting that no such call existed.
func stripGoComments(src string) string {
	src = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(src, "")
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func TestStripGoCommentsActuallyStrips(t *testing.T) {
	got := stripGoComments("code() // calls thing()\n/* block mentions other() */\nreal()")
	assert.NotContains(t, got, "thing()", "a line comment survived, so every assertion below is unsound")
	assert.NotContains(t, got, "other()", "a block comment survived")
	assert.Contains(t, got, "code()")
	assert.Contains(t, got, "real()", "stripping removed real code")
}

// THE THREE WIRING CLAIMS THAT NO LOCAL TEST CAN SEE FROM OUTSIDE.
//
// Observe's digest branches can only be exercised end to end by the integration suite, which
// provisions a kind cluster and therefore does not run locally (clusterguard). These assertions pin
// the three properties whose violation is silent, on the stripped source:
//
//  1. the BOOTSTRAP branch records the digest -- if it stopped, a pruned field would never be
//     detected and the capability condition would never be set;
//  2. the DRIFT branch does NOT record it -- if it started, it would retire the difference before
//     the update acted on it, and a failed update would lose the rotation permanently;
//  3. the read-back runs against the object the APISERVER RETURNED, after the status write.
func TestTheDigestIsRecordedOnBootstrapAndNotOnDrift(t *testing.T) {
	b, err := os.ReadFile("restResources.go")
	require.NoError(t, err)
	src := stripGoComments(string(b))
	require.NotEmpty(t, src, "restResources.go is empty, so every assertion below is a vacuous pass")
	// CONTROL: stripping must leave the file's real code behind.
	require.Contains(t, src, "func (h *handler) Observe(",
		"the stripped source lost its function declarations, so it is not the code any more")

	// (1) and (2): the digest is set in exactly TWO places -- Observe's bootstrap and Update's
	// success branch. A third would almost certainly be a drift-path write.
	setCalls := strings.Count(src, "setResolverDigest(mg,")
	assert.Equal(t, 2, setCalls,
		"setResolverDigest is called %d times, not twice. It belongs in exactly two places: Observe's "+
			"bootstrap (where no drift is reported) and Update's landed-success branch (which knows the "+
			"value reached the API). A call on the drift path retires the difference before the update "+
			"runs, and a failed update then loses the rotation permanently", setCalls)

	// (2) specifically: the drift branch must return !ResourceUpToDate for the secret reason.
	require.Contains(t, src, "a Secret referenced by this resource's update mapping has changed",
		"the drift branch's message is gone, so the digest no longer reports drift at all")

	// (3): the read-back is consulted, and on `mg` -- which Observe reassigns from UpdateStatus's
	// return, i.e. the server's copy.
	assert.Contains(t, src, "resolverDigestWasStored(mg, digestNow)",
		"the read-back is gone or no longer reads the object the apiserver returned. Without it a "+
			"pruned write reads as success and the feature is silently inert -- which is the defect "+
			"issue 598 exists to fix, arriving through its own fix")
	assert.Contains(t, src, "customcondition.ResolverDigestFieldAbsent()",
		"nothing reports a pruned digest field any more")
}
