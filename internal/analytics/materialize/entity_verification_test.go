package materialize

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
)

type entityVerificationDatabase struct{ db *sql.DB }

func (d entityVerificationDatabase) Exec(ctx context.Context, statement string) error {
	_, err := d.db.ExecContext(ctx, statement)
	return err
}
func (d entityVerificationDatabase) Close() error { return d.db.Close() }
func (d entityVerificationDatabase) Path() string { return ":memory:" }
func (d entityVerificationDatabase) Session(context.Context) (analyticsresource.Session, error) {
	return d.db, nil
}

func entityVerificationModel() *semanticmodel.Model {
	return &semanticmodel.Model{Tables: map[string]semanticmodel.Table{
		"orders": {
			Entities: map[string]semanticmodel.ModelEntitySpec{
				"order":    {Type: "primary", Fields: []string{"order_id", "line_no"}},
				"external": {Type: "unique", Fields: []string{"line_no"}},
			},
			GrainEntity: "order",
			Schema: semanticmodel.TableSchema{Columns: []semanticmodel.ColumnSchema{
				{Name: "order_id", PhysicalType: "BIGINT"}, {Name: "line_no", PhysicalType: "BIGINT"},
			}},
		},
	}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}}
}

func TestVerifyEntityClaimsRejectsNullCompositeKey(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	runtimeDB := entityVerificationDatabase{db: db}
	defer runtimeDB.Close()
	if err := runtimeDB.Exec(context.Background(), "CREATE SCHEMA model; CREATE TABLE model.orders (order_id BIGINT, line_no BIGINT); INSERT INTO model.orders VALUES (1, NULL)"); err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{model: entityVerificationModel(), db: runtimeDB}
	if err := runtime.VerifyEntityClaims(context.Background()); err == nil || !strings.Contains(err.Error(), "null key field") {
		t.Fatal("VerifyEntityClaims accepted null composite key")
	}
}

func TestVerifyEntityClaimsRejectsDuplicateCompositeKey(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	runtimeDB := entityVerificationDatabase{db: db}
	defer runtimeDB.Close()
	if err := runtimeDB.Exec(context.Background(), "CREATE SCHEMA model; CREATE TABLE model.orders (order_id BIGINT, line_no BIGINT); INSERT INTO model.orders VALUES (1, 1), (1, 1)"); err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{model: entityVerificationModel(), db: runtimeDB}
	if err := runtime.VerifyEntityClaims(context.Background()); err == nil || !strings.Contains(err.Error(), "duplicate key tuple") {
		t.Fatal("VerifyEntityClaims accepted duplicate composite key")
	}
}

func TestVerifyEntityClaimsAcceptsCompositeAndUniqueEntities(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	runtimeDB := entityVerificationDatabase{db: db}
	defer runtimeDB.Close()
	if err := runtimeDB.Exec(context.Background(), "CREATE SCHEMA model; CREATE TABLE model.orders (order_id BIGINT, line_no BIGINT); INSERT INTO model.orders VALUES (1, 1), (1, 2)"); err != nil {
		t.Fatal(err)
	}
	model := entityVerificationModel()
	planner, err := semanticquery.NewCompiledPlanner(model, semanticquery.WithTableRelation(func(string) (string, error) { return "model.orders", nil }))
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{model: model, planner: planner, db: runtimeDB}
	if err := runtime.VerifyEntityClaims(context.Background()); err != nil {
		t.Fatalf("VerifyEntityClaims() error = %v", err)
	}
}
