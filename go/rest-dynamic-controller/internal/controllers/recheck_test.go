//go:build unit || integration

package restResources

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func defWith(actions ...string) *getter.Resource {
	r := &getter.Resource{}
	for _, a := range actions {
		r.VerbsDescription = append(r.VerbsDescription, getter.VerbsDescription{Action: a})
	}
	return r
}

// THE RULE, APPLIED. Each row is a path and the decision the rule gives it: 15s where the outcome will
// change without anyone acting, zero where it waits on a person, on an act, or on nothing. A new
// recheckPath without a row here fails TestEveryPathIsDecided; a new return site fails the census below.
func TestTheRuleHintOnlyWhereTheOutcomeChangesWithoutAnyoneActing(t *testing.T) {
	rows := []struct {
		name string
		path recheckPath
		def  *getter.Resource
		want metav1.Duration
	}{
		// HINT. Settles with nobody acting.
		{"adopt: no create verb, findby declared (also the PUT-only adopt)", pathNoCreateVerbAdopt, defWith("findby", "update"), metav1.Duration{Duration: RecheckWhenWaiting}},
		{"adopt: no create verb, findby declared, get too", pathNoCreateVerbAdopt, defWith("get", "findby"), metav1.Duration{Duration: RecheckWhenWaiting}},
		{"referent missing, dispatch create (onMissing: requeue is the only way here)", pathReferentMissingDispatch, defWith("dispatch"), metav1.Duration{Duration: RecheckWhenWaiting}},
		{"referent missing, create fields (onMissing: requeue is the only way here)", pathReferentMissingFields, defWith("create"), metav1.Duration{Duration: RecheckWhenWaiting}},
		{"RESTAction observe reports absent, create declared", pathRESTActionAbsent, defWith("create"), metav1.Duration{Duration: RecheckWhenWaiting}},
		{"RESTAction observe reports absent, findby declared", pathRESTActionAbsent, defWith("findby"), metav1.Duration{Duration: RecheckWhenWaiting}},
		{"get not found while create Pending", pathAsyncCreatePendingGet, defWith("create", "get"), metav1.Duration{Duration: RecheckWhenWaiting}},
		{"findby not found while create Pending", pathAsyncCreatePendingFindBy, defWith("create", "findby"), metav1.Duration{Duration: RecheckWhenWaiting}},
		{"async requeue poll still pending", pathAsyncPollPending, defWith("create", "get"), metav1.Duration{Duration: RecheckWhenWaiting}},
		{"create requested this pass (issue 648: handleCreate enqueues no observe)", pathCreateRequested, defWith("create"), metav1.Duration{Duration: RecheckWhenWaiting}},

		// NO HINT: the definition can never settle. ONE predicate, both sides.
		{"no create verb AND no findby, create side", pathNoCreateVerbAdopt, defWith("get", "update"), metav1.Duration{}},
		{"no create verb AND no findby, observe side", pathRESTActionAbsent, defWith("update"), metav1.Duration{}},
		{"no verbs at all", pathNoCreateVerbAdopt, defWith(), metav1.Duration{}},
		{"nil definition", pathNoCreateVerbAdopt, nil, metav1.Duration{}},
		{"the no-create-no-findby path itself", pathNoCreateNoFindBy, defWith("get"), metav1.Duration{}},

		// NO HINT: waits on a person.
		{"authorization denied", pathAuthorizationDenied, defWith("create", "findby"), metav1.Duration{}},
		{"create refused persistently", pathCreateRefusedPersistently, defWith("create"), metav1.Duration{}},
		{"drift not converging", pathDriftNotConverging, defWith("create", "get", "update"), metav1.Duration{}},
		{"correlation token missing", pathCorrelationTokenMissing, defWith("dispatch"), metav1.Duration{}},

		// NO HINT: the pass acted, or nothing waits.
		{"absence or drift reported, the runtime acts", pathActing, defWith("create", "get"), metav1.Duration{}},
		{"exists and up to date", pathSettled, defWith("create", "get"), metav1.Duration{}},
		{"being deleted", pathBeingDeleted, defWith("create"), metav1.Duration{}},

		// NO HINT: the rule WOULD fit. Held back for issue 947 and said so in the row.
		{"dispatch token not yet visible (issue 947: the deadline is count-based)", pathDispatchNotYetVisible, defWith("dispatch"), metav1.Duration{}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			assert.Equal(t, r.want.Duration, recheckAfter(r.path, r.def))
		})
	}
	assert.Equal(t, "15s", RecheckWhenWaiting.String())
}

// A path added to the enum without a decision is the defect this exists to prevent.
func TestEveryPathIsDecided(t *testing.T) {
	require.Equal(t, int(pathCount), len(recheckPolicy),
		"every recheckPath needs exactly one row in recheckPolicy: decide hint or no hint for it")
	for p := recheckPath(0); p < pathCount; p++ {
		rule, ok := recheckPolicy[p]
		require.True(t, ok, "path %d has no row in recheckPolicy", p)
		assert.NotEmpty(t, rule.name)
	}
}

// ONE predicate: a kind that declares nothing able to settle it is the same answer from both sides.
func TestDefinitionCanSettle(t *testing.T) {
	assert.True(t, definitionCanSettle(defWith("create")))
	assert.True(t, definitionCanSettle(defWith("findby")))
	assert.True(t, definitionCanSettle(defWith("DISPATCH")))
	assert.True(t, definitionCanSettle(&getter.Resource{CreateApiRef: &getter.ApiRef{}}))
	assert.False(t, definitionCanSettle(defWith("get", "update", "delete")))
	assert.False(t, definitionCanSettle(nil))
}

func sourceFiles(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	out := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		out[f] = string(b)
	}
	require.NotEmpty(t, out)
	return out
}

// WIRING. The table above proves the rule; this proves production asks it. Exactly one call per hint
// path, none for a no-hint path, and no RecheckAfter set by hand anywhere else.
func TestProductionAsksTheRuleAtEveryHintSite(t *testing.T) {
	src := sourceFiles(t)
	want := map[recheckPath]int{
		pathNoCreateVerbAdopt: 1, pathReferentMissingDispatch: 1, pathReferentMissingFields: 1,
		pathRESTActionAbsent: 1, pathAsyncCreatePendingGet: 1, pathAsyncCreatePendingFindBy: 1,
		pathAsyncPollPending: 1, pathCreateRequested: 5,
	}
	names := map[recheckPath]string{
		pathNoCreateVerbAdopt: "pathNoCreateVerbAdopt", pathReferentMissingDispatch: "pathReferentMissingDispatch",
		pathReferentMissingFields: "pathReferentMissingFields", pathRESTActionAbsent: "pathRESTActionAbsent",
		pathAsyncCreatePendingGet: "pathAsyncCreatePendingGet", pathAsyncCreatePendingFindBy: "pathAsyncCreatePendingFindBy",
		pathAsyncPollPending: "pathAsyncPollPending", pathDispatchNotYetVisible: "pathDispatchNotYetVisible",
		pathActing: "pathActing", pathSettled: "pathSettled", pathNoCreateNoFindBy: "pathNoCreateNoFindBy",
		pathAuthorizationDenied: "pathAuthorizationDenied", pathCreateRefusedPersistently: "pathCreateRefusedPersistently",
		pathDriftNotConverging: "pathDriftNotConverging", pathCorrelationTokenMissing: "pathCorrelationTokenMissing",
		pathBeingDeleted: "pathBeingDeleted", pathCreateRequested: "pathCreateRequested",
	}
	require.Equal(t, int(pathCount), len(names), "a new path needs a name here too")
	for p, n := range names {
		calls := 0
		for f, s := range src {
			if f == "recheck.go" {
				continue
			}
			calls += strings.Count(s, "recheckAfter("+n+",")
		}
		assert.Equal(t, want[p], calls, "%s: hint call sites in production code", n)
	}
	handSet := 0
	for f, s := range src {
		if f == "recheck.go" {
			continue
		}
		handSet += len(regexp.MustCompile(`RecheckAfter:`).FindAllString(s, -1))
	}
	assert.Equal(t, 12, handSet, "RecheckAfter is set only by recheckAfter(...) calls, one per hint site")
}

// THE CENSUS. Every ExternalObservation and CreateResult literal in production code is classified, and
// the classes must sum to what the source contains. Adding a return site changes the source count and
// fails here until someone assigns it a class: the decision cannot be inherited.
func TestEveryReturnSiteIsClassified(t *testing.T) {
	src := sourceFiles(t)
	count := func(re string) (total int) {
		r := regexp.MustCompile(re)
		for _, s := range src {
			total += len(r.FindAllString(s, -1))
		}
		return
	}
	obs := count(`controller\.ExternalObservation\{`)
	cre := count(`controller\.CreateResult\{`)

	observe := map[string]int{
		"hint: get/findby not found while Pending, async poll pending, RESTAction absent": 4,
		"no hint: returns an error (the runtime ignores the hint)":                        80,
		"no hint: handled=false fall-through, the caller carries on":                      5,
		"no hint: absence or drift reported, the runtime acts":                            8,
		"no hint: settled (exists and up to date)":                                        5,
		"no hint: waits on a person (drift not converging, correlation token missing)":    2,
		"no hint: dispatch token not yet visible, issue 947":                              1,
	}
	create := map[string]int{
		"hint: referent missing x2, no create verb with findby (gated), create requested x5": 8,
		"no hint: returns an error":     22,
		"no hint: authorization denied": 1,
		"no hint: being deleted":        1,
	}
	sum := func(m map[string]int) (n int) {
		for _, v := range m {
			n += v
		}
		return
	}
	assert.Equal(t, 105, obs, "ExternalObservation sites in production source")
	assert.Equal(t, sum(observe), obs, "an ExternalObservation return site is unclassified: decide hint or no hint and add it to this table")
	assert.Equal(t, 32, cre, "CreateResult sites in production source")
	assert.Equal(t, sum(create), cre, "a CreateResult return site is unclassified: decide hint or no hint and add it to this table")
	hint := 4 + 8
	assert.Equal(t, 137, obs+cre)
	assert.Equal(t, 125, obs+cre-hint, "hint + no-hint must equal the total")
}

// BEHAVIOUR, NOT SOURCE TEXT: the value arrives in the struct the runtime reads. The narrowest reachable
// units that return a CreateResult are called for real, and the assertion is on the returned
// RecheckAfter. A hint path must carry the constant; the no-hint control must carry zero, so a pass
// distinguishes "the plumbing works" from "every struct happens to carry 15s".
//
// No kind cluster is needed: handlerForCreateGuard builds the handler without one. The full
// CreateWithResult / Observe paths are reached only by the integration suite, which CI runs.
func TestTheHintArrivesInTheReturnedStruct(t *testing.T) {
	newMG := func() *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "example.krateo.io/v1alpha1",
			"kind":       "Thing",
			"metadata":   map[string]interface{}{"name": "t", "namespace": "n"},
		}}
	}
	log := logging.NewNopLogger()

	t.Run("hint path: a create that reached the vendor", func(t *testing.T) {
		h, _ := handlerForCreateGuard()
		got, err := h.createReachedTheVendor(newMG(), errors.New("status write conflicted"), log)
		require.NoError(t, err)
		assert.True(t, got.Requested)
		assert.Equal(t, RecheckWhenWaiting, got.RecheckAfter)
	})

	t.Run("hint path: a create that landed at a redirect", func(t *testing.T) {
		h, _ := handlerForCreateGuard()
		got, err := h.createLandedAtARedirect(newMG(), errors.New("redirect"), log)
		require.NoError(t, err)
		assert.Equal(t, RecheckWhenWaiting, got.RecheckAfter)
	})

	t.Run("control, no-hint path: authorization denied waits on a person", func(t *testing.T) {
		h, _ := handlerForCreateGuard()
		got := h.createAuthorizationDeniedResult(newMG(), "create", errors.New("403"), log)
		assert.False(t, got.Requested)
		assert.Equal(t, time.Duration(0), got.RecheckAfter)
	})
}
