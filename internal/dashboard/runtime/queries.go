package runtime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
)

type QueryService struct {
	snapshots      *SnapshotService
	visualizations *VisualizationDataService
}

type SnapshotService struct {
	mu             *sync.RWMutex
	reports        *ReportService
	runtimes       map[string]*modelRuntime
	filters        *FilterService
	visualizations *VisualizationDataService
}

func (m *Service) QueryDashboard(ctx context.Context, dashboardID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return m.queries.QueryDashboard(ctx, dashboardID, filters)
}

func (m *Service) QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return m.queries.QueryDashboardPage(ctx, dashboardID, pageID, filters)
}

func (m *Service) QueryDashboardVisualizations(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return m.queries.QueryDashboardVisualizations(ctx, dashboardID, pageID, filters)
}

func (m *Service) QueryVisualization(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error) {
	return m.queries.QueryVisualization(ctx, dashboardID, pageID, filters, visualID)
}

func (m *Service) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	return m.queries.QueryVisualizationWindow(ctx, dashboardID, pageID, filters, request)
}

func (m *Service) QueryVisualizationSpatialWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationSpatialWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	return m.queries.QueryVisualizationSpatialWindow(ctx, dashboardID, pageID, filters, request)
}

func (s *QueryService) QueryDashboard(ctx context.Context, dashboardID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return s.snapshots.QueryDashboard(ctx, dashboardID, filters)
}

func (s *QueryService) QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	filters.ActivePageID = pageID
	return s.snapshots.QueryDashboardPage(ctx, dashboardID, pageID, filters)
}

func (s *QueryService) QueryDashboardVisualizations(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	filters.ActivePageID = pageID
	return s.snapshots.QueryDashboardPage(ctx, dashboardID, pageID, filters)
}

func (s *QueryService) QueryVisualization(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error) {
	filters.ActivePageID = pageID
	return s.snapshots.queryVisualizationPage(ctx, dashboardID, pageID, filters, visualID)
}

func (s *QueryService) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	filters.ActivePageID = pageID
	return s.visualizations.queryVisualizationWindowPage(ctx, dashboardID, pageID, filters, request, true)
}

func (s *QueryService) QueryVisualizationSpatialWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationSpatialWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	filters.ActivePageID = pageID
	return s.snapshots.querySpatialVisualPage(ctx, dashboardID, pageID, filters, request)
}

func (s *SnapshotService) QueryDashboard(ctx context.Context, dashboardID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return s.QueryDashboardPage(ctx, dashboardID, "", filters)
}

func (s *SnapshotService) QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	report, runtime, err := s.reports.reportRuntime(dashboardID, s.runtimes)
	if report != nil {
		page := dashboardPage(report, pageID)
		filters.ActivePageID = page.ID
		filters = report.NormalizeFiltersForPage(page.ID, filters)
	} else {
		filters = filters.WithDefaults()
	}
	if err != nil {
		return dashboard.EmptyPatch(filters, err), nil
	}
	if !runtime.ready {
		return dashboard.EmptyPatch(filters, runtime.missing), nil
	}
	if err := s.filters.validateSelections(runtime, report, filters); err != nil {
		return dashboard.EmptyPatch(filters, err), nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	patch := dashboard.Patch{
		Filters: filters,
		Status: dashboard.Status{
			Loading:         false,
			LastUpdated:     refreshLabel(runtime),
			ProgressPercent: dashboard.NormalizeProgressPercent(nil, false),
		},
		Visuals: map[string]visualizationir.VisualizationEnvelope{},
	}

	page := dashboardPage(report, pageID)
	allIDs := pageVisualizationIDs(page)
	inlineIDs := make([]string, 0, len(allIDs))
	windowIDs := make([]string, 0, len(allIDs))
	tiledIDs := make([]string, 0, len(allIDs))
	for _, visualID := range allIDs {
		definition, ok := report.Visualizations[visualID]
		if !ok {
			return dashboard.EmptyPatch(filters, fmt.Errorf("compiled visualization %q not found", visualID)), nil
		}
		if definition.Query.Spatial != nil && definition.Query.Spatial.Tiles != nil {
			tiledIDs = append(tiledIDs, visualID)
		} else if isWindowedResult(definition.Query.ResultShape) {
			windowIDs = append(windowIDs, visualID)
		} else {
			inlineIDs = append(inlineIDs, visualID)
		}
	}
	visuals := make(map[string]visualizationir.VisualizationEnvelope, len(allIDs))
	for _, visualID := range tiledIDs {
		envelope, queryErr := s.visualizations.tiledEnvelope(ctx, runtime, report, dashboardID, page.ID, visualID, filters)
		if queryErr != nil {
			definition := report.Visualizations[visualID]
			envelope, err = visualizationruntime.ErrorEnvelopeFromDefinition(definition, queryErr, 0, 0)
			if err != nil {
				return dashboard.EmptyPatch(filters, errors.Join(queryErr, err)), nil
			}
		}
		visuals[visualID] = envelope
	}
	inlineVisuals, err := s.visualizations.visuals(ctx, runtime, report, filters, inlineIDs)
	if err != nil {
		inlineVisuals = make(map[string]visualizationir.VisualizationEnvelope, len(inlineIDs))
		for _, visualID := range inlineIDs {
			target, queryErr := s.visualizations.visuals(ctx, runtime, report, filters, []string{visualID})
			if queryErr == nil {
				inlineVisuals[visualID] = target[visualID]
				continue
			}
			definition := report.Visualizations[visualID]
			envelope, envelopeErr := visualizationruntime.ErrorEnvelopeFromDefinition(definition, queryErr, 0, 0)
			if envelopeErr != nil {
				return dashboard.EmptyPatch(filters, errors.Join(queryErr, envelopeErr)), nil
			}
			inlineVisuals[visualID] = envelope
		}
	}
	for visualID, envelope := range inlineVisuals {
		visuals[visualID] = envelope
	}
	for _, visualID := range windowIDs {
		request := dashboard.TableRequest{Table: visualID, Block: "a", Count: dashboard.TableChunkSize}.WithDefaults()
		table, queryErr := s.visualizations.queryTablePage(ctx, dashboardID, page.ID, filters, request, true)
		if queryErr != nil {
			definition := report.Visualizations[visualID]
			envelope, envelopeErr := visualizationruntime.ErrorEnvelopeFromDefinition(definition, queryErr, 0, 0)
			if envelopeErr != nil {
				return dashboard.EmptyPatch(filters, errors.Join(queryErr, envelopeErr)), nil
			}
			visuals[visualID] = envelope
			continue
		}
		definition, ok := report.Visualizations[visualID]
		if !ok {
			return dashboard.EmptyPatch(filters, fmt.Errorf("compiled visualization %q not found", visualID)), nil
		}
		envelope, envelopeErr := visualizationruntime.WindowEnvelopeFromDefinition(definition, table, 0, 0)
		if envelopeErr != nil {
			envelope, errorEnvelopeErr := visualizationruntime.ErrorEnvelopeFromDefinition(definition, envelopeErr, 0, 0)
			if errorEnvelopeErr != nil {
				return dashboard.EmptyPatch(filters, errors.Join(envelopeErr, errorEnvelopeErr)), nil
			}
			visuals[visualID] = envelope
			continue
		}
		envelope.Highlights, envelopeErr = selectedHighlights(runtime, report, filters, visualID)
		if envelopeErr != nil {
			return dashboard.EmptyPatch(filters, envelopeErr), nil
		}
		if envelopeErr := visualizationir.ValidateEnvelope(envelope); envelopeErr != nil {
			return dashboard.EmptyPatch(filters, envelopeErr), nil
		}
		visuals[visualID] = envelope
	}
	patch.Visuals = visuals

	return patch, nil
}

func (s *SnapshotService) queryVisualizationPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error) {
	report, runtime, err := s.reports.reportRuntime(dashboardID, s.runtimes)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	definition, ok := report.Visualizations[visualID]
	if !ok {
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("unknown visualization %q", visualID)
	}
	if definition.Query.Spatial != nil && definition.Query.Spatial.Tiles != nil {
		page := dashboardPage(report, pageID)
		filters = report.NormalizeFiltersForPage(page.ID, filters)
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.visualizations.tiledEnvelope(ctx, runtime, report, dashboardID, page.ID, visualID, filters)
	}
	if isWindowedResult(definition.Query.ResultShape) {
		return s.visualizations.queryVisualizationWindowPage(ctx, dashboardID, pageID, filters, visualizationir.VisualizationWindowRequest{VisualID: visualID, SpecRevision: definition.SpecRevision, BlockID: "a", Limit: dashboard.TableChunkSize}, true)
	}
	visuals, err := s.queryVisualsPage(ctx, dashboardID, pageID, filters, []string{visualID})
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	return visuals[visualID], nil
}

func (s *SnapshotService) querySpatialVisualPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request dashboard.SpatialWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	report, runtime, err := s.reports.reportRuntime(dashboardID, s.runtimes)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	if !runtime.ready {
		return visualizationir.VisualizationEnvelope{}, runtime.missing
	}
	page := dashboardPage(report, pageID)
	filters = report.NormalizeFiltersForPage(page.ID, filters)
	if !slices.Contains(pageVisualizationIDs(page), request.VisualID) {
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("visual %q is not on page %q", request.VisualID, page.ID)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.visualizations.spatialEnvelope(ctx, runtime, report, filters, request)
}

func (s *SnapshotService) queryVisualsPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualIDs []string) (map[string]visualizationir.VisualizationEnvelope, error) {
	report, runtime, err := s.reports.reportRuntime(dashboardID, s.runtimes)
	if err != nil {
		return nil, err
	}
	if !runtime.ready {
		return nil, runtime.missing
	}
	page := dashboardPage(report, pageID)
	filters = report.NormalizeFiltersForPage(page.ID, filters)
	pageIDs := pageVisualizationIDs(page)
	for _, visualID := range visualIDs {
		if !slices.Contains(pageIDs, visualID) {
			return nil, fmt.Errorf("visual %q is not on page %q", visualID, page.ID)
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.visualizations.visuals(ctx, runtime, report, filters, append([]string{}, visualIDs...))
}

func (s *SnapshotService) queryVisualBundlePage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualIDs []string) (map[string]visualizationir.VisualizationEnvelope, error) {
	report, runtime, err := s.reports.reportRuntime(dashboardID, s.runtimes)
	if err != nil {
		return nil, err
	}
	if !runtime.ready {
		return nil, runtime.missing
	}
	page := dashboardPage(report, pageID)
	filters = report.NormalizeFiltersForPage(page.ID, filters)
	pageIDs := pageVisualizationIDs(page)
	for _, visualID := range visualIDs {
		if !slices.Contains(pageIDs, visualID) {
			return nil, fmt.Errorf("visual %q is not on page %q", visualID, page.ID)
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.visualizations.bundledVisuals(ctx, runtime, report, filters, append([]string{}, visualIDs...))
}

func dashboardPage(report *dashboarddefinition.Definition, pageID string) dashboard.Page {
	if report == nil || len(report.Pages) == 0 {
		return dashboard.Page{}
	}
	if pageID != "" {
		for _, page := range report.Pages {
			if page.ID == pageID {
				return page.WithDefaults()
			}
		}
	}
	return report.Pages[0].WithDefaults()
}

func pageVisualizationIDs(page dashboard.Page) []string {
	seen := map[string]struct{}{}
	ids := []string{}
	for _, item := range page.Visuals {
		if item.Visual == "" {
			continue
		}
		if _, ok := seen[item.Visual]; ok {
			continue
		}
		seen[item.Visual] = struct{}{}
		ids = append(ids, item.Visual)
	}
	sort.Strings(ids)
	return ids
}

func isWindowedResult(shape visualizationdefinition.ResultShape) bool {
	return shape == visualizationdefinition.ResultDetailWindow || shape == visualizationdefinition.ResultMatrixWindow || shape == visualizationdefinition.ResultPivotWindow
}

func (s *VisualizationDataService) queryVisualizationWindowPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, window visualizationir.VisualizationWindowRequest, includeTotal bool) (visualizationir.VisualizationEnvelope, error) {
	request, err := visualizationTableRequest(window)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	report, runtime, err := s.reports.reportRuntime(dashboardID, s.runtimes)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	page := dashboardPage(report, pageID)
	if !slices.Contains(pageVisualizationIDs(page), window.VisualID) {
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("visual %q is not on page %q", window.VisualID, page.ID)
	}
	definition, ok := report.Visualizations[window.VisualID]
	if !ok || !isWindowedResult(definition.Query.ResultShape) {
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("visual %q is not windowed", window.VisualID)
	}
	if window.SpecRevision != "" && window.SpecRevision != definition.SpecRevision {
		return visualizationir.VisualizationEnvelope{}, fmt.Errorf("visual %q specification revision is stale", window.VisualID)
	}
	table, err := s.queryTablePage(ctx, dashboardID, pageID, filters, request, includeTotal)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	envelope, err := visualizationruntime.WindowEnvelopeFromDefinition(definition, table, window.DataRevision, 0)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	envelope.Highlights, err = selectedHighlights(runtime, report, filters, window.VisualID)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	return envelope, visualizationir.ValidateEnvelope(envelope)
}

func visualizationTableRequest(window visualizationir.VisualizationWindowRequest) (dashboard.TableRequest, error) {
	if window.VisualID == "" {
		return dashboard.TableRequest{}, fmt.Errorf("visual window requires a visual ID")
	}
	if window.Start < 0 || window.Limit < 0 || window.Start > int64(^uint(0)>>1) || window.Limit > int64(^uint(0)>>1) {
		return dashboard.TableRequest{}, fmt.Errorf("invalid visual window coordinates")
	}
	if len(window.Sort) > 1 {
		return dashboard.TableRequest{}, fmt.Errorf("visual window supports exactly one active sort")
	}
	request := dashboard.TableRequest{Table: window.VisualID, Block: window.BlockID, Start: int(window.Start), Count: int(window.Limit), RequestSeq: int(window.RequestSeq), ResetVersion: int(window.ResetVersion)}
	if len(window.Sort) == 1 {
		request.Sort.Key = window.Sort[0].Field.Field
		switch window.Sort[0].Direction {
		case visualizationir.VisualizationSortDirectionAscending:
			request.Sort.Direction = "asc"
		case visualizationir.VisualizationSortDirectionDescending:
			request.Sort.Direction = "desc"
		default:
			return dashboard.TableRequest{}, fmt.Errorf("unsupported visual window sort direction %q", window.Sort[0].Direction)
		}
	}
	return request, nil
}

// queryTableRowsPage returns the requested table window without making an
// exact count part of the row-query critical path. Callers that progressively
// render tables can publish this payload before resolving the total.
func (s *VisualizationDataService) queryTableRowsPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request dashboard.TableRequest) (dashboard.Table, error) {
	return s.queryTablePage(ctx, dashboardID, pageID, filters, request, false)
}

// queryTableCountPage resolves exact governed table cardinality independently
// from the row window so it can be cached and delivered as secondary metadata.
func (s *VisualizationDataService) queryTableCountPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request dashboard.TableRequest) (int, error) {
	report, runtime, err := s.reports.reportRuntime(dashboardID, s.runtimes)
	if report != nil {
		page := dashboardPage(report, pageID)
		filters = report.NormalizeFiltersForPage(page.ID, filters)
	} else {
		filters = filters.WithDefaults()
	}
	request = s.reports.NormalizeVisualizationWindow(dashboardID, request)
	if err != nil {
		return 0, err
	}
	if !runtime.ready {
		return 0, runtime.missing
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	tableModel, ok := report.Visualizations[request.Table]
	if !ok {
		return 0, fmt.Errorf("unknown table %q", request.Table)
	}
	if tableModel.Query.Kind == visualizationdefinition.QueryMatrix || tableModel.Query.Kind == visualizationdefinition.QueryPivot {
		table, err := s.queryAggregateTable(ctx, runtime, report, request, tableModel, filters)
		total, _ := table.Cardinality.ExactValue()
		return total, err
	}
	return s.queryDataTableCount(ctx, runtime, report, request, tableModel, filters)
}

func (s *VisualizationDataService) queryTablePage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request dashboard.TableRequest, includeTotal bool) (dashboard.Table, error) {
	report, runtime, err := s.reports.reportRuntime(dashboardID, s.runtimes)
	if report != nil {
		page := dashboardPage(report, pageID)
		filters = report.NormalizeFiltersForPage(page.ID, filters)
	} else {
		filters = filters.WithDefaults()
	}
	request = s.reports.NormalizeVisualizationWindow(dashboardID, request)
	if err != nil {
		return dashboard.EmptyTable(request, err), nil
	}
	if !runtime.ready {
		return dashboard.EmptyTable(request, runtime.missing), nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	tableModel, ok := report.Visualizations[request.Table]
	if !ok {
		return dashboard.EmptyTable(request, fmt.Errorf("unknown table %q", request.Table)), nil
	}
	if tableModel.Query.Kind == visualizationdefinition.QueryMatrix || tableModel.Query.Kind == visualizationdefinition.QueryPivot {
		return s.queryAggregateTable(ctx, runtime, report, request, tableModel, filters)
	}
	table, err := s.queryDataTableWindow(ctx, runtime, report, request, tableModel, filters)
	_, totalKnown := table.Cardinality.ExactValue()
	if err != nil || !includeTotal || table.Error != "" || totalKnown {
		return table, err
	}
	totalRows, err := s.queryDataTableCount(ctx, runtime, report, request, tableModel, filters)
	if err != nil {
		return dashboard.EmptyTable(request, err), nil
	}
	applyTableTotal(&table, totalRows)
	return table, nil
}

func (s *VisualizationDataService) queryDataTableWindow(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, request dashboard.TableRequest, definition visualizationdefinition.Definition, filters dashboard.Filters) (dashboard.Table, error) {
	tableModel, err := newTablePlan(definition)
	if err != nil {
		return dashboard.EmptyTable(request, err), nil
	}
	base, err := visualizationir.SpecificationBase(definition.Spec)
	if err != nil {
		return dashboard.EmptyTable(request, err), nil
	}
	if base.Calculations != nil && len(*base.Calculations) > 0 {
		return s.queryCalculatedDataTableWindow(ctx, runtime, report, request, tableModel, filters, base)
	}
	count := request.Count
	if count <= 0 {
		count = dashboard.TableChunkSize
	}
	if count > dashboard.TableMaxRequestCount {
		count = dashboard.TableMaxRequestCount
	}
	start := request.Start
	queryCount := count
	blockStarts := map[string]int{request.Block: start}
	if request.Block == "all" {
		currentStart := max(0, (start/count)*count)
		if currentStart == 0 {
			blockStarts = map[string]int{"a": 0, "b": count, "c": count * 2}
		} else {
			blockStarts = map[string]int{"a": max(0, currentStart-count), "b": currentStart, "c": currentStart + count}
		}
		start = blockStarts["a"]
		queryCount = count * 3
	}
	rowRequest, err := s.tableRowRequest(ctx, runtime, report, tableModel, filters, request, start, queryCount)
	if err != nil {
		return dashboard.EmptyTable(request, err), nil
	}
	result, err := runtime.data.ExecuteDataQuery(ctx, reportRowDataQuery(report.SemanticModel, rowRequest, false))
	if err != nil {
		return dashboard.EmptyTable(request, err), nil
	}
	rows := tableRowsFromAnalytics(reportRowsFromDataQuery(result.Rows))
	// A short first page proves the exact cardinality. At a non-zero offset it
	// only proves that the requested window reached or overshot the end; the
	// true count may be lower than start and must remain unknown.
	totalRowsKnown := start == 0 && len(rows) < queryCount
	totalRows := 0
	cardinality := dashboard.TableCardinality{Kind: dashboard.CardinalityUnknown}
	availableRows := dashboard.TableInteractiveRowCap
	if totalRowsKnown {
		totalRows = start + len(rows)
		cardinality = dashboard.ExactCardinality(totalRows)
		availableRows = min(totalRows, dashboard.TableInteractiveRowCap)
	} else if len(rows) > 0 {
		cardinality = dashboard.LowerBoundCardinality(start + len(rows))
	}
	blocks := make(map[string]dashboard.TableBlock, len(blockStarts))
	for _, block := range []string{"a", "b", "c"} {
		blockStart, ok := blockStarts[block]
		if !ok {
			continue
		}
		rowStart := min(len(rows), max(0, blockStart-start))
		rowEnd := min(len(rows), rowStart+count)
		blocks[block] = dashboard.TableBlock{
			Start: blockStart, RequestSeq: request.RequestSeq, ResetVersion: request.ResetVersion,
			Sort: request.Sort, Rows: rows[rowStart:rowEnd],
		}
	}
	if request.Block != "all" {
		blocks[request.Block] = dashboard.TableBlock{
			Start: start, RequestSeq: request.RequestSeq, ResetVersion: request.ResetVersion,
			Sort: request.Sort, Rows: rows,
		}
	}
	style := tableModel.Style.WithDefaults()
	return dashboard.Table{
		Version:       2,
		Kind:          tableModel.Kind,
		Title:         tableModel.Title,
		Style:         style,
		Interaction:   tableModel.Interaction,
		Selection:     []dashboard.InteractionSelectionEntry{},
		Columns:       tableModel.Columns,
		Cardinality:   cardinality,
		AvailableRows: availableRows,
		IsCapped:      totalRows > availableRows,
		RowCap:        dashboard.TableInteractiveRowCap,
		ChunkSize:     dashboard.TableChunkSize,
		RowHeight:     style.RowHeight(),
		ResetVersion:  request.ResetVersion,
		Sort:          request.Sort,
		Blocks:        blocks,
		LoadingBlock:  "",
		Error:         "",
	}, nil
}

func (s *VisualizationDataService) queryCalculatedDataTableWindow(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, request dashboard.TableRequest, tableModel tablePlan, filters dashboard.Filters, base visualizationir.VisualizationSpecBase) (dashboard.Table, error) {
	rowRequest, err := s.tableRowRequest(ctx, runtime, report, tableModel, filters, request, 0, dashboard.TableInteractiveRowCap+1)
	if err != nil {
		return dashboard.EmptyTable(request, err), nil
	}
	result, err := runtime.data.ExecuteDataQuery(ctx, reportRowDataQuery(report.SemanticModel, rowRequest, false))
	if err != nil {
		return dashboard.EmptyTable(request, err), nil
	}
	records := tableRowsFromAnalytics(reportRowsFromDataQuery(result.Rows))
	isCapped := len(records) > dashboard.TableInteractiveRowCap
	if isCapped {
		records = records[:dashboard.TableInteractiveRowCap]
	}
	completeness := visualizationir.VisualizationCompletenessComplete
	if len(records) == 0 {
		completeness = visualizationir.VisualizationCompletenessEmpty
	} else if isCapped {
		completeness = visualizationir.VisualizationCompletenessTruncated
	}
	records, err = applyCalculationsToTableRecords(base, tableModel.Definition.Query.DatasetID, records, completeness)
	if err != nil {
		return dashboard.EmptyTable(request, err), nil
	}
	sortAggregateTableRows(records, request.Sort)

	count := request.Count
	if count <= 0 {
		count = dashboard.TableChunkSize
	}
	if count > dashboard.TableMaxRequestCount {
		count = dashboard.TableMaxRequestCount
	}
	start := max(0, request.Start)
	blockStarts := map[string]int{request.Block: start}
	if request.Block == "all" {
		currentStart := max(0, (start/count)*count)
		if currentStart == 0 {
			blockStarts = map[string]int{"a": 0, "b": count, "c": count * 2}
		} else {
			blockStarts = map[string]int{"a": max(0, currentStart-count), "b": currentStart, "c": currentStart + count}
		}
	}
	blocks := make(map[string]dashboard.TableBlock, len(blockStarts))
	for block, blockStart := range blockStarts {
		rowStart := min(len(records), blockStart)
		rowEnd := min(len(records), rowStart+count)
		blocks[block] = dashboard.TableBlock{
			Start: blockStart, RequestSeq: request.RequestSeq, ResetVersion: request.ResetVersion,
			Sort: request.Sort, Rows: records[rowStart:rowEnd],
		}
	}
	cardinality := dashboard.ExactCardinality(len(records))
	if isCapped {
		cardinality = dashboard.LowerBoundCardinality(dashboard.TableInteractiveRowCap + 1)
	}
	style := tableModel.Style.WithDefaults()
	return dashboard.Table{
		Version: 2, Kind: tableModel.Kind, Title: tableModel.Title, Style: style, Interaction: tableModel.Interaction,
		Selection: []dashboard.InteractionSelectionEntry{}, Columns: tableModel.Columns, Cardinality: cardinality,
		AvailableRows: len(records), IsCapped: isCapped, RowCap: dashboard.TableInteractiveRowCap, ChunkSize: count,
		RowHeight: style.RowHeight(), ResetVersion: request.ResetVersion, Sort: request.Sort, Blocks: blocks,
	}, nil
}

func applyCalculationsToTableRecords(base visualizationir.VisualizationSpecBase, datasetID string, records []map[string]any, completeness visualizationir.VisualizationCompleteness) ([]map[string]any, error) {
	var schema *visualizationir.VisualizationDatasetSchema
	for index := range base.Datasets {
		if base.Datasets[index].ID == datasetID {
			schema = &base.Datasets[index]
			break
		}
	}
	if schema == nil {
		return nil, fmt.Errorf("calculated table targets unknown dataset %q", datasetID)
	}
	columns := sourceFrameColumns(schema.Fields)
	rows := make([][]any, len(records))
	for rowIndex, record := range records {
		rows[rowIndex] = make([]any, len(columns))
		for columnIndex, column := range columns {
			rows[rowIndex][columnIndex] = record[column]
		}
	}
	calculated, _, err := visualizationruntime.ApplyVisualCalculations(base, datasetID, visualizationruntime.Frame{Columns: columns, Rows: rows, Completeness: completeness}, completeness)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, len(records))
	for rowIndex, record := range records {
		next := make(map[string]any, len(calculated.Columns))
		for key, value := range record {
			next[key] = value
		}
		for columnIndex, column := range calculated.Columns {
			next[column] = calculated.Rows[rowIndex][columnIndex]
		}
		out[rowIndex] = next
	}
	return out, nil
}

func (s *VisualizationDataService) queryDataTableCount(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, request dashboard.TableRequest, definition visualizationdefinition.Definition, filters dashboard.Filters) (int, error) {
	tableModel, err := newTablePlan(definition)
	if err != nil {
		return 0, err
	}
	rowRequest, err := s.tableRowRequest(ctx, runtime, report, tableModel, filters, request, 0, 1)
	if err != nil {
		return 0, err
	}
	query := countOnlyDataQuery(reportRowDataQuery(report.SemanticModel, rowRequest, true))
	result, err := runtime.data.ExecuteDataQuery(ctx, query)
	if err != nil {
		return 0, err
	}
	if !result.TotalRowsKnown {
		return 0, fmt.Errorf("table count query did not return total rows")
	}
	return result.TotalRows, nil
}

func applyTableTotal(table *dashboard.Table, totalRows int) {
	table.Cardinality = dashboard.ExactCardinality(totalRows)
	table.AvailableRows = min(totalRows, dashboard.TableInteractiveRowCap)
	table.IsCapped = totalRows > table.AvailableRows
}

func refreshLabel(runtime *modelRuntime) string {
	if runtime.data == nil || runtime.data.LastRefresh().IsZero() {
		return time.Now().Format("15:04:05")
	}
	return runtime.data.LastRefresh().Format("15:04:05")
}
