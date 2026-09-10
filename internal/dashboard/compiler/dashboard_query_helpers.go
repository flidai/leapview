package compiler

import (
	"fmt"
	"regexp"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

var canonicalResultNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func canonicalAlias(value *string, fallback string) (string, error) {
	alias := fallback
	if value != nil {
		alias = strings.TrimSpace(*value)
		if alias == "" {
			return "", fmt.Errorf("alias must not be empty")
		}
	}
	if !canonicalResultNamePattern.MatchString(alias) {
		return "", fmt.Errorf("result name %q is not a valid field identifier", alias)
	}
	return alias, nil
}

func canonicalMemberName(value string) string {
	parts := strings.Split(value, ".")
	return parts[len(parts)-1]
}

func uniqueResultFrame(fields []DashboardQueryResultField) ([]DashboardQueryResultField, error) {
	seen := make(map[string]int, len(fields))
	result := make([]DashboardQueryResultField, len(fields))
	for index, field := range fields {
		if !canonicalResultNamePattern.MatchString(field.Name) {
			return nil, fmt.Errorf("result name %q is not a valid field identifier", field.Name)
		}
		if previous, ok := seen[field.Name]; ok {
			return nil, fmt.Errorf("result name %q is duplicated by fields %d and %d; add distinct aliases", field.Name, previous, index)
		}
		seen[field.Name] = index
		result[index] = field
	}
	return result, nil
}

func canonicalSorts(values *[]document.DashboardSort, frame []DashboardQueryResultField) ([]semanticquery.Sort, error) {
	if values == nil {
		return nil, nil
	}
	allowed := make(map[string]struct{}, len(frame))
	for _, field := range frame {
		allowed[field.Name] = struct{}{}
	}
	sorts := make([]semanticquery.Sort, len(*values))
	for index, value := range *values {
		field := strings.TrimSpace(value.Field)
		if _, ok := allowed[field]; !ok {
			return nil, fmt.Errorf("sort %d references unknown compiled result field %q", index, field)
		}
		direction := string(value.Direction)
		if direction != "asc" && direction != "desc" {
			return nil, fmt.Errorf("sort %d has unsupported direction %q", index, direction)
		}
		sorts[index] = semanticquery.Sort{Field: field, Direction: direction}
	}
	return sorts, nil
}

func canonicalLimit(value *int32, fallback int64) (int64, error) {
	if value == nil {
		return fallback, nil
	}
	if *value <= 0 {
		return 0, fmt.Errorf("query limit must be positive")
	}
	return int64(*value), nil
}

func planCanonicalAggregate(request semanticquery.Request, model *semanticmodel.Model) (semanticquery.Plan, error) {
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		return semanticquery.Plan{}, fmt.Errorf("compile semantic planner: %w", err)
	}
	plan, err := planner.Plan(request)
	if err != nil {
		return semanticquery.Plan{}, fmt.Errorf("plan aggregate query: %w", err)
	}
	return plan, nil
}

func fieldsToBindings(values []DashboardQueryResultField) []visualizationdefinition.FieldBinding {
	result := make([]visualizationdefinition.FieldBinding, len(values))
	for index, value := range values {
		result[index] = visualizationdefinition.FieldBinding{FieldID: value.Source, Alias: value.Name, Grain: value.Grain}
	}
	return result
}

func sortsToBindings(values []semanticquery.Sort) []visualizationdefinition.Sort {
	result := make([]visualizationdefinition.Sort, len(values))
	for index, value := range values {
		result[index] = visualizationdefinition.Sort{FieldID: value.Field, Direction: value.Direction}
	}
	return result
}

func singleDataset(values []string) string {
	if len(values) == 1 {
		return values[0]
	}
	return ""
}
