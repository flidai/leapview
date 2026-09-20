package http

import (
	"database/sql"
	"errors"
	stdhttp "net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/go-chi/chi/v5"
)

var (
	errOwnershipResolutionInvalid   = errors.New("ownership resolution request is invalid")
	errOwnershipMutationUnavailable = errors.New("ownership mutation is unavailable")
	errOwnershipResolutionMalformed = errors.New("ownership resolution request body is malformed")
)

// ResolvePrincipalOwnership transfers or tombstones all live objects owned by
// the path principal. Authorization is evaluated before request parsing, and
// the repository mutation is run through the same audited transaction used by
// other privileged access commands. Product domains retain ownership of their
// own mutation details; this handler only validates the command envelope.
func (h Handler) ResolvePrincipalOwnership(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}

	repo, err := h.repository()
	if err != nil {
		writeOwnershipResolutionError(w, err)
		return
	}
	mutationRepository, ok := repo.(access.OwnershipMutationRepository)
	if !ok || mutationRepository == nil {
		writeOwnershipResolutionError(w, errOwnershipMutationUnavailable)
		return
	}

	principalID := strings.TrimSpace(chi.URLParam(r, "principal"))
	if principalID == "" {
		writeOwnershipResolutionError(w, invalidOwnershipResolution("principal is required"))
		return
	}
	principal, err := repo.PrincipalByID(r.Context(), principalID)
	if err != nil {
		writeOwnershipResolutionError(w, err)
		return
	}
	if strings.TrimSpace(principal.ID) == "" {
		writeOwnershipResolutionError(w, invalidOwnershipResolution("principal identity is invalid"))
		return
	}

	var request accessgen.OwnershipResolutionRequest
	if err := decodeStrictJSON(r, &request); err != nil {
		writeOwnershipResolutionError(w, errors.Join(errOwnershipResolutionMalformed, err))
		return
	}
	action, targetPrincipalID, err := validateOwnershipResolutionRequest(request, principalID)
	if err != nil {
		writeOwnershipResolutionError(w, err)
		return
	}

	var report access.OwnershipReport
	operation := accessgen.GenCommandOperationResolvePrincipalOwnership()
	err = executeAuditedMutation(r, repo, operation, func(tx access.Repository) (access.AuditEventInput, error) {
		txMutationRepository, ok := tx.(access.OwnershipMutationRepository)
		if !ok || txMutationRepository == nil {
			return access.AuditEventInput{}, errOwnershipMutationUnavailable
		}
		// Revalidate the source and transfer target on the transaction-bound
		// repository immediately before the owning-domain mutation. This keeps
		// target lifecycle checks inside the audited mutation transaction.
		currentPrincipal, err := tx.PrincipalByID(r.Context(), principalID)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		if strings.TrimSpace(currentPrincipal.ID) == "" {
			return access.AuditEventInput{}, invalidOwnershipResolution("principal identity is invalid")
		}
		if action == "transfer" {
			target, targetErr := tx.PrincipalByID(r.Context(), targetPrincipalID)
			if targetErr != nil {
				if errors.Is(targetErr, sql.ErrNoRows) {
					return access.AuditEventInput{}, invalidOwnershipResolution("target principal was not found")
				}
				return access.AuditEventInput{}, targetErr
			}
			if !ownershipTargetPrincipalAllowed(target) {
				return access.AuditEventInput{}, invalidOwnershipResolution("target principal must be enabled and locally managed")
			}
			report, err = txMutationRepository.TransferOwnedObjects(r.Context(), principalID, targetPrincipalID)
		} else {
			report, err = txMutationRepository.TombstoneOwnedObjects(r.Context(), principalID)
		}
		if err != nil {
			return access.AuditEventInput{}, err
		}
		metadata, encodeErr := accessgen.EncodeGenResolvePrincipalOwnershipAuditPayload(accessgen.GenSchemaOwnershipResolutionAuditPayload{
			PrincipalId: principalID, Action: action, TargetPrincipalId: targetPrincipalID, ObjectCount: int32(len(report.Objects)),
		})
		if encodeErr != nil {
			return access.AuditEventInput{}, encodeErr
		}
		event := auditInput(r, "principal.ownership.resolved", h.currentPrincipalID(r), "principal", principalID, "", "success", nil)
		event.MetadataJSON = metadata
		return event, nil
	})
	if err != nil {
		writeOwnershipResolutionError(w, err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, ownershipResolutionDTO(principalID, action, targetPrincipalID, report))
}

func validateOwnershipResolutionRequest(request accessgen.OwnershipResolutionRequest, principalID string) (string, string, error) {
	action := strings.TrimSpace(request.Action)
	target := strings.TrimSpace(request.TargetPrincipalId)
	switch action {
	case "transfer":
		if target == "" {
			return "", "", invalidOwnershipResolution("targetPrincipalId is required for transfer")
		}
		if target == principalID {
			return "", "", invalidOwnershipResolution("targetPrincipalId must differ from principal")
		}
	case "tombstone":
		if target != "" {
			return "", "", invalidOwnershipResolution("targetPrincipalId must be empty for tombstone")
		}
	default:
		return "", "", invalidOwnershipResolution("action must be transfer or tombstone")
	}
	return action, target, nil
}

func ownershipTargetPrincipalAllowed(principal access.Principal) bool {
	if principal.AccessDisabled() {
		return false
	}
	return principal.Kind == "" || principal.Kind == access.PrincipalKindUser || principal.Kind == access.PrincipalKindServicePrincipal
}

func invalidOwnershipResolution(detail string) error {
	return errors.Join(errOwnershipResolutionInvalid, errors.New(detail))
}

func ownershipResolutionDTO(principalID, action, targetPrincipalID string, report access.OwnershipReport) accessgen.GenSchemaOwnershipResolutionResponse {
	objects := make([]accessgen.GenSchemaOwnedObject, 0, len(report.Objects))
	for _, object := range report.Objects {
		var name *string
		if strings.TrimSpace(object.Name) != "" {
			value := object.Name
			name = &value
		}
		objects = append(objects, accessgen.GenSchemaOwnedObject{
			Kind: object.Kind, Id: object.ID, Name: name, OwnerPrincipalId: object.OwnerPrincipalID,
			Lifecycle: object.Lifecycle, Transferable: object.Transferable, TombstoneOnOffboard: object.TombstoneOnOffboard,
		})
	}
	return accessgen.GenSchemaOwnershipResolutionResponse{PrincipalId: principalID, Action: action, Objects: objects}
}

func writeOwnershipResolutionError(w stdhttp.ResponseWriter, err error) {
	status := stdhttp.StatusInternalServerError
	code := "INTERNAL_ERROR"
	switch {
	case errors.Is(err, errOwnershipResolutionMalformed):
		status, code = stdhttp.StatusBadRequest, "INVALID_REQUEST_BODY"
	case errors.Is(err, errOwnershipResolutionInvalid):
		status, code = stdhttp.StatusUnprocessableEntity, "OWNERSHIP_RESOLUTION_INVALID"
	case errors.Is(err, sql.ErrNoRows):
		status, code = stdhttp.StatusNotFound, "PRINCIPAL_NOT_FOUND"
	case errors.Is(err, errOwnershipMutationUnavailable), errors.Is(err, access.ErrOffboardingUnavailable):
		status, code = stdhttp.StatusServiceUnavailable, "OWNERSHIP_MUTATION_UNAVAILABLE"
	case errors.Is(err, access.ErrOwnershipConflict):
		status, code = stdhttp.StatusConflict, "PRINCIPAL_OWNS_OBJECTS"
	}
	detail := err.Error()
	writeJSON(w, status, map[string]any{"code": code, "detail": detail, "error": detail})
}
