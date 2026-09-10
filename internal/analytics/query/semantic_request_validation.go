package query

import (
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// SemanticRequestScope is the dataset scope proven by semantic request
// validation. It contains no physical SQL metadata or inferred field types.
// Dashboard authoring uses it to construct semantic query bindings before
// schema discovery; executable planning still uses NewCompiledPlanner.
type SemanticRequestScope struct {
	Datasets []string
}

// ValidateRequest validates an authored aggregate request through the same
// compiled resolution used by the runtime planner, stopping before physical
// PlanIR rendering.
func ValidateRequest(model *semanticmodel.Model, request Request) (SemanticRequestScope, error) {
	planner, err := NewAuthoringPlanner(model)
	if err != nil {
		return SemanticRequestScope{}, err
	}
	analysis, err := planner.ValidateAggregateRequest(request)
	if err != nil {
		return SemanticRequestScope{}, err
	}
	return SemanticRequestScope{Datasets: append([]string(nil), analysis.Datasets...)}, nil
}

// ValidateRowRequest validates an authored row request through activation
// style query views and row population resolution.
func ValidateRowRequest(model *semanticmodel.Model, request RowRequest) (SemanticRequestScope, error) {
	planner, err := NewAuthoringPlanner(model)
	if err != nil {
		return SemanticRequestScope{}, err
	}
	if err := planner.ValidateRowRequest(request); err != nil {
		return SemanticRequestScope{}, err
	}
	return SemanticRequestScope{Datasets: []string{request.Dataset}}, nil
}

// ValidateRawValueRequest validates an authored statistical request without
// requiring a resolved physical numeric input type.
func ValidateRawValueRequest(model *semanticmodel.Model, request RawValueRequest) (SemanticRequestScope, error) {
	planner, err := NewAuthoringPlanner(model)
	if err != nil {
		return SemanticRequestScope{}, err
	}
	return planner.ValidateRawValueRequest(request)
}
