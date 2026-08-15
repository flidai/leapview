package application

import (
	"context"
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/runtimehost"
)

// IntentRequest is the transport-neutral application input for builder
// mutations. The command itself carries the exact dashboard/draft/revision,
// command ID, and provenance; WorkspaceID and ActorID are kept alongside it
// so an HTTP or agent adapter cannot smuggle an alternate scope.
type IntentRequest struct {
	WorkspaceID string
	ActorID     string
	Command     authoring.Command
}

// ExecuteIntent is the single application mutation entrypoint for bounded
// dashboard-builder intents. It delegates persistence to the transactional
// authoring service and performs active semantic-model validation only for an
// AssignField intent. No authored document is accepted from a transport.
func (a *Application) ExecuteIntent(ctx context.Context, request IntentRequest) (authoringservice.Result, error) {
	if err := a.validate(); err != nil {
		return authoringservice.Result{}, err
	}
	workspace, err := workspaceID(request.WorkspaceID)
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
	if request.Command.AssignField != nil {
		field := request.Command.AssignField
		validator = func(ctx context.Context, lifecycle authoring.DashboardLifecycle) error {
			return a.validateAssignedField(ctx, workspace, request.Command, lifecycle, field)
		}
	}
	return a.authoring.ExecuteValidated(ctx, workspace, request.Command, validator)
}

// validateAssignedField resolves the exact current draft/component and then
// checks the field against the semantic model projection from one active
// runtime lease. The lease is released before the transactional edit begins;
// the reducer remains the final authority for optimistic revision and exact
// placement checks.
func (a *Application) validateAssignedField(ctx context.Context, workspace string, command authoring.Command, lifecycle authoring.DashboardLifecycle, field *authoring.AssignFieldPayload) error {
	if err := command.Validate(); err != nil {
		return err
	}
	if lifecycle.WorkspaceID != workspace || lifecycle.ID != command.DashboardID {
		return fmt.Errorf("dashboard intent lifecycle identity does not match request")
	}
	if err := lifecycle.Validate(); err != nil {
		return fmt.Errorf("validate dashboard intent lifecycle: %w", err)
	}
	if lifecycle.Draft == nil || lifecycle.Draft.ID != command.DraftID {
		return fmt.Errorf("%w: intent draft does not match current draft", authoring.ErrStaleRevision)
	}
	if !sameRevision(lifecycle.Draft.Revision, command.ExpectedRevision) {
		return fmt.Errorf("%w: intent expected revision does not match current draft", authoring.ErrStaleRevision)
	}
	revision, err := a.repository.GetRevision(ctx, workspace, command.DashboardID, command.ExpectedRevision.RevisionID)
	if err != nil {
		return err
	}
	if err := revision.Validate(); err != nil {
		return fmt.Errorf("validate dashboard intent revision: %w", err)
	}
	if revision.DashboardID != command.DashboardID || !sameRevision(revision.Token(), command.ExpectedRevision) {
		return fmt.Errorf("%w: intent revision identity does not match request", authoring.ErrStaleRevision)
	}
	if strings.TrimSpace(revision.Document.SemanticModel) != strings.TrimSpace(lifecycle.SemanticModel) {
		return fmt.Errorf("dashboard intent semantic model does not match lifecycle")
	}
	var componentVisual string
	for _, page := range revision.Document.Pages {
		if page.ID != field.PageID {
			continue
		}
		for _, component := range page.Visuals {
			if component.ID == field.VisualID {
				componentVisual = component.Visual
				break
			}
		}
		break
	}
	if componentVisual == "" {
		return fmt.Errorf("%w: visual component %q on page %q", authoring.ErrNotFound, field.VisualID, field.PageID)
	}
	authored, ok := revision.Document.Visuals[componentVisual]
	if !ok {
		return fmt.Errorf("%w: visual definition %q", authoring.ErrNotFound, componentVisual)
	}

	lease, err := a.acquireRuntime(ctx, workspace)
	if err != nil {
		return err
	}
	if lease == nil {
		return fmt.Errorf("dashboard intent runtime lease is empty")
	}
	defer lease.Release()
	if strings.TrimSpace(string(lease.ServingStateID())) == "" {
		return fmt.Errorf("dashboard intent serving-state identity is empty")
	}
	if lease.Runtime() == nil {
		return fmt.Errorf("dashboard intent runtime is empty")
	}
	active, ok := lease.Runtime().(interface {
		runtimehost.Runtime
		SemanticModelProjection(string) (*semanticmodel.Model, bool)
	})
	if !ok || active == nil {
		return fmt.Errorf("active runtime does not provide semantic model projection")
	}
	model, ok := active.SemanticModelProjection(revision.Document.SemanticModel)
	if !ok || model == nil || strings.TrimSpace(model.Name) != strings.TrimSpace(revision.Document.SemanticModel) {
		return fmt.Errorf("semantic model %q is unavailable in active runtime", revision.Document.SemanticModel)
	}
	if err := validateGovernedField(model, field.FieldID, field.Role); err != nil {
		return err
	}
	// Keep this derived identity out of the wire payload and command
	// fingerprint. It is only used by the reducer after the authoritative
	// semantic-model validation above succeeds.
	field.ResolvedTable = resolvedTableForField(model, authored, *field)
	return nil
}

// resolvedTableForField returns a fact/table identity only when the governed
// semantic field has one unambiguous physical owner. Semantic dimensions may
// bind to multiple facts, so they deliberately leave the table unset and let
// the existing compiler relationship validation decide whether the authored
// query is valid.
func resolvedTableForField(model *semanticmodel.Model, authored authoring.AuthoringVisualization, field authoring.AssignFieldPayload) string {
	if authored.Tabular == nil || strings.TrimSpace(authored.Tabular.Query.Table) != "" {
		return ""
	}
	switch field.Role {
	case authoring.FieldRoleMeasure:
		measure, err := model.ResolveMeasure(strings.TrimSpace(field.FieldID))
		if err == nil {
			return strings.TrimSpace(measure.Fact)
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
	case authoring.FieldRoleMeasure:
		if _, _, kind, err := model.ResolveField(field); err == nil && kind == "measure" {
			return nil
		}
		if _, ok := model.Metrics[field]; ok {
			return nil
		}
		return fmt.Errorf("%w: governed measure %q does not exist", authoring.ErrInvalidPayload, field)
	case authoring.FieldRoleDimension, authoring.FieldRoleDetail:
		if err := model.ValidateQueryDimension(field); err != nil {
			return fmt.Errorf("%w: governed dimension %q does not exist: %v", authoring.ErrInvalidPayload, field, err)
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported field role %q", authoring.ErrInvalidPayload, role)
	}
}
