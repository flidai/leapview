package query

import (
	"fmt"
	"sort"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// CompiledModel is the immutable semantic metadata shared by every query in a
// serving-state runtime. Derived and ratio expressions and their dependency
// DAGs are parsed once at activation instead of being rediscovered for every
// dashboard consumer.
type CompiledModel struct {
	Model             *semanticmodel.Model
	MetricExpressions map[string]semanticmodel.Expression
	MemberFacts       map[string][]string
}

func CompileModel(model *semanticmodel.Model) (*CompiledModel, error) {
	if model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	compiled := &CompiledModel{
		Model:             model,
		MetricExpressions: make(map[string]semanticmodel.Expression, len(model.Metrics)),
		MemberFacts:       map[string][]string{},
	}
	for name, metric := range model.Metrics {
		if metric.Type != "aggregate" {
			continue
		}
		compiled.MemberFacts[name] = []string{metric.Dataset}
	}
	for name, metric := range model.Metrics {
		if metric.Type == "aggregate" {
			if metric.Dataset == "" {
				return nil, fmt.Errorf("metric %q aggregate dataset is required", name)
			}
			compiled.MemberFacts[name] = []string{metric.Dataset}
			continue
		}
		expression, err := semanticmodel.ParseExpression(metricExecutableExpression(metric))
		if err != nil {
			return nil, fmt.Errorf("metric %q: %w", name, err)
		}
		compiled.MetricExpressions[name] = expression
	}
	visiting := map[string]bool{}
	var factsFor func(string) ([]string, error)
	factsFor = func(name string) ([]string, error) {
		if facts, ok := compiled.MemberFacts[name]; ok {
			return append([]string{}, facts...), nil
		}
		expression, ok := compiled.MetricExpressions[name]
		if !ok {
			return nil, fmt.Errorf("unknown aggregate member %q", name)
		}
		if visiting[name] {
			return nil, fmt.Errorf("metric dependency cycle includes %q", name)
		}
		visiting[name] = true
		facts := map[string]bool{}
		for _, dependency := range expression.References() {
			dependencyFacts, err := factsFor(dependency)
			if err != nil {
				delete(visiting, name)
				return nil, fmt.Errorf("metric %q: %w", name, err)
			}
			for _, fact := range dependencyFacts {
				facts[fact] = true
			}
		}
		delete(visiting, name)
		resolved := make([]string, 0, len(facts))
		for fact := range facts {
			resolved = append(resolved, fact)
		}
		sort.Strings(resolved)
		compiled.MemberFacts[name] = resolved
		return append([]string{}, resolved...), nil
	}
	metricNames := make([]string, 0, len(model.Metrics))
	for name := range model.Metrics {
		metricNames = append(metricNames, name)
	}
	sort.Strings(metricNames)
	for _, name := range metricNames {
		if _, err := factsFor(name); err != nil {
			return nil, err
		}
	}
	return compiled, nil
}

// metricExecutableExpression is a planner-boundary adapter. Canonical ratio
// metrics retain numerator/denominator fields in the model; only the existing
// expression evaluator receives the governed safe_divide form.
func metricExecutableExpression(metric semanticmodel.Metric) string {
	if metric.Type == "ratio" {
		return fmt.Sprintf("safe_divide(${%s}, ${%s})", metric.Numerator, metric.Denominator)
	}
	return metric.Expression
}

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
	planner := &Planner{Model: model, Compiled: compiled}
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
