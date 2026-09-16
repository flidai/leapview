package module

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type DevelopmentProfileApplicationAPIConfig struct {
	Service          *connectionbinding.ProfileApplicationService
	Store            connectionbinding.ProfileApplicationStore
	CurrentPrincipal func(*http.Request) (string, bool)
	Audit            func(context.Context, string, string, string, string) error
	Enabled          bool
	CheckoutID       string
	RuntimeID        string
	ProfileName      string
	GraphDigest      string
	ProfileDigest    string
	Environment      string
	TargetID         string
	ResolveProjectID func(context.Context) (projectgraph.ResourceID, error)
}

type developmentProfileApplicationAPIHandler struct {
	config DevelopmentProfileApplicationAPIConfig
}

func (handler developmentProfileApplicationAPIHandler) Get(w http.ResponseWriter, r *http.Request, project, target string) {
	_, scope, targetID, ok := handler.scope(w, r, project, target)
	if !ok {
		return
	}
	record, err := handler.config.Store.Application(r.Context(), scope, targetID)
	if err != nil {
		writeConnectionBindingError(w, r, err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, developmentProfileApplicationResponse(record))
}

func (handler developmentProfileApplicationAPIHandler) Apply(w http.ResponseWriter, r *http.Request, project, target string) {
	principalID, scope, targetID, ok := handler.scope(w, r, project, target)
	if !ok {
		return
	}
	var body analyticsgen.DevelopmentProfileApplicationRequest
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", "Request body is invalid", nil)
		return
	}
	if body.GraphDigest != handler.config.GraphDigest || body.ProfileDigest != handler.config.ProfileDigest {
		writeConnectionBindingCommandFailure(w, r, analyticsgen.GenCommandOperationApplyDevelopmentProfile(), connectionbinding.ErrProfileApplicationReplacement)
		return
	}
	connections := make([]connectionbinding.ProfileApplicationBinding, len(body.Connections))
	for index, input := range body.Connections {
		configuration := targetConnectionConfiguration(analyticsgen.TargetConnectionConfiguration{
			ConnectorKind: input.ConnectorKind, AuthenticationMode: input.AuthenticationMode,
			Endpoint: input.Endpoint, CredentialReference: input.CredentialReference,
		})
		connections[index] = connectionbinding.ProfileApplicationBinding{
			ConnectionID: projectgraph.ResourceID(input.LogicalConnection), ConnectorKind: configuration.ConnectorKind,
			AuthenticationMode: configuration.AuthenticationMode, Endpoint: configuration.Endpoint,
			CredentialReference: configuration.CredentialReference,
		}
	}
	computedProfileDigest, err := profileApplicationDigest(handler.config.ProfileName, connections)
	if err != nil || computedProfileDigest != body.ProfileDigest {
		writeConnectionBindingCommandFailure(w, r, analyticsgen.GenCommandOperationApplyDevelopmentProfile(), connectionbinding.ErrInvalidProfileApplication)
		return
	}
	commandContext, err := beginDevelopmentProfileCommand(r, project)
	if err != nil {
		writeConnectionBindingCommandFailure(w, r, analyticsgen.GenCommandOperationApplyDevelopmentProfile(), err)
		return
	}
	record, err := handler.config.Service.Apply(commandContext, connectionbinding.ProfileApplicationRequest{
		Mode: connectionbinding.ProfileApplicationMode(body.Mode), ApplicationID: connectionbinding.ProfileApplicationID(body.ApplicationId),
		CheckoutID: scope.CheckoutID, RuntimeID: scope.RuntimeID, TargetID: targetID,
		ProjectID: scope.ProjectID, Environment: scope.Environment, ProfileName: handler.config.ProfileName,
		SourceDigest: body.SourceDigest, GraphDigest: body.GraphDigest, ProfileDigest: body.ProfileDigest,
		ActorID: principalID, Connections: connections,
	})
	if err != nil {
		writeConnectionBindingCommandFailure(w, r, analyticsgen.GenCommandOperationApplyDevelopmentProfile(), err)
		return
	}
	if err := handler.completeAudit(commandContext, principalID, project, body); err != nil {
		writeConnectionBindingCommandFailure(w, r, analyticsgen.GenCommandOperationApplyDevelopmentProfile(), err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, developmentProfileApplicationResponse(record))
}

func profileApplicationDigest(profileName string, connections []connectionbinding.ProfileApplicationBinding) (string, error) {
	values := make([]connectionbinding.DevelopmentProfileDigestConnection, len(connections))
	for index, connection := range connections {
		value := connectionbinding.DevelopmentProfileDigestConnection{
			ConnectionID: connection.ConnectionID, ConnectorKind: connection.ConnectorKind, Endpoint: connection.Endpoint,
		}
		switch connection.AuthenticationMode {
		case connectionbinding.AuthenticationNone:
			value.Unauthenticated = true
		case connectionbinding.AuthenticationExternalBundle:
			value.CredentialVariable = connection.CredentialReference.SecretKey
		default:
			return "", connectionbinding.ErrInvalidProfileApplication
		}
		values[index] = value
	}
	return connectionbinding.DevelopmentProfileDigest(profileName, values)
}

func (handler developmentProfileApplicationAPIHandler) scope(w http.ResponseWriter, r *http.Request, project, target string) (string, connectionbinding.ProfileApplicationScope, connectionbinding.TargetID, bool) {
	if !handler.config.Enabled || handler.config.Service == nil || handler.config.Store == nil || handler.config.CurrentPrincipal == nil || handler.config.ResolveProjectID == nil || handler.config.CheckoutID == "" || handler.config.RuntimeID == "" {
		apitransport.WriteProblem(w, r, http.StatusNotFound, "LOCAL_PROFILE_APPLICATION_UNAVAILABLE", "Local profile application is unavailable", nil)
		return "", connectionbinding.ProfileApplicationScope{}, "", false
	}
	principal, authenticated := handler.config.CurrentPrincipal(r)
	principal = strings.TrimSpace(principal)
	if !authenticated || principal == "" {
		apitransport.WriteProblem(w, r, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "Bearer authentication is required", nil)
		return "", connectionbinding.ProfileApplicationScope{}, "", false
	}
	projectID := projectgraph.ResourceID(strings.TrimSpace(project))
	targetID := connectionbinding.TargetID(strings.TrimSpace(target))
	expectedProject, err := handler.config.ResolveProjectID(r.Context())
	if err != nil || projectID.Validate() != nil || projectID != expectedProject || targetID.String() == "" || targetID.String() != handler.config.TargetID || handler.config.Environment == "" {
		writeConnectionBindingError(w, r, connectionbinding.ErrInvalidProfileApplication)
		return "", connectionbinding.ProfileApplicationScope{}, "", false
	}
	return principal, connectionbinding.ProfileApplicationScope{
		CheckoutID: handler.config.CheckoutID, RuntimeID: handler.config.RuntimeID,
		ProjectID: projectID, Environment: handler.config.Environment,
	}, targetID, true
}

func beginDevelopmentProfileCommand(r *http.Request, project string) (context.Context, error) {
	if operationID, started := apigencommand.OperationID(r.Context()); started {
		if operationID != string(analyticsgen.GenOperationApplyDevelopmentProfile) {
			return r.Context(), fmt.Errorf("profile application command operation mismatch")
		}
		return r.Context(), nil
	}
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = requestID
	}
	started, _, err := analyticsgen.BeginGenApplyDevelopmentProfileCommand(r.Context(), analyticsgen.GenApplyDevelopmentProfileCommandInvocation{
		Surface: apigencommand.SurfaceAPI, Project: project,
		IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")), RequestID: requestID, CorrelationID: correlationID,
	})
	return started, err
}

func (handler developmentProfileApplicationAPIHandler) completeAudit(ctx context.Context, principalID, project string, body analyticsgen.DevelopmentProfileApplicationRequest) error {
	executor, err := apigencommand.NewExecutor(analyticsgen.GetAPIGenCommandRuntimeContract, slog.Default())
	if err != nil {
		return err
	}
	return executor.Execute(ctx, string(analyticsgen.GenOperationApplyDevelopmentProfile), apigencommand.Execution{
		BestEffortAudit: func(ctx context.Context, contract apigencommand.Contract) error {
			metadata, err := analyticsgen.EncodeGenApplyDevelopmentProfileAuditPayload(analyticsgen.GenSchemaDevelopmentProfileApplicationAuditPayload{
				ApplicationId: body.ApplicationId, Mode: body.Mode, ConnectionCount: int32(len(body.Connections)),
			})
			if err != nil {
				return err
			}
			if handler.config.Audit == nil {
				return errors.New("development profile audit is unavailable")
			}
			return handler.config.Audit(ctx, principalID, project, contract.AuditAction, metadata)
		},
		LogMessage: "development profile application audit failed",
	})
}

func developmentProfileApplicationResponse(record connectionbinding.ProfileApplicationRecord) analyticsgen.DevelopmentProfileApplicationResponse {
	applied := make(map[string]struct{}, len(record.AppliedConnections))
	for _, connection := range record.AppliedConnections {
		applied[connection.ConnectionID.String()] = struct{}{}
	}
	incomplete := make([]string, 0, len(record.RequiredConnections))
	for _, connection := range record.RequiredConnections {
		if _, ok := applied[connection.ConnectionID.String()]; !ok {
			incomplete = append(incomplete, connection.ConnectionID.String())
		}
	}
	response := analyticsgen.DevelopmentProfileApplicationResponse{
		ApplicationId: record.ID.String(), Status: analyticsgen.DevelopmentProfileApplicationStatus(record.Status),
		ProfileName:             record.ProfileName,
		RequiredConnectionCount: int32(len(record.RequiredConnections)), AppliedConnectionCount: int32(len(record.AppliedConnections)),
		IncompleteConnections: incomplete,
		SourceDigest:          record.SourceDigest, GraphDigest: record.GraphDigest, ProfileDigest: record.ProfileDigest,
		Revision: record.Revision, UpdatedAt: record.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}
	lastCompletedID, lastCompletedAt := record.LastCompletedApplicationID, record.LastCompletedAt
	// Non-durable test/fallback stores may not materialize the derived fields;
	// a currently applied record is itself authoritative completion evidence.
	if lastCompletedID == "" && record.Status == connectionbinding.ProfileApplicationApplied {
		lastCompletedID, lastCompletedAt = record.ID, record.UpdatedAt
	}
	if lastCompletedID != "" && !lastCompletedAt.IsZero() {
		applicationID := lastCompletedID.String()
		completedAt := lastCompletedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
		response.LastCompletedApplicationId = &applicationID
		response.LastCompletedAt = &completedAt
	}
	return response
}
