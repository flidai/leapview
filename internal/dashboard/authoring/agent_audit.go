package authoring

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/google/uuid"
)

// Agent tools use the same source-owned transactional audit contract as other
// dashboard command producers. The repository commits this intent atomically
// with the revision, using the exact same native identity as the command.
func AgentMutationContext(ctx context.Context, actorID, projectID string, command Command) (context.Context, error) {
	contract, ok := dashboardgen.GetAPIGenCommandRuntimeContract("executeDashboardAuthoringCommand")
	if !ok {
		return ctx, fmt.Errorf("dashboard authoring command contract is unavailable")
	}
	metadata, err := dashboardgen.EncodeGenExecuteDashboardAuthoringCommandAuditPayload(dashboardgen.GenSchemaDashboardAuthoringCommandAuditPayload{OperationId: contract.OperationID, ProjectId: projectID, DashboardId: command.DashboardID.String(), DraftId: command.DraftID.String(), Origin: string(OriginAgent)})
	if err != nil {
		return ctx, err
	}
	capability := access.CapabilityResourceEdit
	if command.Publish != nil {
		capability = access.CapabilityResourcePublish
	}
	if command.Archive != nil {
		capability = access.CapabilityResourceManage
	}
	return WithAuditIntent(ctx, access.AuditIntent{EventID: string(command.ID), Source: "dashboard.authoring", Operation: contract.OperationID, ActorID: actorID, PrincipalID: actorID, Action: contract.AuditAction, ResourceKind: "dashboard", ResourceID: command.DashboardID.String(), Capability: capability, Outcome: "success", MetadataJSON: metadata}), nil
}

// Creation allocates dashboard/draft IDs in the transaction. The repository
// fills those identities and replays the original committed audit record when
// the tool-call idempotency key is retried.
func AgentCreationContext(ctx context.Context, actorID, projectID, operation string) (context.Context, error) {
	contract, ok := dashboardgen.GetAPIGenCommandRuntimeContract(operation)
	if !ok {
		return ctx, fmt.Errorf("dashboard creation contract is unavailable")
	}
	payload := dashboardgen.GenSchemaDashboardAuthoringCommandAuditPayload{OperationId: operation, ProjectId: projectID, DashboardId: "pending-dashboard", DraftId: "pending-draft", Origin: string(OriginAgent)}
	var metadata string
	var err error
	switch operation {
	case "createDashboardAuthoringDraft":
		metadata, err = dashboardgen.EncodeGenCreateDashboardAuthoringDraftAuditPayload(payload)
	case "forkDashboardAuthoringDraft":
		metadata, err = dashboardgen.EncodeGenForkDashboardAuthoringDraftAuditPayload(payload)
	default:
		return ctx, fmt.Errorf("unsupported creation operation %q", operation)
	}
	if err != nil {
		return ctx, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return ctx, err
	}
	return WithAuditIntent(ctx, access.AuditIntent{EventID: id.String(), Source: "dashboard.authoring", Operation: operation, ActorID: actorID, PrincipalID: actorID, Action: contract.AuditAction, ResourceKind: "dashboard", ResourceID: "pending-dashboard", Capability: access.CapabilityResourceEdit, Outcome: "success", MetadataJSON: metadata}), nil
}
