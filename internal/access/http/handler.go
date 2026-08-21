package http

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"strconv"
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/access/avatar"
)

var (
	errUnauthorized = errors.New("unauthorized")
	errForbidden    = access.ErrForbidden
)

type Principal struct {
	ID          string
	Kind        access.PrincipalKind
	Email       string
	DisplayName string
	CreatedAt   string
	UpdatedAt   string
}

type RepositoryProvider func() (access.Repository, error)
type PrincipalProvider func(*stdhttp.Request) (Principal, bool)
type CredentialProvider func(*stdhttp.Request) (access.APICredential, bool)
type SessionProvider func(*stdhttp.Request) (string, bool)
type EffectiveCapabilitiesProvider func(context.Context, *stdhttp.Request, string) ([]access.Capability, error)
type PlatformAdminProvider func(context.Context, string) (bool, error)
type RequestPlatformAdminProvider func(context.Context, *stdhttp.Request, string) (bool, error)

type AuthoringAuthentication interface {
	InstanceID() string
	BeginDeviceAuthorization(context.Context, access.AuthoringScope) (access.DeviceAuthorizationResponse, error)
	ApproveDeviceAuthorization(context.Context, access.Principal, string) error
	DenyDeviceAuthorization(context.Context, access.Principal, string) error
	ExchangeDeviceCode(context.Context, string) (access.AuthoringTokenSet, error)
	Refresh(context.Context, string) (access.AuthoringTokenSet, error)
	ExchangeWorkloadIdentity(context.Context, access.WorkloadIdentityInput) (access.AuthoringTokenSet, error)
	RevokeAccessToken(context.Context, string) error
	ListSessions(context.Context, string) ([]access.AuthoringSession, error)
	RevokeSession(context.Context, string, string) error
}

type Handler struct {
	Repository                   RepositoryProvider
	CurrentPrincipal             PrincipalProvider
	CurrentCredential            CredentialProvider
	CurrentSession               SessionProvider
	CurrentEffectiveCapabilities func(context.Context, string) ([]access.Capability, error)
	RequestEffectiveCapabilities EffectiveCapabilitiesProvider
	// PlatformAdmin evaluates the durable instance-wide role. It is retained as
	// a narrow callback for non-module callers; RequestPlatformAdmin additionally
	// applies request-credential attenuation.
	PlatformAdmin        PlatformAdminProvider
	RequestPlatformAdmin RequestPlatformAdminProvider
	AuthoringAuth        AuthoringAuthentication
	Avatar               AvatarService
	LocalPasswordEnabled bool
}

func (h Handler) repository() (access.Repository, error) {
	if h.Repository == nil {
		return nil, errors.New("access repository is unavailable")
	}
	return h.Repository()
}
func (h Handler) currentPrincipal(r *stdhttp.Request) (Principal, bool) {
	if h.CurrentPrincipal == nil {
		return Principal{}, false
	}
	return h.CurrentPrincipal(r)
}
func (h Handler) currentPrincipalID(r *stdhttp.Request) string {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		return ""
	}
	return principal.ID
}
func (h Handler) currentCredential(r *stdhttp.Request) (access.APICredential, bool) {
	if h.CurrentCredential == nil {
		return access.APICredential{}, false
	}
	return h.CurrentCredential(r)
}

func (h Handler) requirePlatformAdmin(w stdhttp.ResponseWriter, r *stdhttp.Request) bool {
	principal, ok := h.currentPrincipal(r)
	if !ok {
		writeJSONError(w, errUnauthorized, stdhttp.StatusUnauthorized)
		return false
	}
	var allowed bool
	var err error
	attenuated := false
	if h.RequestPlatformAdmin != nil {
		allowed, err = h.RequestPlatformAdmin(r.Context(), r, principal.ID)
		attenuated = true
	} else if h.PlatformAdmin != nil {
		allowed, err = h.PlatformAdmin(r.Context(), principal.ID)
	} else {
		repository, repositoryErr := h.repository()
		if repositoryErr != nil {
			writeJSONError(w, repositoryErr, stdhttp.StatusInternalServerError)
			return false
		}
		reader, readerOK := repository.(access.PlatformAdminReader)
		if !readerOK {
			writeJSONError(w, errors.New("access repository does not support durable platform administration"), stdhttp.StatusInternalServerError)
			return false
		}
		allowed, err = reader.IsPlatformAdmin(r.Context(), principal.ID)
	}
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusInternalServerError)
		return false
	}
	if !allowed {
		writeJSONError(w, errForbidden, stdhttp.StatusForbidden)
		return false
	}
	if attenuated {
		return true
	}
	if credential, ok := h.currentCredential(r); ok {
		if credential.Principal.ID != "" && credential.Principal.ID != principal.ID {
			writeJSONError(w, errForbidden, stdhttp.StatusForbidden)
			return false
		}
		if credential.Authoring != nil {
			writeJSONError(w, errForbidden, stdhttp.StatusForbidden)
			return false
		}
		if credential.Token.ID != "" && credential.Token.Capabilities != nil {
			if len(credential.Token.Capabilities) == 0 || !containsCapability(credential.Token.Capabilities, access.CapabilityProjectAdmin) {
				writeJSONError(w, errForbidden, stdhttp.StatusForbidden)
				return false
			}
		}
	}
	return true
}

func containsCapability(capabilities []access.Capability, expected access.Capability) bool {
	for _, capability := range capabilities {
		if capability == expected {
			return true
		}
	}
	return false
}
func (h Handler) rejectAuthoringCredential(w stdhttp.ResponseWriter, r *stdhttp.Request) bool {
	if credential, ok := h.currentCredential(r); ok && credential.Authoring != nil {
		writeJSONError(w, errors.New("authoring credentials cannot perform this mutation"), stdhttp.StatusForbidden)
		return true
	}
	return false
}

func principalDTO(row access.Principal) map[string]any {
	return map[string]any{"id": row.ID, "kind": row.Kind, "email": row.Email, "displayName": row.DisplayName, "createdAt": row.CreatedAt, "updatedAt": row.UpdatedAt, "disabledAt": emptyToNil(row.DisabledAt), "blockedAt": emptyToNil(row.BlockedAt)}
}

func (h Handler) currentPrincipalResponse(r *stdhttp.Request, current Principal) (map[string]any, error) {
	accountManagement := true
	if credential, ok := h.currentCredential(r); ok && credential.Authoring != nil {
		accountManagement = false
	}
	if h.Repository == nil {
		return h.currentPrincipalResponseFor(r.Context(), access.Principal{
			ID: current.ID, Kind: current.Kind, Email: current.Email, DisplayName: current.DisplayName,
			CreatedAt: current.CreatedAt, UpdatedAt: current.UpdatedAt,
		}, access.PrincipalIdentityManagement{Source: access.IdentityManagementSystem}, nil, accountManagement)
	}
	repo, err := h.repository()
	if err != nil {
		return nil, err
	}
	stored, err := repo.PrincipalByID(r.Context(), current.ID)
	if err != nil {
		return nil, err
	}
	management, err := principalIdentityManagement(r.Context(), repo, stored.ID)
	if err != nil {
		return nil, err
	}
	return h.currentPrincipalResponseFor(r.Context(), stored, management, repo, accountManagement)
}

func (h Handler) currentPrincipalResponseFor(ctx context.Context, principal access.Principal, management access.PrincipalIdentityManagement, repo access.Repository, accountManagement bool) (map[string]any, error) {
	response := principalDTO(principal)
	identity := map[string]any{"source": management.Source}
	if management.Provider != "" {
		identity["provider"] = management.Provider
	}
	response["identityManagement"] = identity
	isUser := principal.Kind == "" || principal.Kind == access.PrincipalKindUser
	response["capabilities"] = map[string]bool{
		"canUpdateDisplayName":       accountManagement && isUser && management.Source == access.IdentityManagementLocal,
		"canChangePassword":          accountManagement && isUser && h.LocalPasswordEnabled && management.HasLocalPassword,
		"canManageAvatar":            accountManagement && isUser && h.Avatar != nil,
		"canManageSessions":          accountManagement && isUser && repo != nil,
		"canManageAuthoringSessions": isUser && h.AuthoringAuth != nil,
		"canManageApiTokens":         accountManagement && isUser && repo != nil,
	}
	if h.Avatar != nil && isUser {
		metadata, err := h.Avatar.Current(ctx, principal.ID)
		switch {
		case err == nil:
			response["avatar"] = avatarResponse(metadata)
		case errors.Is(err, avatar.ErrNotFound):
		default:
			return nil, err
		}
	}
	return response, nil
}

func (h Handler) principalAdministrationDTO(ctx context.Context, repo access.Repository, principal access.Principal, actorID string) (map[string]any, error) {
	management, err := principalIdentityManagement(ctx, repo, principal.ID)
	if err != nil {
		return nil, err
	}
	response := principalDTO(principal)
	identity := map[string]any{"source": management.Source}
	if management.Provider != "" {
		identity["provider"] = management.Provider
	}
	response["identityManagement"] = identity
	isUser := principalKindAllowsGenericMutation(principal.Kind)
	isSelf := strings.TrimSpace(actorID) != "" && actorID == principal.ID
	response["capabilities"] = map[string]bool{
		"canUpdateProfile":       isUser && management.Source == access.IdentityManagementLocal,
		"canResetPassword":       isUser && management.HasLocalPassword,
		"canBlock":               isUser && !isSelf && principal.BlockedAt == "" && principal.DisabledAt == "",
		"canUnblock":             isUser && principal.BlockedAt != "",
		"canDelete":              isUser && !isSelf && management.Source == access.IdentityManagementLocal,
		"canManageSessions":      isUser,
		"canManageAuthorization": isUser,
	}
	return response, nil
}

func principalIdentityManagement(ctx context.Context, repo access.Repository, principalID string) (access.PrincipalIdentityManagement, error) {
	resolver, ok := repo.(access.PrincipalIdentityManagementRepository)
	if !ok {
		return access.PrincipalIdentityManagement{Source: access.IdentityManagementSystem}, nil
	}
	return resolver.PrincipalIdentityManagement(ctx, principalID)
}

func principalKindAllowsGenericMutation(kind access.PrincipalKind) bool {
	return kind == "" || kind == access.PrincipalKindUser
}

func principalManagedExternallyError(management access.PrincipalIdentityManagement) error {
	provider := strings.TrimSpace(management.Provider)
	if provider == "" {
		provider = "the owning identity subsystem"
	}
	return fmt.Errorf("principal profile is managed by %s", provider)
}

func localPasswordResetDTO(row access.LocalPasswordReset) map[string]any {
	return map[string]any{"principal": principalDTO(row.Principal), "temporaryPassword": row.Password}
}
func groupDTO(row access.Group) map[string]any {
	local := groupIsLocallyManaged(row)
	return map[string]any{
		"id": row.ID, "provider": row.Provider, "externalId": row.ExternalID,
		"name": row.Name, "createdAt": row.CreatedAt,
		"capabilities": map[string]bool{
			"canUpdate": local, "canDelete": local, "canManageMembers": local,
		},
	}
}
func groupMemberPrincipalDTO(row access.GroupMember) map[string]any {
	return map[string]any{"groupId": row.GroupID, "principalId": row.PrincipalID, "kind": row.Kind, "email": row.Email, "displayName": row.DisplayName, "createdAt": row.CreatedAt}
}
func groupAuditMetadata(row access.Group) map[string]any {
	return map[string]any{"provider": row.Provider, "externalId": row.ExternalID, "displayName": row.Name}
}
func apiTokenDTO(row access.APIToken) map[string]any {
	out := map[string]any{"id": row.ID, "principalId": row.PrincipalID, "name": row.Name, "expiresAt": emptyToNil(row.ExpiresAt), "createdAt": row.CreatedAt, "lastUsedAt": emptyToNil(row.LastUsedAt), "revokedAt": emptyToNil(row.RevokedAt)}
	if row.Capabilities != nil {
		values := make([]string, 0, len(row.Capabilities))
		for _, capability := range row.Capabilities {
			values = append(values, string(capability))
		}
		out["capabilities"] = values
	}
	return out
}
func servicePrincipalSecretDTO(row access.ServicePrincipalSecret, raw string) map[string]any {
	out := map[string]any{"id": row.ID, "servicePrincipalId": row.ServicePrincipalID, "name": row.Name, "expiresAt": row.ExpiresAt, "createdAt": row.CreatedAt, "revokedAt": emptyToNil(row.RevokedAt)}
	if raw != "" {
		out["secret"] = raw
	}
	return out
}
func sessionDTOFor(row access.Session, current string) map[string]any {
	return map[string]any{"id": row.ID, "kind": row.Kind, "instanceId": row.InstanceID, "profileId": row.ProfileID, "clientId": row.ClientID, "expiresAt": row.ExpiresAt, "absoluteExpiresAt": row.AbsoluteExpiresAt, "createdAt": row.CreatedAt, "lastSeenAt": row.LastSeenAt, "revokedAt": emptyToNil(row.RevokedAt), "current": row.ID == current}
}
func sessionDTO(row access.Session) map[string]any { return sessionDTOFor(row, "") }
func auditEventDTO(row access.AuditEvent) map[string]any {
	return map[string]any{"id": row.ID, "principalId": emptyToNil(row.PrincipalID), "action": row.Action, "resourceKind": row.ResourceKind, "resourceId": row.ResourceID, "capability": emptyToNil(string(row.Capability)), "status": row.Status, "requestId": emptyToNil(row.RequestID), "correlationId": emptyToNil(row.CorrelationID), "metadata": json.RawMessage(row.MetadataJSON), "createdAt": row.CreatedAt}
}

func auditInput(r *stdhttp.Request, action, principalID, resourceKind, resourceID string, capability access.Capability, status string, metadata map[string]any) access.AuditEventInput {
	if metadata == nil {
		metadata = map[string]any{}
	}
	encoded, _ := json.Marshal(metadata)
	return access.AuditEventInput{PrincipalID: principalID, Action: action, ResourceKind: resourceKind, ResourceID: resourceID, Capability: capability, Status: status, RequestID: requestIDFromRequest(r), CorrelationID: correlationIDFromRequest(r), MetadataJSON: string(encoded)}
}
func runAuditedMutation(r *stdhttp.Request, repo access.Repository, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	transactional, ok := repo.(access.AuditedMutationRepository)
	if !ok {
		return errors.New("transactional access repository is required")
	}
	return transactional.RunAuditedMutation(r.Context(), mutation)
}

// executeAuditedMutation keeps the existing repository transaction as the
// generated command's transactional execution capability. Direct browser
// handlers still use the repository helper unchanged; generated API/CLI
// transports additionally need the command executor to mark the invocation
// complete before the transport guard flushes a successful response.
func executeAuditedMutation(
	r *stdhttp.Request,
	repo access.Repository,
	operationID accessgen.GenCommandOperationID,
	mutation func(access.Repository) (access.AuditEventInput, error),
) error {
	if _, generated := apigencommand.OperationID(r.Context()); !generated {
		return runAuditedMutation(r, repo, mutation)
	}
	executor, err := apigencommand.NewExecutor(accessgen.GetAPIGenCommandRuntimeContract, nil)
	if err != nil {
		return err
	}
	return executor.Execute(r.Context(), operationID.APIGenOperationID(), apigencommand.Execution{
		Transactional: func(context.Context, apigencommand.Contract) error {
			return runAuditedMutation(r, repo, mutation)
		},
	})
}

// runAuditedMutationWithRevision keeps the optimistic-concurrency read and
// comparison in the same transaction as the mutation and its audit event.
// The global identity APIs use this for mutable principals, service
// principals, and groups; callers map the two precondition errors to 412.
func runAuditedMutationWithRevision(
	r *stdhttp.Request,
	repo access.Repository,
	currentRevision func(access.Repository) (string, error),
	mutation func(access.Repository) (access.AuditEventInput, error),
) error {
	transactional, ok := repo.(access.AuditedMutationRepository)
	if !ok {
		return errors.New("transactional access repository is required")
	}
	return transactional.RunAuditedMutation(r.Context(), func(tx access.Repository) (access.AuditEventInput, error) {
		current, err := currentRevision(tx)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		if err := checkIfMatch(r.Header.Get("If-Match"), current); err != nil {
			return access.AuditEventInput{}, err
		}
		return mutation(tx)
	})
}

var (
	errIfMatchRequired = errors.New("If-Match header is required")
	errIfMatchFailed   = errors.New("resource changed")
)

func checkIfMatch(presented, current string) error {
	presented = strings.TrimSpace(presented)
	if presented == "" {
		return errIfMatchRequired
	}
	if presented == "*" {
		return nil
	}
	if presented != current {
		return errIfMatchFailed
	}
	return nil
}

func decodeStrictJSON(r *stdhttp.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}
func writeJSON(w stdhttp.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeSecretJSON(w stdhttp.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, status, value)
}
func writeJSONError(w stdhttp.ResponseWriter, err error, status int) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}
func writePagedJSON[T any](w stdhttp.ResponseWriter, r *stdhttp.Request, items []T) bool {
	page, nextCursor, ok := pageSliceForRequest(w, r, items)
	if !ok {
		return false
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{"items": page, "page": map[string]any{"nextCursor": nextCursor}})
	return true
}

func pageSliceForRequest[T any](w stdhttp.ResponseWriter, r *stdhttp.Request, items []T) ([]T, string, bool) {
	limit, err := parseAPILimitQuery(r.URL.Query().Get("limit"))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return nil, "", false
	}
	cursor, err := decodeKeyCursor(r.URL.Query().Get("pageToken"))
	if err != nil {
		writeJSONError(w, err, stdhttp.StatusBadRequest)
		return nil, "", false
	}
	start := 0
	if cursor != "" {
		start = -1
		for index, item := range items {
			if apiItemPageKey(item) == cursor {
				start = index + 1
				break
			}
		}
		if start < 0 {
			writeJSONError(w, fmt.Errorf("pageToken key is unavailable"), stdhttp.StatusBadRequest)
			return nil, "", false
		}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	next := ""
	if end < len(items) && end > start {
		next = encodeKeyCursor(apiItemPageKey(items[end-1]))
	}
	page := make([]T, end-start)
	copy(page, items[start:end])
	return page, next, true
}
func writeCommandFailure(w stdhttp.ResponseWriter, _ *stdhttp.Request, _ accessgen.GenCommandOperationID, err error) {
	writeJSONError(w, err, stdhttp.StatusBadRequest)
}
func writeAuditedMutationError(w stdhttp.ResponseWriter, _ *stdhttp.Request, _ accessgen.GenCommandOperationID, err error, status int) {
	if errors.Is(err, errIfMatchRequired) || errors.Is(err, errIfMatchFailed) {
		writeJSONError(w, err, stdhttp.StatusPreconditionFailed)
		return
	}
	writeJSONError(w, err, status)
}
func statusForNotFound(err error) int {
	if errors.Is(err, sql.ErrNoRows) {
		return stdhttp.StatusNotFound
	}
	return stdhttp.StatusInternalServerError
}
func emptyToNil(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
func requestIDFromRequest(r *stdhttp.Request) string {
	return firstNonEmpty(r.Header.Get("X-Request-Id"), r.Header.Get("X-Request-ID"))
}
func correlationIDFromRequest(r *stdhttp.Request) string {
	return firstNonEmpty(r.Header.Get("X-Correlation-Id"), r.Header.Get("X-Correlation-ID"), requestIDFromRequest(r))
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
func servicePrincipalSecretHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func resourceETag(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}
func writeAPIProblem(w stdhttp.ResponseWriter, _ *stdhttp.Request, status int, code, detail string, _ any) {
	writeJSON(w, status, map[string]any{"code": code, "detail": detail})
}
func apiTokenID(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }
func encodeAuditPageToken(createdAt, id string) string {
	if strings.TrimSpace(createdAt) == "" || strings.TrimSpace(id) == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "\x00" + id))
}
func validAuditPageToken(token string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return false
	}
	createdAt, id, ok := strings.Cut(string(raw), "\x00")
	return ok && strings.TrimSpace(createdAt) != "" && strings.TrimSpace(id) != ""
}
func parseAPILimitQuery(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 50, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("limit must be an integer")
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("limit must be at least 1")
	}
	if parsed > 200 {
		return 0, fmt.Errorf("limit must not exceed 200")
	}
	return parsed, nil
}

func apiItemPageKey(value any) string {
	encoded, _ := json.Marshal(value)
	var object map[string]any
	if json.Unmarshal(encoded, &object) == nil {
		id, _ := object["id"].(string)
		created, _ := object["createdAt"].(string)
		if id != "" {
			return created + "\x00" + id
		}
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func encodeKeyCursor(key string) string {
	if key == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte("key:" + key))
}

func decodeKeyCursor(token string) (string, error) {
	if strings.TrimSpace(token) == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || !strings.HasPrefix(string(raw), "key:") || strings.TrimPrefix(string(raw), "key:") == "" {
		return "", fmt.Errorf("pageToken is invalid")
	}
	return strings.TrimPrefix(string(raw), "key:"), nil
}
func normalizeTimestamp(value string) string { return strings.TrimSpace(value) }
func hashValue(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func formatAuditTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func groupManagedExternallyError(group access.Group) error {
	return fmt.Errorf("group %q is managed by provider %q", group.ID, group.Provider)
}
func groupIsLocallyManaged(group access.Group) bool {
	return strings.TrimSpace(group.Provider) == "local"
}

func findGroup(ctx context.Context, repo access.Repository, id string) (access.Group, error) {
	rows, err := repo.ListGroups(ctx)
	if err != nil {
		return access.Group{}, err
	}
	for _, row := range rows {
		if row.ID == id {
			return row, nil
		}
	}
	return access.Group{}, sql.ErrNoRows
}
