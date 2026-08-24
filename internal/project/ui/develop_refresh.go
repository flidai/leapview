package ui

import (
	"strings"
	"time"

	projectview "github.com/flidai/leapview/internal/project"
	"github.com/flidai/leapview/internal/project/assetnav"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func ProjectAssetRefreshSignals(project projectview.DevelopView, asset projectview.DevelopAssetView, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, refresh AssetRefreshState, activeSection string) map[string]any {
	lineage := assetLineage(project.ID, asset, assets, edges)
	return map[string]any{
		"page": projectAssetPageSignalWithRefresh(project, asset, assets, edges, activeSection, lineage, refresh),
	}
}

func assetRefreshSignal(refresh AssetRefreshState) uisignals.ResourceAssetRefreshSignal {
	status := assetRefreshStatus(refresh)
	return uisignals.ResourceAssetRefreshSignal{
		Facts:          uisignals.OptionalSlice(definitionFactSignals(refreshOverviewFacts(refresh))),
		Status:         status,
		Running:        status == "queued" || status == "running",
		LastSuccessful: refresh.LatestSuccessful.FinishedAt,
	}
}

func assetRefreshStatus(refresh AssetRefreshState) string {
	status := strings.TrimSpace(refresh.Latest.Status)
	if refresh.Unavailable {
		return "unavailable"
	}
	if status == "" {
		return "not refreshed"
	}
	return status
}

func modelRefreshSignal(asset projectview.DevelopAssetView) uisignals.ResourceAssetRefreshSignal {
	physical := metaMap(asset.Payload, "Physical", "physical")
	lastSuccessful := metaString(physical, "SnapshotAt", "snapshotAt")
	facts := modelRefreshFacts(asset)
	return uisignals.ResourceAssetRefreshSignal{
		Facts:          uisignals.OptionalSlice(definitionFactSignals(facts)),
		Status:         modelRefreshStatus(asset),
		LastSuccessful: lastSuccessful,
	}
}

func modelRefreshFacts(asset projectview.DevelopAssetView) []definitionFact {
	physical := metaMap(asset.Payload, "Physical", "physical")
	facts := []definitionFact{
		{Label: "Status", Value: modelRefreshStatus(asset)},
		modelLastRefreshedFact(asset),
	}
	if len(physical) > 0 {
		facts = append(facts,
			definitionFact{Label: "Rows", Value: formatCatalogCount(metaInt64(physical, "RowCount", "rowCount"))},
			definitionFact{Label: "Physical size", Value: formatCatalogBytes(metaInt64(physical, "SizeBytes", "sizeBytes"))},
			definitionFact{Label: "Data files", Value: formatCatalogCount(metaInt64(physical, "FileCount", "fileCount"))},
			definitionFact{Label: "DuckLake snapshot", Value: formatCatalogCount(metaInt64(physical, "SnapshotID", "snapshotId")), Code: true},
		)
	}
	return facts
}

func modelRefreshStatus(asset projectview.DevelopAssetView) string {
	if len(metaMap(asset.Payload, "Physical", "physical")) > 0 {
		return "available"
	}
	return firstNonEmpty(metaString(asset.Payload, "PhysicalStatus", "physicalStatus"), "not refreshed")
}

func modelLastRefreshedFact(asset projectview.DevelopAssetView) definitionFact {
	physical := metaMap(asset.Payload, "Physical", "physical")
	value := "Never refreshed"
	if len(physical) > 0 {
		value = "Unknown"
	}
	if metaString(asset.Payload, "PhysicalStatus", "physicalStatus") == "unavailable" {
		value = "Unavailable"
	}
	if snapshotAt := metaString(physical, "SnapshotAt", "snapshotAt"); snapshotAt != "" {
		value = formatCatalogTimestamp(snapshotAt)
	}
	return definitionFact{Label: "Last refreshed", Value: value, Wide: true}
}

func formatCatalogTimestamp(value string) string {
	if parsed, ok := parseRefreshTime(value); ok {
		return parsed.UTC().Format("2006-01-02 15:04 UTC")
	}
	return value
}

func assetVersionsSignal(state AssetVersionsState) uisignals.ResourceAssetVersionsSignal {
	return uisignals.ResourceAssetVersionsSignal{
		CurrentContentHash: state.CurrentContentHash,
		Table:              assetVersionsTable(state),
	}
}

func assetVersionsTable(state AssetVersionsState) recordTable {
	rows := make([]map[string]any, 0, len(state.Versions))
	current := strings.TrimSpace(state.CurrentContentHash)
	for _, version := range state.Versions {
		status := version.Status
		if current != "" && version.ContentHash == current {
			status = "current"
		}
		rows = append(rows, map[string]any{
			"version":      shortHash(version.ContentHash),
			"published":    emptyDash(firstNonEmpty(version.ActivatedAt, version.CreatedAt)),
			"status":       recordTableBadge{Label: status, Tone: uisignals.Pointer(versionStatusTone(status))},
			"config_hash":  shortHash(version.ContentHash),
			"source_file":  emptyDash(version.SourceFile),
			"published_by": emptyDash(version.CreatedBy),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "version", Header: "Version", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("150px")},
			{ID: "published", Header: "Published", Width: uisignals.Pointer("180px")},
			{ID: "status", Header: "Status", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("120px")},
			{ID: "config_hash", Header: "Config hash", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("130px")},
			{ID: "source_file", Header: "Source file", Kind: uisignals.Pointer("code"), Width: uisignals.Pointer("220px")},
			{ID: "published_by", Header: "Published by", Width: uisignals.Pointer("150px")},
		},
		Rows:     rows,
		Empty:    "No config versions recorded for this asset yet.",
		MinWidth: uisignals.Pointer("850px"),
	}
}

func versionStatusTone(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "current":
		return "success"
	case "active", "validated":
		return "accent"
	case "inactive":
		return "muted"
	default:
		return "muted"
	}
}

func shortVersionID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 18 {
		return id
	}
	return id[:18]
}

func shortHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) == 0 {
		return "-"
	}
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

func assetRefreshesTable(refresh AssetRefreshState) recordTable {
	rows := make([]map[string]any, 0, len(refresh.Runs))
	for _, run := range refresh.Runs {
		rows = append(rows, map[string]any{
			"status":           refreshStatusGridValue(run.Status),
			"started":          emptyDash(run.StartedAt),
			"duration":         emptyDash(refreshRunDuration(run)),
			"trigger":          refreshTriggerLabel(run.TriggerType),
			"triggered_by":     emptyDash(run.PrincipalDisplayName),
			"runId":            run.ID,
			"environment":      emptyDash(run.Environment),
			"modelId":          emptyDash(run.ModelID),
			"servingStateId":   emptyDash(run.ServingStateID),
			"parentRunId":      emptyDash(run.ParentRunID),
			"targetGeneration": run.TargetGeneration,
			"createdAt":        emptyDash(run.CreatedAt),
			"updatedAt":        emptyDash(run.UpdatedAt),
			"startedAt":        emptyDash(run.StartedAt),
			"finishedAt":       emptyDash(run.FinishedAt),
			"statusLabel":      emptyDash(run.Status),
			"error":            strings.TrimSpace(run.Error),
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "status", Header: "Status", Kind: uisignals.Pointer("status"), Width: uisignals.Pointer("140px")},
			{ID: "started", Header: "Started", Width: uisignals.Pointer("180px")},
			{ID: "duration", Header: "Duration", Width: uisignals.Pointer("110px")},
			{ID: "trigger", Header: "Trigger", Width: uisignals.Pointer("130px")},
			{ID: "triggered_by", Header: "Initiated by", Width: uisignals.Pointer("160px")},
		},
		Rows:      rows,
		Empty:     "No refresh runs have been recorded for this asset.",
		MinWidth:  uisignals.Pointer("720px"),
		RowAction: uisignals.Pointer("open-refresh-run"),
	}
}

func refreshTriggerLabel(trigger string) string {
	switch strings.TrimSpace(trigger) {
	case "manual":
		return "Manual"
	case "schedule":
		return "Schedule"
	case "dependency":
		return "Pipeline"
	default:
		return "-"
	}
}

func refreshStatusGridValue(status string) any {
	status = strings.TrimSpace(status)
	if status == "" {
		status = "not refreshed"
	}
	return recordTableBadge{Label: status, Tone: uisignals.Pointer(refreshStatusBadgeTone(status))}
}

func refreshStatusBadgeTone(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded":
		return "success"
	case "running", "queued":
		return "accent"
	case "failed":
		return "danger"
	default:
		return "muted"
	}
}

func shortRefreshRunID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 18 {
		return id
	}
	return id[:18]
}

func refreshRunDuration(run AssetRefreshRun) string {
	started, ok := parseRefreshTime(run.StartedAt)
	if !ok {
		return ""
	}
	finished, ok := parseRefreshTime(run.FinishedAt)
	if !ok || finished.Before(started) {
		return ""
	}
	return finished.Sub(started).Round(time.Second).String()
}

func parseRefreshTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func assetRefreshable(assetType string) bool {
	return assetType == "refresh_pipeline"
}

func assetHasRefreshHistory(assetType string) bool {
	return assetType == "refresh_pipeline" || assetType == "model_table" || assetType == "semantic_model"
}

func assetDataInspectable(assetType string) bool {
	return assetType == "semantic_model" || assetType == "model_table"
}

func projectAssetDataHref(asset projectview.DevelopAssetView) string {
	return assetnav.CanonicalAssetSectionHref(asset, "data")
}

func normalizeProjectAssetSection(section string) string {
	section = strings.TrimSpace(section)
	if validProjectAssetSectionName(section) {
		return section
	}
	return "details"
}

type assetLineageModel struct {
	Count  int
	Graph  assetLineageGraph
	Uses   recordTable
	UsedBy recordTable
}

type assetLineageGraph = uisignals.AssetLineageGraphSignal
type assetLineageNode = uisignals.AssetLineageNodeSignal
type assetLineageEdge = uisignals.AssetLineageEdgeSignal
