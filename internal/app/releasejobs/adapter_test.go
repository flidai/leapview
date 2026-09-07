package releasejobs

import (
	"context"
	"testing"

	"github.com/flidai/leapview/pkg/jobs"
)

func TestRecordWorkflowFailsClosedWithoutJobsAuthority(t *testing.T) {
	if err := New(nil).RecordWorkflow(context.Background(), nil, jobs.WorkflowIntent{}); err == nil {
		t.Fatal("workflow adapter accepted nil jobs authority")
	}
}
