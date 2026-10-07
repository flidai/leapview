package application

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
)

// LookupChatVisualCopyReplay recovers an atomic copy-and-add result before the
// transport consults a source that may have changed or disappeared. DashboardID
// identifies the original source; authorization is against the retained copy.
func (a *Application) LookupChatVisualCopyReplay(ctx context.Context, request AddChatVisualRequest) (authoringservice.Result, bool, error) {
	if err := a.validate(); err != nil {
		return authoringservice.Result{}, false, err
	}
	if err := request.CommandID.Validate(); err != nil {
		return authoringservice.Result{}, false, err
	}
	if err := request.DashboardID.Validate(); err != nil {
		return authoringservice.Result{}, false, err
	}
	operation := authoring.CreateOperation{ProjectID: request.ProjectID, ActorID: request.ActorID, Kind: "fork", IdempotencyKey: string(request.CommandID)}
	if err := operation.ValidateKey(); err != nil {
		return authoringservice.Result{}, false, err
	}
	repository, ok := a.repository.(authoring.CreateOperationRepository)
	if !ok {
		return authoringservice.Result{}, false, fmt.Errorf("dashboard authoring repository does not support create idempotency")
	}
	stored, found, err := repository.LookupCreateOperation(ctx, operation)
	if err != nil || !found {
		return authoringservice.Result{}, false, err
	}
	target, err := a.repository.Get(ctx, request.ProjectID, stored.DashboardID)
	if err != nil {
		return authoringservice.Result{}, false, err
	}
	if target.ProjectID != request.ProjectID || target.ID != stored.DashboardID {
		return authoringservice.Result{}, false, fmt.Errorf("%w: retained copy identity does not match request", authoring.ErrInvalidAuthoring)
	}
	if err := a.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{ActorID: request.ActorID, ProjectID: request.ProjectID, DashboardID: target.ID, OwnerPrincipalID: target.OwnerPrincipalID, SemanticModel: target.SemanticModel, Target: authoringservice.AuthorizationTargetAuthoredDashboard, Visibility: target.Visibility, Action: authoring.AuthorizationActionEdit}); err != nil {
		return authoringservice.Result{}, false, err
	}
	revision, err := a.repository.GetRevision(ctx, request.ProjectID, target.ID, stored.Revision.RevisionID)
	if err != nil {
		return authoringservice.Result{}, false, err
	}
	if err := revision.Validate(); err != nil {
		return authoringservice.Result{}, false, err
	}
	if revision.DashboardID != target.ID || !sameRevision(revision.Token(), stored.Revision) ||
		!sameChatVisualProvenance(revision.Provenance, request.Provenance) ||
		!chatCopySourceMatches(revision.Provenance.ForkedFrom, request) ||
		!chatVisualReplayMatches(revision.Document, request.PageID, request.Source, request.CommandID) {
		return authoringservice.Result{}, false, authoring.ErrCommandReuse
	}
	return authoringservice.Result{Revision: stored.Revision, Lifecycle: target}, true, nil
}

func chatCopySourceMatches(fork *authoring.ForkEvidence, request AddChatVisualRequest) bool {
	if fork == nil {
		return false
	}
	switch fork.Kind {
	case authoring.ForkSourceInstance:
		return fork.Instance != nil && fork.Instance.SourceProjectID == request.ProjectID && fork.Instance.SourceDashboardID == request.DashboardID
	case authoring.ForkSourceProject:
		return fork.Project != nil && fork.Project.SourceProjectID == request.ProjectID && fork.Project.SourceDashboardID == request.DashboardID
	default:
		return false
	}
}
