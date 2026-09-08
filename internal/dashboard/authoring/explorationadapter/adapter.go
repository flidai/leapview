// Package explorationadapter lowers the renderer-neutral exploration contract
// into the canonical dashboard document contract.  The adapter is deliberately
// pure: it does not know about a dashboard revision, repository, or HTTP
// transport.  Those concerns remain owned by the authoring service.
package explorationadapter

import (
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// Options supplies the target visual used by scoped filters and the
// semantic reverse map needed when an exploration carries dataset-qualified
// physical field names. Dashboard queries intentionally do not have a
// datasetId operand, so accepting such a field without a binding would lose
// the dataset constraint during conversion.
type Options struct {
	VisualID string
	// Bindings maps an exploration field (for example orders.region) to the
	// semantic dimension/metric identifier used by DashboardDocument. Both
	// the qualified field and its unqualified spelling may be supplied.
	Bindings map[string]string
	// MetricRoots proves the compiled semantic root for a mapped metric. A
	// DashboardQuery has no dataset operand, so a spec with DatasetID must
	// supply this evidence for every selected metric.
	MetricRoots map[string]string
}

// Result is the complete closed authoring value produced by Convert. Filters
// target VisualID only; they must not become dashboard-wide filters when a
// tile is copied into an existing dashboard.
type Result struct {
	Visual  document.DashboardVisual
	Filters []document.DashboardFilter
}

// Convert lowers one validated authored exploration. No fields are silently
// discarded: unsupported visualization branches, display formats, malformed
// refs, and dataset-qualified fields without a semantic binding return errors.
func Convert(spec exploration.ExplorationSpec, options Options) (Result, error) {
	if spec.SchemaVersion != 0 && spec.SchemaVersion != 1 {
		return Result{}, fmt.Errorf("unsupported exploration schema version %d", spec.SchemaVersion)
	}
	if strings.TrimSpace(spec.ModelID) == "" {
		return Result{}, fmt.Errorf("exploration model id is required")
	}
	if spec.Limit <= 0 {
		return Result{}, fmt.Errorf("exploration limit must be positive")
	}
	if spec.Visualization != nil && spec.Visualization.Value == nil {
		return Result{}, fmt.Errorf("exploration visualization variant is required")
	}
	visualID := strings.TrimSpace(options.VisualID)
	if len(spec.Filters) > 0 || (spec.Time != nil && spec.Time.Range != nil) {
		if visualID == "" {
			return Result{}, fmt.Errorf("visual id is required for scoped exploration filters")
		}
	}

	ctx := converter{spec: spec, options: options}
	dimensions, metrics, err := ctx.querySelections()
	if err != nil {
		return Result{}, err
	}
	if len(metrics) == 0 {
		return Result{}, fmt.Errorf("exploration requires at least one metric")
	}
	if err := validateQueryOutputs(dimensions, metrics); err != nil {
		return Result{}, err
	}
	visualType, presentation, err := ctx.presentation()
	if err != nil {
		return Result{}, err
	}
	if spec.Table != nil && spec.Table.Columns != nil {
		if err := ctx.validateTableConfigColumns(*spec.Table.Columns); err != nil {
			return Result{}, err
		}
	}
	query, err := ctx.query(visualType, dimensions, metrics)
	if err != nil {
		return Result{}, err
	}
	filters, err := ctx.filters(visualID)
	if err != nil {
		return Result{}, err
	}
	title := "Explore"
	subtitle := (*string)(nil)
	if base, baseErr := spec.Visualization.Base(); baseErr == nil && base != nil {
		if strings.TrimSpace(valueOrEmpty(base.Title)) != "" {
			title = strings.TrimSpace(valueOrEmpty(base.Title))
		}
		subtitle = cloneString(base.Subtitle)
	}
	maxRows := spec.Limit
	if spec.Pivot != nil && spec.Pivot.Window != nil && spec.Pivot.Window.Limit > 0 && spec.Pivot.Window.Limit < maxRows {
		maxRows = spec.Pivot.Window.Limit
	}
	complete := visualizationir.VisualizationCompletenessComplete
	visual := document.DashboardVisual{
		Type: document.DashboardVisualType(visualType), Title: &title, Subtitle: subtitle,
		Query: query, Presentation: presentation,
		DataBudget: &document.DashboardDataBudget{MaxRows: maxRows, RequiredCompleteness: &complete},
	}
	return Result{Visual: visual, Filters: filters}, nil
}

type converter struct {
	spec    exploration.ExplorationSpec
	options Options
}

func (c converter) resolve(field, role string) (string, error) {
	field = strings.TrimSpace(field)
	if field == "" {
		return "", fmt.Errorf("%s field is required", role)
	}
	if mapped := strings.TrimSpace(c.options.Bindings[field]); mapped != "" {
		if err := validateDashboardResultField(mapped); err != nil {
			return "", fmt.Errorf("%s field %q binding: %w", role, field, err)
		}
		return mapped, nil
	}
	if dataset := strings.TrimSpace(valueOrEmpty(c.spec.DatasetID)); dataset != "" {
		return "", fmt.Errorf("%s field %q uses dataset %q but has no semantic dimension binding", role, field, dataset)
	}
	if strings.Contains(field, ".") {
		return "", fmt.Errorf("%s field %q is physical; semantic dimension binding is required", role, field)
	}
	if err := validateDashboardResultField(field); err != nil {
		return "", fmt.Errorf("%s field %q: %w", role, field, err)
	}
	return field, nil
}

func (c converter) dimension(value exploration.ExplorationDimensionRef) (document.DashboardDimensionSelection, error) {
	field, err := c.resolve(value.Field, "dimension")
	if err != nil {
		return document.DashboardDimensionSelection{}, err
	}
	selection := document.DashboardDimensionSelection{}
	if value.Alias == nil && value.Grain == nil {
		selection.String = &field
		return selection, nil
	}
	ref := &document.DashboardDimensionReference{Dimension: field, Alias: cloneString(value.Alias)}
	if value.Alias != nil {
		if err := validateDashboardResultField(*value.Alias); err != nil {
			return document.DashboardDimensionSelection{}, fmt.Errorf("dimension alias %q: %w", *value.Alias, err)
		}
	}
	if value.Grain != nil {
		grain := document.DashboardTimeGrain(string(*value.Grain))
		if !validDashboardTimeGrain(grain) {
			return document.DashboardDimensionSelection{}, fmt.Errorf("unsupported dimension grain %q", grain)
		}
		ref.Grain = &grain
	}
	selection.Reference = ref
	return selection, nil
}

func (c converter) metric(value exploration.ExplorationMetricRef) (document.DashboardMetricSelection, error) {
	field, err := c.resolve(value.Field, "metric")
	if err != nil {
		return document.DashboardMetricSelection{}, err
	}
	if err := c.validateMetricRoot(value.Field, field); err != nil {
		return document.DashboardMetricSelection{}, err
	}
	if value.Alias == nil {
		return document.DashboardMetricSelection{String: &field}, nil
	}
	if err := validateDashboardResultField(*value.Alias); err != nil {
		return document.DashboardMetricSelection{}, fmt.Errorf("metric alias %q: %w", *value.Alias, err)
	}
	return document.DashboardMetricSelection{Reference: &document.DashboardMetricReference{Metric: field, Alias: cloneString(value.Alias)}}, nil
}

func (c converter) validateMetricRoot(source, semantic string) error {
	dataset := strings.TrimSpace(valueOrEmpty(c.spec.DatasetID))
	if dataset == "" {
		return nil
	}
	root := strings.TrimSpace(c.options.MetricRoots[semantic])
	if root == "" {
		root = strings.TrimSpace(c.options.MetricRoots[source])
	}
	if root == "" {
		return fmt.Errorf("metric field %q has no semantic root lineage for dataset %q", source, dataset)
	}
	if root != dataset {
		return fmt.Errorf("metric field %q has root dataset %q, want %q", source, root, dataset)
	}
	return nil
}
