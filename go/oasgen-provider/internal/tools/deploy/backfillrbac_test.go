package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestBuildBackfillClusterRoleGrantsPatchAndNothingElse is the test that can actually fail.
//
// Asserting "patch is in the verb list" would pass under a rule granting `*`, under one granting
// update as well, and under one granting delete -- i.e. under every version of this that is too wide.
// So the assertion is on the EXACT set, and the oversights are named individually so a failure says
// which one arrived rather than printing two slices and leaving the reader to diff them.
func TestBuildBackfillClusterRoleGrantsPatchAndNothingElse(t *testing.T) {
	role := BuildBackfillClusterRole("github.krateo.io", "orgrulesets", "orgrulesetconfigurations")

	if len(role.Rules) != 1 {
		t.Fatalf("Rules has %d entries, want exactly 1: a second rule is a second grant nobody reviewed", len(role.Rules))
	}
	rule := role.Rules[0]

	if len(rule.Verbs) != 1 || rule.Verbs[0] != "patch" {
		t.Errorf("Verbs = %v, want exactly [patch]", rule.Verbs)
	}
	for _, forbidden := range []string{"*", "update", "create", "delete", "deletecollection", "get", "list", "watch", "escalate", "bind"} {
		for _, got := range rule.Verbs {
			if got == forbidden {
				t.Errorf("Verbs contains %q. The backfill issues a merge patch carrying ONE label and performs no "+
					"other operation; %q authorises something this code never does", forbidden, forbidden)
			}
		}
	}

	if len(rule.APIGroups) != 1 || rule.APIGroups[0] != "github.krateo.io" {
		t.Errorf("APIGroups = %v, want exactly [github.krateo.io]: a wildcard group here would restore the "+
			"cluster-wide grant this whole mechanism exists to avoid", rule.APIGroups)
	}
	if len(rule.Resources) != 2 || rule.Resources[0] != "orgrulesets" || rule.Resources[1] != "orgrulesetconfigurations" {
		t.Errorf("Resources = %v, want [orgrulesets orgrulesetconfigurations]", rule.Resources)
	}
	if len(rule.ResourceNames) != 0 {
		t.Errorf("ResourceNames = %v, want empty: the backfill cannot know instance names in advance", rule.ResourceNames)
	}
}

// TestBuildBackfillClusterRoleOmitsAnAbsentConfiguration covers the RestDefinition that generates no
// Configuration CRD. Naming an empty resource would make the rule grant on "" -- not an error the
// apiserver reports, and not a grant on anything.
func TestBuildBackfillClusterRoleOmitsAnAbsentConfiguration(t *testing.T) {
	role := BuildBackfillClusterRole("ironic.krateo.io", "nodeprovisions", "")
	got := role.Rules[0].Resources
	if len(got) != 1 || got[0] != "nodeprovisions" {
		t.Fatalf("Resources = %v, want exactly [nodeprovisions]", got)
	}
	for _, r := range got {
		if r == "" {
			t.Error(`Resources contains the empty string: a rule granting on "" grants on nothing, silently`)
		}
	}
}

// TestBackfillClusterRoleNameIsVersionFree pins the naming decision, which is not cosmetic: a name
// carrying the derived CRD version would orphan the previous version's ClusterRole on every version
// bump, and a teardown running after the OAS had gone -- where the version falls back -- would then
// delete a ClusterRole that was never the live one.
//
// The control is that two DIFFERENT versions of the same kind must produce the SAME name. Asserting the
// name merely "does not contain v1alpha1" would pass for a name built from a version that happened not
// to be v1alpha1.
func TestBackfillClusterRoleNameIsVersionFree(t *testing.T) {
	a := BackfillClusterRoleName(schema.GroupVersionResource{Group: "g.krateo.io", Version: "v1alpha1", Resource: "widgets"})
	b := BackfillClusterRoleName(schema.GroupVersionResource{Group: "g.krateo.io", Version: "v2", Resource: "widgets"})
	if a != b {
		t.Fatalf("name depends on the version: %q vs %q", a, b)
	}

	// And it must distinguish two kinds that differ ONLY by group, or one RestDefinition's grant would
	// silently replace another's.
	c := BackfillClusterRoleName(schema.GroupVersionResource{Group: "other.krateo.io", Resource: "widgets"})
	if a == c {
		t.Errorf("two groups collide on one name %q: the second Apply would overwrite the first group's rule", a)
	}
	d := BackfillClusterRoleName(schema.GroupVersionResource{Group: "g.krateo.io", Resource: "gadgets"})
	if a == d {
		t.Errorf("two resources collide on one name %q", a)
	}
}

// TestChartAggregationLabelMatchesTheConstant compares the two halves that are each correct alone.
//
// The Go constant decides what label the provider STAMPS; the chart's aggregationRule decides what label
// is SELECTED. Nothing links them, and a rename on either side un-aggregates every grant silently --
// the provider keeps applying ClusterRoles, the apiserver keeps accepting them, the rules reach no
// ServiceAccount, and the only symptom is the 403 storm this mechanism was built to stop.
func TestChartAggregationLabelMatchesTheConstant(t *testing.T) {
	// Five levels: deploy -> tools -> internal -> oasgen-provider -> go -> repo root. Go runs a test with
	// cwd set to its package directory, so this is stable regardless of where `go test` is invoked from.
	path := filepath.Join("..", "..", "..", "..", "..", "helm", "oasgen-provider", "templates", "clusterrole-backfill.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the chart template at %s: %v. If the chart moved, this test must follow it rather "+
			"than be deleted: it is the only thing comparing the stamped label to the selected one", path, err)
	}
	chart := string(raw)

	// Positive control: the file must contain the aggregationRule at all, or a selector-match assertion
	// against an empty or unrelated file would pass by vacuity.
	if !strings.Contains(chart, "aggregationRule:") {
		t.Fatalf("%s contains no aggregationRule: the file was found but is not the aggregated ClusterRole, "+
			"so matching a label in it proves nothing", path)
	}

	want := BackfillAggregationLabel + `: "true"`
	if !strings.Contains(chart, want) {
		t.Errorf("the chart's clusterRoleSelectors do not contain %q.\n"+
			"BackfillAggregationLabel is what the provider stamps on every grant it applies; the chart's "+
			"selector is what pulls those grants into the bound ClusterRole. If they disagree, the grants "+
			"are applied successfully and authorise nothing.", want)
	}

	// Negative control: a label the chart must NOT select on, proving the check above is reading the
	// selector rather than matching any string anywhere in a long comment.
	if strings.Contains(chart, `ogen.krateo.io/aggregate-to-backfill-typo: "true"`) {
		t.Error("the chart selects on a label no code stamps")
	}
}
