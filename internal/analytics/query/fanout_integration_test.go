package query

import (
	"database/sql"
	"reflect"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestFanoutMatrixSafeRelationshipPathsPreserveDatasetGrain(t *testing.T) {
	db := openFanoutDatabase(t, []string{
		"CREATE TABLE model.orders(order_id VARCHAR, customer_id VARCHAR, revenue DOUBLE)",
		"INSERT INTO model.orders VALUES ('o1', 'a', 10), ('o2', 'a', 20), ('o3', 'b', 30)",
		"CREATE TABLE model.customers(customer_id VARCHAR, region VARCHAR)",
		"INSERT INTO model.customers VALUES ('a', 'north'), ('b', 'south')",
		"CREATE TABLE model.profiles(customer_id VARCHAR, tier VARCHAR)",
		"INSERT INTO model.profiles VALUES ('a', 'gold'), ('b', 'silver')",
	})
	defer db.Close()

	planner := mustNewCompiledPlanner(t, singleDatasetFanoutModel())
	plan, err := planner.Plan(Request{
		Dimensions: []Field{{Field: "region"}, {Field: "tier"}},
		Metrics:    []Field{{Field: "order_count"}, {Field: "revenue"}},
		Sort:       []Sort{{Field: "region", Direction: "asc"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(plan.SQL, plan.Args...)
	if err != nil {
		t.Fatalf("execute many-to-one plus one-to-one plan:\n%s\n%v", plan.SQL, err)
	}
	got := map[string]struct {
		count   int
		revenue float64
	}{}
	for rows.Next() {
		var region, tier string
		var count int
		var revenue float64
		if err := rows.Scan(&region, &tier, &count, &revenue); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		got[region+"/"+tier] = struct {
			count   int
			revenue float64
		}{count: count, revenue: revenue}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		count   int
		revenue float64
	}{
		"north/gold":   {count: 2, revenue: 30},
		"south/silver": {count: 1, revenue: 30},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("joined metrics = %#v, want %#v", got, want)
	}

	reverse, err := planner.Plan(Request{
		Dimensions: []Field{{Field: "profile_region"}},
		Metrics:    []Field{{Field: "profile_count"}},
		Sort:       []Sort{{Field: "profile_region", Direction: "asc"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reverseRows, err := db.Query(reverse.SQL, reverse.Args...)
	if err != nil {
		t.Fatalf("execute reverse one-to-one plan:\n%s\n%v", reverse.SQL, err)
	}
	reverseGot := map[string]int{}
	for reverseRows.Next() {
		var region string
		var count int
		if err := reverseRows.Scan(&region, &count); err != nil {
			reverseRows.Close()
			t.Fatal(err)
		}
		reverseGot[region] = count
	}
	if err := reverseRows.Close(); err != nil {
		t.Fatal(err)
	}
	if reverseGot["north"] != 1 || reverseGot["south"] != 1 || len(reverseGot) != 2 {
		t.Fatalf("reverse one-to-one counts = %#v", reverseGot)
	}
}

func TestFanoutMatrixMultiDatasetPlansAggregateBeforeStitching(t *testing.T) {
	db := openFanoutDatabase(t, []string{
		"CREATE TABLE model.orders(order_id VARCHAR, customer_id VARCHAR)",
		"INSERT INTO model.orders VALUES ('o1', 'a'), ('o2', 'a'), ('o3', 'b')",
		"CREATE TABLE model.returns(return_id VARCHAR, customer_id VARCHAR)",
		"INSERT INTO model.returns VALUES ('r1', 'a'), ('r2', 'c')",
		"CREATE TABLE model.clicks(click_id VARCHAR, customer_id VARCHAR)",
		"INSERT INTO model.clicks VALUES ('c1', 'a'), ('c2', 'c'), ('c3', 'c'), ('c4', 'c')",
		"CREATE TABLE model.customers(customer_id VARCHAR, region VARCHAR)",
		"INSERT INTO model.customers VALUES ('a', 'north'), ('b', 'south'), ('c', 'north')",
	})
	defer db.Close()

	planner := mustNewCompiledPlanner(t, multiDatasetFanoutModel())
	tests := []struct {
		name    string
		metrics []Field
		want    map[string][]int
	}{
		{
			name:    "two datasets",
			metrics: []Field{{Field: "order_count"}, {Field: "return_count"}},
			want: map[string][]int{
				"north": {2, 2},
				"south": {1, 0},
			},
		},
		{
			name:    "three datasets",
			metrics: []Field{{Field: "order_count"}, {Field: "return_count"}, {Field: "click_count"}},
			want: map[string][]int{
				"north": {2, 2, 4},
				"south": {1, 0, 0},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := planner.Plan(Request{
				Dimensions: []Field{{Field: "region"}},
				Metrics:    test.metrics,
				Sort:       []Sort{{Field: "region", Direction: "asc"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Mode != "multi_dataset" {
				t.Fatalf("plan mode = %q, want multi_dataset", plan.Mode)
			}
			rows, err := db.Query(plan.SQL, plan.Args...)
			if err != nil {
				t.Fatalf("execute %s plan:\n%s\n%v", test.name, plan.SQL, err)
			}
			got := map[string][]int{}
			for rows.Next() {
				var region string
				values := make([]int, len(test.metrics))
				destinations := make([]any, 0, len(values)+1)
				destinations = append(destinations, &region)
				for index := range values {
					destinations = append(destinations, &values[index])
				}
				if err := rows.Scan(destinations...); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				got[region] = values
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("%s results = %#v, want %#v", test.name, got, test.want)
			}
		})
	}
}

func openFanoutDatabase(t *testing.T, statements []string) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("CREATE SCHEMA model"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatalf("execute %q: %v", statement, err)
		}
	}
	return db
}

func singleDatasetFanoutModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "single_dataset_fanout",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Entities: map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "revenue": {Type: "number", Datatype: semanticmodel.DataTypeDecimal},
				},
			},
			"customers": {
				Entities: map[string]semanticmodel.ModelEntitySpec{"customer_id": {Type: "primary", Fields: []string{"customer_id"}}}, GrainEntity: "customer_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "region": {Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
			"profiles": {
				Entities: map[string]semanticmodel.ModelEntitySpec{"customer_id": {Type: "primary", Fields: []string{"customer_id"}}}, GrainEntity: "customer_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "tier": {Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
		},
		Relationships: []semanticmodel.Relationship{
			{ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"},
			{ID: "customers_profiles", FromDataset: "customers", FromFields: []string{"customer_id"}, ToDataset: "profiles", ToFields: []string{"customer_id"}, Cardinality: "one_to_one"},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}, "customers": {Model: "customers"}, "profiles": {Model: "profiles"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"region": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{
				"orders": {Field: "customers.region", Path: []string{"orders_customers"}},
			}},
			"tier": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{
				"orders": {Field: "profiles.tier", Path: []string{"orders_customers", "customers_profiles"}},
			}},
			"profile_region": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{
				"profiles": {Field: "customers.region", Path: []string{"customers_profiles"}},
			}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count":   {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Empty: "zero"},
			"revenue":       {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}, Empty: "zero"},
			"profile_count": {Type: "aggregate", Dataset: "profiles", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "profiles.customer_id"}, Empty: "zero"},
		},
	}
}

func multiDatasetFanoutModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "multi_dataset_fanout",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Entities: map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
			"returns": {
				Entities: map[string]semanticmodel.ModelEntitySpec{"return_id": {Type: "primary", Fields: []string{"return_id"}}}, GrainEntity: "return_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"return_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
			"clicks": {
				Entities: map[string]semanticmodel.ModelEntitySpec{"click_id": {Type: "primary", Fields: []string{"click_id"}}}, GrainEntity: "click_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"click_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
			"customers": {
				Entities: map[string]semanticmodel.ModelEntitySpec{"customer_id": {Type: "primary", Fields: []string{"customer_id"}}}, GrainEntity: "customer_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "region": {Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
		},
		Relationships: []semanticmodel.Relationship{
			{ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"},
			{ID: "returns_customers", FromDataset: "returns", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"},
			{ID: "clicks_customers", FromDataset: "clicks", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}, "returns": {Model: "returns"}, "clicks": {Model: "clicks"}, "customers": {Model: "customers"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"region": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{
				"orders":  {Field: "customers.region", Path: []string{"orders_customers"}},
				"returns": {Field: "customers.region", Path: []string{"returns_customers"}},
				"clicks":  {Field: "customers.region", Path: []string{"clicks_customers"}},
			}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count":  {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Empty: "zero"},
			"return_count": {Type: "aggregate", Dataset: "returns", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "returns.return_id"}, Empty: "zero"},
			"click_count":  {Type: "aggregate", Dataset: "clicks", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "clicks.click_id"}, Empty: "zero"},
		},
	}
}
