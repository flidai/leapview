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

type failingReleaseAuditStore struct{ err error }

func (*failingReleaseAuditStore) Enqueue(context.Context, jobs.EnqueueInput) (jobs.Job, error) {
	return jobs.Job{}, nil
}

func (store *failingReleaseAuditStore) AppendEvent(context.Context, string, string, string, []byte) (jobs.Event, error) {
	return jobs.Event{}, store.err
}

func (*failingReleaseAuditStore) ListEvents(context.Context, string, string, int64, int) ([]jobs.Event, error) {
	return nil, nil
}

func TestReleaseTransactionalAuditCannotUseBestEffortPath(t *testing.T) {
	var logs bytes.Buffer
	module := &Module{
		api:    APIConfig{Jobs: &failingReleaseAuditStore{err: errors.New("audit store unavailable")}},
		logger: slog.New(slog.NewTextHandler(&logs, nil)),
	}

	module.recordBestEffortEvent(
		t.Context(), "createRelease", "release_1",
		releaseCreatedAuditAction, map[string]any{"status": "draft"},
	)

	output := logs.String()
	for _, expected := range []string{"release command contract execution failed", "operation_id=createRelease", "requires transactional auditing"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("audit failure log = %q, missing %q", output, expected)
		}
	}
}
