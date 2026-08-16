package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type QueryAuditRecorder = queryaudit.Recorder

type auditedMetrics struct {
	queryruntime.Metrics
	recorder    queryaudit.Recorder
	principalID func(context.Context) (string, bool)
}

func WithQueryAudit(metrics queryruntime.Metrics, recorder queryaudit.Recorder, principalID func(context.Context) (string, bool)) queryruntime.Metrics {
	if metrics == nil {
		return metrics
	}
	return auditedMetrics{Metrics: metrics, recorder: recorder, principalID: principalID}
}

func (m auditedMetrics) MetricsForProject(projectID projectgraph.ResourceID) (queryruntime.Metrics, bool) {
	if err := projectID.Validate(); err != nil {
		return nil, false
	}
	provider, ok := m.Metrics.(queryruntime.ProjectMetrics)
	if ok {
		metrics, found := provider.MetricsForProject(projectID)
		if !found || metrics == nil {
			return nil, found
		}
		return auditedMetrics{Metrics: metrics, recorder: m.recorder, principalID: m.principalID}, true
	}
	if m.Metrics == nil {
		return nil, false
	}
	catalog := m.Metrics.Catalog()
	if catalog.Project.ID == projectID {
		return m, true
	}
	return nil, false
}

func (m auditedMetrics) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	if m.Metrics == nil {
		return dataquery.Result{}, errors.New("query metrics are not configured")
	}
	ctx = m.auditContext(ctx)
	request = request.WithMetadata(dataquery.MetadataFromContext(ctx))
	if err := request.ProjectID.Validate(); err != nil {
		return dataquery.Result{}, fmt.Errorf("project ID: %w", err)
	}
	return dataquery.ExecuteAudited(ctx, request, m.Metrics.ExecuteDataQuery)
}

func (m auditedMetrics) ExecuteDataQueryArrow(ctx context.Context, request dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	executor, ok := m.Metrics.(arrowquery.Executor)
	if !ok {
		return dataquery.Result{}, errors.New("query metrics do not support native Arrow execution")
	}
	ctx = m.auditContext(ctx)
	request = request.WithMetadata(dataquery.MetadataFromContext(ctx))
	if err := request.ProjectID.Validate(); err != nil {
		return dataquery.Result{}, fmt.Errorf("project ID: %w", err)
	}
	return dataquery.ExecuteAudited(ctx, request, func(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
		return executor.ExecuteDataQueryArrow(ctx, request, sink)
	})
}

func (m auditedMetrics) QueryDashboard(ctx context.Context, dashboardID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return m.QueryDashboardPage(ctx, dashboardID, "", filters)
}

func (m auditedMetrics) QueryCompiledFilterOptions(ctx context.Context, dashboardID string, query dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error) {
	provider, ok := m.Metrics.(interface {
		QueryCompiledFilterOptions(context.Context, string, dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error)
	})
	if !ok {
		return dashboardfilter.OptionResult{}, errors.New("compiled filter options are not supported by this runtime")
	}
	return provider.QueryCompiledFilterOptions(m.auditContext(ctx), dashboardID, query)
}

func (m auditedMetrics) QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	if m.Metrics == nil {
		return dashboard.EmptyPatch(filters.WithDefaults(), errors.New("query metrics are not configured")), nil
	}
	return m.Metrics.QueryDashboardPage(m.auditContext(ctx), dashboardID, pageID, filters)
}

func (m auditedMetrics) QueryDashboardVisualizations(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	if m.Metrics == nil {
		return dashboard.EmptyPatch(filters.WithDefaults(), errors.New("query metrics are not configured")), nil
	}
	return m.Metrics.QueryDashboardVisualizations(m.auditContext(ctx), dashboardID, pageID, filters)
}

func (m auditedMetrics) QueryVisualization(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error) {
	if m.Metrics == nil {
		return visualizationir.VisualizationEnvelope{}, errors.New("query metrics are not configured")
	}
	return m.Metrics.QueryVisualization(m.auditContext(ctx), dashboardID, pageID, filters, visualID)
}

func (m auditedMetrics) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	if m.Metrics == nil {
		return visualizationir.VisualizationEnvelope{}, errors.New("query metrics are not configured")
	}
	return m.Metrics.QueryVisualizationWindow(m.auditContext(ctx), dashboardID, pageID, filters, request)
}

func (m auditedMetrics) QueryVisualizationTile(ctx context.Context, dashboardID, visualID, revision string, zoom, x, y int) (dashboardruntime.SpatialTileResult, error) {
	port, ok := m.Metrics.(visualizationTileMetrics)
	if !ok {
		return dashboardruntime.SpatialTileResult{}, errors.New("spatial tile metrics are not configured")
	}
	return port.QueryVisualizationTile(m.auditContext(ctx), dashboardID, visualID, revision, zoom, x, y)
}

func (m auditedMetrics) ExpireVisualizationTileStream(streamID string) {
	if expirer, ok := m.Metrics.(interface{ ExpireVisualizationTileStream(string) }); ok {
		expirer.ExpireVisualizationTileStream(streamID)
	}
}

func (m auditedMetrics) QueryPublicVisualizationTile(ctx context.Context, publicID, dashboardID, visualID, revision string, zoom, x, y int) (dashboardruntime.SpatialTileResult, error) {
	port, ok := m.Metrics.(publicVisualizationTileMetrics)
	if !ok {
		return dashboardruntime.SpatialTileResult{}, errors.New("public spatial tile metrics are not configured")
	}
	return port.QueryPublicVisualizationTile(m.auditContext(ctx), publicID, dashboardID, visualID, revision, zoom, x, y)
}

func (m auditedMetrics) QuerySemantic(ctx context.Context, modelID string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	if m.Metrics == nil {
		return nil, errors.New("query metrics are not configured")
	}
	return m.Metrics.QuerySemantic(m.auditContext(ctx), modelID, request)
}

func (m auditedMetrics) PreviewSemantic(ctx context.Context, modelID string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	if m.Metrics == nil {
		return nil, errors.New("query metrics are not configured")
	}
	return m.Metrics.PreviewSemantic(m.auditContext(ctx), modelID, request)
}

func (m auditedMetrics) WithDashboardRefreshLease(ctx context.Context, run func(context.Context) error) error {
	if capability, ok := m.Metrics.(interface {
		WithDashboardRefreshLease(context.Context, func(context.Context) error) error
	}); ok {
		return capability.WithDashboardRefreshLease(m.auditContext(ctx), run)
	}
	return run(m.auditContext(ctx))
}

func (m auditedMetrics) DashboardTargetConcurrency() int {
	if capability, ok := m.Metrics.(interface{ DashboardTargetConcurrency() int }); ok && capability.DashboardTargetConcurrency() > 1 {
		return capability.DashboardTargetConcurrency()
	}
	return 1
}

func (m auditedMetrics) ExecuteConsumersPage(ctx context.Context, request consumer.Request, publish consumer.Publisher) error {
	port, ok := m.Metrics.(consumer.Executor)
	if !ok {
		return fmt.Errorf("%T does not provide dashboard consumer execution", m.Metrics)
	}
	return port.ExecuteConsumersPage(m.auditContext(ctx), request, publish)
}

func (m auditedMetrics) auditContext(ctx context.Context) context.Context {
	metadata := dataquery.MetadataFromContext(ctx)
	if metadata.PrincipalID == "" && m.principalID != nil {
		metadata.PrincipalID, _ = m.principalID(ctx)
	}
	ctx = dataquery.WithMetadata(ctx, metadata)
	if m.recorder != nil {
		ctx = dataquery.WithAuditRecorder(ctx, queryEventRecorder{repo: m.recorder})
	}
	return ctx
}

type queryEventRecorder struct{ repo queryaudit.Recorder }

func (r queryEventRecorder) RecordDataQuery(ctx context.Context, request dataquery.Query, result dataquery.Result) error {
	if r.repo == nil {
		return nil
	}
	return r.repo.RecordQueryEvent(ctx, queryEventInput(request, result))
}

func queryEventInput(request dataquery.Query, result dataquery.Result) queryaudit.EventInput {
	return queryaudit.EventInput{
		ProjectID: request.ProjectID, PrincipalID: request.PrincipalID, Surface: request.Surface,
		Operation: request.Operation, QueryKind: string(request.Kind), ModelID: request.ModelID, Target: request.Target,
		ObjectType: request.ObjectType, ObjectID: request.ObjectID, RequestID: request.RequestID, CorrelationID: request.CorrelationID,
		Status: firstNonEmpty(result.Status, dataquery.StatusSuccess), DurationMS: result.DurationMS,
		QueueWaitMS: result.QueueWaitMS, PlanningMS: result.PlanningMS, ConnectionWaitMS: result.ConnectionWaitMS,
		DatabaseMS: result.DatabaseMS, ExecutionMS: result.ExecutionMS, ExecutionState: result.ExecutionState,
		RowsReturned: result.RowsReturned, BytesEstimate: result.BytesEstimate, Error: result.Error,
		SQL: result.SQL, PlanText: result.PlanText, QueryJSON: queryShapeJSON(request),
	}
}

func queryShapeJSON(request dataquery.Query) string {
	bytes, err := json.Marshal(struct {
		ProjectID     string             `json:"projectId,omitempty"`
		Surface       string             `json:"surface,omitempty"`
		Operation     string             `json:"operation,omitempty"`
		RequestID     string             `json:"requestId,omitempty"`
		ObjectType    string             `json:"objectType,omitempty"`
		ObjectID      string             `json:"objectId,omitempty"`
		CorrelationID string             `json:"correlationId,omitempty"`
		ModelID       string             `json:"modelId,omitempty"`
		Kind          dataquery.Kind     `json:"kind"`
		Target        string             `json:"target,omitempty"`
		Fields        []dataquery.Field  `json:"fields,omitempty"`
		Measures      []dataquery.Field  `json:"measures,omitempty"`
		Value         dataquery.Field    `json:"value,omitempty"`
		Time          dataquery.Time     `json:"time,omitempty"`
		Filters       []dataquery.Filter `json:"filters,omitempty"`
		Sort          []dataquery.Sort   `json:"sort,omitempty"`
		Offset        int                `json:"offset,omitempty"`
		Limit         int                `json:"limit,omitempty"`
		BinCount      int                `json:"binCount,omitempty"`
		IncludeTotal  bool               `json:"includeTotal,omitempty"`
	}{
		request.ProjectID.String(), request.Surface, request.Operation, request.RequestID, request.ObjectType,
		request.ObjectID, request.CorrelationID, request.ModelID, request.Kind, request.Target, request.Fields,
		request.Measures, request.Value, request.Time, request.Filters, request.Sort, request.Offset, request.Limit,
		request.BinCount, request.IncludeTotal,
	})
	if err != nil {
		return "{}"
	}
	return string(bytes)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
