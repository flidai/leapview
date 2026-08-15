package authoring

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/project/graph"
)

// CreateInput seeds a lifecycle with its first complete immutable revision.
// A repository never constructs a partial revision; callers are expected to
// validate the document before crossing this persistence boundary.
type CreateInput struct {
	ProjectID graph.ResourceID
	Lifecycle DashboardLifecycle
	Revision  Revision
	// Operation carries optional durable idempotency evidence for create and
	// fork operations. Repositories that implement CreateOperationRepository
	// persist and enforce it atomically with the lifecycle rows.
	Operation CreateOperation
}

// CreateOperation identifies one create/fork invocation in its actor scope.
// IdempotencyKey is a scoped retry identity and is deliberately separate from
// ToolCallID, which remains provenance evidence. An empty key disables
// idempotency for legacy UI callers that do not provide one. Fingerprint
// covers the normalized request payload and never includes generated
// dashboard, draft, or revision IDs.
type CreateOperation struct {
	ProjectID      graph.ResourceID
	ActorID        string
	Kind           string
	IdempotencyKey string
	ConversationID string
	ToolCallID     string
	Fingerprint    string
}

func (o CreateOperation) Enabled() bool {
	return strings.TrimSpace(o.IdempotencyKey) != ""
}

func (o CreateOperation) Validate() error {
	if !o.Enabled() {
		return nil
	}
	if err := validateResourceID("create operation project id", o.ProjectID); err != nil || strings.TrimSpace(o.ActorID) == "" || o.ActorID != strings.TrimSpace(o.ActorID) {
		return fmt.Errorf("%w: create operation scope is invalid", ErrInvalidAuthoring)
	}
	if o.Kind != "create" && o.Kind != "fork" {
		return fmt.Errorf("%w: unsupported create operation kind %q", ErrInvalidAuthoring, o.Kind)
	}
	if o.IdempotencyKey != strings.TrimSpace(o.IdempotencyKey) || utf8.RuneCountInString(o.IdempotencyKey) < 1 || utf8.RuneCountInString(o.IdempotencyKey) > 200 {
		return fmt.Errorf("%w: create operation idempotency key must be 1-200 characters", ErrInvalidAuthoring)
	}
	if o.ConversationID != strings.TrimSpace(o.ConversationID) || o.ToolCallID != strings.TrimSpace(o.ToolCallID) {
		return fmt.Errorf("%w: create operation provenance cannot have surrounding whitespace", ErrInvalidAuthoring)
	}
	if strings.TrimSpace(o.Fingerprint) == "" || o.Fingerprint != strings.TrimSpace(o.Fingerprint) {
		return fmt.Errorf("%w: create operation fingerprint is required", ErrInvalidAuthoring)
	}
	return nil
}

// CreateOperationResult is the immutable result retained for a replay.
type CreateOperationResult struct {
	DashboardID DashboardID
	Revision    RevisionToken
	// Fingerprint is the immutable payload digest retained with the operation.
	// Callers compare it only after authorizing access to DashboardID.
	Fingerprint string
}

// CreateOperationRepository is an optional extension of Repository. Keeping
// the extension separate preserves lightweight in-memory adapters while the
// SQLite implementation provides durable, transactional idempotency.
type CreateOperationRepository interface {
	LookupCreateOperation(context.Context, CreateOperation) (CreateOperationResult, bool, error)
}

type AppendDraftInput struct {
	ProjectID             graph.ResourceID
	DashboardID           DashboardID
	ExpectedDraftRevision RevisionToken
	Revision              Revision
	Next                  DashboardLifecycle
	Evidence              CommandEvidence
}

// LookupCommandResult is the durable idempotency evidence for one authoring
// command. A command result is immutable: once a command ID has been used, a
// request with a different fingerprint is command reuse rather than a new
// mutation. The revision token is intentionally small so callers can replay a
// prior result without loading or reducing the old document.
type CommandResult struct {
	Revision RevisionToken
}

type PublishInput struct {
	ProjectID             graph.ResourceID
	DashboardID           DashboardID
	ExpectedDraftRevision RevisionToken
	Published             Published
	Compilation           CompiledRevision
	Evidence              CommandEvidence
}

type ArchiveInput struct {
	ProjectID               graph.ResourceID
	DashboardID             DashboardID
	ExpectedCurrentRevision RevisionToken
	Evidence                CommandEvidence
}

// ArchiveInput.ExpectedCurrentRevision is compared with the current draft
// pointer when one exists; otherwise it is compared with the published
// pointer. This makes archive an optimistic, project-scoped transition.

// Repository is the persistence port for dashboard authoring.  Implementations
// own transactionality and project scoping, but never apply edit commands or
// trigger deployment/serving-state transitions.
type Repository interface {
	Create(context.Context, CreateInput) (DashboardLifecycle, error)
	Get(context.Context, graph.ResourceID, DashboardID) (DashboardLifecycle, error)
	List(context.Context, graph.ResourceID) ([]DashboardLifecycle, error)
	CountBySemanticModel(context.Context, graph.ResourceID) ([]SemanticModelUsage, error)
	GetRevision(context.Context, graph.ResourceID, DashboardID, RevisionID) (Revision, error)
	LookupCommandResult(context.Context, graph.ResourceID, DashboardID, CommandEvidence) (CommandResult, bool, error)
	AppendDraft(context.Context, AppendDraftInput) (Revision, error)
	Publish(context.Context, PublishInput) (DashboardLifecycle, error)
	Archive(context.Context, ArchiveInput) (DashboardLifecycle, error)
	GetPublishedCompilation(context.Context, graph.ResourceID, DashboardID) (CompiledRevision, error)
}
