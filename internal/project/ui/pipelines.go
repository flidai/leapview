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

// PipelineMonitorCapacity describes the node-wide refresh workload pool. The
// page is global because refresh pipelines contend for this shared capacity.
type PipelineMonitorCapacity struct {
	Running        int
	Queued         int
	MaximumRunning int
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
}

func PipelinesPage(nav catalog.Catalog, state PipelineMonitorState, activeTab, roleLabel string, chromeOptions ...webpage.Provider) g.Node {
	page := pipelineMonitorPageSignal(state, activeTab)
	attrs := []g.Node{g.Attr("slot", "page")}
	if state.RunCommand.OperationID() != "" && state.CancelCommand.OperationID() != "" {
		command := "$pipelineCommand = evt.detail; $pipelineCommandStatus = {loading: true, error: '', message: ''}; " + uiactions.CommandPostSwitch("evt.detail.action", map[string]uicommand.Binding{
			"run": state.RunCommand, "cancel": state.CancelCommand,
		}, "/pipelines/command", "pipelineCommand")
		attrs = append(attrs, g.Attr("data-on:lv-pipeline-command", command))
	}
	return projectRouteDocument("Pipelines", catalogWithoutProjectContext(nav), "pipelines", roleLabel, page, uisignals.RouteKindPipelines,
		g.El("lv-pipelines-page", attrs...),
		projectDocumentExtras{CSRFToken: state.CSRFToken}, chromeOptions,
	)
}

func PipelinesBootstrapSignals(nav catalog.Catalog, state PipelineMonitorState, activeTab, roleLabel string, chromeOptions ...webpage.Provider) map[string]any {
	signals := projectRouteBootstrapSignals(catalogWithoutProjectContext(nav), "pipelines", roleLabel, pipelineMonitorPageSignal(state, activeTab), uisignals.RouteKindPipelines, nil, chromeOptions)
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
	failed := 0
	for _, pipeline := range pipelines {
		status := strings.ToLower(strings.TrimSpace(pipeline.Refresh.Latest.Status))
		if status == "" {
			status = "not refreshed"
		}
		if status == "failed" {
			failed++
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
			Running:       status == "queued" || status == "running",
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
	if capacity.MaximumRunning < 0 {
		capacity.MaximumRunning = 0
	}
	return uisignals.PipelinePageSignal{
		Kind:        uisignals.RouteKindPipelines,
		Title:       "Pipelines",
		Description: "Monitor refresh pipelines and their shared node-wide execution capacity.",
		Environment: state.Environment,
		ActiveTab:   activeTab,
		Pipelines:   items,
		RunsTable:   pipelineRunsTable(pipelines),
		Metrics: []uisignals.PipelineMetricSignal{
			{Label: "Running", Value: fmt.Sprint(capacity.Running), Detail: uisignals.Pointer("Refresh jobs executing now"), Tone: uisignals.Pointer("accent")},
			{Label: "Queued", Value: fmt.Sprint(capacity.Queued), Detail: uisignals.Pointer("Waiting for shared capacity"), Tone: uisignals.Pointer("attention")},
			{Label: "Failed", Value: fmt.Sprint(failed), Detail: uisignals.Pointer("Latest pipeline state"), Tone: uisignals.Pointer(metricFailureTone(failed))},
			{Label: "Refresh capacity", Value: fmt.Sprintf("%d / %d", capacity.Running, capacity.MaximumRunning), Detail: uisignals.Pointer("Running / node maximum"), Tone: uisignals.Pointer("muted")},
		},
	}
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
			started, _ := parseRefreshTime(run.StartedAt)
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
