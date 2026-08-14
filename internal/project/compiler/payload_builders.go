package compiler

import (
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/project/manifest"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/workspace"
)

func catalogPayload(definition *manifest.Workspace) catalogPayloadV1 {
	return catalogPayloadV1{
		Workspace: catalogWorkspacePayloadV1{
			ID:          definition.Catalog.Workspace.ID,
			Title:       workspaceTitle(definition.Catalog.Workspace.Title, definition.Catalog.Workspace.ID),
			Description: definition.Catalog.Workspace.Description,
		},
		SemanticModels: catalogModelsPayload(definition.Catalog.SemanticModels),
		Dashboards:     catalogDashboardsPayload(definition.Catalog.Dashboards),
	}
}

func catalogModelsPayload(models []manifest.CatalogModel) []catalogModelPayloadV1 {
	out := make([]catalogModelPayloadV1, 0, len(models))
	for _, model := range models {
		out = append(out, catalogModelPayloadV1{
			ID:          model.ID,
			Title:       model.Title,
			Path:        model.Path,
			Description: model.Description,
		})
	}
	return out
}

func catalogDashboardsPayload(dashboards []manifest.CatalogDashboard) []catalogDashboardPayloadV1 {
	out := make([]catalogDashboardPayloadV1, 0, len(dashboards))
	for _, dashboard := range dashboards {
		out = append(out, catalogDashboardPayloadV1{
			ID:          dashboard.ID,
			Title:       dashboard.Title,
			Path:        dashboard.Path,
			Description: dashboard.Description,
			Tags:        dashboard.Tags,
			Appearance:  dashboard.Appearance,
		})
	}
	return out
}

func connectionPayload(connection semanticmodel.Connection) connectionPayloadV1 {
	return connectionPayloadV1{
		Kind:     connection.Kind,
		Path:     connection.Path,
		Root:     connection.Root,
		Scope:    connection.Scope,
		Options:  connection.Options,
		Defaults: connectionDefaultsPayloadV1{Options: connection.Defaults.Options},
	}
}

func sourcePayload(source semanticmodel.Source) sourcePayloadV1 {
	return sourcePayloadV1{
		Format:     source.Format,
		Path:       source.Path,
		Connection: source.Connection,
		Object:     source.Object,
		Options:    source.Options,
		Fields:     sourceFieldsPayload(source.Fields),
		Schema:     schemaPayload(source.Schema),
	}
}

func sourceFieldsPayload(fields map[string]semanticmodel.SourceField) map[string]sourceFieldPayloadV1 {
	out := map[string]sourceFieldPayloadV1{}
	for _, name := range sortedMapKeys(fields) {
		field := fields[name]
		out[name] = sourceFieldPayloadV1{Name: field.Name, Field: field.Field, Table: field.Table, Type: field.Type, Description: field.Description}
	}
	return out
}

func modelTablePayload(table semanticmodel.Table) modelTablePayloadV1 {
	dimensions := dimensionsPayload(table.Dimensions)
	return modelTablePayloadV1{
		Source:             table.Source,
		Sources:            table.Sources,
		SourceDependencies: table.SourceDependencies,
		ModelDependencies:  table.ModelDependencies,
		Transform:          transformPayloadV1{SQL: table.Transform.SQL},
		SQL:                table.SQL,
		PrimaryKey:         table.PrimaryKey,
		Grain:              table.Grain,
		Dimensions:         dimensions,
		Fields:             dimensions,
		Columns:            columnsPayload(table.Columns),
		Schema:             schemaPayload(table.Schema),
	}
}

func semanticModelPayload(model *semanticmodel.Model) semanticModelPayloadV1 {
	connections := map[string]connectionPayloadV1{}
	for _, name := range sortedMapKeys(model.Connections) {
		connections[name] = connectionPayload(model.Connections[name])
	}
	sources := map[string]sourcePayloadV1{}
	for _, name := range sortedMapKeys(model.Sources) {
		sources[name] = sourcePayload(model.Sources[name])
	}
	tables := map[string]modelTablePayloadV1{}
	for _, name := range sortedMapKeys(model.Tables) {
		tables[name] = modelTablePayload(model.Tables[name])
	}
	return semanticModelPayloadV1{
		Name:          model.Name,
		Title:         model.Title,
		Description:   model.Description,
		Connections:   connections,
		Sources:       sources,
		Tables:        tables,
		Models:        tables,
		Measures:      measuresPayload(model.Measures),
		Dimensions:    semanticDimensionsPayload(model.Dimensions),
		Metrics:       metricsPayload(model.Metrics),
		Relationships: relationshipsPayload(model.Relationships),
	}
}

func semanticTablePayload(name string, table semanticmodel.Table) semanticTablePayloadV1 {
	return semanticTablePayloadV1{Table: name, modelTablePayloadV1: modelTablePayload(table)}
}

func fieldPayload(field semanticmodel.MetricDimension) fieldPayloadV1 {
	return fieldPayloadV1{
		Field:       field.Field,
		Table:       field.Table,
		Name:        field.Name,
		Label:       field.Label,
		Description: field.Description,
		Type:        field.Type,
		Expr:        field.Expr,
		Expression:  field.Expression,
	}
}

func dimensionsPayload(fields map[string]semanticmodel.MetricDimension) map[string]fieldPayloadV1 {
	out := map[string]fieldPayloadV1{}
	for _, name := range sortedMapKeys(fields) {
		out[name] = fieldPayload(fields[name])
	}
	return out
}

func measurePayload(measure semanticmodel.MetricMeasure) measurePayloadV1 {
	return measurePayloadV1{
		Field:           measure.Field,
		Fact:            measure.Fact,
		Name:            measure.Name,
		Label:           measure.Label,
		Description:     measure.Description,
		Aggregation:     measure.Aggregation,
		InputField:      measure.Input.Field,
		InputExpression: measure.Input.Expression,
		Empty:           measure.Empty,
		Unit:            measure.Unit,
		Format:          measure.Format,
		Hidden:          measure.Hidden,
	}
}

func metricMeasurePayload(metric semanticmodel.Metric) measurePayloadV1 {
	return measurePayloadV1{
		Name:            metric.Name,
		Label:           metric.Label,
		Description:     metric.Description,
		InputExpression: metric.Expression,
		Unit:            metric.Unit,
		Format:          metric.Format,
		Hidden:          metric.Hidden,
	}
}

func semanticDimensionsPayload(dimensions map[string]semanticmodel.SemanticDimension) map[string]semanticDimensionPayloadV1 {
	out := map[string]semanticDimensionPayloadV1{}
	for _, name := range sortedMapKeys(dimensions) {
		dimension := dimensions[name]
		bindings := map[string]semanticDimensionBindingPayloadV1{}
		for _, fact := range sortedMapKeys(dimension.Bindings) {
			binding := dimension.Bindings[fact]
			bindings[fact] = semanticDimensionBindingPayloadV1{Field: binding.Field, Path: append([]string{}, binding.Path...)}
		}
		out[name] = semanticDimensionPayloadV1{
			Name: name, Label: dimension.Label, Description: dimension.Description, Type: dimension.Type,
			Grains: append([]string{}, dimension.Grains...), Timezone: dimension.Timezone,
			Calendar: dimension.Calendar, WeekStart: dimension.WeekStart, Bindings: bindings,
		}
	}
	return out
}

func metricsPayload(metrics map[string]semanticmodel.Metric) map[string]metricPayloadV1 {
	out := map[string]metricPayloadV1{}
	for _, name := range sortedMapKeys(metrics) {
		metric := metrics[name]
		out[name] = metricPayloadV1{Name: name, Label: metric.Label, Description: metric.Description, Expression: metric.Expression, Unit: metric.Unit, Format: metric.Format, Hidden: metric.Hidden}
	}
	return out
}

func measuresPayload(measures map[string]semanticmodel.MetricMeasure) map[string]measurePayloadV1 {
	out := map[string]measurePayloadV1{}
	for _, name := range sortedMapKeys(measures) {
		out[name] = measurePayload(measures[name])
	}
	return out
}

func relationshipPayload(relationship semanticmodel.Relationship) relationshipPayloadV1 {
	return relationshipPayloadV1{
		ID:          relationship.ID,
		Description: relationship.Description,
		From:        relationship.From,
		To:          relationship.To,
		Cardinality: relationship.Cardinality,
	}
}

func relationshipsPayload(relationships []semanticmodel.Relationship) []relationshipPayloadV1 {
	out := make([]relationshipPayloadV1, 0, len(relationships))
	for _, relationship := range relationships {
		out = append(out, relationshipPayload(relationship))
	}
	return out
}

func columnsPayload(columns map[string]semanticmodel.ModelColumn) map[string]modelColumnPayloadV1 {
	out := map[string]modelColumnPayloadV1{}
	for _, name := range sortedMapKeys(columns) {
		column := columns[name]
		out[name] = modelColumnPayloadV1{
			Field:       column.Field,
			Name:        column.Name,
			SourceField: column.SourceField,
			Description: column.Description,
			Type:        column.Type,
		}
	}
	return out
}

func schemaPayload(schema semanticmodel.TableSchema) schemaPayloadV1 {
	columns := make([]schemaColumnPayloadV1, 0, len(schema.Columns))
	for _, column := range schema.Columns {
		columns = append(columns, schemaColumnPayloadV1{
			Name:         column.Name,
			Ordinal:      column.Ordinal,
			PhysicalType: column.PhysicalType,
			Nullable:     column.Nullable,
			Default:      column.Default,
			Comment:      column.Comment,
			PrimaryKey:   column.PrimaryKey,
		})
	}
	return schemaPayloadV1{Columns: columns}
}

func dashboardPayload(report dashboarddefinition.Definition, tags []string) dashboardPayloadV1 {
	return dashboardPayloadV1{ID: report.ID, Title: report.Title, Description: report.Description, SemanticModel: report.SemanticModel, Tags: tags}
}

func pagePayload(page dashboard.Page) pagePayloadV1 {
	return pagePayloadV1{
		ID:          page.ID,
		Title:       page.Title,
		Description: page.Description,
		Canvas:      pageCanvasPayload(page.Canvas),
		Grid:        pageGridPayload(page.Grid),
	}
}

func workspaceGroupPayload(group workspace.WorkspaceGroup) workspaceGroupPayloadV1 {
	members := make([]workspaceGroupMemberPayloadV1, 0, len(group.Members))
	for _, member := range group.Members {
		members = append(members, workspaceGroupMemberPayloadV1{
			PrincipalID: member.PrincipalID,
			Email:       member.Email,
			DisplayName: member.DisplayName,
		})
	}
	return workspaceGroupPayloadV1{
		ID:          group.ID,
		Name:        group.Name,
		Description: group.Description,
		Members:     members,
	}
}

func workspaceRoleBindingPayload(binding workspace.WorkspaceRoleBinding) workspaceRoleBindingPayloadV1 {
	return workspaceRoleBindingPayloadV1{
		ID:   binding.ID,
		Name: binding.Name,
		Role: binding.Role,
		Subject: workspaceRoleBindingSubjectPayloadV1{
			Kind:        binding.Subject.Kind,
			PrincipalID: binding.Subject.PrincipalID,
			Email:       binding.Subject.Email,
			DisplayName: binding.Subject.DisplayName,
			Group:       binding.Subject.Group,
		},
	}
}

func refreshPipelinePayload(pipeline refreshschedule.Definition) refreshPipelinePayloadV1 {
	schedules := make([]refreshPipelineSchedulePayloadV1, 0, len(pipeline.Schedules))
	for _, schedule := range pipeline.Schedules {
		schedules = append(schedules, refreshPipelineSchedulePayloadV1{
			Cron:     schedule.Expression,
			Timezone: schedule.Timezone,
		})
	}
	return refreshPipelinePayloadV1{
		ID:            pipeline.ID,
		Name:          pipeline.Name,
		SemanticModel: pipeline.SemanticModel,
		Schedules:     schedules,
	}
}
func pageCanvasPayload(canvas dashboard.PageCanvas) pageCanvasV1 {
	return pageCanvasV1{Width: canvas.Width, Height: canvas.Height}
}

func pageGridPayload(grid dashboard.PageGrid) pageGridV1 {
	return pageGridV1{Columns: grid.Columns, RowHeight: grid.RowHeight, Gap: grid.Gap, Padding: grid.Padding}
}

func pageItemPayload(item dashboard.PageVisual) pageItemPayloadV1 {
	return pageItemPayloadV1{
		ID:           item.ID,
		Kind:         item.Kind,
		Visual:       item.Visual,
		Binding:      item.Binding,
		Presentation: item.Presentation,
		Description:  item.Description,
		Placement:    pagePlacementPayload(item.Placement),
		Title:        item.Title,
		Subtitle:     item.Subtitle,
		Badges:       item.Badges,
	}
}

func pagePlacementPayload(placement dashboard.PagePlacement) pagePlacementV1 {
	return pagePlacementV1{
		Col:     placement.Col,
		Row:     placement.Row,
		ColSpan: placement.ColSpan,
		RowSpan: placement.RowSpan,
	}
}
