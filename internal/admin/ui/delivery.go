package ui

import (
	"fmt"
	"strings"
	"time"

	uisignals "github.com/flidai/leapview/internal/admin/ui/signals"
	deploymentapi "github.com/flidai/leapview/internal/deployment/api"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
)

type AdminDeliveryData = deploymentapi.AdminDeliveryData

func optionalDeploymentValue(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "-"
	}
	return *value
}

func deliveryStatusLabel(snapshot deploymentgen.DeliveryOperatorSnapshotResponse) string {
	if snapshot.Degraded {
		return "Degraded"
	}
	return "Ready"
}

func deliveryEvidenceLabel(snapshot deploymentgen.DeliveryOperatorSnapshotResponse) string {
	if snapshot.Degraded {
		return "Partial evidence — one or more target-owned reads are unavailable."
	}
	return "Complete for the server-bound target."
}

func deliveryIdentifierLabel(value, noun string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "Unavailable"
	}
	// Native delivery IDs are intentionally kept in the evidence payload, but
	// the first value an operator sees should explain what the ID represents.
	return fmt.Sprintf("%s (%s)", noun, value)
}

func deliveryOperatorFacts(snapshot deploymentgen.DeliveryOperatorSnapshotResponse) []uisignals.DefinitionFactSignal {
	reasons := "None reported"
	if len(snapshot.DegradedReasons) > 0 {
		reasons = strings.Join(snapshot.DegradedReasons, ", ")
	}
	return []uisignals.DefinitionFactSignal{
		{Label: "Status", Value: deliveryStatusLabel(snapshot)},
		{Label: "Evidence", Value: deliveryEvidenceLabel(snapshot)},
		{Label: "Project scope", Value: "Server-bound project"},
		{Label: "Project identifier", Value: deliveryIdentifierLabel(snapshot.ProjectId, "Project")},
		{Label: "Environment", Value: humanizeDeliveryValue(snapshot.Environment)},
		{Label: "Serving target", Value: deliveryIdentifierLabel(snapshot.TargetId, "Target")},
		{Label: "Active generation", Value: optionalDeploymentValue(snapshot.ActiveGeneration)},
		{Label: "Target revision", Value: fmt.Sprint(snapshot.TargetRevision)},
		{Label: "Degraded reasons", Value: reasons},
	}
}

func deliveryPublicationTable(rows []deploymentgen.DeliveryPublicationEvidenceResponse, degraded bool) uisignals.RecordTableSignal {
	table := uisignals.RecordTableSignal{
		Columns: []uisignals.RecordTableColumnSignal{
			{ID: "status", Header: "Status", Kind: uisignals.Pointer("status")},
			{ID: "generation", Header: "Serving result", Kind: uisignals.Pointer("text")},
			{ID: "createdAt", Header: "Requested", Kind: uisignals.Pointer("text")},
			{ID: "completedAt", Header: "Completed", Kind: uisignals.Pointer("text")},
			{ID: "resultRevision", Header: "Result revision", Kind: uisignals.Pointer("number")},
			{ID: "evidence", Header: "Evidence", Kind: uisignals.Pointer("status")},
		},
		Rows:  []map[string]any{},
		Empty: "No publication history is retained. This is an empty collection, not evidence that a publication succeeded.",
	}
	if degraded {
		table.Empty = "Publication history is incomplete because delivery evidence is degraded. No rows were returned for this read."
	}
	for _, row := range rows {
		status := humanizeDeliveryValue(string(row.Status))
		evidence := "Complete"
		if strings.TrimSpace(row.GenerationId) == "" || strings.TrimSpace(row.RequestDigest) == "" {
			evidence = "Partial"
		}
		if degraded {
			evidence = "Partial"
		}
		table.Rows = append(table.Rows, map[string]any{
			"id": row.Id, "status": status, "generation": deliveryPublicationResultLabel(row), "generationId": row.GenerationId,
			"createdAt": row.CreatedAt, "completedAt": optionalDeploymentValue(row.CompletedAt), "resultRevision": row.ResultTargetRevision,
			"evidence": evidence, "planDigest": row.PlanDigest, "requestDigest": row.RequestDigest,
		})
	}
	return table
}

func deliveryGenerationTable(rows []deploymentgen.DeliveryGenerationStatusResponse, _ string, degraded bool) uisignals.RecordTableSignal {
	table := uisignals.RecordTableSignal{
		Columns: []uisignals.RecordTableColumnSignal{
			{ID: "status", Header: "Status", Kind: uisignals.Pointer("status")},
			{ID: "generation", Header: "Serving state", Kind: uisignals.Pointer("text")},
			{ID: "createdAt", Header: "Created", Kind: uisignals.Pointer("text")},
			{ID: "activatedAt", Header: "Activated", Kind: uisignals.Pointer("text")},
			{ID: "retiredAt", Header: "Retired", Kind: uisignals.Pointer("text")},
			{ID: "rollbackUntil", Header: "Rollback until", Kind: uisignals.Pointer("text")},
			{ID: "rollbackState", Header: "Rollback availability", Kind: uisignals.Pointer("status")},
			{ID: "rollbackClass", Header: "Rollback class", Kind: uisignals.Pointer("text")},
			{ID: "evidence", Header: "Evidence", Kind: uisignals.Pointer("status")},
			{ID: "actions", Header: "Actions", Kind: uisignals.Pointer("actions")},
		},
		Rows:  []map[string]any{},
		Empty: "No retained generations are available. Rollback cannot be selected from an empty collection.",
	}
	if degraded {
		table.Empty = "Retained-generation evidence is incomplete because delivery is degraded. No rows were returned for this read."
	}
	now := time.Now().UTC()
	for _, row := range rows {
		rollbackOpen := false
		if row.RollbackUntil != nil {
			if until, err := time.Parse(time.RFC3339, *row.RollbackUntil); err == nil {
				rollbackOpen = until.After(now)
			}
		}
		eligible := row.Status != deploymentgen.DeliveryGenerationStatusActive &&
			rollbackOpen &&
			(row.RollbackClass == deploymentgen.DeliveryRollbackClassRollbackSafe || row.RollbackClass == deploymentgen.DeliveryRollbackClassServingSafe)
		actions := []map[string]any{}
		if eligible {
			actions = append(actions, map[string]any{"label": "Rollback", "action": "rollback", "icon": "undo-2"})
		}
		evidence := "Complete"
		if strings.TrimSpace(row.SnapshotSealId) == "" || strings.TrimSpace(row.ServingArtifactDigest) == "" || degraded {
			evidence = "Partial"
		}
		rollbackState := "Rollback window closed"
		if row.Status == deploymentgen.DeliveryGenerationStatusActive {
			rollbackState = "Currently serving"
		} else if rollbackOpen {
			rollbackState = "Rollback available"
		}
		table.Rows = append(table.Rows, map[string]any{
			"id": row.Id, "status": humanizeDeliveryValue(string(row.Status)), "generation": deliveryGenerationResultLabel(row), "generationId": row.Id, "createdAt": row.CreatedAt,
			"activatedAt": optionalDeploymentValue(row.ActivatedAt), "retiredAt": optionalDeploymentValue(row.RetiredAt),
			"rollbackUntil": optionalDeploymentValue(row.RollbackUntil), "rollbackClass": humanizeDeliveryValue(string(row.RollbackClass)),
			"evidence": evidence, "rollbackState": rollbackState, "actions": actions,
			"snapshotSeal": row.SnapshotSealId, "artifactDigest": row.ServingArtifactDigest,
		})
	}
	return table
}

func deliveryPublicationResultLabel(row deploymentgen.DeliveryPublicationEvidenceResponse) string {
	if strings.TrimSpace(row.GenerationId) == "" {
		return "No serving generation"
	}
	if row.Status == deploymentgen.DeliveryPublicationStatusCommitted {
		return "Committed serving generation"
	}
	return humanizeDeliveryValue(string(row.Status))
}

func deliveryGenerationResultLabel(row deploymentgen.DeliveryGenerationStatusResponse) string {
	switch row.Status {
	case deploymentgen.DeliveryGenerationStatusActive:
		return "Active serving generation"
	case deploymentgen.DeliveryGenerationStatusRetired:
		return "Retained rollback generation"
	default:
		return "Prepared generation"
	}
}

func humanizeDeliveryValue(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(value), "_", " "), "-", " ")
	if value == "" {
		return "Unavailable"
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
