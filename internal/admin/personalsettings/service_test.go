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
	projectgraph "github.com/flidai/leapview/internal/project/graph"
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
	audits          []access.AuditEventInput
	passwordChanged bool
	createdToken    bool
	scopedInput     access.ScopedAPITokenInput
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
func (f *fakeRepository) CreateScopedAPITokenWithMetadata(_ context.Context, input access.ScopedAPITokenInput) (string, access.APIToken, error) {
	f.createdToken = true
	f.scopedInput = input
	row := access.APIToken{ID: "token-typed", PrincipalID: input.PrincipalID, Name: input.Name, Description: input.Description, PermissionProfile: access.PermissionCatalogProfile, Permissions: input.Permissions, CreatedAt: "now"}
	f.tokens = append(f.tokens, row)
	return "lv_typed_secret", row, nil
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
	return &Service{Repository: repo, Preferences: repo, IdentityManagement: repo, Avatar: fakeAvatar{}, Authoring: &fakeAuthoring{sessions: []access.AuthoringSession{{ID: "authoring-1", Kind: access.AuthoringSessionHumanCLI, ClientID: access.AuthoringCLIClientID, CreatedAt: time.Unix(1, 0), Scope: access.AuthoringScope{TargetID: "instance", ProjectID: "project", Capabilities: []access.Capability{access.CapabilityResourcePublish}}}}}, LocalPasswordEnabled: true}
}

func TestServiceLoadBuildsPersonalSettingsSignal(t *testing.T) {
	permission, err := access.NewExactPermissionPair(access.ActionDashboardRead, "project-1", mustPersonalResourceRef(t, "dashboard-1", "dashboard"))
	if err != nil {
		t.Fatal(err)
	}
	repo := &fakeRepository{
		principal: access.Principal{ID: "principal-1", Kind: access.PrincipalKindUser, Email: "user@example.com", DisplayName: "User"},
		identity:  access.PrincipalIdentityManagement{Source: access.IdentityManagementLocal, HasLocalPassword: true},
		sessions:  []access.Session{{ID: "browser-1", Kind: access.SessionKindBrowser, CreatedAt: "today"}, {ID: "desktop-1", Kind: access.SessionKindDesktop, ClientID: "LeapView Desktop"}},
		tokens: []access.APIToken{
			{ID: "token-1", Name: "CI", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{permission}},
			{ID: "token-revoked", Name: "Old CI", RevokedAt: "yesterday"},
		},
		theme: access.ThemeDark,
	}
	service := testService(repo)
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
	if len(state.Security.AuthoringSessions) != 1 || state.Security.AuthoringSessions[0].Capabilities[0] != string(access.CapabilityResourcePublish) {
		t.Fatalf("authoring sessions = %#v", state.Security.AuthoringSessions)
	}
	if len(state.Tokens.Items) != 1 || state.Tokens.Items[0].ID != "token-1" {
		t.Fatalf("tokens = %#v", state.Tokens)
	}
	if state.Tokens.Items[0].PermissionProfile == nil || *state.Tokens.Items[0].PermissionProfile != access.PermissionCatalogProfile || len(state.Tokens.Items[0].Permissions) != 1 {
		t.Fatalf("typed token = %#v", state.Tokens.Items[0])
	}
	if !state.Tokens.PermissionOptionsReady {
		t.Fatal("typed permission options should be marked authoritative")
	}
	if len(state.Tokens.Capabilities) != 0 {
		t.Fatalf("capability options = %#v, want fail-closed empty options without typed authority", state.Tokens.Capabilities)
	}
}

func TestServiceLoadProjectsExactTypedPermissionOptions(t *testing.T) {
	repo := &fakeRepository{principal: access.Principal{ID: "principal-1", Kind: access.PrincipalKindUser}}
	pair, err := access.NewProjectPermissionPair(access.ActionProjectSettingsRead, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	service := testService(repo)
	service.CurrentEffectivePermissionOptions = func(context.Context, string) ([]access.PermissionPair, error) {
		return []access.PermissionPair{pair}, nil
	}
	state, err := service.Load(context.Background(), repo.principal.ID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tokens.Capabilities) != 1 || state.Tokens.Capabilities[0].Permissions == nil {
		t.Fatalf("typed capability options = %#v", state.Tokens.Capabilities)
	}
	if got := (*state.Tokens.Capabilities[0].Permissions)[0].Action; got != string(pair.Action) {
		t.Fatalf("typed option action = %q, want %q", got, pair.Action)
	}
	option := state.Tokens.Capabilities[0]
	if option.Label != "View project settings" || option.Description != "Current project" || option.Category != "Project administration" {
		t.Fatalf("typed option presentation = %#v", option)
	}
	if strings.Contains(option.Label, "project-1") || strings.Contains(option.Description, "project-1") {
		t.Fatalf("typed option leaks raw project ID in default presentation: %#v", option)
	}
}

func TestServiceLoadFailsClosedWhenTypedPermissionProviderIsMissing(t *testing.T) {
	repo := &fakeRepository{principal: access.Principal{ID: "principal-1", Kind: access.PrincipalKindUser}}
	service := testService(repo)
	state, err := service.Load(context.Background(), repo.principal.ID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Tokens.PermissionOptionsReady || state.Tokens.Capabilities == nil || len(state.Tokens.Capabilities) != 0 {
		t.Fatalf("typed options = %#v, ready = %v; want explicit empty authority", state.Tokens.Capabilities, state.Tokens.PermissionOptionsReady)
	}
}

func TestServiceTypedTokenRequiresDurableExactPermissionAuthority(t *testing.T) {
	repo := &fakeRepository{principal: access.Principal{ID: "principal-1", Kind: access.PrincipalKindUser}}
	allowed, err := access.NewExactPermissionPair(access.ActionDashboardRead, "project-1", mustPersonalResourceRef(t, "dashboard_a", "dashboard"))
	if err != nil {
		t.Fatal(err)
	}
	unauthorized, err := access.NewExactPermissionPair(access.ActionDashboardRead, "project-1", mustPersonalResourceRef(t, "dashboard_b", "dashboard"))
	if err != nil {
		t.Fatal(err)
	}
	service := testService(repo)
	service.CurrentEffectivePermissionOptions = func(context.Context, string) ([]access.PermissionPair, error) {
		return []access.PermissionPair{allowed}, nil
	}
	_, err = service.ApplyToken(context.Background(), "principal-1", TokenCommand{
		Action: "create", Name: "cross-resource", Permissions: []PermissionPairSignal{permissionPairSignal(allowed), permissionPairSignal(unauthorized)},
	})
	if !errors.Is(err, access.ErrTokenPermissionNotAllowed) {
		t.Fatalf("unauthorized typed service issuance error = %v, want %v", err, access.ErrTokenPermissionNotAllowed)
	}
	if repo.scopedInput.Permissions != nil {
		t.Fatalf("unauthorized typed service request reached persistence: %#v", repo.scopedInput.Permissions)
	}
}

func TestServiceTypedTokenAllowsDurableExactPermissionAuthority(t *testing.T) {
	repo := &fakeRepository{principal: access.Principal{ID: "principal-1", Kind: access.PrincipalKindUser}}
	pair, err := access.NewProjectPermissionPair(access.ActionProjectSettingsRead, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	service := testService(repo)
	service.CurrentEffectivePermissionOptions = func(context.Context, string) ([]access.PermissionPair, error) {
		return []access.PermissionPair{pair}, nil
	}
	secret, err := service.ApplyToken(context.Background(), "principal-1", TokenCommand{
		Action: "create", Name: "project reader", Permissions: []PermissionPairSignal{permissionPairSignal(pair)},
	})
	if err != nil || secret == nil || *secret != "lv_typed_secret" {
		t.Fatalf("authorized typed service issuance = %v, %v", secret, err)
	}
	if len(repo.scopedInput.Permissions) != 1 || repo.scopedInput.Permissions[0] != pair {
		t.Fatalf("persisted typed permissions = %#v, want %#v", repo.scopedInput.Permissions, []access.PermissionPair{pair})
	}
}

func mustPersonalResourceRef(t *testing.T, id, kind string) access.ResourceRef {
	t.Helper()
	resource, err := access.NewResourceRef(projectgraph.ResourceID(id), projectgraph.Kind(kind))
	if err != nil {
		t.Fatal(err)
	}
	return resource
}

func TestServiceLoadIncludesOnlyUniqueActiveSessions(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	stamp := func(value time.Time) string { return value.Format(time.RFC3339Nano) }
	repo := &fakeRepository{
		principal: access.Principal{ID: "principal-1", Kind: access.PrincipalKindUser, Email: "user@example.com"},
		identity:  access.PrincipalIdentityManagement{Source: access.IdentityManagementLocal},
		sessions: []access.Session{
			{ID: "current", Kind: access.SessionKindBrowser, ExpiresAt: stamp(now.Add(time.Hour))},
			{ID: "current", Kind: access.SessionKindBrowser, ExpiresAt: stamp(now.Add(2 * time.Hour))},
			{ID: "expired", Kind: access.SessionKindBrowser, ExpiresAt: stamp(now)},
			{ID: "revoked", Kind: access.SessionKindDesktop, RevokedAt: stamp(now.Add(-time.Minute))},
			{ID: "desktop", Kind: access.SessionKindDesktop, ClientID: "  Desktop app  ", AbsoluteExpiresAt: stamp(now.Add(time.Hour))},
		},
	}
	service := testService(repo)
	service.Now = func() time.Time { return now }
	service.Authoring = &fakeAuthoring{sessions: []access.AuthoringSession{
		{ID: "cli", Kind: access.AuthoringSessionHumanCLI, ExpiresAt: now.Add(time.Hour)},
		{ID: "cli", Kind: access.AuthoringSessionHumanCLI, ExpiresAt: now.Add(2 * time.Hour)},
		{ID: "old-cli", Kind: access.AuthoringSessionHumanCLI, ExpiresAt: now},
		{ID: "revoked-cli", Kind: access.AuthoringSessionHumanCLI, ExpiresAt: now.Add(time.Hour), RevokedAt: now.Add(-time.Minute)},
	}}

	state, err := service.Load(context.Background(), repo.principal.ID, "current", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Security.Sessions) != 2 {
		t.Fatalf("active sessions = %#v, want one browser and one desktop", state.Security.Sessions)
	}
	if state.Security.Sessions[0].ID != "current" || !state.Security.Sessions[0].Current {
		t.Fatalf("current session = %#v", state.Security.Sessions[0])
	}
	if state.Security.Sessions[1].ID != "desktop" || state.Security.Sessions[1].ClientLabel != "Desktop app" {
		t.Fatalf("desktop session = %#v", state.Security.Sessions[1])
	}
	if len(state.Security.AuthoringSessions) != 1 || state.Security.AuthoringSessions[0].ID != "cli" {
		t.Fatalf("active authoring sessions = %#v", state.Security.AuthoringSessions)
	}
}

func TestServiceMutationsAuditAndValidateIdentity(t *testing.T) {
	repo := &fakeRepository{principal: access.Principal{ID: "principal-1", Kind: access.PrincipalKindUser, Email: "user@example.com"}, identity: access.PrincipalIdentityManagement{Source: access.IdentityManagementLocal, HasLocalPassword: true}}
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
	allowed, err := access.NewExactPermissionPair(access.ActionDashboardRead, "project-1", mustPersonalResourceRef(t, "dashboard_a", "dashboard"))
	if err != nil {
		t.Fatal(err)
	}
	service.CurrentEffectivePermissionOptions = func(context.Context, string) ([]access.PermissionPair, error) {
		return []access.PermissionPair{allowed}, nil
	}
	secret, err := service.ApplyToken(context.Background(), "principal-1", TokenCommand{Action: "create", Name: "CI", Description: "Reporting automation", Permissions: []PermissionPairSignal{permissionPairSignal(allowed)}})
	if err != nil || secret == nil || *secret != "lv_typed_secret" {
		t.Fatalf("create token = %v, %v", secret, err)
	}
	if !repo.passwordChanged || !repo.createdToken || !repo.themeChanged || repo.theme != access.ThemeDarkColorblind || len(repo.audits) != 4 {
		t.Fatalf("mutations changed=%v token=%v audits=%d", repo.passwordChanged, repo.createdToken, len(repo.audits))
	}
	if got := repo.tokens[len(repo.tokens)-1].Description; got != "Reporting automation" {
		t.Fatalf("token description = %q", got)
	}
	unauthorized, err := access.NewExactPermissionPair(access.ActionDashboardRead, "project-1", mustPersonalResourceRef(t, "dashboard_b", "dashboard"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyToken(context.Background(), "principal-1", TokenCommand{Action: "create", Name: "Escalating", Permissions: []PermissionPairSignal{permissionPairSignal(unauthorized)}}); err == nil || !errors.Is(err, access.ErrTokenPermissionNotAllowed) {
		t.Fatalf("escalating token permission error = %v", err)
	}
	if secret, err := service.ApplyToken(context.Background(), "principal-1", TokenCommand{Action: "create", Name: "Deny all", Permissions: []PermissionPairSignal{}}); err != nil || secret == nil {
		t.Fatalf("explicit deny-all token = %v, %v", secret, err)
	}
	repo.identity.Source = access.IdentityManagementExternal
	if err := service.ApplyProfile(context.Background(), "principal-1", ProfileCommand{Action: "save", DisplayName: "Nope"}); !errors.Is(err, ErrDisplayNameManaged) {
		t.Fatalf("external profile error = %v", err)
	}
}

func TestServiceRequiresTypedPermissionTokenCreation(t *testing.T) {
	repo := &fakeRepository{
		principal: access.Principal{ID: "principal-1", Kind: access.PrincipalKindUser},
	}
	service := testService(repo)

	state, err := service.Load(context.Background(), repo.principal.ID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tokens.Capabilities) != 0 {
		t.Fatalf("capability options = %#v, want no legacy-to-typed projection", state.Tokens.Capabilities)
	}

	if _, err := service.ApplyToken(context.Background(), repo.principal.ID, TokenCommand{
		Action: "create", Name: "omitted", Permissions: nil,
	}); !errors.Is(err, access.ErrTokenPermissionsNeeded) {
		t.Fatalf("omitted permissions error = %v", err)
	}
}
