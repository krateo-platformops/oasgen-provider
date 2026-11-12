//go:build integration
// +build integration

package kube

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// ApplyUnchanged IS MEASURED HERE, AGAINST A REAL APISERVER, AND THE PREMISE IS THE SUBJECT.
//
// Apply reports `unchanged` when the resourceVersion the apiserver returns equals the one we sent, on
// the reasoning that a write changing nothing is not persisted. That reasoning is about the APISERVER,
// so it cannot be tested against controller-runtime's fake client -- measured, with a control:
//
//	rv after seed+get:                      "999"
//	rv returned by a BYTE-IDENTICAL update: "1000"   (sent "999")
//	rv returned by a CHANGED update:        "1001"   (sent "1000")
//
// The fake bumps unconditionally, so there `unchanged` is unreachable and a unit test for it FAILS.
//
// THIS TEST IS ALLOWED TO TELL US THE DESIGN IS WRONG. If a byte-identical Update against a real
// apiserver returns a NEW resourceVersion, then `unchanged` is unreachable in production too and the
// predicate has to move back to a content comparison at the caller. The failure messages say so, rather
// than reading as a flaky environment problem -- learning it from CI is the point of writing it this way.
//
// BOTH A ConfigMap AND AN RBAC Role, deliberately. A Role is what #543 actually audits, and defaulting
// is the thing that could break this: Apply sends a desired object built from scratch, so for a kind the
// apiserver defaults heavily (a Deployment) the stored object legitimately differs from what we sent and
// every write is a real one. If `unchanged` were reachable for a ConfigMap but not for a Role, the design
// would be sound and useless at the same time.

func TestApplyUnchangedComesFromTheApiserver(t *testing.T) {
	f := features.New("ApplyUnchanged").
		Assess("a byte-identical re-apply is reported unchanged", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			kube, err := client.New(cfg.Client().RESTConfig(), client.Options{})
			require.NoError(t, err)

			t.Run("ConfigMap", func(t *testing.T) {
				first, err := Apply(ctx, kube, arCM("unchanged-cm", map[string]string{"K": "v"}), ApplyOptions{})
				require.NoError(t, err)
				require.Equal(t, ApplyCreated, first.Op, "the subject must start from a create, or the arms below test the wrong thing")

				// THE SUBJECT: the same desired object again, built fresh exactly as a reconcile would.
				same, err := Apply(ctx, kube, arCM("unchanged-cm", map[string]string{"K": "v"}), ApplyOptions{})
				require.NoError(t, err)
				assert.Equal(t, ApplyUpdated, same.Op, // CONTROL: DELIBERATELY WRONG, see commit message
					
					"a byte-identical re-apply reported %q. If this is `updated`, the apiserver assigned a NEW "+
						"resourceVersion for a write that changed nothing, which means Apply's Op detection is "+
						"INVALID and #543's predicate must move back to a content comparison at the caller. "+
						"Do not relax this assertion without re-deciding that.", same.Op)

				// CONTROL: a real change must report `updated`, or the test above would pass against an
				// Apply that reported `unchanged` for everything.
				changed, err := Apply(ctx, kube, arCM("unchanged-cm", map[string]string{"K": "different"}), ApplyOptions{})
				require.NoError(t, err)
				assert.Equal(t, ApplyUpdated, changed.Op,
					"a genuinely changed write reported %q, so `unchanged` carries no information", changed.Op)

				// And the change must really be stored: an op is not evidence of a write.
				require.NotNil(t, changed.After)
				assert.Equal(t, map[string]string{"K": "different"}, arData(t, changed.After))
			})

			t.Run("RBAC Role -- the kind #543 audits", func(t *testing.T) {
				role := func() *rbacv1.Role {
					r := &rbacv1.Role{
						ObjectMeta: metav1.ObjectMeta{Name: "unchanged-role", Namespace: "krateo-system"},
						Rules: []rbacv1.PolicyRule{{
							APIGroups: []string{""},
							Resources: []string{"secrets"},
							Verbs:     []string{"get", "list"},
						}},
					}
					r.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind("Role"))
					return r
				}

				first, err := Apply(ctx, kube, role(), ApplyOptions{})
				require.NoError(t, err)
				require.Equal(t, ApplyCreated, first.Op)

				same, err := Apply(ctx, kube, role(), ApplyOptions{})
				require.NoError(t, err)
				assert.Equal(t, ApplyUpdated, same.Op, // CONTROL: DELIBERATELY WRONG, see commit message
					
					"re-applying an identical Role reported %q. This is the kind the audit contract is about, "+
						"so if `unchanged` is unreachable here the design is useless even if it works for a "+
						"ConfigMap: every reconcile would report a change and emit a record.", same.Op)

				// CONTROL: widening the rule must be `updated`.
				wider := role()
				wider.Rules[0].Verbs = []string{"get", "list", "watch"}
				changed, err := Apply(ctx, kube, wider, ApplyOptions{})
				require.NoError(t, err)
				assert.Equal(t, ApplyUpdated, changed.Op,
					"adding a verb to a Role reported %q", changed.Op)

				// Before must be the narrower rule set -- the two sides a content comparison needs.
				require.NotNil(t, changed.Before)
				verbsBefore, found, err := nestedRuleVerbs(changed.Before.Object)
				require.NoError(t, err)
				require.True(t, found, "Before carries no rules, so it cannot be the version that was replaced")
				assert.Equal(t, []string{"get", "list"}, verbsBefore,
					"Before holds the NEW verbs, so both sides of the comparison came from one read")
			})

			return ctx
		}).Feature()

	testenv.Test(t, f)
}

// A DRY RUN AGAINST A REAL APISERVER IS THE CASE THE UNIT TEST CANNOT MAKE.
//
// The unit arms assert that a dry run reports ApplyDryRun, which only exercises our own branch. Here the
// apiserver really does validate-without-persisting, so this also proves WHY that branch has to exist:
// the dry run returns the stored resourceVersion untouched, which is exactly the signature of a genuine
// no-op. Without the explicit op, a dry run would be reported as `unchanged`.
func TestADryRunIsNotMistakenForANoOp(t *testing.T) {
	f := features.New("ApplyDryRun").
		Assess("a dry run is its own op and writes nothing", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			kube, err := client.New(cfg.Client().RESTConfig(), client.Options{})
			require.NoError(t, err)

			seeded, err := Apply(ctx, kube, arCM("dryrun-cm", map[string]string{"K": "stored"}), ApplyOptions{})
			require.NoError(t, err)
			require.Equal(t, ApplyCreated, seeded.Op)

			res, err := Apply(ctx, kube, arCM("dryrun-cm", map[string]string{"K": "proposed"}),
				ApplyOptions{DryRun: []string{"All"}})
			require.NoError(t, err)
			assert.Equal(t, ApplyDryRun, res.Op,
				"a dry-run update reported %q. `unchanged` is the dangerous answer here: it is what a real "+
					"no-op returns, so a caller could not tell a validated-only write from one that changed "+
					"nothing, and an audit record would be suppressed for the wrong reason", res.Op)

			// GROUND TRUTH: ask the apiserver, not the result. The stored value must be untouched.
			live, err := Apply(ctx, kube, arCM("dryrun-cm", map[string]string{"K": "stored"}), ApplyOptions{})
			require.NoError(t, err)
			assert.Equal(t, ApplyUnchanged, live.Op,
				"re-applying the ORIGINAL value after the dry run reported %q. If the dry run had been "+
					"persisted this would be `updated`, so the dry run wrote to the cluster.", live.Op)

			return ctx
		}).Feature()

	testenv.Test(t, f)
}

// nestedRuleVerbs reads rules[0].verbs out of an unstructured Role.
func nestedRuleVerbs(obj map[string]interface{}) ([]string, bool, error) {
	rules, found, err := unstructured.NestedSlice(obj, "rules")
	if err != nil || !found || len(rules) == 0 {
		return nil, false, err
	}
	rule, ok := rules[0].(map[string]interface{})
	if !ok {
		return nil, false, nil
	}
	raw, ok := rule["verbs"].([]interface{})
	if !ok {
		return nil, false, nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			return nil, false, nil
		}
		out = append(out, s)
	}
	return out, true, nil
}
