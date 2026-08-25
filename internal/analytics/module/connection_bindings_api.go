package module

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type ConnectionBindingAdministration interface {
	List(context.Context, string, connectionbinding.BindingScope, connectionbinding.TargetID) ([]connectionbinding.TargetBinding, error)
	Create(context.Context, string, connectionbinding.TargetBindingInput) (connectionbinding.TargetBinding, error)
	Get(context.Context, string, connectionbinding.BindingKey) (connectionbinding.TargetBinding, error)
	PlanConfigurationChange(context.Context, string, connectionbinding.BindingKey, connectionbinding.TargetBindingConfiguration) (connectionbinding.BindingChangePlan, error)
	UpdateConfiguration(context.Context, connectionbinding.UpdateConfigurationRequest) (connectionbinding.TargetBinding, error)
	Test(context.Context, string, connectionbinding.BindingKey) (connectionbinding.BindingHealthStatus, error)
	RefreshNow(context.Context, string, connectionbinding.BindingKey) (connectionbinding.BindingHealthStatus, error)
	Enable(context.Context, string, connectionbinding.BindingKey) (connectionbinding.TargetBinding, error)
	Disable(context.Context, string, connectionbinding.BindingKey) (connectionbinding.TargetBinding, error)
	Health(context.Context, string, connectionbinding.BindingKey) (connectionbinding.BindingHealthStatus, error)
}

type ConnectionBindingAPIGenConfig struct {
	Administration   ConnectionBindingAdministration
	CurrentPrincipal func(*http.Request) (string, bool)
	Environment      string
}

type connectionBindingAPIHandler struct {
	config ConnectionBindingAPIGenConfig
}

func (handler connectionBindingAPIHandler) List(
	w http.ResponseWriter,
	r *http.Request,
	project, target string,
) {
	principalID, ok := handler.principal(w, r)
	if !ok {
		return
	}
	bindings, err := handler.config.Administration.List(
		r.Context(),
		principalID,
		connectionbinding.BindingScope{ProjectID: projectgraph.ResourceID(project), Environment: handler.config.Environment},
		connectionbinding.TargetID(target),
	)
	if err != nil {
		writeConnectionBindingError(w, r, err)
		return
	}
	items := make([]analyticsgen.TargetConnectionBindingResponse, 0, len(bindings))
	for _, binding := range bindings {
		items = append(items, targetConnectionBindingResponse(binding))
	}
	apitransport.WriteJSON(w, http.StatusOK, analyticsgen.TargetConnectionBindingListResponse{Items: items})
}

func (handler connectionBindingAPIHandler) Create(
	w http.ResponseWriter,
	r *http.Request,
	project, target string,
) {
	principalID, ok := handler.principal(w, r)
	if !ok {
		return
	}
	var body analyticsgen.TargetConnectionBindingCreateRequest
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", "Request body is invalid", nil)
		return
	}
	configuration := targetConnectionConfiguration(body.Configuration)
	commandContext, err := connectionAdministrationCommandContext(r, "createTargetConnectionBinding", principalID, connectionbinding.AdministrationAuditEvent{
		ProjectID: projectgraph.ResourceID(project), BindingID: connectionbinding.BindingID(body.Id),
		TargetID: connectionbinding.TargetID(target), ConnectionID: projectgraph.ResourceID(body.LogicalConnection),
		Actor: principalID, Action: connectionbinding.AuditBindingCreated,
		Outcome: connectionbinding.AdministrationAuditSucceeded, Revision: 1,
	})
	if err != nil {
		writeConnectionBindingCommandFailure(w, r, analyticsgen.GenCommandOperationCreateTargetConnectionBinding(), err)
		return
	}
	binding, err := handler.config.Administration.Create(commandContext, principalID, connectionbinding.TargetBindingInput{
		ID: connectionbinding.BindingID(body.Id), TargetID: connectionbinding.TargetID(target), ConnectionID: projectgraph.ResourceID(body.LogicalConnection),
		ConnectorKind: configuration.ConnectorKind, AuthenticationMode: configuration.AuthenticationMode,
		Scope:    connectionbinding.BindingScope{ProjectID: projectgraph.ResourceID(project), Environment: handler.config.Environment},
		Endpoint: configuration.Endpoint, CredentialReference: configuration.CredentialReference,
		Enabled: body.Enabled,
	})
	if err != nil {
		writeConnectionBindingCommandFailure(w, r, analyticsgen.GenCommandOperationCreateTargetConnectionBinding(), err)
		return
	}
	if err := completeConnectionBindingCommand(commandContext, analyticsgen.GenCommandOperationCreateTargetConnectionBinding()); err != nil {
		writeConnectionBindingCommandFailure(w, r, analyticsgen.GenCommandOperationCreateTargetConnectionBinding(), err)
		return
	}
	w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/")+"/"+binding.ConnectionID.String())
	apitransport.WriteJSON(w, http.StatusCreated, targetConnectionBindingResponse(binding))
}

func (handler connectionBindingAPIHandler) Get(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
) {
	principalID, key, ok := handler.requestScope(w, r, project, target, handler.config.Environment, connection)
	if !ok {
		return
	}
	binding, err := handler.config.Administration.Get(r.Context(), principalID, key)
	if err != nil {
		writeConnectionBindingError(w, r, err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, targetConnectionBindingResponse(binding))
}

func (handler connectionBindingAPIHandler) Plan(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
) {
	principalID, key, ok := handler.requestScope(w, r, project, target, handler.config.Environment, connection)
	if !ok {
		return
	}
	var body analyticsgen.TargetConnectionBindingPlanRequest
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", "Request body is invalid", nil)
		return
	}
	plan, err := handler.config.Administration.PlanConfigurationChange(
		r.Context(),
		principalID,
		key,
		targetConnectionConfiguration(body.Configuration),
	)
	if err != nil {
		writeConnectionBindingError(w, r, err)
		return
	}
	dependencies := make([]analyticsgen.TargetConnectionDependency, 0, len(plan.Dependencies))
	for _, dependency := range plan.Dependencies {
		dependencies = append(dependencies, analyticsgen.TargetConnectionDependency{
			Kind: dependency.Kind, Id: dependency.ID, Label: dependency.Label,
		})
	}
	apitransport.WriteJSON(w, http.StatusOK, analyticsgen.TargetConnectionBindingChangePlanResponse{
		BindingId: plan.BindingID.String(), ExpectedRevision: plan.ExpectedRevision,
		RequiresConfirmation: plan.RequiresConfirmation,
		ConfirmationToken:    optionalString(plan.ConfirmationToken),
		Dependencies:         dependencies,
	})
}

func (handler connectionBindingAPIHandler) Update(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
) {
	principalID, key, ok := handler.requestScope(w, r, project, target, handler.config.Environment, connection)
	if !ok {
		return
	}
	var body analyticsgen.TargetConnectionBindingUpdateRequest
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", "Request body is invalid", nil)
		return
	}
	current, err := handler.config.Administration.Get(r.Context(), principalID, key)
	if err != nil {
		writeConnectionBindingCommandFailure(w, r, analyticsgen.GenCommandOperationUpdateTargetConnectionBinding(), err)
		return
	}
	commandContext, err := connectionAdministrationCommandContext(r, "updateTargetConnectionBinding", principalID, connectionbinding.AdministrationAuditEvent{
		ProjectID: current.Scope.ProjectID, BindingID: current.ID, TargetID: current.TargetID,
		ConnectionID: current.ConnectionID, Actor: principalID, Action: connectionbinding.AuditBindingUpdated,
		Outcome: connectionbinding.AdministrationAuditSucceeded, Revision: current.Revision + 1,
	})
	if err != nil {
		writeConnectionBindingCommandFailure(w, r, analyticsgen.GenCommandOperationUpdateTargetConnectionBinding(), err)
		return
	}
	binding, err := handler.config.Administration.UpdateConfiguration(
		commandContext,
		connectionbinding.UpdateConfigurationRequest{
			ActorID: principalID, Key: key,
			Configuration:    targetConnectionConfiguration(body.Configuration),
			ExpectedRevision: body.ExpectedRevision, ConfirmationToken: valueOrEmpty(body.ConfirmationToken),
		},
	)
	if err != nil {
		writeConnectionBindingCommandFailure(w, r, analyticsgen.GenCommandOperationUpdateTargetConnectionBinding(), err)
		return
	}
	if err := completeConnectionBindingCommand(commandContext, analyticsgen.GenCommandOperationUpdateTargetConnectionBinding()); err != nil {
		writeConnectionBindingCommandFailure(w, r, analyticsgen.GenCommandOperationUpdateTargetConnectionBinding(), err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, targetConnectionBindingResponse(binding))
}

func completeConnectionBindingCommand(ctx context.Context, operationID analyticsgen.GenCommandOperationID) error {
	executor, err := apigencommand.NewExecutor(analyticsgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	return executor.Execute(ctx, operationID.APIGenOperationID(), apigencommand.Execution{
		Transactional: func(context.Context, apigencommand.Contract) error { return nil },
	})
}

func (handler connectionBindingAPIHandler) Test(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
) {
	principalID, key, ok := handler.requestScope(w, r, project, target, handler.config.Environment, connection)
	if !ok {
		return
	}
	status, err := handler.config.Administration.Test(r.Context(), principalID, key)
	if err != nil {
		writeConnectionBindingCommandFailure(w, r, analyticsgen.GenCommandOperationTestTargetConnectionBinding(), err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, targetConnectionHealthResponse(status))
}

func (handler connectionBindingAPIHandler) Refresh(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
) {
	principalID, key, ok := handler.requestScope(w, r, project, target, handler.config.Environment, connection)
	if !ok {
		return
	}
	status, err := handler.config.Administration.RefreshNow(r.Context(), principalID, key)
	if err != nil {
		writeConnectionBindingCommandFailure(w, r, analyticsgen.GenCommandOperationRefreshTargetConnectionBinding(), err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, targetConnectionHealthResponse(status))
}

func (handler connectionBindingAPIHandler) Enable(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
) {
	handler.setEnabled(w, r, project, target, connection, true)
}

func (handler connectionBindingAPIHandler) Disable(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
) {
	handler.setEnabled(w, r, project, target, connection, false)
}

func (handler connectionBindingAPIHandler) setEnabled(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
	enabled bool,
) {
	principalID, key, ok := handler.requestScope(w, r, project, target, handler.config.Environment, connection)
	if !ok {
		return
	}
	current, err := handler.config.Administration.Get(r.Context(), principalID, key)
	if err != nil {
		operationID := analyticsgen.GenCommandOperationDisableTargetConnectionBinding()
		if enabled {
			operationID = analyticsgen.GenCommandOperationEnableTargetConnectionBinding()
		}
		writeConnectionBindingCommandFailure(w, r, operationID, err)
		return
	}
	action, operationID := connectionbinding.AuditBindingDisabled, "disableTargetConnectionBinding"
	generatedOperationID := analyticsgen.GenCommandOperationDisableTargetConnectionBinding()
	if enabled {
		action, operationID = connectionbinding.AuditBindingEnabled, "enableTargetConnectionBinding"
		generatedOperationID = analyticsgen.GenCommandOperationEnableTargetConnectionBinding()
	}
	commandContext, err := connectionAdministrationCommandContext(r, operationID, principalID, connectionbinding.AdministrationAuditEvent{
		ProjectID: current.Scope.ProjectID, BindingID: current.ID, TargetID: current.TargetID,
		ConnectionID: current.ConnectionID, Actor: principalID, Action: action,
		Outcome: connectionbinding.AdministrationAuditSucceeded, Revision: current.Revision + 1,
	})
	if err != nil {
		writeConnectionBindingCommandFailure(w, r, generatedOperationID, err)
		return
	}
	var binding connectionbinding.TargetBinding
	if enabled {
		binding, err = handler.config.Administration.Enable(commandContext, principalID, key)
	} else {
		binding, err = handler.config.Administration.Disable(commandContext, principalID, key)
	}
	if err != nil {
		operationID := analyticsgen.GenCommandOperationDisableTargetConnectionBinding()
		if enabled {
			operationID = analyticsgen.GenCommandOperationEnableTargetConnectionBinding()
		}
		writeConnectionBindingCommandFailure(w, r, operationID, err)
		return
	}
	if err := completeConnectionBindingCommand(commandContext, generatedOperationID); err != nil {
		writeConnectionBindingCommandFailure(w, r, generatedOperationID, err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, targetConnectionBindingResponse(binding))
}

func connectionAdministrationCommandContext(
	r *http.Request,
	operationID, principalID string,
	event connectionbinding.AdministrationAuditEvent,
) (context.Context, error) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = strings.TrimSpace(r.Header.Get("X-Request-Id"))
	}
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = strings.TrimSpace(r.Header.Get("X-Correlation-Id"))
	}
	if correlationID == "" {
		correlationID = requestID
	}
	intent, err := connectionbinding.BuildConnectionAdministrationAuditIntent(connectionbinding.AdministrationAuditInvocation{
		OperationID: operationID, PrincipalID: principalID,
		RequestID: requestID, CorrelationID: correlationID,
		IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")),
	}, event)
	if err != nil {
		return r.Context(), err
	}
	return connectionbinding.WithAuditIntent(r.Context(), intent), nil
}

func (handler connectionBindingAPIHandler) Health(
	w http.ResponseWriter,
	r *http.Request,
	project, target, connection string,
) {
	principalID, key, ok := handler.requestScope(w, r, project, target, handler.config.Environment, connection)
	if !ok {
		return
	}
	status, err := handler.config.Administration.Health(r.Context(), principalID, key)
	if err != nil {
		writeConnectionBindingError(w, r, err)
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, targetConnectionHealthResponse(status))
}

func (handler connectionBindingAPIHandler) principal(
	w http.ResponseWriter,
	r *http.Request,
) (string, bool) {
	if handler.config.Administration == nil {
		apitransport.WriteProblem(
			w, r, http.StatusServiceUnavailable,
			"CONNECTION_ADMINISTRATION_UNAVAILABLE",
			"Connection administration is unavailable",
			nil,
		)
		return "", false
	}
	if handler.config.CurrentPrincipal == nil {
		apitransport.WriteProblem(
			w, r, http.StatusUnauthorized,
			"AUTHENTICATION_REQUIRED",
			"Bearer authentication is required",
			nil,
		)
		return "", false
	}
	principalID, ok := handler.config.CurrentPrincipal(r)
	principalID = strings.TrimSpace(principalID)
	if !ok || principalID == "" {
		apitransport.WriteProblem(
			w, r, http.StatusUnauthorized,
			"AUTHENTICATION_REQUIRED",
			"Bearer authentication is required",
			nil,
		)
		return "", false
	}
	return principalID, true
}

func (handler connectionBindingAPIHandler) requestScope(
	w http.ResponseWriter,
	r *http.Request,
	project, target, environment, connection string,
) (string, connectionbinding.BindingKey, bool) {
	principalID, ok := handler.principal(w, r)
	if !ok {
		return "", connectionbinding.BindingKey{}, false
	}
	connectionID, err := connectionbinding.ParseConnectionID(connection)
	if err != nil {
		writeConnectionBindingError(w, r, err)
		return "", connectionbinding.BindingKey{}, false
	}
	return principalID, connectionbinding.BindingKey{
		Scope: connectionbinding.BindingScope{
			ProjectID:   projectgraph.ResourceID(strings.TrimSpace(project)),
			Environment: strings.TrimSpace(environment),
		},
		TargetID: connectionbinding.TargetID(strings.TrimSpace(target)), ConnectionID: connectionID,
	}, true
}

func targetConnectionConfiguration(
	input analyticsgen.TargetConnectionConfiguration,
) connectionbinding.TargetBindingConfiguration {
	endpoint := connectionbinding.EndpointConfig{
		Host: valueOrEmpty(input.Endpoint.Host), Port: int(valueOrZero(input.Endpoint.Port)),
		Database:       valueOrEmpty(input.Endpoint.Database),
		ObjectScope:    valueOrEmpty(input.Endpoint.ObjectScope),
		SourceIdentity: valueOrEmpty(input.Endpoint.SourceIdentity),
		TLSMode:        valueOrEmpty(input.Endpoint.TlsMode),
	}
	if input.Endpoint.Options != nil {
		endpoint.Options = make(map[string]string, len(*input.Endpoint.Options))
		for key, value := range *input.Endpoint.Options {
			endpoint.Options[key] = value
		}
	}
	configuration := connectionbinding.TargetBindingConfiguration{
		ConnectorKind:      strings.TrimSpace(input.ConnectorKind),
		AuthenticationMode: connectionbinding.AuthenticationMode(input.AuthenticationMode),
		Endpoint:           endpoint,
	}
	if input.CredentialReference != nil {
		configuration.CredentialReference = connectionbinding.CredentialReference{
			ProjectID:   projectgraph.ResourceID(input.CredentialReference.ProjectId),
			Environment: input.CredentialReference.Environment,
			SecretPath:  input.CredentialReference.SecretPath,
			SecretKey:   input.CredentialReference.SecretKey,
		}
	}
	return configuration
}

func targetConnectionBindingResponse(
	binding connectionbinding.TargetBinding,
) analyticsgen.TargetConnectionBindingResponse {
	// Credential references are write-only browser inputs. The durable
	// binding retains them for provider resolution, but command responses and
	// replays must never echo the project/path/key back to a caller.
	response := analyticsgen.TargetConnectionBindingResponse{
		Id: binding.ID.String(), TargetId: binding.TargetID.String(),
		LogicalConnection:  binding.ConnectionID.String(),
		ConnectorKind:      binding.ConnectorKind,
		AuthenticationMode: analyticsgen.TargetConnectionAuthenticationMode(binding.AuthenticationMode),
		Environment:        binding.Scope.Environment,
		Endpoint:           targetConnectionEndpointResponse(binding.Endpoint),
		Enabled:            binding.Enabled, Health: analyticsgen.TargetConnectionHealth(binding.Health),
		CreatedAt:        binding.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:        binding.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Revision:         binding.Revision,
		ValidatedVersion: optionalString(binding.ValidatedVersion),
		LastValidatedAt:  optionalTime(binding.LastValidatedAt),
	}
	return response
}

func targetConnectionEndpointResponse(
	endpoint connectionbinding.EndpointConfig,
) analyticsgen.TargetConnectionEndpoint {
	response := analyticsgen.TargetConnectionEndpoint{
		Host:           optionalString(endpoint.Host),
		Database:       optionalString(endpoint.Database),
		ObjectScope:    optionalString(endpoint.ObjectScope),
		SourceIdentity: optionalString(endpoint.SourceIdentity),
		TlsMode:        optionalString(endpoint.TLSMode),
	}
	if endpoint.Port != 0 {
		port := int32(endpoint.Port)
		response.Port = &port
	}
	if len(endpoint.Options) > 0 {
		options := make(map[string]string, len(endpoint.Options))
		for key, value := range endpoint.Options {
			options[key] = value
		}
		response.Options = &options
	}
	return response
}

func targetConnectionHealthResponse(
	status connectionbinding.BindingHealthStatus,
) analyticsgen.TargetConnectionBindingHealthResponse {
	response := analyticsgen.TargetConnectionBindingHealthResponse{
		BindingId: status.BindingID.String(), TargetId: status.TargetID.String(),
		LogicalConnection: status.ConnectionID.String(),
		ConnectorKind:     status.ConnectorKind,
		Environment:       status.Scope.Environment,
		BindingRevision:   status.BindingRevision,
		ValidatedVersion:  optionalString(status.ValidatedVersion),
		Health:            analyticsgen.TargetConnectionHealth(status.Health),
		DiagnosticCode:    optionalString(status.DiagnosticCode),
		LastAttemptAt:     optionalTime(status.LastAttemptAt),
		LastValidatedAt:   optionalTime(status.LastValidatedAt),
		HasActivePool:     status.HasActivePool,
	}
	if status.StaleAgeSeconds > 0 {
		response.StaleAgeSeconds = &status.StaleAgeSeconds
	}
	return response
}

func writeConnectionBindingError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, detail := http.StatusInternalServerError, "INTERNAL_ERROR", "Connection operation failed"
	switch {
	case errors.Is(err, connectionbinding.ErrInvalidBinding):
		status, code, detail = http.StatusBadRequest, "INVALID_CONNECTION_BINDING", "Connection binding is invalid"
	case errors.Is(err, connectionbinding.ErrUnauthorizedBinding):
		status, code, detail = http.StatusForbidden, "FORBIDDEN", "Connection operation is forbidden"
	case errors.Is(err, connectionbinding.ErrBindingNotFound):
		status, code, detail = http.StatusNotFound, "CONNECTION_BINDING_NOT_FOUND", "Connection binding was not found"
	case errors.Is(err, connectionbinding.ErrConfirmationRequired):
		status, code, detail = http.StatusPreconditionFailed, "CONFIRMATION_REQUIRED", "Dependency confirmation is required"
	case errors.Is(err, connectionbinding.ErrIncompatibleBinding):
		status, code, detail = http.StatusConflict, "CONNECTION_BINDING_CONFLICT", "Connection binding changed concurrently or is incompatible"
	case errors.Is(err, connectionbinding.ErrDisabledBinding):
		status, code, detail = http.StatusConflict, "CONNECTION_BINDING_DISABLED", "Connection binding is disabled"
	case errors.Is(err, connectionbinding.ErrCredentialDenied),
		errors.Is(err, connectionbinding.ErrCredentialNotFound),
		errors.Is(err, connectionbinding.ErrInvalidCredentialBundle):
		status, code, detail = http.StatusUnprocessableEntity, "CONNECTION_TEST_FAILED", "Connection validation failed"
	case errors.Is(err, connectionbinding.ErrCredentialRateLimited),
		errors.Is(err, connectionbinding.ErrProviderUnavailable),
		errors.Is(err, connectionbinding.ErrRotationAuditUnavailable),
		errors.Is(err, context.DeadlineExceeded):
		status, code, detail = http.StatusServiceUnavailable, "CONNECTION_PROVIDER_UNAVAILABLE", "Connection provider is unavailable"
	}
	apitransport.WriteProblem(w, r, status, code, detail, nil)
}

func writeConnectionBindingCommandFailure(w http.ResponseWriter, r *http.Request, operationID analyticsgen.GenCommandOperationID, err error) {
	err = classifyConnectionBindingCommandFailure(err)
	apitransport.WriteAPIGenCommandFailure(r.Context(), w, r, nil, operationID, analyticsgen.GetAPIGenCommandFailureContracts, err)
}

func classifyConnectionBindingCommandFailure(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return apigenfailure.Wrap("provider_unavailable", err)
	}
	return err
}

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func optionalTime(value time.Time) *string {
	if value.IsZero() {
		return nil
	}
	encoded := value.UTC().Format(time.RFC3339Nano)
	return &encoded
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func valueOrZero(value *int32) int32 {
	if value == nil {
		return 0
	}
	return *value
}
