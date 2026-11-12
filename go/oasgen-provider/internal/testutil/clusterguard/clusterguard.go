// Package clusterguard stops a test suite from provisioning a kind cluster anywhere but CI.
//
// WHY THIS EXISTS. Five suites in this monorepo create a kind cluster in TestMain -- four in
// oasgen-provider (deploy, crd, kube, restdefinition) and one in rest-dynamic-controller. Each creates it
// once per `go test` invocation, so a single `go test -tags=unit,integration ./...` provisions and destroys
// several clusters. On a developer machine that daemon is shared with other work: clusters belonging to
// other sessions run on it, and a full build has filled its disk before now, which surfaced as broken
// custom resources that looked like a code regression.
//
// The rule is that only CI provisions kind. Local cluster work uses a disposable GKE cluster or GCE
// instance instead. That rule used to live only in people's heads, and it was broken by typing the CI
// invocation verbatim on a laptop -- which is exactly what a rule with no enforcement invites.
//
// WHAT THIS DOES NOT DO. It does not let the suites run against a cluster you supply. That is the other
// half of the job and is tracked separately: every Setup/Finish pair has to distinguish "I created this
// cluster" from "I was lent one", and DestroyCluster has to become unreachable for the latter. Until that
// exists, no KRATEO_TEST_KUBECONFIG is honoured here, because a knob nothing reads is worse than no knob.
package clusterguard

import (
	"fmt"
	"os"
)

// AllowEnv names the deliberate local opt-in. Set it only when you know the Docker daemon is yours.
const AllowEnv = "KRATEO_ALLOW_LOCAL_KIND"

// RequireProvisioningAllowed ends the test binary, loudly and NON-ZERO, unless this process may create a
// kind cluster. Call it as the first statement in a TestMain that provisions one.
//
// Non-zero rather than a silent skip, and that was measured rather than assumed: with a zero exit the
// explanation never appeared, because `go test` prints nothing for a package it considers passing, and the
// run reported ok for a suite that had executed no tests. The failure is the message's only delivery
// mechanism.
//
// It is gated on the runner's own variables rather than on anything this repo sets: GitHub-hosted runners
// export both CI and GITHUB_ACTIONS, and the org-wide component-go-checks workflow runs `go test` directly
// on ubuntu-latest with no container, so both reach the test process.
func RequireProvisioningAllowed(suite string) {
	switch decide(os.Getenv) {
	case allowedCI:
		return
	case allowedOptIn:
		fmt.Fprintf(os.Stderr,
			"clusterguard: %s is provisioning a kind cluster because %s is set. "+
				"This competes with every other cluster on this Docker daemon.\n", suite, AllowEnv)
		return
	}

	fmt.Fprintf(os.Stderr, `
clusterguard: SKIPPING %s -- it provisions a kind cluster, and only CI may do that.

  Run the unit suites instead, which need no cluster:
      go test -tags=unit ./...

  For cluster work, use a disposable GKE cluster or GCE instance rather than this
  machine's Docker daemon, which is shared.

  If you really mean to provision kind here, set %s=1.

This exits NON-ZERO on purpose. Exiting 0 was tried first and measured: `+"`go test`"+` suppresses a
passing package's output entirely, so the message above never reached the terminal and the run
printed a bare `+"`ok`"+` for a suite that had executed no tests. A guard that reports success for
work it prevented is worse than no guard.

`, suite, AllowEnv)
	os.Exit(1)
}

// verdict is the decision, separated from the exit so it can be tested.
//
// The CI arm cannot be tested by running it -- doing so would provision the cluster this package exists
// to prevent -- so it is tested here instead, and the three arms are asserted to be distinguishable from
// each other rather than inferred from one of them.
type verdict int

const (
	denied verdict = iota
	allowedCI
	allowedOptIn
)

// decide reads the environment through the supplied getter so a test never mutates the process it runs in.
//
// Gated on the RUNNER's own variables, not on anything this repo sets: a repo-set variable would be absent
// in exactly the situation that matters (someone typing the CI invocation locally), because they would be
// copying the arguments and not the environment.
func decide(env func(string) string) verdict {
	if env("CI") != "" || env("GITHUB_ACTIONS") != "" {
		return allowedCI
	}
	if env(AllowEnv) != "" {
		return allowedOptIn
	}
	return denied
}
