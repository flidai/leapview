package compiler

import (
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/document"
)

// modelForAuthoringRecords adds provisional dimensions for safe root fields
// used by a dashboard while a Model's output schema is still undiscovered.
// The copy is used only for request validation; discovery and activation keep
// the original model as the authority that decides whether those fields exist.
func modelForAuthoringRecords(values []document.DashboardRecordFieldSelection, dataset string, model *semanticmodel.Model) (*semanticmodel.Model, error) {
	table, ok := model.Tables[dataset]
	if !ok || table.AuthoredFields == nil || len(table.Schema.Columns) > 0 {
		return model, nil
	}
	deferred := make([]string, 0)
	for _, value := range values {
		name, _, err := canonicalRecordField(value)
		if err != nil || strings.Contains(name, ".") || !canonicalResultNamePattern.MatchString(name) {
			continue
		}
		qualified := dataset + "." + name
		if _, err := model.ResolveDimension(qualified); err == nil {
			continue
		}
		deferred = append(deferred, name)
	}
	if len(deferred) == 0 {
		return model, nil
	}
	clone := model.ExecutionSnapshot()
	if clone == nil {
		return nil, fmt.Errorf("semantic model snapshot is required")
	}
	clonedTable, ok := clone.Tables[dataset]
	if !ok {
		return nil, fmt.Errorf("records query references unknown dataset %q", dataset)
	}
	if clonedTable.Dimensions == nil {
		clonedTable.Dimensions = map[string]semanticmodel.MetricDimension{}
	}
	if clonedTable.Columns == nil {
		clonedTable.Columns = map[string]semanticmodel.ModelColumn{}
	}
	for name, dimension := range clonedTable.Dimensions {
		if _, exists := clonedTable.Columns[name]; !exists {
			clonedTable.Columns[name] = semanticmodel.ModelColumn{
				Name: name, Field: dataset + "." + name, SourceField: name,
				Type: dimension.Type, Datatype: dimension.Datatype,
			}
		}
	}
	for _, name := range deferred {
		if _, exists := clonedTable.Dimensions[name]; !exists {
			clonedTable.Dimensions[name] = semanticmodel.MetricDimension{}
		}
		if _, exists := clonedTable.Columns[name]; !exists {
			clonedTable.Columns[name] = semanticmodel.ModelColumn{Name: name, Field: dataset + "." + name, SourceField: name}
		}
	}
	clone.Tables[dataset] = clonedTable
	return clone, nil
}
