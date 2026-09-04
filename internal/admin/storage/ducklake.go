package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	ui "github.com/flidai/leapview/internal/admin/view"
	"github.com/flidai/leapview/internal/analytics/catalogstats"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

type Service struct {
	// Runtime is the active serving-generation provider. Production storage
	// reads must come from this provider so PostgreSQL-backed DuckLake metadata
	// is inspected through the exact sealed snapshot admitted to the runtime.
	Runtime projectruntime.Provider
}

var errNoActiveRuntime = errors.New("no active LeapView serving state")

func (s Service) Data(ctx context.Context) ui.AdminStorageData {
	data := ui.AdminStorageData{}
	reader, release, err := s.acquireReader(ctx)
	if err != nil {
		data.Status = err.Error()
		return data
	}
	defer release()
	tables, err := reader.CatalogTableStatistics(ctx)
	if err != nil {
		data.Status = err.Error()
		return data
	}
	if len(tables) > maxStorageTables {
		data.Status = fmt.Sprintf("DuckLake catalog contains more than %d tables.", maxStorageTables)
		return data
	}
	data.Tables = storageTablesFromStatistics(tables)
	data.TableCount = len(data.Tables)
	for _, table := range data.Tables {
		data.DataFileCount += table.FileCount
		data.TotalDataSizeBytes += table.SizeBytes
	}
	data.TotalDataSizeLabel = formatBytes(data.TotalDataSizeBytes)
	return data
}

func (s Service) Table(ctx context.Context, schema, tableName string) (*ui.AdminStorageTable, error) {
	if strings.TrimSpace(schema) == "" || strings.TrimSpace(tableName) == "" {
		return nil, fmt.Errorf("storage table selection is incomplete")
	}
	reader, release, err := s.acquireReader(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	tables, err := reader.CatalogTableStatistics(ctx)
	if err != nil {
		return nil, err
	}
	if len(tables) > maxStorageTables {
		return nil, fmt.Errorf("DuckLake catalog contains more than %d tables", maxStorageTables)
	}
	for _, stats := range tables {
		if stats.Schema != schema || stats.Name != tableName {
			continue
		}
		table := storageTableFromStatistics(stats)
		return &table, nil
	}
	return nil, sql.ErrNoRows
}

func (s Service) acquireReader(ctx context.Context) (catalogstats.Reader, func(), error) {
	if s.Runtime == nil {
		return nil, func() {}, errNoActiveRuntime
	}
	lease, err := s.Runtime.Acquire(ctx)
	if err != nil {
		return nil, func() {}, err
	}
	if lease == nil {
		return nil, func() {}, fmt.Errorf("active DuckLake runtime lease is unavailable")
	}
	release := lease.Release
	runtime := lease.Runtime()
	reader, ok := runtime.(catalogstats.Reader)
	if !ok {
		release()
		return nil, func() {}, fmt.Errorf("active DuckLake runtime does not expose catalog metadata")
	}
	return reader, release, nil
}

const maxStorageTables = 10000

func storageTablesFromStatistics(tables []catalogstats.Table) []ui.AdminStorageTable {
	result := make([]ui.AdminStorageTable, 0, len(tables))
	for _, stats := range tables {
		result = append(result, storageTableFromStatistics(stats))
	}
	return result
}

func storageTableFromStatistics(stats catalogstats.Table) ui.AdminStorageTable {
	return ui.AdminStorageTable{
		Schema: stats.Schema, Name: stats.Name, Type: "table",
		BeginSnapshot: stats.SnapshotID, RowCount: stats.RowCount,
		RowCountLabel: formatCount(stats.RowCount), ColumnCount: int(stats.ColumnCount),
		FileCount: int(stats.FileCount), SizeBytes: stats.SizeBytes,
		SizeLabel: formatBytes(stats.SizeBytes),
	}
}

func formatBytes(bytes int64) string {
	if bytes < 0 {
		return "-"
	}
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PiB", value/unit)
}

func formatCount(value int64) string {
	if value < 0 {
		return "-"
	}
	parts := []string{}
	for value >= 1000 {
		parts = append(parts, fmt.Sprintf("%03d", value%1000))
		value /= 1000
	}
	parts = append(parts, strconv.FormatInt(value, 10))
	for left, right := 0, len(parts)-1; left < right; left, right = left+1, right-1 {
		parts[left], parts[right] = parts[right], parts[left]
	}
	return strings.Join(parts, ",")
}
