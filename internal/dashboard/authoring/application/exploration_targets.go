package application

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/builderview"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/document"
	dashsignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ExplorationTargetsRequest selects only authored dashboards which can be
// edited directly. Project/YAML dashboards are intentionally omitted: their
// only supported path is an explicit sourceadapter.Fork operation.
type ExplorationTargetsRequest struct {
	ProjectID     projectgraph.ResourceID
	ActorID       string
	SourceModelID string
}

// ExplorationTargetRequest identifies one target without carrying any source
// exploration fields. This keeps target authorization ahead of model/spec
// inspection and makes the returned revision token opaque to transports.
type ExplorationTargetRequest struct {
	ProjectID   projectgraph.ResourceID
	ActorID     string
	DashboardID authoring.DashboardID
}

type ExplorationTargetPage struct {
	ID        string
	Title     string
	Placement document.DashboardPlacement
}

// ExplorationTarget is a safe picker projection. RevisionToken contains the
// complete CAS token but is deliberately not decomposed into browser fields.
// The list projection intentionally leaves draft/page fields empty; the
// selected-target read fills them after target authorization.
type ExplorationTarget struct {
	ID            string
	Title         string
	SemanticModel string
	DraftID       string
	RevisionToken string
	Pages         []ExplorationTargetPage
}

const maxExplorationTargets = 128

// ExplorationTargets returns authorized, directly editable authored targets.
// This is deliberately a lightweight repository/catalog projection: explorer
// bootstrap must not read every draft or acquire a builder runtime once per
// dashboard. The selected-target request is the only page/CAS read path.
func (a *Application) ExplorationTargets(ctx context.Context, request ExplorationTargetsRequest) ([]ExplorationTarget, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	project, err := projectID(request.ProjectID)
	if err != nil {
		return nil, err
	}
	actor := strings.TrimSpace(request.ActorID)
	if actor == "" {
		return nil, fmt.Errorf("actor id is required")
	}
	lifecycles, err := a.repository.List(ctx, project)
	if err != nil {
		return nil, err
	}
	wantModel := strings.TrimSpace(request.SourceModelID)
	candidates := make([]authoring.DashboardLifecycle, 0, len(lifecycles))
	for _, lifecycle := range lifecycles {
		if lifecycle.ProjectID != project || lifecycle.Status == authoring.LifecycleStatusArchived || (wantModel != "" && lifecycle.SemanticModel.String() != wantModel) {
			continue
		}
		candidates = append(candidates, lifecycle)
	}
	result := make([]ExplorationTarget, 0, len(candidates))
	for _, lifecycle := range candidates {
		target, targetErr := a.explorationTargetMetadata(ctx, project, actor, lifecycle)
		if targetErr != nil {
			// An authorization miss is intentionally indistinguishable from an
			// absent target. Other failures are actionable and must not silently
			// remove a stale or malformed target from the picker.
			if errors.Is(targetErr, access.ErrForbidden) {
				continue
			}
			return nil, targetErr
		}
		result = append(result, target)
	}
	// Apply the bound only after authorization. An actor must not learn how
	// many private dashboards exist, nor have another principal's targets
	// block their own authorized picker.
	if len(result) > maxExplorationTargets {
		return nil, fmt.Errorf("too many authorized compatible authored dashboards; narrow the active semantic model")
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (a *Application) explorationTargetMetadata(ctx context.Context, project projectgraph.ResourceID, actor string, lifecycle authoring.DashboardLifecycle) (ExplorationTarget, error) {
	if lifecycle.ProjectID != project || lifecycle.ID == "" {
		return ExplorationTarget{}, fmt.Errorf("dashboard target lifecycle is incomplete")
	}
	if err := a.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
		ActorID: actor, ProjectID: project, DashboardID: lifecycle.ID,
		OwnerPrincipalID: lifecycle.OwnerPrincipalID, SemanticModel: lifecycle.SemanticModel,
		Target: authoringservice.AuthorizationTargetAuthoredDashboard, Visibility: lifecycle.Visibility,
		Action: authoring.AuthorizationActionEdit,
	}); err != nil {
		return ExplorationTarget{}, err
	}
	if lifecycle.Draft == nil {
		return ExplorationTarget{}, fmt.Errorf("dashboard target lifecycle is incomplete")
	}
	if err := lifecycle.Validate(); err != nil {
		return ExplorationTarget{}, fmt.Errorf("validate dashboard target lifecycle: %w", err)
	}
	if err := a.authorizeSemanticModelUse(ctx, project, actor, lifecycle.ID, lifecycle); err != nil {
		return ExplorationTarget{}, err
	}
	return ExplorationTarget{ID: lifecycle.ID.String(), Title: lifecycle.Title, SemanticModel: lifecycle.SemanticModel.String(), Pages: []ExplorationTargetPage{}}, nil
}

// ExplorationTarget authorizes dashboard edit and semantic-model use before
// calling the existing builder projection. The builder remains the source of
// page/visual read projection and its lease is released within that call.
func (a *Application) ExplorationTarget(ctx context.Context, request ExplorationTargetRequest) (ExplorationTarget, error) {
	if err := a.validate(); err != nil {
		return ExplorationTarget{}, err
	}
	project, err := projectID(request.ProjectID)
	if err != nil {
		return ExplorationTarget{}, err
	}
	actor := strings.TrimSpace(request.ActorID)
	if actor == "" {
		return ExplorationTarget{}, fmt.Errorf("actor id is required")
	}
	if err := authoring.ValidateDashboardID(request.DashboardID); err != nil {
		return ExplorationTarget{}, err
	}
	lifecycle, err := a.repository.Get(ctx, project, request.DashboardID)
	if err != nil {
		return ExplorationTarget{}, err
	}
	if lifecycle.ProjectID != project || lifecycle.ID != request.DashboardID {
		return ExplorationTarget{}, fmt.Errorf("dashboard lifecycle identity does not match request")
	}
	if err := a.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
		ActorID: actor, ProjectID: project, DashboardID: request.DashboardID,
		OwnerPrincipalID: lifecycle.OwnerPrincipalID, SemanticModel: lifecycle.SemanticModel,
		Target: authoringservice.AuthorizationTargetAuthoredDashboard, Visibility: lifecycle.Visibility,
		Action: authoring.AuthorizationActionEdit,
	}); err != nil {
		return ExplorationTarget{}, err
	}
	if err := a.authorizeSemanticModelUse(ctx, project, actor, request.DashboardID, lifecycle); err != nil {
		return ExplorationTarget{}, err
	}
	builder, err := a.Builder(ctx, builderviewRequest(project, actor, request.DashboardID))
	if err != nil {
		return ExplorationTarget{}, err
	}
	if builder.DashboardID != request.DashboardID.String() || builder.SemanticModel.ID != lifecycle.SemanticModel.String() || builder.DraftID == "" {
		return ExplorationTarget{}, fmt.Errorf("dashboard builder identity does not match target")
	}
	token, err := encodeTargetToken(authoring.DraftID(builder.DraftID), authoring.RevisionToken{RevisionID: authoring.RevisionID(builder.Revision.ID), Number: uint64(builder.Revision.Number), ContentHash: builder.Revision.ContentHash})
	if err != nil {
		return ExplorationTarget{}, err
	}
	pages := make([]ExplorationTargetPage, 0, len(builder.Pages))
	for _, page := range builder.Pages {
		placement, placementErr := safePlacementForBuilderPage(page)
		if placementErr != nil {
			return ExplorationTarget{}, placementErr
		}
		pages = append(pages, ExplorationTargetPage{ID: page.ID, Title: page.Title, Placement: placement})
	}
	if len(pages) == 0 {
		return ExplorationTarget{}, fmt.Errorf("dashboard has no editable pages")
	}
	return ExplorationTarget{ID: builder.DashboardID, Title: builder.Title, SemanticModel: builder.SemanticModel.ID, DraftID: builder.DraftID, RevisionToken: token, Pages: pages}, nil
}

func (a *Application) authorizeSemanticModelUse(ctx context.Context, project projectgraph.ResourceID, actor string, dashboardID authoring.DashboardID, lifecycle authoring.DashboardLifecycle) error {
	if err := lifecycle.SemanticModel.Validate(); err != nil {
		return err
	}
	return a.authorizer.Authorize(ctx, authoringservice.AuthorizationRequest{
		ActorID: actor, ProjectID: project, DashboardID: dashboardID, OwnerPrincipalID: lifecycle.OwnerPrincipalID,
		SemanticModel: lifecycle.SemanticModel, Target: authoringservice.AuthorizationTargetSemanticModel,
		Visibility: lifecycle.Visibility, Action: authoring.AuthorizationActionUse,
	})
}

func builderviewRequest(project projectgraph.ResourceID, actor string, dashboardID authoring.DashboardID) builderview.Request {
	return builderview.Request{ProjectID: project, ActorID: actor, DashboardID: dashboardID}
}

func safePlacementForBuilderPage(page dashsignals.DashboardBuilderPageSignal) (document.DashboardPlacement, error) {
	columns := int64(12)
	if page.Grid.Columns > 0 {
		columns = page.Grid.Columns
	}
	if columns > math.MaxInt32 {
		return document.DashboardPlacement{}, fmt.Errorf("dashboard page %q grid columns exceed placement range", page.ID)
	}
	// The append picker calls this the half-width default; use the same
	// rounded grid width as safePlacementForDocumentPage so its preview and
	// persisted placement agree on non-12-column pages.
	span := (columns + 1) / 2
	if columns < span {
		span = columns
	}
	if span < 1 {
		return document.DashboardPlacement{}, fmt.Errorf("dashboard page %q has no usable grid columns", page.ID)
	}
	row := int64(1)
	for _, visual := range page.Visuals {
		end := int64(visual.Placement.Row) + int64(visual.Placement.RowSpan)
		if end < int64(visual.Placement.Row) || end >= math.MaxInt32 {
			return document.DashboardPlacement{}, fmt.Errorf("dashboard page %q contains an overflowing placement", page.ID)
		}
		if end+1 > row {
			row = end + 1
		}
	}
	if row < 1 || row+4 > math.MaxInt32 {
		return document.DashboardPlacement{}, fmt.Errorf("dashboard page %q has no safe append row", page.ID)
	}
	return document.DashboardPlacement{Column: 1, ColumnSpan: int32(span), Row: int32(row), RowSpan: 4}, nil
}

func pageByID(value document.DashboardDocument, pageID string) (document.DashboardPage, bool) {
	pageID = strings.TrimSpace(pageID)
	for _, page := range value.Spec.Pages {
		if page.ID == pageID {
			return page, true
		}
	}
	return document.DashboardPage{}, false
}

func safePlacementForDocumentPage(page document.DashboardPage, choices ...string) (document.DashboardPlacement, error) {
	columns := int64(12)
	if page.Layout != nil && page.Layout.Columns != nil && *page.Layout.Columns > 0 {
		columns = int64(*page.Layout.Columns)
	}
	choice := "half"
	if len(choices) > 0 && strings.TrimSpace(choices[0]) != "" {
		choice = strings.TrimSpace(choices[0])
	}
	var span int64
	switch choice {
	case "half":
		// Round up so odd grids still receive the closest available half.
		span = (columns + 1) / 2
		if columns < span {
			span = columns
		}
	case "full":
		span = columns
	default:
		return document.DashboardPlacement{}, fmt.Errorf("unsupported exploration placement choice %q", choice)
	}
	if span < 1 {
		return document.DashboardPlacement{}, fmt.Errorf("dashboard page %q has no usable grid columns", page.ID)
	}
	row := int64(1)
	for _, component := range page.Components {
		base, err := component.Base()
		if err != nil {
			return document.DashboardPlacement{}, err
		}
		if base.Placement.Column < 1 || base.Placement.Row < 1 || base.Placement.ColumnSpan < 1 || base.Placement.RowSpan < 1 {
			return document.DashboardPlacement{}, fmt.Errorf("dashboard page %q contains an invalid placement", page.ID)
		}
		end := int64(base.Placement.Row) + int64(base.Placement.RowSpan)
		if end >= math.MaxInt32 {
			return document.DashboardPlacement{}, fmt.Errorf("dashboard page %q contains an overflowing placement", page.ID)
		}
		if end+1 > row {
			row = end + 1
		}
	}
	if row < 1 || row+4 > math.MaxInt32 {
		return document.DashboardPlacement{}, fmt.Errorf("dashboard page %q has no safe append row", page.ID)
	}
	return document.DashboardPlacement{Column: 1, Row: int32(row), ColumnSpan: int32(span), RowSpan: 4}, nil
}

type explorationTargetToken struct {
	DraftID  authoring.DraftID       `json:"draftId"`
	Revision authoring.RevisionToken `json:"revision"`
}

func encodeTargetToken(draftID authoring.DraftID, revision authoring.RevisionToken) (string, error) {
	if err := draftID.Validate(); err != nil {
		return "", err
	}
	if err := revision.ValidateComplete(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(explorationTargetToken{DraftID: draftID, Revision: revision})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeTargetToken(value string) (authoring.DraftID, authoring.RevisionToken, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return "", authoring.RevisionToken{}, fmt.Errorf("revision token is invalid: %w", err)
	}
	var token explorationTargetToken
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&token); err != nil {
		return "", authoring.RevisionToken{}, fmt.Errorf("revision token is invalid: %w", err)
	}
	if err := token.DraftID.Validate(); err != nil {
		return "", authoring.RevisionToken{}, err
	}
	if err := token.Revision.ValidateComplete(); err != nil {
		return "", authoring.RevisionToken{}, err
	}
	return token.DraftID, token.Revision, nil
}
