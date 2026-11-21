//go:build unit || integration

package restResources

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// WHY THIS FILE EXISTS, and it is not to test our own code's behaviour.
//
// This controller writes its own status: SetConditions(mg, ...) followed by tools.UpdateStatus, at 24
// non-test sites. That is CORRECT and those conditions arrive -- but it has a known hazard, raised by
// core-provider as the runtime's #258: our write bumps the object's resourceVersion, so the runtime's
// own in-memory copy from before our call is stale, and the runtime's next write in the same pass can
// lose a 409.
//
// MEASURED: IT DOES NOT BITE US. But it holds by a CONJUNCTION OF THREE FACTS, two of them ours and
// one of them the runtime's, and no single one of them is obviously load-bearing on its own:
//
//	(1) Every UpdateStatus site that DISCARDS the returned object returns within a few lines, so we
//	    never issue a second status write from a stale object ourselves. 14 of 24 sites discard.
//	(2) Delete never persists at all -- 0 UpdateStatus calls in it.
//	(3) The runtime re-fetches before its own status write on every path EXCEPT the failed-delete
//	    branch (worker.go:1875-1883 writes the object it already held, with no re-fetch). That one
//	    exception lands on exactly the path covered by (2).
//
// Remove any one leg and a silent 409 appears. (1) and (2) are ours, so they are asserted here.
//
// (3) IS THE RUNTIME'S, AND IT IS NOW PINNED THERE TOO: unstructured-runtime#125,
// TestHandleDelete_FailureCarriesTheVendorErrorOnReady, asserts that a failed delete persists
// Ready=False/Deleting CARRYING THE VENDOR ERROR, with Synced=ReconcileError and the finalizer kept;
// a mutant writing a plain Deleting with no message is killed. VERIFIED AGAINST THE SERVER AS OPEN,
// NOT MERGED, at the time of writing -- so until it lands, this comment is still the only handoff,
// and a bump reviewer should check worker.go:1875-1883 by hand.
//
// NOTE WHICH HALF IS PINNED, because it is the half that matters and not the obvious one. #125 pins
// that the branch WRITES its own Deleting-with-message. It deliberately does NOT pin whether that
// branch re-fetches first. A re-fetch there would discard our five plain Deleting writes and change
// nothing, because the runtime's write replaces them anyway with one carrying more information. But
// if the branch ever stopped writing its own Deleting, we would silently lose the delete-failure
// message entirely, since we never persist ours.
//
// THIS IS A SOURCE-SHAPE TEST, which is a weak instrument, and it is chosen deliberately: the
// behaviour it protects is a NON-event (a conflict that never happens), and a test that asserts a
// non-event against a fake proves nothing -- our own fakes disagree about whether a no-op Update
// even bumps resourceVersion. Asserting the shape that makes the non-event true is the honest
// instrument available here.

func readControllerSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	require.NoError(t, err, "cannot read %s; if this file moved, this test must move with it", name)
	src := string(b)
	// PARSE CONTROL: without it, a wrong path yields 0 matches and every assertion below passes
	// vacuously -- which is the exact failure this package has hit before.
	require.Contains(t, src, "func NewHandler(",
		"%s does not look like the handler source; the assertions below would pass vacuously", name)
	return src
}

// TestEveryDiscardedUpdateStatusIsTerminal pins leg (1).
//
// THE FIRST VERSION OF THIS ARM WAS VACUOUS AND ITS CONTROL CAUGHT IT. It scanned forward from the
// `tools.UpdateStatus(` line for whichever came first, a `return` or another status write -- and
// found the `return` INSIDE THE IF-STATEMENT'S OWN BODY, which in this codebase's style is always
// there. So it classified every site as terminal no matter what followed, and a mutant with a
// genuine second write passed. The scan must start AFTER the statement, which means finding its
// extent by matching braces, not by looking at the next few lines.
func TestEveryDiscardedUpdateStatusIsTerminal(t *testing.T) {
	src := readControllerSource(t, "restResources.go")
	lines := strings.Split(src, "\n")

	ret := regexp.MustCompile(`^\s*return `)
	checked, verified := 0, 0

	for i, l := range lines {
		if !strings.Contains(l, "tools.UpdateStatus(") || strings.HasPrefix(strings.TrimSpace(l), "//") {
			continue
		}
		// Only sites that THROW AWAY the updated object are at risk; one that reassigns mg carries
		// the fresh resourceVersion forward and is safe by construction.
		if !strings.Contains(l, "_, err") && !strings.Contains(l, "_, uerr") {
			continue
		}
		checked++

		// Walk braces from this line to find where the enclosing statement ENDS, so the scan below
		// starts past its body rather than inside it.
		depth, end := 0, -1
		for k := i; k < len(lines) && k < i+40; k++ {
			depth += strings.Count(lines[k], "{") - strings.Count(lines[k], "}")
			if k > i && depth <= 0 {
				end = k
				break
			}
		}
		require.Greater(t, end, i,
			"could not find the end of the statement at line %d by matching braces; the scan below "+
				"would start inside the statement body and assert nothing", i+1)

		verdict := "neither"
		for k := end + 1; k < len(lines) && k < end+20; k++ {
			n := lines[k]
			if strings.HasPrefix(strings.TrimSpace(n), "//") || strings.TrimSpace(n) == "" {
				continue
			}
			if strings.Contains(n, "tools.UpdateStatus(") {
				verdict = "second-write"
				break
			}
			if ret.MatchString(n) {
				verdict = "return"
				break
			}
			// Anything else that is not a closing brace means ordinary code follows; keep looking,
			// because a status write further down is still the hazard.
		}
		require.NotEqual(t, "second-write", verdict,
			"the tools.UpdateStatus at line %d discards the updated object, and another status write "+
				"follows it in the same path. Ours bumped resourceVersion, so the second write loses "+
				"a 409 -- reassign mg from UpdateStatus, or return before writing again", i+1)
		verified++
	}

	// SETUP CONTROL: if the matcher stopped matching, `checked` would be 0 and the loop would assert
	// nothing while reporting success.
	require.GreaterOrEqual(t, checked, 10,
		"only %d discarding UpdateStatus sites were found; this was measured at 11, so the matcher "+
			"has probably stopped matching and is asserting nothing", checked)
	require.Equal(t, checked, verified, "some sites were counted but never judged")
}

// TestDeleteNeverPersistsItsOwnStatus pins leg (2), which is the one that makes the runtime's
// no-re-fetch delete branch harmless.
func TestDeleteNeverPersistsItsOwnStatus(t *testing.T) {
	src := readControllerSource(t, "restResources.go")

	start := strings.Index(src, "func (h *handler) Delete(")
	require.GreaterOrEqual(t, start, 0, "Delete is gone or renamed; this invariant must be re-measured")
	// Delete is the LAST method in this file, so there is no following `func (h *handler) ` to stop
	// at -- an earlier version of this measurement crashed on exactly that and it is why the
	// fallback to EOF is explicit here rather than implied.
	rest := src[start+10:]
	end := len(src)
	if k := strings.Index(rest, "\nfunc "); k >= 0 {
		end = start + 10 + k
	}
	body := src[start:end]

	require.NotContains(t, body, "tools.UpdateStatus(",
		"Delete now persists its own status. The runtime's FAILED-delete branch does NOT re-fetch "+
			"(worker.go:1875-1883 writes the object it already held), so our write bumps "+
			"resourceVersion and the runtime's write loses a 409 -- which it then requeues quietly, "+
			"turning a reported failure into a silent retry loop. If this is intentional, the "+
			"runtime needs a DeleteWithResult first")

	// SETUP CONTROL: prove the slice really holds Delete's body, or NotContains above passes on an
	// empty string.
	require.Contains(t, body, "SetConditions(",
		"the extracted Delete body has no SetConditions in it, so the slice is wrong and the "+
			"assertion above proved nothing")
	require.Greater(t, len(body), 2000, "the extracted Delete body is implausibly short (%d bytes)", len(body))
}
