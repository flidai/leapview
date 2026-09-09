package application

import (
	"context"
	"fmt"
	"sort"
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
	if request.Command.SetVisualType != nil {
		visual := request.Command.SetVisualType
		validator = func(ctx context.Context, lifecycle authoring.DashboardLifecycle) error {
			return a.prepareVisualTypeSwitch(ctx, project, request.Command, lifecycle, visual)
		}
	}
	return a.authoring.ExecuteValidated(ctx, project, request.Command, validator)
}

func (a *Application) prepareVisualTypeSwitch(ctx context.Context, project projectgraph.ResourceID, command authoring.Command, lifecycle authoring.DashboardLifecycle, patch *authoring.SetVisualTypePayload) error {
	revision, err := a.validateIntentRevision(ctx, project, command, lifecycle)
	if err != nil {
		return err
	}
	visual, err := visualForIntent(revision.Document, patch.PageID, patch.VisualID)
	if err != nil {
		return err
	}
	model, err := a.semanticModelForRevision(ctx, revision)
	if err != nil {
		return err
	}
	bindings := resolveVisualTypeFieldBindings(model, visual)
	patch.ResolvedBindings = &bindings
	return nil
}

func visualForIntent(doc document.DashboardDocument, pageID, componentID string) (document.DashboardVisual, error) {
	for _, page := range doc.Spec.Pages {
		if page.ID != pageID {
			continue
		}
		for _, component := range page.Components {
			base, err := component.Base()
			if err != nil {
				return document.DashboardVisual{}, err
			}
			placed, ok := component.Value.(*document.VisualDashboardPageComponent)
			if !ok || strings.TrimSpace(placed.Visual) == "" {
				continue
			}
			if base.ID != componentID && placed.Visual != componentID {
				continue
			}
			visual, ok := doc.Spec.Visuals[placed.Visual]
			if !ok {
				return document.DashboardVisual{}, fmt.Errorf("%w: visual definition %q", authoring.ErrNotFound, placed.Visual)
			}
			return visual, nil
		}
		return document.DashboardVisual{}, fmt.Errorf("%w: visual component %q on page %q", authoring.ErrNotFound, componentID, pageID)
	}
	return document.DashboardVisual{}, fmt.Errorf("%w: page %q", authoring.ErrNotFound, pageID)
}

func resolveVisualTypeFieldBindings(model *semanticmodel.Model, visual document.DashboardVisual) authoring.VisualTypeFieldBindings {
	bindings := authoring.VisualTypeFieldBindings{}
	recordsByDataset := map[string][]string{}
	datasetOrder := []string{}
	addRecord := func(dataset, field string) {
		dataset = strings.TrimSpace(dataset)
		field = unqualifiedVisualSwitchField(dataset, field)
		if dataset == "" || field == "" {
			return
		}
		if _, exists := recordsByDataset[dataset]; !exists {
			datasetOrder = append(datasetOrder, dataset)
		}
		recordsByDataset[dataset] = appendUniqueVisualSwitchField(recordsByDataset[dataset], field)
	}
	addDimension := func(id string) {
		id = strings.TrimSpace(id)
		dimension, ok := model.Dimensions[id]
		if !ok {
			return
		}
		bindings.Dimensions = appendUniqueVisualSwitchField(bindings.Dimensions, id)
		datasets := make([]string, 0, len(dimension.Bindings))
		for dataset := range dimension.Bindings {
			datasets = append(datasets, dataset)
		}
		sort.Strings(datasets)
		for _, dataset := range datasets {
			binding := dimension.Bindings[dataset]
			if visualSwitchFieldBelongsToDataset(dataset, binding.Field) {
				addRecord(dataset, binding.Field)
			}
		}
	}
	addMetric := func(id string) {
		id = strings.TrimSpace(id)
		metric, ok := model.Metrics[id]
		if !ok {
			return
		}
		bindings.Metrics = appendUniqueVisualSwitchField(bindings.Metrics, id)
		if metric.Input != nil && visualSwitchFieldBelongsToDataset(metric.Dataset, metric.Input.Field) && unqualifiedVisualSwitchField(metric.Dataset, metric.Input.Field) == id {
			addRecord(metric.Dataset, metric.Input.Field)
		}
	}

	switch query := visual.Query.Value.(type) {
	case *document.AggregateDashboardQuery:
		for _, selection := range query.Dimensions {
			addDimension(visualSwitchDimensionID(selection))
		}
		for _, selection := range query.Metrics {
			addMetric(visualSwitchMetricID(selection))
		}
	case *document.PivotDashboardQuery:
		for _, selection := range append(append([]document.DashboardDimensionSelection{}, query.Rows...), query.Columns...) {
			addDimension(visualSwitchDimensionID(selection))
		}
		for _, selection := range query.Metrics {
			addMetric(visualSwitchMetricID(selection))
		}
	case *document.HistogramDashboardQuery:
		addMetric(visualSwitchMetricID(query.Field))
	case *document.DistributionDashboardQuery:
		addMetric(visualSwitchMetricID(query.Field))
		if query.Group != nil {
			addDimension(visualSwitchDimensionID(*query.Group))
		}
	case *document.RecordsDashboardQuery:
		bindings.Dataset = strings.TrimSpace(query.Dataset)
		for _, selection := range query.Fields {
			field := visualSwitchRecordID(selection)
			if field == "" {
				continue
			}
			bindings.Details = appendUniqueVisualSwitchField(bindings.Details, unqualifiedVisualSwitchField(bindings.Dataset, field))
			if dimension := uniqueSemanticDimensionForRecord(model, bindings.Dataset, field); dimension != "" {
				bindings.Dimensions = appendUniqueVisualSwitchField(bindings.Dimensions, dimension)
			}
			if metric := uniqueSemanticMetricForRecord(model, bindings.Dataset, field); metric != "" {
				bindings.Metrics = appendUniqueVisualSwitchField(bindings.Metrics, metric)
			}
		}
	}

	if bindings.Dataset == "" && len(datasetOrder) > 0 {
		bindings.Dataset = datasetOrder[0]
		for _, dataset := range datasetOrder[1:] {
			if len(recordsByDataset[dataset]) > len(recordsByDataset[bindings.Dataset]) {
				bindings.Dataset = dataset
			}
		}
		bindings.Details = append(bindings.Details, recordsByDataset[bindings.Dataset]...)
	}
	return bindings
}

func uniqueSemanticDimensionForRecord(model *semanticmodel.Model, dataset, field string) string {
	field = unqualifiedVisualSwitchField(dataset, field)
	matches := []string{}
	for id, dimension := range model.Dimensions {
		binding, ok := dimension.Bindings[dataset]
		if ok && visualSwitchFieldBelongsToDataset(dataset, binding.Field) && unqualifiedVisualSwitchField(dataset, binding.Field) == field {
			matches = append(matches, id)
		}
	}
	sort.Strings(matches)
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

func uniqueSemanticMetricForRecord(model *semanticmodel.Model, dataset, field string) string {
	field = unqualifiedVisualSwitchField(dataset, field)
	matches := []string{}
	for id, metric := range model.Metrics {
		if metric.Dataset == dataset && metric.Input != nil && visualSwitchFieldBelongsToDataset(dataset, metric.Input.Field) && unqualifiedVisualSwitchField(dataset, metric.Input.Field) == field && id == field {
			matches = append(matches, id)
		}
	}
	sort.Strings(matches)
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

func visualSwitchFieldBelongsToDataset(dataset, field string) bool {
	dataset = strings.TrimSpace(dataset)
	field = strings.TrimSpace(field)
	if dataset == "" || field == "" {
		return false
	}
	parts := strings.SplitN(field, ".", 2)
	return len(parts) == 1 || parts[0] == dataset
}

func unqualifiedVisualSwitchField(dataset, field string) string {
	field = strings.TrimSpace(field)
	parts := strings.SplitN(field, ".", 2)
	if len(parts) == 2 && (strings.TrimSpace(dataset) == "" || parts[0] == strings.TrimSpace(dataset)) {
		return strings.TrimSpace(parts[1])
	}
	return field
}

func appendUniqueVisualSwitchField(fields []string, field string) []string {
	field = strings.TrimSpace(field)
	if field == "" {
		return fields
	}
	for _, existing := range fields {
		if existing == field {
			return fields
		}
	}
	return append(fields, field)
}

func visualSwitchDimensionID(selection document.DashboardDimensionSelection) string {
	if selection.String != nil {
		return *selection.String
	}
	if selection.Reference != nil {
		return selection.Reference.Dimension
	}
	return ""
}

func visualSwitchMetricID(selection document.DashboardMetricSelection) string {
	if selection.String != nil {
		return *selection.String
	}
	if selection.Reference != nil {
		return selection.Reference.Metric
	}
	return ""
}

func visualSwitchRecordID(selection document.DashboardRecordFieldSelection) string {
	if selection.String != nil {
		return *selection.String
	}
	if selection.Reference != nil {
		return selection.Reference.Field
	}
	return ""
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
	// Records queries store detail selections as unqualified root fields (the
	// dataset is authored once on the query). Validation resolves dimensions
	// against the active model, where physical fields are qualified by table.
	// Qualify only the detached validation copy so remove/move commands retain
	// the exact unqualified field ID required by the reducer.
	validationField := *field
	validationField.FieldID = recordDetailFieldIDForValidation(revision.Document, componentVisual, validationField.FieldID, validationField.Role)
	field.ResolvedTable, err = a.validateFieldAgainstRuntime(ctx, project, revision, validationField)
	return err
}

func recordDetailFieldIDForValidation(doc document.DashboardDocument, visualID, fieldID string, role authoring.FieldRole) string {
	fieldID = strings.TrimSpace(fieldID)
	if role != authoring.FieldRoleDetail || fieldID == "" || strings.Contains(fieldID, ".") {
		return fieldID
	}
	visual, ok := doc.Spec.Visuals[visualID]
	if !ok {
		return fieldID
	}
	records, ok := visual.Query.Value.(*document.RecordsDashboardQuery)
	if !ok {
		return fieldID
	}
	dataset := strings.TrimSpace(records.Dataset)
	if dataset == "" || dataset == "pending_dataset" {
		return fieldID
	}
	return dataset + "." + fieldID
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
	model, err := a.semanticModelForRevision(ctx, revision)
	if err != nil {
		return "", err
	}
	if err := validateGovernedField(model, field.FieldID, field.Role); err != nil {
		return "", err
	}
	return resolvedTableForField(model, field), nil
}

func (a *Application) semanticModelForRevision(ctx context.Context, revision authoring.Revision) (*semanticmodel.Model, error) {
	lease, err := a.acquireRuntime(ctx)
	if err != nil {
		return nil, err
	}
	if lease == nil {
		return nil, fmt.Errorf("dashboard intent runtime lease is empty")
	}
	defer lease.Release()
	identity := lease.Identity()
	if err := identity.Validate(); err != nil || identity.GenerationID == "" {
		return nil, fmt.Errorf("dashboard intent serving-state identity is empty")
	}
	if lease.Runtime() == nil {
		return nil, fmt.Errorf("dashboard intent runtime is empty")
	}
	active, ok := lease.Runtime().(interface {
		runtimehost.Runtime
		SemanticModelProjection(projectgraph.ResourceID) (*semanticmodel.Model, bool)
	})
	if !ok || active == nil {
		return nil, fmt.Errorf("active runtime does not provide semantic model projection")
	}
	semanticModelID := projectgraph.ResourceID(revision.Document.Spec.SemanticModel)
	model, ok := active.SemanticModelProjection(semanticModelID)
	if !ok || model == nil {
		return nil, fmt.Errorf("semantic model %q is unavailable in active runtime", semanticModelID)
	}
	return model, nil
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
