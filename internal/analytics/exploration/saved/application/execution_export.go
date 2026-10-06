package application

import (
	"fmt"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	saved "github.com/flidai/leapview/internal/analytics/exploration/saved"
)

// Export windows are independent of both the chart sample and browser scroll.
// One sentinel row proves completeness; the execution and encoder budgets reject
// an oversized result before any file bytes are delivered.
func explorationExportQuery(query dataquery.Query, maxRows int) (dataquery.Query, error) {
	if maxRows == 0 {
		maxRows = saved.ExportDefaultMaxRows
	}
	if maxRows < 1 || maxRows > saved.ExportMaximumMaxRows {
		return dataquery.Query{}, fmt.Errorf("%w: export row limit is out of bounds", saved.ErrExportInvalidRequest)
	}
	query.Offset, query.Limit = 0, maxRows+1
	return query, nil
}
