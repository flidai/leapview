package authoring

import (
	"errors"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
)

// BuildExplorationAppendAuditIntent uses the dashboard-owned generated command
// contract for an Explorer-initiated authoring mutation. The transport must
// validate its request and idempotency identities before calling this function.
func BuildExplorationAppendAuditIntent(operationID, projectID, actorID, dashboardID, requestID, idempotencyKey, correlationID string) (access.AuditIntent, error) {
	contract, ok := dashboardgen.GetAPIGenCommandRuntimeContract(operationID)
	if !ok || contract.Guarantee != apigencommand.GuaranteeTransactional {
		return access.AuditIntent{}, errors.New("dashboard append operation has no transactional audit contract")
	}
	if strings.TrimSpace(correlationID) == "" {
		correlationID = requestID
	}
	metadata, err := dashboardgen.EncodeGenExecuteDashboardAuthoringCommandAuditPayload(dashboardgen.GenSchemaDashboardAuthoringCommandAuditPayload{
		OperationId: contract.OperationID, ProjectId: strings.TrimSpace(projectID), DashboardId: strings.TrimSpace(dashboardID),
		DraftId: "pending-draft", Origin: string(OriginUI),
	})
	if err != nil {
		return access.AuditIntent{}, err
	}
	return access.AuditIntent{
		EventID: strings.TrimSpace(idempotencyKey), Source: "dashboard.authoring", Operation: contract.OperationID,
		ActorID: strings.TrimSpace(actorID), PrincipalID: strings.TrimSpace(actorID), Action: contract.AuditAction,
		ResourceKind: "dashboard", ResourceID: strings.TrimSpace(dashboardID), Capability: access.CapabilityResourceEdit,
		Outcome: "success", RequestID: strings.TrimSpace(requestID), CorrelationID: strings.TrimSpace(correlationID), MetadataJSON: metadata,
	}, nil
}
