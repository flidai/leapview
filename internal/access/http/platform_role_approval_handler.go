package http

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/go-chi/chi/v5"
)

type platformRoleApprovalCreateRequest struct {
	Action           string `json:"action"`
	PrincipalID      string `json:"principalId"`
	ExpectedRevision string `json:"expectedRevision"`
}

type platformRoleApprovalDecisionRequest struct {
	ExpectedRevision int64 `json:"expectedRevision"`
}

func platformRoleApprovalDTO(value access.PlatformRoleApproval) map[string]any {
	return map[string]any{
		"id": value.ID, "action": value.Action, "principalId": value.PrincipalID,
		"requesterId": value.RequesterID, "approverId": emptyToNil(value.ApproverID),
		"canceledBy": emptyToNil(value.CanceledBy), "expiredBy": emptyToNil(value.ExpiredBy),
		"status": string(value.Status), "expectedRevision": value.ExpectedRevision, "revision": value.Revision,
		"idempotencyKey": value.IdempotencyKey, "expiresAt": value.ExpiresAt, "createdAt": value.CreatedAt,
		"approvedAt": emptyToNil(value.ApprovedAt), "canceledAt": emptyToNil(value.CanceledAt),
		"expiredAt": emptyToNil(value.ExpiredAt), "executedAt": emptyToNil(value.ExecutedAt),
		"bindingId": emptyToNil(value.BindingID), "resultRevision": emptyToNil(value.ResultRevision),
	}
}

func (h Handler) platformRoleApprovalWriter(w stdhttp.ResponseWriter, r *stdhttp.Request) (access.PlatformRoleApprovalWriter, access.Repository, bool) {
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return nil, nil, false
	}
	writer, ok := repo.(access.PlatformRoleApprovalWriter)
	if !ok {
		h.recordDeniedPlatformAttempt(r, "platform_role_approval.denied", "platform_role_approval", platformRoleApprovalRequestTarget(r), access.AuditReasonConfigurationUnavailable, nil)
		writeJSONError(w, errors.New("platform role approval writer is unavailable"), stdhttp.StatusServiceUnavailable)
		return nil, nil, false
	}
	return writer, repo, true
}

func (h Handler) ListPlatformRoleApprovals(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	reader, ok := repo.(access.PlatformRoleApprovalReader)
	if !ok {
		writeJSONError(w, errors.New("platform role approval reader is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	items, err := reader.ListPlatformRoleApprovals(r.Context(), "")
	if err != nil {
		writePlatformRoleApprovalError(w, err)
		return
	}
	values := make([]map[string]any, 0, len(items))
	for _, item := range items {
		values = append(values, platformRoleApprovalDTO(item))
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{"items": values})
}

func (h Handler) GetPlatformRoleApproval(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	repo, err := h.repository()
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return
	}
	reader, ok := repo.(access.PlatformRoleApprovalReader)
	if !ok {
		writeJSONError(w, errors.New("platform role approval reader is unavailable"), stdhttp.StatusServiceUnavailable)
		return
	}
	item, err := reader.GetPlatformRoleApproval(r.Context(), chi.URLParam(r, "approval"))
	if err != nil {
		writePlatformRoleApprovalError(w, err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, platformRoleApprovalDTO(item))
}

func (h Handler) RequestPlatformRoleApproval(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input platformRoleApprovalCreateRequest
	if err := decodeStrictJSON(r, &input); err != nil {
		h.recordDeniedPlatformAttempt(r, "platform_role_approval.requested", "platform_role_approval", platformRoleApprovalRequestTarget(r), access.AuditReasonInvalidRequest, nil)
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	_, repo, ok := h.platformRoleApprovalWriter(w, r)
	if !ok {
		return
	}
	var result access.PlatformRoleApproval
	err := executePlatformRoleApprovalMutation(r, repo, accessgen.GenCommandOperationRequestPlatformRoleApproval(), func(tx access.Repository) (access.AuditEventInput, error) {
		txWriter, ok := tx.(access.PlatformRoleApprovalWriter)
		if !ok {
			return access.AuditEventInput{}, errors.New("transactional platform role approval writer is unavailable")
		}
		var err error
		result, err = txWriter.RequestPlatformRoleApproval(r.Context(), access.PlatformRoleApprovalRequestInput{Action: input.Action, PrincipalID: input.PrincipalID, RequesterID: h.currentPrincipalID(r), ExpectedRevision: input.ExpectedRevision, IdempotencyKey: r.Header.Get("Idempotency-Key")})
		if err != nil {
			return access.AuditEventInput{}, err
		}
		return platformRoleApprovalAuditInput(r, accessgen.GenCommandOperationRequestPlatformRoleApproval(), h.currentPrincipalID(r), "platform_role_approval.requested", result)
	})
	if err != nil {
		h.recordDeniedPlatformAttempt(r, "platform_role_approval.requested", "platform_role_approval", platformRoleApprovalRequestTarget(r), auditDenialReason(err), map[string]any{"action": input.Action, "targetPrincipalId": input.PrincipalID})
		writePlatformRoleApprovalError(w, err)
		return
	}
	writeJSON(w, stdhttp.StatusCreated, platformRoleApprovalDTO(result))
}

func (h Handler) ApprovePlatformRoleApproval(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.decidePlatformRoleApproval(w, r, "approve")
}
func (h Handler) CancelPlatformRoleApproval(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.decidePlatformRoleApproval(w, r, "cancel")
}
func (h Handler) ExpirePlatformRoleApproval(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.decidePlatformRoleApproval(w, r, "expire")
}

func (h Handler) decidePlatformRoleApproval(w stdhttp.ResponseWriter, r *stdhttp.Request, action string) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	var input platformRoleApprovalDecisionRequest
	if err := decodeStrictJSON(r, &input); err != nil {
		h.recordDeniedPlatformAttempt(r, "platform_role_approval."+action+"d", "platform_role_approval", platformRoleApprovalRequestTarget(r), access.AuditReasonInvalidRequest, nil)
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return
	}
	if input.ExpectedRevision <= 0 {
		h.recordDeniedPlatformAttempt(r, "platform_role_approval."+action+"d", "platform_role_approval", platformRoleApprovalRequestTarget(r), access.AuditReasonInvalidRequest, nil)
		writeJSONError(w, errors.New("expectedRevision is required"), stdhttp.StatusBadRequest)
		return
	}
	_, repo, ok := h.platformRoleApprovalWriter(w, r)
	if !ok {
		return
	}
	var result access.PlatformRoleApproval
	operation := accessgen.GenCommandOperationApprovePlatformRoleApproval()
	switch action {
	case "cancel":
		operation = accessgen.GenCommandOperationCancelPlatformRoleApproval()
	case "expire":
		operation = accessgen.GenCommandOperationExpirePlatformRoleApproval()
	}
	err := executePlatformRoleApprovalMutation(r, repo, operation, func(tx access.Repository) (access.AuditEventInput, error) {
		txWriter, ok := tx.(access.PlatformRoleApprovalWriter)
		if !ok {
			return access.AuditEventInput{}, errors.New("transactional platform role approval writer is unavailable")
		}
		decision := access.PlatformRoleApprovalDecisionInput{ApprovalID: chi.URLParam(r, "approval"), ActorID: h.currentPrincipalID(r), ExpectedRevision: input.ExpectedRevision, IdempotencyKey: r.Header.Get("Idempotency-Key")}
		var err error
		switch action {
		case "approve":
			result, err = txWriter.ApprovePlatformRoleApproval(r.Context(), decision)
		case "cancel":
			result, err = txWriter.CancelPlatformRoleApproval(r.Context(), decision)
		case "expire":
			result, err = txWriter.ExpirePlatformRoleApproval(r.Context(), decision)
		default:
			err = access.ErrPlatformRoleApprovalInvalid
		}
		if err != nil {
			return access.AuditEventInput{}, err
		}
		return platformRoleApprovalAuditInput(r, operation, h.currentPrincipalID(r), "platform_role_approval."+action+"d", result)
	})
	if err != nil {
		h.recordDeniedPlatformAttempt(r, "platform_role_approval."+action+"d", "platform_role_approval", platformRoleApprovalRequestTarget(r), auditDenialReason(err), nil)
		writePlatformRoleApprovalError(w, err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, platformRoleApprovalDTO(result))
}

func (h Handler) ExecutePlatformRoleApproval(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	_, repo, ok := h.platformRoleApprovalWriter(w, r)
	if !ok {
		return
	}
	var result access.PlatformRoleApproval
	err := executePlatformRoleApprovalBatch(r, repo, accessgen.GenCommandOperationExecutePlatformRoleApproval(), func(tx access.Repository) ([]access.AuditEventInput, error) {
		txWriter, ok := tx.(access.PlatformRoleApprovalWriter)
		if !ok {
			return nil, errors.New("transactional platform role approval writer is unavailable")
		}
		var err error
		result, err = txWriter.ExecutePlatformRoleApproval(r.Context(), access.PlatformRoleApprovalExecuteInput{ApprovalID: chi.URLParam(r, "approval"), ActorID: h.currentPrincipalID(r), IdempotencyKey: r.Header.Get("Idempotency-Key")})
		if err != nil {
			return nil, err
		}
		action := "platform_admin.granted"
		if result.Action == "revoke" {
			action = "platform_admin.revoked"
		}
		approvalAudit, auditErr := platformRoleApprovalAuditInput(r, accessgen.GenCommandOperationExecutePlatformRoleApproval(), h.currentPrincipalID(r), "platform_role_approval.executed", result)
		if auditErr != nil {
			return nil, auditErr
		}
		return []access.AuditEventInput{
			approvalAudit,
			auditInput(r, action, h.currentPrincipalID(r), "platform_role_binding", result.BindingID, "", "success", map[string]any{"principalId": result.PrincipalID, "bindingId": result.BindingID, "revision": result.ResultRevision, "approvalId": result.ID}),
		}, nil
	})
	if err != nil {
		h.recordDeniedPlatformAttempt(r, "platform_role_approval.executed", "platform_role_approval", platformRoleApprovalRequestTarget(r), auditDenialReason(err), nil)
		writePlatformRoleApprovalError(w, err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, platformRoleApprovalDTO(result))
}

func platformRoleApprovalRequestTarget(r *stdhttp.Request) string {
	if r == nil {
		return "platform-role-approval"
	}
	if approval := strings.TrimSpace(chi.URLParam(r, "approval")); approval != "" {
		return approval
	}
	if r.URL != nil && strings.TrimSpace(r.URL.Path) != "" {
		return r.URL.Path
	}
	return "platform-role-approval"
}

// executePlatformRoleApprovalMutation preserves the command runtime's
// completion guard while keeping the domain repository responsible for the
// transaction and its durable audit event.
func executePlatformRoleApprovalMutation(r *stdhttp.Request, repo access.Repository, operation accessgen.GenCommandOperationID, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	if _, generated := apigencommand.OperationID(r.Context()); !generated {
		return runAuditedMutation(r, repo, mutation)
	}
	executor, err := apigencommand.NewExecutor(accessgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	return executor.Execute(r.Context(), operation.APIGenOperationID(), apigencommand.Execution{Transactional: func(context.Context, apigencommand.Contract) error {
		return runAuditedMutation(r, repo, mutation)
	}})
}

func executePlatformRoleApprovalBatch(r *stdhttp.Request, repo access.Repository, operation accessgen.GenCommandOperationID, mutation func(access.Repository) ([]access.AuditEventInput, error)) error {
	if _, generated := apigencommand.OperationID(r.Context()); !generated {
		transactional, ok := repo.(access.AuditedMutationBatchRepository)
		if !ok {
			return errors.New("transactional access repository is required")
		}
		return transactional.RunAuditedMutationBatch(r.Context(), mutation)
	}
	executor, err := apigencommand.NewExecutor(accessgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	return executor.Execute(r.Context(), operation.APIGenOperationID(), apigencommand.Execution{Transactional: func(context.Context, apigencommand.Contract) error {
		transactional, ok := repo.(access.AuditedMutationBatchRepository)
		if !ok {
			return errors.New("transactional access repository is required")
		}
		return transactional.RunAuditedMutationBatch(r.Context(), mutation)
	}})
}

func platformRoleApprovalAuditInput(r *stdhttp.Request, operation accessgen.GenCommandOperationID, actorID, action string, result access.PlatformRoleApproval) (access.AuditEventInput, error) {
	payload := accessgen.GenSchemaPlatformRoleApprovalAuditPayload{Action: result.Action, ApprovalId: result.ID, PrincipalId: result.PrincipalID, Revision: result.Revision, Status: string(result.Status)}
	var encoded string
	var err error
	switch operation {
	case accessgen.GenCommandOperationRequestPlatformRoleApproval():
		encoded, err = accessgen.EncodeGenRequestPlatformRoleApprovalAuditPayload(payload)
	case accessgen.GenCommandOperationApprovePlatformRoleApproval():
		encoded, err = accessgen.EncodeGenApprovePlatformRoleApprovalAuditPayload(payload)
	case accessgen.GenCommandOperationCancelPlatformRoleApproval():
		encoded, err = accessgen.EncodeGenCancelPlatformRoleApprovalAuditPayload(payload)
	case accessgen.GenCommandOperationExpirePlatformRoleApproval():
		encoded, err = accessgen.EncodeGenExpirePlatformRoleApprovalAuditPayload(payload)
	case accessgen.GenCommandOperationExecutePlatformRoleApproval():
		encoded, err = accessgen.EncodeGenExecutePlatformRoleApprovalAuditPayload(payload)
	default:
		err = access.ErrPlatformRoleApprovalInvalid
	}
	if err != nil {
		return access.AuditEventInput{}, err
	}
	// The audit actor is the authenticated principal making this lifecycle
	// transition; the requester's identity remains in the typed payload's
	// resource record and durable approval row.
	event := auditInput(r, action, actorID, "platform_role_approval", result.ID, "", "success", nil)
	event.MetadataJSON = encoded
	return event, nil
}

func writePlatformRoleApprovalError(w stdhttp.ResponseWriter, err error) {
	status := stdhttp.StatusInternalServerError
	switch {
	case errors.Is(err, access.ErrPlatformRoleApprovalInvalid):
		status = stdhttp.StatusBadRequest
	case errors.Is(err, access.ErrPlatformRoleApprovalNotFound):
		status = stdhttp.StatusNotFound
	case errors.Is(err, access.ErrPlatformRoleApprovalSeparationOfDuty):
		status = stdhttp.StatusForbidden
	case errors.Is(err, access.ErrPlatformRoleApprovalConflict), errors.Is(err, access.ErrPlatformRoleApprovalExpired), errors.Is(err, access.ErrPlatformRoleApprovalNotDue), errors.Is(err, access.ErrPlatformAdminStaleRevision):
		status = stdhttp.StatusConflict
	}
	writeJSONError(w, fmt.Errorf("%w", err), status)
}
