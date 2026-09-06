package http

import (
	"context"
	"errors"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
)

func semanticDatasetTarget(datasetID string) semanticquery.SemanticAccessTarget {
	return semanticquery.SemanticAccessTarget{Dataset: datasetID}
}

// semanticTargetAuthorizerForModel returns the one optional capability used by
// semantic consumers. The capability is optional only for unprotected models;
// protected models fail closed when it is absent.
func (h Handler) semanticTargetAuthorizerForModel(modelID string, model *semanticmodel.Model) (semanticTargetAuthorizer, error) {
	compiled := compiledSemanticModel(h.Metrics, modelID)
	if compiled != nil && compiled.SemanticAccessPolicy().Protected() && !compiled.MatchesModel(model) {
		return nil, errSemanticAuthorizationUnavailable
	}
	if !semanticquery.ModelRequiresSemanticAccess(model) && (compiled == nil || !compiled.SemanticAccessPolicy().Protected()) {
		return nil, nil
	}
	authorizer, ok := h.Metrics.(semanticTargetAuthorizer)
	if !ok || authorizer == nil {
		return nil, errSemanticAuthorizationUnavailable
	}
	return authorizer, nil
}

func (h Handler) semanticTargetAllowed(ctx context.Context, modelID string, model *semanticmodel.Model, target semanticquery.SemanticAccessTarget) (bool, error) {
	authorizer, err := h.semanticTargetAuthorizerForModel(modelID, model)
	if err != nil {
		return false, err
	}
	if authorizer == nil {
		return true, nil
	}
	if err := authorizer.AuthorizeSemanticTarget(ctx, modelID, target); err != nil {
		// Target authorization deliberately does not disclose whether the target
		// exists. The caller maps this to the same response as an unknown member.
		if errors.Is(err, errSemanticAuthorizationUnavailable) {
			return false, err
		}
		return false, nil
	}
	return true, nil
}

func (h Handler) requireSemanticTarget(ctx context.Context, modelID string, model *semanticmodel.Model, target semanticquery.SemanticAccessTarget) error {
	allowed, err := h.semanticTargetAllowed(ctx, modelID, model, target)
	if err != nil {
		return err
	}
	if !allowed {
		return errSemanticTargetDenied
	}
	return nil
}

// semanticDimensionNamesForField maps a dataset's physical field back to all
// semantic members governed by the access policy. Protected models must not
// expose an unbound physical field through this API.
func semanticDimensionNamesForField(model *semanticmodel.Model, datasetID, field string) []string {
	if model == nil {
		return nil
	}
	names := make([]string, 0)
	for name, dimension := range model.Dimensions {
		binding, ok := dimension.Bindings[datasetID]
		if !ok {
			continue
		}
		if binding.Field == datasetID+"."+field || strings.TrimPrefix(binding.Field, datasetID+".") == field {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func semanticDimensionTargets(model *semanticmodel.Model, datasetID, field string) []semanticquery.SemanticAccessTarget {
	if model == nil || strings.TrimSpace(field) == "" {
		return nil
	}
	if _, ok := model.Dimensions[field]; ok {
		if datasetID != "" {
			return []semanticquery.SemanticAccessTarget{{Dataset: datasetID, Dimension: field}}
		}
		targets := make([]semanticquery.SemanticAccessTarget, 0, len(model.Dimensions[field].Bindings))
		for dataset := range model.Dimensions[field].Bindings {
			targets = append(targets, semanticquery.SemanticAccessTarget{Dataset: dataset, Dimension: field})
		}
		return targets
	}
	if strings.Contains(field, ".") {
		dimension, err := model.ResolveDimension(field)
		if err != nil {
			return nil
		}
		names := semanticDimensionNamesForField(model, dimension.Table, dimension.Name)
		if len(names) == 0 {
			return nil
		}
		targets := make([]semanticquery.SemanticAccessTarget, 0, len(names))
		for _, name := range names {
			targets = append(targets, semanticquery.SemanticAccessTarget{Dataset: dimension.Table, Dimension: name})
		}
		return targets
	}
	if datasetID == "" {
		return nil
	}
	names := semanticDimensionNamesForField(model, datasetID, field)
	if len(names) == 0 {
		return nil
	}
	targets := make([]semanticquery.SemanticAccessTarget, 0, len(names))
	for _, name := range names {
		targets = append(targets, semanticquery.SemanticAccessTarget{Dataset: datasetID, Dimension: name})
	}
	return targets
}

func semanticMetricTarget(model *semanticmodel.Model, field string) (semanticquery.SemanticAccessTarget, bool) {
	if model == nil {
		return semanticquery.SemanticAccessTarget{}, false
	}
	if _, ok := model.Metrics[field]; !ok {
		return semanticquery.SemanticAccessTarget{}, false
	}
	return semanticquery.SemanticAccessTarget{Metric: field}, true
}

func (h Handler) authorizeSemanticField(ctx context.Context, modelID string, model *semanticmodel.Model, datasetID, field string, metric bool) error {
	authorizer, err := h.semanticTargetAuthorizerForModel(modelID, model)
	if err != nil {
		return err
	}
	if authorizer == nil {
		return nil
	}
	if metric {
		target, ok := semanticMetricTarget(model, field)
		if !ok {
			return errSemanticTargetDenied
		}
		return h.requireSemanticTarget(ctx, modelID, model, target)
	}
	targets := semanticDimensionTargets(model, datasetID, field)
	if len(targets) == 0 {
		return errSemanticTargetDenied
	}
	for _, target := range targets {
		if err := h.requireSemanticTarget(ctx, modelID, model, target); err != nil {
			return err
		}
	}
	return nil
}

func (h Handler) authorizeWholeSemanticModel(ctx context.Context, modelID string, model *semanticmodel.Model) error {
	authorizer, err := h.semanticTargetAuthorizerForModel(modelID, model)
	if err != nil || authorizer == nil {
		return err
	}
	compiled := compiledSemanticModel(h.Metrics, modelID)
	if compiled == nil {
		return errSemanticAuthorizationUnavailable
	}
	// A relationship/source/model projection does not carry member-level
	// filtering semantics. Deny the complete projection if any governed member
	// cannot be proven visible.
	for _, dataset := range compiled.DatasetNames() {
		if err := h.requireSemanticTarget(ctx, modelID, model, semanticquery.SemanticAccessTarget{Dataset: dataset}); err != nil {
			return err
		}
	}
	for name, dimension := range model.Dimensions {
		for dataset := range dimension.Bindings {
			if err := h.requireSemanticTarget(ctx, modelID, model, semanticquery.SemanticAccessTarget{Dataset: dataset, Dimension: name}); err != nil {
				return err
			}
		}
	}
	for name := range model.Metrics {
		if err := h.requireSemanticTarget(ctx, modelID, model, semanticquery.SemanticAccessTarget{Metric: name}); err != nil {
			return err
		}
	}
	for _, filter := range model.Filters {
		targets := semanticDimensionTargets(model, "", filter.Field)
		if len(targets) == 0 {
			return errSemanticTargetDenied
		}
		for _, target := range targets {
			if err := h.requireSemanticTarget(ctx, modelID, model, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h Handler) authorizeSemanticAggregateQuery(ctx context.Context, modelID string, model *semanticmodel.Model, request reportdef.AggregateQuery) error {
	authorizer, err := h.semanticTargetAuthorizerForModel(modelID, model)
	if err != nil {
		return err
	}
	if authorizer == nil {
		return nil
	}
	if request.Dataset != "" {
		if err := h.requireSemanticTarget(ctx, modelID, model, semanticquery.SemanticAccessTarget{Dataset: request.Dataset}); err != nil {
			return err
		}
	}
	for _, field := range request.Dimensions {
		if err := h.authorizeSemanticField(ctx, modelID, model, request.Dataset, field.Field, false); err != nil {
			return err
		}
	}
	for _, field := range request.Metrics {
		if err := h.authorizeSemanticField(ctx, modelID, model, request.Dataset, field.Field, true); err != nil {
			return err
		}
	}
	if request.Time.Field != "" {
		if err := h.authorizeSemanticField(ctx, modelID, model, request.Dataset, request.Time.Field, false); err != nil {
			return err
		}
	}
	if err := h.authorizeSemanticFilters(ctx, modelID, model, request.Dataset, request.Filters); err != nil {
		return err
	}
	for _, sortSpec := range request.Sort {
		if err := h.authorizeSemanticField(ctx, modelID, model, request.Dataset, sortSpec.Field, false); err != nil {
			if _, ok := semanticMetricTarget(model, sortSpec.Field); ok {
				err = h.authorizeSemanticField(ctx, modelID, model, request.Dataset, sortSpec.Field, true)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (h Handler) authorizeSemanticRowQuery(ctx context.Context, modelID string, model *semanticmodel.Model, request reportdef.RowQuery) error {
	return h.authorizeSemanticAggregateQuery(ctx, modelID, model, reportdef.AggregateQuery{
		Dataset: request.Dataset, Dimensions: request.Dimensions, Metrics: request.Metrics,
		Filters: request.Filters, Sort: request.Sort,
	})
}

func (h Handler) authorizeSemanticFilters(ctx context.Context, modelID string, model *semanticmodel.Model, datasetID string, filters []reportdef.QueryFilter) error {
	for _, filter := range filters {
		filterDataset := filter.Dataset
		if filterDataset == "" {
			filterDataset = datasetID
		}
		if _, ok := semanticMetricTarget(model, filter.Field); ok {
			if err := h.authorizeSemanticField(ctx, modelID, model, filterDataset, filter.Field, true); err != nil {
				return err
			}
		} else if err := h.authorizeSemanticField(ctx, modelID, model, filterDataset, filter.Field, false); err != nil {
			return err
		}
		for _, group := range filter.Groups {
			if err := h.authorizeSemanticFilters(ctx, modelID, model, filterDataset, group.Filters); err != nil {
				return err
			}
		}
		if filter.Spatial != nil {
			for _, field := range []string{filter.Spatial.LatitudeField, filter.Spatial.LongitudeField} {
				if err := h.authorizeSemanticField(ctx, modelID, model, filter.Spatial.Dataset, field, false); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
