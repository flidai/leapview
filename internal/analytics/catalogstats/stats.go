// Package catalogstats defines the physical, generation-bound metadata that
// an analytical runtime may expose to catalog presentation surfaces.
package catalogstats

import (
	"context"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// Table describes one physical DuckLake table at the runtime's serving
// snapshot. It deliberately excludes file paths and storage credentials.
type Table struct {
	Schema      string
	Name        string
	RowCount    int64
	ColumnCount int64
	FileCount   int64
	SizeBytes   int64
	SnapshotID  int64
	Columns     []semanticmodel.ColumnSchema
}

// Reader is implemented by serving runtimes that can inspect their own
// immutable physical catalog.
type Reader interface {
	CatalogTableStatistics(context.Context) ([]Table, error)
}
