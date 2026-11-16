package restResources

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestNoBodySendingCallSiteBypassesTheHelper encodes the invariant issue 718 is actually about.
//
// The bug was not a missing line. It was that build-and-transform were TWO steps, copied into three
// places, so a fourth call path could do the first half and look complete. A test asserting "dispatch
// applies the transform" would pass today and say nothing about the fifth caller.
//
// So the assertion is on the SHAPE: builder.BuildCallConfig is called from exactly one place that sends
// a body -- buildTransformedCallConfig -- and every other occurrence must be at a call that sends none.
// Those are enumerated below BY INTENT rather than by count, so a new one has to be justified here
// instead of bumping a number.
func TestNoBodySendingCallSiteBypassesTheHelper(t *testing.T) {
	// Counted across the whole package, because the point is that no file gets to be special.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	call := regexp.MustCompile(`builder\.BuildCallConfig\(`)
	type site struct {
		file string
		line int
		text string
	}
	var sites []site
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, rerr := os.ReadFile(name)
		if rerr != nil {
			t.Fatalf("reading %s: %v", name, rerr)
		}
		for i, l := range strings.Split(string(raw), "\n") {
			if call.MatchString(l) {
				sites = append(sites, site{file: name, line: i + 1, text: strings.TrimSpace(l)})
			}
		}
	}

	// POSITIVE CONTROL. If the regexp, the directory walk or the file filter is broken, `sites` is empty
	// and every assertion below passes by vacuity -- which is exactly the failure this whole test exists
	// to prevent in the production code.
	if len(sites) == 0 {
		t.Fatal("found no builder.BuildCallConfig call sites at all. The call exists in this package, so " +
			"this means the scan is broken and the assertions below would pass over anything")
	}

	// The ONLY body-sending caller. Create, Update, Delete (both its build and its rebuild) and the
	// dispatch verb all go through it.
	const helper = "transformedcallconfig.go"

	// THE BODY-LESS CALLERS, ENUMERATED. Every one builds for apiaction.Get or apiaction.FindBy and passes
	// nil for `resolved`: a read assembles no body, so there is nothing for a whole-document program to run
	// over. builder.go says as much on the RequestTransform field itself.
	//
	// This list is the audit that found the fix complete. Issue 718 was reported against ONE path; writing
	// this test enumerated every call site against the contract instead of against the symptom, and all
	// nine others turned out to be reads. That is the result -- not an assumption -- and it is why the fix
	// is one call site rather than an unknown number.
	//
	// A NEW FILE MUST JOIN THIS LIST WITH ITS REASON, or the test fails. That is the point: a site added
	// without a reason is exactly how dispatch came to exist.
	bodyless := map[string]string{
		"async_handler.go":              "apiaction.Get -- polls the async operation",
		"async_requeue.go":              "apiaction.Get -- polls the async operation (two sites)",
		"delete_identifier_recovery.go": "apiaction.FindBy -- re-finds an identifier before deleting",
		"exists.go":                     "apiaction.FindBy -- the existence probe",
		"dispatch.go":                   "apiaction.FindBy -- observeDispatch's correlation walk. The DISPATCH call itself goes through the helper; TestDispatchGoesThroughTheHelper pins that separately",
		"mutate_restaction.go":          "apiaction.Get -- reads before mutating a RestAction",
		"restResources.go":              "apiaction.Get and apiaction.FindBy -- the two observe paths",
	}

	for _, s := range sites {
		if s.file == helper {
			continue
		}
		reason, ok := bodyless[s.file]
		if !ok {
			t.Errorf("%s:%d calls builder.BuildCallConfig outside %s:\n\t%s\n"+
				"If this call sends a BODY it must go through buildTransformedCallConfig, or a "+
				"requestTransform declared on its verb is accepted and silently dropped -- issue 718. "+
				"If it sends no body, add this file to the bodyless map above with the reason.",
				s.file, s.line, helper, s.text)
			continue
		}
		_ = reason
	}

	// And the helper must actually be the thing that transforms, or the indirection above proves nothing.
	raw, rerr := os.ReadFile(helper)
	if rerr != nil {
		t.Fatalf("reading %s: %v", helper, rerr)
	}
	body := string(raw)
	for _, want := range []string{"builder.BuildCallConfig(", "fieldmapping.ApplyRequestTransform("} {
		if !strings.Contains(body, want) {
			t.Errorf("%s does not contain %s. The helper every body-sending caller routes through must do "+
				"BOTH halves; without this, routing them through it is worse than the duplication it replaced.",
				helper, want)
		}
	}
}

// TestDispatchGoesThroughTheHelper is the narrow companion to the shape test above: it pins the specific
// regression, so a failure says "dispatch lost its transform again" rather than "the shape changed".
//
// Kept separate on purpose. The shape test would still pass if dispatch stopped calling the helper
// altogether and built its own config some other way; this one would not.
func TestDispatchGoesThroughTheHelper(t *testing.T) {
	raw, err := os.ReadFile("dispatch.go")
	if err != nil {
		t.Fatalf("reading dispatch.go: %v", err)
	}

	// Scoped to createDispatch's own body. The file also holds observeDispatch, whose correlation walk is
	// a findby and legitimately calls builder.BuildCallConfig -- asserting over the whole file would make
	// this test fail for a correct reason and teach the next reader to relax it.
	const marker = "func (h *handler) createDispatch("
	whole := string(raw)
	start := strings.Index(whole, marker)
	if start < 0 {
		t.Fatalf("dispatch.go no longer declares %s. If it was renamed, this test must follow it: it is the "+
			"only thing pinning the specific regression of issue 718", marker)
	}
	rest := whole[start+len(marker):]
	end := strings.Index(rest, "\nfunc ")
	if end < 0 {
		end = len(rest)
	}
	s := rest[:end]

	if !strings.Contains(s, "buildTransformedCallConfig(") {
		t.Error("dispatch.go does not call buildTransformedCallConfig. A requestTransform declared on an " +
			"action: dispatch verb is then parsed, validated, admitted, plumbed to the call and dropped -- " +
			"and the vendor may ACCEPT the untransformed body, filing a record that carries no correlation " +
			"token and can never be observed again (issue 718)")
	}
	if strings.Contains(s, "builder.BuildCallConfig(") {
		t.Error("dispatch.go calls builder.BuildCallConfig directly again, which is how it lost the " +
			"transform in the first place")
	}
}
