package storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

	ui "github.com/flidai/leapview/internal/admin/view"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
	"github.com/flidai/leapview/internal/workload"
)

type Service struct {
	CatalogPath string
	DataPath    string
	Analytics   AnalyticalProvider
	Admitter    workload.Admitter
}

type AnalyticalProvider interface {
	analyticsresource.Provider
	analyticsresource.SessionProvider
}

func (s Service) Data(ctx context.Context) ui.AdminStorageData {
	data := ui.AdminStorageData{}
	if strings.TrimSpace(s.CatalogPath) == "" {
		data.Status = "No DuckLake catalog has been initialized."
		return data
	}
	catalogInfo, err := os.Stat(s.CatalogPath)
	if err != nil {
		if os.IsNotExist(err) {
			data.Status = "No DuckLake catalog has been initialized."
		} else {
			data.Status = fmt.Sprintf("DuckLake catalog cannot be read: %v", err)
		}
		return data
	}
	if catalogInfo.IsDir() {
		data.Status = "DuckLake catalog path is a directory."
		return data
	}
	if strings.TrimSpace(s.DataPath) == "" {
		data.Status = "DuckLake data path is not configured."
		return data
	}

	ctx, analytics, release, err := s.acquireAnalytics(ctx, "admin.storage.read")
	if err != nil {
		data.Status = err.Error()
		return data
	}
	defer release()
	tables, err := inspectDuckLakeTables(ctx, analytics)
	if err != nil {
		data.Status = err.Error()
		return data
	}
	summary, err := inspectDuckLakeSummary(ctx, analytics)
	if err != nil {
		data.Status = err.Error()
		return data
	}
	data.Tables = tables
	data.TableCount = len(tables)
	data.DataFileCount = summary.DataFileCount
	data.TotalDataSizeBytes = summary.TotalDataSizeBytes
	data.TotalDataSizeLabel = formatBytes(summary.TotalDataSizeBytes)
	return data
}

func (s Service) Table(ctx context.Context, schema, tableName string) (*ui.AdminStorageTable, error) {
	if strings.TrimSpace(schema) == "" || strings.TrimSpace(tableName) == "" {
		return nil, fmt.Errorf("storage table selection is incomplete")
	}
	if strings.TrimSpace(s.CatalogPath) == "" || strings.TrimSpace(s.DataPath) == "" {
		return nil, fmt.Errorf("DuckLake catalog is not configured")
	}
	ctx, analytics, release, err := s.acquireAnalytics(ctx, "admin.storage.table.read")
	if err != nil {
		return nil, err
	}
	defer release()
	table, err := inspectDuckLakeTable(ctx, analytics, schema, tableName)
	if err != nil {
		return nil, err
	}
	table.Files, err = inspectDuckLakeFiles(ctx, analytics, table.TableID)
	if err != nil {
		return nil, err
	}
	return table, nil
}

func (s Service) acquireAnalytics(ctx context.Context, operation string) (context.Context, analyticsresource.Session, func(), error) {
	if s.Admitter == nil || s.Analytics == nil {
		return ctx, nil, func() {}, fmt.Errorf("DuckLake analytical session is not configured")
	}
	workloadLease, err := s.Admitter.Acquire(ctx, workload.Request{Class: workload.Control, PrincipalID: "system:admin-storage", Operation: operation, EstimatedMemoryBytes: 64 << 20})
	if err != nil {
		return ctx, nil, func() {}, err
	}
	analyticalLease, err := s.Analytics.Acquire(workloadLease.Context())
	if err != nil {
		workloadLease.Release()
		return ctx, nil, func() {}, err
	}
	session, err := s.Analytics.Session(analyticalLease.Context())
	if err != nil {
		analyticalLease.Release()
		workloadLease.Release()
		return ctx, nil, func() {}, err
	}
	return analyticalLease.Context(), session, func() {
		analyticalLease.Release()
		workloadLease.Release()
	}, nil
}

type queryDatabase interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type duckLakeStorageSummary struct {
	DataFileCount      int
	TotalDataSizeBytes int64
}

func inspectDuckLakeSummary(ctx context.Context, db queryDatabase) (duckLakeStorageSummary, error) {
	row := db.QueryRowContext(ctx, `
SELECT
	(SELECT count(*) FROM __ducklake_metadata_lake.ducklake_data_file WHERE end_snapshot IS NULL),
	(SELECT coalesce(sum(file_size_bytes), 0) FROM __ducklake_metadata_lake.ducklake_data_file WHERE end_snapshot IS NULL)`)
	var summary duckLakeStorageSummary
	if err := row.Scan(&summary.DataFileCount, &summary.TotalDataSizeBytes); err != nil {
		return duckLakeStorageSummary{}, duckLakeMetadataError(err)
	}
	return summary, nil
}

func inspectDuckLakeTables(ctx context.Context, db queryDatabase) ([]ui.AdminStorageTable, error) {
	rows, err := db.QueryContext(ctx, `
WITH active_tables AS (
	SELECT s.schema_name, s.path AS schema_path, t.table_name, t.path AS table_path, t.table_id, t.table_uuid, t.begin_snapshot, t.end_snapshot
	FROM __ducklake_metadata_lake.ducklake_table t
	JOIN __ducklake_metadata_lake.ducklake_schema s ON s.schema_id = t.schema_id
	WHERE t.end_snapshot IS NULL
), file_rollup AS (
	SELECT table_id, count(*) AS file_count, coalesce(sum(record_count), 0) AS row_count, coalesce(sum(file_size_bytes), 0) AS byte_count
	FROM __ducklake_metadata_lake.ducklake_data_file
	WHERE end_snapshot IS NULL
	GROUP BY table_id
), column_rollup AS (
	SELECT table_id, count(*) AS column_count
	FROM __ducklake_metadata_lake.ducklake_column
	WHERE end_snapshot IS NULL AND parent_column IS NULL
	GROUP BY table_id
)
SELECT a.schema_name, a.schema_path, a.table_name, a.table_path, a.table_id, a.table_uuid, a.begin_snapshot, a.end_snapshot,
       coalesce(f.row_count, 0), coalesce(c.column_count, 0), coalesce(f.file_count, 0), coalesce(f.byte_count, 0)
FROM active_tables a
LEFT JOIN file_rollup f ON f.table_id = a.table_id
LEFT JOIN column_rollup c ON c.table_id = a.table_id
ORDER BY a.schema_name, a.table_name`)
	if err != nil {
		return nil, duckLakeMetadataError(err)
	}
	defer rows.Close()

	var tables []ui.AdminStorageTable
	for rows.Next() {
		var schemaName, schemaPath, tableName, tablePath string
		var tableUUID []byte
		var tableID, beginSnapshot, rowCount, sizeBytes int64
		var endSnapshot sql.NullInt64
		var columnCount, fileCount int
		if err := rows.Scan(&schemaName, &schemaPath, &tableName, &tablePath, &tableID, &tableUUID, &beginSnapshot, &endSnapshot, &rowCount, &columnCount, &fileCount, &sizeBytes); err != nil {
			return nil, err
		}
		end := int64(0)
		if endSnapshot.Valid {
			end = endSnapshot.Int64
		}
		tables = append(tables, ui.AdminStorageTable{
			Schema: schemaName, Name: tableName, Type: "table", TableID: tableID, TableUUID: formatDuckLakeUUID(tableUUID),
			DuckLakePath: duckLakeTablePath(schemaPath, tablePath), BeginSnapshot: beginSnapshot, EndSnapshot: end,
			RowCount: rowCount, RowCountLabel: formatCount(rowCount), ColumnCount: columnCount,
			FileCount: fileCount, SizeBytes: sizeBytes, SizeLabel: formatBytes(sizeBytes),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

func inspectDuckLakeTable(ctx context.Context, db queryDatabase, schema, tableName string) (*ui.AdminStorageTable, error) {
	tables, err := inspectDuckLakeTables(ctx, db)
	if err != nil {
		return nil, err
	}
	for i := range tables {
		if tables[i].Schema == schema && tables[i].Name == tableName {
			return &tables[i], nil
		}
	}
	return nil, sql.ErrNoRows
}

func formatDuckLakeUUID(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	if len(value) == 16 {
		encoded := hex.EncodeToString(value)
		return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
	}

	text := strings.ToLower(strings.TrimSpace(string(value)))
	compact := strings.ReplaceAll(text, "-", "")
	if len(compact) == 32 {
		if _, err := hex.DecodeString(compact); err == nil {
			return compact[:8] + "-" + compact[8:12] + "-" + compact[12:16] + "-" + compact[16:20] + "-" + compact[20:]
		}
	}
	return hex.EncodeToString(value)
}

func inspectDuckLakeFiles(ctx context.Context, db queryDatabase, tableID int64) ([]ui.AdminStorageFile, error) {
	rows, err := db.QueryContext(ctx, `
SELECT data_file_id, path, file_format, record_count, file_size_bytes, begin_snapshot, end_snapshot
FROM __ducklake_metadata_lake.ducklake_data_file
WHERE table_id = ? AND end_snapshot IS NULL
ORDER BY file_order, data_file_id`, tableID)
	if err != nil {
		return nil, duckLakeMetadataError(err)
	}
	defer rows.Close()
	var files []ui.AdminStorageFile
	for rows.Next() {
		var file ui.AdminStorageFile
		var endSnapshot sql.NullInt64
		if err := rows.Scan(&file.ID, &file.Path, &file.Format, &file.RecordCount, &file.SizeBytes, &file.BeginSnapshot, &endSnapshot); err != nil {
			return nil, err
		}
		if endSnapshot.Valid {
			file.EndSnapshot = endSnapshot.Int64
		}
		file.RecordCountLabel = formatCount(file.RecordCount)
		file.SizeLabel = formatBytes(file.SizeBytes)
		files = append(files, file)
	}
	return files, rows.Err()
}

func duckLakeTablePath(schemaPath, tablePath string) string {
	schemaPath = strings.TrimSpace(schemaPath)
	tablePath = strings.TrimSpace(tablePath)
	if schemaPath == "" {
		return tablePath
	}
	if tablePath == "" {
		return schemaPath
	}
	return strings.TrimRight(schemaPath, "/") + "/" + strings.TrimLeft(tablePath, "/")
}

func duckLakeMetadataError(err error) error {
	if isMissingSQLiteTableError(err) {
		return fmt.Errorf("No DuckLake catalog has been initialized.")
	}
	return err
}

func isMissingSQLiteTableError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such table") || strings.Contains(message, "does not exist")
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
