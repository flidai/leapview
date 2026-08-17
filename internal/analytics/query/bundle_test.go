package query

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	duckdb "github.com/duckdb/duckdb-go/v2"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestPlanBundleUsesOneStatementAndSharedMaterializedScanForDifferentShapes(t *testing.T) {
	bundle, err := mustNewCompiledPlanner(t, executableMultiDatasetModel()).PlanBundle([]BundleRequest{
		{ID: "kpi", Request: Request{Dataset: "orders", Metrics: []Field{{Field: "order_count", Alias: "value"}}, Filters: bundleConsumerFilter()}},
		{ID: "by_customer", Request: Request{Dataset: "orders", Dimensions: []Field{{Field: "customer", Alias: "label"}}, Metrics: []Field{{Field: "revenue", Alias: "value"}}, Filters: bundleConsumerFilter(), Sort: []Sort{{Field: "label", Direction: "asc"}}, Limit: 10}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sql := bundle.Plan.SQL
	if got := strings.Count(sql, "FROM model.orders"); got != 1 {
		t.Fatalf("dataset scans = %d, want 1:\n%s", got, sql)
	}
	if strings.Count(sql, "AS MATERIALIZED") != 1 || strings.Contains(sql, "CREATE TEMP") {
		t.Fatalf("bundle did not use one statement-local materialized scan:\n%s", sql)
	}
	explain, err := bundle.Plan.Explain()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[BundleBranches]", "limit=10", "branches=[{kpi 0", "{by_customer 1"} {
		if !strings.Contains(explain, want) {
			t.Fatalf("bundle PlanIR missing %q:\n%s", want, explain)
		}
	}
	if len(bundle.Plan.Args) != 2 || bundle.Plan.Args[0] != "consumer" || bundle.Plan.Args[1] != "consumer" {
		t.Fatalf("args = %#v", bundle.Plan.Args)
	}
}

func exactDecimalEqual(value any, want string) bool {
	var text string
	switch typed := value.(type) {
	case duckdb.Decimal:
		text = typed.String()
	case *duckdb.Decimal:
		if typed == nil {
			return false
		}
		text = typed.String()
	default:
		return false
	}
	normalize := func(input string) string {
		if !strings.Contains(input, ".") {
			return input
		}
		input = strings.TrimRight(input, "0")
		input = strings.TrimRight(input, ".")
		if input == "-0" {
			return "0"
		}
		return input
	}
	return normalize(text) == normalize(want)
}

func TestBundleAppliesNamedMetricWhereFiltersSingleDataset(t *testing.T) {
	model := executableMultiDatasetModel()
	model.Filters = map[string]semanticmodel.SemanticFilterSpec{
		"business_customers": {Field: "customers.state", Operator: "equals", Value: "business"},
	}
	filteredRevenue := model.Metrics["revenue"]
	filteredRevenue.Where = []string{"business_customers"}
	model.Metrics["business_revenue"] = filteredRevenue

	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id VARCHAR, customer_id VARCHAR, segment VARCHAR, amount DOUBLE)",
		"INSERT INTO model.orders VALUES ('o1', 'a', 'consumer', 10), ('o2', 'a', 'consumer', 20), ('o3', 'b', 'business', 30)",
		"CREATE TABLE model.customers(customer_id VARCHAR, state VARCHAR)",
		"INSERT INTO model.customers VALUES ('a', 'consumer'), ('b', 'business')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := mustNewCompiledPlanner(t, model).PlanBundle([]BundleRequest{
		{ID: "filtered_total", Request: Request{Dataset: "orders", Metrics: []Field{{Field: "business_revenue", Alias: "value"}}}},
		{ID: "all_by_segment", Request: Request{Dataset: "orders", Dimensions: []Field{{Field: "segment", Alias: "label"}}, Metrics: []Field{{Field: "revenue", Alias: "value"}}, Sort: []Sort{{Field: "label", Direction: "asc"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := queryBundlePlan(db, bundle)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := bundle.Decode(rows)
	if err != nil {
		t.Fatal(err)
	}
	filtered := decoded["filtered_total"]
	if len(filtered) != 1 || filtered[0]["value"] != float64(30) {
		t.Fatalf("filtered total = %#v, want 30", filtered)
	}
	all := map[string]float64{}
	for _, row := range decoded["all_by_segment"] {
		all[row["label"].(string)] = row["value"].(float64)
	}
	if fmt.Sprint(all) != "map[business:30 consumer:30]" {
		t.Fatalf("unfiltered segment revenue = %v", all)
	}
}

func TestBundleAppliesNamedMetricWhereFiltersMultiDataset(t *testing.T) {
	model := executableMultiDatasetModel()
	model.Filters = map[string]semanticmodel.SemanticFilterSpec{
		"vip_customer": {Field: "customers.state", Operator: "equals", Value: "vip"},
	}
	filteredTagCount := model.Metrics["tag_count"]
	filteredTagCount.Where = []string{"vip_customer"}
	model.Metrics["vip_tag_count"] = filteredTagCount

	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id VARCHAR, customer_id VARCHAR, segment VARCHAR, amount DOUBLE)",
		"INSERT INTO model.orders VALUES ('o1', 'a', 'consumer', 10), ('o2', 'b', 'business', 30)",
		"CREATE TABLE model.tags(tag_id VARCHAR, customer_id VARCHAR, segment VARCHAR, tag VARCHAR)",
		"INSERT INTO model.tags VALUES ('t1', 'a', 'consumer', 'new'), ('t2', 'c', 'consumer', 'vip'), ('t3', 'c', 'consumer', 'repeat')",
		"CREATE TABLE model.customers(customer_id VARCHAR, state VARCHAR)",
		"INSERT INTO model.customers VALUES ('a', 'consumer'), ('b', 'business'), ('c', 'vip')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := mustNewCompiledPlanner(t, model).PlanBundle([]BundleRequest{
		{ID: "filtered_tags", Request: Request{Metrics: []Field{{Field: "vip_tag_count", Alias: "value"}}}},
		{ID: "all_tags", Request: Request{Metrics: []Field{{Field: "tag_count", Alias: "value"}}}},
		{ID: "all_orders", Request: Request{Metrics: []Field{{Field: "order_count", Alias: "value"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Plan.Mode != "multi_dataset" {
		t.Fatalf("bundle mode = %q, want multi_dataset", bundle.Plan.Mode)
	}
	rows, err := queryBundlePlan(db, bundle)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := bundle.Decode(rows)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		id    string
		value int64
	}{
		{id: "filtered_tags", value: 2},
		{id: "all_tags", value: 3},
		{id: "all_orders", value: 2},
	}
	for _, check := range checks {
		got := decoded[check.id]
		if len(got) != 1 || got[0]["value"] != check.value {
			t.Fatalf("%s = %#v, want %d", check.id, got, check.value)
		}
	}
}

func TestPlanBundleRejectsDifferentGovernedScopes(t *testing.T) {
	_, err := mustNewCompiledPlanner(t, executableMultiDatasetModel()).PlanBundle([]BundleRequest{
		{ID: "a", Request: Request{Dataset: "orders", Metrics: []Field{{Field: "order_count"}}, Filters: bundleConsumerFilter()}},
		{ID: "b", Request: Request{Dataset: "orders", Metrics: []Field{{Field: "order_count"}}, Filters: []Filter{{Field: "orders.segment", Dataset: "orders", Operator: "equals", Values: []any{"business"}}}}},
	})
	if err == nil || !strings.Contains(err.Error(), "governed scope") {
		t.Fatalf("error = %v", err)
	}
}

func TestPlanBundleScansEachDatasetOnceForMultiDatasetBranches(t *testing.T) {
	bundle, err := mustNewCompiledPlanner(t, executableMultiDatasetModel()).PlanBundle([]BundleRequest{
		{ID: "by_customer", Request: Request{Dimensions: []Field{{Field: "customer", Alias: "label"}}, Metrics: []Field{{Field: "tags_per_order", Alias: "value"}}}},
		{ID: "by_segment", Request: Request{Dimensions: []Field{{Field: "segment", Alias: "label"}}, Metrics: []Field{{Field: "tags_per_order", Alias: "value"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(bundle.Plan.SQL, "FROM model.orders") != 1 || strings.Count(bundle.Plan.SQL, "FROM model.tags") != 1 {
		t.Fatalf("multi-dataset bundle does not scan each dataset once:\n%s", bundle.Plan.SQL)
	}
	for _, want := range []string{"AS MATERIALIZED", "FULL OUTER JOIN", "IS NOT DISTINCT FROM"} {
		if !strings.Contains(bundle.Plan.SQL, want) {
			t.Fatalf("multi-dataset bundle missing %q:\n%s", want, bundle.Plan.SQL)
		}
	}
	explain, err := bundle.Plan.Explain()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(explain, "[StitchAggregates]") != 2 || !strings.Contains(explain, "[BundleBranches]") {
		t.Fatalf("multi-root bundle PlanIR =\n%s", explain)
	}
}

func TestPlanBundleSharesDatasetScansAcrossSingleAndMultiDatasetBranches(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id VARCHAR, customer_id VARCHAR, segment VARCHAR, amount DOUBLE)",
		"INSERT INTO model.orders VALUES ('o1', 'a', 'consumer', 10), ('o2', 'a', 'consumer', 20), ('o3', 'b', 'business', 30)",
		"CREATE TABLE model.tags(tag_id VARCHAR, customer_id VARCHAR, segment VARCHAR, tag VARCHAR)",
		"INSERT INTO model.tags VALUES ('t1', 'a', 'consumer', 'new'), ('t2', 'c', 'consumer', 'vip'), ('t3', 'c', 'consumer', 'repeat')",
		"CREATE TABLE model.clicks(click_id VARCHAR, customer_id VARCHAR, segment VARCHAR)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := mustNewCompiledPlanner(t, executableMultiDatasetModel()).PlanBundle([]BundleRequest{
		{
			ID: "orders_by_local_segment",
			Request: Request{
				Dataset:    "orders",
				Dimensions: []Field{{Field: "orders.segment", Alias: "label"}},
				Metrics:    []Field{{Field: "revenue", Alias: "value"}},
				Sort:       []Sort{{Field: "label", Direction: "asc"}},
			},
		},
		{
			ID: "orders_by_customer",
			Request: Request{
				Dataset:    "orders",
				Dimensions: []Field{{Field: "customer", Alias: "label"}},
				Metrics:    []Field{{Field: "revenue", Alias: "value"}},
				Sort:       []Sort{{Field: "label", Direction: "asc"}},
			},
		},
		{
			ID: "ratio_by_customer",
			Request: Request{
				Dimensions: []Field{{Field: "customer", Alias: "label"}},
				Metrics:    []Field{{Field: "tags_per_order", Alias: "value"}},
				Sort:       []Sort{{Field: "label", Direction: "asc"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(bundle.Plan.SQL, "FROM model.orders"); got != 1 {
		t.Fatalf("orders scans = %d, want 1:\n%s", got, bundle.Plan.SQL)
	}
	if got := strings.Count(bundle.Plan.SQL, "FROM model.tags"); got != 1 {
		t.Fatalf("tags scans = %d, want 1:\n%s", got, bundle.Plan.SQL)
	}
	if !strings.Contains(bundle.Plan.SQL, "AS MATERIALIZED") || strings.Contains(bundle.Plan.SQL, "CROSS JOIN UNNEST") {
		t.Fatalf("heterogeneous bundle did not reuse a statement-local governed projection:\n%s", bundle.Plan.SQL)
	}
	rows, err := queryBundlePlan(db, bundle)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := bundle.Decode(rows)
	if err != nil {
		t.Fatal(err)
	}
	segments := decoded["orders_by_local_segment"]
	if len(segments) != 2 || segments[0]["label"] != "business" || segments[0]["value"] != float64(30) || segments[1]["label"] != "consumer" || segments[1]["value"] != float64(30) {
		t.Fatalf("single-dataset local branch = %#v", segments)
	}
	orderCustomers := decoded["orders_by_customer"]
	if len(orderCustomers) != 2 || orderCustomers[0]["label"] != "a" || orderCustomers[0]["value"] != float64(30) || orderCustomers[1]["label"] != "b" || orderCustomers[1]["value"] != float64(30) {
		t.Fatalf("single-dataset conformed branch leaked multi-dataset-only groups: %#v", orderCustomers)
	}
	customers := decoded["ratio_by_customer"]
	if len(customers) != 3 || customers[0]["label"] != "a" || !exactDecimalEqual(customers[0]["value"], "0.5") || customers[1]["label"] != "b" || !exactDecimalEqual(customers[1]["value"], "0") || customers[2]["label"] != "c" || customers[2]["value"] != nil {
		t.Fatalf("multi-dataset branch = %#v", customers)
	}
}

func TestMultiDatasetBundleScalarCountOnlyExecutesAcrossThreeDatasets(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id VARCHAR, customer_id VARCHAR, segment VARCHAR, amount DOUBLE)",
		"INSERT INTO model.orders VALUES ('o1', 'a', 'consumer', 10), ('o2', 'b', 'business', 30)",
		"CREATE TABLE model.tags(tag_id VARCHAR, customer_id VARCHAR, segment VARCHAR, tag VARCHAR)",
		"INSERT INTO model.tags VALUES ('t1', 'a', 'consumer', 'new'), ('t2', 'c', 'consumer', 'vip'), ('t3', 'c', 'consumer', 'repeat')",
		"CREATE TABLE model.clicks(click_id VARCHAR, customer_id VARCHAR, segment VARCHAR)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := mustNewCompiledPlanner(t, executableMultiDatasetModel()).PlanBundle([]BundleRequest{{
		ID: "totals",
		Request: Request{Metrics: []Field{
			{Field: "order_count", Alias: "orders"},
			{Field: "tag_count", Alias: "tags"},
			{Field: "click_count", Alias: "clicks"},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bundle.Plan.SQL, "SELECT \n") {
		t.Fatalf("count-only dataset emitted an empty SELECT list:\n%s", bundle.Plan.SQL)
	}
	rows, err := queryBundlePlan(db, bundle)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := bundle.Decode(rows)
	if err != nil {
		t.Fatal(err)
	}
	got := decoded["totals"]
	if len(got) != 1 || got[0]["orders"] != int64(2) || got[0]["tags"] != int64(3) || got[0]["clicks"] != int64(0) {
		t.Fatalf("three-dataset scalar = %#v", got)
	}
}

func TestMultiDatasetBundleExecutesExactOuterStitchAcrossGroupingSets(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id VARCHAR, customer_id VARCHAR, segment VARCHAR, amount DOUBLE)",
		"INSERT INTO model.orders VALUES ('o1', 'a', 'consumer', 10), ('o2', 'a', 'consumer', 20), ('o3', 'b', 'business', 30)",
		"CREATE TABLE model.tags(tag_id VARCHAR, customer_id VARCHAR, segment VARCHAR, tag VARCHAR)",
		"INSERT INTO model.tags VALUES ('t1', 'a', 'consumer', 'new'), ('t2', 'c', 'consumer', 'vip'), ('t3', 'c', 'consumer', 'repeat')",
		"CREATE TABLE model.clicks(click_id VARCHAR, customer_id VARCHAR, segment VARCHAR)",
		"INSERT INTO model.clicks VALUES ('c1', 'a', 'consumer'), ('c2', 'd', 'business'), ('c3', 'd', 'business')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := mustNewCompiledPlanner(t, executableMultiDatasetModel()).PlanBundle([]BundleRequest{
		{ID: "by_customer", Request: Request{Dimensions: []Field{{Field: "customer", Alias: "label"}}, Metrics: []Field{{Field: "tags_per_order", Alias: "value"}, {Field: "click_count", Alias: "clicks"}}, Sort: []Sort{{Field: "label", Direction: "asc"}}}},
		{ID: "by_segment", Request: Request{Dimensions: []Field{{Field: "segment", Alias: "label"}}, Metrics: []Field{{Field: "tags_per_order", Alias: "value"}, {Field: "click_count", Alias: "clicks"}}, Sort: []Sort{{Field: "label", Direction: "asc"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := queryBundlePlan(db, bundle)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := bundle.Decode(rows)
	if err != nil {
		t.Fatal(err)
	}
	customers := map[string]any{}
	customerClicks := map[string]any{}
	for _, row := range decoded["by_customer"] {
		customers[row["label"].(string)] = row["value"]
		customerClicks[row["label"].(string)] = row["clicks"]
	}
	if !exactDecimalEqual(customers["a"], "0.5") || !exactDecimalEqual(customers["b"], "0") || customers["c"] != nil || customers["d"] != nil {
		t.Fatalf("customers = %#v", customers)
	}
	if customerClicks["a"] != int64(1) || customerClicks["b"] != int64(0) || customerClicks["c"] != int64(0) || customerClicks["d"] != int64(2) {
		t.Fatalf("customer clicks = %#v", customerClicks)
	}
	segments := map[string]any{}
	segmentClicks := map[string]any{}
	for _, row := range decoded["by_segment"] {
		segments[row["label"].(string)] = row["value"]
		segmentClicks[row["label"].(string)] = row["clicks"]
	}
	if !exactDecimalEqual(segments["consumer"], "1.5") || !exactDecimalEqual(segments["business"], "0") {
		t.Fatalf("segments = %#v", segments)
	}
	if segmentClicks["consumer"] != int64(1) || segmentClicks["business"] != int64(2) {
		t.Fatalf("segment clicks = %#v", segmentClicks)
	}
}

func queryBundlePlan(db *sql.DB, bundle BundlePlan) (Rows, error) {
	result, err := db.Query(bundle.Plan.SQL, bundle.Plan.Args...)
	if err != nil {
		return nil, fmt.Errorf("execute bundle: %w\n%s", err, bundle.Plan.SQL)
	}
	defer result.Close()
	rows := Rows{}
	for result.Next() {
		values := make([]any, len(bundle.Plan.Columns))
		scans := make([]any, len(values))
		for i := range values {
			scans[i] = &values[i]
		}
		if err := result.Scan(scans...); err != nil {
			return nil, err
		}
		row := Row{}
		for i, column := range bundle.Plan.Columns {
			row[column] = values[i]
		}
		rows = append(rows, row)
	}
	return rows, result.Err()
}

func TestPlanBundleFailsClosedForColumnMasks(t *testing.T) {
	_, err := mustNewCompiledPlanner(t, executableMultiDatasetModel()).PlanBundle([]BundleRequest{{
		ID: "masked",
		Request: Request{
			Dataset:     "orders",
			Dimensions:  []Field{{Field: "customer"}},
			Metrics:     []Field{{Field: "order_count"}},
			ColumnMasks: []ColumnMask{{Field: "orders.customer_id", Mask: "redact"}},
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "not safely bundleable") {
		t.Fatalf("error = %v", err)
	}
}

func TestPlanBundleRejectsDuplicateBranchOutputAliases(t *testing.T) {
	_, err := mustNewCompiledPlanner(t, executableMultiDatasetModel()).PlanBundle([]BundleRequest{{
		ID: "duplicate",
		Request: Request{
			Dataset:    "orders",
			Dimensions: []Field{{Field: "customer", Alias: "value"}},
			Metrics:    []Field{{Field: "order_count", Alias: "value"}},
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("error = %v, want duplicate output alias", err)
	}
}

func TestBundleExecutesOneStatementAndDecodesExactTypedBranches(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id VARCHAR, customer_id VARCHAR, segment VARCHAR, amount DOUBLE)",
		"INSERT INTO model.orders VALUES ('o1', 'a', 'consumer', 10), ('o2', 'a', 'consumer', 20), ('o3', 'b', 'business', 30)",
		"CREATE TABLE model.tags(tag_id VARCHAR, customer_id VARCHAR, segment VARCHAR, tag VARCHAR)",
		"CREATE TABLE model.clicks(click_id VARCHAR, customer_id VARCHAR, segment VARCHAR)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := mustNewCompiledPlanner(t, executableMultiDatasetModel()).PlanBundle([]BundleRequest{
		{ID: "kpi", Request: Request{Dataset: "orders", Metrics: []Field{{Field: "order_count", Alias: "value"}}}},
		{ID: "customer", Request: Request{Dataset: "orders", Dimensions: []Field{{Field: "customer", Alias: "label"}}, Metrics: []Field{{Field: "revenue", Alias: "value"}}, Sort: []Sort{{Field: "label", Direction: "asc"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	explainRows, err := db.Query("EXPLAIN "+bundle.Plan.SQL, bundle.Plan.Args...)
	if err != nil {
		t.Fatal(err)
	}
	var explain strings.Builder
	for explainRows.Next() {
		var kind, text string
		if err := explainRows.Scan(&kind, &text); err != nil {
			t.Fatal(err)
		}
		explain.WriteString(text)
	}
	explainRows.Close()
	if scans := strings.Count(explain.String(), "memory.model.orders"); scans != 1 {
		t.Fatalf("physical plan reads orders %d times, want once:\n%s", scans, explain.String())
	}
	result, err := db.Query(bundle.Plan.SQL, bundle.Plan.Args...)
	if err != nil {
		t.Fatalf("execute bundle: %v\n%s", err, bundle.Plan.SQL)
	}
	defer result.Close()
	rows := Rows{}
	for result.Next() {
		values := make([]any, len(bundle.Plan.Columns))
		scans := make([]any, len(values))
		for i := range values {
			scans[i] = &values[i]
		}
		if err := result.Scan(scans...); err != nil {
			t.Fatal(err)
		}
		row := Row{}
		for i, column := range bundle.Plan.Columns {
			row[column] = values[i]
		}
		rows = append(rows, row)
	}
	decoded, err := bundle.Decode(rows)
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded["kpi"]; len(got) != 1 || got[0]["value"] != int64(3) {
		t.Fatalf("kpi = %#v", got)
	}
	got := map[string]float64{}
	for _, row := range decoded["customer"] {
		got[row["label"].(string)] = row["value"].(float64)
	}
	if fmt.Sprint(got) != "map[a:30 b:30]" {
		t.Fatalf("customer = %v", got)
	}
}

func TestBundleDecodePreservesDeterministicAuthoredBranchOrdering(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id VARCHAR, customer_id VARCHAR, segment VARCHAR, amount DOUBLE)",
		"INSERT INTO model.orders VALUES ('o1', 'b', 'consumer', 20), ('o2', 'a', 'consumer', 10), ('o3', 'c', 'consumer', 30)",
		"CREATE TABLE model.tags(tag_id VARCHAR, customer_id VARCHAR, segment VARCHAR, tag VARCHAR)",
		"CREATE TABLE model.clicks(click_id VARCHAR, customer_id VARCHAR, segment VARCHAR)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := mustNewCompiledPlanner(t, executableMultiDatasetModel()).PlanBundle([]BundleRequest{
		{ID: "descending", Request: Request{Dataset: "orders", Dimensions: []Field{{Field: "customer", Alias: "label"}}, Metrics: []Field{{Field: "revenue", Alias: "value"}}, Sort: []Sort{{Field: "label", Direction: "desc"}}, Limit: 2}},
		{ID: "ascending", Request: Request{Dataset: "orders", Dimensions: []Field{{Field: "customer", Alias: "label"}}, Metrics: []Field{{Field: "revenue", Alias: "value"}}, Sort: []Sort{{Field: "label", Direction: "asc"}}, Limit: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.Query(bundle.Plan.SQL, bundle.Plan.Args...)
	if err != nil {
		t.Fatalf("execute bundle: %v\n%s", err, bundle.Plan.SQL)
	}
	defer result.Close()
	rows := Rows{}
	for result.Next() {
		values := make([]any, len(bundle.Plan.Columns))
		scans := make([]any, len(values))
		for i := range values {
			scans[i] = &values[i]
		}
		if err := result.Scan(scans...); err != nil {
			t.Fatal(err)
		}
		row := Row{}
		for i, column := range bundle.Plan.Columns {
			row[column] = values[i]
		}
		rows = append(rows, row)
	}
	decoded, err := bundle.Decode(rows)
	if err != nil {
		t.Fatal(err)
	}
	labels := func(rows Rows) string {
		values := make([]string, len(rows))
		for i, row := range rows {
			values[i] = row["label"].(string)
		}
		return strings.Join(values, ",")
	}
	if got := labels(decoded["descending"]); got != "c,b" {
		t.Fatalf("descending labels = %q, want c,b", got)
	}
	if got := labels(decoded["ascending"]); got != "a,b" {
		t.Fatalf("ascending labels = %q, want a,b", got)
	}
	if !strings.Contains(bundle.Plan.SQL, "ORDER BY __bundle_branch ASC, __bundle_row ASC") {
		t.Fatalf("bundle has no deterministic final ordering:\n%s", bundle.Plan.SQL)
	}
}

func bundleConsumerFilter() []Filter {
	return []Filter{{Field: "orders.segment", Dataset: "orders", Operator: "equals", Values: []any{"consumer"}}}
}
