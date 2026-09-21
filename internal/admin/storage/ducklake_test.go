package storage

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/catalogstats"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

func TestServiceReadsActiveRuntimeCatalogForProduction(t *testing.T) {
	tables := []catalogstats.Table{{
		Schema: "model", Name: "orders", RowCount: 42, ColumnCount: 3,
		FileCount: 2, SizeBytes: 4096, SnapshotID: 17,
	}}
	provider := &storageRuntimeProvider{runtime: &storageRuntimeStub{tables: tables}}
	service := Service{Runtime: provider}

	data := service.Data(context.Background())
	if data.Status != "" {
		t.Fatalf("Data() status = %q, want empty", data.Status)
	}
	if data.TableCount != 1 || data.DataFileCount != 2 || data.TotalDataSizeBytes != 4096 {
		t.Fatalf("Data() summary = %#v", data)
	}
	if len(data.Tables) != 1 || data.Tables[0].Schema != "model" || data.Tables[0].Name != "orders" || data.Tables[0].BeginSnapshot != 17 {
		t.Fatalf("Data() tables = %#v", data.Tables)
	}

	table, err := service.Table(context.Background(), "model", "orders")
	if err != nil {
		t.Fatal(err)
	}
	if table.RowCount != 42 || table.FileCount != 2 || table.SizeBytes != 4096 {
		t.Fatalf("Table() = %#v", table)
	}
	if _, err := service.Table(context.Background(), "model", "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing Table() error = %v, want sql.ErrNoRows", err)
	}
}

func TestServiceDoesNotFallBackWithoutActiveRuntime(t *testing.T) {
	service := Service{}

	data := service.Data(context.Background())
	if data.Status != storageInactiveStatus {
		t.Fatalf("Data() status = %q, want no-active-runtime status", data.Status)
	}
	if data.TableCount != 0 || data.DataFileCount != 0 || data.TotalDataSizeBytes != 0 || len(data.Tables) != 0 {
		t.Fatalf("Data() without runtime = %#v, want empty data", data)
	}

	if _, err := service.Table(context.Background(), "model", "orders"); !errors.Is(err, errNoActiveRuntime) {
		t.Fatalf("Table() error = %v, want errNoActiveRuntime", err)
	}
}

func TestServiceDoesNotExposeCatalogErrorsInStatus(t *testing.T) {
	const technical = `Catalog Error: Table with name ducklake_table does not exist (SELECT * FROM secret_relation)`
	provider := &storageRuntimeProvider{runtime: &storageRuntimeErrorStub{err: errors.New(technical)}}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelError}))
	service := Service{Runtime: provider, Logger: logger}

	data := service.Data(context.Background())
	if data.Status != storageUnavailableStatus {
		t.Fatalf("Data() status = %q, want bounded unavailable status", data.Status)
	}
	if strings.Contains(data.Status, "ducklake_table") || strings.Contains(data.Status, "SELECT") {
		t.Fatalf("Data() leaked technical catalog details: %q", data.Status)
	}
	if !strings.Contains(logs.String(), "ducklake_table") || !strings.Contains(logs.String(), "read_catalog") {
		t.Fatalf("technical catalog diagnostic was not logged: %q", logs.String())
	}
}

type storageRuntimeProvider struct {
	runtime projectruntime.Runtime
}

func (p *storageRuntimeProvider) Acquire(context.Context) (projectruntime.Lease, error) {
	return &storageRuntimeLease{runtime: p.runtime}, nil
}

type storageRuntimeLease struct {
	runtime projectruntime.Runtime
}

func (l *storageRuntimeLease) Runtime() projectruntime.Runtime { return l.runtime }
func (*storageRuntimeLease) Identity() projectgraph.ServingIdentity {
	return projectgraph.ServingIdentity{}
}
func (*storageRuntimeLease) Release() {}

type storageRuntimeStub struct {
	tables []catalogstats.Table
}

type storageRuntimeErrorStub struct{ err error }

func (s *storageRuntimeErrorStub) Close() error { return nil }
func (s *storageRuntimeErrorStub) CatalogTableStatistics(context.Context) ([]catalogstats.Table, error) {
	return nil, s.err
}

func (s *storageRuntimeStub) Close() error { return nil }
func (s *storageRuntimeStub) CatalogTableStatistics(context.Context) ([]catalogstats.Table, error) {
	return s.tables, nil
}
