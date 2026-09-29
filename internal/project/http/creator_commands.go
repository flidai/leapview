package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/connectionadmin"
	uicommand "github.com/flidai/leapview/internal/platform/web/uicommand"
	projectview "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectui "github.com/flidai/leapview/internal/project/ui"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	"github.com/flidai/leapview/pkg/pagestream"
)

type creatorConnectionCommand struct {
	Command projectsignals.ConnectionAdministrationCommandSignal `json:"connectionAdmin"`
}

type creatorPipelineCommand struct {
	Command projectsignals.PipelineCommandSignal `json:"pipelineCommand"`
}

func (h *BrowserHandler) ConnectionAdministrationConfigurationCommand(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var payload creatorConnectionCommand
	if err := pagestream.ReadSignals(r, &payload); err != nil {
		stdhttp.Error(w, "connection administration command payload is required", stdhttp.StatusBadRequest)
		return
	}
	command := payload.Command
	operation := h.connectionOperation(command.Action)
	if operation == "" || uicommand.VerifyClaim(uicommand.OperationClaims(r), operation) != nil {
		h.connectionCommandPatch(w, r, command, "The connection command is invalid.")
		return
	}
	if command.Action != "create" && command.Action != "update" {
		h.connectionCommandPatch(w, r, command, "The connection command is invalid.")
		return
	}
	principal, ok := h.currentPrincipal(r)
	if !ok || h.ConnectionAdministration == nil {
		h.connectionCommandPatch(w, r, command, "Connection administration is unavailable.")
		return
	}
	projectID, assets, edges, ok := h.assets(w, r)
	if !ok {
		return
	}
	assets, err := h.projectAssetReadModels(r.Context(), assets)
	if err != nil {
		h.connectionCommandPatch(w, r, command, "Connection administration is unavailable.")
		return
	}
	if command.Action == "create" {
		if !h.connectionCreateAllowed(r, projectID) {
			h.connectionCommandPatch(w, r, command, "Connection operation is forbidden.")
			return
		}
	}
	connectionID := resolveConnectionID(command, assets)
	if connectionID == "" {
		h.connectionCommandPatch(w, r, command, "Connection was not found.")
		return
	}
	if command.Action == "update" {
		if !h.connectionMutationAllowed(r, connectionID, access.CapabilityResourceManage) {
			h.connectionCommandPatch(w, r, command, "Connection operation is forbidden.")
			return
		}
	}
	started, beginErr := h.beginConnectionInvocation(r, command.Action, projectID.String(), connectionID, command.ExpectedRevision)
	if beginErr != nil {
		h.connectionCommandPatch(w, r, command, "The connection command is invalid.")
		return
	}
	r = started
	configuration, err := connectionConfiguration(command)
	if err != nil {
		h.connectionCommandPatch(w, r, command, "Connection configuration is invalid.")
		return
	}
	scope := connectionadmin.BindingScope{ProjectID: projectID, Environment: h.Environment}
	target := connectionadmin.TargetID(strings.TrimSpace(h.TargetID))
	if target == "" {
		h.connectionCommandPatch(w, r, command, "Connection target is unavailable.")
		return
	}
	var operationErr error
	if command.Action == "create" {
		bindingID := connectionadmin.BindingID("binding_" + strings.ReplaceAll(target.String()+"_"+connectionID, " ", "_"))
		_, operationErr = h.ConnectionAdministration.Create(r.Context(), principal.ID, connectionadmin.TargetBindingInput{
			ID: bindingID, TargetID: target, ConnectionID: projectgraph.ResourceID(connectionID),
			ConnectorKind: configuration.ConnectorKind, AuthenticationMode: configuration.AuthenticationMode,
			Scope: scope, Endpoint: configuration.Endpoint, CredentialReference: configuration.CredentialReference, Enabled: true,
		})
	} else {
		_, operationErr = h.ConnectionAdministration.UpdateConfiguration(r.Context(), connectionadmin.UpdateConfigurationRequest{
			ActorID: principal.ID, Key: connectionadmin.BindingKey{Scope: scope, TargetID: target, ConnectionID: projectgraph.ResourceID(connectionID)},
			Configuration: configuration, ExpectedRevision: command.ExpectedRevision, ConfirmationToken: command.ConfirmationToken,
		})
	}
	if operationErr != nil {
		message := publicConnectionError(operationErr)
		h.connectionCommandPatch(w, r, command, message)
		return
	}
	message := "Connection configuration saved."
	h.connectionCommandSuccess(w, r, command, projectID, assets, edges, message)
}

func (h *BrowserHandler) ConnectionAdministrationLifecycleCommand(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var payload creatorConnectionCommand
	if err := pagestream.ReadSignals(r, &payload); err != nil {
		stdhttp.Error(w, "connection administration command payload is required", stdhttp.StatusBadRequest)
		return
	}
	command := payload.Command
	operation := h.connectionOperation(command.Action)
	if operation == "" || uicommand.VerifyClaim(uicommand.OperationClaims(r), operation) != nil {
		h.connectionCommandPatch(w, r, command, "The connection command is invalid.")
		return
	}
	if command.Action != "test" && command.Action != "refresh" && command.Action != "enable" && command.Action != "disable" {
		h.connectionCommandPatch(w, r, command, "The connection command is invalid.")
		return
	}
	principal, ok := h.currentPrincipal(r)
	if !ok || h.ConnectionAdministration == nil {
		h.connectionCommandPatch(w, r, command, "Connection administration is unavailable.")
		return
	}
	projectID, assets, edges, ok := h.assets(w, r)
	if !ok {
		return
	}
	assets, err := h.projectAssetReadModels(r.Context(), assets)
	if err != nil {
		h.connectionCommandPatch(w, r, command, "Connection administration is unavailable.")
		return
	}
	connectionID := resolveConnectionID(command, assets)
	target := connectionadmin.TargetID(strings.TrimSpace(h.TargetID))
	if connectionID == "" || target == "" {
		h.connectionCommandPatch(w, r, command, "Connection binding was not found.")
		return
	}
	if !h.connectionMutationAllowed(r, connectionID, access.CapabilityResourceManage) {
		h.connectionCommandPatch(w, r, command, "Connection operation is forbidden.")
		return
	}
	started, beginErr := h.beginConnectionInvocation(r, command.Action, projectID.String(), connectionID, command.ExpectedRevision)
	if beginErr != nil {
		h.connectionCommandPatch(w, r, command, "The connection command is invalid.")
		return
	}
	r = started
	key := connectionadmin.BindingKey{Scope: connectionadmin.BindingScope{ProjectID: projectID, Environment: h.Environment}, TargetID: target, ConnectionID: projectgraph.ResourceID(connectionID)}
	var operationErr error
	switch command.Action {
	case "test":
		_, operationErr = h.ConnectionAdministration.Test(r.Context(), principal.ID, key)
	case "refresh":
		_, operationErr = h.ConnectionAdministration.RefreshNow(r.Context(), principal.ID, key)
	case "enable":
		_, operationErr = h.ConnectionAdministration.Enable(r.Context(), principal.ID, key)
	case "disable":
		_, operationErr = h.ConnectionAdministration.Disable(r.Context(), principal.ID, key)
	}
	if operationErr != nil {
		message := publicConnectionError(operationErr)
		h.connectionCommandPatch(w, r, command, message)
		return
	}
	message := connectionActionCompletionMessage(command.Action)
	h.connectionCommandSuccess(w, r, command, projectID, assets, edges, message)
}

func connectionActionCompletionMessage(action string) string {
	switch action {
	case "enable":
		return "Enable completed."
	case "disable":
		return "Disable completed."
	case "test":
		return "Test completed."
	case "refresh":
		return "Refresh completed."
	default:
		return "Connection command completed."
	}
}

func (h *BrowserHandler) connectionOperation(action string) string {
	switch action {
	case "create":
		return h.ConnectionCommands.Create.OperationID()
	case "update":
		return h.ConnectionCommands.Update.OperationID()
	case "test":
		return h.ConnectionCommands.Test.OperationID()
	case "refresh":
		return h.ConnectionCommands.Refresh.OperationID()
	case "enable":
		return h.ConnectionCommands.Enable.OperationID()
	case "disable":
		return h.ConnectionCommands.Disable.OperationID()
	default:
		return ""
	}
}

func (h *BrowserHandler) beginConnectionInvocation(r *stdhttp.Request, action, project, connection string, revision int64) (*stdhttp.Request, error) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		return r, apigencommand.ErrIdempotencyRequired
	}
	key := "ui:" + requestID
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if h == nil || h.BeginConnectionCommand == nil {
		return r, errors.New("connection command invocation is unavailable")
	}
	if action != "create" && action != "update" && action != "test" && action != "refresh" && action != "enable" && action != "disable" {
		return r, fmt.Errorf("unsupported connection operation")
	}
	ctx, err := h.BeginConnectionCommand(r.Context(), CreatorCommandInvocation{Action: action, Project: project, Resource: connection, IdempotencyKey: key, RequestID: requestID, CorrelationID: correlationID, Revision: revision})
	if err != nil {
		return r, err
	}
	return r.WithContext(ctx), nil
}

func (h *BrowserHandler) connectionCommandPatch(w stdhttp.ResponseWriter, r *stdhttp.Request, command projectsignals.ConnectionAdministrationCommandSignal, message string) {
	command = redactConnectionCommand(command)
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"connectionAdmin": projectsignals.ConnectionAdministrationSignal{Command: command, Status: projectsignals.ConnectionAdministrationStatusSignal{Error: message}}})
}

func (h *BrowserHandler) connectionCommandSuccess(w stdhttp.ResponseWriter, r *stdhttp.Request, command projectsignals.ConnectionAdministrationCommandSignal, projectID projectgraph.ResourceID, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, message string) {
	command = redactConnectionCommand(command)
	view, _ := h.connectionAdministrationView(r.Context(), projectID, assets, edges, r)
	patch := map[string]any{"connectionAdmin": projectsignals.ConnectionAdministrationSignal{Command: command, Status: projectsignals.ConnectionAdministrationStatusSignal{Message: message}}}
	if command.Surface == "list" {
		patch["page"] = projectui.ConnectionsListResultsPatchWithAdministration(assets, edges, view)["page"]
	} else if asset, found := projectview.AssetByID(assets, command.AssetID); found {
		catalog := h.navigationCatalog(r)
		project := projectview.DevelopView{ID: projectID.String(), Title: catalog.Project.Title, Description: catalog.Project.Description}
		patch["page"] = projectui.ConnectionAssetBootstrapSignalsWithAdministrationForEnvironment(catalog, project, asset, assets, edges, "details", h.Environment, "", projectui.AssetVersionsState{}, view, h.layout(r))["page"]
	}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch(patch))
}

func redactConnectionCommand(command projectsignals.ConnectionAdministrationCommandSignal) projectsignals.ConnectionAdministrationCommandSignal {
	command.CredentialEnvironment = ""
	command.CredentialProjectID = ""
	command.SecretPath = ""
	command.SecretKey = ""
	return command
}

func (h *BrowserHandler) PipelineCommand(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var payload creatorPipelineCommand
	if err := pagestream.ReadSignals(r, &payload); err != nil {
		stdhttp.Error(w, "pipeline command payload is required", stdhttp.StatusBadRequest)
		return
	}
	command, valid := canonicalPipelineCommand(payload.Command)
	if !valid {
		h.pipelineCommandPatch(w, r, command, "The pipeline command is invalid.")
		return
	}
	operation := h.PipelineRunCommand.OperationID()
	if command.Action == "cancel" || command.Action == "cancel-intent" {
		operation = h.PipelineCancelCommand.OperationID()
	}
	if operation == "" || uicommand.VerifyClaim(uicommand.OperationClaims(r), operation) != nil {
		h.pipelineCommandPatch(w, r, command, "The pipeline command is invalid.")
		return
	}
	if command.Action != "run" && command.Action != "retry" && command.Action != "cancel" && command.Action != "cancel-intent" {
		h.pipelineCommandPatch(w, r, command, "The pipeline command is invalid.")
		return
	}
	principal, ok := h.currentPrincipal(r)
	if !ok || (h.RunPipeline == nil && h.CancelPipeline == nil && h.CancelPipelineIntent == nil) {
		h.pipelineCommandPatch(w, r, command, "Pipeline operations are unavailable.")
		return
	}
	// Every pipeline command is resource-scoped. A missing authorizer must
	// fail closed even when a callback happens to be configured (for example
	// in a partial runtime or a test harness).
	if h.AuthorizePipeline == nil {
		h.pipelineCommandPatch(w, r, command, "Pipeline operation is unavailable.")
		return
	}
	if !h.pipelineMutationAllowed(r, command.PipelineID) {
		h.pipelineCommandPatch(w, r, command, "Pipeline operation is forbidden.")
		return
	}
	projectID, resolveErr := h.boundProject(r.Context())
	if resolveErr != nil {
		h.pipelineCommandPatch(w, r, command, "Pipeline operations are unavailable.")
		return
	}
	started, beginErr := h.beginPipelineInvocation(r, command.Action, projectID.String())
	if beginErr != nil {
		h.pipelineCommandPatch(w, r, command, "The pipeline command is invalid.")
		return
	}
	r = started
	var operationErr error
	if command.Action == "cancel-intent" {
		if h.CancelPipelineIntent == nil || strings.TrimSpace(command.IntentID) == "" {
			operationErr = errors.New("waiting request cancellation is unavailable")
		} else {
			operationErr = h.CancelPipelineIntent(r.Context(), command.PipelineID, command.IntentID, principal.ID, "ui:"+strings.TrimSpace(r.Header.Get("X-Request-ID")))
		}
	} else if command.Action == "cancel" {
		if h.CancelPipeline == nil {
			operationErr = errors.New("pipeline cancellation is unavailable")
		} else {
			operationErr = h.CancelPipeline(r.Context(), command.PipelineID, command.RunID, principal.ID)
		}
	} else if h.RunPipeline == nil {
		operationErr = errors.New("pipeline runner is unavailable")
	} else {
		retryOf := ""
		if command.Action == "retry" {
			retryOf = command.RunID
		}
		operationErr = h.RunPipeline(r.Context(), command.PipelineID, principal.ID, retryOf, "ui:"+strings.TrimSpace(r.Header.Get("X-Request-ID")))
	}
	if operationErr != nil {
		message := publicPipelineError(operationErr, command.Action)
		h.pipelineCommandPatch(w, r, command, message)
		return
	}
	message := "Pipeline command accepted."
	if command.Action == "run" || command.Action == "retry" {
		message = "Pipeline request queued."
	} else if command.Action == "cancel-intent" {
		message = "Waiting request cancelled."
	}
	h.pipelineCommandSuccess(w, r, command, message)
}

func (h *BrowserHandler) pipelineCommandSuccess(w stdhttp.ResponseWriter, r *stdhttp.Request, command projectsignals.PipelineCommandSignal, message string) {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("surface")), "pipeline_detail") {
		h.pipelineDetailCommandSuccess(w, r, command, message)
		return
	}
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("surface")), "asset") {
		h.pipelineAssetCommandSuccess(w, r, command, message)
		return
	}
	// The durable command protocol stores the response for replay and bounds its
	// size. Run history can make the page projection larger than that bound, so
	// acknowledge only the mutation here. The live page stream owns refreshes.
	h.pipelineCommandAcknowledgement(w, r, command, message)
}

func (h *BrowserHandler) pipelineDetailCommandSuccess(w stdhttp.ResponseWriter, r *stdhttp.Request, command projectsignals.PipelineCommandSignal, message string) {
	assetID := strings.TrimSpace(r.URL.Query().Get("asset"))
	if assetID == "" || assetID != strings.TrimSpace(command.AssetID) || assetID != strings.TrimSpace(command.PipelineID) {
		h.pipelineCommandPatch(w, r, command, "Pipeline command target is invalid.")
		return
	}
	h.pipelineCommandAcknowledgement(w, r, command, message)
}

// pipelineAssetCommandSuccess acknowledges the command without replacing the
// asset page with a pipeline collection projection. The live stream refreshes
// the mounted asset page independently.
func (h *BrowserHandler) pipelineAssetCommandSuccess(w stdhttp.ResponseWriter, r *stdhttp.Request, command projectsignals.PipelineCommandSignal, message string) {
	assetID := strings.TrimSpace(r.URL.Query().Get("asset"))
	if assetID == "" || assetID != strings.TrimSpace(command.AssetID) {
		h.pipelineCommandPatch(w, r, command, "Pipeline command target is invalid.")
		return
	}
	h.pipelineCommandAcknowledgement(w, r, command, message)
}

func (h *BrowserHandler) pipelineCommandAcknowledgement(w stdhttp.ResponseWriter, r *stdhttp.Request, command projectsignals.PipelineCommandSignal, message string) {
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"pipelineCommand": command, "pipelineCommandStatus": projectsignals.PipelineCommandStatusSignal{Message: message}})
}

func (h *BrowserHandler) pipelineCommandPatch(w stdhttp.ResponseWriter, r *stdhttp.Request, command projectsignals.PipelineCommandSignal, message string) {
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"pipelineCommand": command, "pipelineCommandStatus": projectsignals.PipelineCommandStatusSignal{Error: message}})
}

func (h *BrowserHandler) beginPipelineInvocation(r *stdhttp.Request, action, project string) (*stdhttp.Request, error) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		return r, apigencommand.ErrIdempotencyRequired
	}
	key := "ui:" + requestID
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if h == nil || h.BeginPipelineCommand == nil {
		return r, errors.New("pipeline command invocation is unavailable")
	}
	ctx, err := h.BeginPipelineCommand(r.Context(), CreatorCommandInvocation{Action: action, Project: project, IdempotencyKey: key, RequestID: requestID, CorrelationID: correlationID})
	if err != nil {
		return r, err
	}
	return r.WithContext(ctx), nil
}

func (h *BrowserHandler) currentPrincipal(r *stdhttp.Request) (Principal, bool) {
	if h.CurrentUser == nil {
		return Principal{}, false
	}
	return h.CurrentUser(r)
}

// AuthorizeCreatorMutationReplay rechecks the exact operation target before
// the durable protocol returns a captured browser response. The request body
// is restored because the command handler still needs it on the first call.
func (h *BrowserHandler) AuthorizeCreatorMutationReplay(r *stdhttp.Request) bool {
	if h == nil || r == nil {
		return false
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	switch r.URL.Path {
	case "/pipelines/command":
		var payload creatorPipelineCommand
		if json.Unmarshal(body, &payload) != nil || h.AuthorizePipeline == nil {
			return false
		}
		command, valid := canonicalPipelineCommand(payload.Command)
		if !valid {
			return false
		}
		return h.pipelineMutationAllowed(r, command.PipelineID)
	case "/connections/administration/configuration", "/connections/administration/lifecycle":
		var payload creatorConnectionCommand
		if json.Unmarshal(body, &payload) != nil {
			return false
		}
		if payload.Command.Action == "create" {
			projectID, projectErr := h.boundProject(r.Context())
			if projectErr != nil {
				return false
			}
			return h.connectionCreateAllowed(r, projectID)
		}
		connectionID := strings.TrimSpace(payload.Command.AssetID)
		if connectionID == "" {
			connectionID = strings.TrimSpace(payload.Command.LogicalConnection)
		}
		if connectionID == "" {
			return false
		}
		return h.connectionMutationAllowed(r, connectionID, access.CapabilityResourceManage)
	default:
		return false
	}
}

func resolveConnectionID(command projectsignals.ConnectionAdministrationCommandSignal, assets []projectview.DevelopAssetView) string {
	if asset, ok := projectview.AssetByID(assets, command.AssetID); ok && asset.Type == string(projectview.AssetTypeConnection) {
		return asset.ID
	}
	logical := strings.TrimSpace(command.LogicalConnection)
	for _, asset := range assets {
		if asset.Type == string(projectview.AssetTypeConnection) && projectui.ConnectionLogicalName(asset, assets, nil) == logical {
			return asset.ID
		}
	}
	return logical
}

func (h *BrowserHandler) connectionCreateAllowed(r *stdhttp.Request, projectID projectgraph.ResourceID) bool {
	if h == nil || r == nil {
		return false
	}
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return false
	}
	if principal.DevBypass {
		return true
	}
	if h.AuthorizeConnectionCreate == nil {
		return false
	}
	allowed, err := h.AuthorizeConnectionCreate(r, projectID, access.CapabilityProjectAdmin)
	return err == nil && allowed
}

func (h *BrowserHandler) connectionMutationAllowed(r *stdhttp.Request, connectionID string, capability access.Capability) bool {
	if h == nil || r == nil {
		return false
	}
	principal, ok := h.currentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return false
	}
	if principal.DevBypass {
		return true
	}
	if h.AuthorizeConnection == nil {
		return false
	}
	allowed, err := h.AuthorizeConnection(r, strings.TrimSpace(connectionID), capability)
	return err == nil && allowed
}

// canonicalPipelineCommand keeps the browser contract tolerant of older
// clients that sent only pipelineId while making AssetID authoritative for
// current clients. AssetID is populated with the same canonical resource ID
// so the command, authorization target, and refresh callback cannot drift.
func canonicalPipelineCommand(command projectsignals.PipelineCommandSignal) (projectsignals.PipelineCommandSignal, bool) {
	assetID := strings.TrimSpace(command.AssetID)
	pipelineID := strings.TrimSpace(command.PipelineID)
	if assetID != "" {
		pipelineID = assetID
	}
	if pipelineID == "" {
		return command, false
	}
	command.AssetID = pipelineID
	command.PipelineID = pipelineID
	return command, true
}

func connectionConfiguration(command projectsignals.ConnectionAdministrationCommandSignal) (connectionadmin.TargetBindingConfiguration, error) {
	configuration := connectionadmin.TargetBindingConfiguration{ConnectorKind: strings.TrimSpace(command.ConnectorKind), AuthenticationMode: connectionadmin.AuthenticationMode(strings.TrimSpace(command.AuthenticationMode)), Endpoint: connectionadmin.EndpointConfig{Host: strings.TrimSpace(command.Host), Database: strings.TrimSpace(command.Database), ObjectScope: strings.TrimSpace(command.ObjectScope), SourceIdentity: strings.TrimSpace(command.SourceIdentity), TLSMode: strings.TrimSpace(command.TLSMode)}}
	if command.Port != "" {
		var port int
		if _, err := fmt.Sscanf(command.Port, "%d", &port); err != nil || port < 0 {
			return configuration, errors.New("invalid port")
		}
		configuration.Endpoint.Port = port
	}
	if strings.TrimSpace(command.Options) != "" {
		if err := json.Unmarshal([]byte(command.Options), &configuration.Endpoint.Options); err != nil {
			return configuration, err
		}
	}
	if configuration.AuthenticationMode == connectionadmin.AuthenticationExternalBundle {
		configuration.CredentialReference = connectionadmin.CredentialReference{ProjectID: projectgraph.ResourceID(strings.TrimSpace(command.CredentialProjectID)), Environment: strings.TrimSpace(command.CredentialEnvironment), SecretPath: strings.TrimSpace(command.SecretPath), SecretKey: strings.TrimSpace(command.SecretKey)}
	}
	return configuration, nil
}

func publicConnectionError(err error) string {
	if errors.Is(err, connectionadmin.ErrUnauthorizedBinding) {
		return "Connection operation is forbidden."
	}
	if errors.Is(err, connectionadmin.ErrBindingNotFound) {
		return "Connection binding was not found."
	}
	if errors.Is(err, connectionadmin.ErrIncompatibleBinding) {
		return "Connection changed concurrently; refresh and try again."
	}
	if errors.Is(err, connectionadmin.ErrCredentialDenied) || errors.Is(err, connectionadmin.ErrCredentialNotFound) || errors.Is(err, connectionadmin.ErrInvalidCredentialBundle) {
		return "Connection validation failed."
	}
	if errors.Is(err, connectionadmin.ErrProviderUnavailable) {
		return "Connection provider is unavailable."
	}
	return "Connection operation failed."
}

func publicPipelineError(err error, action string) string {
	if action == "cancel-intent" {
		if kind, ok := apigenfailure.KindOf(err); ok && kind == "conflict" {
			return "This request has already started. Reload to see its run."
		}
	}
	if action == "cancel" {
		if kind, ok := apigenfailure.KindOf(err); ok && kind == "not_cancellable" {
			return "This run has already started and cannot be cancelled. Reload to see its current status."
		}
	}
	return "Pipeline operation failed; review the run history and try again."
}

func (h *BrowserHandler) connectionAdministrationView(ctx context.Context, projectID projectgraph.ResourceID, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, r *stdhttp.Request) (projectui.ConnectionAdministrationView, error) {
	view := projectui.ConnectionAdministrationView{Bindings: map[string]projectui.ConnectionBindingView{}, RequiresBinding: map[string]bool{}}
	for _, asset := range assets {
		if asset.Type != string(projectview.AssetTypeConnection) {
			continue
		}
		logical := projectui.ConnectionLogicalName(asset, assets, edges)
		view.RequiresBinding[logical] = connectionPayloadBool(asset.Payload, "credentials_required")
	}
	if h.ConnectionAdministration == nil || strings.TrimSpace(h.TargetID) == "" || h.CurrentUser == nil {
		return view, nil
	}
	principal, ok := h.CurrentUser(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return view, nil
	}
	view.CanCreate = h.connectionCreateAllowed(r, projectID)
	bindings, err := h.ConnectionAdministration.List(ctx, principal.ID, connectionadmin.BindingScope{ProjectID: projectID, Environment: h.Environment}, connectionadmin.TargetID(h.TargetID))
	if err != nil {
		return view, err
	}
	view.CanManage, view.CanTest = true, true
	for _, binding := range bindings {
		value := projectui.ConnectionBindingView{ID: binding.ID.String(), LogicalConnection: binding.ConnectionID.String(), ConnectorKind: binding.ConnectorKind, AuthenticationMode: string(binding.AuthenticationMode), Host: binding.Endpoint.Host, Port: binding.Endpoint.Port, Database: binding.Endpoint.Database, ObjectScope: binding.Endpoint.ObjectScope, SourceIdentity: binding.Endpoint.SourceIdentity, TLSMode: binding.Endpoint.TLSMode, Options: binding.Endpoint.Options, Enabled: binding.Enabled, Health: string(binding.Health), ValidatedVersion: binding.ValidatedVersion, Revision: binding.Revision}
		// Credential references are write-only. Keep only health/configuration
		// metadata in the browser snapshot; paths, keys, project IDs, and
		// environments remain server-side and must be re-entered for a change.
		if !binding.LastValidatedAt.IsZero() {
			value.LastValidatedAt = binding.LastValidatedAt.UTC().Format(time.RFC3339Nano)
		}
		view.Bindings[binding.ConnectionID.String()] = value
		view.Bindings[strings.TrimPrefix(binding.ConnectionID.String(), "connection:")] = value
	}
	return view, nil
}

func connectionPayloadBool(payload map[string]any, key string) bool {
	value, ok := payload[key]
	if !ok {
		for candidate, candidateValue := range payload {
			if strings.EqualFold(candidate, key) {
				value, ok = candidateValue, true
				break
			}
		}
	}
	boolean, _ := value.(bool)
	return ok && boolean
}
