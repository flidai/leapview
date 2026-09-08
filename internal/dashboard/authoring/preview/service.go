// Package preview provides the read-only application boundary for rendering an
// exact project dashboard draft. A preview is deliberately not an authoring
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
	"github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
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
	Get(context.Context, graph.ResourceID, authoring.DashboardID) (authoring.DashboardLifecycle, error)
	GetRevision(context.Context, graph.ResourceID, authoring.DashboardID, authoring.RevisionID) (authoring.Revision, error)
}

// Runtime is the immutable capability required from one active runtime lease.
// The concrete active implementation is dashboard/runtime.Service. Keeping
// this interface local prevents preview from reaching into runtime internals or
// opening another data runtime.
type CompileRuntime interface {
	Close() error
	SemanticModelProjection(graph.ResourceID) (*semanticmodel.Model, bool)
}

type Runtime interface {
	CompileRuntime
	QueryDashboardPageForDefinition(context.Context, dashboarddefinition.Definition, string, dashboard.Filters) (dashboard.Patch, error)
}

// Lease is the exact project-generation capability used by preview. It is
// deliberately narrower than a host lease so preview cannot resolve another
// project or acquire another generation behind the caller's back.
type Lease = projectruntime.Lease

// PreviewProvider is the lease-bearing runtime boundary. Provider.Acquire is
// called exactly once for each successful preview attempt that reaches runtime.
type PreviewProvider interface {
	Acquire(context.Context) (projectruntime.Lease, error)
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
	ProjectID        graph.ResourceID
	ActorID          string
	DashboardID      authoring.DashboardID
	DraftID          authoring.DraftID
	ExpectedRevision authoring.RevisionToken
	PageID           string
	Filters          dashboard.Filters
	// BestEffortVisuals is reserved for the interactive builder. It isolates
	// visual lowering failures while keeping strict structural, semantic,
	// filter, and layout validation. Headless preview and publish remain strict.
	BestEffortVisuals bool
}

// CompileRequest identifies one exact draft revision without selecting or
// executing a page. It exists for consumers such as filter-option loading
// that need the immutable filter contract but must not depend on the health
// of unrelated visual queries.
type CompileRequest struct {
	ProjectID        graph.ResourceID
	ActorID          string
	DashboardID      authoring.DashboardID
	DraftID          authoring.DraftID
	ExpectedRevision authoring.RevisionToken
}

// SemanticServingStateEvidence binds the returned draft output to the exact
// active runtime generation and semantic model used for compilation/query.
type SemanticServingStateEvidence struct {
	SemanticModel      string                `json:"semanticModel"`
	RuntimeModel       string                `json:"runtimeModel"`
	Identity           graph.ServingIdentity `json:"identity"`
	DuckLakeSnapshotID int64                 `json:"duckLakeSnapshotId"`
}

// Preview is the successful read-only preview result. Revision is the exact
// immutable authored token; Definition and PagePatch are compiler/runtime
// outputs, while Evidence is diagnostic identity for the model/runtime pair.
type Preview struct {
	Revision         authoring.RevisionToken        `json:"revision"`
	Definition       dashboarddefinition.Definition `json:"definition"`
	PagePatch        dashboard.Patch                `json:"pagePatch"`
	SemanticEvidence SemanticServingStateEvidence   `json:"semanticEvidence"`
	VisualErrors     map[string]string              `json:"visualErrors,omitempty"`
}

// Compilation is the exact-revision, compile-only draft result. It carries
// the same serving-state evidence as Preview but intentionally has no page
// patch because no dashboard consumer query has run.
type Compilation struct {
	Revision         authoring.RevisionToken        `json:"revision"`
	Definition       dashboarddefinition.Definition `json:"definition"`
	SemanticEvidence SemanticServingStateEvidence   `json:"semanticEvidence"`
	VisualErrors     map[string]string              `json:"visualErrors,omitempty"`
}

type preparedCompilation struct {
	Compilation
	runtime Runtime
	release func()
}

// Compile loads and compiles the filter contract for one exact draft revision
// through a single active runtime lease. It neither lowers unrelated visual
// queries nor executes a dashboard page query.
func (s *Service) Compile(ctx context.Context, request CompileRequest) (Compilation, error) {
	prepared, err := s.prepareCompilation(ctx, request, true, false)
	if err != nil {
		return Compilation{}, err
	}
	defer prepared.release()
	return prepared.Compilation, nil
}

// Preview loads exactly the expected draft revision, strictly compiles it
// against the semantic model from one active runtime lease, and executes the
// requested page through that same runtime and lease. No repository write or
// serving-state operation is reachable from this method.
func (s *Service) Preview(ctx context.Context, request PreviewRequest) (Preview, error) {
	pageID := strings.TrimSpace(request.PageID)
	if pageID == "" {
		return Preview{}, fmt.Errorf("preview page id is required")
	}
	prepared, err := s.prepareCompilation(ctx, CompileRequest{
		ProjectID: request.ProjectID, ActorID: request.ActorID,
		DashboardID: request.DashboardID, DraftID: request.DraftID,
		ExpectedRevision: request.ExpectedRevision,
	}, false, request.BestEffortVisuals)
	if err != nil {
		return Preview{}, err
	}
	defer prepared.release()
	patch, err := prepared.runtime.QueryDashboardPageForDefinition(ctx, prepared.Definition, pageID, request.Filters)
	result := Preview{
		Revision: prepared.Revision, Definition: prepared.Definition,
		PagePatch: patch, SemanticEvidence: prepared.SemanticEvidence, VisualErrors: prepared.VisualErrors,
	}
	if err != nil {
		return result, fmt.Errorf("query dashboard draft page: %w", err)
	}
	return result, nil
}

func (s *Service) prepareCompilation(ctx context.Context, request CompileRequest, filterContractOnly, bestEffortVisuals bool) (preparedCompilation, error) {
	if s == nil || s.repository == nil || s.authorizer == nil || s.provider == nil {
		return preparedCompilation{}, fmt.Errorf("dashboard preview service is not configured")
	}
	projectID, actorID := request.ProjectID, strings.TrimSpace(request.ActorID)
	if err := projectID.Validate(); err != nil || actorID == "" {
		return preparedCompilation{}, fmt.Errorf("project and actor are required")
	}
	if err := authoring.ValidateDashboardID(request.DashboardID); err != nil {
		return preparedCompilation{}, err
	}
	if err := request.DraftID.Validate(); err != nil {
		return preparedCompilation{}, fmt.Errorf("expected preview draft: %w", err)
	}
	if err := request.ExpectedRevision.ValidateComplete(); err != nil {
		return preparedCompilation{}, fmt.Errorf("expected preview revision: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return preparedCompilation{}, err
	}

	// Lifecycle metadata is loaded before authorization, but no draft pointer or
	// revision bytes are returned to the caller. Authorization is the first
	// boundary that can expose any draft state to this operation.
	lifecycle, err := s.repository.Get(ctx, projectID, request.DashboardID)
	if err != nil {
		if errors.Is(err, authoring.ErrNotFound) {
			return preparedCompilation{}, fmt.Errorf("%w: %v", ErrNotFound, err)
		}
		return preparedCompilation{}, err
	}
	if err := s.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
		ActorID: actorID, ProjectID: projectID, DashboardID: request.DashboardID,
		OwnerPrincipalID: lifecycle.OwnerPrincipalID, SemanticModel: lifecycle.SemanticModel,
		Target: authoringservice.AuthorizationTargetAuthoredDashboard, Visibility: lifecycle.Visibility,
		Action: authoring.AuthorizationActionEdit,
	}); err != nil {
		return preparedCompilation{}, err
	}
	if lifecycle.ProjectID != projectID || lifecycle.ID != request.DashboardID {
		return preparedCompilation{}, fmt.Errorf("dashboard preview lifecycle identity does not match request")
	}

	if lifecycle.Status == authoring.LifecycleStatusArchived {
		return preparedCompilation{}, fmt.Errorf("%w: dashboard is archived", ErrNotFound)
	}
	if lifecycle.Status != authoring.LifecycleStatusDraft && lifecycle.Status != authoring.LifecycleStatusPublished {
		return preparedCompilation{}, fmt.Errorf("%w: unsupported lifecycle status %q", ErrNotFound, lifecycle.Status)
	}
	if lifecycle.Draft == nil {
		return preparedCompilation{}, fmt.Errorf("%w: dashboard has no draft", ErrNotFound)
	}
	if lifecycle.Draft.DashboardID != lifecycle.ID {
		return preparedCompilation{}, fmt.Errorf("dashboard preview draft identity does not match lifecycle")
	}
	if lifecycle.Draft.ID != request.DraftID {
		return preparedCompilation{}, fmt.Errorf("%w: expected draft does not match current draft", ErrNotFound)
	}
	if !sameRevision(lifecycle.Draft.Revision, request.ExpectedRevision) {
		return preparedCompilation{}, fmt.Errorf("%w: expected revision does not match current draft", ErrStaleRevision)
	}

	// The revision ID comes only from the already-authorized, exact lifecycle
	// pointer. We never ask the repository for a revision selected by an
	// untrusted token other than after this equality check.
	revision, err := s.repository.GetRevision(ctx, projectID, request.DashboardID, lifecycle.Draft.Revision.RevisionID)
	if err != nil {
		if errors.Is(err, authoring.ErrNotFound) {
			return preparedCompilation{}, fmt.Errorf("%w: draft revision is unavailable", ErrNotFound)
		}
		return preparedCompilation{}, err
	}
	if err := revision.Validate(); err != nil {
		return preparedCompilation{}, fmt.Errorf("validate draft revision: %w", err)
	}
	if revision.DashboardID != request.DashboardID || !sameRevision(revision.Token(), lifecycle.Draft.Revision) {
		return preparedCompilation{}, fmt.Errorf("%w: retained draft revision does not match lifecycle pointer", ErrStaleRevision)
	}
	if revision.Document.Metadata.ID != request.DashboardID.String() {
		return preparedCompilation{}, fmt.Errorf("dashboard preview document identity does not match lifecycle")
	}
	if revision.Document.Spec.SemanticModel != lifecycle.SemanticModel.String() {
		return preparedCompilation{}, fmt.Errorf("%w: draft semantic model does not match lifecycle", ErrSemanticMismatch)
	}

	lease, err := s.provider.Acquire(ctx)
	if err != nil {
		return preparedCompilation{}, err
	}
	if lease == nil {
		return preparedCompilation{}, fmt.Errorf("dashboard preview runtime lease is empty")
	}
	releaseOnError := true
	defer func() {
		if releaseOnError {
			lease.Release()
		}
	}()
	identity := lease.Identity()
	if err := identity.Validate(); err != nil {
		return preparedCompilation{}, fmt.Errorf("dashboard preview serving identity does not match project: %w", err)
	}
	if identity.ProjectID != projectID {
		return preparedCompilation{}, fmt.Errorf("dashboard preview serving identity project %q does not match %q", identity.ProjectID, projectID)
	}
	runtime := lease.Runtime()
	active, activeOK := runtime.(CompileRuntime)
	if !activeOK || active == nil {
		return preparedCompilation{}, fmt.Errorf("active runtime does not provide dashboard draft compilation capability")
	}
	var queryRuntime Runtime
	if !filterContractOnly {
		queryRuntime, activeOK = runtime.(Runtime)
		if !activeOK || queryRuntime == nil {
			return preparedCompilation{}, fmt.Errorf("active runtime does not provide dashboard preview capability")
		}
	}
	model, modelOK := active.SemanticModelProjection(lifecycle.SemanticModel)
	if !modelOK || model == nil {
		return preparedCompilation{}, fmt.Errorf("%w: semantic model %q is unavailable in active runtime", ErrSemanticMismatch, lifecycle.SemanticModel)
	}

	var compiled compiler.DocumentResult
	visualErrors := map[string]string{}
	if filterContractOnly {
		compiled, err = compiler.CompileDocumentFilterContract(revision.Document, map[string]*semanticmodel.Model{lifecycle.SemanticModel.String(): model})
	} else if bestEffortVisuals {
		var builderPreview compiler.BuilderPreviewResult
		builderPreview, err = compiler.CompileDocumentBuilderPreview(revision.Document, map[string]*semanticmodel.Model{lifecycle.SemanticModel.String(): model})
		compiled = builderPreview.DocumentResult
		visualErrors = builderPreview.VisualErrors
	} else {
		compiled, err = compiler.CompileDocument(revision.Document, map[string]*semanticmodel.Model{lifecycle.SemanticModel.String(): model})
	}
	if err != nil {
		return preparedCompilation{}, fmt.Errorf("strictly compile dashboard draft: %w", err)
	}
	if compiled.Definition.ID != request.DashboardID.String() || compiled.Definition.ID != revision.Document.Metadata.ID {
		return preparedCompilation{}, fmt.Errorf("dashboard preview compiled definition identity does not match lifecycle")
	}
	if compiled.Definition.SemanticModel != lifecycle.SemanticModel.String() || compiled.Definition.SemanticModel != revision.Document.Spec.SemanticModel {
		return preparedCompilation{}, fmt.Errorf("%w: compiled semantic model does not match lifecycle", ErrSemanticMismatch)
	}
	snapshotID := int64(0)
	if snapshotRuntime, ok := runtime.(interface{ DuckLakeSnapshotID() int64 }); ok {
		snapshotID = snapshotRuntime.DuckLakeSnapshotID()
	}

	evidence := SemanticServingStateEvidence{
		SemanticModel: lifecycle.SemanticModel.String(), RuntimeModel: model.Name,
		Identity:           identity,
		DuckLakeSnapshotID: snapshotID,
	}
	releaseOnError = false
	return preparedCompilation{
		Compilation: Compilation{Revision: revision.Token(), Definition: compiled.Definition, SemanticEvidence: evidence, VisualErrors: visualErrors},
		runtime:     queryRuntime, release: lease.Release,
	}, nil
}

func sameRevision(left, right authoring.RevisionToken) bool {
	return left.RevisionID == right.RevisionID && left.Number == right.Number && left.ContentHash == right.ContentHash
}
