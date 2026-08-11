// Package command enforces generated APIGen command audit guarantees at runtime.
package command

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	apigenaudit "github.com/Yacobolo/toolbelt/apigen/runtime/audit"
)

type Guarantee string

const (
	GuaranteeTransactional Guarantee = "transactional"
	GuaranteeBestEffort    Guarantee = "best-effort"
)

var (
	ErrContractNotFound     = errors.New("APIGen command contract not found")
	ErrInvalidContract      = errors.New("invalid APIGen command contract")
	ErrExecutionUnavailable = errors.New("APIGen command execution is unavailable")
)

// Contract is the runtime-normalized subset of a generated command contract.
type Contract struct {
	OperationID  string
	Owner        string
	AuditAction  string
	Guarantee    Guarantee
	AuditPayload *apigenaudit.Contract
	Execution    *AsyncExecutionContract
}

type AsyncExecutionContract struct {
	Mode            string
	Guarantee       string
	JobKind         string
	ResourceKind    string
	InitialEvent    string
	InitialState    string
	StatusOperation string
	EventsOperation string
	Cancellation    string
}

func (c Contract) Validate() error {
	if strings.TrimSpace(c.OperationID) == "" || strings.TrimSpace(c.Owner) == "" || strings.TrimSpace(c.AuditAction) == "" {
		return fmt.Errorf("%w: operation ID, owner, and audit action are required", ErrInvalidContract)
	}
	switch c.Guarantee {
	case GuaranteeTransactional, GuaranteeBestEffort:
	default:
		return fmt.Errorf("%w: operation %q has unsupported audit guarantee %q", ErrInvalidContract, c.OperationID, c.Guarantee)
	}
	if c.AuditPayload == nil {
		return fmt.Errorf("%w: operation %q requires a typed audit payload", ErrInvalidContract, c.OperationID)
	}
	if err := c.AuditPayload.Validate(); err != nil {
		return fmt.Errorf("%w: operation %q audit payload: %v", ErrInvalidContract, c.OperationID, err)
	}
	if execution := c.Execution; execution != nil {
		if execution.Mode != "async" || execution.Guarantee != "transactional" || strings.TrimSpace(execution.JobKind) == "" || strings.TrimSpace(execution.ResourceKind) == "" ||
			strings.TrimSpace(execution.InitialEvent) == "" || strings.TrimSpace(execution.InitialState) == "" || strings.TrimSpace(execution.StatusOperation) == "" ||
			strings.TrimSpace(execution.EventsOperation) == "" || (execution.Cancellation != "supported" && execution.Cancellation != "unsupported") {
			return fmt.Errorf("%w: operation %q has an incomplete async execution contract", ErrInvalidContract, c.OperationID)
		}
	}
	return nil
}

type Lookup func(operationID string) (Contract, bool)

// Execution supplies the guarantee-specific capability. Execute selects the
// callback from the generated contract; callers do not select the policy.
type Execution struct {
	BestEffortAudit func(context.Context, Contract) error
	Transactional   func(context.Context, Contract) error
	LogMessage      string
	LogAttributes   []slog.Attr
}

type Executor struct {
	lookup Lookup
	logger *slog.Logger
}

func NewExecutor(lookup Lookup, logger *slog.Logger) (*Executor, error) {
	if lookup == nil {
		return nil, fmt.Errorf("%w: generated command lookup is required", ErrExecutionUnavailable)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Executor{lookup: lookup, logger: logger}, nil
}

// Execute applies the generated guarantee for operationID. Best-effort audit
// failures are logged and do not replace an already-successful mutation
// result. Transactional failures are returned to the caller so the supplied
// atomic capability can roll back the mutation and audit together.
func (e *Executor) Execute(ctx context.Context, operationID string, execution Execution) error {
	if e == nil || e.lookup == nil {
		return fmt.Errorf("%w: executor is not configured", ErrExecutionUnavailable)
	}
	if strings.TrimSpace(operationID) == "" {
		operationID, _ = OperationID(ctx)
	}
	contract, ok := e.lookup(operationID)
	if !ok {
		return fmt.Errorf("%w: %q", ErrContractNotFound, operationID)
	}
	if err := contract.Validate(); err != nil {
		return err
	}

	switch contract.Guarantee {
	case GuaranteeTransactional:
		if execution.Transactional == nil {
			return fmt.Errorf("%w: operation %q requires transactional auditing", ErrExecutionUnavailable, operationID)
		}
		if err := execution.Transactional(ctx, contract); err != nil {
			return err
		}
		markCompleted(ctx, contract)
		return nil
	case GuaranteeBestEffort:
		if execution.BestEffortAudit == nil {
			return fmt.Errorf("%w: operation %q requires a best-effort audit recorder", ErrExecutionUnavailable, operationID)
		}
		if err := execution.BestEffortAudit(ctx, contract); err != nil {
			attrs := []slog.Attr{
				slog.String("operation_id", contract.OperationID),
				slog.String("operation_owner", contract.Owner),
				slog.String("audit_action", contract.AuditAction),
				slog.String("audit_guarantee", string(contract.Guarantee)),
				slog.Any("error", err),
			}
			message := strings.TrimSpace(execution.LogMessage)
			if message == "" {
				message = "best-effort command audit failed"
			}
			e.logger.LogAttrs(ctx, slog.LevelError, message, append(attrs, execution.LogAttributes...)...)
		}
		markCompleted(ctx, contract)
		return nil
	default:
		return fmt.Errorf("%w: operation %q has unsupported audit guarantee %q", ErrInvalidContract, operationID, contract.Guarantee)
	}
}

type executionContextKey struct{}

type executionState struct {
	contract  Contract
	completed atomic.Bool
}

// Guard tracks whether a generated command passed through Executor. It is
// intended for generated transport boundaries that buffer successful replies.
type Guard struct {
	state *executionState
}

func Begin(ctx context.Context, contract Contract) (context.Context, *Guard, error) {
	if err := contract.Validate(); err != nil {
		return ctx, nil, err
	}
	state := &executionState{contract: contract}
	return context.WithValue(ctx, executionContextKey{}, state), &Guard{state: state}, nil
}

func (g *Guard) Completed() bool {
	return g != nil && g.state != nil && g.state.completed.Load()
}

func OperationID(ctx context.Context) (string, bool) {
	state, ok := ctx.Value(executionContextKey{}).(*executionState)
	if !ok || state == nil {
		return "", false
	}
	return state.contract.OperationID, true
}

func markCompleted(ctx context.Context, contract Contract) {
	state, ok := ctx.Value(executionContextKey{}).(*executionState)
	if ok && state != nil && state.contract.OperationID == contract.OperationID && state.contract.Guarantee == contract.Guarantee {
		state.completed.Store(true)
	}
}
