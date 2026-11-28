//go:build unit

package restdefinition

import (
	"context"
	"fmt"
	"testing"

	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/stretchr/testify/require"
)

// recLogger records Debug lines as "msg k=v k=v" so a test can read what an operator would see.
type recLogger struct{ lines *[]string }

func newRecLogger() recLogger { return recLogger{lines: &[]string{}} }

func (r recLogger) Info(string, ...any)         {}
func (r recLogger) Error(error, string, ...any) {}
func (r recLogger) Debug(msg string, kv ...any) {
	s := msg
	for i := 0; i+1 < len(kv); i += 2 {
		s += fmt.Sprintf(" %v=%v", kv[i], kv[i+1])
	}
	*r.lines = append(*r.lines, s)
}
func (r recLogger) WithValues(...any) logging.Logger { return r }
func (r recLogger) Warn(string, ...any)              {}
func (r recLogger) WithName(string) logging.Logger   { return r }

// "The watch fired and matched nothing" and "the watch never fired" must be distinguishable in a log:
// the first leaves a matched=0 line, the second leaves none.
func TestMapFunctionsLogMatchedAndListedCounts(t *testing.T) {
	rd := rdFor("pullrequest", "example.krateo.io", "PullRequest")
	kube := mapperKube(t, rd)

	t.Run("generated", func(t *testing.T) {
		l := newRecLogger()
		f := enqueueRestDefinitionForGeneratedKind(kube, l)

		require.Len(t, f(context.Background(), instanceOf("example.krateo.io", resourceVersion, "PullRequest", "ns", "a")), 1)
		require.Contains(t, (*l.lines)[0], "registry=generated")
		require.Contains(t, (*l.lines)[0], "matched=1 listed=1")

		require.Empty(t, f(context.Background(), instanceOf("example.krateo.io", resourceVersion, "Issue", "ns", "b")))
		require.Contains(t, (*l.lines)[1], "matched=0 listed=1")
		require.Len(t, *l.lines, 2, "exactly one line per incoming object")
	})

	t.Run("configuration", func(t *testing.T) {
		l := newRecLogger()
		f := enqueueRestDefinitionForConfiguration(kube, l)

		require.Len(t, f(context.Background(), configurationEvent("example.krateo.io", "PullRequest")), 1)
		require.Empty(t, f(context.Background(), configurationEvent("other.example", "Widget")))
		require.Len(t, *l.lines, 2, "exactly one line per incoming object")
		require.Contains(t, (*l.lines)[0], "registry=configuration")
		require.Contains(t, (*l.lines)[0], "matched=1 listed=1")
		require.Contains(t, (*l.lines)[1], "matched=0 listed=1")
	})
}
