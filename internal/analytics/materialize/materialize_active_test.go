package materialize_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestActiveRefreshExecutesPlannedTablesInDependencyOrder(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				PrimaryKey: "order_id",
			},
			"order_summary": {
				PrimaryKey:        "status",
				ModelDependencies: []string{"orders"},
			},
		},
	}
	planner := &activeMaterializePlanner{plans: map[string]analyticsmaterialize.ModelTablePlan{
		"orders":        {Mode: analyticsmaterialize.PlanModeDirectSourceRead, SQL: "CREATE TABLE model.orders AS SELECT 1"},
		"order_summary": {Mode: analyticsmaterialize.PlanModeModelSQL, SQL: "CREATE TABLE model.order_summary AS SELECT * FROM model.orders"},
	}}
	executor := &activeMaterializeExecutor{}

	refreshed, err := analyticsmaterialize.Refresh(context.Background(), executor, planner, model)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if refreshed.IsZero() {
		t.Fatal("Refresh() returned zero timestamp")
	}
	if got, want := planner.calls, []string{"orders", "order_summary"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("planned tables = %#v, want %#v", got, want)
	}
	if got, want := executor.statements, []string{
		"CREATE SCHEMA IF NOT EXISTS model",
		"CREATE TABLE model.orders AS SELECT 1",
		"CREATE TABLE model.order_summary AS SELECT * FROM model.orders",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("executed statements = %#v, want %#v", got, want)
	}
}

func TestActiveRefreshPropagatesPlannerFailureAndStopsLaterTables(t *testing.T) {
	plannerErr := errors.New("planner unavailable")
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders":        {PrimaryKey: "order_id"},
			"order_summary": {PrimaryKey: "status", ModelDependencies: []string{"orders"}},
		},
	}
	planner := &activeMaterializePlanner{
		plans: map[string]analyticsmaterialize.ModelTablePlan{
			"orders": {SQL: "CREATE TABLE model.orders AS SELECT 1"},
		},
		errors: map[string]error{"order_summary": plannerErr},
	}
	executor := &activeMaterializeExecutor{}

	_, err := analyticsmaterialize.Refresh(context.Background(), executor, planner, model)
	if !errors.Is(err, plannerErr) {
		t.Fatalf("Refresh() error = %v, want planner error", err)
	}
	if got, want := planner.calls, []string{"orders", "order_summary"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("planned tables = %#v, want %#v", got, want)
	}
	if got, want := executor.statements, []string{
		"CREATE SCHEMA IF NOT EXISTS model",
		"CREATE TABLE model.orders AS SELECT 1",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("executed statements = %#v, want %#v", got, want)
	}
}

func TestActiveRefreshWrapsExecutionFailureWithTableIdentity(t *testing.T) {
	executionErr := errors.New("database write failed")
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {PrimaryKey: "order_id"},
		},
	}
	planner := &activeMaterializePlanner{plans: map[string]analyticsmaterialize.ModelTablePlan{
		"orders": {SQL: "CREATE TABLE model.orders AS SELECT 1"},
	}}
	executor := &activeMaterializeExecutor{failAt: 1, err: executionErr}

	_, err := analyticsmaterialize.Refresh(context.Background(), executor, planner, model)
	if !errors.Is(err, executionErr) {
		t.Fatalf("Refresh() error = %v, want execution error", err)
	}
	if !strings.Contains(err.Error(), "materializing model.orders") {
		t.Fatalf("Refresh() error = %v, want table context", err)
	}
	if got, want := executor.statements, []string{"CREATE SCHEMA IF NOT EXISTS model", "CREATE TABLE model.orders AS SELECT 1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("executed statements = %#v, want %#v", got, want)
	}
}

func TestActiveModelTableDependencyOrderIncludesTransitiveDependencies(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders":        {PrimaryKey: "order_id"},
			"order_summary": {PrimaryKey: "status", ModelDependencies: []string{"orders"}},
			"daily_summary": {PrimaryKey: "day", ModelDependencies: []string{"order_summary"}},
		},
	}

	order, err := analyticsmaterialize.ModelTableDependencyOrder(model, "daily_summary")
	if err != nil {
		t.Fatalf("ModelTableDependencyOrder() error = %v", err)
	}
	if want := []string{"orders", "order_summary", "daily_summary"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("dependency order = %#v, want %#v", order, want)
	}
}

func TestActiveModelTableDependencyOrderRejectsCyclesAndUnknownDependencies(t *testing.T) {
	cycle := &semanticmodel.Model{Tables: map[string]semanticmodel.Table{
		"orders":  {ModelDependencies: []string{"summary"}},
		"summary": {ModelDependencies: []string{"orders"}},
	}}
	if _, err := analyticsmaterialize.ModelTableDependencyOrder(cycle, "orders"); err == nil || !strings.Contains(err.Error(), "dependency cycle") {
		t.Fatalf("cycle error = %v, want dependency cycle", err)
	}

	unknown := &semanticmodel.Model{Tables: map[string]semanticmodel.Table{
		"summary": {ModelDependencies: []string{"missing"}},
	}}
	if _, err := analyticsmaterialize.ModelTableDependencyOrder(unknown, "summary"); err == nil || !strings.Contains(err.Error(), `unknown model table "missing"`) {
		t.Fatalf("unknown dependency error = %v, want missing table", err)
	}
}

func TestActiveModelTablesNamedValidatesInputsBeforePlanning(t *testing.T) {
	model := &semanticmodel.Model{
		Name:   "sales",
		Tables: map[string]semanticmodel.Table{"orders": {PrimaryKey: "order_id"}},
	}
	planner := &activeMaterializePlanner{plans: map[string]analyticsmaterialize.ModelTablePlan{
		"orders": {SQL: "CREATE TABLE model.orders AS SELECT 1"},
	}}
	executor := &activeMaterializeExecutor{}

	if err := analyticsmaterialize.ModelTablesNamed(context.Background(), executor, planner, model, []string{"missing"}); err == nil || !strings.Contains(err.Error(), `unknown model table "missing"`) {
		t.Fatalf("unknown table error = %v, want unknown model table", err)
	}
	if planner.calls != nil {
		t.Fatalf("planner calls = %#v, want no planning for unknown table", planner.calls)
	}
	if err := analyticsmaterialize.ModelTablesNamed(context.Background(), executor, planner, model, []string{"orders;DROP"}); err == nil || !strings.Contains(err.Error(), "invalid identifier") {
		t.Fatalf("invalid identifier error = %v, want invalid identifier", err)
	}
}

func TestActiveValidateFilesSkipsRemoteSources(t *testing.T) {
	model := &semanticmodel.Model{
		Connections: map[string]semanticmodel.Connection{
			"warehouse": {Kind: "s3"},
		},
		Sources: map[string]semanticmodel.Source{
			"events": {Path: "s3://bucket/events/*.parquet", Connection: "warehouse"},
		},
	}
	if err := analyticsmaterialize.ValidateFiles(model); err != nil {
		t.Fatalf("ValidateFiles() error = %v, want nil for remote source", err)
	}
}

func TestActiveValidateFilesReportsManagedRevisionPathsSorted(t *testing.T) {
	root := filepath.Join(t.TempDir(), "revision")
	model := &semanticmodel.Model{
		Connections: map[string]semanticmodel.Connection{
			"managed": {Kind: "managed", Root: root},
		},
		Sources: map[string]semanticmodel.Source{
			"z_orders": {Path: "z/orders.csv", Connection: "managed"},
			"a_events": {Path: "a/events.csv", Connection: "managed"},
		},
	}
	err := analyticsmaterialize.ValidateFiles(model)
	var missing *analyticsmaterialize.MissingDataError
	if !errors.As(err, &missing) {
		t.Fatalf("ValidateFiles() error = %v, want MissingDataError", err)
	}
	want := []string{filepath.Join(root, "a/events.csv"), filepath.Join(root, "z/orders.csv")}
	if !reflect.DeepEqual(missing.Missing, want) {
		t.Fatalf("missing files = %#v, want %#v", missing.Missing, want)
	}
}

func TestActiveValidateFilesRejectsManagedPathEscape(t *testing.T) {
	model := &semanticmodel.Model{
		Connections: map[string]semanticmodel.Connection{
			"managed": {Kind: "managed", Root: t.TempDir()},
		},
		Sources: map[string]semanticmodel.Source{
			"orders": {Path: "../orders.csv", Connection: "managed"},
		},
	}
	err := analyticsmaterialize.ValidateFiles(model)
	if err == nil || !strings.Contains(err.Error(), "escapes its active revision") {
		t.Fatalf("ValidateFiles() error = %v, want managed path escape error", err)
	}
}

func TestActiveValidateFilesUsesResolverErrors(t *testing.T) {
	resolverErr := errors.New("resolver failed")
	model := &semanticmodel.Model{
		Connections: map[string]semanticmodel.Connection{
			"managed": {Kind: "managed", Root: t.TempDir()},
		},
		Sources: map[string]semanticmodel.Source{
			"orders": {Path: "orders.csv", Connection: "managed"},
		},
	}
	resolver := activeSourcePathResolver{err: resolverErr}
	if err := analyticsmaterialize.ValidateFilesWithResolver(model, resolver); !errors.Is(err, resolverErr) {
		t.Fatalf("ValidateFilesWithResolver() error = %v, want resolver error", err)
	}
}

type activeMaterializePlanner struct {
	plans  map[string]analyticsmaterialize.ModelTablePlan
	errors map[string]error
	calls  []string
}

func (p *activeMaterializePlanner) PlanModelTable(_ context.Context, _ *semanticmodel.Model, tableName string, _ semanticmodel.Table) (analyticsmaterialize.ModelTablePlan, error) {
	p.calls = append(p.calls, tableName)
	if err := p.errors[tableName]; err != nil {
		return analyticsmaterialize.ModelTablePlan{}, err
	}
	plan, ok := p.plans[tableName]
	if !ok {
		return analyticsmaterialize.ModelTablePlan{}, fmt.Errorf("no plan for %s", tableName)
	}
	return plan, nil
}

type activeMaterializeExecutor struct {
	statements []string
	failAt     int
	err        error
}

func (e *activeMaterializeExecutor) Exec(_ context.Context, statement string) error {
	e.statements = append(e.statements, statement)
	if e.failAt > 0 && len(e.statements)-1 == e.failAt {
		return e.err
	}
	return nil
}

type activeSourcePathResolver struct{ err error }

func (r activeSourcePathResolver) ResolveSourcePath(*semanticmodel.Model, semanticmodel.Source) (string, error) {
	return "", r.err
}
