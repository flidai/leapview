package application

import (
	"context"
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
)

// IntentRequest is the transport-neutral application input for builder
// mutations. The command itself carries the exact dashboard/draft/revision,
// command ID, and provenance; ProjectID and ActorID are kept alongside it
// so an HTTP or agent adapter cannot smuggle an alternate scope.
type IntentRequest struct {
	ProjectID projectgraph.ResourceID
	ActorID   string
	Command   authoring.Command
}

// ExecuteIntent is the single application mutation entrypoint for bounded
// dashboard-builder intents. It delegates persistence to the transactional
// authoring service and performs active semantic-model validation for every
// field-binding mutation. No authored document is accepted from a transport.
func (a *Application) ExecuteIntent(ctx context.Context, request IntentRequest) (authoringservice.Result, error) {
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
	if strings.TrimSpace(request.Command.Provenance.ActorID) != actor {
		return authoringservice.Result{}, fmt.Errorf("command provenance actor does not match request actor")
	}
	if !request.Command.IsBuilderIntent() {
		return authoringservice.Result{}, fmt.Errorf("%w: command is not a dashboard builder intent", authoring.ErrInvalidPayload)
	}
	var validator func(context.Context, authoring.DashboardLifecycle) error
	if request.Command.AddVisual != nil && request.Command.AddVisual.FieldID != "" {
		visual := request.Command.AddVisual
		validator = func(ctx context.Context, lifecycle authoring.DashboardLifecycle) error {
			return a.validateInitialVisualField(ctx, project, request.Command, lifecycle, visual)
		}
	}
	if request.Command.AssignField != nil {
		field := request.Command.AssignField
		validator = func(ctx context.Context, lifecycle authoring.DashboardLifecycle) error {
			return a.validateAssignedField(ctx, project, request.Command, lifecycle, field)
		}
	}
	if request.Command.RemoveField != nil {
		field := request.Command.RemoveField
		validator = func(ctx context.Context, lifecycle authoring.DashboardLifecycle) error {
			return a.validateFieldMutation(ctx, project, request.Command, lifecycle, field.PageID, field.VisualID, field.FieldID, field.Role)
		}
	}
	if request.Command.MoveField != nil {
		field := request.Command.MoveField
		validator = func(ctx context.Context, lifecycle authoring.DashboardLifecycle) error {
			if field.TargetRole != "" && field.TargetRole != field.Role {
				return fmt.Errorf("%w: cross-role field moves are not supported", authoring.ErrInvalidPayload)
			}
			return a.validateFieldMutation(ctx, project, request.Command, lifecycle, field.PageID, field.VisualID, field.FieldID, field.Role)
		}
	}
	return a.authoring.ExecuteValidated(ctx, project, request.Command, validator)
}

func (a *Application) validateInitialVisualField(ctx context.Context, project projectgraph.ResourceID, command authoring.Command, lifecycle authoring.DashboardLifecycle, visual *authoring.AddVisualPayload) error {
	revision, err := a.validateIntentRevision(ctx, project, command, lifecycle)
	if err != nil {
		return err
	}
	pageFound := false
	for _, page := range revision.Document.Spec.Pages {
		if page.ID == visual.PageID {
			pageFound = true
			break
		}
	}
	if !pageFound {
		return fmt.Errorf("%w: page %q", authoring.ErrNotFound, visual.PageID)
	}
	field := authoring.AssignFieldPayload{PageID: visual.PageID, FieldID: visual.FieldID, Role: visual.Role}
	resolvedTable, err := a.validateFieldAgainstRuntime(ctx, project, revision, field)
	if err != nil {
		return err
	}
	visual.ResolvedTable = resolvedTable
	visual.FieldValidated = true
	return nil
}

// validateFieldMutation reuses the governed assignment validator to ensure
// remove/move intents resolve the active semantic model and selected component
// before the reducer mutates the draft. A remove/move never carries the
// assignment-only resolved table back into the command.
func (a *Application) validateFieldMutation(ctx context.Context, project projectgraph.ResourceID, command authoring.Command, lifecycle authoring.DashboardLifecycle, pageID, visualID, fieldID string, role authoring.FieldRole) error {
	assignment := &authoring.AssignFieldPayload{PageID: pageID, VisualID: visualID, FieldID: fieldID, Role: role}
	return a.validateAssignedField(ctx, project, command, lifecycle, assignment)
}

// validateAssignedField resolves the exact current draft/component and then
// checks the field against the semantic model projection from one active
// runtime lease. The lease is released before the transactional edit begins;
// the reducer remains the final authority for optimistic revision and exact
// placement checks.
func (a *Application) validateAssignedField(ctx context.Context, project projectgraph.ResourceID, command authoring.Command, lifecycle authoring.DashboardLifecycle, field *authoring.AssignFieldPayload) error {
	revision, err := a.validateIntentRevision(ctx, project, command, lifecycle)
	if err != nil {
		return err
	}
	var componentVisual string
	for _, page := range revision.Document.Spec.Pages {
		if page.ID != field.PageID {
			continue
		}
		for _, component := range page.Components {
			base, baseErr := component.Base()
			if baseErr != nil {
				return baseErr
			}
			if base.ID == field.VisualID {
				if visual, ok := component.Value.(*document.VisualDashboardPageComponent); ok {
					componentVisual = visual.Visual
				}
				break
			}
		}
		break
	}
	if componentVisual == "" {
		return fmt.Errorf("%w: visual component %q on page %q", authoring.ErrNotFound, field.VisualID, field.PageID)
	}
	if _, ok := revision.Document.Spec.Visuals[componentVisual]; !ok {
		return fmt.Errorf("%w: visual definition %q", authoring.ErrNotFound, componentVisual)
	}
	field.ResolvedTable, err = a.validateFieldAgainstRuntime(ctx, project, revision, *field)
	return err
}

func (a *Application) validateIntentRevision(ctx context.Context, project projectgraph.ResourceID, command authoring.Command, lifecycle authoring.DashboardLifecycle) (authoring.Revision, error) {
	if err := command.Validate(); err != nil {
		return authoring.Revision{}, err
	}
	if lifecycle.ProjectID != project || lifecycle.ID != command.DashboardID {
		return authoring.Revision{}, fmt.Errorf("dashboard intent lifecycle identity does not match request")
	}
	if err := lifecycle.Validate(); err != nil {
		return authoring.Revision{}, fmt.Errorf("validate dashboard intent lifecycle: %w", err)
	}
	if lifecycle.Draft == nil || lifecycle.Draft.ID != command.DraftID {
		return authoring.Revision{}, fmt.Errorf("%w: intent draft does not match current draft", authoring.ErrStaleRevision)
	}
	if !sameRevision(lifecycle.Draft.Revision, command.ExpectedRevision) {
		return authoring.Revision{}, fmt.Errorf("%w: intent expected revision does not match current draft", authoring.ErrStaleRevision)
	}
	revision, err := a.repository.GetRevision(ctx, project, command.DashboardID, command.ExpectedRevision.RevisionID)
	if err != nil {
		return authoring.Revision{}, err
	}
	if err := revision.Validate(); err != nil {
		return authoring.Revision{}, fmt.Errorf("validate dashboard intent revision: %w", err)
	}
	if revision.DashboardID != command.DashboardID || !sameRevision(revision.Token(), command.ExpectedRevision) {
		return authoring.Revision{}, fmt.Errorf("%w: intent revision identity does not match request", authoring.ErrStaleRevision)
	}
	if revision.Document.Spec.SemanticModel != lifecycle.SemanticModel.String() {
		return authoring.Revision{}, fmt.Errorf("dashboard intent semantic model does not match lifecycle")
	}
	return revision, nil
}

func (a *Application) validateFieldAgainstRuntime(ctx context.Context, project projectgraph.ResourceID, revision authoring.Revision, field authoring.AssignFieldPayload) (string, error) {
	lease, err := a.acquireRuntime(ctx)
	if err != nil {
		return "", err
	}
	if lease == nil {
		return "", fmt.Errorf("dashboard intent runtime lease is empty")
	}
	defer lease.Release()
	identity := lease.Identity()
	if err := identity.Validate(); err != nil || identity.GenerationID == "" {
		return "", fmt.Errorf("dashboard intent serving-state identity is empty")
	}
	if lease.Runtime() == nil {
		return "", fmt.Errorf("dashboard intent runtime is empty")
	}
	active, ok := lease.Runtime().(interface {
		runtimehost.Runtime
		SemanticModelProjection(projectgraph.ResourceID) (*semanticmodel.Model, bool)
	})
	if !ok || active == nil {
		return "", fmt.Errorf("active runtime does not provide semantic model projection")
	}
	semanticModelID := projectgraph.ResourceID(revision.Document.Spec.SemanticModel)
	model, ok := active.SemanticModelProjection(semanticModelID)
	if !ok || model == nil {
		return "", fmt.Errorf("semantic model %q is unavailable in active runtime", semanticModelID)
	}
	if err := validateGovernedField(model, field.FieldID, field.Role); err != nil {
		return "", err
	}
	return resolvedTableForField(model, field), nil
}

// resolvedTableForField returns a dataset/table identity only when the governed
// semantic field has one unambiguous physical owner. Semantic dimensions may
// bind to multiple datasets, so they deliberately leave the table unset and let
// the existing compiler relationship validation decide whether the authored
// query is valid.
func resolvedTableForField(model *semanticmodel.Model, field authoring.AssignFieldPayload) string {
	switch field.Role {
	case authoring.FieldRoleMetric:
		if metric, ok := model.Metrics[strings.TrimSpace(field.FieldID)]; ok {
			return strings.TrimSpace(metric.Dataset)
		}
	case authoring.FieldRoleDimension, authoring.FieldRoleDetail:
		dimension, err := model.ResolveDimension(strings.TrimSpace(field.FieldID))
		if err == nil {
			return strings.TrimSpace(dimension.Table)
		}
	}
	return ""
}

func validateGovernedField(model *semanticmodel.Model, field string, role authoring.FieldRole) error {
	field = strings.TrimSpace(field)
	if !authoring.ValidGovernedFieldID(field) {
		return fmt.Errorf("%w: field must be a governed semantic field identifier", authoring.ErrInvalidPayload)
	}
	switch role {
	case authoring.FieldRoleMetric:
		if _, _, kind, err := model.ResolveField(field); err == nil && kind == "metric" {
			return nil
		}
		if _, ok := model.Metrics[field]; ok {
			return nil
		}
		return fmt.Errorf("%w: governed metric %q does not exist", authoring.ErrInvalidPayload, field)
	case authoring.FieldRoleDimension, authoring.FieldRoleDetail:
		if err := model.ValidateQueryDimension(field); err != nil {
			return fmt.Errorf("%w: governed dimension %q does not exist: %v", authoring.ErrInvalidPayload, field, err)
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported field role %q", authoring.ErrInvalidPayload, role)
	}
}
