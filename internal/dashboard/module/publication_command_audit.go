package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/dashboard/publication"
)

var errPublicationCommandAuditUnavailable = errors.New("dashboard publication command audit is unavailable")

type publicationCommandAuditContract struct {
	owner     string
	action    string
	privilege access.Privilege
}

type publicationCommandAuditInput struct {
	operationID   string
	workspaceID   string
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
		privilege, ok := access.ParsePrivilege(command.Privilege)
		if !ok || command.AuthzMode != "privilege" || generated.AuthzMode != command.AuthzMode ||
			!command.Audit.Required || command.Audit.SuccessAction == "" || command.Target == nil ||
			command.Audit.Guarantee != "best-effort" || command.Target.Type != "workspace" || command.Target.Parameter != "workspace" {
			return nil, fmt.Errorf("dashboard publication operation %q has an invalid generated command audit contract", operationID)
		}
		contracts[operationID] = publicationCommandAuditContract{
			owner: command.Owner, action: command.Audit.SuccessAction, privilege: privilege,
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
		metadata, err := json.Marshal(map[string]string{
			"operationId": input.operationID,
			"owner":       contract.owner,
			"surface":     input.surface,
		})
		if err != nil {
			return err
		}
		// The publication repository commits its constrained domain event before
		// this cross-domain Access audit. The caller observes recorder failures but
		// preserves the already-successful publication result.
		return record(ctx, access.AuditEventInput{
			WorkspaceID: input.workspaceID, PrincipalID: input.principalID,
			Action: contract.action, TargetType: "dashboard_publication", TargetID: input.targetID,
			Privilege: contract.privilege, Status: "success",
			RequestID: input.requestID, CorrelationID: input.correlationID,
			MetadataJSON: string(metadata),
		})
	}, nil
}

func publicationOperationID(action publication.Action) (string, bool) {
	switch action {
	case publication.ActionSuspend:
		return dashboardgen.GenOperationSuspendDashboardPublication, true
	case publication.ActionResume:
		return dashboardgen.GenOperationResumeDashboardPublication, true
	case publication.ActionRotate:
		return dashboardgen.GenOperationRotateDashboardPublication, true
	default:
		return "", false
	}
}

func publicationAuditRequestInput(r *http.Request, operationID, workspaceID, principalID, targetID string) publicationCommandAuditInput {
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
		operationID: operationID, workspaceID: strings.TrimSpace(workspaceID), principalID: strings.TrimSpace(principalID),
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
