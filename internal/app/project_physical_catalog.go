package app

import (
	"context"
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projecthttp "github.com/flidai/leapview/internal/project/http"
	"github.com/flidai/leapview/internal/runtimehost"
)

type activeProjectPhysicalCatalog struct {
	provider runtimehost.Provider
}

func (r activeProjectPhysicalCatalog) ModelPhysicalMetadata(ctx context.Context, projectID projectgraph.ResourceID, environment string) (map[string]projecthttp.ModelPhysicalMetadata, error) {
	if r.provider == nil {
		return nil, fmt.Errorf("active runtime provider is unavailable")
	}
	lease, err := r.provider.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer lease.Release()
	identity := lease.Identity()
	if identity.ProjectID != projectID || identity.Environment != strings.TrimSpace(environment) {
		return nil, fmt.Errorf("active runtime identity does not match requested catalog scope")
	}
	reader, ok := lease.Runtime().(analyticsmodule.CatalogStatisticsReader)
	if !ok {
		return nil, fmt.Errorf("active runtime does not expose physical catalog metadata")
	}
	tables, err := reader.CatalogTableStatistics(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]projecthttp.ModelPhysicalMetadata, len(tables))
	for _, table := range tables {
		if table.Schema != "model" || strings.TrimSpace(table.Name) == "" {
			continue
		}
		out[table.Name] = projecthttp.ModelPhysicalMetadata{
			RowCount: table.RowCount, ColumnCount: table.ColumnCount, FileCount: table.FileCount,
			SizeBytes: table.SizeBytes, SnapshotID: table.SnapshotID, SnapshotAt: table.SnapshotAt,
			Schema: semanticmodel.TableSchema{Columns: append([]semanticmodel.ColumnSchema(nil), table.Columns...)},
		}
	}
	return out, nil
}
