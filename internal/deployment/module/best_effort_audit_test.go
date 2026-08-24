package module

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/flidai/leapview/pkg/jobs"
)

type failingDeploymentAuditStore struct{ err error }

func (*failingDeploymentAuditStore) Enqueue(context.Context, jobs.EnqueueInput) (jobs.Job, error) {
	return jobs.Job{}, nil
}

func (*failingDeploymentAuditStore) Cancel(context.Context, string) error { return nil }

func (store *failingDeploymentAuditStore) AppendEvent(context.Context, string, string, string, []byte) (jobs.Event, error) {
	return jobs.Event{}, store.err
}

func (*failingDeploymentAuditStore) ListEvents(context.Context, string, string, int64, int) ([]jobs.Event, error) {
	return nil, nil
}

func TestDeploymentTransactionalAuditCannotUseBestEffortPath(t *testing.T) {
	var logs bytes.Buffer
	module := &Module{
		api:    APIConfig{Jobs: &failingDeploymentAuditStore{err: errors.New("audit store unavailable")}},
		logger: slog.New(slog.NewTextHandler(&logs, nil)),
	}

	module.recordBestEffortAPIEvent(
		t.Context(), "cancelDeployment", "deployment_1",
		deploymentCancelledAuditAction, map[string]any{"status": "cancelled"},
	)

	output := logs.String()
	for _, expected := range []string{"deployment command contract execution failed", "operation_id=cancelDeployment", "requires transactional auditing"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("audit failure log = %q, missing %q", output, expected)
		}
	}
}
