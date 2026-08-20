package jobs

import (
	"context"

	"github.com/flidai/leapview/internal/platform/transaction"
	publicjobs "github.com/flidai/leapview/pkg/jobs"
)

// WorkflowRecorder writes an event and any required follow-up job using the
// transaction owned by the capability making the state transition.
type WorkflowRecorder interface {
	RecordWorkflow(context.Context, transaction.Transaction, publicjobs.WorkflowIntent) error
}

// WorkflowRecorderFunc adapts a transaction-bound workflow callback.
type WorkflowRecorderFunc func(context.Context, transaction.Transaction, publicjobs.WorkflowIntent) error

func (f WorkflowRecorderFunc) RecordWorkflow(ctx context.Context, tx transaction.Transaction, intent publicjobs.WorkflowIntent) error {
	return f(ctx, tx, intent)
}
