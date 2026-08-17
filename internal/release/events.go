package release

// FinalizationEventData returns the durable public event projection for a
// terminal release finalization transition.
func FinalizationEventData(row Release) map[string]any {
	connections := make([]map[string]any, 0, len(row.Manifest.Connections))
	for _, item := range row.Manifest.Connections {
		connections = append(connections, map[string]any{"connection": item.ConnectionID, "revisionId": item.RevisionID})
	}
	result := map[string]any{
		"id": row.ID, "projectId": row.ServingIdentity.ProjectID.String(), "environment": row.ServingIdentity.Environment,
		"generationId": row.ServingIdentity.GenerationID, "projectDigest": row.ProjectDigest,
		"artifactDigest": row.ArtifactDigest, "status": string(row.Status),
		"createdBy": row.CreatedBy, "createdAt": row.CreatedAt, "connections": connections,
	}
	if row.FinalizedAt != "" {
		result["finalizedAt"] = row.FinalizedAt
	}
	if row.Error != "" {
		result["error"] = row.Error
	}
	return result
}
