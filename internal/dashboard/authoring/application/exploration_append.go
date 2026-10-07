package application

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/explorationadapter"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/google/uuid"
)

// ExplorationTargetsRequest selects editable authored dashboards for one
// canonical semantic model. Project and YAML dashboard artifacts are not
// direct append targets; they must first be forked into an authored draft.
type ExplorationTargetsRequest struct {
	ProjectID     projectgraph.ResourceID
	ActorID       string
	SourceModelID string
}

// ExplorationTargetRequest identifies one target without accepting any
// dashboard document data from the browser.
type ExplorationTargetRequest struct {
	ProjectID     projectgraph.ResourceID
	ActorID       string
	SourceModelID string
	DashboardID   authoring.DashboardID
}

type ExplorationTargetPage struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// ExplorationTarget is a small authorized picker projection. The revision
// token is opaque to the browser and is checked again before the append.
type ExplorationTarget struct {
	ID            string                  `json:"id"`
	Title         string                  `json:"title"`
	SemanticModel string                  `json:"semanticModel"`
	DraftID       string                  `json:"draftId,omitempty"`
	RevisionToken string                  `json:"revisionToken,omitempty"`
	Pages         []ExplorationTargetPage `json:"pages,omitempty"`
}

const maxExplorationTargets = 128

// ExplorationTargets returns only directly editable authored drafts for the
// same semantic model. It does not read any authored document or execute a
// query for the picker.
func (a *Application) ExplorationTargets(ctx context.Context, request ExplorationTargetsRequest) ([]ExplorationTarget, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	project, err := projectID(request.ProjectID)
	if err != nil {
		return nil, err
	}
	actor := strings.TrimSpace(request.ActorID)
	modelID := strings.TrimSpace(request.SourceModelID)
	if actor == "" || modelID == "" {
		return nil, fmt.Errorf("actor and source semantic model are required")
	}
	if err := projectgraph.ResourceID(modelID).Validate(); err != nil {
		return nil, fmt.Errorf("source semantic model is invalid: %w", err)
	}
	if err := a.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
		ActorID: actor, ProjectID: project, SemanticModel: projectgraph.ResourceID(modelID),
		Target: authoringservice.AuthorizationTargetSemanticModel, Action: authoring.AuthorizationActionView,
	}); err != nil {
		return nil, err
	}
	lifecycles, err := a.repository.List(ctx, project)
	if err != nil {
		return nil, err
	}
	result := make([]ExplorationTarget, 0)
	for _, lifecycle := range lifecycles {
		if lifecycle.ProjectID != project || lifecycle.SemanticModel.String() != modelID ||
			lifecycle.Status == authoring.LifecycleStatusArchived || lifecycle.Draft == nil {
			continue
		}
		if err := a.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
			ActorID: actor, ProjectID: project, DashboardID: lifecycle.ID,
			OwnerPrincipalID: lifecycle.OwnerPrincipalID, SemanticModel: lifecycle.SemanticModel,
			Target: authoringservice.AuthorizationTargetAuthoredDashboard, Visibility: lifecycle.Visibility,
			Action: authoring.AuthorizationActionEdit,
		}); err != nil {
			if errors.Is(err, access.ErrForbidden) {
				continue
			}
			return nil, err
		}
		if err := lifecycle.Validate(); err != nil {
			return nil, fmt.Errorf("validate authorized dashboard target: %w", err)
		}
		result = append(result, ExplorationTarget{ID: lifecycle.ID.String(), Title: lifecycle.Title, SemanticModel: modelID})
		if len(result) > maxExplorationTargets {
			return nil, fmt.Errorf("too many authorized dashboard targets")
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// ExplorationTarget returns page choices and the exact current draft token
// only after rechecking edit authorization and model compatibility.
func (a *Application) ExplorationTarget(ctx context.Context, request ExplorationTargetRequest) (ExplorationTarget, error) {
	if err := a.validate(); err != nil {
		return ExplorationTarget{}, err
	}
	project, err := projectID(request.ProjectID)
	if err != nil {
		return ExplorationTarget{}, err
	}
	actor := strings.TrimSpace(request.ActorID)
	modelID := strings.TrimSpace(request.SourceModelID)
	if actor == "" || modelID == "" {
		return ExplorationTarget{}, fmt.Errorf("actor and source semantic model are required")
	}
	if err := projectgraph.ResourceID(modelID).Validate(); err != nil {
		return ExplorationTarget{}, fmt.Errorf("source semantic model is invalid: %w", err)
	}
	if err := a.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
		ActorID: actor, ProjectID: project, SemanticModel: projectgraph.ResourceID(modelID),
		Target: authoringservice.AuthorizationTargetSemanticModel, Action: authoring.AuthorizationActionView,
	}); err != nil {
		return ExplorationTarget{}, err
	}
	if err := authoring.ValidateDashboardID(request.DashboardID); err != nil {
		return ExplorationTarget{}, err
	}
	lifecycle, err := a.repository.Get(ctx, project, request.DashboardID)
	if err != nil {
		return ExplorationTarget{}, err
	}
	if lifecycle.ProjectID != project || lifecycle.ID != request.DashboardID {
		return ExplorationTarget{}, fmt.Errorf("dashboard target identity does not match request")
	}
	if err := a.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
		ActorID: actor, ProjectID: project, DashboardID: lifecycle.ID,
		OwnerPrincipalID: lifecycle.OwnerPrincipalID, SemanticModel: lifecycle.SemanticModel,
		Target: authoringservice.AuthorizationTargetAuthoredDashboard, Visibility: lifecycle.Visibility,
		Action: authoring.AuthorizationActionEdit,
	}); err != nil {
		return ExplorationTarget{}, err
	}
	if lifecycle.SemanticModel.String() != modelID {
		return ExplorationTarget{}, fmt.Errorf("%w: dashboard semantic model does not match exploration", authoring.ErrConflict)
	}
	if lifecycle.Draft == nil || lifecycle.Status == authoring.LifecycleStatusArchived {
		return ExplorationTarget{}, fmt.Errorf("%w: dashboard has no editable draft", authoring.ErrConflict)
	}
	revision, err := a.repository.GetRevision(ctx, project, lifecycle.ID, lifecycle.Draft.Revision.RevisionID)
	if err != nil {
		return ExplorationTarget{}, err
	}
	if err := revision.Validate(); err != nil {
		return ExplorationTarget{}, err
	}
	if revision.DashboardID != lifecycle.ID || !sameRevision(revision.Token(), lifecycle.Draft.Revision) {
		return ExplorationTarget{}, fmt.Errorf("%w: dashboard draft revision changed", authoring.ErrStaleRevision)
	}
	token, err := encodeExplorationTargetToken(lifecycle.Draft.ID, revision.Token())
	if err != nil {
		return ExplorationTarget{}, err
	}
	pages := make([]ExplorationTargetPage, 0, len(revision.Document.Spec.Pages))
	for _, page := range revision.Document.Spec.Pages {
		pages = append(pages, ExplorationTargetPage{ID: page.ID, Title: page.Title})
	}
	if len(pages) == 0 {
		return ExplorationTarget{}, fmt.Errorf("dashboard has no pages")
	}
	return ExplorationTarget{
		ID: lifecycle.ID.String(), Title: lifecycle.Title, SemanticModel: modelID,
		DraftID: lifecycle.Draft.ID.String(), RevisionToken: token, Pages: pages,
	}, nil
}

type ExplorationAppendRequest struct {
	ProjectID       projectgraph.ResourceID
	ActorID         string
	DashboardID     authoring.DashboardID
	PageID          string
	RevisionToken   string
	IdempotencyKey  string
	PlacementChoice string
	Spec            exploration.ExplorationSpec
}

// AppendExploration copies a canonical Explorer spec into one independently
// authored dashboard visual. It uses the active semantic model for field
// resolution, validates the candidate with the dashboard compiler, then
// appends the immutable document revision through the ordinary authoring CAS.
func (a *Application) AppendExploration(ctx context.Context, request ExplorationAppendRequest) (authoringservice.Result, error) {
	if err := a.validate(); err != nil {
		return authoringservice.Result{}, err
	}
	project, err := projectID(request.ProjectID)
	if err != nil {
		return authoringservice.Result{}, err
	}
	actor := strings.TrimSpace(request.ActorID)
	idempotencyKey := strings.TrimSpace(request.IdempotencyKey)
	pageID := strings.TrimSpace(request.PageID)
	if actor == "" || pageID == "" || idempotencyKey == "" || len(idempotencyKey) > 256 {
		return authoringservice.Result{}, fmt.Errorf("actor, page, and bounded idempotency key are required")
	}
	if !canonicalUUIDv7(idempotencyKey) {
		return authoringservice.Result{}, fmt.Errorf("dashboard append idempotency key must be a canonical UUIDv7")
	}
	if err := authoring.ValidateDashboardID(request.DashboardID); err != nil {
		return authoringservice.Result{}, err
	}
	if err := exploration.ValidateShape(&request.Spec); err != nil {
		return authoringservice.Result{}, fmt.Errorf("invalid canonical exploration: %w", err)
	}
	draftID, expected, err := decodeExplorationTargetToken(request.RevisionToken)
	if err != nil {
		return authoringservice.Result{}, err
	}

	lifecycle, err := a.repository.Get(ctx, project, request.DashboardID)
	if err != nil {
		return authoringservice.Result{}, err
	}
	if lifecycle.ProjectID != project || lifecycle.ID != request.DashboardID {
		return authoringservice.Result{}, fmt.Errorf("dashboard target identity does not match request")
	}
	if err := a.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
		ActorID: actor, ProjectID: project, DashboardID: lifecycle.ID,
		OwnerPrincipalID: lifecycle.OwnerPrincipalID, SemanticModel: lifecycle.SemanticModel,
		Target: authoringservice.AuthorizationTargetAuthoredDashboard, Visibility: lifecycle.Visibility,
		Action: authoring.AuthorizationActionEdit,
	}); err != nil {
		return authoringservice.Result{}, err
	}
	if lifecycle.Draft == nil || lifecycle.Status == authoring.LifecycleStatusArchived ||
		lifecycle.Draft.ID != draftID || !sameRevision(lifecycle.Draft.Revision, expected) {
		return authoringservice.Result{}, fmt.Errorf("%w: target draft changed; refresh dashboard targets", authoring.ErrStaleRevision)
	}
	if lifecycle.SemanticModel.String() != request.Spec.ModelID {
		return authoringservice.Result{}, fmt.Errorf("%w: exploration model does not match dashboard", authoring.ErrConflict)
	}
	if err := a.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
		ActorID: actor, ProjectID: project, SemanticModel: lifecycle.SemanticModel,
		Target: authoringservice.AuthorizationTargetSemanticModel, Action: authoring.AuthorizationActionView,
	}); err != nil {
		return authoringservice.Result{}, err
	}
	revision, err := a.repository.GetRevision(ctx, project, lifecycle.ID, expected.RevisionID)
	if err != nil {
		return authoringservice.Result{}, err
	}
	if err := revision.Validate(); err != nil {
		return authoringservice.Result{}, err
	}
	if revision.DashboardID != lifecycle.ID || !sameRevision(revision.Token(), expected) {
		return authoringservice.Result{}, fmt.Errorf("%w: target draft revision changed", authoring.ErrStaleRevision)
	}
	pageIndex := explorationPageIndex(revision.Document, pageID)
	if pageIndex < 0 {
		return authoringservice.Result{}, fmt.Errorf("%w: page %q", authoring.ErrNotFound, pageID)
	}

	lease, model, compiledModel, activeIdentity, err := a.acquireExplorationModel(ctx, project, projectgraph.ResourceID(request.Spec.ModelID))
	if err != nil {
		return authoringservice.Result{}, err
	}
	defer lease.Release()
	visualID, componentID, commandID := explorationAppendIDs(idempotencyKey, request.DashboardID, pageID)
	options, err := deriveExplorationAdapterOptions(request.Spec, model, compiledModel, visualID)
	if err != nil {
		return authoringservice.Result{}, err
	}
	converted, err := explorationadapter.Convert(request.Spec, options)
	if err != nil {
		return authoringservice.Result{}, err
	}
	placement, err := explorationAppendPlacement(revision.Document, pageIndex, request.PlacementChoice, converted.Visual.Type)
	if err != nil {
		return authoringservice.Result{}, err
	}
	candidate, err := revision.Document.Clone()
	if err != nil {
		return authoringservice.Result{}, err
	}
	if candidate.Spec.Visuals == nil {
		candidate.Spec.Visuals = map[string]document.DashboardVisual{}
	}
	if _, exists := candidate.Spec.Visuals[visualID]; exists {
		return authoringservice.Result{}, fmt.Errorf("%w: generated visual id already exists", authoring.ErrConflict)
	}
	candidate.Spec.Visuals[visualID] = converted.Visual
	candidate.Spec.Pages[pageIndex].Components = append(candidate.Spec.Pages[pageIndex].Components, document.DashboardPageComponent{
		Value: &document.VisualDashboardPageComponent{
			DashboardPageComponentBase: document.DashboardPageComponentBase{ID: componentID, Type: "visual", Placement: placement},
			Type:                       "visual", Visual: visualID,
		},
	})
	candidate.Spec.Filters = append(candidate.Spec.Filters, converted.Filters...)
	if err := authoring.ValidateCanonicalDocument(candidate); err != nil {
		return authoringservice.Result{}, fmt.Errorf("validate appended dashboard document: %w", err)
	}
	if a.compiler == nil {
		return authoringservice.Result{}, fmt.Errorf("dashboard candidate compiler is unavailable")
	}
	compilation, err := a.compiler.Compile(ctx, project, lifecycle.SemanticModel, candidate)
	if err != nil {
		return authoringservice.Result{}, fmt.Errorf("compile appended dashboard visual: %w", err)
	}
	if compilation.Definition.ID != candidate.Metadata.ID ||
		compilation.Definition.SemanticModel != lifecycle.SemanticModel.String() ||
		compilation.SemanticIdentity != activeIdentity {
		return authoringservice.Result{}, fmt.Errorf("compiled candidate identity does not match active dashboard model")
	}
	if err := compilation.SemanticIdentity.Validate(); err != nil {
		return authoringservice.Result{}, fmt.Errorf("compiled candidate identity is invalid: %w", err)
	}

	return a.Execute(ctx, project, authoring.Command{
		ID: commandID, DashboardID: request.DashboardID, DraftID: draftID, ExpectedRevision: expected,
		Provenance:      authoring.Provenance{Origin: authoring.OriginUI, ActorID: actor},
		ReplaceDocument: &authoring.ReplaceDocumentPayload{Document: candidate},
	})
}

func (a *Application) acquireExplorationModel(ctx context.Context, project, modelID projectgraph.ResourceID) (runtimehost.Lease, *semanticmodel.Model, *semanticquery.CompiledModel, projectgraph.ServingIdentity, error) {
	if a.acquireRuntime == nil {
		return nil, nil, nil, projectgraph.ServingIdentity{}, fmt.Errorf("dashboard runtime provider is unavailable")
	}
	lease, err := a.acquireRuntime(ctx)
	if err != nil {
		return nil, nil, nil, projectgraph.ServingIdentity{}, err
	}
	if lease == nil || lease.Runtime() == nil {
		if lease != nil {
			lease.Release()
		}
		return nil, nil, nil, projectgraph.ServingIdentity{}, fmt.Errorf("active dashboard runtime lease is empty")
	}
	identity := lease.Identity()
	if err := identity.Validate(); err != nil || identity.ProjectID != project || identity.GenerationID == "" {
		lease.Release()
		return nil, nil, nil, projectgraph.ServingIdentity{}, fmt.Errorf("active dashboard runtime identity does not match project")
	}
	activeIdentity, ok := lease.Runtime().(interface {
		Identity() projectgraph.ServingIdentity
	})
	if !ok || activeIdentity.Identity() != identity {
		lease.Release()
		return nil, nil, nil, projectgraph.ServingIdentity{}, fmt.Errorf("active runtime identity does not match serving lease")
	}
	active, ok := lease.Runtime().(interface {
		runtimehost.Runtime
		SemanticModelProjection(projectgraph.ResourceID) (*semanticmodel.Model, bool)
		CompiledSemanticModel(string) (*semanticquery.CompiledModel, bool)
	})
	if !ok {
		lease.Release()
		return nil, nil, nil, projectgraph.ServingIdentity{}, fmt.Errorf("active runtime does not expose governed semantic projections")
	}
	model, ok := active.SemanticModelProjection(modelID)
	if !ok || model == nil {
		lease.Release()
		return nil, nil, nil, projectgraph.ServingIdentity{}, fmt.Errorf("semantic model %q is not active", modelID)
	}
	compiled, ok := active.CompiledSemanticModel(modelID.String())
	if !ok || compiled == nil || !compiled.MatchesModel(model) {
		lease.Release()
		return nil, nil, nil, projectgraph.ServingIdentity{}, fmt.Errorf("compiled semantic model %q is unavailable or stale", modelID)
	}
	return lease, model, compiled, identity, nil
}

type explorationTargetToken struct {
	DraftID  string                  `json:"draftId"`
	Revision authoring.RevisionToken `json:"revision"`
}

func encodeExplorationTargetToken(draft authoring.DraftID, revision authoring.RevisionToken) (string, error) {
	if err := draft.Validate(); err != nil {
		return "", err
	}
	if err := revision.ValidateComplete(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(explorationTargetToken{DraftID: draft.String(), Revision: revision})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeExplorationTargetToken(value string) (authoring.DraftID, authoring.RevisionToken, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return "", authoring.RevisionToken{}, fmt.Errorf("dashboard revision token is invalid")
	}
	var token explorationTargetToken
	if err := json.Unmarshal(raw, &token); err != nil {
		return "", authoring.RevisionToken{}, fmt.Errorf("dashboard revision token is invalid")
	}
	draft := authoring.DraftID(token.DraftID)
	if err := draft.Validate(); err != nil {
		return "", authoring.RevisionToken{}, err
	}
	if err := token.Revision.ValidateComplete(); err != nil {
		return "", authoring.RevisionToken{}, err
	}
	return draft, token.Revision, nil
}

func explorationAppendIDs(idempotencyKey string, dashboardID authoring.DashboardID, pageID string) (string, string, authoring.CommandID) {
	seed := idempotencyKey + "\x00" + dashboardID.String() + "\x00" + pageID
	digest := sha256.Sum256([]byte(seed))
	visualID := "explore_" + hex.EncodeToString(digest[:12])
	componentID := "explore_component_" + hex.EncodeToString(digest[:12])
	// Durable dashboard command identities are native UUIDv7 values. The
	// The browser's Idempotency-Key is already a UUIDv7 and is stable for a
	// retry, so use it directly rather than deriving a non-native token.
	return visualID, componentID, authoring.CommandID(idempotencyKey)
}

func canonicalUUIDv7(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.Version() == 7 && parsed.String() == value
}

func explorationPageIndex(value document.DashboardDocument, pageID string) int {
	for index, page := range value.Spec.Pages {
		if page.ID == pageID {
			return index
		}
	}
	return -1
}

func explorationAppendPlacement(value document.DashboardDocument, pageIndex int, choice string, visualType document.DashboardVisualType) (document.DashboardPlacement, error) {
	if pageIndex < 0 || pageIndex >= len(value.Spec.Pages) {
		return document.DashboardPlacement{}, fmt.Errorf("dashboard page is unavailable")
	}
	columns := int32(12)
	if value.Spec.Layout != nil && value.Spec.Layout.Columns > 0 {
		columns = value.Spec.Layout.Columns
	}
	page := value.Spec.Pages[pageIndex]
	if page.Layout != nil && page.Layout.Columns != nil {
		if *page.Layout.Columns <= 0 {
			return document.DashboardPlacement{}, fmt.Errorf("dashboard page grid columns are invalid")
		}
		columns = *page.Layout.Columns
	}
	span := columns
	switch strings.TrimSpace(choice) {
	case "full":
	case "half":
		span = (columns + 1) / 2
	default:
		return document.DashboardPlacement{}, fmt.Errorf("unsupported dashboard tile width")
	}
	rowSpan := int32(4)
	if visualType == document.DashboardVisualTypeTable || visualType == document.DashboardVisualTypeMatrix || visualType == document.DashboardVisualTypePivot {
		rowSpan = 5
	}
	row := int64(1)
	for _, component := range page.Components {
		base, err := component.Base()
		if err != nil || base == nil {
			return document.DashboardPlacement{}, fmt.Errorf("dashboard page contains an invalid component placement")
		}
		end := int64(base.Placement.Row) + int64(base.Placement.RowSpan)
		if base.Placement.Row <= 0 || base.Placement.RowSpan <= 0 || end > math.MaxInt32 {
			return document.DashboardPlacement{}, fmt.Errorf("dashboard page component placement exceeds coordinate bounds")
		}
		if end > row {
			row = end
		}
	}
	if row+int64(rowSpan) > math.MaxInt32 {
		return document.DashboardPlacement{}, fmt.Errorf("dashboard page has no safe space for another visual")
	}
	return document.DashboardPlacement{Column: 1, Row: int32(row), ColumnSpan: span, RowSpan: rowSpan}, nil
}
