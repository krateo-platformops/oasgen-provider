package restResources

import (
	"context"
	"testing"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/async"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestAsyncRequeueEnabled(t *testing.T) {
	assert.False(t, asyncRequeueEnabled(nil))
	assert.False(t, asyncRequeueEnabled(&getter.AsyncConfig{}), "unset mode defaults to blocking (Model A)")
	assert.False(t, asyncRequeueEnabled(&getter.AsyncConfig{Mode: "blocking"}))
	assert.True(t, asyncRequeueEnabled(&getter.AsyncConfig{Mode: "requeue"}))
	assert.True(t, asyncRequeueEnabled(&getter.AsyncConfig{Mode: "Requeue"}), "mode match is case-insensitive")
}

func TestAsyncOperationInFlight(t *testing.T) {
	mg := &unstructured.Unstructured{}
	id, action := asyncOperationInFlight(mg)
	assert.Empty(t, id)
	assert.Empty(t, action)

	mg.SetAnnotations(map[string]string{
		annotationAsyncOperationID:     "op-9",
		annotationAsyncOperationAction: "create",
	})
	id, action = asyncOperationInFlight(mg)
	assert.Equal(t, "op-9", id)
	assert.Equal(t, "create", action)
}

func TestPollAsyncOnce(t *testing.T) {
	// A clientInfo with no get verb: pollAsyncOnce falls back to binding only {operationId}, which is all the
	// mock needs to answer. This exercises the classify-from-status path without a full builder setup.
	clientInfo := &getter.Info{Resource: getter.Resource{VerbsDescription: []getter.VerbsDescription{}}}
	cfg := &getter.AsyncConfig{Poll: getter.PollConfig{
		Path:          "/ops/{operationId}",
		StatusPath:    "status",
		SuccessValues: []string{"succeeded"},
		FailureValues: []string{"failed"},
	}}

	t.Run("pending", func(t *testing.T) {
		cli := &mockAsyncClient{pollBodies: []map[string]interface{}{{"status": "running"}}}
		outcome, status, err := pollAsyncOnce(context.Background(), cli, clientInfo, &unstructured.Unstructured{}, cfg, "op-1")
		require.NoError(t, err)
		assert.Equal(t, async.OutcomePending, outcome)
		assert.Equal(t, "running", status)
		assert.Equal(t, "op-1", cli.lastConf.Parameters["operationId"], "poll binds the operation handle")
	})

	t.Run("succeeded", func(t *testing.T) {
		cli := &mockAsyncClient{pollBodies: []map[string]interface{}{{"status": "succeeded"}}}
		outcome, _, err := pollAsyncOnce(context.Background(), cli, clientInfo, &unstructured.Unstructured{}, cfg, "op-2")
		require.NoError(t, err)
		assert.Equal(t, async.OutcomeSucceeded, outcome)
	})

	t.Run("failed", func(t *testing.T) {
		cli := &mockAsyncClient{pollBodies: []map[string]interface{}{{"status": "failed"}}}
		outcome, _, err := pollAsyncOnce(context.Background(), cli, clientInfo, &unstructured.Unstructured{}, cfg, "op-3")
		require.NoError(t, err)
		assert.Equal(t, async.OutcomeFailed, outcome)
	})

	t.Run("poll error is surfaced", func(t *testing.T) {
		cli := &mockAsyncClient{callErr: assert.AnError}
		_, _, err := pollAsyncOnce(context.Background(), cli, clientInfo, &unstructured.Unstructured{}, cfg, "op-4")
		require.Error(t, err)
	})
}

func TestPollBaseFromRecord_UsesPersistedParams(t *testing.T) {
	mg := &unstructured.Unstructured{}
	mg.SetAnnotations(map[string]string{
		annotationAsyncOperationParams: `{"parameters":{"organization":"acme"},"query":{"api-version":"7.0"}}`,
	})
	// With the params annotation present, the base is decoded from it without touching cli/clientInfo.
	base := pollBaseFromRecord(context.Background(), nil, nil, mg)
	require.NotNil(t, base)
	assert.Equal(t, "acme", base.Parameters["organization"])
	assert.Equal(t, "7.0", base.Query["api-version"])
}

func TestPollAsyncOnce_ReusesPersistedParams(t *testing.T) {
	// The poll runs in a later reconcile where the trigger request is gone; it must still send the trigger's
	// resolved shared inputs (persisted at record time) so a poll endpoint requiring e.g. api-version validates.
	cli := &mockAsyncClient{pollBodies: []map[string]interface{}{{"status": "succeeded"}}}
	mg := &unstructured.Unstructured{}
	mg.SetAnnotations(map[string]string{
		annotationAsyncOperationParams: `{"parameters":{"organization":"acme"},"query":{"api-version":"7.0"}}`,
	})
	cfg := &getter.AsyncConfig{Poll: getter.PollConfig{
		Path:          "/{organization}/ops/{operationId}",
		StatusPath:    "status",
		SuccessValues: []string{"succeeded"},
	}}

	outcome, _, err := pollAsyncOnce(context.Background(), cli, &getter.Info{}, mg, cfg, "op-1")
	require.NoError(t, err)
	assert.Equal(t, async.OutcomeSucceeded, outcome)
	require.NotNil(t, cli.lastConf)
	assert.Equal(t, "acme", cli.lastConf.Parameters["organization"], "poll reuses the trigger's persisted path param")
	assert.Equal(t, "7.0", cli.lastConf.Query["api-version"], "poll reuses the trigger's persisted query param")
	assert.Equal(t, "op-1", cli.lastConf.Parameters["operationId"], "operationId is bound")
}
