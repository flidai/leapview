package run

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestSafeWorkerFailureMessageUsesOnlyBoundedCategories(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "plan", err: WithWorkerFailureStage(WorkerFailureStagePlan, errors.New("warehouse password=secret")), want: WorkerFailureMessagePlan},
		{name: "build", err: WithWorkerFailureStage(WorkerFailureStageBuild, errors.New("select * from private_table")), want: WorkerFailureMessageBuild},
		{name: "evidence", err: WithWorkerFailureStage(WorkerFailureStageEvidence, errors.New("token=secret")), want: WorkerFailureMessageEvidence},
		{name: "stale", err: fmt.Errorf("details %w: credential=secret", ErrRunStale), want: WorkerFailureMessageStale},
		{name: "timeout", err: fmt.Errorf("details: %w", context.DeadlineExceeded), want: WorkerFailureMessageTimeout},
		{name: "unclassified", err: errors.New("database password=secret"), want: WorkerFailureMessageGeneric},
		{name: "nil", want: WorkerFailureMessageGeneric},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SafeWorkerFailureMessage(tt.err); got != tt.want {
				t.Fatalf("SafeWorkerFailureMessage() = %q, want %q", got, tt.want)
			}
			if got := AllowWorkerFailureMessage(tt.want); got != tt.want {
				t.Fatalf("AllowWorkerFailureMessage(safe value) = %q, want %q", got, tt.want)
			}
		})
	}
	for _, raw := range []string{"password=secret", WorkerFailureMessageBuild + " password=secret", ""} {
		if got := AllowWorkerFailureMessage(raw); got != WorkerFailureMessageGeneric {
			t.Fatalf("AllowWorkerFailureMessage(%q) = %q, want generic fallback", raw, got)
		}
	}
}
