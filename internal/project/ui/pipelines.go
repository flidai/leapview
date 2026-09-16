package ui

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectview "github.com/flidai/leapview/internal/project"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	g "maragu.dev/gomponents"
)

// PipelineMonitorCapacity counts the active pipeline runs represented by the
// monitor read model. It is not a node-wide admission limit.
type PipelineMonitorCapacity struct {
	Running  int
	Queued   int
	Prepared int
}

type PipelineMonitorPipeline struct {
	Asset     projectview.DevelopAssetView
	Refresh   AssetRefreshState
	CanRun    bool
	CanCancel bool
}

type PipelineMonitorState struct {
	Environment   string
	CSRFToken     string
	Capacity      PipelineMonitorCapacity
	Pipelines     []PipelineMonitorPipeline
	RunCommand    uicommand.Binding
	CancelCommand uicommand.Binding
	RunMonitor    *PipelineRunMonitor
}

type PipelineRunMonitor struct {
	Query, Range, Status, Trigger    string
	Page                             int64
	PageSize                         int32
	Total, Failed, Completed, Active int64
	Runs                             []PipelineMonitorRun
}

type PipelineMonitorRun struct {
	PipelineID string
	Run        AssetRefreshRun
}

func PipelinesPage(nav catalog.Catalog, state PipelineMonitorState, activeTab, roleLabel string, chromeOptions ...webpage.Provider) g.Node {
	page := pipelineMonitorPageSignal(state, activeTab)
	active := page.ActiveTab
	attrs := []g.Node{g.Attr("slot", "page")}
	if state.RunCommand.OperationID() != "" && state.CancelCommand.OperationID() != "" {
		commandURL := "/pipelines/command"
		if page.ActiveTab == "runs" && page.RunMonitor != nil {
			values := url.Values{"view": {"runs"}, "q": {page.RunMonitor.Query}, "range": {page.RunMonitor.Range},
				"status": {page.RunMonitor.Status}, "trigger": {page.RunMonitor.Trigger}, "page": {fmt.Sprint(page.RunMonitor.Page)}}
			commandURL += "?" + values.Encode()
		}
		command := "$pipelineCommand = evt.detail; $pipelineCommandStatus = {loading: true, error: '', message: ''}; " + uiactions.CommandPostSwitch("evt.detail.action", map[string]uicommand.Binding{
			"run": state.RunCommand, "cancel": state.CancelCommand,
		}, commandURL, "pipelineCommand")
		attrs = append(attrs, g.Attr("data-on:lv-pipeline-command", command))
	}
	return projectRouteDocument(page.Title, catalogWithoutProjectContext(nav), active, roleLabel, page, uisignals.RouteKindPipelines,
		g.El("lv-pipelines-page", attrs...),
		projectDocumentExtras{CSRFToken: state.CSRFToken}, chromeOptions,
	)
}

func PipelinesBootstrapSignals(nav catalog.Catalog, state PipelineMonitorState, activeTab, roleLabel string, chromeOptions ...webpage.Provider) map[string]any {
	page := pipelineMonitorPageSignal(state, activeTab)
	signals := projectRouteBootstrapSignals(catalogWithoutProjectContext(nav), page.ActiveTab, roleLabel, page, uisignals.RouteKindPipelines, nil, chromeOptions)
	signals["pipelineCommand"] = uisignals.PipelineCommandSignal{}
	signals["pipelineCommandStatus"] = uisignals.PipelineCommandStatusSignal{}
	return signals
}

func PipelinesPagePatch(state PipelineMonitorState, activeTab string) map[string]any {
	return map[string]any{"page": pipelineMonitorPageSignal(state, activeTab)}
}

func pipelineMonitorPageSignal(state PipelineMonitorState, activeTab string) uisignals.PipelinePageSignal {
	activeTab = strings.ToLower(strings.TrimSpace(activeTab))
	if activeTab != "runs" {
		activeTab = "pipelines"
	}
	pipelines := append([]PipelineMonitorPipeline(nil), state.Pipelines...)
	sort.SliceStable(pipelines, func(i, j int) bool {
		left := strings.ToLower(pipelines[i].Asset.Title)
		right := strings.ToLower(pipelines[j].Asset.Title)
		return left < right
	})

	items := make([]uisignals.PipelineListItemSignal, 0, len(pipelines))
	for _, pipeline := range pipelines {
		status := strings.ToLower(strings.TrimSpace(pipeline.Refresh.Latest.Status))
		if status == "" {
			status = "not refreshed"
		}
		assetHref := strings.TrimSpace(pipeline.Asset.Href)
		if assetHref == "" {
			assetHref = "/pipelines/" + url.PathEscape(pipeline.Asset.ID) + "/details"
		}
		item := uisignals.PipelineListItemSignal{
			AssetID: pipeline.Asset.ID,
			CanRun:  pipeline.CanRun,
			// Resource IDs are the only stable identity that command handlers
			// and authorization understand. Keep the symbolic key for labels and
			// search only; never use it as a row or command identity.
			ID:            pipeline.Asset.ID,
			Title:         firstNonEmpty(pipeline.Asset.Title, pipeline.Asset.Key, pipeline.Asset.ID),
			Description:   uisignals.Optional(pipeline.Asset.Description),
			Href:          assetHref,
			SemanticModel: emptyDash(metaString(pipeline.Asset.Payload, "SemanticModel", "semanticModel")),
			Schedule:      pipelineScheduleLabel(pipeline.Asset.Payload),
			PipelineID:    pipeline.Asset.ID,
			Running:       status == "queued" || status == "running" || status == "prepared",
			Status:        status,
			Duration:      uisignals.Optional(refreshRunDuration(pipeline.Refresh.Latest)),
			LastSuccessful: uisignals.Optional(
				pipeline.Refresh.LatestSuccessful.FinishedAt,
			),
		}
		if !pipeline.Refresh.NextRun.IsZero() {
			item.NextRun = uisignals.Optional(pipeline.Refresh.NextRun.UTC().Format(time.RFC3339))
		}
		items = append(items, item)
	}

	capacity := state.Capacity
	page := uisignals.PipelinePageSignal{
		Kind:        uisignals.RouteKindPipelines,
		Title:       "Pipelines",
		Description: "Browse refresh pipelines and their schedules.",
		Environment: state.Environment,
		ActiveTab:   activeTab,
		Pipelines:   items,
		RunsTable:   pipelineRunsTable(pipelines),
	}
	if activeTab == "runs" {
		page.Title = "Runs"
		page.Description = "Monitor pipeline execution across the environment."
		if monitor := state.RunMonitor; monitor != nil {
			byID := make(map[string]PipelineMonitorPipeline, len(pipelines))
			for _, pipeline := range pipelines {
				byID[pipeline.Asset.ID] = pipeline
			}
			monitorRows := make([]PipelineMonitorPipeline, 0, len(monitor.Runs))
			for _, item := range monitor.Runs {
				pipeline, found := byID[item.PipelineID]
				if !found {
					continue
				}
				pipeline.Refresh.Runs = []AssetRefreshRun{item.Run}
				monitorRows = append(monitorRows, pipeline)
			}
			page.RunsTable = pipelineRunsTable(monitorRows)
			page.RunMonitor = &uisignals.PipelineRunMonitorSignal{Query: monitor.Query, Range: monitor.Range, Status: monitor.Status, Trigger: monitor.Trigger,
				Page: monitor.Page, PageSize: monitor.PageSize, Total: monitor.Total}
			page.Metrics = []uisignals.PipelineMetricSignal{
				{Label: "Active now", Value: fmt.Sprint(monitor.Active), Detail: uisignals.Pointer("All time · current state"), Tone: uisignals.Pointer("accent")},
				{Label: "Failed in range", Value: fmt.Sprint(monitor.Failed), Detail: uisignals.Pointer("Selected time range"), Tone: uisignals.Pointer(metricFailureTone(int(monitor.Failed)))},
				{Label: "Completed in range", Value: fmt.Sprint(monitor.Completed), Detail: uisignals.Pointer("Succeeded runs"), Tone: uisignals.Pointer("success")},
			}
		} else {
			page.Metrics = []uisignals.PipelineMetricSignal{
				{Label: "Running", Value: fmt.Sprint(capacity.Running), Detail: uisignals.Pointer("Refresh jobs executing now"), Tone: uisignals.Pointer("accent")},
				{Label: "Queued", Value: fmt.Sprint(capacity.Queued), Detail: uisignals.Pointer("Waiting for shared capacity"), Tone: uisignals.Pointer("attention")},
				{Label: "Prepared", Value: fmt.Sprint(capacity.Prepared), Detail: uisignals.Pointer("Preparing a result for publication"), Tone: uisignals.Pointer("attention")},
			}
		}
	}
	return page
}

func pipelineScheduleLabel(payload map[string]any) string {
	schedules := metaSlice(payload, "Schedules", "schedules")
	if len(schedules) == 0 {
		return "Manual only"
	}
	entry, _ := schedules[0].(map[string]any)
	cron := metaString(entry, "Cron", "cron")
	timezone := metaString(entry, "Timezone", "timezone")
	label := strings.TrimSpace(strings.Join([]string{cron, timezone}, " · "))
	label = strings.Trim(label, " ·")
	if len(schedules) > 1 {
		label += fmt.Sprintf(" +%d", len(schedules)-1)
	}
	return firstNonEmpty(label, "Scheduled")
}

func pipelineRunsTable(pipelines []PipelineMonitorPipeline) recordTable {
	type runRow struct {
		started time.Time
		row     map[string]any
	}
	all := make([]runRow, 0)
	seen := map[string]struct{}{}
	for _, pipeline := range pipelines {
		href := strings.TrimSpace(pipeline.Asset.Href)
		if href == "" {
			href = "/pipelines/" + url.PathEscape(pipeline.Asset.ID) + "/refreshes"
		}
		for _, run := range pipeline.Refresh.Runs {
			key := pipeline.Asset.ID + "\x00" + run.ID
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			started, _ := parseRefreshTime(run.CreatedAt)
			status := strings.ToLower(strings.TrimSpace(run.Status))
			actions := []map[string]any{{"label": "View run details", "action": "detail", "icon": "details"}}
			if status == "queued" && pipeline.CanCancel {
				actions = append(actions, map[string]any{"label": "Cancel run", "action": "cancel", "icon": "cancel"})
			} else if status != "" && status != "queued" && status != "running" && pipeline.CanRun {
				actions = append(actions, map[string]any{"label": "Run again", "action": "run", "icon": "refresh"})
			}
			semanticModel := firstNonEmpty(run.ModelID, metaString(pipeline.Asset.Payload, "SemanticModel", "semanticModel"))
			all = append(all, runRow{started: started, row: map[string]any{
				"id":                     run.ID,
				"status":                 refreshStatusGridValue(run.Status),
				"pipeline":               firstNonEmpty(pipeline.Asset.Title, pipeline.Asset.Key, pipeline.Asset.ID),
				"pipeline_href":          href,
				"started":                emptyDash(run.StartedAt),
				"duration":               emptyDash(refreshRunDuration(run)),
				"trigger":                refreshTriggerLabel(run.TriggerType),
				"triggered_by":           emptyDash(run.PrincipalDisplayName),
				"run":                    emptyDash(shortRefreshRunID(run.ID)),
				"error":                  emptyDash(run.Error),
				"actions":                actions,
				"environment":            emptyDash(run.Environment),
				"semantic_model":         emptyDash(semanticModel),
				"principal_id":           emptyDash(run.PrincipalID),
				"principal_display_name": emptyDash(run.PrincipalDisplayName),
				"created_at":             emptyDash(run.CreatedAt),
				"updated_at":             emptyDash(run.UpdatedAt),
				"started_at":             emptyDash(run.StartedAt),
				"finished_at":            emptyDash(run.FinishedAt),
				"parent_run_id":          emptyDash(run.ParentRunID),
				"serving_state_id":       emptyDash(run.ServingStateID),
				"target_generation":      run.TargetGeneration,
				"asset_id":               pipeline.Asset.ID,
				"pipeline_id":            pipeline.Asset.ID,
				"run_id":                 run.ID,
				"status_value":           strings.ToLower(strings.TrimSpace(run.Status)),
				"trigger_value":          strings.ToLower(strings.TrimSpace(run.TriggerType)),
				"pipeline_search": strings.ToLower(strings.Join([]string{
					pipeline.Asset.Title, pipeline.Asset.Key, pipeline.Asset.ID, run.ID,
				}, " ")),
			}})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].started.After(all[j].started) })
	rows := make([]map[string]any, 0, len(all))
	for _, item := range all {
		rows = append(rows, item.row)
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "status", Header: "Status", Kind: uisignals.Pointer("status"), Width: uisignals.Pointer("120px")},
			{ID: "pipeline", Header: "Pipeline", Kind: uisignals.Pointer("link"), HrefKey: uisignals.Pointer("pipeline_href"), Width: uisignals.Pointer("200px")},
			{ID: "started", Header: "Started", Width: uisignals.Pointer("170px")},
			{ID: "duration", Header: "Duration", Width: uisignals.Pointer("90px")},
			{ID: "trigger", Header: "Trigger", Width: uisignals.Pointer("100px")},
			{ID: "triggered_by", Header: "Triggered by", Width: uisignals.Pointer("140px")},
			{ID: "actions", Header: "", Kind: uisignals.Pointer("actions"), Toggleable: uisignals.Pointer(false), Width: uisignals.Pointer("80px")},
		},
		Rows: rows, Empty: "No pipeline runs have been recorded yet.", MinWidth: uisignals.Pointer("1050px"), RowAction: uisignals.Pointer("detail"),
	}
}

func metricFailureTone(failed int) string {
	if failed > 0 {
		return "danger"
	}
	return "success"
}
