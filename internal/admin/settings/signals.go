// Package settings contains the presentation contracts used by the settings
// surfaces.  The package deliberately has no HTTP or Datastar dependencies:
// callers can put these values in an existing page envelope/stream and keep
// mutations on the normal CSRF-protected command path.
package settings

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/workspace"
)

// WorkspaceRegistrySignal is the read-only workspace registry used by the
// settings overview.  An item includes ownership, administrators, runtime
// deployment state, and links to the owning API resources.
type WorkspaceRegistrySignal struct {
	Items      []WorkspaceRegistryItemSignal `json:"items"`
	Empty      string                        `json:"empty,omitempty"`
	Loading    bool                          `json:"loading"`
	Error      string                        `json:"error,omitempty"`
	NextCursor string                        `json:"nextCursor,omitempty"`
	HasMore    bool                          `json:"hasMore"`
}

type WorkspaceRegistryItemSignal struct {
	ID                   string                   `json:"id"`
	Title                string                   `json:"title"`
	Description          string                   `json:"description,omitempty"`
	Href                 string                   `json:"href"`
	CreatedAt            string                   `json:"createdAt,omitempty"`
	UpdatedAt            string                   `json:"updatedAt,omitempty"`
	Owner                *WorkspaceSubjectSignal  `json:"owner,omitempty"`
	Administrators       []WorkspaceSubjectSignal `json:"administrators"`
	Environment          string                   `json:"environment,omitempty"`
	ActiveServingStateID string                   `json:"activeServingStateId,omitempty"`
	ServingStateStatus   string                   `json:"servingStateStatus,omitempty"`
	ServingStateSince    string                   `json:"servingStateSince,omitempty"`
	ProjectID            string                   `json:"projectId,omitempty"`
	CurrentDeploymentID  string                   `json:"currentDeploymentId,omitempty"`
	DeploymentStatus     string                   `json:"deploymentStatus,omitempty"`
	DeploymentSince      string                   `json:"deploymentSince,omitempty"`
	CurrentReleaseID     string                   `json:"currentReleaseId,omitempty"`
	Links                WorkspaceLinksSignal     `json:"links"`
}

type WorkspaceSubjectSignal struct {
	SubjectType string `json:"subjectType"`
	SubjectID   string `json:"subjectId"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role,omitempty"`
}

type WorkspaceLinksSignal struct {
	Self         string `json:"self"`
	Workspace    string `json:"workspace"`
	Project      string `json:"project,omitempty"`
	Release      string `json:"release,omitempty"`
	Deployment   string `json:"deployment,omitempty"`
	Deployments  string `json:"deployments,omitempty"`
	Connections  string `json:"connections,omitempty"`
	Publications string `json:"publications,omitempty"`
	Agent        string `json:"agent,omitempty"`
}

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
	WorkspaceID   string         `json:"workspaceId,omitempty"`
	PrincipalID   string         `json:"principalId,omitempty"`
	Action        string         `json:"action"`
	TargetType    string         `json:"targetType"`
	TargetID      string         `json:"targetId"`
	Privilege     string         `json:"privilege,omitempty"`
	Status        string         `json:"status,omitempty"`
	RequestID     string         `json:"requestId,omitempty"`
	CorrelationID string         `json:"correlationId,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	CreatedAt     string         `json:"createdAt"`
}

type AuditLogFilters struct {
	WorkspaceID string `json:"workspaceId"`
	PrincipalID string `json:"principalId"`
	Action      string `json:"action"`
	TargetType  string `json:"targetType"`
	TargetID    string `json:"targetId"`
	From        string `json:"from"`
	To          string `json:"to"`
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
	filters.WorkspaceID = strings.TrimSpace(filters.WorkspaceID)
	filters.PrincipalID = strings.TrimSpace(filters.PrincipalID)
	filters.Action = strings.TrimSpace(filters.Action)
	filters.TargetType = strings.TrimSpace(filters.TargetType)
	filters.TargetID = strings.TrimSpace(filters.TargetID)
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

// WorkspaceSignalFromSummary provides a useful empty-state row when an
// administration projection is unavailable.
func WorkspaceSignalFromSummary(summary workspace.Summary, environment string) WorkspaceRegistryItemSignal {
	id := string(summary.ID)
	return WorkspaceRegistryItemSignal{ID: id, Title: summary.Title, Description: summary.Description,
		Href: "/workspaces/" + url.PathEscape(id), CreatedAt: summary.CreatedAt, UpdatedAt: summary.UpdatedAt,
		Environment: environment, ActiveServingStateID: string(summary.ActiveServingStateID),
		Administrators: []WorkspaceSubjectSignal{},
		Links:          WorkspaceLinksSignal{Self: "/api/v1/workspaces/" + url.PathEscape(id), Workspace: "/workspaces/" + url.PathEscape(id)}}
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
	return AuditEventSignal{ID: event.ID, WorkspaceID: event.WorkspaceID, PrincipalID: event.PrincipalID,
		Action: event.Action, TargetType: event.TargetType, TargetID: event.TargetID, Privilege: string(event.Privilege),
		Status: event.Status, RequestID: event.RequestID, CorrelationID: event.CorrelationID, Metadata: metadata, CreatedAt: event.CreatedAt}
}

func SortWorkspaceItems(items []WorkspaceRegistryItemSignal) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := strings.ToLower(items[i].Title), strings.ToLower(items[j].Title)
		if left == right {
			return items[i].ID < items[j].ID
		}
		return left < right
	})
}
