package run

import (
	"context"
	"errors"
)

// WorkerFailureStage is an internal, executor-owned hint used only to choose
// a bounded user-facing failure category. It must never carry raw diagnostics.
type WorkerFailureStage string

const (
	WorkerFailureStagePlan     WorkerFailureStage = "plan"
	WorkerFailureStageBuild    WorkerFailureStage = "build"
	WorkerFailureStageEvidence WorkerFailureStage = "evidence"
)

const (
	WorkerFailureMessageGeneric  = "refresh execution failed"
	WorkerFailureMessageStale    = "refresh base changed before execution"
	WorkerFailureMessageTimeout  = "refresh execution timed out"
	WorkerFailureMessagePlan     = "refresh plan validation failed"
	WorkerFailureMessageBuild    = "refresh build failed"
	WorkerFailureMessageEvidence = "refresh result evidence verification failed"
)

type workerFailure struct {
	stage WorkerFailureStage
	err   error
}

func (e workerFailure) Error() string { return e.err.Error() }
func (e workerFailure) Unwrap() error { return e.err }

// WithWorkerFailureStage marks a failure with a fixed executor phase. The
// original error remains available to worker logs and errors.Is/As, but is
// never copied into persisted run diagnostics.
func WithWorkerFailureStage(stage WorkerFailureStage, err error) error {
	if err == nil {
		return nil
	}
	switch stage {
	case WorkerFailureStagePlan, WorkerFailureStageBuild, WorkerFailureStageEvidence:
		return workerFailure{stage: stage, err: err}
	default:
		return err
	}
}

// SafeWorkerFailureMessage collapses worker errors to a small allowlist of
// actionable summaries. Arbitrary error text may include SQL, credentials,
// warehouse responses, or source values and is intentionally discarded.
func SafeWorkerFailureMessage(err error) string {
	if errors.Is(err, ErrRunStale) {
		return WorkerFailureMessageStale
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return WorkerFailureMessageTimeout
	}
	var staged workerFailure
	if errors.As(err, &staged) {
		switch staged.stage {
		case WorkerFailureStagePlan:
			return WorkerFailureMessagePlan
		case WorkerFailureStageBuild:
			return WorkerFailureMessageBuild
		case WorkerFailureStageEvidence:
			return WorkerFailureMessageEvidence
		}
	}
	return WorkerFailureMessageGeneric
}

// AllowWorkerFailureMessage accepts only summaries produced by
// SafeWorkerFailureMessage. Persistence adapters use it as a second boundary
// so direct callers cannot persist arbitrary strings.
func AllowWorkerFailureMessage(message string) string {
	switch message {
	case WorkerFailureMessageStale, WorkerFailureMessageTimeout,
		WorkerFailureMessagePlan, WorkerFailureMessageBuild,
		WorkerFailureMessageEvidence:
		return message
	default:
		return WorkerFailureMessageGeneric
	}
}
