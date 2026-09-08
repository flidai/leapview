package module

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"testing/synctest"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/command"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	"github.com/flidai/leapview/internal/dashboard/publication"
	queryauthz "github.com/flidai/leapview/internal/dashboard/queryauthz"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/workload"
)

type preparationAdmitter struct{}

func (*preparationAdmitter) Acquire(context.Context, workload.Request) (workload.Lease, error) {
	return nil, fmt.Errorf("preparation must not acquire physical execution")
}

type preparationAudit struct{}

func (*preparationAudit) RecordQueryEvent(context.Context, queryaudit.EventInput) error { return nil }

type preparationExecutor struct {
	runtimeMetrics
	check func(context.Context, consumer.Request) error
}

func (m preparationExecutor) ExecuteConsumersPage(ctx context.Context, request consumer.Request, _ consumer.Publisher) error {
	return m.check(ctx, request)
}

func TestPublicDefaultPreparationPreservesComposedDecorators(t *testing.T) {
	for _, order := range []string{"production", "authorization-admission-audit"} {
		t.Run(order, func(t *testing.T) {
			compiled := moduleCompiledRevision(t, "project_1", "published", "state-1")
			compiled.Definition.FilterDefinitions = map[string]dashboardfilter.Definition{"state": {ValueKind: dashboardfilter.ValueString, Predicates: []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionSet, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorIn}}}}}
			compiled.Definition.FilterBindings = map[string]dashboardfilter.Binding{
				"state": {Key: "state", ID: "state", Filter: "state", Scope: dashboardfilter.ScopeReport,
					Default: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionSet, Operator: dashboardfilter.OperatorIn, Values: []dashboardfilter.Value{{Kind: dashboardfilter.ValueString, Value: "WA"}}}},
			}
			compiled.Definition.Pages[0].Visuals = []dashboard.PageVisual{{ID: "table", Visual: "table"}}
			spec := visualizationir.VisualizationSpec{Value: &visualizationir.TableVisualizationSpec{
				VisualizationSpecBase: visualizationir.VisualizationSpecBase{Kind: "table", Title: "State", Accessibility: visualizationir.VisualizationAccessibility{Title: "State", Description: "State"}, Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: []visualizationir.VisualizationField{{ID: "state", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Label: "State"}}}}, DataBudget: visualizationir.VisualizationDataBudget{MaxRows: 100}},
				Kind:                  "table", Columns: []visualizationir.TableVisualizationColumn{{Field: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "state"}, Label: "State", Formatting: []visualizationir.TableVisualizationFormattingRule{}}}, Presentation: visualizationir.GridVisualizationPresentation{RowHeight: 28, ShowHeader: true},
			}}
			visual, err := visualizationdefinition.New("table", spec, visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryDetail, ResultShape: visualizationdefinition.ResultDetailWindow, ModelID: "sales_model", DatasetID: "primary", Detail: &visualizationdefinition.DetailQueryBinding{TableID: "orders", Fields: []visualizationdefinition.FieldBinding{{FieldID: "state", Alias: "state"}}, Limit: 100}})
			if err != nil {
				t.Fatal(err)
			}
			compiled.Definition.Visualizations = map[string]visualizationdefinition.Definition{"table": visual}
			compiled, err = authoring.NewCompiledRevision(compiled.ProjectID, compiled.DashboardID, compiled.AuthoredRevision, compiled.Definition, compiled.SemanticIdentity, compiled.CompiledAt)
			if err != nil {
				t.Fatal(err)
			}
			if err := compiled.Validate(); err != nil {
				t.Fatal(err)
			}
			runtime := &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}
			provider := &resolverTestProvider{runtime: runtime, stateID: "state-1"}
			base := NewRuntimeMetrics(RuntimeMetricsOptions{Provider: provider, ProjectID: "project_1", PublishedCompilationReader: moduleCompilationReader{compiled: compiled}}).(runtimeMetrics)
			row := publication.Publication{ProjectID: "project_1", Name: "public-sales", Dashboard: "published", DefaultPage: "overview", Configured: true, ServingStateID: "state-1"}
			ctx := PublicationExecutionContext(context.Background(), row, "sales_model")
			metadata := dataquery.MetadataFromContext(ctx)
			if metadata.PrincipalID != "dashboard_publication:project_1.public-sales" || metadata.Surface != dataquery.SurfacePublicDashboard {
				t.Fatalf("public identity = %#v", metadata)
			}
			admitter := &preparationAdmitter{}
			calls := 0
			blockExecution := false
			var metrics queryruntime.Metrics = preparationExecutor{runtimeMetrics: base, check: func(ctx context.Context, request consumer.Request) error {
				calls++
				if _, ok := ctx.Value(dashboardRefreshRuntimeKey{}).(dashboardRefreshRuntime); !ok {
					return fmt.Errorf("executor lost refresh lease")
				}
				if got, ok := workload.FromContext(ctx); !ok || got != admitter {
					return fmt.Errorf("executor lost admission")
				}
				if _, ok := dataquery.GovernorFromContext(ctx); !ok {
					return fmt.Errorf("executor lost authorization governor")
				}
				if _, ok := dataquery.AuditRecorderFromContext(ctx); !ok {
					return fmt.Errorf("executor lost audit recorder")
				}
				if !reflect.DeepEqual(dataquery.MetadataFromContext(ctx), metadata) {
					return fmt.Errorf("decorators changed public metadata")
				}
				if request.PageID != "overview" || request.Filters.CompiledState == nil || len(request.Targets) != 1 || request.Targets[0].Kind != consumer.KindWindow {
					return fmt.Errorf("incorrect default request: %#v", request)
				}
				if blockExecution {
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}}
			if order == "production" {
				metrics = WithAdmission(metrics, admitter)
				metrics = queryauthz.New(metrics, queryauthz.Options{})
				metrics = WithQueryAudit(metrics, &preparationAudit{}, nil)
			} else {
				metrics = WithQueryAudit(metrics, &preparationAudit{}, nil)
				metrics = WithAdmission(metrics, admitter)
				metrics = queryauthz.New(metrics, queryauthz.Options{})
			}
			err = metrics.(interface {
				WithDashboardRefreshLease(context.Context, func(context.Context) error) error
			}).WithDashboardRefreshLease(ctx, func(ctx context.Context) error {
				request, warm, err := preparePublicDashboardRefresh(ctx, row)
				if err != nil {
					return err
				}
				if provider.acquires != 1 {
					return fmt.Errorf("preparation acquired %d runtimes, want one refresh lease", provider.acquires)
				}
				if warm.Filters.CompiledState == nil || len(warm.Filters.CompiledState.AppliedControls) != 1 {
					return fmt.Errorf("preparation lost non-empty compiled default filter state")
				}
				// Foreground defaults follow PublicDashboardUpdates; preparation
				// remains the same command algorithm, including window reset.
				filters := compiled.Definition.NormalizeFiltersForPage("overview", compiled.Definition.FiltersFromURLForPage("overview", nil))
				state, err := compiled.Definition.FilterStateFromURL("overview", nil)
				if err != nil {
					return err
				}
				filters.CompiledState = &state
				foreground, err := (command.Service{Metrics: base}).PrepareInitial(request, filters)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(warm, foreground) {
					return fmt.Errorf("warm defaults differ from foreground: warm=%#v foreground=%#v", warm, foreground)
				}
				return metrics.ExecuteConsumersPage(ctx, consumer.Request{DashboardID: request.DashboardID, PageID: request.PageID, ModelID: request.ModelID, Filters: warm.Filters, Targets: warm.Plan.Targets}, func(consumer.Result) bool { return true })
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("consumer calls = %d", calls)
			}
			// Exercise the actual worker adapter, including public resolution,
			// preparation and TargetWork's nested refresh-lease capability.
			row.ID, row.PublicID, row.Revision = "selected", "public-token", 1
			repository := &publicationRepositoryStub{row: row}
			m := &Module{prewarmConfig: prewarmTestConfig(), publicationService: publication.NewService(repository, nil)}
			m.handler.Metrics = metrics
			before := provider.acquires
			if got := m.executePrewarm(context.Background(), row); got != (prewarmResult{"completed", "executed"}) {
				t.Fatalf("worker adapter result = %#v", got)
			}
			if calls != 2 || provider.acquires != before+1 {
				t.Fatalf("worker calls=%d acquisitions=%d; want 2 calls and exactly one additional lease", calls, provider.acquires-before)
			}
			if provider.lease.releases != 1 {
				t.Fatal("completed warmup did not release its runtime lease")
			}
			synctest.Test(t, func(t *testing.T) {
				blockExecution = true
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				config := prewarmTestConfig()
				config.PublicationIDs = []string{row.ID}
				c := newPrewarmCoordinator(config, m.executePrewarm, nil)
				done := make(chan struct{})
				go func() { c.run(ctx); close(done) }()
				c.reconcile([]publication.Publication{row}, row.ServingStateID)
				synctest.Wait()
				if calls != 3 || provider.lease.releases != 0 {
					t.Fatal("warm owner did not retain its lease through active execution")
				}
				cancel()
				synctest.Wait()
				select {
				case <-done:
				default:
					t.Fatal("coordinator did not drain canceled governed execution")
				}
				if provider.lease.releases != 1 {
					t.Fatal("canceled warmup leaked its runtime lease")
				}
			})
		})
	}
}

func TestPublicPreparationRejectsMissingLease(t *testing.T) {
	if _, _, err := preparePublicDashboardRefresh(context.Background(), publication.Publication{}); err == nil {
		t.Fatal("preparation accepted an unleased runtime")
	}
}

func TestPublicPreparationRejectsPublicationMismatch(t *testing.T) {
	for _, mutation := range []string{"generation", "project", "suspended", "unconfigured", "page", "canceled"} {
		t.Run(mutation, func(t *testing.T) {
			compiled := moduleCompiledRevision(t, "project_1", "published", "state-1")
			provider := &resolverTestProvider{runtime: &resolverTestRuntime{model: &semanticmodel.Model{Name: "sales_model"}}, stateID: "state-1"}
			metrics := NewRuntimeMetrics(RuntimeMetricsOptions{Provider: provider, ProjectID: "project_1", PublishedCompilationReader: moduleCompilationReader{compiled: compiled}}).(runtimeMetrics)
			row := publication.Publication{ProjectID: "project_1", Dashboard: "published", DefaultPage: "overview", Configured: true, ServingStateID: "state-1"}
			err := metrics.WithDashboardRefreshLease(context.Background(), func(ctx context.Context) error {
				switch mutation {
				case "generation":
					row.ServingStateID = "state-2"
				case "project":
					row.ProjectID = "project_2"
				case "suspended":
					row.SuspendedAt = "2026-09-07"
				case "unconfigured":
					row.Configured = false
				case "page":
					row.DefaultPage = "missing"
				case "canceled":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				if _, _, err := preparePublicDashboardRefresh(ctx, row); err == nil {
					return fmt.Errorf("accepted %s publication", mutation)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if provider.acquires != 1 || provider.lease.releases != 1 {
				t.Fatal("rejected preparation did not retain/release exactly one lease")
			}
		})
	}
}
