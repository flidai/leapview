package exploration

import (
	"errors"
	"fmt"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func validateSort(sort ExplorationSort, selected map[string]string, index int) error {
	if err := validateFieldReference(sort.Field, fmt.Sprintf("sort %d field", index)); err != nil {
		return err
	}
	if _, ok := selected[sort.Field]; !ok {
		return fmt.Errorf("exploration sort field %q is not selected", sort.Field)
	}
	if sort.Direction != ExplorationSortDirectionAsc && sort.Direction != ExplorationSortDirectionDesc {
		return fmt.Errorf("invalid exploration sort direction %q", sort.Direction)
	}
	return nil
}

func validatePivotShape(pivot *ExplorationPivotConfig, selected map[string]string) error {
	if pivot == nil {
		return nil
	}
	if len(pivot.Rows) > 100 || len(pivot.Columns) > 100 || len(pivot.Metrics) > 100 {
		return errors.New("pivot fields exceed the maximum item count")
	}
	pivotSelected := map[string]string{}
	add := func(field, alias, kind string) error {
		if err := validateFieldID(field, kind+" field"); err != nil {
			return err
		}
		if err := validateAlias(aliasPointer(alias), kind, field); err != nil {
			return err
		}
		if _, exists := pivotSelected[field]; exists {
			return fmt.Errorf("duplicate pivot field %q", field)
		}
		if previous, exists := selected[field]; exists && previous != field {
			return fmt.Errorf("pivot field %q conflicts with selected alias", field)
		}
		pivotSelected[field] = field
		selected[field] = field
		if alias == "" {
			return nil
		}
		if _, exists := pivotSelected[alias]; exists {
			return fmt.Errorf("duplicate pivot alias %q", alias)
		}
		if previous, exists := selected[alias]; exists && previous != field {
			return fmt.Errorf("pivot alias %q conflicts with selected field", alias)
		}
		pivotSelected[alias] = field
		selected[alias] = field
		selected[field] = field
		return nil
	}
	for _, row := range pivot.Rows {
		if err := add(row.Field, stringPointerValue(row.Alias), "pivot row"); err != nil {
			return err
		}
		if row.Grain != nil {
			if err := validateTimeGrain(*row.Grain); err != nil {
				return err
			}
		}
	}
	for _, column := range pivot.Columns {
		if err := add(column.Field, stringPointerValue(column.Alias), "pivot column"); err != nil {
			return err
		}
		if column.Grain != nil {
			if err := validateTimeGrain(*column.Grain); err != nil {
				return err
			}
		}
	}
	for _, metric := range pivot.Metrics {
		if err := add(metric.Field, stringPointerValue(metric.Alias), "pivot metric"); err != nil {
			return err
		}
	}
	if pivot.Sort != nil {
		if len(*pivot.Sort) > 100 {
			return errors.New("pivot sort count exceeds 100")
		}
		for index, sort := range *pivot.Sort {
			if err := validateSort(sort, pivotSelected, index); err != nil {
				return fmt.Errorf("invalid pivot sort: %w", err)
			}
		}
	}
	if pivot.Window != nil {
		if pivot.Window.Limit < 1 || pivot.Window.Limit > 1000 {
			return fmt.Errorf("pivot window limit %d is outside 1..1000", pivot.Window.Limit)
		}
		if pivot.Window.Offset != nil && *pivot.Window.Offset < 0 {
			return errors.New("pivot window offset cannot be negative")
		}
	}
	return nil
}

func aliasPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func validatePivot(model *semanticmodel.Model, pivot *ExplorationPivotConfig, selected map[string]string) error {
	if pivot == nil {
		return nil
	}
	pivotSelected := map[string]string{}
	add := func(field, alias string) error {
		if field == "" {
			return errors.New("pivot field is required")
		}
		if _, exists := pivotSelected[field]; exists {
			return fmt.Errorf("duplicate pivot field %q", field)
		}
		if previous, exists := selected[field]; exists && previous != field {
			return fmt.Errorf("pivot field %q conflicts with selected alias", field)
		}
		pivotSelected[field] = field
		if alias != "" {
			if _, exists := pivotSelected[alias]; exists {
				return fmt.Errorf("duplicate pivot alias %q", alias)
			}
			if previous, exists := selected[alias]; exists && previous != field {
				return fmt.Errorf("pivot alias %q conflicts with selected field", alias)
			}
			pivotSelected[alias] = field
			if _, exists := selected[alias]; !exists {
				selected[alias] = field
			}
		}
		if _, exists := selected[field]; !exists {
			selected[field] = field
		}
		return nil
	}
	for _, row := range pivot.Rows {
		if err := model.ValidateQueryDimension(row.Field); err != nil {
			return fmt.Errorf("invalid pivot row %q: %w", row.Field, err)
		}
		if err := validateAlias(row.Alias, "pivot row", row.Field); err != nil {
			return err
		}
		if row.Grain != nil {
			resolved, err := resolveExplorationDimension(model, row.Field)
			if err != nil {
				return fmt.Errorf("invalid pivot row %q: %w", row.Field, err)
			}
			if err := validateModelGrain(model, row.Field, resolved, *row.Grain); err != nil {
				return err
			}
		}
		if err := add(row.Field, stringPointerValue(row.Alias)); err != nil {
			return err
		}
	}
	for _, column := range pivot.Columns {
		if err := model.ValidateQueryDimension(column.Field); err != nil {
			return fmt.Errorf("invalid pivot column %q: %w", column.Field, err)
		}
		if err := validateAlias(column.Alias, "pivot column", column.Field); err != nil {
			return err
		}
		if column.Grain != nil {
			resolved, err := resolveExplorationDimension(model, column.Field)
			if err != nil {
				return fmt.Errorf("invalid pivot column %q: %w", column.Field, err)
			}
			if err := validateModelGrain(model, column.Field, resolved, *column.Grain); err != nil {
				return err
			}
		}
		if err := add(column.Field, stringPointerValue(column.Alias)); err != nil {
			return err
		}
	}
	for _, metric := range pivot.Metrics {
		if err := model.ValidateAggregateMember(metric.Field); err != nil {
			return fmt.Errorf("invalid pivot metric %q: %w", metric.Field, err)
		}
		if err := validateAlias(metric.Alias, "pivot metric", metric.Field); err != nil {
			return err
		}
		if err := add(metric.Field, stringPointerValue(metric.Alias)); err != nil {
			return err
		}
	}
	if pivot.Sort != nil {
		for index, sort := range *pivot.Sort {
			if err := validateSort(sort, pivotSelected, index); err != nil {
				return fmt.Errorf("invalid pivot sort: %w", err)
			}
		}
	}
	if pivot.Window != nil {
		if pivot.Window.Limit < 1 || pivot.Window.Limit > 1000 {
			return fmt.Errorf("pivot window limit %d is outside 1..1000", pivot.Window.Limit)
		}
		if pivot.Window.Offset != nil && *pivot.Window.Offset < 0 {
			return errors.New("pivot window offset cannot be negative")
		}
	}
	return nil
}
