package http

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectnavigation "github.com/flidai/leapview/internal/project/navigation"
	projectui "github.com/flidai/leapview/internal/project/ui"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	pipelineRunEventResourceKind = "refresh"
	pipelineRunEventPageSize     = 200
	pipelineRunChildRunPageSize  = 100
)

// RunAttemptReader is an optional extension to the scoped run reader. It is
// deliberately separate so deployments with legacy/test stores can still
// render run details while reporting attempt evidence as unavailable.
type RunAttemptReader interface {
	ListRunAttempts(context.Context, refreshrun.ReadScope, string) (refreshrun.RunAttemptPage, error)
}

type pipelineRunDocumentData struct {
	Page    projectsignals.PipelineRunDetailPageSignal
	Project projectnavigation.Catalog
}

type pipelineRunDocumentError struct {
	status int
	err    error
}

func (e pipelineRunDocumentError) Error() string { return e.err.Error() }
func (e pipelineRunDocumentError) Unwrap() error { return e.err }

// pipelineRunDocument serves the canonical full-page investigation view for
// one root run. The project path selector is checked against the run's
// generation-bound plan before the page uses any historical metadata.
func (h *BrowserHandler) pipelineRunDocument(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var err error
	r, err = pipelineRunCanonicalRouteRequest(r)
	if err != nil {
		writePipelineRunDocumentError(w, r, pipelineRunDocumentError{status: stdhttp.StatusNotFound, err: errors.New("pipeline run not found")})
		return
	}
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindPipeline}) {
		return
	}
	data, err := h.pipelineRunDocumentData(r)
	if err != nil {
		writePipelineRunDocumentError(w, r, err)
		return
	}
	writeDocument(w, projectui.PipelineRunDetailDocument(data.Project, data.Page, h.csrf(r), h.layout(r)))
}

// pipelineRunBootstrap is the typed /updates bootstrap paired with
// pipelineRunDocument. The server reconstructs the same run-scoped evidence
// for both the first document and reconnects.
func (h *BrowserHandler) pipelineRunBootstrap(w stdhttp.ResponseWriter, r *stdhttp.Request) (map[string]any, bool) {
	var err error
	r, err = pipelineRunCanonicalRouteRequest(r)
	if err != nil {
		writePipelineRunDocumentError(w, r, pipelineRunDocumentError{status: stdhttp.StatusNotFound, err: errors.New("pipeline run not found")})
		return nil, false
	}
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindPipeline}) {
		return nil, false
	}
	data, err := h.pipelineRunDocumentData(r)
	if err != nil {
		writePipelineRunDocumentError(w, r, err)
		return nil, false
	}
	patch := projectui.PipelineRunDetailBootstrapSignals(data.Project, data.Page, h.layout(r))
	// A fresh stream identity lets the browser confirm recovery even when the
	// durable run projection is byte-for-byte unchanged after reconnecting.
	runtime := patch["runtime"].(projectsignals.RouteRuntimeSignal)
	runtime.StreamInstanceID = projectsignals.Optional(uuid.NewString())
	patch["runtime"] = runtime
	return patch, true
}

func (h *BrowserHandler) pipelineRunDocumentData(r *stdhttp.Request) (pipelineRunDocumentData, error) {
	if h == nil || r == nil {
		return pipelineRunDocumentData{}, pipelineRunDocumentError{status: stdhttp.StatusServiceUnavailable, err: errors.New("pipeline run browser is unavailable")}
	}
	var err error
	r, err = pipelineRunCanonicalRouteRequest(r)
	if err != nil {
		return pipelineRunDocumentData{}, pipelineRunDocumentError{status: stdhttp.StatusNotFound, err: errors.New("pipeline run not found")}
	}
	pipelineID := strings.TrimSpace(chi.URLParam(r, "asset"))
	runID := strings.TrimSpace(chi.URLParam(r, "run"))
	if pipelineID == "" {
		pipelineID = strings.TrimSpace(r.URL.Query().Get("asset"))
	}
	if runID == "" {
		runID = strings.TrimSpace(r.URL.Query().Get("run"))
	}
	parsedPipelineID, err := projectgraph.NewResourceID(pipelineID)
	if err != nil || parsedPipelineID.String() != pipelineID || runID == "" {
		return pipelineRunDocumentData{}, pipelineRunDocumentError{status: stdhttp.StatusNotFound, err: errors.New("pipeline run not found")}
	}
	projectID, err := h.boundProject(r.Context())
	if err != nil {
		return pipelineRunDocumentData{}, pipelineRunDocumentError{status: stdhttp.StatusServiceUnavailable, err: err}
	}
	scope := refreshrun.ReadScope{
		ProjectID: projectID,
		Environment: string(servingstate.NormalizeEnvironment(
			servingstate.Environment(h.Environment),
		)),
	}
	if err := scope.Validate(); err != nil {
		return pipelineRunDocumentData{}, pipelineRunDocumentError{status: stdhttp.StatusServiceUnavailable, err: err}
	}
	if h.RunDetailReader == nil {
		return pipelineRunDocumentData{}, pipelineRunDocumentError{status: stdhttp.StatusServiceUnavailable, err: errors.New("pipeline run detail reader is unavailable")}
	}
	run, err := h.RunDetailReader.GetRun(r.Context(), scope, runID)
	if err != nil || !scope.Matches(run.Identity) || !validPipelineRootRun(run, projectID, scope.Environment, parsedPipelineID) {
		return pipelineRunDocumentData{}, pipelineRunDocumentError{status: stdhttp.StatusNotFound, err: errors.New("pipeline run not found")}
	}
	if !h.pipelineRunAuthorized(r, parsedPipelineID) {
		return pipelineRunDocumentData{}, pipelineRunDocumentError{status: stdhttp.StatusNotFound, err: errors.New("pipeline run not found")}
	}
	if run.SemanticModelID.Validate() != nil || run.PlanDigest == "" || !validPipelineRunMaterializationScope(run.MaterializationScope) {
		return pipelineRunDocumentData{}, pipelineRunDocumentError{status: stdhttp.StatusServiceUnavailable, err: errors.New("pipeline run execution evidence is unavailable or inconsistent")}
	}
	pageState := projectui.PipelineRunDetailPageState{
		Title:       "Run " + shortPipelineRunID(run.ID),
		Description: "Execution status and diagnostics for this pipeline run.",
		ActiveTab:   pipelineRunRequestedSection(r), Environment: run.Identity.Environment,
		PipelineID: parsedPipelineID.String(), PipelineTitle: h.currentPipelineTitle(r.Context(), parsedPipelineID),
		PipelineHref: "/pipelines/" + url.PathEscape(parsedPipelineID.String()) + "/details",
		RunID:        run.ID, Status: run.Status, StatusLabel: pipelineRunStatusLabel(run.Status), RunError: run.Error,
	}
	historicalGraph, historicalGraphAvailable, historicalPipelineTitle, graphUnavailableReason := h.historicalPipelineGraph(r.Context(), r, projectID, scope, run, parsedPipelineID)
	if historicalGraphAvailable && historicalPipelineTitle != "" {
		pageState.PipelineTitle = historicalPipelineTitle
	}
	children, childRunsErr := h.RunDetailReader.ListChildRuns(r.Context(), scope, run.ID)
	if childRunsErr != nil {
		children = nil
	}
	pageState.Execution = projectsignals.PipelineRunExecutionSignal{
		CreatedAt:                 run.CreatedAt,
		ValidationOutcome:         "unknown",
		ValidationTimingAvailable: false,
		PublicationOutcome:        "unverified",
		Attempts:                  []projectsignals.PipelineRunAttemptSignal{},
		Models:                    pipelineRunModelsFrom(run.MaterializationScope, run, children),
	}
	attemptReader, attemptReaderAvailable := h.RunDetailReader.(RunAttemptReader)
	rootAttemptsUnavailable := !attemptReaderAvailable
	if attemptReaderAvailable {
		attempts, attemptErr := attemptReader.ListRunAttempts(r.Context(), scope, run.ID)
		if attemptErr != nil {
			rootAttemptsUnavailable = true
		} else {
			pageState.Execution.Attempts = pipelineRunAttemptSignalsFrom(attempts.Attempts)
			if attempts.Truncated {
				pageState.Execution.AttemptsTruncated = projectsignals.Optional(true)
			}
		}
	}
	if rootAttemptsUnavailable {
		pageState.Execution.AttemptsUnavailable = projectsignals.Optional(true)
	}
	childrenByModel := pipelineRunChildrenByModel(run.MaterializationScope, run, children)
	for modelIndex := range pageState.Execution.Models {
		child, exists := childrenByModel[pageState.Execution.Models[modelIndex].ModelID]
		if childRunsErr != nil || !attemptReaderAvailable {
			pageState.Execution.Models[modelIndex].AttemptsUnavailable = projectsignals.Optional(true)
			continue
		}
		if !exists {
			continue
		}
		attempts, attemptErr := attemptReader.ListRunAttempts(r.Context(), scope, child.ID)
		if attemptErr != nil {
			pageState.Execution.Models[modelIndex].AttemptsUnavailable = projectsignals.Optional(true)
			continue
		}
		pageState.Execution.Models[modelIndex].Attempts = pipelineRunAttemptSignalsFrom(attempts.Attempts)
		if attempts.Truncated {
			pageState.Execution.Models[modelIndex].AttemptsTruncated = projectsignals.Optional(true)
		}
	}
	if h.RunPublicationReader == nil {
		pageState.Execution.PublicationOutcome = pipelineRunPublicationOutcome(run.Status, false, errors.New("publication reader unavailable"))
	} else {
		publication, published, publicationErr := h.RunPublicationReader.RunPublication(r.Context(), scope, run.ID)
		pageState.Execution.PublicationOutcome = pipelineRunPublicationOutcome(run.Status, published, publicationErr)
		if publicationErr == nil && published {
			pageState.Execution.Publication = &projectsignals.PipelineRunPublicationSignal{
				PublishedAt:    publication.CommittedAt.UTC().Format(time.RFC3339Nano),
				SnapshotID:     publication.SnapshotID,
				ServingStateID: publication.ResultGenerationID,
			}
		}
	}
	if childRunsErr != nil || len(children) >= pipelineRunChildRunPageSize {
		pageState.Execution.ModelsUnavailable = projectsignals.Optional(true)
	}
	if historicalGraphAvailable {
		pageState.Execution.Graph = &historicalGraph
	} else {
		pageState.Execution.GraphUnavailableReason = projectsignals.Optional(graphUnavailableReason)
	}
	if run.StartedAt != "" {
		pageState.Execution.StartedAt = projectsignals.Optional(run.StartedAt)
	}
	if run.FinishedAt != "" {
		pageState.Execution.FinishedAt = projectsignals.Optional(run.FinishedAt)
	}
	if duration := pipelineRunDuration(run.StartedAt, run.FinishedAt); duration != "" {
		pageState.Execution.Duration = projectsignals.Optional(duration)
	}
	var events pipelineRunEventPage
	events.Rows = []projectsignals.PipelineRunEventSignal{}
	eventsUnavailable := h.RunEventReader == nil
	if !eventsUnavailable {
		events, err = h.pipelineRunEvents(r.Context(), run.ID)
		eventsUnavailable = err != nil
	}
	if eventsUnavailable {
		events.Rows = []projectsignals.PipelineRunEventSignal{}
	}
	pageState.Events = events.Rows
	pageState.EventsTruncated = events.Truncated
	pageState.EventsUnavailable = eventsUnavailable
	trigger := strings.TrimSpace(run.InvocationSource)
	if trigger == "" {
		trigger = run.TriggerType
	}
	pageState.Details = projectsignals.PipelineRunDetailsSignal{
		Trigger:                            trigger,
		TriggeredBy:                        optionalPipelineRunValue(run.PrincipalDisplayName),
		PrincipalID:                        optionalPipelineRunValue(run.PrincipalID),
		ServingStateID:                     run.Identity.GenerationID,
		PlanDigest:                         run.PlanDigest,
		SemanticModelID:                    run.SemanticModelID.String(),
		MaterializationScope:               append([]string{}, run.MaterializationScope...),
		MatchingScheduleIds:                append([]string{}, run.MatchingScheduleIDs...),
		HistoricalPipelineVersionAvailable: historicalGraphAvailable,
		HistoricalPipelineName:             optionalPipelineRunValue(historicalPipelineTitle),
	}
	page := projectui.PipelineRunDetailPageSignal(pageState)
	return pipelineRunDocumentData{Page: page, Project: h.navigationCatalog(r)}, nil
}

func pipelineRunRequestedSection(r *stdhttp.Request) string {
	if r == nil {
		return ""
	}
	if section := strings.TrimSpace(chi.URLParam(r, "section")); section != "" {
		return section
	}
	return r.URL.Query().Get("section")
}

type pipelineRunRouteNormalizedKey struct{}

// pipelineRunCanonicalRouteRequest decodes route params once before the
// general browser authorization helper sees the path-bound asset selector.
// It retains the normal Chi context and records that values are canonical so
// document and SSE projection helpers can share the same request safely.
func pipelineRunCanonicalRouteRequest(r *stdhttp.Request) (*stdhttp.Request, error) {
	if r == nil {
		return nil, errors.New("pipeline run request is unavailable")
	}
	if normalized, _ := r.Context().Value(pipelineRunRouteNormalizedKey{}).(bool); normalized {
		return r, nil
	}
	route := chi.RouteContext(r.Context())
	if route != nil {
		clone := *route
		clone.URLParams.Keys = append([]string(nil), route.URLParams.Keys...)
		clone.URLParams.Values = append([]string(nil), route.URLParams.Values...)
		for index, key := range clone.URLParams.Keys {
			switch key {
			case "asset", "run", "section":
				decoded, err := url.PathUnescape(clone.URLParams.Values[index])
				if err != nil {
					return nil, err
				}
				clone.URLParams.Values[index] = decoded
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, &clone))
	}
	return r.WithContext(context.WithValue(r.Context(), pipelineRunRouteNormalizedKey{}, true)), nil
}

type pipelineRunEventPage struct {
	Rows      []projectsignals.PipelineRunEventSignal
	Truncated bool
}

func (h *BrowserHandler) pipelineRunEvents(ctx context.Context, runID string) (pipelineRunEventPage, error) {
	rows, err := h.RunEventReader.ListEvents(ctx, pipelineRunEventResourceKind, runID, 0, pipelineRunEventPageSize)
	if err != nil {
		return pipelineRunEventPage{}, err
	}
	events := pipelineRunEventPage{Rows: make([]projectsignals.PipelineRunEventSignal, 0, len(rows))}
	for _, row := range rows {
		if row.ResourceKind != pipelineRunEventResourceKind || row.ResourceID != runID {
			continue
		}
		events.Rows = append(events.Rows, projectsignals.PipelineRunEventSignal{
			ID: fmt.Sprint(row.ID), Type: row.EventType, CreatedAt: row.CreatedAt,
		})
	}
	if len(rows) == pipelineRunEventPageSize {
		probe, probeErr := h.RunEventReader.ListEvents(ctx, pipelineRunEventResourceKind, runID, rows[len(rows)-1].ID, 1)
		if probeErr != nil {
			return pipelineRunEventPage{}, probeErr
		}
		events.Truncated = len(probe) > 0
	}
	return events, nil
}

func (h *BrowserHandler) historicalPipelineGraph(ctx context.Context, r *stdhttp.Request, projectID projectgraph.ResourceID, scope refreshrun.ReadScope, run refreshrun.RunRecord, pipelineID projectgraph.ResourceID) (projectsignals.AssetLineageGraphSignal, bool, string, string) {
	if h.HistoricalGraph == nil {
		return projectsignals.AssetLineageGraphSignal{}, false, "", "The historical serving graph for this run is unavailable."
	}
	graph, found, err := h.HistoricalGraph.ServingStateGraph(ctx, scope.ProjectID, scope.Environment, servingstate.ID(run.Identity.GenerationID))
	if err != nil || !found {
		return projectsignals.AssetLineageGraphSignal{}, false, "", "The historical serving graph for this run is unavailable."
	}
	var filtered bool
	graph, filtered = h.authorizedHistoricalGraph(ctx, r, graph)
	if filtered {
		return projectsignals.AssetLineageGraphSignal{}, false, "", "The historical graph includes assets you cannot access, so the complete graph is hidden."
	}
	lineage, found, err := projectui.PipelineRunHistoricalGraph(projectID.String(), pipelineID.String(), run.Identity.GenerationID, graph)
	if err != nil || !found {
		return projectsignals.AssetLineageGraphSignal{}, false, "", "The historical pipeline graph could not be reconstructed from this run's serving state."
	}
	name := ""
	for _, node := range lineage.Nodes {
		if node.ID == pipelineID.String() {
			name = strings.TrimSpace(node.Label)
			break
		}
	}
	return lineage, true, name, ""
}

func (h *BrowserHandler) authorizedHistoricalGraph(ctx context.Context, r *stdhttp.Request, graph servingstate.AssetGraph) (servingstate.AssetGraph, bool) {
	if h == nil || h.CurrentUser == nil || r == nil {
		return servingstate.AssetGraph{}, true
	}
	principal, ok := h.CurrentUser(r)
	if !ok {
		return servingstate.AssetGraph{}, true
	}
	if principal.DevBypass {
		return graph, false
	}
	if h.Catalog == nil {
		return servingstate.AssetGraph{}, true
	}
	visible := make(map[projectgraph.ResourceID]struct{}, len(graph.Assets))
	assets := make([]servingstate.Asset, 0, len(graph.Assets))
	for _, asset := range graph.Assets {
		kind, valid := catalogKindForAssetType(asset.Type)
		if !valid {
			continue
		}
		if _, err := h.Catalog.Resolve(ctx, principal.ID, projectcatalog.Ref{ID: asset.ID, Kind: kind}, access.CapabilityResourceRead, false); err != nil {
			continue
		}
		visible[asset.ID] = struct{}{}
		assets = append(assets, asset)
	}
	edges := make([]servingstate.AssetEdge, 0, len(graph.Edges))
	for _, edge := range graph.Edges {
		if _, ok := visible[edge.FromAssetID]; !ok {
			continue
		}
		if _, ok := visible[edge.ToAssetID]; !ok {
			continue
		}
		edges = append(edges, edge)
	}
	return servingstate.AssetGraph{Assets: assets, Edges: edges}, len(assets) != len(graph.Assets)
}

func validPipelineRunMaterializationScope(scope []string) bool {
	if len(scope) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(scope))
	for _, id := range scope {
		parsed, err := projectgraph.NewResourceID(id)
		if err != nil || parsed.String() != id {
			return false
		}
		if _, exists := seen[id]; exists {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func validPipelineRootRun(run refreshrun.RunRecord, projectID projectgraph.ResourceID, environment string, pipelineID projectgraph.ResourceID) bool {
	if run.ID == "" || run.Identity.Validate() != nil || run.PipelineID.Validate() != nil || run.TargetID.Validate() != nil ||
		run.TargetType != refreshrun.TargetRefreshPipeline || run.ParentRunID != "" || run.PipelineID != pipelineID || run.TargetID != pipelineID ||
		run.Identity.ProjectID != projectID || run.Identity.Environment != environment {
		return false
	}
	switch run.Status {
	case refreshrun.RunStatusQueued, refreshrun.RunStatusRunning, refreshrun.RunStatusPrepared,
		refreshrun.RunStatusSucceeded, refreshrun.RunStatusFailed, refreshrun.RunStatusCancelled,
		refreshrun.RunStatusSuperseded, refreshrun.RunStatusSkipped:
		return true
	default:
		return false
	}
}

func (h *BrowserHandler) pipelineRunAuthorized(r *stdhttp.Request, pipelineID projectgraph.ResourceID) bool {
	if principal, ok := h.currentPrincipal(r); ok && principal.DevBypass {
		return true
	}
	if h.AuthorizePipeline == nil {
		return false
	}
	allowed, err := h.AuthorizePipeline(r, pipelineID.String(), access.CapabilityResourceRead)
	return err == nil && allowed
}

func pipelineRunModelsFrom(modelOrder []string, root refreshrun.RunRecord, children []refreshrun.RunRecord) []projectsignals.PipelineRunModelSignal {
	byModel := pipelineRunChildrenByModel(modelOrder, root, children)
	models := make([]projectsignals.PipelineRunModelSignal, 0, len(modelOrder))
	for _, modelID := range modelOrder {
		model := projectsignals.PipelineRunModelSignal{ModelID: modelID, Attempts: []projectsignals.PipelineRunAttemptSignal{}}
		if child, ok := byModel[modelID]; ok {
			model.Status = projectsignals.Optional(child.Status)
			label := pipelineRunStatusLabel(child.Status)
			if child.Status == refreshrun.RunStatusPrepared {
				label = "Ready to publish"
			}
			model.StatusLabel = projectsignals.Optional(label)
			model.Error = optionalPipelineRunValue(child.Error)
			if duration := pipelineRunDuration(child.StartedAt, child.FinishedAt); duration != "" {
				model.Duration = projectsignals.Optional(duration)
			}
		}
		models = append(models, model)
	}
	return models
}

func pipelineRunChildrenByModel(modelOrder []string, root refreshrun.RunRecord, children []refreshrun.RunRecord) map[string]refreshrun.RunRecord {
	planned := make(map[string]struct{}, len(modelOrder))
	for _, modelID := range modelOrder {
		planned[modelID] = struct{}{}
	}
	byModel := make(map[string]refreshrun.RunRecord, len(children))
	for _, child := range children {
		if child.ParentRunID != root.ID || child.TargetType != refreshrun.TargetModel || child.Identity != root.Identity || child.PipelineID != root.PipelineID {
			continue
		}
		if _, exists := planned[child.TargetID.String()]; !exists {
			continue
		}
		if _, duplicate := byModel[child.TargetID.String()]; duplicate {
			continue
		}
		byModel[child.TargetID.String()] = child
	}
	return byModel
}

func pipelineRunAttemptSignalsFrom(attempts []refreshrun.RunAttemptRecord) []projectsignals.PipelineRunAttemptSignal {
	rows := make([]projectsignals.PipelineRunAttemptSignal, 0, len(attempts))
	for _, attempt := range attempts {
		row := projectsignals.PipelineRunAttemptSignal{
			Number: attempt.Number, Status: attempt.Status, ClaimedAt: attempt.ClaimedAt,
			StartedAt: optionalPipelineRunValue(attempt.StartedAt), FinishedAt: optionalPipelineRunValue(attempt.FinishedAt),
			Error: optionalPipelineRunValue(attempt.Error),
		}
		if duration := pipelineRunDuration(attempt.StartedAt, attempt.FinishedAt); duration != "" {
			row.Duration = projectsignals.Optional(duration)
		}
		rows = append(rows, row)
	}
	return rows
}

func pipelineRunStatusLabel(status string) string {
	switch status {
	case refreshrun.RunStatusQueued:
		return "Queued"
	case refreshrun.RunStatusRunning:
		return "Running"
	case refreshrun.RunStatusPrepared:
		return "Running"
	case refreshrun.RunStatusSucceeded:
		return "Succeeded"
	case refreshrun.RunStatusFailed:
		return "Failed"
	case refreshrun.RunStatusCancelled:
		return "Cancelled"
	case refreshrun.RunStatusSuperseded:
		return "Superseded"
	case refreshrun.RunStatusSkipped:
		return "Skipped"
	default:
		return status
	}
}

func pipelineRunPublicationOutcome(status string, published bool, err error) string {
	if err != nil {
		return "unverified"
	}
	if published {
		return "published"
	}
	switch status {
	case refreshrun.RunStatusQueued, refreshrun.RunStatusRunning, refreshrun.RunStatusPrepared:
		return "pending"
	case refreshrun.RunStatusFailed, refreshrun.RunStatusCancelled,
		refreshrun.RunStatusSuperseded, refreshrun.RunStatusSkipped:
		return "not_published"
	default:
		return "unverified"
	}
}

func pipelineRunDuration(startedAt, finishedAt string) string {
	if startedAt == "" || finishedAt == "" {
		return ""
	}
	started, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return ""
	}
	finished, err := time.Parse(time.RFC3339Nano, finishedAt)
	if err != nil || finished.Before(started) {
		return ""
	}
	return finished.Sub(started).Round(time.Second).String()
}

func optionalPipelineRunValue(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return projectsignals.Optional(value)
}

func shortPipelineRunID(runID string) string {
	if len(runID) > 12 {
		return runID[:12]
	}
	return runID
}

func (h *BrowserHandler) currentPipelineTitle(ctx context.Context, pipelineID projectgraph.ResourceID) string {
	if h != nil && h.ProjectDefinitionReader != nil {
		definition, _, err := h.ProjectDefinitionReader.ProjectDefinitionSnapshot(ctx)
		if err == nil {
			if pipeline, found := definition.RefreshPipelines[pipelineID.String()]; found {
				if name := strings.TrimSpace(pipeline.Name); name != "" {
					return name
				}
			}
		}
	}
	value := strings.TrimSpace(pipelineID.String())
	if _, key, found := strings.Cut(value, ":"); found {
		return key
	}
	return value
}

func writePipelineRunDocumentError(w stdhttp.ResponseWriter, r *stdhttp.Request, err error) {
	status := stdhttp.StatusServiceUnavailable
	var detailErr pipelineRunDocumentError
	if errors.As(err, &detailErr) && detailErr.status != 0 {
		status = detailErr.status
	}
	stdhttp.Error(w, stdhttp.StatusText(status), status)
}
