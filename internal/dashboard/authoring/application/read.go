package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
)

// DraftRequest identifies the current immutable draft revision for one
// workspace dashboard. Draft reads are edit-authorized because they expose
// private authored content.
type DraftRequest struct {
	WorkspaceID string
	ActorID     string
	DashboardID authoring.DashboardID
}

// DraftRead is the lifecycle pointer and exact retained revision returned by
// a governed draft read. Both values are detached from repository storage.
type DraftRead struct {
	Lifecycle authoring.DashboardLifecycle
	Revision  authoring.Revision
}

// RevisionRequest identifies one immutable authored revision. Action is
// explicit so transports cannot accidentally expose a draft through a VIEW
// only path.
type RevisionRequest struct {
	WorkspaceID string
	ActorID     string
	DashboardID authoring.DashboardID
	DraftID     authoring.DraftID
	RevisionID  authoring.RevisionID
	Action      authoring.AuthorizationAction
}

// CreateFromDocument keeps the complete-document create operation on the
// application facade. HTTP and other transports never need to reach through
// to the transactional service directly.
func (a *Application) CreateFromDocument(ctx context.Context, request authoringservice.CreateFromDocumentRequest) (authoringservice.Result, error) {
	if err := a.validate(); err != nil {
		return authoringservice.Result{}, err
	}
	workspaceID, err := workspaceID(request.WorkspaceID)
	if err != nil {
		return authoringservice.Result{}, err
	}
	request.WorkspaceID = workspaceID
	return a.authoring.CreateFromDocument(ctx, request)
}

// Draft loads the current draft pointer and exact retained revision after an
// EDIT authorization decision. It intentionally never interprets a missing
// draft as "latest published".
func (a *Application) Draft(ctx context.Context, request DraftRequest) (DraftRead, error) {
	if err := a.validate(); err != nil {
		return DraftRead{}, err
	}
	workspaceID, err := workspaceID(request.WorkspaceID)
	if err != nil {
		return DraftRead{}, err
	}
	if strings.TrimSpace(request.ActorID) == "" {
		return DraftRead{}, fmt.Errorf("actor id is required")
	}
	if err := request.DashboardID.Validate(); err != nil {
		return DraftRead{}, err
	}
	lifecycle, err := a.repository.Get(ctx, workspaceID, request.DashboardID)
	if err != nil {
		return DraftRead{}, err
	}
	if err := a.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
		ActorID: request.ActorID, WorkspaceID: workspaceID, DashboardID: request.DashboardID,
		OwnerPrincipalID: lifecycle.OwnerPrincipalID, SemanticModel: lifecycle.SemanticModel,
		Action: authoring.AuthorizationActionEdit,
	}); err != nil {
		return DraftRead{}, err
	}
	if lifecycle.WorkspaceID != workspaceID || lifecycle.ID != request.DashboardID {
		return DraftRead{}, fmt.Errorf("dashboard draft lifecycle identity does not match request")
	}
	if err := lifecycle.Validate(); err != nil {
		return DraftRead{}, fmt.Errorf("validate dashboard draft lifecycle: %w", err)
	}
	if lifecycle.Status == authoring.LifecycleStatusArchived || lifecycle.Draft == nil {
		return DraftRead{}, fmt.Errorf("%w: dashboard has no draft", authoring.ErrNotFound)
	}
	if lifecycle.Draft.DashboardID != lifecycle.ID {
		return DraftRead{}, fmt.Errorf("dashboard draft identity does not match lifecycle")
	}
	if err := lifecycle.Draft.Revision.ValidateComplete(); err != nil {
		return DraftRead{}, fmt.Errorf("validate current draft pointer: %w", err)
	}
	revision, err := a.repository.GetRevision(ctx, workspaceID, request.DashboardID, lifecycle.Draft.Revision.RevisionID)
	if err != nil {
		return DraftRead{}, err
	}
	if err := revision.Validate(); err != nil {
		return DraftRead{}, fmt.Errorf("validate dashboard draft revision: %w", err)
	}
	if revision.DashboardID != request.DashboardID || !sameRevision(revision.Token(), lifecycle.Draft.Revision) {
		return DraftRead{}, fmt.Errorf("%w: draft revision does not match lifecycle pointer", authoring.ErrStaleRevision)
	}
	return DraftRead{Lifecycle: lifecycle, Revision: revision}, nil
}

// Revision loads an exact immutable revision after the requested action is
// authorized. Historical revisions are allowed for EDIT callers; published
// revisions may be read with VIEW.
func (a *Application) Revision(ctx context.Context, request RevisionRequest) (authoring.Revision, error) {
	if err := a.validate(); err != nil {
		return authoring.Revision{}, err
	}
	workspaceID, err := workspaceID(request.WorkspaceID)
	if err != nil {
		return authoring.Revision{}, err
	}
	if strings.TrimSpace(request.ActorID) == "" {
		return authoring.Revision{}, fmt.Errorf("actor id is required")
	}
	if err := request.DashboardID.Validate(); err != nil {
		return authoring.Revision{}, err
	}
	if err := request.RevisionID.Validate(); err != nil {
		return authoring.Revision{}, err
	}
	if request.Action != authoring.AuthorizationActionView && request.Action != authoring.AuthorizationActionEdit {
		return authoring.Revision{}, fmt.Errorf("%w: revision reads require view or edit authorization", authoring.ErrInvalidAuthoring)
	}
	if err := request.Action.Validate(); err != nil {
		return authoring.Revision{}, err
	}
	if strings.TrimSpace(string(request.DraftID)) != "" {
		if err := request.DraftID.Validate(); err != nil {
			return authoring.Revision{}, err
		}
	}
	lifecycle, err := a.repository.Get(ctx, workspaceID, request.DashboardID)
	if err != nil {
		return authoring.Revision{}, err
	}
	if err := a.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
		ActorID: request.ActorID, WorkspaceID: workspaceID, DashboardID: request.DashboardID,
		OwnerPrincipalID: lifecycle.OwnerPrincipalID, SemanticModel: lifecycle.SemanticModel,
		Action: request.Action,
	}); err != nil {
		return authoring.Revision{}, err
	}
	if lifecycle.WorkspaceID != workspaceID || lifecycle.ID != request.DashboardID {
		return authoring.Revision{}, fmt.Errorf("dashboard revision lifecycle identity does not match request")
	}
	if err := lifecycle.Validate(); err != nil {
		return authoring.Revision{}, fmt.Errorf("validate dashboard revision lifecycle: %w", err)
	}
	if lifecycle.Status == authoring.LifecycleStatusArchived {
		return authoring.Revision{}, fmt.Errorf("%w: dashboard is archived", authoring.ErrNotFound)
	}
	if request.Action == authoring.AuthorizationActionEdit {
		if strings.TrimSpace(string(request.DraftID)) == "" || lifecycle.Draft == nil || lifecycle.Draft.ID != request.DraftID || lifecycle.Draft.Revision.RevisionID != request.RevisionID {
			return authoring.Revision{}, fmt.Errorf("%w: revision is not the exact current draft revision", authoring.ErrNotFound)
		}
	} else if strings.TrimSpace(string(request.DraftID)) != "" || lifecycle.Published == nil || lifecycle.Published.Revision.RevisionID != request.RevisionID {
		return authoring.Revision{}, fmt.Errorf("%w: revision is not the exact published dashboard revision", authoring.ErrNotFound)
	}
	revision, err := a.repository.GetRevision(ctx, workspaceID, request.DashboardID, request.RevisionID)
	if err != nil {
		if errors.Is(err, authoring.ErrNotFound) {
			return authoring.Revision{}, fmt.Errorf("%w: revision is unavailable", authoring.ErrNotFound)
		}
		return authoring.Revision{}, err
	}
	if err := revision.Validate(); err != nil {
		return authoring.Revision{}, fmt.Errorf("validate dashboard revision: %w", err)
	}
	if revision.DashboardID != request.DashboardID {
		return authoring.Revision{}, fmt.Errorf("dashboard revision identity does not match request")
	}
	return revision, nil
}

func sameRevision(left, right authoring.RevisionToken) bool {
	return left.RevisionID == right.RevisionID && left.Number == right.Number && left.ContentHash == right.ContentHash
}
