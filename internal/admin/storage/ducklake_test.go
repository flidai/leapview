package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/analytics/catalogstats"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

func TestFormatDuckLakeUUID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value []byte
		want  string
	}{
		{
			name:  "binary UUID",
			value: []byte{0x01, 0xb5, 0xee, 0x02, 0xaa, 0x5d, 0x73, 0x53, 0x99, 0x20, 0x1e, 0xa7, 0x1f, 0x67, 0x88, 0xfe},
			want:  "01b5ee02-aa5d-7353-9920-1ea71f6788fe",
		},
		{
			name:  "text UUID",
			value: []byte("01B5EE02-AA5D-7353-9920-1EA71F6788FE"),
			want:  "01b5ee02-aa5d-7353-9920-1ea71f6788fe",
		},
		{
			name:  "compact text UUID",
			value: []byte("01b5ee02aa5d735399201ea71f6788fe"),
			want:  "01b5ee02-aa5d-7353-9920-1ea71f6788fe",
		},
		{
			name:  "unexpected binary value",
			value: []byte{0xff, 0x00},
			want:  "ff00",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := formatDuckLakeUUID(test.value); got != test.want {
				t.Fatalf("formatDuckLakeUUID() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestServiceReadsActiveRuntimeCatalogForProduction(t *testing.T) {
	tables := []catalogstats.Table{{
		Schema: "model", Name: "orders", RowCount: 42, ColumnCount: 3,
		FileCount: 2, SizeBytes: 4096, SnapshotID: 17,
	}}
	provider := &storageRuntimeProvider{runtime: &storageRuntimeStub{tables: tables}}
	service := Service{Runtime: provider, CatalogPath: "/must/not/be/read", DataPath: "/must/not/be/read"}

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

func (s *storageRuntimeStub) Close() error { return nil }
func (s *storageRuntimeStub) CatalogTableStatistics(context.Context) ([]catalogstats.Table, error) {
	return s.tables, nil
}
