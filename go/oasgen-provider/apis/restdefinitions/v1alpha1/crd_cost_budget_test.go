package v1alpha1

import (
	"fmt"
	"os"
	"path/filepath"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
	"testing"
)

// THE CRD'S CEL COST BUDGET IS SCHEMA-WIDE, AND THAT IS THE WHOLE POINT OF THIS FILE.
//
// The apiserver estimates the cost of every x-kubernetes-validations rule in a CRD and rejects the
// CRD -- not the field -- when the total exceeds its limit. Measured independently by two sessions:
//
//	installer         real GKE apiserver v1.36.4, server dry-run: today's schema admits; the boundary
//	                  sits 25-35 rules of their (more expensive) shape above it
//	core-provider     envtest, setup-envtest 1.36.0: today's schema admits, 6 rules on each
//	                  verbsDescription item admits, and it REJECTS between 81 and 106 rules per item
//
// Both reported the rejection verbatim:
//
//	x-kubernetes-validations[0].rule: Forbidden: contributed to estimated rule cost total
//	exceeding cost limit for ENTIRE OpenAPIv3 schema
//
// TWO CONSEQUENCES THAT MOTIVATE A TEST RATHER THAN A COMMENT.
//
// First, the cost is anti-correlated with diff size. 0.73.0 added EIGHT rules while adding no
// validation of its own: embedding the existing Pagination and JQProgram types under
// correlation.observe re-instantiates their rules at the new path. Reusing a Go type is free;
// re-instantiating it in a CRD schema is not. A pull request that adds two optional fields and spends
// eight rules is exactly the one a reviewer waves through.
//
// Second, the rejection names the wrong rule. Both sessions saw it finger rule INDEX [0] -- a rule
// that was already there and fine -- because the budget is a total. So a cost failure reads as a
// pre-existing CRD problem rather than as the newest addition.
const (
	// wantRuleTotal and wantRuleNodes are a LEDGER, not a limit.
	//
	// Asserting only "the CRD admits" cannot fail in the direction that matters: it stays green if
	// someone REMOVES rules, or if the generator silently stops emitting a validation. installer's
	// point, from two instruments of their own that proved only what they went looking for -- a line
	// that pins the count turns this from pass/fail into a budget ledger, so the next +8 from embedding
	// shows up as a number moving, in the pull request that moved it.
	//
	// UPDATE THESE DELIBERATELY. A diff here is the test working.
	//
	// FIRST CAUGHT, on this test's own first run: 70/33 -> 78/37. This branch was cut from main before
	// #710 merged, and #710 spent EIGHT rules by embedding Pagination and JQProgram under
	// correlation.observe -- adding no validation of its own. So the very +8-from-embedding this test
	// was written about is what it caught, before anyone had to remember to look.
	wantRuleTotal = 78
	wantRuleNodes = 37
)

// The package already has generatedCRDPath, and a comment explaining why these tests read the SHIPPED
// file rather than the markers: "A marker with a typo'd field name still generates a rule -- one that
// quietly evaluates to true and admits everything." The ledger below rests on the same reasoning, so it
// reuses the same constant rather than introducing a second way to find the same file.

// countRules walks the whole document, so it counts rules wherever they are -- including at paths
// nobody remembered, which is the case the ledger exists for.
func countRules(t *testing.T, path string) (total, nodes int) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var doc interface{}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	var walk func(interface{})
	walk = func(n interface{}) {
		switch v := n.(type) {
		case map[string]interface{}:
			if rules, ok := v["x-kubernetes-validations"].([]interface{}); ok && len(rules) > 0 {
				total += len(rules)
				nodes++
			}
			for _, child := range v {
				walk(child)
			}
		case []interface{}:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(doc)
	return total, nodes
}

// TestSchemaRuleLedger is the half that runs everywhere, with no apiserver.
//
// It does not assert the CRD admits -- it cannot know that. It asserts what the CRD COSTS, so a change
// to the cost is visible in review even where the envtest below is skipped.
func TestSchemaRuleLedger(t *testing.T) {
	total, nodes := countRules(t, generatedCRDPath)
	if total != wantRuleTotal || nodes != wantRuleNodes {
		t.Errorf(`the generated CRD carries %d x-kubernetes-validations rules across %d nodes; the ledger says %d across %d.

If you ADDED rules: that is fine as long as there is headroom -- the measured boundary is roughly 80+
rules on a single verbsDescription item, and the budget is SCHEMA-WIDE, so rules anywhere compete.
Update the ledger in the same commit.

If you added NO validation and the number still moved: you probably embedded an existing type at a new
path, which re-instantiates its rules. 0.73.0 spent 8 rules that way without adding one of its own.

If the number went DOWN: a validation stopped being emitted. That is the direction a "the CRD admits"
test cannot catch, which is why this ledger exists.`,
			total, nodes, wantRuleTotal, wantRuleNodes)
	}
}

// TestCRDCostBudgetHasHeadroom asks the only authority that can answer: a real apiserver.
//
// SKIPPED without KUBEBUILDER_ASSETS, and the skip says so loudly, because a silent skip is a test
// that cannot fail. The ledger above is what guards where this cannot run.
func TestCRDCostBudgetHasHeadroom(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("SKIPPED, NOT PASSED: KUBEBUILDER_ASSETS is unset, so no apiserver is available to " +
			"estimate CRD rule cost. Run `setup-envtest use` and export it, or rely on TestSchemaRuleLedger. " +
			"This test is the only one here that can answer whether the CRD actually ADMITS.")
	}
	runCostBudgetEnvtest(t, generatedCRDPath)
}

// overCostedCopy writes a copy of the CRD with n additional distinct, constant-size copies of one real
// rule on every verbsDescription item.
//
// The control design is core-provider's, and it is better than the one I proposed. I suggested
// multiplying a rule; they multiplied it AND made each copy distinct and constant-size
// (`<rule> || <i> == -1`), so identical copies cannot be collapsed or cached and no second term grows
// alongside the one under test. They also declined to drop any maxLength/maxItems bounds, because that
// would change the estimate for reasons unrelated to rule count and make a rejection unattributable.
func overCostedCopy(t *testing.T, path string, n int) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	added := 0
	var walk func(interface{})
	walk = func(node interface{}) {
		switch v := node.(type) {
		case map[string]interface{}:
			if props, ok := v["properties"].(map[string]interface{}); ok {
				if vd, ok := props["verbsDescription"].(map[string]interface{}); ok {
					if item, ok := vd["items"].(map[string]interface{}); ok {
						rules, _ := item["x-kubernetes-validations"].([]interface{})
						for i := 0; i < n; i++ {
							rules = append(rules, map[string]interface{}{
								"rule":    fmt.Sprintf("has(self.action) || %d == -1", i),
								"message": fmt.Sprintf("cost control copy %d", i),
							})
							added++
						}
						item["x-kubernetes-validations"] = rules
					}
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []interface{}:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(doc)
	if added == 0 {
		t.Fatal("the control added no rules: verbsDescription.items was not found, so a later " +
			"'it was rejected' could not be attributed to rule count")
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatalf("re-marshalling the over-costed copy: %v", err)
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "overcosted.yaml")
	if err := os.WriteFile(dst, out, 0o600); err != nil {
		t.Fatalf("writing the over-costed copy: %v", err)
	}
	return dst
}

// runCostBudgetEnvtest installs each CRD on a fresh apiserver and reports what it did.
//
// Two cases, and the second is what makes the first mean anything:
//
//	the generated CRD        must INSTALL   -- the subject
//	the same + N copies      must be REJECTED on cost -- proof the estimator is engaged
//
// Without the second, "it installed" is equally consistent with "there is headroom" and "the estimator
// never ran in this configuration". core-provider measured the boundary between 81 and 106 copies on a
// verbsDescription item, so costControlCopies is set well past it.
const costControlCopies = 150

func runCostBudgetEnvtest(t *testing.T, crdPath string) {
	t.Helper()

	install := func(label, path string) error {
		env := &envtest.Environment{
			CRDInstallOptions: envtest.CRDInstallOptions{Paths: []string{path}, ErrorIfPathMissing: true},
		}
		cfg, err := env.Start()
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		_ = cfg
		return env.Stop()
	}

	if err := install("baseline", crdPath); err != nil {
		t.Fatalf(`the generated CRD was REJECTED by a real apiserver: %v

If the message mentions "estimated rule cost total exceeding cost limit", the schema has run out of
budget. Note that the rejection names a rule INDEX, which will be a rule that was already there and
fine -- the budget is a total, so the error fingers an innocent rule rather than the newest one.`, err)
	}

	over := overCostedCopy(t, crdPath, costControlCopies)
	if err := install("over-costed control", over); err == nil {
		t.Fatalf(`CONTROL FAILED: a CRD with %d extra rules on every verbsDescription item INSTALLED.

The estimator is therefore not rejecting on cost in this configuration, which means the baseline
passing above proves nothing about headroom. Either the limit has changed, or the copies are not being
counted -- check that overCostedCopy actually found verbsDescription.items.`, costControlCopies)
	}
}
