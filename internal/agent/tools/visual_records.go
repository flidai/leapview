package tools

import (
	"fmt"

	"github.com/flidai/leapview/internal/dashboard/document"
)

// Require the explicit physical-field form for agent records. The string
// shorthand can collide with semantic metrics, and consulting metric names
// here would disclose protected members before field authorization.
func validateAgentRecordsFields(visual document.DashboardVisual) error {
	query, ok := visual.Query.Value.(*document.RecordsDashboardQuery)
	if !ok {
		return nil
	}
	for _, selection := range query.Fields {
		if selection.String != nil {
			return fmt.Errorf("agent records queries require explicit physical field objects such as {\"field\":\"column_name\"}; use an aggregate query for semantic metrics")
		}
	}
	return nil
}
