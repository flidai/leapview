package reportmodel

import (
	"fmt"
	"sort"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
)

func FieldAppliesToTarget(d *dashboardauthoring.Dashboard, model *semanticmodel.Model, field, dataset, targetKind, targetID string) (bool, error) {
	datasets, err := TargetDatasets(d, model, targetKind, targetID)
	if err != nil {
		return false, err
	}
	if dimension, ok := model.Dimensions[field]; ok {
		for _, targetDataset := range datasets {
			if _, ok := dimension.Bindings[targetDataset]; !ok {
				return false, nil
			}
		}
		return true, nil
	}
	if len(datasets) != 1 {
		if dataset == "" {
			return false, nil
		}
		for _, targetDataset := range datasets {
			if targetDataset == dataset {
				return model.CanReachField(targetDataset, field) == nil, nil
			}
		}
		return false, nil
	}
	if err := model.CanReachField(datasets[0], field); err != nil {
		return false, nil
	}
	return true, nil
}

func TargetDatasets(d *dashboardauthoring.Dashboard, model *semanticmodel.Model, targetKind, targetID string) ([]string, error) {
	var dataset string
	var metrics []dashboardauthoring.FieldRef
	switch targetKind {
	case "visual":
		if visual, ok := d.Visuals[targetID]; ok {
			if visual.Chart != nil {
				dataset, metrics = visual.Chart.Query.Dataset, visual.Chart.Query.Metrics
			} else if visual.Tabular != nil {
				dataset, metrics = visual.Tabular.Query.Dataset, visual.Tabular.Query.Metrics
			}
		} else {
			return nil, fmt.Errorf("unknown target visual %q", targetID)
		}
	default:
		return nil, fmt.Errorf("unknown target kind %q", targetKind)
	}
	if dataset != "" {
		if _, ok := model.Tables[dataset]; !ok {
			return nil, fmt.Errorf("query references unknown dataset %q", dataset)
		}
		return []string{dataset}, nil
	}
	datasetSet := map[string]struct{}{}
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
			datasetSet[metric.Dataset] = struct{}{}
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
	datasets := make([]string, 0, len(datasetSet))
	for dataset := range datasetSet {
		datasets = append(datasets, dataset)
	}
	sort.Strings(datasets)
	if len(datasets) == 0 {
		return nil, fmt.Errorf("query requires at least one dataset")
	}
	return datasets, nil
}

func TargetBaseTable(d *dashboardauthoring.Dashboard, model *semanticmodel.Model, targetKind, targetID string) (string, error) {
	datasets, err := TargetDatasets(d, model, targetKind, targetID)
	if err != nil {
		return "", err
	}
	if len(datasets) != 1 {
		return "", fmt.Errorf("target uses multiple datasets")
	}
	return datasets[0], nil
}
