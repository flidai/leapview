package application

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/explorationadapter"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
)

// ExplorationAppendRequest is the closed application command for an atomic
// exploration append. The server re-reads the target draft and derives every
// authored identifier, placement, semantic binding, and compilation fact.
type ExplorationAppendRequest struct {
	ProjectID       projectgraph.ResourceID
	ActorID         string
	DashboardID     authoring.DashboardID
	PageID          string
	RevisionToken   string
	RequestID       string
	PlacementChoice string
	Spec            exploration.ExplorationSpec
}

// AppendExploration reads one authorized current draft, admits the selected
// semantic model and candidate document under one active lease, then submits
// the closed append command through the existing transactional CAS service.
func (a *Application) AppendExploration(ctx context.Context, request ExplorationAppendRequest) (authoringservice.Result, error) {
	if err := a.validate(); err != nil {
		return authoringservice.Result{}, err
	}
	project, err := projectID(request.ProjectID)
	if err != nil {
		return authoringservice.Result{}, err
	}
	actor := strings.TrimSpace(request.ActorID)
	if actor == "" {
		return authoringservice.Result{}, fmt.Errorf("actor id is required")
	}
	if err := authoring.ValidateDashboardID(request.DashboardID); err != nil {
		return authoringservice.Result{}, err
	}
	draftID, expected, err := decodeTargetToken(request.RevisionToken)
	if err != nil {
		return authoringservice.Result{}, err
	}
	requestID := strings.TrimSpace(request.RequestID)
	if requestID == "" || len(requestID) > 256 {
		return authoringservice.Result{}, fmt.Errorf("request id is required and must be at most 256 characters")
	}
	intent := authoringservice.AppendExplorationIntent{
		ProjectID: project, ActorID: actor, DashboardID: request.DashboardID, DraftID: draftID,
		ExpectedRevision: expected, PageID: request.PageID, RequestID: requestID, PlacementChoice: request.PlacementChoice, Spec: request.Spec,
	}
	return a.authoring.AppendExploration(ctx, intent, a.prepareAppendExploration)
}

func appendIDs(requestID string, dashboardID authoring.DashboardID, pageID string) (string, string, authoring.CommandID) {
	seed := requestID + "\x00" + dashboardID.String() + "\x00" + pageID
	digest := sha256.Sum256([]byte(seed))
	encoded := fmt.Sprintf("%x", digest[:12])
	// The service validates RequestID as the canonical UUIDv7 durable key
	// before preparation. Reuse it here rather than deriving a second command
	// identity that could drift from the service's replay lookup.
	return "exploration_" + encoded, "exploration_component_" + encoded, authoring.CommandID(requestID)
}

// prepareAppendExploration performs active model, selected binding, and
// candidate compiler admission after durable replay lookup. Its lease is
// returned to the specialized service, which releases it only after CAS.
func (a *Application) prepareAppendExploration(ctx context.Context, input authoringservice.AppendExplorationIntent, lifecycle authoring.DashboardLifecycle) (authoringservice.AppendExplorationPreparation, error) {
	project := input.ProjectID
	if lifecycle.ProjectID != project || lifecycle.ID != input.DashboardID || lifecycle.Draft == nil || lifecycle.Draft.ID != input.DraftID || !sameRevision(lifecycle.Draft.Revision, input.ExpectedRevision) {
		return authoringservice.AppendExplorationPreparation{}, fmt.Errorf("%w: target draft changed before admission", authoring.ErrStaleRevision)
	}
	if lifecycle.SemanticModel.String() != input.Spec.ModelID {
		return authoringservice.AppendExplorationPreparation{}, fmt.Errorf("%w: exploration model does not match target", authoring.ErrConflict)
	}
	if err := exploration.ValidateShape(&input.Spec); err != nil {
		return authoringservice.AppendExplorationPreparation{}, fmt.Errorf("invalid exploration spec: %w", err)
	}
	if err := a.authorizeSemanticModelUse(ctx, project, input.ActorID, input.DashboardID, lifecycle); err != nil {
		return authoringservice.AppendExplorationPreparation{}, err
	}
	revision, err := a.repository.GetRevision(ctx, project, input.DashboardID, input.ExpectedRevision.RevisionID)
	if err != nil {
		return authoringservice.AppendExplorationPreparation{}, err
	}
	if err := revision.Validate(); err != nil || revision.DashboardID != input.DashboardID || !sameRevision(revision.Token(), input.ExpectedRevision) {
		return authoringservice.AppendExplorationPreparation{}, fmt.Errorf("%w: target draft revision changed before admission", authoring.ErrStaleRevision)
	}
	page, ok := pageByID(revision.Document, input.PageID)
	if !ok {
		return authoringservice.AppendExplorationPreparation{}, fmt.Errorf("%w: page %q", authoring.ErrNotFound, input.PageID)
	}
	placement, err := safePlacementForDocumentPage(page, input.PlacementChoice)
	if err != nil {
		return authoringservice.AppendExplorationPreparation{}, err
	}
	visualID, componentID, _ := appendIDs(input.RequestID, input.DashboardID, page.ID)
	lease, err := a.acquireRuntime(ctx)
	if err != nil {
		return authoringservice.AppendExplorationPreparation{}, err
	}
	if lease == nil {
		return authoringservice.AppendExplorationPreparation{}, fmt.Errorf("dashboard exploration runtime lease is empty")
	}
	released := false
	release := func() {
		if !released {
			released = true
			lease.Release()
		}
	}
	fail := func(err error) (authoringservice.AppendExplorationPreparation, error) {
		release()
		return authoringservice.AppendExplorationPreparation{}, err
	}
	identity := lease.Identity()
	if err := identity.Validate(); err != nil || identity.ProjectID != project || identity.GenerationID == "" {
		return fail(fmt.Errorf("active serving identity does not match project"))
	}
	runtimeValue := lease.Runtime()
	runtimeIdentity, hasRuntimeIdentity := runtimeValue.(interface {
		Identity() projectgraph.ServingIdentity
	})
	if !hasRuntimeIdentity || runtimeIdentity.Identity() != identity {
		return fail(fmt.Errorf("active runtime identity does not match lease identity"))
	}
	active, ok := runtimeValue.(interface {
		runtimehost.Runtime
		SemanticModelProjection(projectgraph.ResourceID) (*semanticmodel.Model, bool)
		CompiledSemanticModel(string) (*semanticquery.CompiledModel, bool)
	})
	if !ok || active == nil {
		return fail(fmt.Errorf("active runtime does not expose semantic model and compiled planner projections"))
	}
	modelID := projectgraph.ResourceID(input.Spec.ModelID)
	model, ok := active.SemanticModelProjection(modelID)
	if !ok || model == nil {
		return fail(fmt.Errorf("semantic model %q is unavailable in active runtime", modelID))
	}
	compiledModel, ok := active.CompiledSemanticModel(modelID.String())
	if !ok || compiledModel == nil || !compiledModel.MatchesModel(model) {
		return fail(fmt.Errorf("active compiled semantic model %q is unavailable or stale", modelID))
	}
	options, err := deriveExplorationAdapterOptions(input.Spec, model, compiledModel, visualID)
	if err != nil {
		return fail(err)
	}
	converted, err := explorationadapter.Convert(input.Spec, options)
	if err != nil {
		return fail(err)
	}
	payload := authoring.AppendExplorationVisualPayload{
		PageID: page.ID, VisualID: visualID, ComponentID: componentID, Placement: placement,
		SemanticModel: input.Spec.ModelID, Visual: converted.Visual, Filters: converted.Filters,
	}
	candidate, err := authoring.CandidateAppendExplorationDocument(revision.Document, payload)
	if err != nil {
		return fail(err)
	}
	compiled, err := compiler.CompileDocument(candidate, map[string]*semanticmodel.Model{modelID.String(): model})
	if err != nil {
		return fail(fmt.Errorf("compile appended exploration visual: %w", err))
	}
	if compiled.Definition.ID != candidate.Metadata.ID || compiled.Definition.SemanticModel != modelID.String() || compiled.Definition.SemanticModel != candidate.Spec.SemanticModel {
		return fail(fmt.Errorf("compiled candidate identity does not match target semantic model"))
	}
	_, _, commandID := appendIDs(input.RequestID, input.DashboardID, page.ID)
	return authoringservice.AppendExplorationPreparation{Command: authoring.Command{
		ID: commandID, DashboardID: input.DashboardID, DraftID: input.DraftID, ExpectedRevision: input.ExpectedRevision,
		Provenance:              authoring.Provenance{Origin: authoring.OriginUI, ActorID: input.ActorID},
		AppendExplorationVisual: &authoring.AppendExplorationVisualPayload{PageID: page.ID, VisualID: visualID, ComponentID: componentID, Placement: placement, SemanticModel: input.Spec.ModelID, Visual: converted.Visual, Filters: converted.Filters},
	}, Release: release}, nil
}
