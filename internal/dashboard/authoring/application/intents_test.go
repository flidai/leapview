package application

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/compiler"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestResolveVisualTypeFieldBindingsMapsOnlyExactGovernedEquivalents(t *testing.T) {
	model := &semanticmodel.Model{
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"category": {Bindings: map[string]semanticmodel.DimensionBinding{"sales_orders": {Field: "sales_orders.category"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"revenue":     {Dataset: "sales_orders", Input: &semanticmodel.MetricInput{Field: "sales_orders.revenue"}},
			"order_count": {Dataset: "sales_orders", Input: &semanticmodel.MetricInput{Field: "sales_orders.order_id"}},
		},
	}
	revenue, orderID, customerID := "revenue", "order_id", "customer_id"
	visual := document.DashboardVisual{Query: document.DashboardQuery{Value: &document.RecordsDashboardQuery{
		Type: "records", Dataset: "sales_orders",
		Fields: []document.DashboardRecordFieldSelection{{String: &revenue}, {String: &orderID}, {String: &customerID}},
	}}}

	got := resolveVisualTypeFieldBindings(model, visual)
	want := authoring.VisualTypeFieldBindings{
		Metrics: []string{"revenue"}, Dataset: "sales_orders", Details: []string{"revenue", "order_id", "customer_id"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved bindings = %#v, want %#v", got, want)
	}
}

func TestResolveVisualTypeFieldBindingsMapsSemanticQueryBackToOneDataset(t *testing.T) {
	model := &semanticmodel.Model{
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"category": {Bindings: map[string]semanticmodel.DimensionBinding{"sales_orders": {Field: "sales_orders.category"}}},
			"state":    {Bindings: map[string]semanticmodel.DimensionBinding{"sales_orders": {Field: "sales_customers.state"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"revenue":     {Dataset: "sales_orders", Input: &semanticmodel.MetricInput{Field: "sales_orders.revenue"}},
			"order_count": {Dataset: "sales_orders", Input: &semanticmodel.MetricInput{Field: "sales_orders.order_id"}},
		},
	}
	category, state, revenue, orders := "category", "state", "revenue", "order_count"
	visual := document.DashboardVisual{Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
		Type:       "aggregate",
		Dimensions: []document.DashboardDimensionSelection{{String: &category}, {String: &state}},
		Metrics:    []document.DashboardMetricSelection{{String: &revenue}, {String: &orders}},
	}}}

	got := resolveVisualTypeFieldBindings(model, visual)
	want := authoring.VisualTypeFieldBindings{
		Dimensions: []string{"category", "state"}, Metrics: []string{"revenue", "order_count"},
		Dataset: "sales_orders", Details: []string{"category", "revenue"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved bindings = %#v, want %#v", got, want)
	}
}

func TestResolveVisualTypeFieldBindingsForTargetCompletesRequiredRolesFromSourceDataset(t *testing.T) {
	model := &semanticmodel.Model{
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"scenario":   {Bindings: map[string]semanticmodel.DimensionBinding{"cash_forecast": {Field: "cash_scenarios.scenario"}}},
			"week_start": {Bindings: map[string]semanticmodel.DimensionBinding{"cash_forecast": {Field: "cash_weeks.week_start"}}},
			"region":     {Bindings: map[string]semanticmodel.DimensionBinding{"sales": {Field: "sales.region"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"current_cash":      {Dataset: "cash_forecast", Input: &semanticmodel.MetricInput{Field: "cash_forecast.current_cash"}},
			"collections":       {Dataset: "cash_forecast", Input: &semanticmodel.MetricInput{Field: "cash_forecast.collections"}},
			"net_cash_flow":     {Dataset: "cash_forecast", Input: &semanticmodel.MetricInput{Field: "cash_forecast.net_cash_flow"}},
			"supplier_payments": {Dataset: "cash_forecast", Input: &semanticmodel.MetricInput{Field: "cash_forecast.supplier_payments"}},
			"sales_total":       {Dataset: "sales", Input: &semanticmodel.MetricInput{Field: "sales.total"}},
		},
		Tables: map[string]semanticmodel.Table{
			"cash_forecast": {Dimensions: map[string]semanticmodel.MetricDimension{
				"current_cash": {Datatype: semanticmodel.DataTypeDecimal},
				"scenario":     {Datatype: semanticmodel.DataTypeString},
				"week_start":   {Datatype: semanticmodel.DataTypeDate},
			}},
			"sales": {Dimensions: map[string]semanticmodel.MetricDimension{"region": {Datatype: semanticmodel.DataTypeString}}},
		},
	}
	currentCash := "current_cash"
	visual := document.DashboardVisual{Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
		Type: "aggregate", Metrics: []document.DashboardMetricSelection{{String: &currentCash}},
	}}}

	for _, entry := range authoring.CanonicalVisualCatalog() {
		t.Run(string(entry.Type), func(t *testing.T) {
			got := resolveVisualTypeFieldBindingsForTarget(model, visual, entry.Type)
			if got.Dataset != "cash_forecast" {
				t.Fatalf("dataset = %q, want cash_forecast", got.Dataset)
			}
			if len(got.Metrics) == 0 || got.Metrics[0] != "current_cash" {
				t.Fatalf("metrics = %#v, want current_cash retained first", got.Metrics)
			}
			if entry.Type == document.DashboardVisualTypeMap {
				if len(got.Dimensions) != 0 {
					t.Fatalf("map dimensions = %#v, want empty without numeric latitude/longitude", got.Dimensions)
				}
				return
			}
			for _, limit := range authoring.CanonicalVisualRoleLimits(entry.Type) {
				var count int
				switch limit.Role {
				case string(authoring.FieldRoleDimension):
					count = len(got.Dimensions)
				case string(authoring.FieldRoleMetric):
					count = len(got.Metrics)
				case string(authoring.FieldRoleDetail):
					count = len(got.Details)
				}
				if int32(count) < limit.Minimum {
					t.Fatalf("%s fields = %d, want at least %d: %#v", limit.Role, count, limit.Minimum, got)
				}
			}
			for _, dimension := range got.Dimensions {
				if dimension == "region" {
					t.Fatalf("cross-dataset dimension leaked into bindings: %#v", got)
				}
			}
			for _, metric := range got.Metrics {
				if metric == "sales_total" {
					t.Fatalf("cross-dataset metric leaked into bindings: %#v", got)
				}
			}
		})
	}
}

func TestResolveVisualTypeFieldBindingsForMapPrefersNumericLatitudeAndLongitude(t *testing.T) {
	model := &semanticmodel.Model{
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"country":   {Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"stores": {Field: "stores.country"}}},
			"latitude":  {Datatype: semanticmodel.DataTypeFloat, Bindings: map[string]semanticmodel.DimensionBinding{"stores": {Field: "stores.latitude"}}},
			"longitude": {Datatype: semanticmodel.DataTypeFloat, Bindings: map[string]semanticmodel.DimensionBinding{"stores": {Field: "stores.longitude"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"store_count": {Dataset: "stores"},
		},
	}
	storeCount := "store_count"
	want := []string{"latitude", "longitude"}
	for _, test := range []struct {
		name       string
		dimensions []string
	}{
		{name: "auto-selects coordinates before country"},
		{name: "removes other existing dimensions and orders coordinates", dimensions: []string{"country", "longitude", "latitude"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			queryDimensions := make([]document.DashboardDimensionSelection, 0, len(test.dimensions))
			for _, id := range test.dimensions {
				id := id
				queryDimensions = append(queryDimensions, document.DashboardDimensionSelection{String: &id})
			}
			visual := document.DashboardVisual{Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
				Type: "aggregate", Dimensions: queryDimensions, Metrics: []document.DashboardMetricSelection{{String: &storeCount}},
			}}}

			got := resolveVisualTypeFieldBindingsForTarget(model, visual, document.DashboardVisualTypeMap)
			if !reflect.DeepEqual(got.Dimensions, want) {
				t.Fatalf("map dimensions = %#v, want numeric coordinates in latitude/longitude order: %#v", got.Dimensions, want)
			}
		})
	}
}

func TestResolveVisualTypeFieldBindingsForMapStaysEmptyWithoutCoordinatePair(t *testing.T) {
	model := &semanticmodel.Model{
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"country":  {Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"stores": {Field: "stores.country"}}},
			"revenue":  {Datatype: semanticmodel.DataTypeDecimal, Bindings: map[string]semanticmodel.DimensionBinding{"stores": {Field: "stores.revenue"}}},
			"latitude": {Datatype: semanticmodel.DataTypeFloat, Bindings: map[string]semanticmodel.DimensionBinding{"other_dataset": {Field: "other_dataset.latitude"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"store_count": {Dataset: "stores"},
		},
	}
	country, revenue, latitude, storeCount := "country", "revenue", "latitude", "store_count"
	visual := document.DashboardVisual{Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
		Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: &country}, {String: &revenue}, {String: &latitude}}, Metrics: []document.DashboardMetricSelection{{String: &storeCount}},
	}}}

	got := resolveVisualTypeFieldBindingsForTarget(model, visual, document.DashboardVisualTypeMap)
	if len(got.Dimensions) != 0 {
		t.Fatalf("map dimensions = %#v, want empty without a latitude/longitude pair", got.Dimensions)
	}
}

func TestVisualTypeSwitchDoesNotReplaceDerivedMetricsWithUnrelatedRecordColumns(t *testing.T) {
	model := &semanticmodel.Model{
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"segment": {Bindings: map[string]semanticmodel.DimensionBinding{"financial_performance": {Field: "segments.segment"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"revenue_variance": {Dataset: "financial_performance"},
		},
		Tables: map[string]semanticmodel.Table{
			"financial_performance": {Dimensions: map[string]semanticmodel.MetricDimension{
				"budget_cogs": {Datatype: semanticmodel.DataTypeDecimal},
			}},
		},
	}
	segment, variance := "segment", "revenue_variance"
	visual := document.DashboardVisual{Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
		Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: &segment}},
		Metrics: []document.DashboardMetricSelection{{String: &variance}},
	}}}
	got := resolveVisualTypeFieldBindingsForTarget(model, visual, document.DashboardVisualTypeTable)
	if got.Dataset != "financial_performance" || len(got.Details) != 0 {
		t.Fatalf("table must ask for columns when the source has no exact record equivalent, got %#v", got)
	}
}

func TestRecordDetailFieldIDForValidationQualifiesAgainstRecordsDataset(t *testing.T) {
	field := "customer_id"
	doc := document.DashboardDocument{Spec: document.DashboardSpec{Visuals: map[string]document.DashboardVisual{
		"orders": {Query: document.DashboardQuery{Value: &document.RecordsDashboardQuery{
			Type: "records", Dataset: "sales_orders",
			Fields: []document.DashboardRecordFieldSelection{{String: &field}},
		}}},
	}}}

	model := &semanticmodel.Model{Tables: map[string]semanticmodel.Table{
		"sales_orders":    {Dimensions: map[string]semanticmodel.MetricDimension{"customer_id": {}}},
		"sales_customers": {Dimensions: map[string]semanticmodel.MetricDimension{"customer_id": {}}},
	}}

	qualified := recordDetailFieldIDForValidation(doc, "orders", " customer_id ", authoring.FieldRoleDetail)
	if qualified != "sales_orders.customer_id" {
		t.Fatalf("validation field ID = %q, want sales_orders.customer_id", qualified)
	}
	if err := validateGovernedField(model, qualified, authoring.FieldRoleDetail); err != nil {
		t.Fatalf("qualified records detail rejected: %v", err)
	}

	for _, test := range []struct {
		name    string
		fieldID string
		role    authoring.FieldRole
		want    string
	}{
		{name: "already qualified", fieldID: "sales_orders.customer_id", role: authoring.FieldRoleDetail, want: "sales_orders.customer_id"},
		{name: "pending dataset", fieldID: "customer_id", role: authoring.FieldRoleDetail, want: "customer_id"},
		{name: "aggregate role", fieldID: "customer_id", role: authoring.FieldRoleDimension, want: "customer_id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			testDoc := doc
			if test.name == "pending dataset" {
				testDoc.Spec.Visuals = map[string]document.DashboardVisual{
					"orders": {Query: document.DashboardQuery{Value: &document.RecordsDashboardQuery{Type: "records", Dataset: "pending_dataset"}}},
				}
			}
			if got := recordDetailFieldIDForValidation(testDoc, "orders", test.fieldID, test.role); got != test.want {
				t.Fatalf("validation field ID = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAssignedFieldFilterCompatibilityRejectsOnlyNewlyIncompatibleAssignments(t *testing.T) {
	t.Run("rejects a candidate that makes a targeted report filter incompatible", func(t *testing.T) {
		lifecycle, revision, command, model := assignmentFilterCompatibilityFixture(t, false, false)
		if _, _, err := authoring.ApplyEdit(lifecycle, revision, command, "candidate-revision", revision.Number+1, time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)); err != nil {
			t.Fatalf("build assignment candidate: %v", err)
		}
		_, err := compiler.CompileCanonicalDashboardBuilderFilters(revision.Document, model)
		if err != nil {
			t.Fatalf("baseline filter contract should be valid: %v", err)
		}
		if err := validateAssignedFieldFilterCompatibility(lifecycle, revision, command, model); err == nil {
			t.Fatal("assignment that makes finance_date incompatible was accepted")
		} else if !errors.Is(err, authoring.ErrInvalidPayload) {
			t.Fatalf("assignment error = %v, want ErrInvalidPayload", err)
		} else if !strings.Contains(err.Error(), "finance_date") {
			t.Fatalf("assignment error = %v, want actionable finance_date incompatibility", err)
		}
	})

	t.Run("allows legitimate multi-dataset assignment when the filter dimension binds to both", func(t *testing.T) {
		lifecycle, revision, command, model := assignmentFilterCompatibilityFixture(t, true, false)
		if err := validateAssignedFieldFilterCompatibility(lifecycle, revision, command, model); err != nil {
			t.Fatalf("multi-dataset assignment with a shared filter dimension was rejected: %v", err)
		}
	})

	t.Run("does not lock an already-invalid draft from further edits", func(t *testing.T) {
		lifecycle, revision, command, model := assignmentFilterCompatibilityFixture(t, false, true)
		if _, err := compiler.CompileCanonicalDashboardBuilderFilters(revision.Document, model); err == nil {
			t.Fatal("fixture should start with an incompatible filter target")
		}
		if err := validateAssignedFieldFilterCompatibility(lifecycle, revision, command, model); err != nil {
			t.Fatalf("assignment was blocked by pre-existing incompatibility: %v", err)
		}
	})
}

func assignmentFilterCompatibilityFixture(t *testing.T, dateDimensionBindsToForecast, alreadyInvalid bool) (authoring.DashboardLifecycle, authoring.Revision, authoring.Command, *semanticmodel.Model) {
	t.Helper()
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "audit-actor"}
	model := &semanticmodel.Model{
		Name: "finance",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"financial_performance": {Model: "financial_performance"},
			"cash_forecast":         {Model: "cash_forecast"},
		},
		Tables: map[string]semanticmodel.Table{
			"financial_performance": {
				ModelName: "financial_performance", GrainEntity: "performance_id",
				Entities: map[string]semanticmodel.EntityDefinition{"performance_id": {Type: "primary", Fields: []string{"performance_id"}}},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"performance_id": {Type: "string", Datatype: semanticmodel.DataTypeString},
					"finance_date":   {Type: "date", Datatype: semanticmodel.DataTypeDate},
					"finance_month":  {Type: "string", Datatype: semanticmodel.DataTypeString},
					"net_sales":      {Type: "number", Datatype: semanticmodel.DataTypeDecimal},
				},
			},
			"cash_forecast": {
				ModelName: "cash_forecast", GrainEntity: "forecast_id",
				Entities: map[string]semanticmodel.EntityDefinition{"forecast_id": {Type: "primary", Fields: []string{"forecast_id"}}},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"forecast_id":   {Type: "string", Datatype: semanticmodel.DataTypeString},
					"finance_date":  {Type: "date", Datatype: semanticmodel.DataTypeDate},
					"finance_month": {Type: "string", Datatype: semanticmodel.DataTypeString},
					"base_headroom": {Type: "number", Datatype: semanticmodel.DataTypeDecimal},
				},
			},
		},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"finance_date": {
				Type: "date", Datatype: semanticmodel.DataTypeDate,
				Bindings: map[string]semanticmodel.DimensionBinding{
					"financial_performance": {Field: "financial_performance.finance_date"},
				},
			},
			"finance_month": {
				Type: "string", Datatype: semanticmodel.DataTypeString,
				Bindings: map[string]semanticmodel.DimensionBinding{
					"financial_performance": {Field: "financial_performance.finance_month"},
					"cash_forecast":         {Field: "cash_forecast.finance_month"},
				},
			},
		},
		Metrics: map[string]semanticmodel.Metric{
			"net_sales":     {Type: "aggregate", Dataset: "financial_performance", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "financial_performance.net_sales"}},
			"base_headroom": {Type: "aggregate", Dataset: "cash_forecast", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "cash_forecast.base_headroom"}},
		},
	}
	if dateDimensionBindsToForecast {
		date := model.Dimensions["finance_date"]
		date.Bindings["cash_forecast"] = semanticmodel.DimensionBinding{Field: "cash_forecast.finance_date"}
		model.Dimensions["finance_date"] = date
	}
	month, sales, headroom := "finance_month", "net_sales", "base_headroom"
	metrics := []document.DashboardMetricSelection{{String: &sales}}
	if alreadyInvalid {
		metrics = append(metrics, document.DashboardMetricSelection{String: &headroom})
	}
	visual := document.DashboardVisual{
		Type: document.DashboardVisualTypeBar,
		Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
			DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate",
			Dimensions: []document.DashboardDimensionSelection{{String: &month}}, Metrics: metrics,
		}},
		Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{
			DashboardPresentationBase: document.DashboardPresentationBase{Type: "cartesian"}, Type: "cartesian",
		}},
	}
	targets := []string{"overview/monthly-component"}
	doc := document.DashboardDocument{
		APIVersion: document.DashboardApiVersionLeapviewDevV1,
		Kind:       document.DashboardResourceKindDashboard,
		Metadata:   document.DashboardMetadata{ID: "dashboard:finance", Name: "finance"},
		Spec: document.DashboardSpec{
			SemanticModel: "finance",
			Filters:       []document.DashboardFilter{{ID: "finance_date", Label: "Reporting period", Dimension: "finance_date", Targets: &targets, Control: document.DashboardFilterControl{Value: &document.DateRangeDashboardFilterControl{Type: "dateRange"}}}},
			Visuals:       map[string]document.DashboardVisual{"monthly-performance": visual},
			Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{{Value: &document.VisualDashboardPageComponent{
				DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "monthly-component", Type: "visual", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 12, RowSpan: 6}},
				Type:                       "visual", Visual: "monthly-performance",
			}}}}},
		},
	}
	revision, err := authoring.NewRevision("finance-revision", "dashboard:finance", 1, time.Date(2026, 9, 25, 11, 0, 0, 0, time.UTC), doc, provenance)
	if err != nil {
		t.Fatalf("create fixture revision: %v", err)
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		ProjectID: "project:finance", ID: "dashboard:finance", OwnerPrincipalID: "audit-actor", Slug: "finance-audit", Title: "Finance audit", SemanticModel: "finance",
		Visibility: authoring.VisibilityPrivate, Draft: &authoring.Draft{ID: "finance-draft", DashboardID: "dashboard:finance", Revision: revision.Token(), Provenance: provenance},
	})
	if err != nil {
		t.Fatalf("create fixture lifecycle: %v", err)
	}
	command := authoring.Command{
		ID: "assign-headroom", DashboardID: lifecycle.ID, DraftID: lifecycle.Draft.ID, ExpectedRevision: revision.Token(), Provenance: provenance,
		AssignField: &authoring.AssignFieldPayload{PageID: "overview", VisualID: "monthly-component", FieldID: "base_headroom", Role: authoring.FieldRoleMetric, ResolvedTable: "cash_forecast"},
	}
	return lifecycle, revision, command, model
}
