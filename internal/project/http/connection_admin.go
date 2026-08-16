package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"strconv"
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/connectionadmin"
	"github.com/flidai/leapview/internal/analytics/connectors"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/flidai/leapview/internal/project"
	"github.com/flidai/leapview/internal/project/ui"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
)

type ConnectionPrivilegeAuthorizer func(context.Context, string, access.Privilege, string) (bool, error)

type connectionAdministrationPayload struct {
	ConnectionAdmin uisignals.ConnectionAdministrationSignal `json:"connectionAdmin"`
}

func (h Handler) connectionAdministrationView(r *nethttp.Request, assets []project.DevelopAssetView, edges []project.DevelopEdgeView, status uisignals.ConnectionAdministrationStatusSignal) ui.ConnectionAdministrationView {
	view := ui.ConnectionAdministrationView{
		Bindings:        map[string]ui.ConnectionBindingView{},
		RequiresBinding: map[string]bool{},
		Status:          status,
	}
	for _, asset := range assets {
		if asset.Type != string(project.AssetTypeConnection) {
			continue
		}
		logical := ui.ConnectionLogicalName(asset, assets, edges)
		kind := strings.TrimSpace(connectionAssetKind(asset))
		spec, ok := connectors.LookupConnection(kind)
		view.RequiresBinding[logical] = ok && spec.ActivationMode == connectors.TargetBindingActivation
	}
	if h.ConnectionAdministration == nil {
		return view
	}
	principal, ok := h.ReadModel.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return view
	}
	if principal.DevBypass {
		view.CanManage, view.CanTest = true, true
	} else if h.ConnectionAuthorize != nil {
		view.CanManage, _ = h.ConnectionAuthorize(r.Context(), principal.ID, access.PrivilegeManageConnectionMetadata, h.connectionProjectID())
		view.CanTest, _ = h.ConnectionAuthorize(r.Context(), principal.ID, access.PrivilegeTestConnection, h.connectionProjectID())
	}
	// Listing binding metadata is itself a management operation. Keep test-only
	// principals on the read-only surface until the API exposes a health list
	// that does not disclose endpoint metadata.
	if !view.CanManage {
		view.CanTest = false
		return view
	}
	bindings, err := h.ConnectionAdministration.List(r.Context(), principal.ID, h.connectionBindingScope(r), h.ConnectionTargetID)
	if err != nil {
		view.Status.Error = connectionAdministrationErrorMessage(err)
		return view
	}
	for _, binding := range bindings {
		view.Bindings[binding.LogicalConnectionID.String()] = connectionBindingView(binding)
	}
	return view
}

func connectionBindingView(binding connectionadmin.TargetBinding) ui.ConnectionBindingView {
	lastValidatedAt := ""
	if !binding.LastValidatedAt.IsZero() {
		lastValidatedAt = binding.LastValidatedAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	return ui.ConnectionBindingView{
		ID: binding.ID, LogicalConnection: binding.LogicalConnectionID.String(), ConnectorKind: binding.ConnectorKind,
		AuthenticationMode: string(binding.AuthenticationMode), Host: binding.Endpoint.Host, Port: binding.Endpoint.Port,
		Database: binding.Endpoint.Database, ObjectScope: binding.Endpoint.ObjectScope, SourceIdentity: binding.Endpoint.SourceIdentity,
		TLSMode: binding.Endpoint.TLSMode, Options: binding.Endpoint.Options,
		CredentialProjectID: binding.CredentialReference.ProjectID, CredentialEnvironment: binding.CredentialReference.Environment,
		SecretPath: binding.CredentialReference.SecretPath, SecretKey: binding.CredentialReference.SecretKey,
		Enabled: binding.Enabled, Health: string(binding.Health), DiagnosticCode: binding.HealthReason,
		ValidatedVersion: binding.ValidatedVersion, LastValidatedAt: lastValidatedAt, Revision: binding.Revision,
	}
}

func connectionAssetKind(asset project.DevelopAssetView) string {
	for _, key := range []string{"Kind", "kind", "Provider", "provider"} {
		if value, ok := asset.Payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (h Handler) ConnectionConfiguration(w nethttp.ResponseWriter, r *nethttp.Request) {
	payload, ok := readConnectionAdministrationPayload(w, r)
	if !ok {
		return
	}
	command := payload.ConnectionAdmin.Command
	assets, edges, asset, principalID, ok := h.connectionCommandContext(w, r, command)
	if !ok {
		return
	}
	configuration, err := connectionBindingConfiguration(command)
	if err == nil {
		kind := strings.TrimSpace(connectionAssetKind(asset))
		spec, supported := connectors.LookupConnection(kind)
		if !supported || spec.ActivationMode != connectors.TargetBindingActivation || configuration.ConnectorKind != kind {
			err = connectionadmin.ErrInvalidBinding
		}
	}
	if err != nil {
		h.patchConnectionAdministration(w, r, command, assets, edges, asset, uisignals.ConnectionAdministrationStatusSignal{Error: connectionAdministrationErrorMessage(err)}, false)
		return
	}
	status := uisignals.ConnectionAdministrationStatusSignal{}
	switch command.Action {
	case "create":
		if err := uicommand.VerifyClaim(uicommand.OperationClaims(r), h.ConnectionCommands.Create.OperationID()); err != nil {
			status.Error = "The configure command could not be verified."
			break
		}
		_, err = h.ConnectionAdministration.Create(r.Context(), principalID, connectionadmin.TargetBindingInput{
			ID: command.LogicalConnection, TargetID: h.ConnectionTargetID, LogicalConnectionID: command.LogicalConnection,
			ConnectorKind: configuration.ConnectorKind, AuthenticationMode: configuration.AuthenticationMode,
			Scope: h.connectionBindingScope(r), Endpoint: configuration.Endpoint,
			CredentialReference: configuration.CredentialReference, Enabled: true,
		})
		if err == nil {
			status.Message = "Connection configured. Test it before use."
		}
	case "update":
		if err := uicommand.VerifyClaim(uicommand.OperationClaims(r), h.ConnectionCommands.Update.OperationID()); err != nil {
			status.Error = "The update command could not be verified."
			break
		}
		key := h.connectionBindingKey(r, command.LogicalConnection)
		plan, planErr := h.ConnectionAdministration.PlanConfigurationChange(r.Context(), principalID, key, configuration)
		if planErr != nil {
			err = planErr
			break
		}
		if plan.RequiresConfirmation && command.ConfirmationToken != plan.ConfirmationToken {
			command.ConfirmationToken = plan.ConfirmationToken
			command.ExpectedRevision = plan.ExpectedRevision
			status.Error = fmt.Sprintf("This change affects %d dependent source(s). Review the configuration and confirm the update.", len(plan.Dependencies))
			h.patchConnectionAdministration(w, r, command, assets, edges, asset, status, true)
			return
		}
		_, err = h.ConnectionAdministration.UpdateConfiguration(r.Context(), connectionadmin.UpdateConfigurationRequest{
			ActorID: principalID, Key: key, Configuration: configuration,
			ExpectedRevision: command.ExpectedRevision, ConfirmationToken: command.ConfirmationToken,
		})
		if err == nil {
			status.Message = "Connection updated. Test it before use."
		}
	default:
		err = connectionadmin.ErrInvalidBinding
	}
	if err != nil {
		status.Error = connectionAdministrationErrorMessage(err)
	}
	h.patchConnectionAdministration(w, r, command, assets, edges, asset, status, false)
}

func (h Handler) ConnectionLifecycle(w nethttp.ResponseWriter, r *nethttp.Request) {
	payload, ok := readConnectionAdministrationPayload(w, r)
	if !ok {
		return
	}
	command := payload.ConnectionAdmin.Command
	assets, edges, asset, principalID, ok := h.connectionCommandContext(w, r, command)
	if !ok {
		return
	}
	key := h.connectionBindingKey(r, command.LogicalConnection)
	status := uisignals.ConnectionAdministrationStatusSignal{}
	var err error
	switch command.Action {
	case "test":
		err = verifyConnectionClaim(r, h.ConnectionCommands.Test)
		if err == nil {
			_, err = h.ConnectionAdministration.Test(r.Context(), principalID, key)
		}
		if err == nil {
			status.Message = "Connection test succeeded."
		}
	case "refresh":
		err = verifyConnectionClaim(r, h.ConnectionCommands.Refresh)
		if err == nil {
			_, err = h.ConnectionAdministration.RefreshNow(r.Context(), principalID, key)
		}
		if err == nil {
			status.Message = "Credentials refreshed."
		}
	case "enable":
		err = verifyConnectionClaim(r, h.ConnectionCommands.Enable)
		if err == nil {
			_, err = h.ConnectionAdministration.Enable(r.Context(), principalID, key)
		}
		if err == nil {
			status.Message = "Connection enabled. Test it before use."
		}
	case "disable":
		err = verifyConnectionClaim(r, h.ConnectionCommands.Disable)
		if err == nil {
			_, err = h.ConnectionAdministration.Disable(r.Context(), principalID, key)
		}
		if err == nil {
			status.Message = "Connection disabled."
		}
	default:
		err = connectionadmin.ErrInvalidBinding
	}
	if err != nil {
		status.Error = connectionAdministrationErrorMessage(err)
	}
	h.patchConnectionAdministration(w, r, command, assets, edges, asset, status, false)
}

func verifyConnectionClaim(r *nethttp.Request, binding uicommand.Binding) error {
	if err := uicommand.VerifyClaim(uicommand.OperationClaims(r), binding.OperationID()); err != nil {
		return connectionadmin.ErrUnauthorizedBinding
	}
	return nil
}

func readConnectionAdministrationPayload(w nethttp.ResponseWriter, r *nethttp.Request) (connectionAdministrationPayload, bool) {
	payload := connectionAdministrationPayload{}
	if err := pagestream.ReadSignals(r, &payload); err != nil {
		nethttp.Error(w, "Connection command is invalid.", nethttp.StatusBadRequest)
		return payload, false
	}
	return payload, true
}

func (h Handler) connectionCommandContext(w nethttp.ResponseWriter, r *nethttp.Request, command uisignals.ConnectionAdministrationCommandSignal) ([]project.DevelopAssetView, []project.DevelopEdgeView, project.DevelopAssetView, string, bool) {
	if h.ConnectionAdministration == nil {
		nethttp.Error(w, "Connection administration is unavailable.", nethttp.StatusServiceUnavailable)
		return nil, nil, project.DevelopAssetView{}, "", false
	}
	assets, edges, err := h.platformAssetsAndEdges(r)
	if err != nil {
		nethttp.Error(w, "Connection catalog is unavailable.", nethttp.StatusInternalServerError)
		return nil, nil, project.DevelopAssetView{}, "", false
	}
	asset, ok := project.AssetByID(assets, command.AssetID)
	if !ok || asset.Type != string(project.AssetTypeConnection) || ui.ConnectionLogicalName(asset, assets, edges) != strings.TrimSpace(command.LogicalConnection) {
		nethttp.Error(w, "Connection was not found.", nethttp.StatusNotFound)
		return nil, nil, project.DevelopAssetView{}, "", false
	}
	principal, ok := h.ReadModel.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		nethttp.Error(w, "Connection operation is forbidden.", nethttp.StatusForbidden)
		return nil, nil, project.DevelopAssetView{}, "", false
	}
	return assets, edges, asset, principal.ID, true
}

func connectionBindingConfiguration(command uisignals.ConnectionAdministrationCommandSignal) (connectionadmin.TargetBindingConfiguration, error) {
	port := 0
	if strings.TrimSpace(command.Port) != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(command.Port))
		if err != nil || parsed <= 0 || parsed > 65535 {
			return connectionadmin.TargetBindingConfiguration{}, fmt.Errorf("%w: port must be between 1 and 65535", connectionadmin.ErrInvalidBinding)
		}
		port = parsed
	}
	options := map[string]string{}
	if strings.TrimSpace(command.Options) != "" {
		if err := json.Unmarshal([]byte(command.Options), &options); err != nil {
			return connectionadmin.TargetBindingConfiguration{}, fmt.Errorf("%w: options must be a JSON object of string values", connectionadmin.ErrInvalidBinding)
		}
	}
	return connectionadmin.TargetBindingConfiguration{
		ConnectorKind:      strings.TrimSpace(command.ConnectorKind),
		AuthenticationMode: connectionadmin.AuthenticationMode(strings.TrimSpace(command.AuthenticationMode)),
		Endpoint: connectionadmin.EndpointConfig{
			Host: strings.TrimSpace(command.Host), Port: port, Database: strings.TrimSpace(command.Database),
			ObjectScope: strings.TrimSpace(command.ObjectScope), SourceIdentity: strings.TrimSpace(command.SourceIdentity),
			TLSMode: strings.TrimSpace(command.TLSMode), Options: options,
		},
		CredentialReference: connectionadmin.CredentialReference{
			ProjectID: strings.TrimSpace(command.CredentialProjectID), Environment: strings.TrimSpace(command.CredentialEnvironment),
			SecretPath: strings.TrimSpace(command.SecretPath), SecretKey: strings.TrimSpace(command.SecretKey),
		},
	}, nil
}

func (h Handler) patchConnectionAdministration(w nethttp.ResponseWriter, r *nethttp.Request, command uisignals.ConnectionAdministrationCommandSignal, assets []project.DevelopAssetView, edges []project.DevelopEdgeView, asset project.DevelopAssetView, status uisignals.ConnectionAdministrationStatusSignal, preserveCommand bool) {
	view := h.connectionAdministrationView(r, assets, edges, status)
	adminSignal := uisignals.ConnectionAdministrationSignal{Status: status}
	if preserveCommand {
		adminSignal.Command = command
	}
	patch := pagestream.SignalPatch{"connectionAdmin": adminSignal}
	if command.Surface == "list" {
		patch["page"] = map[string]any{"connections": ui.ConnectionsListResultsPatchWithAdministration(assets, edges, view)["page"].(map[string]any)["connections"]}
	} else {
		lifecycle := ui.ConnectionLifecycleForAsset(asset, assets, edges, view)
		patch["page"] = map[string]any{"connectionLifecycle": lifecycle}
	}
	_ = pagestream.PatchResponse(w, r, patch)
}

func (h Handler) connectionProjectID() string {
	if value := strings.TrimSpace(string(h.ActiveProjectID)); value != "" {
		return value
	}
	return "platform"
}

func (h Handler) connectionBindingScope(r *nethttp.Request) connectionadmin.BindingScope {
	return connectionadmin.BindingScope{ProjectID: h.connectionProjectID(), Environment: h.environment(r)}
}

func (h Handler) connectionBindingKey(r *nethttp.Request, logical string) connectionadmin.BindingKey {
	parsed, _ := connectionadmin.ParseLogicalConnectionID(strings.TrimSpace(logical))
	return connectionadmin.BindingKey{Scope: h.connectionBindingScope(r), TargetID: h.ConnectionTargetID, LogicalConnectionID: parsed}
}

func connectionAdministrationErrorMessage(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, connectionadmin.ErrUnauthorizedBinding):
		return "You do not have permission to administer this connection."
	case errors.Is(err, connectionadmin.ErrBindingNotFound):
		return "The connection is not configured for this environment."
	case errors.Is(err, connectionadmin.ErrDisabledBinding):
		return "Enable the connection before running this operation."
	case errors.Is(err, connectionadmin.ErrIncompatibleBinding):
		return "The connection changed while you were editing it. Reopen the configuration and try again."
	case errors.Is(err, connectionadmin.ErrCredentialDenied), errors.Is(err, connectionadmin.ErrCredentialNotFound), errors.Is(err, connectionadmin.ErrInvalidCredentialBundle):
		return "Connection validation failed. Check the credential reference and try again."
	case errors.Is(err, connectionadmin.ErrCredentialRateLimited), errors.Is(err, connectionadmin.ErrProviderUnavailable):
		return "The credential provider is temporarily unavailable. Try again shortly."
	case errors.Is(err, connectionadmin.ErrInvalidBinding):
		return "The connection configuration is invalid. Check the required fields and try again."
	default:
		return "The connection operation could not be completed."
	}
}
