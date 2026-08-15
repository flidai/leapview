package compiler

import (
	"fmt"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
)

// Result is the immutable result of crossing the dashboard authoring boundary.
// Normalized is a compiler-owned clone; the input document passed to Compile is
// never modified. Definition is the serving-ready dashboard definition derived
// from that normalized document.
type Result struct {
	Normalized dashboardauthoring.Dashboard
	Definition dashboarddefinition.Definition
}

// Compile validates and normalizes one authored dashboard, compiles its
// visualizations, and builds the immutable serving definition. The model map
// is keyed by canonical semantic-model ID and is read-only for the duration of
// compilation.
func Compile(document dashboardauthoring.Dashboard, models map[string]*semanticmodel.Model) (Result, error) {
	normalized, err := ValidateAndNormalizeDashboard(&document, models)
	if err != nil {
		return Result{}, err
	}
	model := models[normalized.SemanticModel]
	visualizations, err := CompileVisualizationDefinitions(normalized, model)
	if err != nil {
		return Result{}, fmt.Errorf("compile dashboard %q visualizations: %w", normalized.ID, err)
	}
	definition, err := CompileDashboardDefinition(normalized, visualizations)
	if err != nil {
		return Result{}, fmt.Errorf("compile dashboard %q definition: %w", normalized.ID, err)
	}
	return Result{Normalized: *normalized, Definition: definition}, nil
}
