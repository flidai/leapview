package authz

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/workspace"
	workspacesqlite "github.com/flidai/leapview/internal/workspace/sqlite"
)

func BenchmarkGovernDataQueryWithPolicies(b *testing.B) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(b.TempDir(), "leapview.db"))
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "test", Title: "Test"}); err != nil {
		b.Fatalf("ensure workspace: %v", err)
	}
	repo := accesssqlite.NewRepository(store.SQLDB())
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "bench_query", Email: "bench-query@example.com"})
	if err != nil {
		b.Fatalf("upsert principal: %v", err)
	}
	model := access.ItemObject(access.SecurableSemanticModel, "test", "sales")
	dataset := access.ItemObjectWithParent(access.SecurableDataset, "test", "sales/orders", model)
	emailColumn := access.ItemObjectWithParent(access.SecurableColumn, "test", "sales/orders/email", dataset)
	statusColumn := access.ItemObjectWithParent(access.SecurableColumn, "test", "sales/orders/status", dataset)
	for _, object := range []access.ObjectRef{dataset, emailColumn, statusColumn} {
		if _, err := repo.UpsertSecurableObject(ctx, object, ""); err != nil {
			b.Fatalf("upsert securable: %v", err)
		}
	}
	if _, err := repo.CreateGrant(ctx, access.GrantInput{Object: dataset, SubjectType: access.SubjectPrincipal, SubjectID: principal.ID, Privilege: access.PrivilegePreviewData}); err != nil {
		b.Fatalf("create grant: %v", err)
	}
	rowFilter, _ := json.Marshal(map[string]any{"field": "orders.status", "operator": "eq", "values": []any{"complete"}})
	if _, err := repo.UpsertDataPolicy(ctx, access.DataPolicyInput{Object: dataset, PolicyType: "row_filter", ExpressionJSON: string(rowFilter)}); err != nil {
		b.Fatalf("upsert row filter: %v", err)
	}
	columnMask, _ := json.Marshal(map[string]any{"field": "orders.email", "mask": "redact"})
	if _, err := repo.UpsertDataPolicy(ctx, access.DataPolicyInput{Object: emailColumn, PolicyType: "column_mask", ExpressionJSON: string(columnMask)}); err != nil {
		b.Fatalf("upsert column mask: %v", err)
	}
	metrics := New(nil, Options{
		Repo: repo,
		PrincipalFromContext: func(context.Context) (Principal, bool) {
			return Principal{ID: principal.ID}, true
		},
	})
	request := dataquery.Query{
		WorkspaceID: "test",
		ModelID:     "sales",
		Kind:        dataquery.KindSemanticRows,
		Target:      "orders",
		Operation:   dataquery.OperationAPIPreview,
		Fields: []dataquery.Field{
			{Field: "orders.email", Alias: "email"},
			{Field: "orders.status", Alias: "status"},
		},
		Limit: 100,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		governed, _, err := metrics.GovernDataQuery(ctx, request)
		if err != nil {
			b.Fatalf("govern data query: %v", err)
		}
		if len(governed.Filters) != 1 || len(governed.ColumnMasks) != 1 {
			b.Fatalf("governed query = %#v, want row filter and column mask", governed)
		}
	}
}
