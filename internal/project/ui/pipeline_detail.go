package ui

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectview "github.com/flidai/leapview/internal/project"
	"github.com/flidai/leapview/internal/project/assetnav"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

const (
	PipelineDetailOverview   = "overview"
	PipelineDetailRuns       = "runs"
	PipelineDetailDefinition = "definition"
)

// PipelineDetailState is the compiler-derived input for the purpose-built
// pipeline detail route. Asset.Payload.Configuration is authored YAML from the
// active serving generation; SourceFile and ContentHash preserve its identity.
type PipelineDetailState struct {
	Project                   projectview.DevelopView
	Asset                     projectview.DevelopAssetView
	Assets                    []projectview.DevelopAssetView
	Edges                     []projectview.DevelopEdgeView
	Refresh                   AssetRefreshState
	RunMonitor                *PipelineRunMonitor
	MonitorRuns               []PipelineMonitorRun
	Environment               string
	ActiveTab                 string
	PublicationRunID          string
	PublicationServingStateID string
	PublicationSnapshotID     int64
	PublicationAt             time.Time
	PublicationUnavailable    bool
	CanRun                    bool
	RunCommand                uicommand.Binding
	CSRFToken                 string
}

type PipelineDetailPageSignal = uisignals.PipelineDetailPageSignal
type PipelineDetailTabSignal = uisignals.PipelineDetailTabSignal
type PipelineDetailLinkSignal = uisignals.PipelineDetailLinkSignal
type PipelineDetailScheduleSignal = uisignals.PipelineDetailScheduleSignal
type PipelineDetailRunSignal = uisignals.PipelineDetailRunSignal
type PipelineDetailPublicationSignal = uisignals.PipelineDetailPublicationSignal

func PipelineDetailPage(nav catalog.Catalog, state PipelineDetailState, roleLabel string, chromeOptions ...webpage.Provider) g.Node {
	page := pipelineDetailPageSignal(state, state.ActiveTab)
	attrs := []g.Node{g.Attr("slot", "page")}
	if state.RunCommand.OperationID() != "" {
		commandValues := url.Values{"surface": {"pipeline_detail"}, "asset": {state.Asset.ID}, "section": {page.ActiveTab}}
		if monitor := page.RunMonitor; monitor != nil {
			commandValues.Set("q", monitor.Query)
			commandValues.Set("range", monitor.Range)
			commandValues.Set("status", monitor.Status)
			commandValues.Set("trigger", monitor.Trigger)
			commandValues.Set("page", strconv.FormatInt(monitor.Page, 10))
		}
		commandURL := "/pipelines/command?" + commandValues.Encode()
		command := "$pipelineCommand = evt.detail; $pipelineCommandStatus = {loading: true, error: '', message: ''}; " + uiactions.CommandPostSwitch("evt.detail.action", map[string]uicommand.Binding{"run": state.RunCommand}, commandURL, "pipelineCommand")
		attrs = append(attrs, g.Attr("data-on:lv-pipeline-command", command))
	}
	extraHead := []g.Node{
		h.Link(h.Rel("stylesheet"), h.Href(projectStaticAssetURL(chromeOptions, "/static/asset-lineage-graph.css"))),
		h.Script(h.Type("module"), h.Src(projectStaticAssetURL(chromeOptions, "/static/asset-lineage-graph.js"))),
	}
	return projectRouteDocument(page.Title, catalogWithoutProjectContext(nav), "pipelines", roleLabel, page,
		uisignals.RouteKind("pipeline_detail"), g.El("lv-pipeline-detail-page", attrs...),
		projectDocumentExtras{CSRFToken: state.CSRFToken}, chromeOptions, extraHead...)
}

func PipelineDetailBootstrapSignals(nav catalog.Catalog, state PipelineDetailState, roleLabel string, chromeOptions ...webpage.Provider) map[string]any {
	page := pipelineDetailPageSignal(state, state.ActiveTab)
	signals := projectRouteBootstrapSignals(catalogWithoutProjectContext(nav), "pipelines", roleLabel, page,
		uisignals.RouteKind("pipeline_detail"), nil, chromeOptions)
	signals["pipelineCommand"] = uisignals.PipelineCommandSignal{}
	signals["pipelineCommandStatus"] = uisignals.PipelineCommandStatusSignal{}
	return signals
}

func pipelineDetailPageSignal(state PipelineDetailState, activeTab string) PipelineDetailPageSignal {
	activeTab = normalizePipelineDetailTab(activeTab)
	asset := state.Asset
	if asset.Title == "" {
		asset.Title = firstNonEmpty(asset.Key, asset.ID)
	}
	assetHref := assetnav.CanonicalAssetSectionHref(asset, PipelineDetailOverview)
	page := PipelineDetailPageSignal{
		Kind:                    "pipeline_detail",
		Title:                   firstNonEmpty(asset.Title, asset.Key, asset.ID),
		Description:             "Inspect this refresh pipeline, its scheduled runs, and its authored definition.",
		Environment:             state.Environment,
		ActiveTab:               activeTab,
		CanRun:                  state.CanRun,
		Asset:                   uisignals.PipelineDetailAssetSignal{ID: asset.ID, Key: asset.Key, Title: asset.Title, Description: uisignals.Optional(asset.Description), Href: assetHref, SourceFile: asset.SourceFile, ContentHash: asset.ContentHash},
		Tabs:                    pipelineDetailTabs(asset, activeTab),
		DashboardConsumers:      []PipelineDetailLinkSignal{},
		DashboardConsumersNote:  "These dashboards are potential consumers of the selected semantic model and are not executed by a pipeline run.",
		Schedules:               pipelineDetailSchedules(asset.Payload),
		Timezone:                metaString(asset.Payload, "Timezone", "timezone"),
		ConcurrencyPolicy:       metaString(asset.Payload, "ConcurrencyPolicy", "concurrencyPolicy"),
		StartingDeadlineSeconds: metaInt64(asset.Payload, "StartingDeadlineSeconds", "startingDeadlineSeconds"),
		DefinitionYaml:          metaString(asset.Payload, "Configuration", "configuration"),
		RecentRuns:              pipelineDetailRuns(asset, state.Refresh),
	}
	if monitor := state.RunMonitor; monitor != nil {
		page.RunMonitor = &uisignals.PipelineRunMonitorSignal{
			Query: monitor.Query, Range: monitor.Range, Pipeline: asset.ID, Status: monitor.Status, Trigger: monitor.Trigger,
			Page: monitor.Page, PageSize: monitor.PageSize, Total: monitor.Total,
		}
		monitorPipeline := PipelineMonitorPipeline{Asset: asset, CanRun: state.CanRun}
		for _, item := range state.MonitorRuns {
			monitorPipeline.Refresh.Runs = append(monitorPipeline.Refresh.Runs, item.Run)
		}
		table := pipelineRunsTable([]PipelineMonitorPipeline{monitorPipeline})
		page.RunsTable = &table
	}
	page.ConcurrencyDescription = pipelineConcurrencyDescription(page.ConcurrencyPolicy, len(page.Schedules) > 0)
	if !state.Refresh.NextRun.IsZero() {
		value := state.Refresh.NextRun.UTC().Format(time.RFC3339)
		page.NextRunAt = &value
	}
	semanticRef := metaString(asset.Payload, "SemanticModel", "semanticModel", "SemanticModelID", "semanticModelId")
	if semanticModel, ok := pipelineDetailFindAsset(state.Assets, string(projectview.AssetTypeSemanticModel), semanticRef); ok {
		page.SemanticModel = &PipelineDetailLinkSignal{ID: semanticModel.ID, Label: firstNonEmpty(semanticModel.Title, semanticModel.Key, semanticModel.ID), Href: assetnav.CanonicalAssetSectionHref(semanticModel, "details"), Type: "Semantic model"}
	}
	page.Graph = pipelineDetailDependencyGraph(state.Project.ID, asset, state.Assets, state.Edges)
	if page.SemanticModel != nil {
		model, ok := pipelineDetailFindAsset(state.Assets, string(projectview.AssetTypeSemanticModel), page.SemanticModel.ID)
		if ok {
			page.DashboardConsumers = pipelineDetailDashboardConsumers(model, state.Assets, state.Edges)
		}
	}
	page.LatestRun = pipelineDetailLatestRun(asset, state.Refresh)
	page.PublicationStatus = "none"
	if state.PublicationRunID != "" && state.PublicationSnapshotID > 0 {
		page.PublicationStatus = "confirmed"
		page.Publication = &PipelineDetailPublicationSignal{
			SnapshotID: state.PublicationSnapshotID, ServingStateID: state.PublicationServingStateID,
			PublishedAt: uisignals.Optional(state.PublicationAt.UTC().Format(time.RFC3339)),
			RunID:       uisignals.Optional(state.PublicationRunID),
		}
	} else if version := state.Refresh.DataVersion; version.SnapshotID > 0 {
		if version.Source == refreshschedule.DataVersionSourceRefresh && version.PipelineID == asset.ID && strings.TrimSpace(version.RunID) != "" {
			page.PublicationStatus = "confirmed"
			publication := &PipelineDetailPublicationSignal{SnapshotID: version.SnapshotID, ServingStateID: version.ServingStateID, RunID: uisignals.Optional(version.RunID)}
			if !version.RefreshedAt.IsZero() {
				publication.PublishedAt = uisignals.Optional(version.RefreshedAt.UTC().Format(time.RFC3339))
			}
			page.Publication = publication
		} else {
			// An active snapshot exists, but the available provenance does not
			// establish that this pipeline/run published it. Do not leak or
			// attribute another pipeline's identifiers here.
			page.PublicationStatus = "unavailable"
		}
	} else if state.PublicationUnavailable {
		page.PublicationStatus = "unavailable"
	}
	return page
}

func normalizePipelineDetailTab(tab string) string {
	switch strings.ToLower(strings.TrimSpace(tab)) {
	case PipelineDetailRuns:
		return PipelineDetailRuns
	case PipelineDetailDefinition:
		return PipelineDetailDefinition
	default:
		return PipelineDetailOverview
	}
}

func pipelineDetailTabs(asset projectview.DevelopAssetView, activeTab string) []PipelineDetailTabSignal {
	tabs := []PipelineDetailTabSignal{}
	for _, tab := range []struct{ id, label string }{{PipelineDetailOverview, "Overview"}, {PipelineDetailRuns, "Runs"}, {PipelineDetailDefinition, "Definition"}} {
		tabs = append(tabs, PipelineDetailTabSignal{ID: tab.id, Label: tab.label, Href: assetnav.CanonicalAssetSectionHref(asset, tab.id), Active: tab.id == activeTab})
	}
	return tabs
}

func pipelineDetailSchedules(payload map[string]any) []PipelineDetailScheduleSignal {
	schedules := metaSlice(payload, "Schedules", "schedules")
	out := make([]PipelineDetailScheduleSignal, 0, len(schedules))
	for _, value := range schedules {
		entry, _ := value.(map[string]any)
		if cron := strings.TrimSpace(metaString(entry, "Cron", "cron")); cron != "" {
			out = append(out, PipelineDetailScheduleSignal{Cron: cron})
		}
	}
	return out
}

func pipelineConcurrencyDescription(policy string, scheduled bool) string {
	if !scheduled {
		return "Manual only; scheduled overlap policy does not apply."
	}
	switch strings.TrimSpace(policy) {
	case refreshschedule.ConcurrencyForbid:
		return "Forbid: a scheduled occurrence is skipped while an earlier run is active."
	case refreshschedule.ConcurrencyReplace:
		return "Replace: a new scheduled run supersedes the active scheduled run."
	default:
		return "No overlap policy is available."
	}
}

func pipelineDetailDependencyGraph(projectID string, pipeline projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) uisignals.AssetLineageGraphSignal {
	semanticRef := metaString(pipeline.Payload, "SemanticModel", "semanticModel", "SemanticModelID", "semanticModelId")
	semantic, found := pipelineDetailFindAsset(assets, string(projectview.AssetTypeSemanticModel), semanticRef)
	if !found {
		// Historical serving graphs can omit the authored payload while still
		// retaining compiled relationships. Resolve the selected model from the
		// pipeline's direct dependency edge in that exact graph.
		for _, edge := range edges {
			candidateID := ""
			switch pipeline.ID {
			case edge.FromAssetID:
				candidateID = edge.ToAssetID
			case edge.ToAssetID:
				candidateID = edge.FromAssetID
			}
			if candidate, ok := projectview.AssetByID(assets, candidateID); ok && candidate.Type == string(projectview.AssetTypeSemanticModel) {
				semantic = candidate
				found = true
				break
			}
		}
	}
	if !found {
		return uisignals.AssetLineageGraphSignal{Nodes: []uisignals.AssetLineageNodeSignal{}, Edges: []uisignals.AssetLineageEdgeSignal{}}
	}
	graph := assetLineage(projectID, semantic, assets, edges).Graph
	visibleIDs := map[string]struct{}{}
	for _, node := range graph.Nodes {
		// Keep only the selected semantic model and its upstream data assets.
		// Pipeline orchestration is page context, and downstream consumers are
		// not part of this pipeline's data flow.
		if !uisignals.ValueOrZero(node.Selected) && node.Rank >= 0 {
			continue
		}
		visibleIDs[node.ID] = struct{}{}
	}
	filtered := uisignals.AssetLineageGraphSignal{Nodes: make([]uisignals.AssetLineageNodeSignal, 0, len(graph.Nodes)), Edges: make([]uisignals.AssetLineageEdgeSignal, 0, len(graph.Edges))}
	for _, node := range graph.Nodes {
		if _, ok := visibleIDs[node.ID]; ok {
			filtered.Nodes = append(filtered.Nodes, node)
		}
	}
	for _, edge := range graph.Edges {
		if _, sourceOK := visibleIDs[edge.Source]; !sourceOK {
			continue
		}
		if _, targetOK := visibleIDs[edge.Target]; !targetOK {
			continue
		}
		filtered.Edges = append(filtered.Edges, edge)
	}
	return filtered
}

func pipelineDetailDashboardConsumers(model projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) []PipelineDetailLinkSignal {
	links := semanticModelDashboardLinks(model, assets, edges)
	out := make([]PipelineDetailLinkSignal, 0, len(links))
	for _, link := range links {
		for _, candidate := range assets {
			if candidate.Type != string(projectview.AssetTypeDashboard) || assetnav.CanonicalAssetSectionHref(candidate, "details") != link.Href {
				continue
			}
			out = append(out, PipelineDetailLinkSignal{ID: candidate.ID, Label: link.Label, Href: link.Href, Type: "Dashboard"})
			break
		}
	}
	return out
}

func pipelineDetailFindAsset(assets []projectview.DevelopAssetView, typ, ref string) (projectview.DevelopAssetView, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return projectview.DevelopAssetView{}, false
	}
	for _, candidate := range assets {
		if candidate.Type == typ && assetReferenceMatches(candidate, ref) {
			return candidate, true
		}
	}
	return projectview.DevelopAssetView{}, false
}

func pipelineDetailRuns(asset projectview.DevelopAssetView, refresh AssetRefreshState) []PipelineDetailRunSignal {
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
		left := firstNonEmpty(runs[i].StartedAt, runs[i].CreatedAt)
		right := firstNonEmpty(runs[j].StartedAt, runs[j].CreatedAt)
		return left > right
	})
	out := make([]PipelineDetailRunSignal, 0, len(runs))
	for _, run := range runs {
		out = append(out, pipelineDetailRun(asset, run))
	}
	return out
}

func pipelineDetailLatestRun(asset projectview.DevelopAssetView, refresh AssetRefreshState) *PipelineDetailRunSignal {
	if refresh.Latest.ID != "" {
		latest := pipelineDetailRun(asset, refresh.Latest)
		return &latest
	}
	if len(refresh.Runs) == 0 {
		return nil
	}
	runs := append([]AssetRefreshRun(nil), refresh.Runs...)
	sort.SliceStable(runs, func(i, j int) bool {
		return firstNonEmpty(runs[i].StartedAt, runs[i].CreatedAt) > firstNonEmpty(runs[j].StartedAt, runs[j].CreatedAt)
	})
	latest := pipelineDetailRun(asset, runs[0])
	return &latest
}

func pipelineDetailRun(asset projectview.DevelopAssetView, run AssetRefreshRun) PipelineDetailRunSignal {
	return PipelineDetailRunSignal{
		ID: run.ID, Status: strings.ToLower(strings.TrimSpace(run.Status)), StartedAt: uisignals.Optional(run.StartedAt),
		FinishedAt: uisignals.Optional(run.FinishedAt), Duration: uisignals.Optional(refreshRunDuration(run)), Trigger: uisignals.Optional(refreshTriggerLabel(run.TriggerType)),
		Error: uisignals.Optional(run.Error), Href: assetnav.CanonicalAssetSectionHref(asset, PipelineDetailRuns) + "/" + url.PathEscape(run.ID),
	}
}
