package ui

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	projectview "github.com/flidai/leapview/internal/project"
	"github.com/flidai/leapview/internal/project/assetnav"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func assetOverviewSignal(projectID string, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, refresh AssetRefreshState, versions AssetVersionsState) uisignals.AssetOverviewSignal {
	metadata := metaMap(asset.Payload, "metadata", "Metadata")
	owner := firstNonEmpty(metaString(metadata, "owner", "Owner"), metaString(asset.Payload, "owner", "Owner"))
	tags := stringSlice(metaValue(metadata, "tags", "Tags"))
	if len(tags) == 0 {
		tags = stringSlice(metaValue(asset.Payload, "tags", "Tags"))
	}
	lineage := assetLineage(projectID, asset, assets, edges)
	overview := uisignals.AssetOverviewSignal{
		DownstreamAssets: assetOverviewLinksFromRows(lineage.UsedBy.Rows),
		Owner:            uisignals.Optional(owner),
		Pipelines:        []uisignals.AssetOverviewLinkSignal{},
		Tags:             uisignals.OptionalSlice(tags),
		UpstreamAssets:   assetOverviewLinksFromRows(lineage.Uses.Rows),
	}
	if asset.Type == string(projectview.AssetTypeSemanticModel) {
		datasetCount := int64(len(metaMap(asset.Payload, "Datasets", "datasets")))
		overview.UpstreamDatasetCount = &datasetCount
		overview.Pipelines = semanticModelPipelineLinks(asset, assets, edges)
		overview.DownstreamAssets = appendUniqueAssetOverviewLinks(
			overview.DownstreamAssets,
			semanticModelAdjacentLinks(asset.ID, string(projectview.AssetTypeDashboard), assets, edges),
		)
	}
	overview.UpstreamAssets = appendUniqueAssetOverviewLinks(overview.UpstreamAssets, referencedUpstreamAssetLinks(asset, assets))
	if asset.Type == string(projectview.AssetTypeRefreshPipeline) || asset.Type == "pipeline" {
		overview.PipelineMonitor = pipelineOverviewMonitor(asset, refresh)
		modelRef := metaString(asset.Payload, "SemanticModel", "semanticModel", "semantic_model")
		for _, target := range assets {
			if modelRef == "" || target.Type != string(projectview.AssetTypeSemanticModel) || !assetReferenceMatches(target, modelRef) {
				continue
			}
			overview.DownstreamAssets = appendUniqueAssetOverviewLinks(overview.DownstreamAssets, semanticModelDashboardLinks(target, assets, edges))
			break
		}
	}
	if asset.Type != string(projectview.AssetTypeConnection) && len(versions.Versions) > 0 {
		overview.ActiveVersion = uisignals.Pointer(int64(len(versions.Versions)))
	}
	return overview
}

func pipelineOverviewMonitor(asset projectview.DevelopAssetView, refresh AssetRefreshState) *uisignals.PipelineOverviewMonitorSignal {
	monitor := &uisignals.PipelineOverviewMonitorSignal{
		RecentRuns: []uisignals.PipelineOverviewRunSignal{},
		Schedule:   pipelineScheduleLabel(asset.Payload),
		Status:     "not_run",
	}
	if timezone := metaString(asset.Payload, "Timezone", "timezone"); timezone != "" && monitor.Schedule != "Manual only" && !strings.Contains(monitor.Schedule, timezone) {
		monitor.Schedule += " · " + timezone
	}
	if !refresh.NextRun.IsZero() {
		monitor.NextRunAt = uisignals.Pointer(refresh.NextRun.UTC().Format(time.RFC3339))
	}
	monitor.LastSuccessfulAt = uisignals.Optional(refresh.LatestSuccessful.FinishedAt)
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
	for index, run := range runs {
		if index == 5 {
			break
		}
		monitor.RecentRuns = append(monitor.RecentRuns, pipelineOverviewRun(asset, run))
	}
	latest := refresh.Latest
	if latest.ID == "" && len(runs) > 0 {
		latest = runs[0]
	}
	if latest.ID != "" {
		monitor.LatestRun = uisignals.Pointer(pipelineOverviewRun(asset, latest))
		monitor.Status = strings.ToLower(strings.TrimSpace(latest.Status))
		if monitor.Status == "" {
			monitor.Status = "unknown"
		}
	}
	if refresh.Unavailable {
		monitor.Status = "unavailable"
	}
	return monitor
}

func pipelineOverviewRun(asset projectview.DevelopAssetView, run AssetRefreshRun) uisignals.PipelineOverviewRunSignal {
	return uisignals.PipelineOverviewRunSignal{
		Duration:  uisignals.Optional(refreshRunDuration(run)),
		Error:     uisignals.Optional(strings.TrimSpace(run.Error)),
		Href:      assetnav.CanonicalAssetSectionHref(asset, "refreshes") + "?refresh=" + url.QueryEscape(run.ID),
		ID:        run.ID,
		StartedAt: uisignals.Optional(run.StartedAt),
		Status:    strings.ToLower(strings.TrimSpace(run.Status)),
	}
}

func semanticModelDashboardLinks(model projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) []uisignals.AssetOverviewLinkSignal {
	links := semanticModelAdjacentLinks(model.ID, string(projectview.AssetTypeDashboard), assets, edges)
	for _, candidate := range assets {
		modelRef := metaString(candidate.Payload, "SemanticModel", "semanticModel", "semantic_model")
		if modelRef == "" || candidate.Type != string(projectview.AssetTypeDashboard) || !assetReferenceMatches(model, modelRef) {
			continue
		}
		links = appendUniqueAssetOverviewLinks(links, []uisignals.AssetOverviewLinkSignal{assetOverviewLink(candidate)})
	}
	return links
}

func assetOverviewLinksFromRows(rows []map[string]any) []uisignals.AssetOverviewLinkSignal {
	links := make([]uisignals.AssetOverviewLinkSignal, 0, len(rows))
	for _, row := range rows {
		label := strings.TrimSpace(fmt.Sprint(row["asset"]))
		href := strings.TrimSpace(fmt.Sprint(row["assetHref"]))
		if label == "" || href == "" {
			continue
		}
		links = append(links, uisignals.AssetOverviewLinkSignal{
			Href:  href,
			Label: label,
			Type:  strings.TrimSpace(fmt.Sprint(row["type"])),
		})
	}
	return links
}

func referencedUpstreamAssetLinks(selected projectview.DevelopAssetView, assets []projectview.DevelopAssetView) []uisignals.AssetOverviewLinkSignal {
	refs := []struct {
		typ string
		ref string
	}{}
	switch selected.Type {
	case string(projectview.AssetTypeRefreshPipeline), "pipeline", string(projectview.AssetTypeDashboard):
		refs = append(refs, struct {
			typ string
			ref string
		}{string(projectview.AssetTypeSemanticModel), metaString(selected.Payload, "SemanticModel", "semanticModel", "semantic_model")})
	case string(projectview.AssetTypeSource):
		refs = append(refs, struct {
			typ string
			ref string
		}{string(projectview.AssetTypeConnection), metaString(selected.Payload, "Connection", "connection")})
	case string(projectview.AssetTypeModel):
		for _, ref := range modelSourceNames(selected.Payload) {
			refs = append(refs, struct {
				typ string
				ref string
			}{string(projectview.AssetTypeSource), ref})
		}
	}
	links := make([]uisignals.AssetOverviewLinkSignal, 0, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref.ref) == "" {
			continue
		}
		for _, candidate := range assets {
			if candidate.Type != ref.typ || !assetReferenceMatches(candidate, ref.ref) {
				continue
			}
			links = append(links, assetOverviewLink(candidate))
			break
		}
	}
	return links
}

func assetReferenceMatches(asset projectview.DevelopAssetView, ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == asset.ID || ref == asset.Key {
		return true
	}
	if separator := strings.Index(asset.ID, ":"); separator >= 0 && asset.ID[separator+1:] == ref {
		return true
	}
	return false
}

func appendUniqueAssetOverviewLinks(current, candidates []uisignals.AssetOverviewLinkSignal) []uisignals.AssetOverviewLinkSignal {
	seen := make(map[string]struct{}, len(current)+len(candidates))
	for _, link := range current {
		seen[link.Href] = struct{}{}
	}
	for _, link := range candidates {
		if _, ok := seen[link.Href]; ok {
			continue
		}
		current = append(current, link)
		seen[link.Href] = struct{}{}
	}
	sort.Slice(current, func(i, j int) bool {
		return strings.ToLower(current[i].Label) < strings.ToLower(current[j].Label)
	})
	return current
}

func semanticModelPipelineLinks(selected projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) []uisignals.AssetOverviewLinkSignal {
	links := semanticModelAdjacentLinks(selected.ID, string(projectview.AssetTypeRefreshPipeline), assets, edges)
	links = append(links, semanticModelAdjacentLinks(selected.ID, "pipeline", assets, edges)...)
	included := make(map[string]struct{}, len(links))
	for _, link := range links {
		included[link.Href] = struct{}{}
	}
	for _, candidate := range assets {
		if candidate.Type != string(projectview.AssetTypeRefreshPipeline) && candidate.Type != "pipeline" {
			continue
		}
		semanticModel := metaString(candidate.Payload, "SemanticModel", "semanticModel", "SemanticModelID", "semanticModelId")
		if semanticModel != selected.ID && semanticModel != selected.Key {
			continue
		}
		link := assetOverviewLink(candidate)
		if _, ok := included[link.Href]; ok {
			continue
		}
		links = append(links, link)
		included[link.Href] = struct{}{}
	}
	sort.Slice(links, func(i, j int) bool {
		return strings.ToLower(links[i].Label) < strings.ToLower(links[j].Label)
	})
	return links
}

func semanticModelAdjacentLinks(selectedID, assetType string, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView) []uisignals.AssetOverviewLinkSignal {
	adjacent := map[string]struct{}{}
	for _, edge := range edges {
		switch {
		case edge.FromAssetID == selectedID:
			adjacent[edge.ToAssetID] = struct{}{}
		case edge.ToAssetID == selectedID:
			adjacent[edge.FromAssetID] = struct{}{}
		}
	}
	links := make([]uisignals.AssetOverviewLinkSignal, 0)
	for _, candidate := range assets {
		if candidate.Type != assetType {
			continue
		}
		if _, ok := adjacent[candidate.ID]; !ok {
			continue
		}
		links = append(links, assetOverviewLink(candidate))
	}
	sort.Slice(links, func(i, j int) bool {
		return strings.ToLower(links[i].Label) < strings.ToLower(links[j].Label)
	})
	return links
}

func assetOverviewLink(asset projectview.DevelopAssetView) uisignals.AssetOverviewLinkSignal {
	href := assetnav.CanonicalAssetSectionHref(asset, "details")
	if asset.Type == "pipeline" {
		href = "/pipelines/" + url.PathEscape(asset.ID) + "/details"
	}
	return uisignals.AssetOverviewLinkSignal{
		Href:  href,
		Label: assetTitle(asset),
		Type:  assetTypeLabel(asset.Type),
	}
}

func semanticDimensionsTable(meta map[string]any) recordTable {
	dimensions := metaMap(meta, "Dimensions", "dimensions")
	rows := make([]map[string]any, 0, len(dimensions))
	for _, name := range sortedMapKeys(dimensions) {
		dimension := asMap(dimensions[name])
		bindings := metaMap(dimension, "Bindings", "bindings")
		datasets := sortedMapKeys(bindings)
		fields := make([]string, 0, len(datasets))
		for _, dataset := range datasets {
			binding := asMap(bindings[dataset])
			if field := strings.TrimSpace(metaString(binding, "Field", "field")); field != "" {
				fields = append(fields, field)
			}
		}
		grains := metaStringSlice(dimension, "Grains", "grains")
		rows = append(rows, map[string]any{
			"name":        name,
			"label":       emptyDash(metaString(dimension, "Label", "label")),
			"datatype":    recordTableBadgeValue(firstNonEmpty(metaString(dimension, "Datatype", "datatype"), metaString(dimension, "Type", "type")), "muted"),
			"datasets":    emptyDash(strings.Join(datasets, ", ")),
			"bindings":    emptyDash(strings.Join(fields, ", ")),
			"time_grains": emptyDash(strings.Join(grains, ", ")),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "name", Header: "Name", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("170px")},
			{ID: "label", Header: "Label", Width: uisignals.Pointer("160px")},
			{ID: "datatype", Header: "Type", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("110px")},
			{ID: "datasets", Header: "Datasets", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("220px")},
			{ID: "bindings", Header: "Bindings", Kind: uisignals.Pointer("expression")},
			{ID: "time_grains", Header: "Time grains", Width: uisignals.Pointer("180px")},
		},
		Rows:     rows,
		Empty:    "No conformed dimensions are defined for this semantic model.",
		MinWidth: uisignals.Pointer("1040px"),
	}
}
