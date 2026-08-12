package personalsettings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/avatar"
	"github.com/flidai/leapview/internal/workspace"
)

var (
	ErrPrincipalRequired        = errors.New("authenticated principal is required")
	ErrCommandInvalid           = errors.New("personal settings command is invalid")
	ErrDisplayNameManaged       = errors.New("display name is managed by the identity provider")
	ErrLocalPasswordUnavailable = errors.New("local password changes are unavailable for this principal")
	ErrTokenPrincipal           = errors.New("personal API tokens are only available to user principals")
	ErrSessionNotOwned          = errors.New("session does not belong to the current principal")
)

// Repository is intentionally narrower than access.Repository.  It keeps the
// settings vertical slice easy to exercise with in-memory fakes while the
// production access repository satisfies it directly.
type Repository interface {
	PrincipalByID(context.Context, string) (access.Principal, error)
	UpsertPrincipal(context.Context, access.PrincipalInput) (access.Principal, error)
	ChangeLocalPassword(context.Context, string, string, string) (access.LocalCredential, error)
	ListSessions(context.Context, string) ([]access.Session, error)
	RevokeSessionForPrincipal(context.Context, string, string) error
	ListAPITokens(context.Context, string) ([]access.APIToken, error)
	CreateAPITokenWithMetadata(context.Context, access.APITokenInput) (string, access.APIToken, error)
	RevokeAPITokenForPrincipal(context.Context, string, string) error
	EffectivePrivileges(context.Context, string, access.ObjectRef) ([]access.Privilege, error)
	RecordAuditEvent(context.Context, access.AuditEventInput) error
}

type IdentityManagementReader interface {
	PrincipalIdentityManagement(context.Context, string) (access.PrincipalIdentityManagement, error)
}

type PreferencesRepository interface {
	access.AuditedPrincipalPreferences
}

type AvatarReader interface {
	Current(context.Context, string) (avatar.Metadata, error)
}

type AuthoringReader interface {
	ListSessions(context.Context, string) ([]access.AuthoringSession, error)
	RevokeSession(context.Context, string, string) error
}

type WorkspaceReader interface {
	List(context.Context) ([]workspace.Summary, error)
}

type Service struct {
	Repository           Repository
	Preferences          PreferencesRepository
	IdentityManagement   IdentityManagementReader
	Avatar               AvatarReader
	Authoring            AuthoringReader
	Workspaces           WorkspaceReader
	LocalPasswordEnabled bool
	Now                  func() time.Time
}

func (s *Service) Load(ctx context.Context, principalID, currentSessionID string, includeTokenScopes bool) (Signal, error) {
	principalID = strings.TrimSpace(principalID)
	if principalID == "" {
		return Signal{}, ErrPrincipalRequired
	}
	if s == nil || s.Repository == nil {
		return Signal{}, fmt.Errorf("personal settings repository is unavailable")
	}
	principal, err := s.Repository.PrincipalByID(ctx, principalID)
	if err != nil {
		return Signal{}, err
	}
	theme := access.ThemeSystem
	if s.Preferences != nil {
		preferences, preferencesErr := s.Preferences.PrincipalPreferences(ctx, principalID)
		if preferencesErr == nil {
			theme = preferences.Theme
		} else if !errors.Is(preferencesErr, sql.ErrNoRows) {
			return Signal{}, preferencesErr
		}
	}
	identity := access.PrincipalIdentityManagement{Source: access.IdentityManagementLocal}
	if s.IdentityManagement != nil {
		identity, err = s.IdentityManagement.PrincipalIdentityManagement(ctx, principalID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Signal{}, err
		}
	}
	var avatarURL *string
	if s.Avatar != nil && principal.Kind == access.PrincipalKindUser {
		if metadata, avatarErr := s.Avatar.Current(ctx, principalID); avatarErr == nil {
			if currentURL := avatar.URLForPrincipal(principalID, metadata); currentURL != "" {
				avatarURL = &currentURL
			}
		}
	}
	sessions, err := s.Repository.ListSessions(ctx, principalID)
	if err != nil {
		return Signal{}, err
	}
	tokens, err := s.Repository.ListAPITokens(ctx, principalID)
	if err != nil {
		return Signal{}, err
	}
	tokenScopes := []TokenScopeSignal{}
	if includeTokenScopes {
		tokenScopes, err = s.tokenScopes(ctx, principalID)
		if err != nil {
			return Signal{}, err
		}
	}
	result := Signal{
		Profile:  signalFromPrincipal(principal, identity, avatarURL, theme),
		Security: SecuritySignal{LocalPasswordEnabled: s.LocalPasswordEnabled, Sessions: make([]SessionSignal, 0, len(sessions))},
		Tokens:   TokensSignal{Items: make([]TokenSignal, 0, len(tokens)), Scopes: tokenScopes},
	}
	for _, session := range sessions {
		result.Security.Sessions = append(result.Security.Sessions, sessionSignal(session, currentSessionID))
	}
	for _, token := range tokens {
		if strings.TrimSpace(token.RevokedAt) != "" {
			continue
		}
		result.Tokens.Items = append(result.Tokens.Items, tokenSignal(token))
	}
	if s.Authoring != nil {
		authoringSessions, authoringErr := s.Authoring.ListSessions(ctx, principalID)
		if authoringErr != nil {
			return Signal{}, authoringErr
		}
		result.Security.AuthoringSessions = make([]AuthoringSessionSignal, 0, len(authoringSessions))
		for _, session := range authoringSessions {
			result.Security.AuthoringSessions = append(result.Security.AuthoringSessions, authoringSessionSignal(session))
		}
	} else {
		result.Security.AuthoringSessions = []AuthoringSessionSignal{}
	}
	return result, nil
}

func (s *Service) ApplyTheme(ctx context.Context, principalID string, command ThemeCommand) error {
	if strings.TrimSpace(command.Action) != "save" {
		return fmt.Errorf("%w: theme action %q", ErrCommandInvalid, command.Action)
	}
	theme, ok := access.ParseThemeMode(command.Theme)
	if !ok {
		return fmt.Errorf("unsupported theme %q", command.Theme)
	}
	if s == nil || s.Preferences == nil {
		return fmt.Errorf("personal preferences are unavailable")
	}
	return s.Preferences.SetPrincipalThemeAudited(ctx, strings.TrimSpace(principalID), theme)
}

func (s *Service) ApplyProfile(ctx context.Context, principalID string, command ProfileCommand) error {
	if strings.TrimSpace(command.Action) != "save" && strings.TrimSpace(command.Action) != "update" {
		return fmt.Errorf("%w: profile action %q", ErrCommandInvalid, command.Action)
	}
	principal, identity, err := s.principalAndIdentity(ctx, principalID)
	if err != nil {
		return err
	}
	if identity.Source != "" && identity.Source != access.IdentityManagementLocal {
		return ErrDisplayNameManaged
	}
	displayName := strings.TrimSpace(command.DisplayName)
	if displayName == "" || len(displayName) > 200 {
		return fmt.Errorf("display name must contain between 1 and 200 bytes")
	}
	return s.runAudited(ctx, func(repository Repository) (access.AuditEventInput, error) {
		updated, err := repository.UpsertPrincipal(ctx, access.PrincipalInput{
			ID: principal.ID, Kind: principal.Kind, Email: principal.Email, DisplayName: displayName,
		})
		return access.AuditEventInput{
			PrincipalID: principal.ID, Action: "principal.profile.updated", TargetType: "principal", TargetID: updated.ID,
			Status: "success", MetadataJSON: `{"field":"displayName"}`,
		}, err
	})
}

func (s *Service) ApplyPassword(ctx context.Context, principalID string, command PasswordCommand) error {
	if command.CurrentPassword == "" || command.NewPassword == "" || command.CurrentPassword == command.NewPassword {
		return fmt.Errorf("current and new passwords must be present and differ")
	}
	_, identity, err := s.principalAndIdentity(ctx, principalID)
	if err != nil {
		return err
	}
	if !s.LocalPasswordEnabled || !identity.HasLocalPassword {
		return ErrLocalPasswordUnavailable
	}
	return s.runAudited(ctx, func(repository Repository) (access.AuditEventInput, error) {
		_, err := repository.ChangeLocalPassword(ctx, principalID, command.CurrentPassword, command.NewPassword)
		return access.AuditEventInput{
			PrincipalID: principalID, Action: "password.changed", TargetType: "principal", TargetID: principalID,
			Status: "success", MetadataJSON: `{"provider":"local"}`,
		}, err
	})
}

func (s *Service) RevokeSession(ctx context.Context, principalID string, command SessionCommand) error {
	if strings.TrimSpace(command.Action) != "revoke" || strings.TrimSpace(command.SessionID) == "" {
		return fmt.Errorf("%w: session revoke", ErrCommandInvalid)
	}
	return s.runAudited(ctx, func(repository Repository) (access.AuditEventInput, error) {
		err := repository.RevokeSessionForPrincipal(ctx, principalID, strings.TrimSpace(command.SessionID))
		return access.AuditEventInput{
			PrincipalID: principalID, Action: "session.revoked", TargetType: "session", TargetID: command.SessionID,
			Status: "success", MetadataJSON: `{}`,
		}, err
	})
}

func (s *Service) RevokeAuthoringSession(ctx context.Context, principalID string, command AuthoringSessionCommand) error {
	if s.Authoring == nil {
		return fmt.Errorf("authoring sessions are unavailable")
	}
	if strings.TrimSpace(command.Action) != "revoke" || strings.TrimSpace(command.SessionID) == "" {
		return fmt.Errorf("%w: authoring session revoke", ErrCommandInvalid)
	}
	// The authoring service owns its transactional audit event.
	return s.Authoring.RevokeSession(ctx, principalID, strings.TrimSpace(command.SessionID))
}

func (s *Service) ApplyToken(ctx context.Context, principalID string, command TokenCommand) (*string, error) {
	principal, err := s.Repository.PrincipalByID(ctx, principalID)
	if err != nil {
		return nil, err
	}
	if principal.Kind != "" && principal.Kind != access.PrincipalKindUser {
		return nil, ErrTokenPrincipal
	}
	switch strings.TrimSpace(command.Action) {
	case "revoke":
		if strings.TrimSpace(command.TokenID) == "" {
			return nil, fmt.Errorf("%w: token id is required", ErrCommandInvalid)
		}
		return nil, s.runAudited(ctx, func(repository Repository) (access.AuditEventInput, error) {
			err := repository.RevokeAPITokenForPrincipal(ctx, principalID, command.TokenID)
			return access.AuditEventInput{PrincipalID: principalID, Action: "api_token.revoked", TargetType: "api_token", TargetID: command.TokenID, Status: "success", MetadataJSON: `{}`}, err
		})
	case "create":
		name := strings.TrimSpace(command.Name)
		if name == "" || len(name) > 200 {
			return nil, fmt.Errorf("token name must contain between 1 and 200 bytes")
		}
		if len(command.Privileges) == 0 {
			return nil, fmt.Errorf("select at least one privilege for the API token")
		}
		var privileges []access.Privilege
		if len(command.Privileges) > 0 {
			privileges = make([]access.Privilege, 0, len(command.Privileges))
		}
		for _, raw := range command.Privileges {
			privilege, ok := access.ParsePrivilege(raw)
			if !ok {
				return nil, fmt.Errorf("unsupported API token privilege %q", raw)
			}
			privileges = append(privileges, privilege)
		}
		workspaceID := strings.TrimSpace(command.WorkspaceID)
		if len(privileges) > 0 {
			target := access.PlatformObject()
			if workspaceID != "" {
				target = access.WorkspaceObject(workspaceID)
			}
			effective, effectiveErr := s.Repository.EffectivePrivileges(ctx, principalID, target)
			if effectiveErr != nil {
				return nil, effectiveErr
			}
			allowed := make(map[access.Privilege]struct{}, len(effective))
			for _, privilege := range effective {
				allowed[privilege] = struct{}{}
			}
			for _, privilege := range privileges {
				if _, ok := allowed[privilege]; !ok {
					return nil, fmt.Errorf("requested API token privileges exceed effective access")
				}
			}
		}
		var expiresAt time.Time
		if strings.TrimSpace(command.ExpiresAt) != "" {
			expiresAt, err = time.Parse(time.RFC3339, strings.TrimSpace(command.ExpiresAt))
			if err != nil {
				return nil, fmt.Errorf("invalid token expiry: %w", err)
			}
			if !expiresAt.After(s.now()) {
				return nil, fmt.Errorf("token expiry must be in the future")
			}
		}
		var secret string
		err = s.runAudited(ctx, func(repository Repository) (access.AuditEventInput, error) {
			createdSecret, token, createErr := repository.CreateAPITokenWithMetadata(ctx, access.APITokenInput{
				PrincipalID: principalID, WorkspaceID: workspaceID, Name: name, Privileges: privileges, ExpiresAt: expiresAt,
			})
			secret = createdSecret
			return access.AuditEventInput{PrincipalID: principalID, WorkspaceID: workspaceID, Action: "api_token.created", TargetType: "api_token", TargetID: token.ID, Status: "success", MetadataJSON: metadataJSON(map[string]string{"name": name})}, createErr
		})
		if err != nil {
			return nil, err
		}
		return &secret, nil
	default:
		return nil, fmt.Errorf("%w: token action %q", ErrCommandInvalid, command.Action)
	}
}

func (s *Service) principalAndIdentity(ctx context.Context, principalID string) (access.Principal, access.PrincipalIdentityManagement, error) {
	principal, err := s.Repository.PrincipalByID(ctx, strings.TrimSpace(principalID))
	if err != nil {
		return access.Principal{}, access.PrincipalIdentityManagement{}, err
	}
	identity := access.PrincipalIdentityManagement{Source: access.IdentityManagementLocal}
	if s.IdentityManagement != nil {
		identity, err = s.IdentityManagement.PrincipalIdentityManagement(ctx, principal.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return access.Principal{}, access.PrincipalIdentityManagement{}, err
		}
	}
	return principal, identity, nil
}

func (s *Service) audit(ctx context.Context, event access.AuditEventInput) error {
	if event.MetadataJSON == "" {
		event.MetadataJSON = "{}"
	}
	return s.Repository.RecordAuditEvent(ctx, event)
}

func (s *Service) runAudited(ctx context.Context, mutation func(Repository) (access.AuditEventInput, error)) error {
	if transactional, ok := s.Repository.(access.AuditedMutationRepository); ok {
		return transactional.RunAuditedMutation(ctx, func(repository access.Repository) (access.AuditEventInput, error) {
			return mutation(repository)
		})
	}
	event, err := mutation(s.Repository)
	if err != nil {
		return err
	}
	return s.audit(ctx, event)
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func metadataJSON(value map[string]string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}
