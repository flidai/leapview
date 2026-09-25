package authoring

import (
	"context"
	"fmt"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// WithAuditIntent carries the command's source-built durable audit intent into
// the authoring repository transaction. The repository fills identities that
// are allocated as part of the mutation before handing the intent to Access.
func WithAuditIntent(ctx context.Context, intent access.AuditIntent) context.Context {
	return context.WithValue(ctx, auditIntentContextKey{}, intent)
}

// WithCommandAuditIntent binds the generated dashboard command contract to a
// non-HTTP authoring invocation. The repository completes the pending target
// IDs inside the same transaction that records the domain event and audit row.
func WithCommandAuditIntent(ctx context.Context, operation string, project projectgraph.ResourceID, eventID, actor, dashboardID, draftID string, origin Origin, capability access.Capability) (context.Context, error) {
	contract, ok := dashboardgen.GetAPIGenCommandRuntimeContract(operation)
	if !ok || contract.Guarantee != apigencommand.GuaranteeTransactional {
		return nil, fmt.Errorf("dashboard authoring operation %q has no transactional audit contract", operation)
	}
	if dashboardID == "" {
		dashboardID = "pending-dashboard"
	}
	if draftID == "" {
		draftID = "pending-draft"
	}
	payload := dashboardgen.GenSchemaDashboardAuthoringCommandAuditPayload{
		OperationId: operation, ProjectId: project.String(), DashboardId: dashboardID,
		DraftId: draftID, Origin: string(origin),
	}
	var metadata string
	var err error
	switch operation {
	case "createDashboardAuthoringDraft":
		metadata, err = dashboardgen.EncodeGenCreateDashboardAuthoringDraftAuditPayload(payload)
	case "forkDashboardAuthoringDraft":
		metadata, err = dashboardgen.EncodeGenForkDashboardAuthoringDraftAuditPayload(payload)
	case "executeDashboardAuthoringCommand":
		metadata, err = dashboardgen.EncodeGenExecuteDashboardAuthoringCommandAuditPayload(payload)
	default:
		return nil, fmt.Errorf("unknown dashboard authoring operation %q", operation)
	}
	if err != nil {
		return nil, err
	}
	intent := access.AuditIntent{
		EventID: eventID, Source: "dashboard.authoring", Operation: operation,
		ActorID: actor, PrincipalID: actor, Action: contract.AuditAction,
		ResourceKind: "dashboard", ResourceID: dashboardID, Capability: capability,
		Outcome: "success", MetadataJSON: metadata,
	}
	return WithAuditIntent(ctx, intent), nil
}

// AuditIntentFromContext returns the intent supplied by the command producer.
func AuditIntentFromContext(ctx context.Context) (access.AuditIntent, bool) {
	if ctx == nil {
		return access.AuditIntent{}, false
	}
	intent, ok := ctx.Value(auditIntentContextKey{}).(access.AuditIntent)
	return intent, ok
}

type auditIntentContextKey struct{}
