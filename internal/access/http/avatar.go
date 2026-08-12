package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	stdhttp "net/http"
	"strconv"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/access/avatar"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
)

const avatarCacheControl = "private, max-age=31536000, immutable"

type AvatarService interface {
	Upload(context.Context, string, io.Reader) (avatar.Metadata, error)
	Current(context.Context, string) (avatar.Metadata, error)
	Open(context.Context, string, string) (io.ReadCloser, avatar.Metadata, error)
	Delete(context.Context, string) error
}

func (h Handler) UploadCurrentAvatar(w stdhttp.ResponseWriter, r *stdhttp.Request, contentType string) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeAvatarProblem(w, r, stdhttp.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "An authenticated user is required")
		return
	}
	if h.rejectAuthoringCredential(w, r) {
		return
	}
	if principal.Kind != "" && principal.Kind != "user" {
		writeAvatarProblem(w, r, stdhttp.StatusForbidden, "AVATAR_USER_REQUIRED", "Only user principals can have avatars")
		return
	}
	if h.Avatar == nil {
		writeAvatarProblem(w, r, stdhttp.StatusServiceUnavailable, "AVATAR_STORAGE_UNAVAILABLE", "Avatar storage is unavailable")
		return
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || !supportedAvatarMediaType(mediaType) {
		writeAvatarProblem(w, r, stdhttp.StatusUnsupportedMediaType, "UNSUPPORTED_AVATAR_MEDIA_TYPE", "Avatar uploads require image/jpeg, image/png, or image/webp")
		return
	}
	metadata, err := h.Avatar.Upload(r.Context(), principal.ID, stdhttp.MaxBytesReader(w, r.Body, avatar.MaxUploadBytes+1))
	if err != nil {
		switch {
		case errors.Is(err, avatar.ErrTooLarge):
			writeAvatarProblem(w, r, stdhttp.StatusRequestEntityTooLarge, "AVATAR_TOO_LARGE", fmt.Sprintf("Avatar uploads must not exceed %d bytes", avatar.MaxUploadBytes))
		case errors.Is(err, avatar.ErrInvalid):
			writeAvatarProblem(w, r, stdhttp.StatusUnprocessableEntity, "INVALID_AVATAR_IMAGE", err.Error())
		default:
			writeAvatarProblem(w, r, stdhttp.StatusInternalServerError, "AVATAR_UPLOAD_FAILED", "The avatar could not be stored")
		}
		return
	}
	auditPayload, err := accessgen.EncodeGenUploadCurrentAvatarAuditPayload(accessgen.GenSchemaCurrentAvatarUploadedAuditPayload{
		PrincipalId: principal.ID,
		Sha256:      metadata.SHA256,
	})
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUploadCurrentAvatar(), err)
		return
	}
	if err := h.completeAvatarCommand(r, accessgen.GenCommandOperationUploadCurrentAvatar(), principal.ID, "principal.avatar.updated", auditPayload); err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationUploadCurrentAvatar(), err)
		return
	}
	apitransport.WriteJSON(w, stdhttp.StatusOK, avatarResponse(metadata))
}

func (h Handler) GetPrincipalAvatar(w stdhttp.ResponseWriter, r *stdhttp.Request, principalID, digest string) {
	if h.Avatar == nil {
		writeAvatarProblem(w, r, stdhttp.StatusServiceUnavailable, "AVATAR_STORAGE_UNAVAILABLE", "Avatar storage is unavailable")
		return
	}
	reader, metadata, err := h.Avatar.Open(r.Context(), principalID, digest)
	if err != nil {
		if errors.Is(err, avatar.ErrNotFound) {
			writeAvatarProblem(w, r, stdhttp.StatusNotFound, "AVATAR_NOT_FOUND", "The avatar does not exist")
			return
		}
		writeAvatarProblem(w, r, stdhttp.StatusInternalServerError, "AVATAR_READ_FAILED", "The avatar could not be read")
		return
	}
	defer reader.Close()
	etag := `"` + metadata.SHA256 + `"`
	w.Header().Set("Content-Type", metadata.MediaType)
	w.Header().Set("Content-Length", strconv.FormatInt(metadata.SizeBytes, 10))
	w.Header().Set("Cache-Control", avatarCacheControl)
	w.Header().Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(stdhttp.StatusNotModified)
		return
	}
	w.WriteHeader(stdhttp.StatusOK)
	_, _ = io.Copy(w, reader)
}

func (h Handler) DeleteCurrentAvatar(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeAvatarProblem(w, r, stdhttp.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "An authenticated user is required")
		return
	}
	if h.rejectAuthoringCredential(w, r) {
		return
	}
	if h.Avatar == nil {
		writeAvatarProblem(w, r, stdhttp.StatusServiceUnavailable, "AVATAR_STORAGE_UNAVAILABLE", "Avatar storage is unavailable")
		return
	}
	if err := h.Avatar.Delete(r.Context(), principal.ID); err != nil && !errors.Is(err, avatar.ErrNotFound) {
		writeAvatarProblem(w, r, stdhttp.StatusInternalServerError, "AVATAR_DELETE_FAILED", "The avatar could not be deleted")
		return
	}
	auditPayload, err := accessgen.EncodeGenDeleteCurrentAvatarAuditPayload(accessgen.GenSchemaCurrentAvatarDeletedAuditPayload{PrincipalId: principal.ID})
	if err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeleteCurrentAvatar(), err)
		return
	}
	if err := h.completeAvatarCommand(r, accessgen.GenCommandOperationDeleteCurrentAvatar(), principal.ID, "principal.avatar.deleted", auditPayload); err != nil {
		writeCommandFailure(w, r, accessgen.GenCommandOperationDeleteCurrentAvatar(), err)
		return
	}
	w.WriteHeader(stdhttp.StatusNoContent)
}

func (h Handler) completeAvatarCommand(r *stdhttp.Request, operationID accessgen.GenCommandOperationID, principalID, action, auditPayload string) error {
	if _, generatedCommand := apigencommand.OperationID(r.Context()); !generatedCommand {
		return nil
	}
	executor, err := apigencommand.NewExecutor(accessgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	return executor.Execute(r.Context(), operationID.APIGenOperationID(), apigencommand.Execution{
		BestEffortAudit: func(ctx context.Context, contract apigencommand.Contract) error {
			if contract.AuditAction != action {
				return fmt.Errorf("generated audit action %q does not match avatar mutation action %q", contract.AuditAction, action)
			}
			repository, err := h.repository()
			if err != nil {
				return err
			}
			return repository.RecordAuditEvent(ctx, access.AuditEventInput{
				PrincipalID:   principalID,
				Action:        action,
				TargetType:    "principal",
				TargetID:      principalID,
				Status:        "success",
				RequestID:     requestIDFromRequest(r),
				CorrelationID: correlationIDFromRequest(r),
				MetadataJSON:  auditPayload,
			})
		},
	})
}

func avatarResponse(value avatar.Metadata) accessgen.AvatarResponse {
	return accessgen.AvatarResponse{
		PrincipalId: value.PrincipalID,
		Sha256:      value.SHA256,
		MediaType:   value.MediaType,
		SizeBytes:   value.SizeBytes,
		Width:       int32(value.Width),
		Height:      int32(value.Height),
		UpdatedAt:   normalizeTimestamp(value.UpdatedAt),
		Url:         avatar.URL(value),
	}
}

func supportedAvatarMediaType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image/jpeg", "image/png", "image/webp":
		return true
	default:
		return false
	}
}

func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag || candidate == "W/"+etag {
			return true
		}
	}
	return false
}

func writeAvatarProblem(w stdhttp.ResponseWriter, r *stdhttp.Request, status int, code, detail string) {
	apitransport.WriteProblem(w, r, status, code, detail, nil)
}
