package personalsettings

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/avatar"
)

func TestBootstrapSignalsExplicitlyClearNullableState(t *testing.T) {
	payload, err := json.Marshal(BootstrapSignals(Signal{})["personalSettings"])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"avatarUrl":null`, `"newToken":null`} {
		if !strings.Contains(string(payload), want) {
			t.Fatalf("payload = %s, want %s", payload, want)
		}
	}
}

type fakeRepository struct {
	principal       access.Principal
	identity        access.PrincipalIdentityManagement
	identityErr     error
	sessions        []access.Session
	tokens          []access.APIToken
	effective       []access.Capability
	audits          []access.AuditEventInput
	passwordChanged bool
	createdToken    bool
	theme           access.ThemeMode
	themeChanged    bool
}

func (f *fakeRepository) PrincipalPreferences(context.Context, string) (access.PrincipalPreferences, error) {
	return access.PrincipalPreferences{PrincipalID: f.principal.ID, Theme: f.theme}, nil
}

func (f *fakeRepository) SetPrincipalThemeAudited(_ context.Context, principalID string, theme access.ThemeMode) error {
	f.theme = theme
	f.themeChanged = true
	f.audits = append(f.audits, access.AuditEventInput{PrincipalID: principalID, Action: "principal.theme.updated"})
	return nil
}

func (f *fakeRepository) PrincipalByID(context.Context, string) (access.Principal, error) {
	return f.principal, nil
}
func (f *fakeRepository) UpsertPrincipal(_ context.Context, input access.PrincipalInput) (access.Principal, error) {
	f.principal.DisplayName = input.DisplayName
	return f.principal, nil
}
func (f *fakeRepository) ChangeLocalPassword(context.Context, string, string, string) (access.LocalCredential, error) {
	f.passwordChanged = true
	return access.LocalCredential{}, nil
}
func (f *fakeRepository) ListSessions(context.Context, string) ([]access.Session, error) {
	return f.sessions, nil
}
func (f *fakeRepository) RevokeSessionForPrincipal(_ context.Context, _, id string) error {
	for i := range f.sessions {
		if f.sessions[i].ID == id {
			f.sessions[i].RevokedAt = "revoked"
			return nil
		}
	}
	return errors.New("missing session")
}
func (f *fakeRepository) ListAPITokens(context.Context, string) ([]access.APIToken, error) {
	return f.tokens, nil
}
func (f *fakeRepository) CreateAPITokenWithMetadata(_ context.Context, input access.APITokenInput) (string, access.APIToken, error) {
	f.createdToken = true
	row := access.APIToken{ID: "token-2", PrincipalID: input.PrincipalID, Name: input.Name, Capabilities: input.Capabilities, CreatedAt: "now"}
	f.tokens = append(f.tokens, row)
	return "lv_test_secret", row, nil
}
func (f *fakeRepository) RevokeAPITokenForPrincipal(_ context.Context, _, id string) error {
	for i := range f.tokens {
		if f.tokens[i].ID == id {
			f.tokens[i].RevokedAt = "revoked"
			return nil
		}
	}
	return errors.New("missing token")
}
func (f *fakeRepository) EffectiveCapabilities(context.Context, string) ([]access.Capability, error) {
	return f.effective, nil
}
func (f *fakeRepository) RecordAuditEvent(_ context.Context, event access.AuditEventInput) error {
	f.audits = append(f.audits, event)
	return nil
}
func (f *fakeRepository) PrincipalIdentityManagement(context.Context, string) (access.PrincipalIdentityManagement, error) {
	return f.identity, f.identityErr
}

type fakeAvatar struct{}

func (fakeAvatar) Current(context.Context, string) (avatar.Metadata, error) {
	return avatar.Metadata{SHA256: "abc123"}, nil
}

type fakeAuthoring struct{ sessions []access.AuthoringSession }

func (f *fakeAuthoring) ListSessions(context.Context, string) ([]access.AuthoringSession, error) {
	return f.sessions, nil
}
func (f *fakeAuthoring) RevokeSession(context.Context, string, string) error { return nil }

func testService(repo *fakeRepository) *Service {
	return &Service{Repository: repo, Preferences: repo, IdentityManagement: repo, Avatar: fakeAvatar{}, Authoring: &fakeAuthoring{sessions: []access.AuthoringSession{{ID: "authoring-1", Kind: access.AuthoringSessionHumanCLI, ClientID: access.AuthoringCLIClientID, CreatedAt: time.Unix(1, 0), Scope: access.AuthoringScope{TargetID: "instance", ProjectID: "project", Capabilities: []access.Capability{access.CapabilityResourcePublish}}}}}, CurrentEffectiveCapabilities: repo.EffectiveCapabilities, LocalPasswordEnabled: true}
}

func TestServiceLoadBuildsPersonalSettingsSignal(t *testing.T) {
	repo := &fakeRepository{
		principal: access.Principal{ID: "principal-1", Kind: access.PrincipalKindUser, Email: "user@example.com", DisplayName: "User"},
		identity:  access.PrincipalIdentityManagement{Source: access.IdentityManagementLocal, HasLocalPassword: true},
		sessions:  []access.Session{{ID: "browser-1", Kind: access.SessionKindBrowser, CreatedAt: "today"}, {ID: "desktop-1", Kind: access.SessionKindDesktop, ClientID: "LeapView Desktop"}},
		tokens: []access.APIToken{
			{ID: "token-1", Name: "CI", Capabilities: []access.Capability{access.CapabilityResourceRead}},
			{ID: "token-revoked", Name: "Old CI", RevokedAt: "yesterday"},
		},
		theme: access.ThemeDark,
	}
	service := testService(repo)
	repo.effective = []access.Capability{access.CapabilityResourceRead, access.CapabilityResourceEdit, access.CapabilityResourcePublish}
	state, err := service.Load(context.Background(), "principal-1", "desktop-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if state.Profile.AvatarURL == nil || *state.Profile.AvatarURL != "/profile/avatars/principal-1/abc123" {
		t.Fatalf("avatar URL = %v", state.Profile.AvatarURL)
	}
	if !state.Profile.CanEditDisplayName || !state.Profile.HasLocalPassword {
		t.Fatalf("profile edit state = %#v", state.Profile)
	}
	if state.Profile.Theme != string(access.ThemeDark) {
		t.Fatalf("profile theme = %q, want dark", state.Profile.Theme)
	}
	if !state.Security.Sessions[1].Current || state.Security.Sessions[1].ClientLabel != "LeapView Desktop" {
		t.Fatalf("sessions = %#v", state.Security.Sessions)
	}
	if len(state.Security.AuthoringSessions) != 1 || state.Security.AuthoringSessions[0].Privileges[0] != string(access.CapabilityResourcePublish) {
		t.Fatalf("authoring sessions = %#v", state.Security.AuthoringSessions)
	}
	if len(state.Tokens.Items) != 1 || state.Tokens.Items[0].ID != "token-1" {
		t.Fatalf("tokens = %#v", state.Tokens)
	}
	if len(state.Tokens.Capabilities) != len(repo.effective) {
		t.Fatalf("capability options = %#v", state.Tokens.Capabilities)
	}
	if option := state.Tokens.Capabilities[0]; option.Value != string(access.CapabilityResourceRead) || option.Category != "Resource" || option.Description == "" {
		t.Fatalf("resource capability option = %#v", option)
	}
}

func TestServiceMutationsAuditAndValidateIdentity(t *testing.T) {
	repo := &fakeRepository{principal: access.Principal{ID: "principal-1", Kind: access.PrincipalKindUser, Email: "user@example.com"}, identity: access.PrincipalIdentityManagement{Source: access.IdentityManagementLocal, HasLocalPassword: true}, effective: []access.Capability{access.CapabilityResourceRead}}
	service := testService(repo)
	if err := service.ApplyProfile(context.Background(), "principal-1", ProfileCommand{Action: "save", DisplayName: "Updated"}); err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyPassword(context.Background(), "principal-1", PasswordCommand{CurrentPassword: "old", NewPassword: "new"}); err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyTheme(context.Background(), "principal-1", ThemeCommand{Action: "save", Theme: "dark_colorblind"}); err != nil {
		t.Fatal(err)
	}
	secret, err := service.ApplyToken(context.Background(), "principal-1", TokenCommand{Action: "create", Name: "CI", Capabilities: []string{string(access.CapabilityResourceRead)}})
	if err != nil || secret == nil || *secret != "lv_test_secret" {
		t.Fatalf("create token = %v, %v", secret, err)
	}
	if !repo.passwordChanged || !repo.createdToken || !repo.themeChanged || repo.theme != access.ThemeDarkColorblind || len(repo.audits) != 4 {
		t.Fatalf("mutations changed=%v token=%v audits=%d", repo.passwordChanged, repo.createdToken, len(repo.audits))
	}
	if _, err := service.ApplyToken(context.Background(), "principal-1", TokenCommand{Action: "create", Name: "Escalating", Capabilities: []string{string(access.CapabilityResourcePublish)}}); err == nil || !errors.Is(err, access.ErrCapabilityNotAllowed) {
		t.Fatalf("escalating token capability error = %v", err)
	}
	if secret, err := service.ApplyToken(context.Background(), "principal-1", TokenCommand{Action: "create", Name: "Deny all", Capabilities: []string{}}); err != nil || secret == nil {
		t.Fatalf("explicit deny-all token = %v, %v", secret, err)
	}
	repo.identity.Source = access.IdentityManagementExternal
	if err := service.ApplyProfile(context.Background(), "principal-1", ProfileCommand{Action: "save", DisplayName: "Nope"}); !errors.Is(err, ErrDisplayNameManaged) {
		t.Fatalf("external profile error = %v", err)
	}
}
