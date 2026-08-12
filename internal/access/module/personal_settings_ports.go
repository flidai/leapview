package module

import (
	"context"
	"net/http"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/avatar"
)

type PersonalAvatar interface {
	Current(context.Context, string) (avatar.Metadata, error)
}

type AuthoringSessions interface {
	ListSessions(context.Context, string) ([]access.AuthoringSession, error)
	RevokeSession(context.Context, string, string) error
}

// SettingsAdministration is the access-owned administration surface consumed
// by product settings. The concrete persistence adapter remains private to the
// access module.
type SettingsAdministration interface {
	access.Repository
	access.AuditedPrincipalPreferences
	ListServicePrincipalSecrets(context.Context, string) ([]access.ServicePrincipalSecret, error)
	PrincipalIdentityManagement(context.Context, string) (access.PrincipalIdentityManagement, error)
}

func (m *Module) PersonalAvatar() PersonalAvatar {
	if m == nil {
		return nil
	}
	return m.handler.Avatar
}

func (m *Module) AuthoringSessions() AuthoringSessions {
	if m == nil || m.authoringAuth == nil {
		return nil
	}
	return m.authoringAuth
}

func (m *Module) SettingsAdministration() SettingsAdministration {
	if m == nil {
		return nil
	}
	repository := m.repositoryValue()
	if repository == nil {
		return nil
	}
	settings, _ := repository.(SettingsAdministration)
	return settings
}

func (m *Module) CurrentSessionID(r *http.Request) (string, bool) {
	if m == nil || m.handler.CurrentSession == nil {
		return "", false
	}
	return m.handler.CurrentSession(r)
}
