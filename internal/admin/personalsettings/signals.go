// Package personalsettings contains the personal settings vertical slice used
// by the platform administration surface.  It deliberately owns its signal
// models so the admin page can adopt the surface without coupling the access
// API transport to the Datastar command loop.
package personalsettings

import (
	"time"

	"github.com/flidai/leapview/internal/access"
	uisignals "github.com/flidai/leapview/internal/admin/ui/signals"
)

// These aliases keep the vertical slice readable while making the generated
// TypeSpec models the only definition of the browser contract.
type Signal = uisignals.PersonalSettingsSignal
type ProfileSignal = uisignals.PersonalProfileSignal
type SecuritySignal = uisignals.PersonalSecuritySignal
type SessionSignal = uisignals.PersonalSessionSignal
type AuthoringSessionSignal = uisignals.PersonalAuthoringSessionSignal
type TokensSignal = uisignals.PersonalTokensSignal
type TokenSignal = uisignals.PersonalTokenSignal
type CapabilityOptionSignal = uisignals.PersonalCapabilityOptionSignal
type ProfileCommand = uisignals.PersonalProfileCommand
type ThemeCommand = uisignals.PersonalThemeCommand
type PasswordCommand = uisignals.PersonalPasswordCommand
type SessionCommand = uisignals.PersonalSessionCommand
type AuthoringSessionCommand = uisignals.PersonalAuthoringSessionCommand
type TokenCommand = uisignals.PersonalTokenCommand

func signalFromPrincipal(principal access.Principal, identity access.PrincipalIdentityManagement, avatarURL *string, theme access.ThemeMode) ProfileSignal {
	identitySource := string(identity.Source)
	if identitySource == "" {
		identitySource = string(access.IdentityManagementLocal)
	}
	return ProfileSignal{
		ID: principal.ID, Email: principal.Email, DisplayName: principal.DisplayName,
		AvatarURL: avatarURL, Theme: string(theme), IdentitySource: identitySource,
		CanEditDisplayName: identitySource == string(access.IdentityManagementLocal),
		HasLocalPassword:   identity.HasLocalPassword,
	}
}

func sessionSignal(value access.Session, currentSessionID string) SessionSignal {
	label := "Browser"
	if value.Kind == access.SessionKindDesktop {
		label = value.ClientID
		if label == "" {
			label = "Desktop app"
		}
	}
	return SessionSignal{
		ID: value.ID, Kind: string(value.Kind), ClientLabel: label,
		Current:   value.ID != "" && value.ID == currentSessionID,
		CreatedAt: value.CreatedAt, LastSeenAt: value.LastSeenAt,
		ExpiresAt: value.ExpiresAt, AbsoluteExpiresAt: value.AbsoluteExpiresAt,
		RevokedAt: value.RevokedAt,
	}
}

func authoringSessionSignal(value access.AuthoringSession) AuthoringSessionSignal {
	capabilities := make([]string, 0, len(value.Scope.Capabilities))
	for _, capability := range value.Scope.Capabilities {
		capabilities = append(capabilities, string(capability))
	}
	return AuthoringSessionSignal{
		ID: value.ID, Kind: string(value.Kind), ClientID: value.ClientID,
		TargetID: value.Scope.TargetID, ProjectID: value.Scope.ProjectID.String(),
		Capabilities: capabilities, CreatedAt: formatTime(value.CreatedAt),
		LastUsedAt: formatTime(value.LastUsedAt), ExpiresAt: formatTime(value.ExpiresAt),
		RevokedAt: formatTime(value.RevokedAt),
	}
}

func tokenSignal(value access.APIToken) TokenSignal {
	capabilities := make([]string, 0, len(value.Capabilities))
	for _, capability := range value.Capabilities {
		capabilities = append(capabilities, string(capability))
	}
	return TokenSignal{
		ID: value.ID, Name: value.Name, Capabilities: capabilities, CreatedAt: value.CreatedAt, LastUsedAt: value.LastUsedAt,
		ExpiresAt: value.ExpiresAt, RevokedAt: value.RevokedAt,
	}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format("2006-01-02T15:04:05Z07:00")
}
