package authoring

import (
	"fmt"

	"github.com/flidai/leapview/internal/dashboard"
)

func validateTableStyle(name string, style dashboard.TableStyle) error {
	switch style.Density {
	case "", "compact", "comfortable", "spacious":
	default:
		return fmt.Errorf("table %q has unsupported density %q", name, style.Density)
	}
	switch style.Grid {
	case "", "none", "rows", "columns", "full":
	default:
		return fmt.Errorf("table %q has unsupported grid %q", name, style.Grid)
	}
	return nil
}

func validateTableColumn(tableName string, column dashboard.TableColumn) error {
	switch column.Format {
	case "", "text", "integer", "decimal", "currency", "days":
	default:
		return fmt.Errorf("table %q column %q has unsupported format %q", tableName, column.Key, column.Format)
	}
	for _, rule := range column.Formatting {
		if err := validateTableFormattingRule(tableName, column.Key, rule); err != nil {
			return err
		}
	}
	return nil
}

func validateTableFormattingRule(tableName, field string, rule dashboard.TableFormattingRule) error {
	switch rule.Kind {
	case "badge", "text_color", "background_scale", "data_bar":
	default:
		return fmt.Errorf("table %q column %q has unsupported formatting kind %q", tableName, field, rule.Kind)
	}
	return nil
}

func validatePlacement(page dashboard.Page, visual dashboard.PageVisual) error {
	placement := visual.Placement
	if placement.IsZero() {
		return fmt.Errorf("page %q visual %q requires placement", page.ID, visual.ID)
	}
	if placement.Col <= 0 || placement.Row <= 0 || placement.ColSpan <= 0 || placement.RowSpan <= 0 {
		return fmt.Errorf("page %q visual %q has invalid placement", page.ID, visual.ID)
	}
	if placement.Col+placement.ColSpan-1 > page.Grid.Columns {
		return fmt.Errorf("page %q visual %q placement exceeds %d grid columns", page.ID, visual.ID, page.Grid.Columns)
	}
	return nil
}
