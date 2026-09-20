package module

import (
	"context"
	"net/http"
	"time"

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

func (m *Module) PlatformAdminAuthority() access.PlatformAdminAuthorityLister {
	if m == nil {
		return nil
	}
	lister, _ := m.repositoryValue().(access.PlatformAdminAuthorityLister)
	return lister
}

func (m *Module) PlatformAdminWriter() access.PlatformAdminWriter {
	if m == nil {
		return nil
	}
	writer, _ := m.repositoryValue().(access.PlatformAdminWriter)
	return writer
}

// CurrentInteractiveAuthentication reports the server-recorded login time for
// the browser session on the request. It deliberately does not inspect client
// supplied timestamps or bearer credentials.
func (m *Module) CurrentInteractiveAuthentication(r *http.Request) (time.Time, bool) {
	if m == nil || m.handler.InteractiveAuthentication == nil {
		return time.Time{}, false
	}
	return m.handler.InteractiveAuthentication(r)
}

// RequirePlatformRoleApproval reports the deployment policy applied to
// platform-role mutations. Admin/product settings consumes this same
// access-owned policy so direct Settings commands cannot bypass API policy.
func (m *Module) RequirePlatformRoleApproval() bool {
	return m != nil && m.handler.RequirePlatformRoleApproval
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
