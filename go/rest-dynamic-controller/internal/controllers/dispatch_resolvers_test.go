//go:build unit

package restResources

import (
	"os"
	"strings"
	"testing"
)

// Source-level, like TestDispatchGoesThroughTheHelper and no_create_verb_test.go beside it, and for the
// same reason those are: CreateWithResult needs a live dynamic client, a clientSet and an audit sink, so
// driving it to assert an ORDERING would cost a harness larger than the thing under test. The invariants
// here are textual properties of two functions, and the regressions they guard are edits, not inputs.
//
// What they pin is the second half of issue 718. The first half -- a requestTransform plumbed and dropped
// -- is pinned by TestDispatchGoesThroughTheHelper. This one covers the resolver half: a request-direction
// fieldMapping, secretRef among them, accepted at admission and never applied.

func bodyOf(t *testing.T, file, marker string) string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	whole := string(raw)
	start := strings.Index(whole, marker)
	if start < 0 {
		t.Fatalf("%s no longer declares %s. If it was renamed, this test must follow it: it is the only "+
			"thing pinning issue 718's resolver half", file, marker)
	}
	rest := whole[start+len(marker):]
	if end := strings.Index(rest, "\nfunc "); end >= 0 {
		return rest[:end]
	}
	return rest
}

// The resolvers must RUN. Before #718 this function passed a literal nil for the resolved map, so a
// declared secretRef was parsed, validated, admitted -- and the vendor got a body without it.
func TestDispatchRunsTheRequestFieldResolvers(t *testing.T) {
	s := bodyOf(t, "dispatch.go", "func (h *handler) createDispatch(")

	if !strings.Contains(s, "h.resolveRequestFields(") {
		t.Error("createDispatch does not call resolveRequestFields. A request-direction fieldMapping on an " +
			"action: dispatch verb -- secretRef among them -- is then accepted at admission and silently " +
			"never applied, so the dispatch files a vendor record missing the value its declaration named " +
			"(issue 718)")
	}

	// The specific regression, not merely the absence of the call: the resolved argument was a literal nil.
	// A `resolveRequestFields` call whose result is then thrown away would pass the check above.
	call := "buildTransformedCallConfig(ctx, callInfo, mg, clientInfo.ConfigurationSpec, "
	i := strings.Index(s, call)
	if i < 0 {
		t.Fatalf("createDispatch no longer calls buildTransformedCallConfig with the expected argument " +
			"list; TestDispatchGoesThroughTheHelper covers the call itself, so this test needs updating " +
			"to find the resolved argument")
	}
	arg := s[i+len(call):]
	if end := strings.Index(arg, ")"); end >= 0 {
		arg = arg[:end]
	}
	if strings.TrimSpace(arg) == "nil" {
		t.Error("createDispatch passes a literal nil as the resolved map again. That is the exact shape of " +
			"issue 718's resolver half: the resolvers are wired but their output never reaches the request")
	}
}

// The ORDER is the invariant. A resolver whose referent does not exist yet means nothing was sent -- the
// resolvers run strictly before the request is built -- so auditing it writes a row asserting an external
// write that never happened, into the audit trail that block exists to keep complete.
func TestTheDispatchWaitIsTestedBeforeTheAudit(t *testing.T) {
	s := bodyOf(t, "restResources.go", "func (h *handler) CreateWithResult(")

	d := strings.Index(s, "h.createDispatch(")
	if d < 0 {
		t.Fatal("CreateWithResult no longer calls createDispatch; this test must follow it")
	}
	block := s[d:]

	wait := strings.Index(block, "isLookupReferentMissing(")
	audit := strings.Index(block, "h.emitAudit(")

	if wait < 0 {
		t.Fatal("the dispatch block does not test isLookupReferentMissing. A dispatch whose dependent " +
			"lookup has no referent yet is a WAIT, not a failure: without this test it is audited as an " +
			"external write, given a Warning event and returned as an error (issue 718)")
	}
	if audit < 0 {
		t.Fatal("the dispatch block no longer audits at all. A dispatch IS this resource's external " +
			"write; dropping the audit makes the trail silently incomplete for exactly the resources " +
			"that have no create verb")
	}
	if wait > audit {
		t.Errorf("the dispatch block audits (offset %d) BEFORE it tests isLookupReferentMissing (offset "+
			"%d). Nothing has been sent at that point, so the audit records an external write that never "+
			"happened -- and it is unconditional, so it fires on every wait (issue 718)", audit, wait)
	}

	// A wait must not also emit a Warning: it would page someone for an ordering race that resolves
	// itself. The Warning lives after the discriminator, so its offset must too.
	if warn := strings.Index(block, "event.Warning(reasonCreated, \"Dispatch\""); warn >= 0 && warn < wait {
		t.Errorf("the dispatch block emits its Warning event (offset %d) before the wait is discriminated "+
			"(offset %d), so a referent that does not exist yet pages someone", warn, wait)
	}

	// And the clientSet has to reach createDispatch, or the resolvers above have nothing to resolve with.
	if !strings.Contains(block[:min(len(block), 200)], "h.createDispatch(ctx, mg, cs,") {
		t.Error("createDispatch is no longer passed the clientSet. resolveRequestFields needs it, so " +
			"without it the resolver half of issue 718 cannot be wired at all")
	}
}
