package clusterguard

import "testing"

// THE CI ARM IS THE ONE THAT CANNOT BE TESTED BY RUNNING IT.
//
// Exercising it for real would provision the kind cluster this package exists to prevent, so it is
// asserted here on the decision instead. That makes this table the only thing standing between a typo in
// the gate and either (a) CI silently skipping every integration suite -- which would print `ok` for all
// five and look exactly like a pass -- or (b) a laptop provisioning clusters again.
func TestDecide(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want verdict
		why  string
	}{
		{
			name: "GitHub-hosted runner: CI is set",
			env:  map[string]string{"CI": "true"},
			want: allowedCI,
			why:  "CI must still provision kind, or every integration suite silently stops running",
		},
		{
			name: "GITHUB_ACTIONS alone is enough",
			env:  map[string]string{"GITHUB_ACTIONS": "true"},
			want: allowedCI,
			why:  "the authoritative Actions variable, in case CI is ever unset by a runner change",
		},
		{
			name: "a laptop: neither is set",
			env:  map[string]string{},
			want: denied,
			why:  "THE CASE THIS PACKAGE EXISTS FOR -- the shared Docker daemon must not get a new cluster",
		},
		{
			name: "a laptop with the deliberate opt-in",
			env:  map[string]string{AllowEnv: "1"},
			want: allowedOptIn,
			why:  "an escape hatch that is distinguishable from CI, so it can warn and CI need not",
		},
		{
			name: "the opt-in set to empty is NOT an opt-in",
			env:  map[string]string{AllowEnv: ""},
			want: denied,
			why:  "an exported-but-empty variable is how a shell leaks an unset value; it must not open the gate",
		},
		{
			name: "CI wins over the opt-in",
			env:  map[string]string{"CI": "true", AllowEnv: "1"},
			want: allowedCI,
			why:  "CI must not print the opt-in's warning on every run",
		},
	}

	// All three verdicts must appear, asserted rather than assumed: a table that never produces `denied`
	// passes against a gate that always allows, which is the pre-guard behaviour.
	seen := map[verdict]int{}
	for _, tc := range cases {
		seen[tc.want]++
	}
	for v, name := range map[verdict]string{denied: "denied", allowedCI: "allowedCI", allowedOptIn: "allowedOptIn"} {
		if seen[v] == 0 {
			t.Fatalf("no row expects %s, so this table cannot detect a gate that never returns it", name)
		}
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decide(func(k string) string {
				v, ok := tc.env[k]
				if !ok {
					return ""
				}
				return v
			})
			if got != tc.want {
				t.Errorf("decide() = %v, want %v -- %s", got, tc.want, tc.why)
			}
		})
	}
}
