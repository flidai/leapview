package module

import (
	"context"
	"errors"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

// SavedExplorationUICommandBindings is the analytics-owned identity surface
// projected into the authenticated project browser. These bindings are
// generated from the REST command contracts, so browser actions cannot drift
// into an unregistered operation identity.
type SavedExplorationUICommandBindings struct {
	Create    uicommand.Binding
	Update    uicommand.Binding
	Duplicate uicommand.Binding
	Archive   uicommand.Binding
}

// SavedExplorationUICommandInvocation is the browser-to-generated-command
// boundary. Revision deliberately retains all token members; a revision
// number alone cannot attest an exact CAS precondition.
type SavedExplorationUICommandInvocation struct {
	Action              string
	Project             string
	Resource            string
	IdempotencyKey      string
	RequestID           string
	CorrelationID       string
	Revision            saved.RevisionToken
	ConcurrencyRevision *saved.RevisionToken
}

func (*Module) SavedExplorationUICommandBindings() SavedExplorationUICommandBindings {
	return SavedExplorationUICommandBindings{
		Create:    analyticsgen.GenUIActionCreateSavedExploration(),
		Update:    analyticsgen.GenUIActionUpdateSavedExploration(),
		Duplicate: analyticsgen.GenUIActionDuplicateSavedExploration(),
		Archive:   analyticsgen.GenUIActionArchiveSavedExploration(),
	}
}

// BeginSavedExplorationUICommand applies the generated exposure,
// idempotency, and exact-token requirements before browser dispatch.
func (*Module) BeginSavedExplorationUICommand(ctx context.Context, invocation SavedExplorationUICommandInvocation) (context.Context, error) {
	surface := apigencommand.SurfaceUI
	switch invocation.Action {
	case "create":
		started, _, err := analyticsgen.BeginGenCreateSavedExplorationCommand(ctx, analyticsgen.GenCreateSavedExplorationCommandInvocation{
			Surface: surface, Project: invocation.Project, IdempotencyKey: invocation.IdempotencyKey,
			RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		})
		return started, err
	case "update":
		concurrencyToken, err := savedExplorationConcurrencyToken(invocation.Revision, true)
		if err != nil {
			return ctx, err
		}
		started, _, err := analyticsgen.BeginGenUpdateSavedExplorationCommand(ctx, analyticsgen.GenUpdateSavedExplorationCommandInvocation{
			Surface: surface, Project: invocation.Project, IdempotencyKey: invocation.IdempotencyKey,
			ConcurrencyToken: concurrencyToken, RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		})
		return started, err
	case "duplicate":
		concurrencyToken, err := savedExplorationConcurrencyToken(invocation.Revision, true)
		if err != nil {
			return ctx, err
		}
		started, _, err := analyticsgen.BeginGenDuplicateSavedExplorationCommand(ctx, analyticsgen.GenDuplicateSavedExplorationCommandInvocation{
			Surface: surface, Project: invocation.Project, IdempotencyKey: invocation.IdempotencyKey,
			ConcurrencyToken: concurrencyToken, RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		})
		return started, err
	case "archive":
		concurrencyToken, err := savedExplorationConcurrencyToken(invocation.Revision, true)
		if err != nil {
			return ctx, err
		}
		started, _, err := analyticsgen.BeginGenArchiveSavedExplorationCommand(ctx, analyticsgen.GenArchiveSavedExplorationCommandInvocation{
			Surface: surface, Project: invocation.Project, IdempotencyKey: invocation.IdempotencyKey,
			ConcurrencyToken: concurrencyToken, RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		})
		return started, err
	default:
		return ctx, errors.New("unsupported saved exploration UI command")
	}
}

// ExecuteSavedExplorationUICommand runs the generated transactional command
// around the application-service callback. For CAS operations, attestation
// uses the exact pre-write token returned by the service's mutation result.
// The pointer is populated by the browser callback after its service call;
// SQLite remains the authoritative same-transaction CAS guard.
func (*Module) ExecuteSavedExplorationUICommand(ctx context.Context, invocation SavedExplorationUICommandInvocation, transaction func(context.Context) error) error {
	executor, err := apigencommand.NewExecutor(analyticsgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	var concurrencyToken string
	var canonicalConcurrencyToken string
	if invocation.Action != "create" {
		concurrencyToken, err = savedExplorationConcurrencyToken(invocation.Revision, true)
		if err != nil {
			return err
		}
		if invocation.ConcurrencyRevision == nil {
			return saved.ErrInvalidRevision
		}
	}
	execution := apigencommand.Execution{Transactional: func(ctx context.Context, _ apigencommand.Contract) error {
		if transaction == nil {
			return errors.New("saved exploration transaction is required")
		}
		if err := transaction(ctx); err != nil {
			return err
		}
		if concurrencyToken == "" {
			return nil
		}
		var err error
		canonicalConcurrencyToken, err = savedExplorationConcurrencyToken(*invocation.ConcurrencyRevision, true)
		if err != nil {
			return err
		}
		return savedExplorationCheckConcurrency(ctx, executor, invocation.Action, concurrencyToken, canonicalConcurrencyToken)
	}}
	surface := apigencommand.SurfaceUI
	switch invocation.Action {
	case "create":
		return analyticsgen.ExecuteGenCreateSavedExplorationCommand(ctx, executor, analyticsgen.GenCreateSavedExplorationCommandInvocation{
			Surface: surface, Project: invocation.Project, IdempotencyKey: invocation.IdempotencyKey,
			RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		}, execution)
	case "update":
		return analyticsgen.ExecuteGenUpdateSavedExplorationCommand(ctx, executor, analyticsgen.GenUpdateSavedExplorationCommandInvocation{
			Surface: surface, Project: invocation.Project, IdempotencyKey: invocation.IdempotencyKey,
			ConcurrencyToken: concurrencyToken, RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		}, execution)
	case "duplicate":
		return analyticsgen.ExecuteGenDuplicateSavedExplorationCommand(ctx, executor, analyticsgen.GenDuplicateSavedExplorationCommandInvocation{
			Surface: surface, Project: invocation.Project, IdempotencyKey: invocation.IdempotencyKey,
			ConcurrencyToken: concurrencyToken, RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		}, execution)
	case "archive":
		return analyticsgen.ExecuteGenArchiveSavedExplorationCommand(ctx, executor, analyticsgen.GenArchiveSavedExplorationCommandInvocation{
			Surface: surface, Project: invocation.Project, IdempotencyKey: invocation.IdempotencyKey,
			ConcurrencyToken: concurrencyToken, RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID,
		}, execution)
	default:
		return errors.New("unsupported saved exploration UI command")
	}
}

func savedExplorationConcurrencyToken(token saved.RevisionToken, required bool) (string, error) {
	if token.IsZero() && !required {
		return "", nil
	}
	if token.IsZero() {
		return "", saved.ErrInvalidRevision
	}
	if err := token.ValidateComplete(); err != nil {
		return "", err
	}
	return apigencommand.RevisionToken(token)
}

func savedExplorationCheckConcurrency(ctx context.Context, executor *apigencommand.Executor, action, presented, current string) error {
	switch action {
	case "update":
		return analyticsgen.CheckGenUpdateSavedExplorationCommandConcurrency(ctx, executor, presented, current)
	case "duplicate":
		return analyticsgen.CheckGenDuplicateSavedExplorationCommandConcurrency(ctx, executor, presented, current)
	case "archive":
		return analyticsgen.CheckGenArchiveSavedExplorationCommandConcurrency(ctx, executor, presented, current)
	default:
		return errors.New("saved exploration command has no concurrency policy")
	}
}
