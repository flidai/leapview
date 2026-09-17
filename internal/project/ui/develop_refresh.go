package ui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	projectview "github.com/flidai/leapview/internal/project"
	"github.com/flidai/leapview/internal/project/assetnav"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	"github.com/pmezard/go-difflib/difflib"
)

func assetRefreshSignal(refresh AssetRefreshState) uisignals.ResourceAssetRefreshSignal {
	status := assetRefreshStatus(refresh)
	return uisignals.ResourceAssetRefreshSignal{
		Facts:          uisignals.OptionalSlice(definitionFactSignals(refreshOverviewFacts(refresh))),
		Status:         status,
		Running:        status == "queued" || status == "running",
		LastSuccessful: assetLastSuccessful(refresh),
	}
}

func assetRefreshStatus(refresh AssetRefreshState) string {
	status := strings.TrimSpace(refresh.Latest.Status)
	if refresh.Unavailable {
		return "unavailable"
	}
	if status == "" {
		if strings.TrimSpace(refresh.LatestSuccessful.Status) != "" {
			return strings.TrimSpace(refresh.LatestSuccessful.Status)
		}
		if strings.TrimSpace(refresh.LatestSuccessful.FinishedAt) != "" {
			return "succeeded"
		}
		for _, run := range refresh.Runs {
			if runStatus := strings.TrimSpace(run.Status); runStatus != "" {
				return runStatus
			}
		}
		if refresh.DataVersion.SnapshotID > 0 && !refresh.DataVersion.RefreshedAt.IsZero() {
			return "succeeded"
		}
		return "not refreshed"
	}
	return status
}

func assetLastSuccessful(refresh AssetRefreshState) string {
	if refresh.DataVersion.SnapshotID > 0 && !refresh.DataVersion.RefreshedAt.IsZero() {
		return refresh.DataVersion.RefreshedAt.UTC().Format(time.RFC3339Nano)
	}
	if value := firstNonEmpty(refresh.LatestSuccessful.FinishedAt, refresh.LatestSuccessful.UpdatedAt, refresh.LatestSuccessful.CreatedAt); value != "" {
		return value
	}
	if run, ok := latestSuccessfulRefreshRun(refresh.Runs); ok {
		return firstNonEmpty(run.FinishedAt, run.UpdatedAt, run.CreatedAt)
	}
	return ""
}

func latestSuccessfulRefreshRun(runs []AssetRefreshRun) (AssetRefreshRun, bool) {
	for _, run := range runs {
		switch strings.ToLower(strings.TrimSpace(run.Status)) {
		case "succeeded", "success", "completed":
			return run, true
		}
	}
	return AssetRefreshRun{}, false
}

func modelRefreshSignal(asset projectview.DevelopAssetView, refresh AssetRefreshState) uisignals.ResourceAssetRefreshSignal {
	physical := metaMap(asset.Payload, "Physical", "physical")
	lastSuccessful := metaString(physical, "SnapshotAt", "snapshotAt")
	status := modelRefreshStatus(asset)
	if refresh.Unavailable || refresh.Latest.Status != "" || refresh.LatestSuccessful.Status != "" || refresh.LatestSuccessful.FinishedAt != "" || len(refresh.Runs) > 0 || refresh.DataVersion.SnapshotID > 0 {
		status = assetRefreshStatus(refresh)
		lastSuccessful = assetLastSuccessful(refresh)
	}
	return uisignals.ResourceAssetRefreshSignal{
		Status:         status,
		LastSuccessful: lastSuccessful,
	}
}

func modelRefreshStatus(asset projectview.DevelopAssetView) string {
	if len(metaMap(asset.Payload, "Physical", "physical")) > 0 {
		return "available"
	}
	return firstNonEmpty(metaString(asset.Payload, "PhysicalStatus", "physicalStatus"), "not refreshed")
}

func modelLastRefreshedFact(asset projectview.DevelopAssetView, refresh AssetRefreshState) definitionFact {
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
	if lastSuccessful := assetLastSuccessful(refresh); lastSuccessful != "" {
		value = formatCatalogTimestamp(lastSuccessful)
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
	versions := normalizedAssetVersionHistory(state.Versions)
	rows := make([]map[string]any, 0, len(versions))
	current := strings.TrimSpace(state.CurrentContentHash)
	currentIndex := -1
	if current != "" {
		for index, version := range versions {
			if version.ContentHash == current {
				currentIndex = index
				break
			}
		}
	}
	if currentIndex < 0 {
		for index, version := range versions {
			if status := strings.ToLower(strings.TrimSpace(version.Status)); status == "active" || status == "current" {
				currentIndex = index
				break
			}
		}
	}
	for index, version := range versions {
		versionNumber := len(versions) - index
		status := strings.ToLower(strings.TrimSpace(version.Status))
		if currentIndex >= 0 {
			if index == currentIndex {
				status = "current"
			} else if status == "active" || status == "current" || status == "" {
				status = "inactive"
			}
		}
		compiledConfiguration := formatCompiledConfiguration(version.PayloadJSON)
		changes := ""
		changesSummary := "This is the first recorded version."
		previousVersion := ""
		var diffStat any = "—"
		if index+1 < len(versions) {
			previous := versions[index+1]
			previousVersion = strconv.Itoa(versionNumber - 1)
			changes = compiledConfigurationDiff(previous, version)
			additions, deletions := compiledConfigurationDiffStats(previous, version)
			if additions == 0 && deletions == 0 {
				diffStat = "—"
			} else {
				diffStat = recordTableDiff{
					Label:     diffStatLabel(additions, deletions),
					Additions: additions,
					Deletions: deletions,
				}
			}
			changesSummary = "No compiled configuration changes."
			if strings.TrimSpace(changes) != "" {
				changesSummary = ""
			}
		}
		rows = append(rows, map[string]any{
			"version":               versionNumber,
			"published":             formatRefreshTimestamp(firstNonEmpty(version.ActivatedAt, version.CreatedAt)),
			"status":                recordTableBadge{Label: status, Tone: uisignals.Pointer(versionStatusTone(status))},
			"published_by":          unavailableDash(firstNonEmpty(version.CreatedByDisplayName, principalDisplayLabel(version.CreatedBy))),
			"diff_stat":             diffStat,
			"versionId":             version.ServingStateID,
			"statusLabel":           unavailableDash(status),
			"contentHash":           unavailableDash(version.ContentHash),
			"sourceFile":            unavailableDash(version.SourceFile),
			"environment":           unavailableDash(version.Environment),
			"snapshotId":            unavailableDash(version.SnapshotID),
			"servingStateId":        unavailableDash(version.ServingStateID),
			"servingDigest":         unavailableDash(version.Digest),
			"createdAt":             formatRefreshTimestamp(version.CreatedAt),
			"activatedAt":           formatRefreshTimestamp(version.ActivatedAt),
			"compiledConfiguration": compiledConfiguration,
			"previousVersion":       previousVersion,
			"changes":               changes,
			"changesSummary":        changesSummary,
		})
	}
	return recordTable{
		Columns: []recordTableColumn{
			{ID: "version", Header: "Version", Kind: uisignals.Pointer("number"), Align: uisignals.Pointer("right"), Width: uisignals.Pointer("90px")},
			{ID: "published", Header: "Published", Width: uisignals.Pointer("180px")},
			{ID: "diff_stat", Header: "Changes", Kind: uisignals.Pointer("diff"), Width: uisignals.Pointer("120px")},
			{ID: "status", Header: "Status", Kind: uisignals.Pointer("badge"), Width: uisignals.Pointer("120px")},
			{ID: "published_by", Header: "Published by", Width: uisignals.Pointer("180px")},
		},
		Rows:      rows,
		Empty:     "No config versions recorded for this asset yet.",
		MinWidth:  uisignals.Pointer("700px"),
		RowAction: uisignals.Pointer("open-asset-version"),
	}
}

func normalizedAssetVersionHistory(versions []AssetVersionState) []AssetVersionState {
	normalized := make([]AssetVersionState, 0, len(versions))
	byServingState := map[string]int{}
	for _, version := range versions {
		servingStateID := strings.TrimSpace(version.ServingStateID)
		if servingStateID == "" {
			normalized = append(normalized, version)
			continue
		}
		if index, exists := byServingState[servingStateID]; exists {
			if assetVersionTime(version).After(assetVersionTime(normalized[index])) {
				normalized[index] = version
			}
			continue
		}
		byServingState[servingStateID] = len(normalized)
		normalized = append(normalized, version)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		left, right := assetVersionTime(normalized[i]), assetVersionTime(normalized[j])
		if !left.Equal(right) {
			return left.After(right)
		}
		return normalized[i].ServingStateID > normalized[j].ServingStateID
	})
	return normalized
}

func assetVersionTime(version AssetVersionState) time.Time {
	if parsed, ok := parseRefreshTime(firstNonEmpty(version.ActivatedAt, version.CreatedAt)); ok {
		return parsed
	}
	return time.Time{}
}

func formatCompiledConfiguration(payload string) string {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return ""
	}
	var value any
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		return payload
	}
	formatted, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return payload
	}
	return string(formatted) + "\n"
}

func compiledConfigurationDiff(previous, current AssetVersionState) string {
	before := formatCompiledConfiguration(previous.PayloadJSON)
	after := formatCompiledConfiguration(current.PayloadJSON)
	if before == after {
		return ""
	}
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: difflib.SplitLines(before), B: difflib.SplitLines(after),
		FromFile: shortHash(previous.ContentHash), ToFile: shortHash(current.ContentHash),
		Context: 3,
	})
	if err != nil {
		return ""
	}
	return diff
}

func compiledConfigurationDiffStats(previous, current AssetVersionState) (additions, deletions int) {
	before := difflib.SplitLines(formatCompiledConfiguration(previous.PayloadJSON))
	after := difflib.SplitLines(formatCompiledConfiguration(current.PayloadJSON))
	for _, operation := range difflib.NewMatcher(before, after).GetOpCodes() {
		switch operation.Tag {
		case 'r':
			deletions += operation.I2 - operation.I1
			additions += operation.J2 - operation.J1
		case 'd':
			deletions += operation.I2 - operation.I1
		case 'i':
			additions += operation.J2 - operation.J1
		}
	}
	return additions, deletions
}

func diffStatLabel(additions, deletions int) string {
	if additions == 0 && deletions == 0 {
		return "—"
	}
	return fmt.Sprintf("%d %s, %d %s", additions, pluralizeLine(additions, "addition"), deletions, pluralizeLine(deletions, "deletion"))
}

func pluralizeLine(count int, singular string) string {
	if count == 1 {
		return singular
	}
	return singular + "s"
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
		startedAt := refreshRunStartedAt(run)
		rows = append(rows, map[string]any{
			"status":           refreshStatusGridValue(run.Status),
			"started":          formatRefreshTimestamp(startedAt),
			"duration":         unavailableDash(refreshRunDuration(run)),
			"trigger":          refreshRunTriggerLabel(run),
			"triggered_by":     unavailableDash(firstNonEmpty(run.PrincipalDisplayName, principalDisplayLabel(run.PrincipalID))),
			"runId":            run.ID,
			"environment":      unavailableDash(run.Environment),
			"modelId":          unavailableDash(run.ModelID),
			"servingStateId":   unavailableDash(run.ServingStateID),
			"parentRunId":      unavailableDash(run.ParentRunID),
			"targetGeneration": run.TargetGeneration,
			"createdAt":        formatRefreshTimestamp(run.CreatedAt),
			"updatedAt":        formatRefreshTimestamp(run.UpdatedAt),
			"startedAt":        formatRefreshTimestamp(startedAt),
			"finishedAt":       formatRefreshTimestamp(run.FinishedAt),
			"statusLabel":      unavailableDash(run.Status),
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
		return "—"
	}
}

func refreshRunTriggerLabel(run AssetRefreshRun) string {
	if strings.TrimSpace(run.TriggerType) != "" {
		return refreshTriggerLabel(run.TriggerType)
	}
	if strings.TrimSpace(run.ParentRunID) != "" {
		return "Pipeline"
	}
	return "—"
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
	started, ok := parseRefreshTime(refreshRunStartedAt(run))
	if !ok {
		return ""
	}
	finished, ok := parseRefreshTime(run.FinishedAt)
	if !ok || finished.Before(started) {
		return ""
	}
	return finished.Sub(started).Round(time.Second).String()
}

func refreshRunStartedAt(run AssetRefreshRun) string {
	return firstNonEmpty(run.StartedAt, run.CreatedAt)
}

func formatRefreshTimestamp(value string) string {
	if parsed, ok := parseRefreshTime(value); ok {
		return parsed.Local().Format("02 Jan 2006, 15:04 MST")
	}
	return unavailableDash(value)
}

func principalDisplayLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if looksLikeUUID(value) {
		return "Unknown actor"
	}
	lower := strings.ToLower(value)
	for _, prefix := range []string{"principal:", "user:"} {
		if strings.HasPrefix(lower, prefix) {
			return humanizeIdentifier(value[len(prefix):])
		}
	}
	for _, prefix := range []string{"service:", "service-account:"} {
		if strings.HasPrefix(lower, prefix) {
			label := humanizeIdentifier(value[len(prefix):])
			if label == "" {
				return "Service account"
			}
			return label + " service account"
		}
	}
	return value
}

func looksLikeUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
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
	return assetType == "refresh_pipeline" || assetType == "pipeline"
}

func assetHasRefreshHistory(assetType string) bool {
	return assetType == "refresh_pipeline" || assetType == "pipeline" || assetType == "model" || assetType == "semantic_model"
}

func assetDataInspectable(assetType string) bool {
	return assetType == "semantic_model" || assetType == "model"
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
