package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	api "github.com/flidai/leapview/internal/platform/http/model"
	httptransport "github.com/flidai/leapview/internal/platform/http/transport"
)

type CreateHeaders struct{ IdempotencyKey string }
type ActivateHeaders struct{ IdempotencyKey string }
type createRequest struct {
	Environment       string `json:"environment"`
	GenerationID      string `json:"generationId"`
	ArtifactDigest    string `json:"artifactDigest"`
	PriorGenerationID string `json:"priorGenerationId,omitempty"`
}
type deploymentResponse struct {
	ID                  string  `json:"id"`
	Project             string  `json:"project"`
	Environment         string  `json:"environment"`
	GenerationID        string  `json:"generationId"`
	ArtifactDigest      string  `json:"artifactDigest"`
	PriorGenerationID   *string `json:"priorGenerationId,omitempty"`
	RequestDigest       string  `json:"requestDigest"`
	Status              string  `json:"status"`
	CreatedAt           string  `json:"createdAt"`
	ActivatedAt         *string `json:"activatedAt,omitempty"`
	ActivationPrincipal *string `json:"activationPrincipal,omitempty"`
	VerificationDigest  *string `json:"verificationDigest,omitempty"`
	VerifiedAt          *string `json:"verifiedAt,omitempty"`
	Error               *string `json:"error,omitempty"`
}

func (h *Handler) Create(w stdhttp.ResponseWriter, r *stdhttp.Request, project string, headers CreateHeaders) {
	principal, ok := h.principal(r)
	if !ok {
		writeError(w, fmt.Errorf("authenticated principal is required"), stdhttp.StatusUnauthorized)
		return
	}
	if strings.TrimSpace(headers.IdempotencyKey) == "" {
		writeError(w, fmt.Errorf("Idempotency-Key is required"), stdhttp.StatusBadRequest)
		return
	}
	var body createRequest
	if err := decodeJSON(w, r, &body, h.options.MaxJSONBodyBytes); err != nil {
		writeError(w, err, stdhttp.StatusBadRequest)
		return
	}
	if h.options.InstanceEnvironment != "" && body.Environment != h.options.InstanceEnvironment {
		writeEnvironmentConflict(w, body.Environment, h.options.InstanceEnvironment)
		return
	}
	result, err := h.options.Coordinator.Create(r.Context(), apiadapter.CreateRequest{
		Project: project, Environment: body.Environment, GenerationID: body.GenerationID, ArtifactDigest: body.ArtifactDigest, PriorGenerationID: body.PriorGenerationID, Actor: principal.ID, IdempotencyKey: headers.IdempotencyKey,
	})
	if err != nil {
		h.writePublicError(w, r, err)
		return
	}
	writeJSON(w, stdhttp.StatusCreated, response(result))
}

func (h *Handler) Get(w stdhttp.ResponseWriter, r *stdhttp.Request, project, deploymentID string) {
	result, err := h.options.Coordinator.Get(r.Context(), apiadapter.Scope{Project: project, DeploymentID: deploymentID})
	if err != nil {
		h.writePublicError(w, r, err)
		return
	}
	if !h.environmentAllowed(w, result.Environment) {
		return
	}
	writeJSON(w, stdhttp.StatusOK, response(result))
}

func (h *Handler) Activate(w stdhttp.ResponseWriter, r *stdhttp.Request, project, deploymentID string, headers ActivateHeaders) {
	principal, ok := h.principal(r)
	if !ok {
		writeError(w, fmt.Errorf("authenticated principal is required"), stdhttp.StatusUnauthorized)
		return
	}
	if strings.TrimSpace(headers.IdempotencyKey) == "" {
		writeError(w, fmt.Errorf("Idempotency-Key is required"), stdhttp.StatusBadRequest)
		return
	}
	if h.options.InstanceEnvironment != "" {
		deployment, err := h.options.Coordinator.Get(r.Context(), apiadapter.Scope{Project: project, DeploymentID: deploymentID})
		if err != nil {
			h.writePublicError(w, r, err)
			return
		}
		if !h.environmentAllowed(w, deployment.Environment) {
			return
		}
	}
	result, err := h.options.Coordinator.Activate(r.Context(), apiadapter.ActivateRequest{
		Scope: apiadapter.Scope{Project: project, DeploymentID: deploymentID}, Actor: principal.ID, IdempotencyKey: headers.IdempotencyKey,
	})
	if err != nil {
		h.writePublicError(w, r, err)
		return
	}
	writeJSON(w, stdhttp.StatusOK, response(result))
}

func (h *Handler) environmentAllowed(w stdhttp.ResponseWriter, environment string) bool {
	if h.options.InstanceEnvironment == "" || environment == h.options.InstanceEnvironment {
		return true
	}
	writeEnvironmentConflict(w, environment, h.options.InstanceEnvironment)
	return false
}

func (h *Handler) principal(r *stdhttp.Request) (Principal, bool) {
	if h.options.CurrentPrincipal == nil {
		return Principal{}, false
	}
	principal, ok := h.options.CurrentPrincipal(r)
	return principal, ok && strings.TrimSpace(principal.ID) != ""
}

func response(value apiadapter.Deployment) deploymentResponse {
	result := deploymentResponse{
		ID: value.ID, Project: value.Project, Environment: value.Environment, GenerationID: value.GenerationID, ArtifactDigest: value.ArtifactDigest, RequestDigest: value.RequestDigest,
		Status: string(value.Status), CreatedAt: value.CreatedAt,
	}
	if value.PriorGenerationID != "" {
		result.PriorGenerationID = &value.PriorGenerationID
	}
	if value.ActivatedAt != "" {
		result.ActivatedAt = &value.ActivatedAt
	}
	if value.ActivationPrincipal != "" {
		result.ActivationPrincipal = &value.ActivationPrincipal
	}
	if value.VerificationDigest != "" {
		result.VerificationDigest = &value.VerificationDigest
	}
	if value.VerifiedAt != "" {
		result.VerifiedAt = &value.VerifiedAt
	}
	if value.Error != "" {
		result.Error = &value.Error
	}
	return result
}

func decodeJSON(w stdhttp.ResponseWriter, r *stdhttp.Request, destination any, limit int64) error {
	r.Body = stdhttp.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("malformed JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("malformed JSON: multiple JSON values")
		}
		return fmt.Errorf("malformed JSON: %w", err)
	}
	return nil
}

func statusFor(err error) int {
	if errors.Is(err, apiadapter.ErrInvalid) {
		return stdhttp.StatusBadRequest
	}
	if errors.Is(err, deployment.ErrNotFound) {
		return stdhttp.StatusNotFound
	}
	if errors.Is(err, deployment.ErrConflict) {
		return stdhttp.StatusConflict
	}
	return stdhttp.StatusInternalServerError
}

func (h *Handler) writePublicError(w stdhttp.ResponseWriter, r *stdhttp.Request, err error) {
	status := statusFor(err)
	if status == stdhttp.StatusInternalServerError {
		h.options.Logger.ErrorContext(r.Context(), "project deployment request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, fmt.Errorf("internal server error"), status)
		return
	}
	writeError(w, err, status)
}

func writeJSON(w stdhttp.ResponseWriter, status int, value any) {
	httptransport.WriteJSON(w, status, value)
}

func writeError(w stdhttp.ResponseWriter, err error, status int) {
	writeJSON(w, status, api.ErrorResponse{Code: status, Message: err.Error(), Details: map[string]any{}, RequestID: ""})
}

func writeEnvironmentConflict(w stdhttp.ResponseWriter, requested, instance string) {
	writeJSON(w, stdhttp.StatusConflict, api.ErrorResponse{
		Code:      stdhttp.StatusConflict,
		Message:   fmt.Sprintf("requested environment %q does not match instance environment %q", requested, instance),
		Details:   map[string]any{"requestedEnvironment": requested, "instanceEnvironment": instance},
		RequestID: "",
	})
}
