package v1alpha1

import (
	"os"
	"testing"

	"sigs.k8s.io/yaml"
)

// A LEDGER, NOT A LIMIT -- and deliberately NOT a second cost test.
//
// TestGeneratedCRDIsWithinCELCostBudget (crd_cost_budget_test.go, from #239) already asks the
// authoritative question: it runs the shipped CRD through the apiserver's own validator and refuses an
// over-costed schema, statically, in milliseconds. Measured: it passes on today's schema and FAILS when
// 150 rules are injected. Nothing here duplicates that, and an envtest version of it was written and
// then dropped for exactly that reason.
//
// What that test cannot do is notice that the cost MOVED while still fitting. Two cases:
//
//	a rule was REMOVED, or the generator silently stopped emitting one -- a passing "does it fit"
//	stays green, which is the direction where a green test tells you nothing;
//
//	rules were ADDED INVISIBLY. #710 spent EIGHT rules while adding no validation of its own, purely by
//	embedding Pagination and JQProgram under correlation.observe, which re-instantiates their rules at
//	the new path. Reusing a Go type is free; re-instantiating it in a CRD schema is not. "Does it fit"
//	said yes, correctly, and the change was invisible.
//
// The second matters because the budget is SCHEMA-WIDE and the apiserver's rejection names a rule
// INDEX -- measured by two sessions on real apiservers, both seeing it finger a rule that was already
// there and fine. So a cost failure reads as a pre-existing CRD problem rather than as the newest
// addition, and the PR that spent the headroom is not the one that goes red.
const (
	// UPDATE THESE DELIBERATELY. A diff here is the test working.
	//
	// FIRST CAUGHT, on its own first run: 70/33 -> 77/36, the +8 from #710's embedding, before anyone
	// had to remember to look.
	// 78/37 -> 77/36: one rule removed, on ONE node, by unfreezing configurationFields. A removal has to
	// move this ledger too, or the next addition is measured against a stale baseline and the +1 that
	// restores 78 reads as no change at all.
	wantRuleTotal = 77
	wantRuleNodes = 36
)

// countRules walks the whole document, so it counts rules wherever they are -- including at paths
// nobody remembered, which is the case this exists for.
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

func TestSchemaRuleLedger(t *testing.T) {
	total, nodes := countRules(t, generatedCRDPath)
	if total != wantRuleTotal || nodes != wantRuleNodes {
		t.Errorf(`the generated CRD carries %d x-kubernetes-validations rules across %d nodes; the ledger says %d across %d.

If you ADDED rules: fine, as long as TestGeneratedCRDIsWithinCELCostBudget still passes -- that is the
test that decides whether the CRD installs. Update the ledger in the same commit.

If you added NO validation and the number still moved: you probably embedded an existing type at a new
path, which re-instantiates its rules. #710 spent 8 rules that way.

If the number went DOWN: a validation stopped being emitted. That is the direction a "does it fit" test
cannot catch, which is why this ledger exists.`,
			total, nodes, wantRuleTotal, wantRuleNodes)
	}
}
