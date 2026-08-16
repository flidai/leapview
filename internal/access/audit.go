package access

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type AuditEventRecorder interface {
	RecordAuditEvent(context.Context, AuditEventInput) error
}

const auditWriteAttempts = 3

// PersistAuditEvent retries short-lived storage failures and always reports a
// sustained failure. Callers that can fail closed should also propagate the
// returned error.
func PersistAuditEvent(ctx context.Context, recorder AuditEventRecorder, input AuditEventInput) error {
	if recorder == nil {
		return fmt.Errorf("audit event recorder is required")
	}
	var lastErr error
	for attempt := 1; attempt <= auditWriteAttempts; attempt++ {
		lastErr = recorder.RecordAuditEvent(ctx, input)
		if lastErr == nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			lastErr = err
			break
		}
		if attempt < auditWriteAttempts {
			timer := time.NewTimer(time.Duration(attempt) * 10 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				lastErr = ctx.Err()
				attempt = auditWriteAttempts
			case <-timer.C:
			}
		}
	}
	slog.ErrorContext(ctx, "audit event persistence failed",
		"action", input.Action,
		"principal_id", input.PrincipalID,
		"request_id", input.RequestID,
		"error", lastErr,
	)
	return fmt.Errorf("persist audit event %q: %w", input.Action, lastErr)
}
