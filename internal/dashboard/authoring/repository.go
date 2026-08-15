package authoring

import (
	"context"
)

// CreateInput seeds a lifecycle with its first complete immutable revision.
// A repository never constructs a partial revision; callers are expected to
// validate the document before crossing this persistence boundary.
type CreateInput struct {
	WorkspaceID string
	Lifecycle   DashboardLifecycle
	Revision    Revision
}

type AppendDraftInput struct {
	WorkspaceID           string
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
	WorkspaceID           string
	DashboardID           DashboardID
	ExpectedDraftRevision RevisionToken
	Published             Published
	Compilation           CompiledRevision
	Evidence              CommandEvidence
}

type ArchiveInput struct {
	WorkspaceID             string
	DashboardID             DashboardID
	ExpectedCurrentRevision RevisionToken
	Evidence                CommandEvidence
}

// ArchiveInput.ExpectedCurrentRevision is compared with the current draft
// pointer when one exists; otherwise it is compared with the published
// pointer. This makes archive an optimistic, workspace-scoped transition.

// Repository is the persistence port for dashboard authoring.  Implementations
// own transactionality and workspace scoping, but never apply edit commands or
// trigger deployment/serving-state transitions.
type Repository interface {
	Create(context.Context, CreateInput) (DashboardLifecycle, error)
	Get(context.Context, string, DashboardID) (DashboardLifecycle, error)
	List(context.Context, string) ([]DashboardLifecycle, error)
	CountBySemanticModel(context.Context, string) ([]SemanticModelUsage, error)
	GetRevision(context.Context, string, DashboardID, RevisionID) (Revision, error)
	LookupCommandResult(context.Context, string, DashboardID, CommandEvidence) (CommandResult, bool, error)
	AppendDraft(context.Context, AppendDraftInput) (Revision, error)
	Publish(context.Context, PublishInput) (DashboardLifecycle, error)
	Archive(context.Context, ArchiveInput) (DashboardLifecycle, error)
	GetPublishedCompilation(context.Context, string, DashboardID) (CompiledRevision, error)
}
