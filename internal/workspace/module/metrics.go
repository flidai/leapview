package module

import (
	"context"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	workspacehttp "github.com/flidai/leapview/internal/workspace/http"
	"github.com/flidai/leapview/internal/workspace/navigation"
)

type MetricsAdapter struct{ queryruntime.Metrics }

func (m MetricsAdapter) Catalog() navigation.Catalog {
	return navigationCatalog(m.Metrics.Catalog())
}

func (m MetricsAdapter) DataExplorerModel(modelID string) (workspacehttp.DataExplorerModel, bool) {
	model, ok := m.Metrics.SemanticModel(modelID)
	if !ok || model == nil {
		return workspacehttp.DataExplorerModel{}, false
	}
	projection := workspacehttp.DataExplorerModel{
		ID: model.Name, Title: model.Title, Description: model.Description,
		Sources:       make(map[string]workspacehttp.DataExplorerSource, len(model.Sources)),
		Tables:        make(map[string]workspacehttp.DataExplorerTable, len(model.Tables)),
		Measures:      make(map[string]workspacehttp.DataExplorerMeasure, len(model.Measures)),
		Relationships: make([]workspacehttp.DataExplorerRelationship, 0, len(model.Relationships)),
	}
	for name, source := range model.Sources {
		projected := workspacehttp.DataExplorerSource{
			Fields:  make(map[string]workspacehttp.DataExplorerField, len(source.Fields)),
			Columns: make([]workspacehttp.DataExplorerColumn, 0, len(source.Schema.Columns)),
		}
		for fieldName, field := range source.Fields {
			projected.Fields[fieldName] = workspacehttp.DataExplorerField{Name: field.Name, Label: field.Name, Type: field.Type, Description: field.Description}
		}
		for _, column := range source.Schema.Columns {
			projected.Columns = append(projected.Columns, workspacehttp.DataExplorerColumn{
				Name: column.Name, PhysicalType: column.PhysicalType, Ordinal: column.Ordinal,
				Nullable: column.Nullable, Default: column.Default, Comment: column.Comment, PrimaryKey: column.PrimaryKey,
			})
		}
		projection.Sources[name] = projected
	}
	for name, table := range model.Tables {
		projected := workspacehttp.DataExplorerTable{
			Description: table.Description, Grain: table.Grain,
			Dimensions: make(map[string]workspacehttp.DataExplorerField, len(table.Dimensions)),
			Columns:    make(map[string]workspacehttp.DataExplorerField, len(table.Columns)),
			Schema:     make([]workspacehttp.DataExplorerColumn, 0, len(table.Schema.Columns)),
		}
		for fieldName, field := range table.Dimensions {
			projected.Dimensions[fieldName] = workspacehttp.DataExplorerField{Name: field.Name, Label: field.Label, Type: field.Type, Description: field.Description}
		}
		for fieldName, field := range table.Columns {
			projected.Columns[fieldName] = workspacehttp.DataExplorerField{Name: field.Name, Label: field.Name, Type: field.Type, Description: field.Description}
		}
		for _, column := range table.Schema.Columns {
			projected.Schema = append(projected.Schema, workspacehttp.DataExplorerColumn{
				Name: column.Name, PhysicalType: column.PhysicalType, Ordinal: column.Ordinal,
				Nullable: column.Nullable, Default: column.Default, Comment: column.Comment, PrimaryKey: column.PrimaryKey,
			})
		}
		projection.Tables[name] = projected
	}
	for name, measure := range model.Measures {
		projection.Measures[name] = workspacehttp.DataExplorerMeasure{
			Name: name, Label: measure.Label, Description: measure.Description, Fact: measure.Fact,
			Type: measure.Aggregation, Hidden: measure.Hidden,
		}
	}
	for _, relationship := range model.Relationships {
		projection.Relationships = append(projection.Relationships, workspacehttp.DataExplorerRelationship{
			ID: relationship.ID, Description: relationship.Description, From: relationship.From,
			To: relationship.To, Cardinality: relationship.Cardinality,
		})
	}
	return projection, true
}

func (m MetricsAdapter) ExecuteDataPreview(ctx context.Context, request workspacehttp.DataPreviewRequest) (workspacehttp.DataPreviewResult, error) {
	sortSpec := []dataquery.Sort(nil)
	if request.SortColumn != "" {
		sortSpec = []dataquery.Sort{{Field: request.SortColumn, Direction: request.Direction}}
	}
	var query dataquery.Query
	switch request.Layer {
	case "model_table":
		query = dataquery.ModelTableRows(request.ModelID, request.Table, request.Columns, sortSpec, request.Offset, request.Limit, request.IncludeTotal)
	case "semantic_view":
		fields := make([]dataquery.Field, 0, len(request.Columns))
		for _, column := range request.Columns {
			fields = append(fields, dataquery.Field{Field: request.Table + "." + column, Alias: column})
		}
		query = dataquery.SemanticRows(request.ModelID, request.Table, fields, nil, nil, sortSpec, request.Offset, request.Limit, request.IncludeTotal)
	default:
		query = dataquery.Query{
			ModelID: request.ModelID, Kind: dataquery.Kind(request.Layer), Target: request.Table,
			Limit: request.Limit, Offset: request.Offset, IncludeTotal: request.IncludeTotal,
		}
	}
	query.WorkspaceID = request.WorkspaceID
	query = query.WithMetadata(dataquery.Metadata{
		Surface: dataquery.SurfaceDataExplorer, Operation: dataquery.OperationPreviewWindow,
		ObjectType: request.Layer, ObjectID: request.WorkspaceID + ":" + request.ObjectKey,
	})
	result, err := m.Metrics.ExecuteDataQuery(ctx, query)
	if err != nil {
		return workspacehttp.DataPreviewResult{}, err
	}
	rows := make([]map[string]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		converted := make(map[string]any, len(row))
		for key, value := range row {
			converted[key] = value
		}
		rows = append(rows, converted)
	}
	return workspacehttp.DataPreviewResult{
		Rows: rows, TotalRows: result.TotalRows, TotalRowsKnown: result.TotalRowsKnown, SQL: result.SQL,
	}, nil
}

func (m MetricsAdapter) ExecuteDataExplore(ctx context.Context, request workspacehttp.DataExploreRequest) (workspacehttp.DataExploreResult, error) {
	fields := make([]dataquery.Field, 0, len(request.Dimensions))
	aliases := exploreAliases(request.Dimensions, request.Measures)
	for _, field := range request.Dimensions {
		fields = append(fields, dataquery.Field{Field: field, Alias: aliases[field]})
	}
	measures := make([]dataquery.Field, 0, len(request.Measures))
	for _, field := range request.Measures {
		measures = append(measures, dataquery.Field{Field: field, Alias: aliases[field]})
	}
	filters := make([]dataquery.Filter, 0, len(request.Filters))
	for _, filter := range request.Filters {
		values := make([]any, 0, len(filter.Values))
		for _, value := range filter.Values {
			values = append(values, value)
		}
		filters = append(filters, dataquery.Filter{Field: filter.Field, Fact: filter.Fact, Operator: filter.Operator, Values: values})
	}
	sortSpec := make([]dataquery.Sort, 0, len(request.Sort))
	for _, sort := range request.Sort {
		sortSpec = append(sortSpec, dataquery.Sort{Field: sort.Field, Direction: sort.Direction})
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 100
	}
	query := dataquery.SemanticAggregate(request.ModelID, request.DatasetID, fields, measures, filters, sortSpec, 0, limit+1)
	query.Time = dataquery.Time{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias}
	query.WorkspaceID = request.WorkspaceID
	query = query.WithMetadata(dataquery.Metadata{
		Surface: dataquery.SurfaceDataExplorer, Operation: dataquery.OperationSemanticExplore,
		ObjectType: "semantic_dataset", ObjectID: request.WorkspaceID + ":" + request.ModelID + ":" + request.DatasetID,
	})
	result, err := m.Metrics.ExecuteDataQuery(ctx, query)
	if err != nil {
		return workspacehttp.DataExploreResult{}, err
	}
	truncated := len(result.Rows) > limit
	rows := result.Rows
	if truncated {
		rows = rows[:limit]
	}
	converted := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		item := make(map[string]any, len(row))
		for key, value := range row {
			item[key] = value
		}
		converted = append(converted, item)
	}
	columns := make([]string, 0, len(result.Columns))
	for _, column := range result.Columns {
		columns = append(columns, column.Name)
	}
	return workspacehttp.DataExploreResult{
		Columns: columns, Rows: converted, SQL: result.SQL, Plan: result.PlanText,
		DurationMS: result.DurationMS, RowsReturned: len(converted), Truncated: truncated,
		Warnings: append([]string(nil), result.Warnings...),
	}, nil
}

func exploreAliases(dimensions, measures []string) map[string]string {
	all := append(append([]string(nil), dimensions...), measures...)
	counts := map[string]int{}
	for _, field := range all {
		name := field
		if index := len(field) - 1; index >= 0 {
			for index >= 0 && field[index] != '.' {
				index--
			}
			if index >= 0 && index+1 < len(field) {
				name = field[index+1:]
			}
		}
		counts[name]++
	}
	out := map[string]string{}
	for _, field := range all {
		name := field
		table := ""
		for index := len(field) - 1; index >= 0; index-- {
			if field[index] == '.' {
				table, name = field[:index], field[index+1:]
				break
			}
		}
		if counts[name] > 1 && table != "" {
			name = table + "__" + name
		}
		out[field] = name
	}
	return out
}
