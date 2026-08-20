package duckdbsql

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestDecodePlanExtractsTypedScans(t *testing.T) {
	payload := `[
		{
			"name": "PROJECTION",
			"children": [
				{
					"name": "SEQ_SCAN",
					"extra_info": {
						"Table": "memory.\"source\".orders",
						"Projections": ["status", "order_id"]
					}
				},
				{
					"name": "SEQ_SCAN",
					"extra_info": {
						"Table": "memory.source.payments",
						"Projections": "order_id\npayment_value"
					}
				}
			]
		}
	]`

	got, err := decodePlan(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Scans) != 2 {
		t.Fatalf("scan count = %d, want 2", len(got.Scans))
	}
	if got.Scans[0].Catalog != "memory" || got.Scans[0].Schema != "source" || got.Scans[0].Table != "orders" {
		t.Fatalf("first scan = %#v", got.Scans[0])
	}
	if strings.Join(got.Scans[0].Projections, ",") != "status,order_id" {
		t.Fatalf("first projections = %#v", got.Scans[0].Projections)
	}
	if got.Scans[1].Table != "payments" || strings.Join(got.Scans[1].Projections, ",") != "order_id,payment_value" {
		t.Fatalf("second scan = %#v", got.Scans[1])
	}
}

func TestDecodePlanKeepsScanWithoutProjections(t *testing.T) {
	got, err := decodePlan(`[{"name":"SEQ_SCAN","extra_info":{"Table":"source.orders","Estimated Cardinality":"1"}}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Scans) != 1 || got.Scans[0].Schema != "source" || got.Scans[0].Table != "orders" {
		t.Fatalf("scans = %#v, want source.orders", got.Scans)
	}
	if len(got.Scans[0].Projections) != 0 {
		t.Fatalf("projections = %#v, want empty", got.Scans[0].Projections)
	}
}

func TestDecodePlanRejectsMalformedOrUnknownShape(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "empty roots", payload: `[]`, want: "no roots"},
		{name: "unknown node field", payload: `[{"name":"SEQ_SCAN","unexpected":true}]`, want: "unknown node field"},
		{name: "missing node name", payload: `[{"children":[]}]`, want: "missing name"},
		{name: "wrong table type", payload: `[{"name":"SEQ_SCAN","extra_info":{"Table":17}}]`, want: "Table must be a string"},
		{name: "wrong projections type", payload: `[{"name":"SEQ_SCAN","extra_info":{"Table":"orders","Projections":17}}]`, want: "Projections must be"},
		{name: "trailing value", payload: `[{"name":"SEQ_SCAN"}] {}`, want: "trailing values"},
		{name: "invalid JSON", payload: `[{`, want: "decoding plan JSON"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodePlan(test.payload)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decodePlan() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDecodePlanRejectsExcessiveDepth(t *testing.T) {
	payload := "["
	for index := 0; index < maxPlanDepth+2; index++ {
		payload += "{\"name\":\"NODE\",\"children\":["
	}
	payload += "{\"name\":\"LEAF\"}"
	for index := 0; index < maxPlanDepth+2; index++ {
		payload += "]}"
	}
	payload += "]"
	_, err := decodePlan(payload)
	if err == nil || !strings.Contains(err.Error(), "nesting exceeds") {
		t.Fatalf("decodePlan() error = %v, want nesting bound", err)
	}
}

func TestAnalyzePlanRejectsNilOrEmptyRequest(t *testing.T) {
	if _, err := AnalyzePlan(context.Background(), nil, "SELECT 1"); err == nil || !strings.Contains(err.Error(), "nil plan querier") {
		t.Fatalf("nil querier error = %v", err)
	}
	querier := fakeQuerier{}
	if _, err := AnalyzePlan(context.Background(), querier, "  \n"); err == nil || !strings.Contains(err.Error(), "empty SQL") {
		t.Fatalf("empty SQL error = %v", err)
	}
}

func TestAnalyzePlanValidatesQueryBeforeIssuingExplain(t *testing.T) {
	for _, sqlText := range []string{
		"SELECT 1; SELECT 2",
		"CREATE TABLE should_not_reach_explain (id INTEGER)",
	} {
		querier := &recordingQuerier{}
		if _, err := AnalyzePlan(context.Background(), querier, sqlText); err == nil {
			t.Fatalf("AnalyzePlan(%q) unexpectedly succeeded", sqlText)
		}
		if querier.calls != 0 {
			t.Fatalf("AnalyzePlan(%q) issued EXPLAIN before validation", sqlText)
		}
	}
}

func TestAnalyzePlanAgainstPinnedDuckDB(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), "CREATE TABLE orders (id INTEGER, status VARCHAR)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), "INSERT INTO orders VALUES (1, 'open')"); err != nil {
		t.Fatal(err)
	}
	got, err := AnalyzePlan(context.Background(), db, "SELECT id FROM orders WHERE status = 'open'")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Scans) == 0 {
		t.Fatalf("plan scans = %#v, want a table scan", got.Scans)
	}
	found := false
	for _, scan := range got.Scans {
		if scan.Table == "orders" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("plan scans = %#v, want orders scan", got.Scans)
	}
}

type fakeQuerier struct{}

func (fakeQuerier) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, nil
}

type recordingQuerier struct{ calls int }

func (querier *recordingQuerier) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	querier.calls++
	return nil, nil
}
