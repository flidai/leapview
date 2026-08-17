// Package settings contains the presentation contracts used by the settings
// surfaces.  The package deliberately has no HTTP or Datastar dependencies:
// callers can put these values in an existing page envelope/stream and keep
// mutations on the normal CSRF-protected command path.
package settings

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

// ServiceAccountsSignal contains account rows and the selected account's
// secret metadata. Raw secrets are only present in ServiceAccountSecretSignal
// at creation time and are never copied into this list signal.
type ServiceAccountsSignal struct {
	Items         []ServiceAccountSignal       `json:"items"`
	SelectedID    string                       `json:"selectedId"`
	Secrets       []ServiceAccountSecretSignal `json:"secrets"`
	CreatedSecret string                       `json:"createdSecret"`
	Error         string                       `json:"error"`
	Loading       bool                         `json:"loading"`
	NextCursor    string                       `json:"nextCursor,omitempty"`
	HasMore       bool                         `json:"hasMore"`
}

type ServiceAccountSignal struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email,omitempty"`
	Kind        string `json:"kind"`
	CreatedAt   string `json:"createdAt,omitempty"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
	DisabledAt  string `json:"disabledAt,omitempty"`
}

type ServiceAccountSecretSignal struct {
	ID                 string `json:"id"`
	ServicePrincipalID string `json:"servicePrincipalId"`
	Name               string `json:"name"`
	ExpiresAt          string `json:"expiresAt,omitempty"`
	CreatedAt          string `json:"createdAt,omitempty"`
	RevokedAt          string `json:"revokedAt,omitempty"`
}

type ServiceAccountCommand struct {
	Action      string `json:"action"`
	AccountID   string `json:"accountId,omitempty"`
	SecretID    string `json:"secretId,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	SecretName  string `json:"secretName,omitempty"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
}

// AuditLogSignal is a product-level, read-only audit table. Filters and page
// tokens are carried in AuditLogCommand so the stream remains the sole data
// transport.
type AuditLogSignal struct {
	Items       []AuditEventSignal `json:"items"`
	Filters     AuditLogFilters    `json:"filters"`
	NextCursor  string             `json:"nextCursor"`
	HasMore     bool               `json:"hasMore"`
	LoadedCount int                `json:"loadedCount"`
	Loading     bool               `json:"loading"`
	Error       string             `json:"error"`
}

type AuditEventSignal struct {
	ID            string         `json:"id"`
	ProjectID     string         `json:"projectId,omitempty"`
	PrincipalID   string         `json:"principalId,omitempty"`
	Action        string         `json:"action"`
	ResourceKind  string         `json:"resourceKind"`
	ResourceID    string         `json:"resourceId"`
	Capability    string         `json:"capability,omitempty"`
	Status        string         `json:"status,omitempty"`
	RequestID     string         `json:"requestId,omitempty"`
	CorrelationID string         `json:"correlationId,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	CreatedAt     string         `json:"createdAt"`
}

type AuditLogFilters struct {
	ProjectID    string `json:"projectId"`
	PrincipalID  string `json:"principalId"`
	Action       string `json:"action"`
	ResourceKind string `json:"resourceKind"`
	ResourceID   string `json:"resourceId"`
	From         string `json:"from"`
	To           string `json:"to"`
}

type AuditLogCommand struct {
	Action    string          `json:"action"`
	Filters   AuditLogFilters `json:"filters"`
	PageToken string          `json:"pageToken,omitempty"`
	Limit     int             `json:"limit,omitempty"`
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}

// NormalizeAuditLogCommand drops unknown actions and bounds the page size.
// Filtering actions reset pagination so a stale cursor cannot skip results.
func NormalizeAuditLogCommand(command AuditLogCommand) AuditLogCommand {
	command.Action = strings.TrimSpace(command.Action)
	switch command.Action {
	case "load_more", "reset", "filter", "clear":
	default:
		command.Action = "reset"
	}
	command.Filters = NormalizeAuditLogFilters(command.Filters)
	command.Limit = normalizeLimit(command.Limit)
	command.PageToken = strings.TrimSpace(command.PageToken)
	if command.Action == "reset" || command.Action == "filter" || command.Action == "clear" {
		command.PageToken = ""
	}
	return command
}

func NormalizeAuditLogFilters(filters AuditLogFilters) AuditLogFilters {
	filters.ProjectID = strings.TrimSpace(filters.ProjectID)
	filters.PrincipalID = strings.TrimSpace(filters.PrincipalID)
	filters.Action = strings.TrimSpace(filters.Action)
	filters.ResourceKind = strings.TrimSpace(filters.ResourceKind)
	filters.ResourceID = strings.TrimSpace(filters.ResourceID)
	filters.From = strings.TrimSpace(filters.From)
	filters.To = strings.TrimSpace(filters.To)
	return filters
}

// AuditPageToken encodes the stable createdAt/id cursor used by the access
// repository and API. It is exported so command handlers can produce the same
// cursor as the REST endpoint without reaching into sqlite implementation.
func AuditPageToken(createdAt, id string) string {
	if strings.TrimSpace(createdAt) == "" || strings.TrimSpace(id) == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "\x00" + id))
}

func ServiceAccountSignalFromPrincipal(principal access.Principal) ServiceAccountSignal {
	return ServiceAccountSignal{ID: principal.ID, DisplayName: principal.DisplayName, Email: principal.Email,
		Kind: string(principal.Kind), CreatedAt: principal.CreatedAt, UpdatedAt: principal.UpdatedAt, DisabledAt: principal.DisabledAt}
}

func ServiceAccountSecretSignalFromDomain(secret access.ServicePrincipalSecret) ServiceAccountSecretSignal {
	return ServiceAccountSecretSignal{ID: secret.ID, ServicePrincipalID: secret.ServicePrincipalID, Name: secret.Name,
		ExpiresAt: secret.ExpiresAt, CreatedAt: secret.CreatedAt, RevokedAt: secret.RevokedAt}
}

func AuditEventSignalFromDomain(event access.AuditEvent) AuditEventSignal {
	metadata := map[string]any{}
	if strings.TrimSpace(event.MetadataJSON) != "" {
		_ = json.Unmarshal([]byte(event.MetadataJSON), &metadata)
	}
	return AuditEventSignal{ID: event.ID, PrincipalID: event.PrincipalID,
		Action: event.Action, ResourceKind: event.ResourceKind, ResourceID: event.ResourceID, Capability: string(event.Capability),
		Status: event.Status, RequestID: event.RequestID, CorrelationID: event.CorrelationID, Metadata: metadata, CreatedAt: event.CreatedAt}
}

// LoadAuditLog reads the canonical access audit stream. ProjectID is retained
// in the UI filter contract for future graph-scoped events; current identity
// audit records are globally keyed by resource kind/id.
func LoadAuditLog(ctx context.Context, repository access.Repository, filters AuditLogFilters, pageToken string, limit int) (AuditLogSignal, error) {
	state := AuditLogSignal{Items: []AuditEventSignal{}, Filters: NormalizeAuditLogFilters(filters), NextCursor: "", LoadedCount: 0, Loading: false}
	if repository == nil {
		return state, nil
	}
	limit = normalizeLimit(limit)
	rows, err := repository.ListAuditEvents(ctx, access.AuditEventFilter{
		PrincipalID: strings.TrimSpace(filters.PrincipalID), Action: strings.TrimSpace(filters.Action),
		ResourceKind: strings.TrimSpace(filters.ResourceKind), ResourceID: strings.TrimSpace(filters.ResourceID),
		PageToken: strings.TrimSpace(pageToken), Limit: limit + 1,
	})
	if err != nil {
		return state, err
	}
	state.HasMore = len(rows) > limit
	if state.HasMore {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		state.NextCursor = AuditPageToken(last.CreatedAt, last.ID)
	}
	for _, row := range rows {
		state.Items = append(state.Items, AuditEventSignalFromDomain(row))
	}
	state.LoadedCount = len(state.Items)
	return state, nil
}
