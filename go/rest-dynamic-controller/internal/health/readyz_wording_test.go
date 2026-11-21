//go:build unit

package health

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNoSurfaceSaysAWaitingReplicaIsNotReady guards the defect that produced this file.
//
// ReadyzCheck fails on two things -- the informer's first list, and the lease being unREADABLE or
// unWRITABLE. A replica merely WAITING for a peer's lease is READY (readyz.go:43, and
// ownershipReadyzError returns non-nil only when lastErr is set).
//
// ONE WRONG BELIEF REACHED FOUR SURFACES: this package's doc, main.go's flag help,
// values.schema.json's description and the 0.85.0 log section all said a waiting replica reports
// NotReady. None of them was contradicted by anything nearby, and the code never agreed with any of
// them. A consumer asking which of the two readings to assert is what found it.
//
// So this asserts on the SHAPE OF THE CLAIM rather than on any one wording: no documentation surface
// may put "waiting" and "NotReady" in the same sentence about a peer's lease.
// TestEverySurfaceSaysAWaitingReplicaIsReady guards the defect that produced this file, and it is
// phrased POSITIVELY on purpose.
//
// ReadyzCheck fails on two things -- the informer's first list, and the lease being unREADABLE or
// unWRITABLE. A replica merely WAITING for a peer's lease is READY (readyz.go:43, and
// ownershipReadyzError returns non-nil only when lastErr is set).
//
// ONE WRONG BELIEF REACHED FOUR SURFACES: this package's doc, main.go's flag help,
// values.schema.json's description and the 0.85.0 log section all said a waiting replica reports
// NotReady. The code never agreed with any of them, and nothing nearby contradicted them. A consumer
// asking which of the two readings to assert is what found it.
//
// WHY NOT "THE WRONG SENTENCE IS ABSENT", which is what I wrote first: the correction itself QUOTES the
// wrong claim in order to explain it, so a forbidding pattern fired on the fix. That is the same
// equivalence-class error as grepping for text a replacement deliberately quotes -- and I made it twice
// in one day, the second time inside the fix for the first. A POSITIVE assertion cannot be satisfied by
// a quotation of the error, which is the property that makes it the right instrument here.
func TestEverySurfaceSaysAWaitingReplicaIsReady(t *testing.T) {
	// go test runs with the PACKAGE directory as cwd, so every path is relative to internal/health. The
	// first version used repo-relative paths and failed on "no such file or directory" -- a path error
	// wearing a wording error's clothes, which is why each read below is require.NoError and not a skip.
	root := filepath.Join("..", "..", "..", "..")
	surfaces := []string{
		"health.go",
		filepath.Join("..", "..", "main.go"),
		filepath.Join(root, "helm", "oasgen-provider", "values.schema.json"),
	}

	// Each surface must SAY the waiting replica is ready, in whatever words -- "waiting" near "READY".
	//
	// NEWLINES AND GO CONCATENATION ARE ALLOWED IN THE GAP, because this reads SOURCE TEXT and main.go
	// splits the sentence across two string literals:
	//
	//	"... A replica merely WAITING "+
	//		"for a peer's lease is READY: ..."
	//
	// A pattern that stopped at \n reported main.go as missing the statement it makes. The alternative
	// was reflowing the source to suit the guard, which would make the guard fail on the next reflow --
	// an instrument that constrains the thing it measures is measuring itself.
	good := regexp.MustCompile(`(?is)WAITING[^.]{0,140}is READY`)

	checked := 0
	for _, rel := range surfaces {
		b, err := os.ReadFile(rel)
		require.NoError(t, err, "could not read %s", rel)
		require.NotEmpty(t, b, "%s is empty", rel)
		checked++
		require.True(t, good.Match(b),
			"%s never states that a replica waiting for a peer's lease is READY. That is what the code "+
				"does, and the inverse shipped on four surfaces at once because no check asserted it. An "+
				"operator reading this surface would expect a NotReady standby and conclude the probe was "+
				"broken when it is not", rel)
	}
	require.Equal(t, 3, checked, "not every documentation surface was checked")

	// THE GUARD MUST BE ABLE TO FAIL. A pattern matching anything would pass on any input, which is the
	// one failure mode a wording guard cannot afford.
	require.False(t, good.MatchString("a replica waiting for the lease reports NotReady"),
		"the pattern matches the sentence that actually shipped, so it would accept the defect")
	require.True(t, good.MatchString("a replica merely WAITING for a peer's lease is READY"),
		"the pattern does not match the corrected sentence, so no surface could ever satisfy it")
}
