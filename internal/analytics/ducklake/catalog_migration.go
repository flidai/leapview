package ducklake

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/flidai/leapview/internal/extension"
)

const sqliteCatalogHeader = "SQLite format 3\x00"

// migrateLegacySQLiteCatalog upgrades the previous local SQLite-backed
// DuckLake metadata catalog before the process-owned DuckDB environment opens
// its catalog. The source remains as a backup when the target path changed.
func migrateLegacySQLiteCatalog(ctx context.Context, targetPath string, admission extension.Admission) (bool, error) {
	if admission == nil {
		return false, fmt.Errorf("DuckLake catalog migration requires extension admission")
	}
	targetPath = filepath.Clean(strings.TrimSpace(targetPath))
	if targetPath == "." || targetPath == "" {
		return false, fmt.Errorf("DuckLake catalog path is required")
	}
	targetSQLite, targetExists, err := sqliteCatalogFile(targetPath)
	if err != nil {
		return false, err
	}
	if targetExists && !targetSQLite {
		return false, nil
	}

	sourcePath := targetPath
	inPlace := targetExists && targetSQLite
	if !targetExists {
		if filepath.Base(targetPath) != "catalog.duckdb" {
			return false, nil
		}
		sourcePath = filepath.Join(filepath.Dir(targetPath), "catalog.sqlite")
		sourceSQLite, sourceExists, sourceErr := sqliteCatalogFile(sourcePath)
		if sourceErr != nil {
			return false, sourceErr
		}
		if !sourceExists {
			return false, nil
		}
		if !sourceSQLite {
			return false, fmt.Errorf("legacy DuckLake catalog %q is not a SQLite database", sourcePath)
		}
	}

	unlock := lockCatalogWrites(targetPath)
	defer unlock()
	// Recheck after acquiring the process-wide catalog lock in case another
	// environment completed migration while this caller was waiting.
	if targetSQLite, targetExists, err = sqliteCatalogFile(targetPath); err != nil {
		return false, err
	} else if targetExists && !targetSQLite {
		return false, nil
	}

	temporary, err := os.CreateTemp(filepath.Dir(targetPath), ".catalog-migration-*.duckdb")
	if err != nil {
		return false, fmt.Errorf("create DuckLake catalog migration target: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return false, err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return false, err
	}
	defer os.Remove(temporaryPath)

	if err := copySQLiteCatalogToDuckDB(ctx, sourcePath, temporaryPath, admission); err != nil {
		return false, err
	}
	if err := os.Chmod(temporaryPath, catalogFileMode); err != nil {
		return false, fmt.Errorf("secure migrated DuckLake catalog: %w", err)
	}
	if err := syncFile(temporaryPath); err != nil {
		return false, err
	}

	if inPlace {
		backupPath := targetPath + ".legacy.sqlite"
		if _, err := os.Stat(backupPath); err == nil {
			return false, fmt.Errorf("legacy DuckLake catalog backup already exists at %q", backupPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		if err := os.Rename(targetPath, backupPath); err != nil {
			return false, fmt.Errorf("preserve legacy DuckLake catalog: %w", err)
		}
		if err := os.Rename(temporaryPath, targetPath); err != nil {
			_ = os.Rename(backupPath, targetPath)
			return false, fmt.Errorf("activate migrated DuckLake catalog: %w", err)
		}
	} else if err := os.Rename(temporaryPath, targetPath); err != nil {
		return false, fmt.Errorf("activate migrated DuckLake catalog: %w", err)
	}
	if err := syncCatalogDirectory(filepath.Dir(targetPath)); err != nil {
		return false, err
	}
	return true, nil
}

func copySQLiteCatalogToDuckDB(ctx context.Context, sourcePath, targetPath string, admission extension.Admission) error {
	if admission == nil {
		return fmt.Errorf("DuckLake catalog migration requires extension admission")
	}
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	statements := make([]string, 0, 4)
	for _, name := range []string{"sqlite"} {
		admitted, admitErr := admission.AdmitExtension(ctx, name)
		if admitErr != nil {
			_ = db.Close()
			return fmt.Errorf("admit %s extension for DuckLake catalog migration: %w", name, admitErr)
		}
		if err := validateAdmittedExtension(admitted, name); err != nil {
			_ = db.Close()
			return fmt.Errorf("validate %s extension for DuckLake catalog migration: %w", name, err)
		}
		statements = append(statements, "LOAD '"+sqlLiteral(admitted.Path)+"'")
	}
	statements = append(statements,
		fmt.Sprintf("ATTACH '%s' AS legacy_catalog (TYPE sqlite)", sqlLiteral(sourcePath)),
		fmt.Sprintf("ATTACH '%s' AS migrated_catalog", sqlLiteral(targetPath)),
		"COPY FROM DATABASE legacy_catalog TO migrated_catalog",
		"CHECKPOINT migrated_catalog",
	)
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			_ = db.Close()
			return fmt.Errorf("migrate legacy DuckLake catalog: %w", err)
		}
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close migrated DuckLake catalog: %w", err)
	}
	return nil
}

func sqliteCatalogFile(path string) (sqlite bool, exists bool, err error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	defer file.Close()
	header := make([]byte, len(sqliteCatalogHeader))
	if _, err := io.ReadFull(file, header); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, true, err
	}
	return string(header) == sqliteCatalogHeader, true, nil
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	err = file.Sync()
	return errors.Join(err, file.Close())
}

func syncCatalogDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	return errors.Join(err, directory.Close())
}
