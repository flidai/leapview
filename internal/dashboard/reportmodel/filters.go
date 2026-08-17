package reportmodel

import (
	"fmt"
	"sort"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
)

func FieldAppliesToTarget(d *dashboardauthoring.Dashboard, model *semanticmodel.Model, field, fact, targetKind, targetID string) (bool, error) {
	facts, err := TargetFacts(d, model, targetKind, targetID)
	if err != nil {
		return false, err
	}
	if dimension, ok := model.Dimensions[field]; ok {
		for _, targetFact := range facts {
			if _, ok := dimension.Bindings[targetFact]; !ok {
				return false, nil
			}
		}
		return true, nil
	}
	if len(facts) != 1 {
		if fact == "" {
			return false, nil
		}
		for _, targetFact := range facts {
			if targetFact == fact {
				return model.CanReachField(targetFact, field) == nil, nil
			}
		}
		return false, nil
	}
	if err := model.CanReachField(facts[0], field); err != nil {
		return false, nil
	}
	return true, nil
}

func TargetFacts(d *dashboardauthoring.Dashboard, model *semanticmodel.Model, targetKind, targetID string) ([]string, error) {
	var table string
	var metrics []dashboardauthoring.FieldRef
	switch targetKind {
	case "visual":
		if visual, ok := d.Visuals[targetID]; ok {
			if visual.Chart != nil {
				table, metrics = visual.Chart.Query.Table, visual.Chart.Query.Metrics
			} else if visual.Tabular != nil {
				table, metrics = visual.Tabular.Query.Table, visual.Tabular.Query.Metrics
			}
		} else {
			return nil, fmt.Errorf("unknown target visual %q", targetID)
		}
	default:
		return nil, fmt.Errorf("unknown target kind %q", targetKind)
	}
	if table != "" {
		if _, ok := model.Tables[table]; !ok {
			return nil, fmt.Errorf("query references unknown table %q", table)
		}
		return []string{table}, nil
	}
	factSet := map[string]struct{}{}
	var addMember func(string) error
	visiting := map[string]bool{}
	addMember = func(name string) error {
		metric, ok := model.Metrics[name]
		if !ok {
			return fmt.Errorf("unknown metric %q", name)
		}
		if visiting[name] {
			return fmt.Errorf("metric dependency cycle includes %q", name)
		}
		if metric.Type == "aggregate" {
			factSet[metric.Dataset] = struct{}{}
			return nil
		}
		visiting[name] = true
		expressionSource := metric.Expression
		if metric.Type == "ratio" {
			expressionSource = fmt.Sprintf("safe_divide(${%s}, ${%s})", metric.Numerator, metric.Denominator)
		}
		expression, err := semanticmodel.ParseExpression(expressionSource)
		if err != nil {
			return err
		}
		for _, ref := range expression.References() {
			if err := addMember(ref); err != nil {
				return err
			}
		}
		delete(visiting, name)
		return nil
	}
	for _, metric := range metrics {
		if err := addMember(metric.Field); err != nil {
			return nil, err
		}
	}
	facts := make([]string, 0, len(factSet))
	for fact := range factSet {
		facts = append(facts, fact)
	}
	sort.Strings(facts)
	if len(facts) == 0 {
		return nil, fmt.Errorf("query requires at least one fact")
	}
	return facts, nil
}

func TargetBaseTable(d *dashboardauthoring.Dashboard, model *semanticmodel.Model, targetKind, targetID string) (string, error) {
	facts, err := TargetFacts(d, model, targetKind, targetID)
	if err != nil {
		return "", err
	}
	if len(facts) != 1 {
		return "", fmt.Errorf("target uses multiple facts")
	}
	return facts[0], nil
}
