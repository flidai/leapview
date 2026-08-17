package module

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/dashboard/publication"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var errPublicationCommandAuditUnavailable = apigenfailure.New("audit_unavailable", "dashboard publication command audit is unavailable")

type publicationCommandAuditContract struct {
	owner      string
	action     string
	capability access.Capability
}

type publicationCommandAuditInput struct {
	operationID   string
	projectID     projectgraph.ResourceID
	principalID   string
	targetID      string
	requestID     string
	correlationID string
	surface       string
}

func buildPublicationCommandAuditRecorder(
	record func(context.Context, access.AuditEventInput) error,
) (func(context.Context, publicationCommandAuditInput) error, error) {
	operationIDs := []string{
		dashboardgen.GenOperationSuspendDashboardPublication,
		dashboardgen.GenOperationResumeDashboardPublication,
		dashboardgen.GenOperationRotateDashboardPublication,
	}
	contracts := make(map[string]publicationCommandAuditContract, len(operationIDs))
	for _, operationID := range operationIDs {
		generated, ok := dashboardgen.GetAPIGenOperationContract(operationID)
		if !ok || generated.Command == nil {
			return nil, fmt.Errorf("dashboard publication operation %q is missing its generated command contract", operationID)
		}
		command := generated.Command
		capability := access.CapabilityResourcePublish
		if command.AuthzMode != "authenticated" || generated.AuthzMode != command.AuthzMode ||
			!command.Audit.Required || command.Audit.SuccessAction == "" || command.Target == nil ||
			command.Audit.Guarantee != "best-effort" || command.Target.Type != "project" || command.Target.Parameter != "project" {
			return nil, fmt.Errorf("dashboard publication operation %q has an invalid generated command audit contract", operationID)
		}
		contracts[operationID] = publicationCommandAuditContract{
			owner: command.Owner, action: command.Audit.SuccessAction, capability: capability,
		}
	}
	if record == nil {
		return nil, errPublicationCommandAuditUnavailable
	}
	return func(ctx context.Context, input publicationCommandAuditInput) error {
		contract, ok := contracts[input.operationID]
		if !ok {
			return fmt.Errorf("dashboard publication operation %q has no command audit contract", input.operationID)
		}
		payload := dashboardgen.GenSchemaDashboardPublicationCommandAuditPayload{
			OperationId: input.operationID, Owner: contract.owner, Surface: input.surface,
		}
		var metadata string
		var err error
		switch input.operationID {
		case string(dashboardgen.GenOperationSuspendDashboardPublication):
			metadata, err = dashboardgen.EncodeGenSuspendDashboardPublicationAuditPayload(payload)
		case string(dashboardgen.GenOperationResumeDashboardPublication):
			metadata, err = dashboardgen.EncodeGenResumeDashboardPublicationAuditPayload(payload)
		case string(dashboardgen.GenOperationRotateDashboardPublication):
			metadata, err = dashboardgen.EncodeGenRotateDashboardPublicationAuditPayload(payload)
		default:
			return fmt.Errorf("dashboard publication operation %q has no audit payload encoder", input.operationID)
		}
		if err != nil {
			return err
		}
		// The publication repository commits its constrained domain event before
		// this cross-domain Access audit. The caller observes recorder failures but
		// preserves the already-successful publication result.
		return record(ctx, access.AuditEventInput{
			ResourceKind: "project", ResourceID: input.projectID.String(), PrincipalID: input.principalID,
			Action: contract.action, Capability: contract.capability, Status: "success",
			RequestID: input.requestID, CorrelationID: input.correlationID,
			MetadataJSON: metadata,
		})
	}, nil
}

func publicationOperationID(action publication.Action) (dashboardgen.GenCommandOperationID, bool) {
	switch action {
	case publication.ActionSuspend:
		return dashboardgen.GenCommandOperationSuspendDashboardPublication(), true
	case publication.ActionResume:
		return dashboardgen.GenCommandOperationResumeDashboardPublication(), true
	case publication.ActionRotate:
		return dashboardgen.GenCommandOperationRotateDashboardPublication(), true
	default:
		return dashboardgen.GenCommandOperationID{}, false
	}
}

func publicationAuditRequestInput(r *http.Request, operationID string, projectID projectgraph.ResourceID, principalID, targetID string) publicationCommandAuditInput {
	requestID := firstPublicationHeader(r, "X-Request-Id", "X-Request-ID")
	correlationID := firstPublicationHeader(r, "X-Correlation-Id", "X-Correlation-ID")
	if correlationID == "" {
		correlationID = requestID
	}
	surface := "api"
	if strings.EqualFold(firstPublicationHeader(r, "X-LeapView-Invocation-Surface", "X-LeapView-Client"), "cli") {
		surface = "cli"
	}
	return publicationCommandAuditInput{
		operationID: operationID, projectID: projectID, principalID: strings.TrimSpace(principalID),
		targetID: strings.TrimSpace(targetID), requestID: requestID, correlationID: correlationID, surface: surface,
	}
}

func firstPublicationHeader(r *http.Request, names ...string) string {
	if r == nil {
		return ""
	}
	for _, name := range names {
		if value := strings.TrimSpace(r.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}
