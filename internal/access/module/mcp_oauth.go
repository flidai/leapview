package module

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessui "github.com/flidai/leapview/internal/access/ui"
	"github.com/gorilla/csrf"
)

func (s *Module) OAuthToken(w http.ResponseWriter, r *http.Request) {
	if requestTargetsAuthoringOAuth(r) {
		s.AuthoringOAuthToken(w, r)
		return
	}
	if requestTargetsMCPOAuth(r) {
		s.MCPOAuthToken(w, r)
		return
	}
	s.handler.OAuthToken(w, r)
}

func (s *Module) OAuthRevoke(w http.ResponseWriter, r *http.Request) {
	if requestTargetsAuthoringOAuthRevoke(r) {
		s.AuthoringOAuthRevoke(w, r)
		return
	}
	s.MCPOAuthRevoke(w, r)
}

func requestTargetsAuthoringOAuth(r *http.Request) bool {
	if r == nil || strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return false
	}
	if err := r.ParseForm(); err != nil {
		return false
	}
	if r.Form.Get("grant_type") == authoringDeviceGrantType ||
		r.Form.Get("client_id") == access.AuthoringCLIClientID {
		return true
	}
	return r.Form.Get("grant_type") == "client_credentials" &&
		strings.TrimSpace(r.Form.Get("project_id")) != "" &&
		strings.TrimSpace(r.Form.Get("lifetime_seconds")) != ""
}

func requestTargetsMCPOAuth(r *http.Request) bool {
	if r == nil || strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return false
	}
	if err := r.ParseForm(); err != nil {
		return false
	}
	if strings.TrimSpace(r.Form.Get("resource")) != "" {
		return true
	}
	for _, scope := range strings.Fields(r.Form.Get("scope")) {
		if scope == "mcp:use" {
			return true
		}
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code", "refresh_token":
		return true
	default:
		return false
	}
}

func requestTargetsAuthoringOAuthRevoke(r *http.Request) bool {
	if r == nil || strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return false
	}
	if err := r.ParseForm(); err != nil {
		return false
	}
	return r.Form.Get("client_id") == access.AuthoringCLIClientID
}

func (s *Module) MCPProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	if s.oauthResource == nil {
		http.Error(w, "MCP OAuth is unavailable", http.StatusServiceUnavailable)
		return
	}
	s.oauthResource.ProtectedResourceMetadata(w, r)
}

func (s *Module) MCPAuthorizationServerMetadata(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil {
		http.Error(w, "MCP OAuth is unavailable", http.StatusServiceUnavailable)
		return
	}
	s.oauth.AuthorizationServerMetadata(w, r)
}

func (s *Module) MCPOAuthRegister(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil {
		http.Error(w, "MCP OAuth is unavailable", http.StatusServiceUnavailable)
		return
	}
	s.oauth.Register(w, r)
}

func (s *Module) MCPOAuthToken(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil {
		http.Error(w, "MCP OAuth is unavailable", http.StatusServiceUnavailable)
		return
	}
	s.oauth.Token(w, r)
}

func (s *Module) MCPOAuthRevoke(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil {
		http.Error(w, "MCP OAuth is unavailable", http.StatusServiceUnavailable)
		return
	}
	s.oauth.Revoke(w, r)
}

func (s *Module) MCPOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil || s.auth == nil {
		http.Error(w, "MCP OAuth is unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := s.auth.Principal(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	consent, err := s.oauth.Consent(r)
	if err != nil {
		http.Error(w, "Invalid OAuth authorization request", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPost {
		approved := r.FormValue("decision") == "approve"
		s.oauth.Authorize(w, r, principal.ID, approved)
		s.recordMCPOAuthAuthorization(r, principal.ID, consent.ClientID, approved)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := accessui.OAuthConsentPage(consent, r.URL.Query(), csrf.Token(r), s.presentation, s.assets).Render(w); err != nil {
		s.logger.ErrorContext(r.Context(), "render MCP OAuth consent failed", "error", err)
	}
}

func (s *Module) recordMCPOAuthAuthorization(r *http.Request, principalID, clientID string, approved bool) {
	repository := s.repositoryValue()
	if repository == nil {
		return
	}
	status := "denied"
	if approved {
		status = "success"
	}
	metadata, _ := json.Marshal(map[string]any{"clientId": clientID, "approved": approved})
	_ = access.PersistAuditEvent(r.Context(), repository, access.AuditEventInput{
		PrincipalID: principalID, Action: "mcp_oauth.authorization", ResourceKind: "oauth_client",
		ResourceID: clientID, Status: status,
		RequestID: r.Header.Get("X-Request-ID"), CorrelationID: r.Header.Get("X-Correlation-ID"),
		MetadataJSON: string(metadata),
	})
}
