package query

import (
	"fmt"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

type PlannerOption func(*Planner) error

func WithTableRelation(relation TableRelation) PlannerOption {
	return func(planner *Planner) error {
		if relation == nil {
			return fmt.Errorf("table relation resolver is required")
		}
		planner.tableRelation = relation
		return nil
	}
}

func NewCompiledPlanner(model *semanticmodel.Model, options ...PlannerOption) (*Planner, error) {
	compiled, err := CompileModel(model)
	if err != nil {
		return nil, err
	}
	return newPlanner(compiled, options...)
}

// NewAuthoringPlanner returns a planner that can validate semantic request
// bindings while physical field datatypes are still pending discovery. It is
// intentionally unsuitable for execution: executable callers must use
// NewCompiledPlanner, which performs strict semantic graph validation.
func NewAuthoringPlanner(model *semanticmodel.Model, options ...PlannerOption) (*Planner, error) {
	compiled, err := CompileAuthoringModel(model)
	if err != nil {
		return nil, err
	}
	return newPlanner(compiled, options...)
}

func newPlanner(compiled *CompiledModel, options ...PlannerOption) (*Planner, error) {
	planner := &Planner{compiled: compiled}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("planner option is required")
		}
		if err := option(planner); err != nil {
			return nil, err
		}
	}
	return planner, nil
}
