package http

import (
	"context"
	"errors"
	nethttp "net/http"
	"net/url"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	adminsettings "github.com/flidai/leapview/internal/admin/settings"
	"github.com/flidai/leapview/internal/admin/ui"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/pagestream"
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
	projectID := ""
	if command.Action == "grant_role" || command.Action == "revoke_role" {
		if h.ReadModel.CurrentProjectID == nil && h.RoleBindingProjectID == nil {
			nethttp.Error(w, "active project identity is unavailable", nethttp.StatusServiceUnavailable)
			return
		}
		resolve := h.RoleBindingProjectID
		if resolve == nil {
			resolve = func(r *nethttp.Request) (projectgraph.ResourceID, error) {
				return h.ReadModel.CurrentProjectID(r.Context())
			}
		}
		resolved, resolveErr := resolve(r)
		if resolveErr != nil {
			nethttp.Error(w, resolveErr.Error(), nethttp.StatusServiceUnavailable)
			return
		}
		projectID = resolved.String()
	}
	started, err := beginAccessAdministrationInvocation(r, command, projectID)
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
	if command.Action == "grant_role" || command.Action == "revoke_role" {
		if h.RoleBindingMutation == nil {
			nethttp.Error(w, "role administration is unavailable", nethttp.StatusServiceUnavailable)
			return
		}
		subject := access.SubjectRef{}
		if command.Action == "grant_role" {
			var subjectErr error
			subject, subjectErr = access.NewSubjectRef(access.SubjectKind(command.SubjectType), command.SubjectID)
			if subjectErr != nil {
				nethttp.Error(w, subjectErr.Error(), nethttp.StatusBadRequest)
				return
			}
		}
		_, mutationErr := h.RoleBindingMutation(r, access.RoleBindingAdministrationCommand{
			Action: command.Action, BindingID: command.BindingID, Subject: subject,
			Role: access.PermissionRole(command.Role), ExpectedRevision: command.ExpectedRevision,
			IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")),
		})
		state, loadErr := h.loadAccessAdministration(r, actorID, command.PrincipalID, command.GroupID)
		if loadErr != nil {
			nethttp.Error(w, loadErr.Error(), nethttp.StatusInternalServerError)
			return
		}
		if mutationErr != nil {
			state.Error = mutationErr.Error()
			if state.RoleMutationUnavailableReason != "" {
				state.Error = state.RoleMutationUnavailableReason
			}
		} else if command.Action == "grant_role" {
			state.Message = "Role assigned. New authority becomes active with the next authorized release."
		} else {
			state.Message = "Role removed. Revocation is enforced immediately by current-policy restriction."
		}
		_ = pagestream.PatchResponse(w, r, map[string]any{"adminAccess": state})
		return
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
		state, loadErr := h.loadAccessAdministration(r, actorID, selectedPrincipalID, selectedGroupID)
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
		state, loadErr := h.loadAccessAdministration(r, actorID, "", "")
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
	state, err := h.loadAccessAdministration(r, actorID, selectedPrincipalID, selectedGroupID)
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

const devBypassRoleMutationUnavailable = "Role changes require a signed-in account with a real session or scoped API credential. This dev server uses authentication bypass, which cannot issue or revoke roles. Restart with LEAPVIEW_DEV_AUTH_BYPASS=false and sign in."

func (h Handler) loadAccessAdministration(r *nethttp.Request, actorID, selectedPrincipalID, selectedGroupID string) (adminsettings.AccessAdministrationSignal, error) {
	var readers []adminsettings.AuthorizationProjectionReader
	if h.AuthorizationProjection != nil {
		readers = append(readers, h.AuthorizationProjection)
	}
	state, err := adminsettings.LoadAccessAdministration(r.Context(), h.SettingsRepository, actorID, selectedPrincipalID, selectedGroupID, readers...)
	if h.ReadModel.CurrentPrincipal != nil {
		if principal, ok := h.ReadModel.CurrentPrincipal(r); ok && principal.DevBypass {
			state.RoleMutationUnavailableReason = devBypassRoleMutationUnavailable
		}
	}
	if err == nil && h.RoleBindingAdministrationForRequest != nil {
		policy, handled, policyErr := h.RoleBindingAdministrationForRequest(r)
		if policyErr != nil {
			return state, policyErr
		}
		if handled {
			adminsettings.ApplyRoleBindingAdministrationState(&state, adminsettings.RoleBindingAdministrationStateFromAccess(policy))
			return state, nil
		}
	}
	if err == nil && h.RoleBindingAdministration != nil {
		policy, policyErr := h.RoleBindingAdministration(r.Context())
		if policyErr != nil {
			if errors.Is(policyErr, access.ErrAuthorizationPolicyInvalidScope) || errors.Is(policyErr, access.ErrAuthorizationPolicyNotFound) {
				return state, nil
			}
			return state, policyErr
		}
		adminsettings.ApplyRoleBindingAdministrationState(&state, adminsettings.RoleBindingAdministrationStateFromAccess(policy))
	}
	return state, err
}

func beginAccessAdministrationInvocation(r *nethttp.Request, command adminsettings.AccessAdministrationCommand, projectID ...string) (*nethttp.Request, error) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	idempotencyKey := "ui:" + requestID
	boundProjectID := ""
	if len(projectID) > 0 {
		boundProjectID = strings.TrimSpace(projectID[0])
	}
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
			ctx, _, err := accessgen.BeginGenCreateGroupCommand(r.Context(), accessgen.GenCreateGroupCommandInvocation{Surface: apigencommand.SurfaceUI, IdempotencyKey: idempotencyKey, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	case "update_group":
		return begin(accessgen.GenUIActionUpdateGroup(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenUpdateGroupCommand(r.Context(), accessgen.GenUpdateGroupCommandInvocation{Surface: apigencommand.SurfaceUI, Group: command.GroupID, ConcurrencyToken: command.Revision, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	case "delete_group":
		return begin(accessgen.GenUIActionDeleteGroup(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenDeleteGroupCommand(r.Context(), accessgen.GenDeleteGroupCommandInvocation{Surface: apigencommand.SurfaceUI, Group: command.GroupID, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	case "add_group_member":
		return begin(accessgen.GenUIActionAddGroupMember(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenAddGroupMemberCommand(r.Context(), accessgen.GenAddGroupMemberCommandInvocation{Surface: apigencommand.SurfaceUI, Group: command.GroupID, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	case "remove_group_member":
		return begin(accessgen.GenUIActionRemoveGroupMember(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenRemoveGroupMemberCommand(r.Context(), accessgen.GenRemoveGroupMemberCommandInvocation{Surface: apigencommand.SurfaceUI, Group: command.GroupID, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	case "grant_role":
		return begin(accessgen.GenUIActionCreateProjectRoleBinding(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenCreateProjectRoleBindingCommand(r.Context(), accessgen.GenCreateProjectRoleBindingCommandInvocation{Surface: apigencommand.SurfaceUI, Project: boundProjectID, IdempotencyKey: idempotencyKey, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	case "revoke_role":
		return begin(accessgen.GenUIActionDeleteProjectRoleBinding(), func() (context.Context, error) {
			ctx, _, err := accessgen.BeginGenDeleteProjectRoleBindingCommand(r.Context(), accessgen.GenDeleteProjectRoleBindingCommandInvocation{Surface: apigencommand.SurfaceUI, Project: boundProjectID, IdempotencyKey: idempotencyKey, RequestID: requestID, CorrelationID: correlationID})
			return ctx, err
		})
	default:
		return r, nethttp.ErrNotSupported
	}
}
