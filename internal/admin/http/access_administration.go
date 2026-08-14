package http

import (
	"context"
	nethttp "net/http"
	"net/url"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/Yacobolo/toolbelt/pagestream"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	adminsettings "github.com/flidai/leapview/internal/admin/settings"
	"github.com/flidai/leapview/internal/admin/ui"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

type accessAdministrationCommandSignals struct {
	Command adminsettings.AccessAdministrationCommand `json:"adminAccessCommand"`
}

func (h Handler) AccessAdministrationCommand(w nethttp.ResponseWriter, r *nethttp.Request) {
	if h.SettingsRepository == nil {
		nethttp.Error(w, "access administration is unavailable", nethttp.StatusServiceUnavailable)
		return
	}
	var signals accessAdministrationCommandSignals
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	command := adminsettings.NormalizeAccessAdministrationCommand(signals.Command)
	section := strings.TrimSpace(r.URL.Query().Get("section"))
	started, err := beginAccessAdministrationInvocation(r, command)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	r = started
	actorID := ""
	if h.ReadModel.CurrentPrincipal != nil {
		if principal, ok := h.ReadModel.CurrentPrincipal(r); ok {
			actorID = principal.ID
		}
	}
	result, err := adminsettings.ApplyAccessAdministrationCommand(r.Context(), h.SettingsRepository, actorID, command)
	if err != nil {
		selectedPrincipalID, selectedGroupID := command.PrincipalID, command.GroupID
		if section == "principals" {
			selectedPrincipalID = ""
		}
		if section == "groups" {
			selectedGroupID = ""
		}
		state, loadErr := h.loadAccessAdministration(r.Context(), actorID, selectedPrincipalID, selectedGroupID)
		if loadErr != nil {
			nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
			return
		}
		state.Error = err.Error()
		_ = pagestream.PatchResponse(w, r, map[string]any{"adminAccess": state})
		return
	}
	if result.Deleted {
		destination := "/admin/principals"
		if strings.Contains(command.Action, "group") {
			destination = "/admin/groups"
		}
		state, loadErr := h.loadAccessAdministration(r.Context(), actorID, "", "")
		if loadErr != nil {
			nethttp.Error(w, loadErr.Error(), nethttp.StatusInternalServerError)
			return
		}
		state.Message = result.Message
		state.RedirectTo = destination
		_ = pagestream.PatchResponse(w, r, map[string]any{"adminAccess": state})
		return
	}
	selectedPrincipalID, selectedGroupID := result.SelectedPrincipalID, result.SelectedGroupID
	if section == "principals" {
		selectedPrincipalID = ""
	}
	if section == "groups" {
		selectedGroupID = ""
	}
	state, err := h.loadAccessAdministration(r.Context(), actorID, selectedPrincipalID, selectedGroupID)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	state.TemporaryPassword = result.TemporaryPassword
	state.Message = result.Message
	if command.Action == "create_group" && section == "groups" && result.SelectedGroupID != "" {
		state.RedirectTo = "/admin/groups/" + url.PathEscape(result.SelectedGroupID)
	}
	patch := map[string]any{"adminAccess": state}
	if section != "" {
		data, pageErr := h.adminDataForUpdates(r, section)
		if pageErr == nil {
			if page, ok := ui.AdminBootstrapSignals(section, data, h.layout(r))["page"]; ok {
				patch["page"] = page
			}
		}
	}
	_ = pagestream.PatchResponse(w, r, patch)
}

func (h Handler) loadAccessAdministration(ctx context.Context, actorID, selectedPrincipalID, selectedGroupID string) (adminsettings.AccessAdministrationSignal, error) {
	state, err := adminsettings.LoadAccessAdministration(ctx, h.SettingsRepository, actorID, selectedPrincipalID, selectedGroupID)
	if err != nil || h.WorkspaceSettings == nil {
		return state, err
	}
	summaries, err := h.WorkspaceSettings.List(ctx)
	if err != nil {
		return state, err
	}
	state.Workspaces = make([]adminsettings.AccessWorkspaceSignal, 0, len(summaries))
	for _, summary := range summaries {
		name := strings.TrimSpace(summary.Title)
		if name == "" {
			name = string(summary.ID)
		}
		state.Workspaces = append(state.Workspaces, adminsettings.AccessWorkspaceSignal{ID: string(summary.ID), Name: name})
	}
	return state, nil
}

func beginAccessAdministrationInvocation(r *nethttp.Request, command adminsettings.AccessAdministrationCommand) (*nethttp.Request, error) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	idempotencyKey := "ui:" + requestID
	begin := func(binding uicommand.Binding, start func() (context.Context, error)) (*nethttp.Request, error) {
		if err := uicommand.VerifyClaim(uicommand.OperationClaims(r), binding.OperationID()); err != nil {
			return r, err
		}
		ctx, err := start()
		if err != nil {
			return r, err
		}
		return r.WithContext(ctx), nil
	}
	workspaceID := command.WorkspaceID
	switch command.Action {
	case "create_principal":
		return begin(accessgen.GenUIActionCreatePrincipal(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenCreatePrincipalCommand(r.Context(), accessgen.GenCreatePrincipalCommandInvocation{Surface: apigencommand.SurfaceUI, IdempotencyKey: idempotencyKey, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	case "update_principal":
		return begin(accessgen.GenUIActionUpdatePrincipal(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenUpdatePrincipalCommand(r.Context(), accessgen.GenUpdatePrincipalCommandInvocation{Surface: apigencommand.SurfaceUI, Principal: command.PrincipalID, ConcurrencyToken: command.Revision, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	case "delete_principal":
		return begin(accessgen.GenUIActionDeletePrincipal(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenDeletePrincipalCommand(r.Context(), accessgen.GenDeletePrincipalCommandInvocation{Surface: apigencommand.SurfaceUI, Principal: command.PrincipalID, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	case "block_principal":
		return begin(accessgen.GenUIActionDisablePrincipal(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenDisablePrincipalCommand(r.Context(), accessgen.GenDisablePrincipalCommandInvocation{Surface: apigencommand.SurfaceUI, Principal: command.PrincipalID, IdempotencyKey: idempotencyKey, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	case "unblock_principal":
		return begin(accessgen.GenUIActionEnablePrincipal(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenEnablePrincipalCommand(r.Context(), accessgen.GenEnablePrincipalCommandInvocation{Surface: apigencommand.SurfaceUI, Principal: command.PrincipalID, IdempotencyKey: idempotencyKey, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	case "reset_password":
		return begin(accessgen.GenUIActionResetPrincipalPassword(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenResetPrincipalPasswordCommand(r.Context(), accessgen.GenResetPrincipalPasswordCommandInvocation{Surface: apigencommand.SurfaceUI, Principal: command.PrincipalID, IdempotencyKey: idempotencyKey, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	case "revoke_session", "revoke_all_sessions":
		return begin(accessgen.GenUIActionRevokePrincipalSession(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenRevokePrincipalSessionCommand(r.Context(), accessgen.GenRevokePrincipalSessionCommandInvocation{Surface: apigencommand.SurfaceUI, Principal: command.PrincipalID, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	case "create_group":
		return begin(accessgen.GenUIActionCreateGroup(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenCreateGroupCommand(r.Context(), accessgen.GenCreateGroupCommandInvocation{Surface: apigencommand.SurfaceUI, Workspace: workspaceID, IdempotencyKey: idempotencyKey, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	case "update_group":
		return begin(accessgen.GenUIActionUpdateGroup(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenUpdateGroupCommand(r.Context(), accessgen.GenUpdateGroupCommandInvocation{Surface: apigencommand.SurfaceUI, Workspace: workspaceID, ConcurrencyToken: command.Revision, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	case "delete_group":
		return begin(accessgen.GenUIActionDeleteGroup(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenDeleteGroupCommand(r.Context(), accessgen.GenDeleteGroupCommandInvocation{Surface: apigencommand.SurfaceUI, Workspace: workspaceID, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	case "add_group_member":
		return begin(accessgen.GenUIActionAddGroupMember(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenAddGroupMemberCommand(r.Context(), accessgen.GenAddGroupMemberCommandInvocation{Surface: apigencommand.SurfaceUI, Workspace: workspaceID, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	case "remove_group_member":
		return begin(accessgen.GenUIActionRemoveGroupMember(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenRemoveGroupMemberCommand(r.Context(), accessgen.GenRemoveGroupMemberCommandInvocation{Surface: apigencommand.SurfaceUI, Workspace: workspaceID, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	default:
		return r, nethttp.ErrNotSupported
	}
}
