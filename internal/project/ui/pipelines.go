package ui

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectview "github.com/flidai/leapview/internal/project"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	g "maragu.dev/gomponents"
)

type PipelineMonitorPipeline struct {
	Asset              projectview.DevelopAssetView
	Refresh            AssetRefreshState
	CanRun             bool
	CanCancel          bool
	SemanticModelTitle string
	PublicationAt      time.Time
	PublicationStatus  string
}

type PipelineMonitorState struct {
	Environment    string
	CSRFToken      string
	Pipelines      []PipelineMonitorPipeline
	WaitingIntents []PipelineWaitingIntent
	RunCommand     uicommand.Binding
	CancelCommand  uicommand.Binding
	RunMonitor     *PipelineRunMonitor
}

// PipelineWaitingIntent is a committed manual request that has not yet become
// an immutable execution run. RunID is set only after the request attaches.
type PipelineWaitingIntent struct {
	IntentID      string
	PipelineID    string
	Status        string
	CreatedAt     string
	Reason        string
	QueuePosition int64
	RunID         string
	CancelAllowed bool
}

type PipelineRunMonitor struct {
	Query, Range, Pipeline, Status, Trigger string
	Page                                    int64
	PageSize                                int32
	Total                                   int64
	Runs                                    []PipelineMonitorRun
}

type PipelineMonitorRun struct {
	PipelineID string
	Run        AssetRefreshRun
}

func PipelinesPage(nav catalog.Catalog, state PipelineMonitorState, activeTab, roleLabel string, chromeOptions ...webpage.Provider) g.Node {
	page := pipelineMonitorPageSignal(state, activeTab)
	active := "pipelines"
	attrs := []g.Node{g.Attr("slot", "page")}
	if state.RunCommand.OperationID() != "" && state.CancelCommand.OperationID() != "" {
		commandURL := "/pipelines/command"
		if page.ActiveTab == "runs" && page.RunMonitor != nil {
			values := url.Values{"view": {"runs"}, "q": {page.RunMonitor.Query}, "range": {page.RunMonitor.Range}, "pipeline": {page.RunMonitor.Pipeline},
				"status": {page.RunMonitor.Status}, "trigger": {page.RunMonitor.Trigger}, "page": {fmt.Sprint(page.RunMonitor.Page)}}
			commandURL += "?" + values.Encode()
		}
		command := "$pipelineCommand = evt.detail; $pipelineCommandStatus = {loading: true, error: '', message: ''}; " + uiactions.CommandPostSwitch("evt.detail.action", map[string]uicommand.Binding{
			"run": state.RunCommand, "cancel": state.CancelCommand, "cancel-intent": state.CancelCommand,
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
	signals := projectRouteBootstrapSignals(catalogWithoutProjectContext(nav), "pipelines", roleLabel, page, uisignals.RouteKindPipelines, nil, chromeOptions)
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
			switch {
			case pipeline.Refresh.Latest.ID != "":
				status = "unknown"
			case len(pipeline.Refresh.Runs) > 0:
				status = firstNonEmpty(strings.ToLower(strings.TrimSpace(pipeline.Refresh.Runs[0].Status)), "unknown")
			case pipeline.Refresh.LatestSuccessful.ID != "":
				status = firstNonEmpty(strings.ToLower(strings.TrimSpace(pipeline.Refresh.LatestSuccessful.Status)), "succeeded")
			default:
				status = "never run"
			}
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
			ID:                pipeline.Asset.ID,
			Title:             firstNonEmpty(pipeline.Asset.Title, pipeline.Asset.Key, pipeline.Asset.ID),
			Href:              assetHref,
			SemanticModel:     pipelineSemanticModelDisplayName(firstNonEmpty(pipeline.SemanticModelTitle, metaString(pipeline.Asset.Payload, "SemanticModel", "semanticModel"))),
			Schedule:          pipelineScheduleLabel(pipeline.Asset.Payload),
			PipelineID:        pipeline.Asset.ID,
			Status:            status,
			PublicationStatus: firstNonEmpty(pipeline.PublicationStatus, "none"),
			RecentRuns:        pipelineRecentRuns(pipeline.Asset.ID, pipeline.Refresh),
		}
		if !pipeline.PublicationAt.IsZero() {
			item.LastPublishedAt = uisignals.Optional(pipeline.PublicationAt.UTC().Format(time.RFC3339))
			item.PublicationStatus = "confirmed"
		} else if version := pipeline.Refresh.DataVersion; version.Source == refreshschedule.DataVersionSourceRefresh && version.PipelineID == pipeline.Asset.ID && version.RunID != "" && !version.RefreshedAt.IsZero() {
			item.LastPublishedAt = uisignals.Optional(version.RefreshedAt.UTC().Format(time.RFC3339))
			item.PublicationStatus = "confirmed"
		}
		if !pipeline.Refresh.NextRun.IsZero() {
			item.NextRun = uisignals.Optional(pipeline.Refresh.NextRun.UTC().Format(time.RFC3339))
		}
		items = append(items, item)
	}

	runsTable := pipelineRunsTable(pipelines)
	page := uisignals.PipelinePageSignal{
		Kind:           uisignals.RouteKindPipelines,
		Title:          "Pipelines",
		Description:    "Browse refresh pipelines and their schedules.",
		Environment:    state.Environment,
		ActiveTab:      activeTab,
		Pipelines:      items,
		WaitingIntents: pipelineWaitingIntentSignals(state.WaitingIntents, runsTable.Rows),
		RunsTable:      runsTable,
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
			page.WaitingIntents = pipelineWaitingIntentSignals(state.WaitingIntents, page.RunsTable.Rows)
			page.RunMonitor = &uisignals.PipelineRunMonitorSignal{Query: monitor.Query, Range: monitor.Range, Pipeline: monitor.Pipeline, Status: monitor.Status, Trigger: monitor.Trigger,
				Page: monitor.Page, PageSize: monitor.PageSize, Total: monitor.Total}
		}
	}
	return page
}

func pipelineWaitingIntentSignals(intents []PipelineWaitingIntent, rows []map[string]any) []uisignals.PipelineWaitingIntentSignal {
	visibleRuns := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		pipelineID, _ := row["pipeline_id"].(string)
		runID, _ := row["run_id"].(string)
		pipelineID = strings.TrimSpace(pipelineID)
		runID = strings.TrimSpace(runID)
		if pipelineID != "" && runID != "" {
			visibleRuns[pipelineID+"\x00"+runID] = struct{}{}
		}
	}

	result := make([]uisignals.PipelineWaitingIntentSignal, 0, len(intents))
	for _, intent := range intents {
		intentID := strings.TrimSpace(intent.IntentID)
		pipelineID := strings.TrimSpace(intent.PipelineID)
		if intentID == "" || pipelineID == "" {
			continue
		}
		runID := strings.TrimSpace(intent.RunID)
		status := strings.ToLower(strings.TrimSpace(intent.Status))
		if status != "waiting" && status != "claimed" && status != "attached" && status != "stale" {
			continue
		}
		if status == "attached" && runID != "" {
			if _, found := visibleRuns[pipelineID+"\x00"+runID]; found {
				continue
			}
		}
		signal := uisignals.PipelineWaitingIntentSignal{
			IntentID: intentID, PipelineID: pipelineID, Status: status, CreatedAt: strings.TrimSpace(intent.CreatedAt), Reason: uisignals.Optional(strings.TrimSpace(intent.Reason)),
			RunID: uisignals.Optional(runID), CancelAllowed: intent.CancelAllowed,
		}
		if status == "stale" && signal.Reason == nil {
			signal.Reason = uisignals.Optional("Pipeline definition changed while waiting; start a new request")
		}
		if intent.QueuePosition > 0 {
			signal.QueuePosition = uisignals.Optional(intent.QueuePosition)
		}
		result = append(result, signal)
	}
	sort.SliceStable(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.QueuePosition != nil && right.QueuePosition != nil && *left.QueuePosition != *right.QueuePosition {
			return *left.QueuePosition < *right.QueuePosition
		}
		if left.QueuePosition != nil && right.QueuePosition == nil {
			return true
		}
		if left.QueuePosition == nil && right.QueuePosition != nil {
			return false
		}
		if left.CreatedAt != right.CreatedAt {
			return left.CreatedAt < right.CreatedAt
		}
		return left.IntentID < right.IntentID
	})
	return result
}

func pipelineRecentRuns(pipelineID string, refresh AssetRefreshState) []uisignals.PipelineDetailRunSignal {
	runs := append([]AssetRefreshRun(nil), refresh.Runs...)
	if refresh.Latest.ID != "" {
		found := false
		for _, run := range runs {
			if run.ID == refresh.Latest.ID {
				found = true
				break
			}
		}
		if !found {
			runs = append(runs, refresh.Latest)
		}
	}
	sort.SliceStable(runs, func(i, j int) bool {
		return firstNonEmpty(runs[i].StartedAt, runs[i].CreatedAt) > firstNonEmpty(runs[j].StartedAt, runs[j].CreatedAt)
	})
	recent := make([]uisignals.PipelineDetailRunSignal, 0, 5)
	for _, run := range runs {
		if run.ID == "" || run.ParentRunID != "" {
			continue
		}
		recent = append(recent, uisignals.PipelineDetailRunSignal{
			ID: run.ID, Status: firstNonEmpty(strings.ToLower(strings.TrimSpace(run.Status)), "unknown"), Href: pipelineRunHref(pipelineID, run.ID),
			StartedAt: uisignals.Optional(firstNonEmpty(run.StartedAt, run.CreatedAt)),
		})
		if len(recent) == 5 {
			break
		}
	}
	return recent
}

func pipelineSemanticModelDisplayName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "—" {
		return "—"
	}
	lower := strings.ToLower(value)
	for _, prefix := range []string{"semantic-model:", "semantic_model:", "semantic:", "semantic-model/", "semantic_model/", "semantic/"} {
		if strings.HasPrefix(lower, prefix) {
			value = value[len(prefix):]
			break
		}
	}
	if strings.Contains(strings.ToLower(value), "semantic model") {
		return value
	}
	words := strings.FieldsFunc(value, func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || r == '/' || r == ':'
	})
	for index, word := range words {
		if word != "" {
			first, size := utf8.DecodeRuneInString(word)
			words[index] = strings.ToUpper(string(first)) + word[size:]
		}
	}
	if len(words) == 0 {
		return "—"
	}
	return strings.Join(words, " ") + " Semantic Model"
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
			href = "/pipelines/" + url.PathEscape(pipeline.Asset.ID) + "/details"
		}
		for _, run := range pipeline.Refresh.Runs {
			if strings.TrimSpace(run.ParentRunID) != "" {
				continue
			}
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
				"run_href":               pipelineRunHref(pipeline.Asset.ID, run.ID),
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

func pipelineRunHref(pipelineID, runID string) string {
	return "/pipelines/" + url.PathEscape(pipelineID) + "/runs/" + url.PathEscape(runID)
}
