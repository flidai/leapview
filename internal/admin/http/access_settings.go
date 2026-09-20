package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	adminsettings "github.com/flidai/leapview/internal/admin/settings"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/flidai/leapview/pkg/pagestream"
)

type accessSettingsCommandSignals struct {
	Command adminsettings.AccessSettingsCommand `json:"adminAccessSettingsCommand"`
}

func (h Handler) accessSettingsScope(ctx context.Context) (adminsettings.AccessSettingsScope, error) {
	projectID, err := h.ReadModel.projectID(ctx)
	if err != nil {
		return adminsettings.AccessSettingsScope{}, err
	}
	scope := adminsettings.AccessSettingsScope{TargetID: h.AuthorizationPolicyTargetID, ProjectID: projectID.String(), Environment: h.AuthorizationPolicyEnvironment}
	if strings.TrimSpace(scope.TargetID) == "" || strings.TrimSpace(scope.Environment) == "" {
		return adminsettings.AccessSettingsScope{}, fmt.Errorf("authorization policy scope is not configured")
	}
	return scope, nil
}

func (h Handler) accessSettingsPrincipalID(r *nethttp.Request) string {
	if h.ReadModel.CurrentPrincipal == nil {
		return ""
	}
	principal, ok := h.ReadModel.CurrentPrincipal(r)
	if !ok {
		return ""
	}
	return principal.ID
}

func (h Handler) AccessSettingsCommand(w nethttp.ResponseWriter, r *nethttp.Request) {
	if h.SettingsRepository == nil {
		nethttp.Error(w, "project role binding administration is unavailable", nethttp.StatusServiceUnavailable)
		return
	}
	var signals accessSettingsCommandSignals
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	command := adminsettings.NormalizeAccessSettingsCommand(signals.Command)
	scope, err := h.accessSettingsScope(r.Context())
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusServiceUnavailable)
		return
	}
	started, operation, err := beginAccessSettingsInvocation(r, scope.ProjectID, command)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	r = started
	executor, err := apigencommand.NewExecutor(accessgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusServiceUnavailable)
		return
	}
	var state adminsettings.AccessSettingsSignal
	err = executor.Execute(r.Context(), operation, apigencommand.Execution{Transactional: func(ctx context.Context, _ apigencommand.Contract) error {
		state, err = h.applyAccessSettingsAudited(ctx, r, scope, command)
		return err
	}})
	if err != nil {
		current, loadErr := adminsettings.LoadAccessSettingsForPrincipal(r.Context(), h.SettingsRepository, scope, h.accessSettingsPrincipalID(r), h.ReadModel.EffectiveAccess)
		if loadErr == nil {
			current.Error = err.Error()
			_ = pagestream.PatchResponse(w, r, map[string]any{"adminAccessSettings": current})
			return
		}
		nethttp.Error(w, err.Error(), accessSettingsCommandStatus(err))
		return
	}
	adminsettings.AttachEffectiveAccess(r.Context(), &state, h.accessSettingsPrincipalID(r), h.ReadModel.EffectiveAccess)
	_ = pagestream.PatchResponse(w, r, map[string]any{"adminAccessSettings": state})
}

func (h Handler) applyAccessSettingsAudited(ctx context.Context, r *nethttp.Request, scope adminsettings.AccessSettingsScope, command adminsettings.AccessSettingsCommand) (adminsettings.AccessSettingsSignal, error) {
	transactional, ok := h.SettingsRepository.(access.AuditedMutationRepository)
	if !ok {
		return adminsettings.AccessSettingsSignal{}, errors.New("transactional access repository is required")
	}
	var state adminsettings.AccessSettingsSignal
	err := transactional.RunAuditedMutation(ctx, func(tx access.Repository) (access.AuditEventInput, error) {
		var err error
		state, err = adminsettings.ApplyAccessSettingsCommand(ctx, tx, scope, command, strings.TrimSpace(r.Header.Get("Idempotency-Key")))
		if err != nil {
			return access.AuditEventInput{}, err
		}
		metadata, _ := json.Marshal(map[string]any{
			"targetId": scope.TargetID, "projectId": scope.ProjectID, "environment": scope.Environment,
			"bindingId": command.BindingID, "subjectType": command.SubjectType, "subjectId": command.SubjectID,
			"role": command.Role, "policyRevision": state.PolicyRevision, "policyDigest": state.PolicyDigest,
		})
		action := "role_binding.created"
		if command.Action == "delete" {
			action = "role_binding.deleted"
		}
		actorID := ""
		if h.ReadModel.CurrentPrincipal != nil {
			if principal, ok := h.ReadModel.CurrentPrincipal(r); ok {
				actorID = principal.ID
			}
		}
		return access.AuditEventInput{ProjectID: scope.ProjectID, PrincipalID: actorID, Action: action, ResourceKind: "role_binding", ResourceID: command.BindingID, Capability: access.CapabilityProjectAdmin, Status: "success", RequestID: r.Header.Get("X-Request-ID"), CorrelationID: r.Header.Get("X-Correlation-ID"), MetadataJSON: string(metadata)}, nil
	})
	return state, err
}

func beginAccessSettingsInvocation(r *nethttp.Request, projectID string, command adminsettings.AccessSettingsCommand) (*nethttp.Request, string, error) {
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if command.Action != "create" && command.Action != "delete" {
		return r, "", errors.New("unknown project access command")
	}
	if strings.TrimSpace(projectID) == "" {
		return r, "", apigencommand.ErrTargetRequired
	}
	if idempotencyKey == "" {
		return r, "", apigencommand.ErrIdempotencyRequired
	}
	operation := accessgen.GenCommandOperationCreateProjectRoleBinding().APIGenOperationID()
	if command.Action == "delete" {
		operation = accessgen.GenCommandOperationDeleteProjectRoleBinding().APIGenOperationID()
	}
	if err := uicommand.VerifyClaim(uicommand.OperationClaims(r), operation); err != nil {
		return r, "", err
	}
	contract, ok := accessgen.GetAPIGenCommandRuntimeContract(operation)
	if !ok {
		return r, "", fmt.Errorf("%w: %q", apigencommand.ErrContractNotFound, operation)
	}
	// These role-binding operations are canonical API commands and are not
	// declared as a second generated UI exposure. This admin route still uses
	// their contract for durable audit/execution guarantees after verifying its
	// own browser operation claim above.
	ctx, _, err := apigencommand.Begin(r.Context(), contract)
	return r.WithContext(ctx), operation, err
}

func accessSettingsCommandStatus(err error) int {
	switch {
	case errors.Is(err, access.ErrAuthorizationPolicyInvalidBinding):
		return nethttp.StatusBadRequest
	case errors.Is(err, access.ErrAuthorizationPolicyNotFound):
		return nethttp.StatusNotFound
	case errors.Is(err, access.ErrAuthorizationPolicyConflict), errors.Is(err, access.ErrAuthorizationPolicyStaleRevision), errors.Is(err, access.ErrAuthorizationPolicyIdempotency):
		return nethttp.StatusConflict
	default:
		return nethttp.StatusServiceUnavailable
	}
}
