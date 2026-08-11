// Package command enforces generated APIGen command audit guarantees at runtime.
package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
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
	ErrInvocationRejected   = errors.New("APIGen command invocation rejected")
	ErrIdempotencyRequired  = errors.New("command idempotency key is required")
	ErrTargetRequired       = errors.New("command authorization target is required")
	ErrSurfaceNotExposed    = errors.New("command surface is not exposed")
	ErrOperationMismatch    = errors.New("command operation identity does not match")
	ErrPreconditionRequired = errors.New("command precondition is required")
	ErrPreconditionFailed   = errors.New("command precondition failed")
)

type IdempotencyPolicy string

const (
	IdempotencyNone     IdempotencyPolicy = ""
	IdempotencyRequired IdempotencyPolicy = "required"
)

type ConcurrencyPolicy string

const (
	ConcurrencyNone    ConcurrencyPolicy = ""
	ConcurrencyIfMatch ConcurrencyPolicy = "if-match"
)

type Surface string

const (
	SurfaceAPI        Surface = "api"
	SurfaceCLI        Surface = "cli"
	SurfaceUI         Surface = "ui"
	SurfaceAgent      Surface = "agent"
	SurfaceAutomation Surface = "automation"
)

type Dependency string

const (
	DependencyAuthorization Dependency = "authorization"
	DependencyIdempotency   Dependency = "idempotency"
	DependencyConcurrency   Dependency = "concurrency"
	DependencyAudit         Dependency = "audit"
	DependencyJobQueue      Dependency = "job-queue"
)

type TargetContract struct {
	Parameter string
	Type      string
}

type Invocation struct {
	OperationID      string
	Surface          Surface
	TargetValues     map[string]string
	IdempotencyKey   string
	ConcurrencyToken string
	RequestID        string
	CorrelationID    string
}

// Contract is the runtime-normalized subset of a generated command contract.
type Contract struct {
	OperationID         string
	Owner               string
	Method              string
	Path                string
	Target              *TargetContract
	Idempotency         IdempotencyPolicy
	Concurrency         ConcurrencyPolicy
	AuthzMode           string
	Privilege           string
	AdditionalExposures []Surface
	AuditAction         string
	Guarantee           Guarantee
	AuditPayload        *apigenaudit.Contract
	Execution           *AsyncExecutionContract
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
	if strings.TrimSpace(c.OperationID) == "" || strings.TrimSpace(c.Owner) == "" || strings.TrimSpace(c.Method) == "" || strings.TrimSpace(c.Path) == "" || strings.TrimSpace(c.AuditAction) == "" {
		return fmt.Errorf("%w: operation ID, owner, method, path, and audit action are required", ErrInvalidContract)
	}
	switch c.AuthzMode {
	case "none", "authenticated":
		if strings.TrimSpace(c.Privilege) != "" {
			return fmt.Errorf("%w: operation %q privilege requires privilege authorization", ErrInvalidContract, c.OperationID)
		}
	case "privilege":
		if strings.TrimSpace(c.Privilege) == "" {
			return fmt.Errorf("%w: operation %q privilege authorization requires a privilege", ErrInvalidContract, c.OperationID)
		}
	default:
		return fmt.Errorf("%w: operation %q has unsupported authorization mode %q", ErrInvalidContract, c.OperationID, c.AuthzMode)
	}
	if c.Target != nil && (strings.TrimSpace(c.Target.Parameter) == "" || strings.TrimSpace(c.Target.Type) == "") {
		return fmt.Errorf("%w: operation %q has an incomplete authorization target", ErrInvalidContract, c.OperationID)
	}
	switch c.Idempotency {
	case IdempotencyNone, IdempotencyRequired:
	default:
		return fmt.Errorf("%w: operation %q has unsupported idempotency policy %q", ErrInvalidContract, c.OperationID, c.Idempotency)
	}
	switch c.Concurrency {
	case ConcurrencyNone, ConcurrencyIfMatch:
	default:
		return fmt.Errorf("%w: operation %q has unsupported concurrency policy %q", ErrInvalidContract, c.OperationID, c.Concurrency)
	}
	seenSurfaces := map[Surface]struct{}{}
	for _, surface := range c.AdditionalExposures {
		switch surface {
		case SurfaceUI, SurfaceAgent, SurfaceAutomation:
		default:
			return fmt.Errorf("%w: operation %q has unsupported additional exposure %q", ErrInvalidContract, c.OperationID, surface)
		}
		if _, exists := seenSurfaces[surface]; exists {
			return fmt.Errorf("%w: operation %q duplicates exposure %q", ErrInvalidContract, c.OperationID, surface)
		}
		seenSurfaces[surface] = struct{}{}
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

// Exposes reports whether the command may be invoked through a surface. API
// and CLI are canonical generated transports; direct UI, agent, and
// automation invocation must be explicitly declared.
func (c Contract) Exposes(surface Surface) bool {
	if surface == SurfaceAPI || surface == SurfaceCLI {
		return true
	}
	for _, exposed := range c.AdditionalExposures {
		if exposed == surface {
			return true
		}
	}
	return false
}

// Dependencies derives startup/readiness requirements from the generated
// command policy so dependencies do not become a second authored framework.
func (c Contract) Dependencies() []Dependency {
	dependencies := make([]Dependency, 0, 5)
	if c.AuthzMode != "none" {
		dependencies = append(dependencies, DependencyAuthorization)
	}
	if c.Idempotency == IdempotencyRequired {
		dependencies = append(dependencies, DependencyIdempotency)
	}
	if c.Concurrency == ConcurrencyIfMatch {
		dependencies = append(dependencies, DependencyConcurrency)
	}
	dependencies = append(dependencies, DependencyAudit)
	if c.Execution != nil {
		dependencies = append(dependencies, DependencyJobQueue)
	}
	return dependencies
}

func (c Contract) SpanName() string {
	return "command." + c.OperationID
}

// ValidateDependencies fails startup/readiness when a generated command
// requires a capability that the composition root did not provide.
func ValidateDependencies(contracts map[string]Contract, available map[Dependency]bool) error {
	for operationID, contract := range contracts {
		if err := contract.Validate(); err != nil {
			return fmt.Errorf("command %q: %w", operationID, err)
		}
		for _, dependency := range contract.Dependencies() {
			if !available[dependency] {
				return fmt.Errorf("%w: operation %q requires %q", ErrExecutionUnavailable, operationID, dependency)
			}
		}
	}
	return nil
}

// MatchPath matches a generated route template against a concrete path.
// Parameter extraction remains transport-owned; policy selection does not.
func MatchPath(pattern, path string) bool {
	patternParts := strings.Split(strings.Trim(strings.TrimSpace(pattern), "/"), "/")
	pathParts := strings.Split(strings.Trim(strings.TrimSpace(path), "/"), "/")
	if len(patternParts) != len(pathParts) {
		return false
	}
	for index, part := range patternParts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") && len(part) > 2 {
			if pathParts[index] == "" {
				return false
			}
			continue
		}
		if part != pathParts[index] {
			return false
		}
	}
	return true
}

// RevisionToken returns the stable strong token used by generated concurrency
// policies. Domains choose the canonical revision source; the executor owns
// token construction and comparison.
func RevisionToken(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode command revision: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return strconv.Quote(hex.EncodeToString(sum[:])), nil
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
			e.observe(ctx, contract, "failed")
			return err
		}
		markCompleted(ctx, contract)
		e.observe(ctx, contract, "succeeded")
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
		e.observe(ctx, contract, "succeeded")
		return nil
	default:
		return fmt.Errorf("%w: operation %q has unsupported audit guarantee %q", ErrInvalidContract, operationID, contract.Guarantee)
	}
}

func (e *Executor) observe(ctx context.Context, contract Contract, outcome string) {
	if e == nil || e.logger == nil {
		return
	}
	surface := Surface("")
	if state, ok := ctx.Value(executionContextKey{}).(*executionState); ok && state != nil {
		surface = state.invocation.Surface
	}
	targetType := ""
	if contract.Target != nil {
		targetType = contract.Target.Type
	}
	e.logger.LogAttrs(ctx, slog.LevelDebug, "command execution completed",
		slog.String("span_name", contract.SpanName()),
		slog.String("operation_id", contract.OperationID),
		slog.String("operation_owner", contract.Owner),
		slog.String("outcome", outcome),
		slog.String("surface", string(surface)),
		slog.String("target_type", targetType),
		slog.String("authz_mode", contract.AuthzMode),
		slog.String("idempotency_policy", string(contract.Idempotency)),
		slog.String("concurrency_policy", string(contract.Concurrency)),
	)
}

type executionContextKey struct{}

type executionState struct {
	contract           Contract
	invocation         Invocation
	completed          atomic.Bool
	concurrencyChecked atomic.Bool
}

// Guard tracks whether a generated command passed through Executor. It is
// intended for generated transport boundaries that buffer successful replies.
type Guard struct {
	state *executionState
}

func Begin(ctx context.Context, contract Contract) (context.Context, *Guard, error) {
	return begin(ctx, contract, Invocation{})
}

// BeginInvocation validates the generated cross-surface policy before domain
// dispatch. Authorization itself remains an injected capability, but target
// extraction, exposure, idempotency, and concurrency requirements come from
// this one contract.
func BeginInvocation(ctx context.Context, contract Contract, invocation Invocation) (context.Context, *Guard, error) {
	if err := contract.Validate(); err != nil {
		return ctx, nil, err
	}
	if invocation.Surface == "" {
		return ctx, nil, fmt.Errorf("%w: invocation surface is required", ErrInvocationRejected)
	}
	if claimed := strings.TrimSpace(invocation.OperationID); claimed != "" && claimed != contract.OperationID {
		return ctx, nil, fmt.Errorf("%w: %w: claimed %q, dispatched %q", ErrInvocationRejected, ErrOperationMismatch, claimed, contract.OperationID)
	}
	if !contract.Exposes(invocation.Surface) {
		return ctx, nil, fmt.Errorf("%w: %w: operation %q does not expose %q", ErrInvocationRejected, ErrSurfaceNotExposed, contract.OperationID, invocation.Surface)
	}
	if contract.Target != nil && strings.TrimSpace(invocation.TargetValues[contract.Target.Parameter]) == "" {
		return ctx, nil, fmt.Errorf("%w: %w: operation %q target parameter %q", ErrInvocationRejected, ErrTargetRequired, contract.OperationID, contract.Target.Parameter)
	}
	if contract.Idempotency == IdempotencyRequired && strings.TrimSpace(invocation.IdempotencyKey) == "" {
		return ctx, nil, fmt.Errorf("%w: %w: operation %q", ErrInvocationRejected, ErrIdempotencyRequired, contract.OperationID)
	}
	if contract.Concurrency == ConcurrencyIfMatch && strings.TrimSpace(invocation.ConcurrencyToken) == "" {
		return ctx, nil, fmt.Errorf("%w: %w: operation %q requires If-Match", ErrInvocationRejected, ErrPreconditionRequired, contract.OperationID)
	}
	started, guard := beginValidated(ctx, contract, invocation)
	return started, guard, nil
}

func begin(ctx context.Context, contract Contract, invocation Invocation) (context.Context, *Guard, error) {
	if err := contract.Validate(); err != nil {
		return ctx, nil, err
	}
	started, guard := beginValidated(ctx, contract, invocation)
	return started, guard, nil
}

func beginValidated(ctx context.Context, contract Contract, invocation Invocation) (context.Context, *Guard) {
	state := &executionState{contract: contract, invocation: invocation}
	if contract.Concurrency == ConcurrencyNone {
		state.concurrencyChecked.Store(true)
	}
	return context.WithValue(ctx, executionContextKey{}, state), &Guard{state: state}
}

func (g *Guard) Completed() bool {
	return g != nil && g.state != nil && g.state.completed.Load() && g.state.concurrencyChecked.Load()
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

// CheckConcurrency applies the generated concurrency policy. Callers supply
// the current canonical revision from inside the same transaction as the
// mutation; the executor owns parsing and comparison semantics.
func (e *Executor) CheckConcurrency(ctx context.Context, operationID, presented, current string) error {
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
	if contract.Concurrency != ConcurrencyIfMatch {
		return fmt.Errorf("%w: operation %q does not declare a concurrency policy", ErrInvalidContract, operationID)
	}
	presented = strings.TrimSpace(presented)
	if presented == "" {
		return fmt.Errorf("%w: operation %q requires If-Match", ErrPreconditionRequired, operationID)
	}
	current = strings.TrimSpace(current)
	matched := false
	for _, candidate := range strings.Split(presented, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || (candidate != "" && !strings.HasPrefix(candidate, "W/") && candidate == current) {
			matched = true
			break
		}
	}
	if !matched {
		return fmt.Errorf("%w: operation %q revision does not match", ErrPreconditionFailed, operationID)
	}
	state, ok := ctx.Value(executionContextKey{}).(*executionState)
	if ok && state != nil && state.contract.OperationID == contract.OperationID {
		state.concurrencyChecked.Store(true)
	}
	return nil
}
