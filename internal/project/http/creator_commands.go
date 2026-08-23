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
	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/connectionadmin"
	uicommand "github.com/flidai/leapview/internal/platform/web/uicommand"
	projectview "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectui "github.com/flidai/leapview/internal/project/ui"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
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
		if h.AuthorizeConnectionCreate == nil {
			h.connectionCommandPatch(w, r, command, "Connection administration is unavailable.")
			return
		}
		allowed, authErr := h.AuthorizeConnectionCreate(r, projectID, access.CapabilityProjectAdmin)
		if authErr != nil || !allowed {
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
		if h.AuthorizeConnection == nil {
			h.connectionCommandPatch(w, r, command, "Connection administration is unavailable.")
			return
		}
		allowed, authErr := h.AuthorizeConnection(r, connectionID, access.CapabilityResourceManage)
		if authErr != nil || !allowed {
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
	if h.AuthorizeConnection == nil {
		h.connectionCommandPatch(w, r, command, "Connection administration is unavailable.")
		return
	}
	allowed, authErr := h.AuthorizeConnection(r, connectionID, access.CapabilityResourceManage)
	if authErr != nil || !allowed {
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
	message := strings.Title(command.Action) + " completed."
	h.connectionCommandSuccess(w, r, command, projectID, assets, edges, message)
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
		project := projectview.DevelopView{ID: projectID.String(), Title: h.navigationCatalog(r).Project.Title, Description: h.navigationCatalog(r).Project.Description}
		patch["page"] = projectui.ConnectionAssetBootstrapSignalsWithAdministrationForEnvironment(h.navigationCatalog(r), project, asset, assets, edges, "details", h.Environment, "", projectui.AssetVersionsState{}, view, h.layout(r))["page"]
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
	if command.Action == "cancel" {
		operation = h.PipelineCancelCommand.OperationID()
	}
	if operation == "" || uicommand.VerifyClaim(uicommand.OperationClaims(r), operation) != nil {
		h.pipelineCommandPatch(w, r, command, "The pipeline command is invalid.")
		return
	}
	if command.Action != "run" && command.Action != "retry" && command.Action != "cancel" {
		h.pipelineCommandPatch(w, r, command, "The pipeline command is invalid.")
		return
	}
	principal, ok := h.currentPrincipal(r)
	if !ok || (h.RunPipeline == nil && h.CancelPipeline == nil) {
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
	allowed, authErr := h.AuthorizePipeline(r, command.PipelineID, access.CapabilityResourceUse)
	if authErr != nil || !allowed {
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
	if command.Action == "cancel" {
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
		operationErr = h.RunPipeline(r.Context(), command.PipelineID, principal.ID, retryOf)
	}
	if operationErr != nil {
		message := publicPipelineError(operationErr)
		h.pipelineCommandPatch(w, r, command, message)
		return
	}
	message := "Pipeline command accepted."
	h.pipelineCommandSuccess(w, r, command, message)
}

func (h *BrowserHandler) pipelineCommandSuccess(w stdhttp.ResponseWriter, r *stdhttp.Request, command projectsignals.PipelineCommandSignal, message string) {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("surface")), "asset") {
		h.pipelineAssetCommandSuccess(w, r, command, message)
		return
	}
	// The mutation has already committed. Resolve the refreshed projection
	// against a sink so a read-side outage cannot turn that successful write
	// into a 5xx response that the durable idempotency layer would abandon.
	// The browser can safely reload the authoritative state later.
	projectID, assets, _, assetsOK := h.assets(newCreatorResponseSink(), r)
	if !assetsOK {
		h.pipelineCommandPatch(w, r, command, message+" Reload the page to refresh pipeline status.")
		return
	}
	assets, _ = h.projectAssetReadModels(r.Context(), assets)
	state := projectui.PipelineMonitorState{Environment: h.Environment, CSRFToken: h.csrf(r), RunCommand: h.PipelineRunCommand, CancelCommand: h.PipelineCancelCommand}
	for _, asset := range projectview.FilterProjectLandingAssets(assets, string(projectview.AssetTypeRefreshPipeline), "") {
		refresh, _ := h.assetRefreshState(r.Context(), projectID, asset)
		canUse := h.pipelineMutationAllowed(r, asset.ID)
		state.Pipelines = append(state.Pipelines, projectui.PipelineMonitorPipeline{
			Asset: asset, Refresh: refresh,
			CanRun:    canUse && !refresh.Unavailable && h.PipelineRunCommand.OperationID() != "",
			CanCancel: canUse && !refresh.Unavailable && h.PipelineCancelCommand.OperationID() != "",
		})
	}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"page": projectui.PipelinesPagePatch(state, r.URL.Query().Get("view")), "pipelineCommand": command, "pipelineCommandStatus": projectsignals.PipelineCommandStatusSignal{Message: message}})
}

// pipelineAssetCommandSuccess refreshes the detail projection that initiated
// a pipeline command. The list projection used by /pipelines is a different
// signal contract and must never replace a ResourceAssetPageSignal mounted by
// an asset-detail route.
func (h *BrowserHandler) pipelineAssetCommandSuccess(w stdhttp.ResponseWriter, r *stdhttp.Request, command projectsignals.PipelineCommandSignal, message string) {
	assetID := strings.TrimSpace(r.URL.Query().Get("asset"))
	if assetID == "" || assetID != strings.TrimSpace(command.AssetID) {
		h.pipelineCommandPatch(w, r, command, "Pipeline command target is invalid.")
		return
	}
	projectID, assets, edges, assetsOK := h.assets(newCreatorResponseSink(), r)
	if !assetsOK {
		h.pipelineCommandPatch(w, r, command, message+" Reload the page to refresh pipeline status.")
		return
	}
	assets, err := h.projectAssetReadModels(r.Context(), assets)
	if err != nil {
		h.pipelineCommandPatch(w, r, command, message+" Reload the page to refresh pipeline status.")
		return
	}
	asset, found := projectview.AssetByID(assets, assetID)
	if !found || asset.Type != string(projectview.AssetTypeRefreshPipeline) {
		h.pipelineCommandPatch(w, r, command, message+" Reload the page to refresh pipeline status.")
		return
	}
	section := strings.TrimSpace(r.URL.Query().Get("section"))
	if !projectui.ValidProjectAssetSection(asset.Type, section) {
		section = "details"
	}
	refresh, refreshErr := h.assetRefreshState(r.Context(), projectID, asset)
	if refreshErr != nil {
		refresh.Unavailable = true
	}
	refresh.CanRun = h.pipelineMutationAllowed(r, asset.ID)
	refresh.CSRFToken = h.csrf(r)
	versions, versionsErr := h.assetVersionsState(r.Context(), projectID, asset, section)
	if versionsErr != nil {
		h.pipelineCommandPatch(w, r, command, message+" Reload the page to refresh pipeline status.")
		return
	}
	project := projectview.DevelopView{ID: projectID.String(), Title: h.navigationCatalog(r).Project.Title, Description: h.navigationCatalog(r).Project.Description}
	detailPatch := projectui.ProjectAssetBootstrapSignalsForEnvironment(h.navigationCatalog(r), project, asset, assets, edges, section, h.Environment, "", refresh, versions, h.layout(r))
	page, found := detailPatch["page"]
	if !found {
		h.pipelineCommandPatch(w, r, command, message+" Reload the page to refresh pipeline status.")
		return
	}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"page": page, "pipelineCommand": command, "pipelineCommandStatus": projectsignals.PipelineCommandStatusSignal{Message: message}})
}

type creatorResponseSink struct {
	header stdhttp.Header
}

func newCreatorResponseSink() *creatorResponseSink {
	return &creatorResponseSink{header: make(stdhttp.Header)}
}

func (w *creatorResponseSink) Header() stdhttp.Header { return w.header }

func (*creatorResponseSink) Write(body []byte) (int, error) { return len(body), nil }

func (*creatorResponseSink) WriteHeader(int) {}

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
		allowed, authErr := h.AuthorizePipeline(r, command.PipelineID, access.CapabilityResourceUse)
		return authErr == nil && allowed
	case "/connections/administration/configuration", "/connections/administration/lifecycle":
		var payload creatorConnectionCommand
		if json.Unmarshal(body, &payload) != nil {
			return false
		}
		if payload.Command.Action == "create" {
			projectID, projectErr := h.boundProject(r.Context())
			if projectErr != nil || h.AuthorizeConnectionCreate == nil {
				return false
			}
			allowed, authErr := h.AuthorizeConnectionCreate(r, projectID, access.CapabilityProjectAdmin)
			return authErr == nil && allowed
		}
		connectionID := strings.TrimSpace(payload.Command.AssetID)
		if connectionID == "" {
			connectionID = strings.TrimSpace(payload.Command.LogicalConnection)
		}
		if connectionID == "" || h.AuthorizeConnection == nil {
			return false
		}
		allowed, authErr := h.AuthorizeConnection(r, connectionID, access.CapabilityResourceManage)
		return authErr == nil && allowed
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

func publicPipelineError(error) string {
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
	if h.AuthorizeConnectionCreate != nil {
		view.CanCreate, _ = h.AuthorizeConnectionCreate(r, projectID, access.CapabilityProjectAdmin)
	}
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
