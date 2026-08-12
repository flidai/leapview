package product

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/go-chi/chi/v5"
)

type Principal struct{ ID string }

type AuthenticationStatus struct {
	BrowserEnabled bool              `json:"browserEnabled"`
	APITokenOnly   bool              `json:"apiTokenOnly"`
	Local          Availability      `json:"local"`
	OIDC           NamedAvailability `json:"oidc"`
	Azure          Availability      `json:"azure"`
	SCIM           Availability      `json:"scim"`
	ManagedBy      string            `json:"managedBy"`
}

type Availability struct {
	Available bool `json:"available"`
	Enabled   bool `json:"enabled"`
}

type NamedAvailability struct {
	Available bool   `json:"available"`
	Enabled   bool   `json:"enabled"`
	Provider  string `json:"provider,omitempty"`
}

type APIStatus struct {
	BearerCredentials Availability `json:"bearerCredentials"`
	ServicePrincipals Availability `json:"servicePrincipals"`
	OAuth             Availability `json:"oauth"`
	MCP               Availability `json:"mcp"`
	ExternalMCPIssuer bool         `json:"externalMcpIssuer"`
}

type AgentStatus struct {
	Available       bool   `json:"available"`
	Configured      bool   `json:"configured"`
	Provider        string `json:"provider,omitempty"`
	ModelConfigured bool   `json:"modelConfigured"`
}

type Limits struct {
	QueryResultMaxRows          int   `json:"queryResultMaxRows"`
	QueryResultMaxBytes         int64 `json:"queryResultMaxBytes"`
	ManagedDataMaxFiles         int   `json:"managedDataMaxFiles"`
	ManagedDataMaxFileBytes     int64 `json:"managedDataMaxFileBytes"`
	ManagedDataMaxRevisionBytes int64 `json:"managedDataMaxRevisionBytes"`
}

type SystemStatus struct {
	InstanceID      string             `json:"instanceId"`
	CanonicalOrigin string             `json:"canonicalOrigin"`
	Environment     string             `json:"environment"`
	Build           buildinfo.Identity `json:"build"`
	ControlPlane    string             `json:"controlPlane"`
	StorageBackend  string             `json:"storageBackend"`
	Agent           AgentStatus        `json:"agent"`
	Limits          Limits             `json:"limits"`
}

// Status is a redacted snapshot assembled from deployment configuration.
// It intentionally contains no credential values, client identifiers, issuer
// URLs, callback URLs, filesystem paths, buckets, or tenant identifiers.
type Status struct {
	Authentication AuthenticationStatus
	API            APIStatus
	System         SystemStatus
}

type HTTPConfig struct {
	Service          *Service
	Status           Status
	CurrentPrincipal func(*http.Request) (Principal, bool)
	CommandFailure   CommandFailureWriter
	Logger           *slog.Logger
}

type CommandFailureWriter func(context.Context, http.ResponseWriter, *http.Request, string, error)

type Handler struct{ config HTTPConfig }

func NewHandler(config HTTPConfig) (*Handler, error) {
	if config.Service == nil {
		return nil, fmt.Errorf("product identity service is required")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Handler{config: config}, nil
}

type SettingsResponse struct {
	DisplayName string        `json:"displayName"`
	Logo        *LogoResponse `json:"logo,omitempty"`
	UpdatedAt   string        `json:"updatedAt"`
}

type LogoResponse struct {
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	MediaType string `json:"mediaType"`
	SizeBytes int64  `json:"sizeBytes"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

func (h *Handler) GetSettings(w http.ResponseWriter, r *http.Request) {
	identity, err := h.config.Service.Get(r.Context())
	if err != nil {
		h.problem(w, r, err)
		return
	}
	h.writeIdentity(w, http.StatusOK, identity)
}

func (h *Handler) PatchSettings(w http.ResponseWriter, r *http.Request) {
	expected, ok := h.requireRevision(w, r)
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(r.Header.Get("Content-Type")))
	if err != nil || mediaType != "application/json" {
		apitransport.WriteProblem(w, r, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json", nil)
		return
	}
	var body struct {
		DisplayName *string `json:"displayName"`
	}
	if err := apitransport.DecodeBody(w, r, &body); err != nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST_BODY", "The request body must be a JSON object containing displayName", nil)
		return
	}
	if body.DisplayName == nil {
		apitransport.WriteProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST", "displayName is required", []apitransport.ProblemFieldError{{Field: "displayName", Code: "REQUIRED", Detail: "displayName is required"}})
		return
	}
	identity, err := h.config.Service.SetDisplayName(r.Context(), expected, *body.DisplayName, h.mutation(r))
	if err != nil {
		h.problem(w, r, err)
		return
	}
	h.writeIdentity(w, http.StatusOK, identity)
}

func (h *Handler) UploadLogo(w http.ResponseWriter, r *http.Request) {
	expected, ok := h.requireRevision(w, r)
	if !ok {
		return
	}
	identity, err := h.config.Service.UploadLogo(r.Context(), expected, r.Header.Get("Content-Type"), r.Body, h.mutation(r))
	if err != nil {
		h.problem(w, r, err)
		return
	}
	h.writeIdentity(w, http.StatusOK, identity)
}

func (h *Handler) DeleteLogo(w http.ResponseWriter, r *http.Request) {
	expected, ok := h.requireRevision(w, r)
	if !ok {
		return
	}
	identity, err := h.config.Service.DeleteLogo(r.Context(), expected, h.mutation(r))
	if err != nil {
		h.problem(w, r, err)
		return
	}
	h.writeIdentity(w, http.StatusOK, identity)
}

func (h *Handler) GetLogo(w http.ResponseWriter, r *http.Request) {
	reader, logo, err := h.config.Service.OpenLogo(r.Context(), chi.URLParam(r, "digest"))
	if err != nil {
		h.problem(w, r, err)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", logo.MediaType)
	w.Header().Set("Content-Length", strconv.FormatInt(logo.SizeBytes, 10))
	w.Header().Set("ETag", `"`+logo.SHA256+`"`)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, reader)
}

func (h *Handler) GetAuthentication(w http.ResponseWriter, _ *http.Request) {
	apitransport.WriteJSON(w, http.StatusOK, h.config.Status.Authentication)
}

func (h *Handler) GetSystem(w http.ResponseWriter, r *http.Request) {
	status := h.config.Status.System
	status.ControlPlane = "available"
	if err := h.config.Service.db.PingContext(r.Context()); err != nil {
		status.ControlPlane = "unavailable"
	}
	apitransport.WriteJSON(w, http.StatusOK, status)
}

func (h *Handler) GetAPIStatus(w http.ResponseWriter, _ *http.Request) {
	apitransport.WriteJSON(w, http.StatusOK, h.config.Status.API)
}

func (h *Handler) writeIdentity(w http.ResponseWriter, status int, identity Identity) {
	w.Header().Set("ETag", revisionETag(identity.Revision))
	apitransport.WriteJSON(w, status, settingsResponse(identity))
}

func settingsResponse(identity Identity) SettingsResponse {
	result := SettingsResponse{DisplayName: identity.DisplayName, UpdatedAt: identity.UpdatedAt}
	if identity.Logo != nil {
		logo := identity.Logo
		result.Logo = &LogoResponse{URL: "/api/v1/instance/logo/" + logo.SHA256, SHA256: logo.SHA256, MediaType: logo.MediaType, SizeBytes: logo.SizeBytes, Width: logo.Width, Height: logo.Height}
	}
	return result
}

func revisionETag(revision int64) string { return `"product-` + strconv.FormatInt(revision, 10) + `"` }

func (h *Handler) requireRevision(w http.ResponseWriter, r *http.Request) (int64, bool) {
	value := strings.TrimSpace(r.Header.Get("If-Match"))
	if value == "" {
		apitransport.WriteProblem(w, r, http.StatusPreconditionFailed, "IF_MATCH_REQUIRED", "If-Match is required", nil)
		return 0, false
	}
	value = strings.Trim(value, `"`)
	text, ok := strings.CutPrefix(value, "product-")
	if !ok {
		if _, generatedCommand := apigencommand.OperationID(r.Context()); generatedCommand && h.config.CommandFailure != nil {
			h.problem(w, r, ErrPrecondition)
			return 0, false
		}
		apitransport.WriteProblem(w, r, http.StatusPreconditionFailed, "ETAG_MISMATCH", "If-Match does not match the current product identity", nil)
		return 0, false
	}
	revision, err := strconv.ParseInt(text, 10, 64)
	if err != nil || revision <= 0 {
		if _, generatedCommand := apigencommand.OperationID(r.Context()); generatedCommand && h.config.CommandFailure != nil {
			h.problem(w, r, ErrPrecondition)
			return 0, false
		}
		apitransport.WriteProblem(w, r, http.StatusPreconditionFailed, "ETAG_MISMATCH", "If-Match does not match the current product identity", nil)
		return 0, false
	}
	return revision, true
}

func (h *Handler) mutation(r *http.Request) Mutation {
	mutation := Mutation{
		RequestID:        r.Header.Get("X-Request-ID"),
		CorrelationID:    r.Header.Get("X-Correlation-ID"),
		ConcurrencyToken: r.Header.Get("If-Match"),
	}
	if h.config.CurrentPrincipal != nil {
		if principal, ok := h.config.CurrentPrincipal(r); ok {
			mutation.PrincipalID = principal.ID
		}
	}
	return mutation
}

func (h *Handler) problem(w http.ResponseWriter, r *http.Request, err error) {
	if operationID, generatedCommand := apigencommand.OperationID(r.Context()); generatedCommand && h.config.CommandFailure != nil {
		h.config.CommandFailure(r.Context(), w, r, operationID, err)
		return
	}
	switch {
	case errors.Is(err, ErrInvalid):
		apitransport.WriteProblem(w, r, http.StatusUnprocessableEntity, "INVALID_PRODUCT_IDENTITY", err.Error(), nil)
	case errors.Is(err, ErrPrecondition):
		apitransport.WriteProblem(w, r, http.StatusPreconditionFailed, "ETAG_MISMATCH", "If-Match does not match the current product identity", nil)
	case errors.Is(err, ErrNotFound):
		apitransport.WriteProblem(w, r, http.StatusNotFound, "PRODUCT_LOGO_NOT_FOUND", "The product logo does not exist", nil)
	default:
		h.config.Logger.ErrorContext(r.Context(), "product administration request failed", "error", err)
		apitransport.WriteProblem(w, r, http.StatusInternalServerError, "PRODUCT_ADMINISTRATION_FAILED", "The product administration request could not be completed", nil)
	}
}
