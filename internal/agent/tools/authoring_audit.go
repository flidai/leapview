package tools

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	agentcore "github.com/flidai/leapview/pkg/agent"
	"github.com/google/uuid"
)

// The model's tool-call ID is not a UUID. Derive a stable UUIDv7-formatted
// command identity from its authenticated conversation so a retried call uses
// the same repository idempotency and audit event identity.
func authoringInvocationID(project projectgraph.ResourceID, scope Scope, call agentcore.ToolCall) (string, error) {
	if strings.TrimSpace(call.ID) == "" {
		return "", fmt.Errorf("dashboard authoring tool call ID is required")
	}
	sum := sha256.Sum256([]byte(project.String() + "\x00" + scope.PrincipalID + "\x00" + scope.ConversationID + "\x00" + call.ID))
	var id uuid.UUID
	copy(id[:], sum[:16])
	id[6] = (id[6] & 0x0f) | 0x70
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String(), nil
}

// The repository requires the same transaction-bound generated audit envelope
// for Agent mutations as for browser and API mutations.
func agentAuthoringAuditContext(ctx context.Context, project projectgraph.ResourceID, scope Scope, call agentcore.ToolCall, operation, dashboardID, draftID string, capability access.Capability) (context.Context, string, error) {
	id, err := authoringInvocationID(project, scope, call)
	if err != nil {
		return nil, "", err
	}
	contract, ok := dashboardgen.GetAPIGenCommandRuntimeContract(operation)
	if !ok || contract.Guarantee != apigencommand.GuaranteeTransactional {
		return nil, "", fmt.Errorf("dashboard authoring operation %q has no transactional audit contract", operation)
	}
	if dashboardID == "" {
		dashboardID = "pending-dashboard"
	}
	if draftID == "" {
		draftID = "pending-draft"
	}
	payload := dashboardgen.GenSchemaDashboardAuthoringCommandAuditPayload{
		OperationId: operation, ProjectId: project.String(), DashboardId: dashboardID,
		DraftId: draftID, Origin: string(authoring.OriginAgent),
	}
	var metadata string
	switch operation {
	case "createDashboardAuthoringDraft":
		metadata, err = dashboardgen.EncodeGenCreateDashboardAuthoringDraftAuditPayload(payload)
	case "forkDashboardAuthoringDraft":
		metadata, err = dashboardgen.EncodeGenForkDashboardAuthoringDraftAuditPayload(payload)
	case "executeDashboardAuthoringCommand":
		metadata, err = dashboardgen.EncodeGenExecuteDashboardAuthoringCommandAuditPayload(payload)
	default:
		return nil, "", fmt.Errorf("unknown dashboard authoring operation %q", operation)
	}
	if err != nil {
		return nil, "", err
	}
	intent := access.AuditIntent{
		EventID: id, Source: "dashboard.authoring", Operation: operation,
		ActorID: scope.PrincipalID, PrincipalID: scope.PrincipalID, Action: contract.AuditAction,
		ResourceKind: "dashboard", ResourceID: dashboardID, Capability: capability,
		Outcome: "success", MetadataJSON: metadata,
	}
	return authoring.WithAuditIntent(ctx, intent), id, nil
}

func agentAuthoringCapability(command authoring.Command) access.Capability {
	switch action, _ := command.RequiredAction(); action {
	case authoring.AuthorizationActionPublish:
		return access.CapabilityResourcePublish
	case authoring.AuthorizationActionArchive, authoring.AuthorizationActionDelete:
		return access.CapabilityResourceManage
	default:
		return access.CapabilityResourceEdit
	}
}
