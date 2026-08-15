// Package preview provides the read-only application boundary for rendering an
// exact workspace dashboard draft. A preview is deliberately not an authoring
// mutation or a serving-state candidate: it reads one immutable revision,
// compiles it in memory, and executes it through the already-active runtime.
package preview

import (
	"context"
	"errors"
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/compiler"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/runtimehost"
)

var (
	// ErrNotFound intentionally covers a missing or archived draft. Callers
	// should not be able to use preview to probe archived lifecycle records.
	ErrNotFound = authoring.ErrNotFound
	// ErrStaleRevision identifies an expected revision token that is no longer
	// the lifecycle's current draft pointer.
	ErrStaleRevision = authoring.ErrStaleRevision
	// ErrSemanticMismatch identifies a model or serving-state identity crossing
	// between authoring and runtime boundaries.
	ErrSemanticMismatch = errors.New("dashboard draft preview semantic identity mismatch")
)

// DraftRepository is intentionally read-only. Keeping this port narrower than
// authoring.Repository makes it impossible for a preview operation to persist,
// publish, activate, or create any authoring/deployment state.
type DraftRepository interface {
	Get(context.Context, string, authoring.DashboardID) (authoring.DashboardLifecycle, error)
	GetRevision(context.Context, string, authoring.DashboardID, authoring.RevisionID) (authoring.Revision, error)
}

// Runtime is the immutable capability required from one active runtime lease.
// The concrete active implementation is dashboard/runtime.Service. Keeping
// this interface local prevents preview from reaching into runtime internals or
// opening another data runtime.
type Runtime interface {
	runtimehost.Runtime
	SemanticModelProjection(string) (*semanticmodel.Model, bool)
	QueryDashboardPageForDefinition(context.Context, dashboarddefinition.Definition, string, dashboard.Filters) (dashboard.Patch, error)
}

// PreviewProvider is the lease-bearing runtime boundary. Provider.Acquire is
// called exactly once for each successful preview attempt that reaches runtime.
type PreviewProvider interface {
	Acquire(context.Context) (runtimehost.Lease, error)
}

// Options wires the read-only preview dependencies.
type Options struct {
	Repository DraftRepository
	Authorizer authoringservice.Authorizer
	Provider   PreviewProvider
}

// Service executes strict, non-persistent dashboard draft previews.
type Service struct {
	repository DraftRepository
	authorizer authoringservice.Authorizer
	provider   PreviewProvider
}

func NewService(options Options) (*Service, error) {
	if options.Repository == nil {
		return nil, fmt.Errorf("dashboard preview repository is required")
	}
	if options.Authorizer == nil {
		return nil, fmt.Errorf("dashboard preview authorizer is required")
	}
	if options.Provider == nil {
		return nil, fmt.Errorf("dashboard preview runtime provider is required")
	}
	return &Service{repository: options.Repository, authorizer: options.Authorizer, provider: options.Provider}, nil
}

// PreviewRequest identifies one exact draft revision and the page to render.
// ExpectedRevision must be complete; the zero token is never interpreted as
// "latest". Filters are passed to the existing definition query path and may
// be empty when the runtime should apply compiled defaults.
type PreviewRequest struct {
	WorkspaceID      string
	ActorID          string
	DashboardID      authoring.DashboardID
	ExpectedRevision authoring.RevisionToken
	PageID           string
	Filters          dashboard.Filters
}

// SemanticServingStateEvidence binds the returned draft output to the exact
// active runtime generation and semantic model used for compilation/query.
type SemanticServingStateEvidence struct {
	SemanticModel      string `json:"semanticModel"`
	RuntimeModel       string `json:"runtimeModel"`
	ServingStateID     string `json:"servingStateId"`
	DuckLakeSnapshotID int64  `json:"duckLakeSnapshotId"`
}

// Preview is the successful read-only preview result. Revision is the exact
// immutable authored token; Definition and PagePatch are compiler/runtime
// outputs, while Evidence is diagnostic identity for the model/runtime pair.
type Preview struct {
	Revision         authoring.RevisionToken        `json:"revision"`
	Definition       dashboarddefinition.Definition `json:"definition"`
	PagePatch        dashboard.Patch                `json:"pagePatch"`
	SemanticEvidence SemanticServingStateEvidence   `json:"semanticEvidence"`
}

// Preview loads exactly the expected draft revision, strictly compiles it
// against the semantic model from one active runtime lease, and executes the
// requested page through that same runtime and lease. No repository write or
// serving-state operation is reachable from this method.
func (s *Service) Preview(ctx context.Context, request PreviewRequest) (Preview, error) {
	if s == nil || s.repository == nil || s.authorizer == nil || s.provider == nil {
		return Preview{}, fmt.Errorf("dashboard preview service is not configured")
	}
	workspaceID, actorID := strings.TrimSpace(request.WorkspaceID), strings.TrimSpace(request.ActorID)
	if workspaceID == "" || actorID == "" {
		return Preview{}, fmt.Errorf("workspace and actor are required")
	}
	if err := request.DashboardID.Validate(); err != nil {
		return Preview{}, err
	}
	if err := request.ExpectedRevision.ValidateComplete(); err != nil {
		return Preview{}, fmt.Errorf("expected preview revision: %w", err)
	}
	pageID := strings.TrimSpace(request.PageID)
	if pageID == "" {
		return Preview{}, fmt.Errorf("preview page id is required")
	}
	if err := ctx.Err(); err != nil {
		return Preview{}, err
	}

	// Lifecycle metadata is loaded before authorization, but no draft pointer or
	// revision bytes are returned to the caller. Authorization is the first
	// boundary that can expose any draft state to this operation.
	lifecycle, err := s.repository.Get(ctx, workspaceID, request.DashboardID)
	if err != nil {
		if errors.Is(err, authoring.ErrNotFound) {
			return Preview{}, fmt.Errorf("%w: %v", ErrNotFound, err)
		}
		return Preview{}, err
	}
	if lifecycle.WorkspaceID != workspaceID || lifecycle.ID != request.DashboardID {
		return Preview{}, fmt.Errorf("dashboard preview lifecycle identity does not match request")
	}
	if err := s.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
		ActorID: actorID, WorkspaceID: workspaceID, DashboardID: request.DashboardID,
		OwnerPrincipalID: lifecycle.OwnerPrincipalID, SemanticModel: lifecycle.SemanticModel,
		Action: authoring.AuthorizationActionEdit,
	}); err != nil {
		return Preview{}, err
	}

	if lifecycle.Status == authoring.LifecycleStatusArchived {
		return Preview{}, fmt.Errorf("%w: dashboard is archived", ErrNotFound)
	}
	if lifecycle.Status != authoring.LifecycleStatusDraft && lifecycle.Status != authoring.LifecycleStatusPublished {
		return Preview{}, fmt.Errorf("%w: unsupported lifecycle status %q", ErrNotFound, lifecycle.Status)
	}
	if lifecycle.Draft == nil {
		return Preview{}, fmt.Errorf("%w: dashboard has no draft", ErrNotFound)
	}
	if lifecycle.Draft.DashboardID != lifecycle.ID {
		return Preview{}, fmt.Errorf("dashboard preview draft identity does not match lifecycle")
	}
	if !sameRevision(lifecycle.Draft.Revision, request.ExpectedRevision) {
		return Preview{}, fmt.Errorf("%w: expected revision does not match current draft", ErrStaleRevision)
	}

	// The revision ID comes only from the already-authorized, exact lifecycle
	// pointer. We never ask the repository for a revision selected by an
	// untrusted token other than after this equality check.
	revision, err := s.repository.GetRevision(ctx, workspaceID, request.DashboardID, lifecycle.Draft.Revision.RevisionID)
	if err != nil {
		if errors.Is(err, authoring.ErrNotFound) {
			return Preview{}, fmt.Errorf("%w: draft revision is unavailable", ErrNotFound)
		}
		return Preview{}, err
	}
	if err := revision.Validate(); err != nil {
		return Preview{}, fmt.Errorf("validate draft revision: %w", err)
	}
	if revision.DashboardID != request.DashboardID || !sameRevision(revision.Token(), lifecycle.Draft.Revision) {
		return Preview{}, fmt.Errorf("%w: retained draft revision does not match lifecycle pointer", ErrStaleRevision)
	}
	if revision.Document.ID != request.DashboardID.String() {
		return Preview{}, fmt.Errorf("dashboard preview document identity does not match lifecycle")
	}
	if strings.TrimSpace(revision.Document.SemanticModel) != strings.TrimSpace(lifecycle.SemanticModel) {
		return Preview{}, fmt.Errorf("%w: draft semantic model does not match lifecycle", ErrSemanticMismatch)
	}

	lease, err := s.provider.Acquire(ctx)
	if err != nil {
		return Preview{}, err
	}
	if lease == nil {
		return Preview{}, fmt.Errorf("dashboard preview runtime lease is empty")
	}
	defer lease.Release()
	active, activeOK := lease.Runtime().(Runtime)
	if !activeOK || active == nil {
		return Preview{}, fmt.Errorf("active runtime does not provide dashboard preview capability")
	}
	model, modelOK := active.SemanticModelProjection(lifecycle.SemanticModel)
	if !modelOK || model == nil {
		return Preview{}, fmt.Errorf("%w: semantic model %q is unavailable in active runtime", ErrSemanticMismatch, lifecycle.SemanticModel)
	}
	if strings.TrimSpace(model.Name) != strings.TrimSpace(lifecycle.SemanticModel) {
		return Preview{}, fmt.Errorf("%w: runtime semantic model %q does not match lifecycle %q", ErrSemanticMismatch, model.Name, lifecycle.SemanticModel)
	}

	compiled, err := compiler.Compile(revision.Document, map[string]*semanticmodel.Model{lifecycle.SemanticModel: model})
	if err != nil {
		return Preview{}, fmt.Errorf("strictly compile dashboard draft: %w", err)
	}
	if compiled.Definition.ID != request.DashboardID.String() || compiled.Definition.ID != revision.Document.ID {
		return Preview{}, fmt.Errorf("dashboard preview compiled definition identity does not match lifecycle")
	}
	if compiled.Definition.SemanticModel != lifecycle.SemanticModel || compiled.Definition.SemanticModel != revision.Document.SemanticModel {
		return Preview{}, fmt.Errorf("%w: compiled semantic model does not match lifecycle", ErrSemanticMismatch)
	}
	servingStateID := strings.TrimSpace(string(lease.ServingStateID()))
	if servingStateID == "" {
		return Preview{}, fmt.Errorf("dashboard preview serving-state identity is unavailable")
	}

	evidence := SemanticServingStateEvidence{
		SemanticModel: lifecycle.SemanticModel, RuntimeModel: model.Name,
		ServingStateID:     servingStateID,
		DuckLakeSnapshotID: lease.DuckLakeSnapshotID(),
	}
	patch, err := active.QueryDashboardPageForDefinition(ctx, compiled.Definition, pageID, request.Filters)
	result := Preview{Revision: revision.Token(), Definition: compiled.Definition, PagePatch: patch, SemanticEvidence: evidence}
	if err != nil {
		return result, fmt.Errorf("query dashboard draft page: %w", err)
	}
	return result, nil
}

func sameRevision(left, right authoring.RevisionToken) bool {
	return left.RevisionID == right.RevisionID && left.Number == right.Number && left.ContentHash == right.ContentHash
}
