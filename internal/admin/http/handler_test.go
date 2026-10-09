package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/admin/personalsettings"
	"github.com/flidai/leapview/internal/admin/ui"
	"github.com/flidai/leapview/internal/agent/api"
	"github.com/flidai/leapview/internal/dashboard/publication"
)

func TestPublicationMutationStatusPreservesDomainSemantics(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
	}{
		{name: "not found", err: publication.ErrNotFound, status: http.StatusNotFound},
		{name: "conflict", err: publication.ErrConflict, status: http.StatusConflict},
		{name: "precondition", err: apigenfailure.Wrap("precondition", publication.ErrConflict), status: http.StatusPreconditionFailed},
		{name: "audit unavailable", err: apigenfailure.New("audit_unavailable", "private"), status: http.StatusServiceUnavailable},
		{name: "unknown storage", err: errors.New("private database detail"), status: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := publicationMutationStatus(test.err); got != test.status {
				t.Fatalf("status = %d, want %d", got, test.status)
			}
		})
	}
}

func TestDirectoryListReadsSkipUnrelatedAdminProviders(t *testing.T) {
	agentCalls, publicationCalls := 0, 0
	model := ReadModel{
		AgentDetails: func(context.Context) (api.AdminAgentResponse, error) {
			agentCalls++
			return api.AdminAgentResponse{}, nil
		},
		Publications: func(*http.Request) ([]ui.AdminPublication, bool, error) {
			publicationCalls++
			return nil, false, nil
		},
	}
	for _, path := range []string{"/admin/principals?q=analyst", "/admin/groups?q=ops"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		var err error
		if strings.Contains(path, "principals") {
			_, err = model.PrincipalsListData(req)
		} else {
			_, err = model.GroupsListData(req)
		}
		if err != nil {
			t.Fatalf("list data for %s: %v", path, err)
		}
	}
	if agentCalls != 0 || publicationCalls != 0 {
		t.Fatalf("unrelated provider calls = agent:%d publications:%d, want zero", agentCalls, publicationCalls)
	}
}

func TestAdminRootRedirectsToDefaultProfile(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)

	Handler{ReadModel: ReadModel{}}.AdminRoot(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusSeeOther, rec.Body.String())
	}
	if location := rec.Header().Get("Location"); location != "/admin/profile" {
		t.Fatalf("location = %q, want /admin/profile", location)
	}
}

func TestProfileRendersAdminOwnedPageAdapter(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/profile", nil)

	Handler{ReadModel: ReadModel{}}.Profile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "<lv-admin-page") || !strings.Contains(body, "section=profile") {
		t.Fatalf("profile handler did not render the profile route shell:\n%s", body)
	}
}

func TestPersonalSettingsRejectAuthoringCredentials(t *testing.T) {
	handler := Handler{ReadModel: ReadModel{}, CurrentCredential: func(*http.Request) (access.APICredential, bool) {
		return access.APICredential{Authoring: &access.AuthoringSession{ID: "authoring-1"}}, true
	}}
	for _, path := range []string{"/admin/profile", "/admin/security", "/admin/api-tokens", "/admin/api-tokens/new"} {
		recorder := httptest.NewRecorder()
		handler.Profile(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d, want %d", path, recorder.Code, http.StatusForbidden)
		}
	}
}

func TestBuildAdminPrincipalsKeepsEmailDuplicatesIDDistinct(t *testing.T) {
	principals := []ui.AdminPrincipal{
		{ID: "principal-old", Email: "analyst@example.com", DisplayName: "Sales Analyst", CreatedAt: "2026-08-06T00:00:00Z"},
		{ID: "principal-new", Email: "ANALYST@example.com", DisplayName: "", CreatedAt: "2026-08-08T00:00:00Z"},
		{ID: "other", Email: "other@example.com", DisplayName: "Other"},
	}
	bindings := []adminRoleBindingView{
		{SubjectType: "principal", PrincipalID: "principal-old", Role: "viewer"},
		{SubjectType: "principal", PrincipalID: "principal-new", Role: "editor"},
	}
	groups := map[string]access.Group{
		"sales": {ID: "sales", Name: "Sales"},
	}
	members := map[string][]ui.AdminPrincipalRef{
		"sales": {
			{ID: "principal-old"},
			{ID: "principal-new"},
		},
	}

	got := buildAdminPrincipals(principals, bindings, groups, members)
	if len(got) != 3 {
		t.Fatalf("principal count = %d, want 3: %#v", len(got), got)
	}
	oldPrincipal, newPrincipal := got[0], got[1]
	if oldPrincipal.ID != "principal-old" || newPrincipal.ID != "principal-new" {
		t.Fatalf("duplicate-email principals = %#v, want distinct IDs", got[:2])
	}
	if len(oldPrincipal.DirectRoles) != 1 || oldPrincipal.DirectRoles[0] != "viewer" {
		t.Fatalf("old principal roles = %#v, want viewer", oldPrincipal.DirectRoles)
	}
	if len(newPrincipal.DirectRoles) != 1 || newPrincipal.DirectRoles[0] != "editor" {
		t.Fatalf("new principal roles = %#v, want editor", newPrincipal.DirectRoles)
	}
	if len(oldPrincipal.Groups) != 1 || len(newPrincipal.Groups) != 1 {
		t.Fatalf("principal groups = %#v / %#v, want independent Sales memberships", oldPrincipal.Groups, newPrincipal.Groups)
	}
}

// End each idle updates stream after its first complete signal patch.
type personalSettingsUpdatesRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (r *personalSettingsUpdatesRecorder) Flush() {
	r.ResponseRecorder.Flush()
	if strings.Contains(r.Body.String(), "data: signals ") {
		r.cancel()
	}
}

type personalSettingsUpdatesRepository struct {
	personalsettings.Repository
	tokens []access.APIToken
}

func (*personalSettingsUpdatesRepository) PrincipalByID(_ context.Context, id string) (access.Principal, error) {
	return access.Principal{ID: id, Kind: access.PrincipalKindUser}, nil
}

func (*personalSettingsUpdatesRepository) ListSessions(context.Context, string) ([]access.Session, error) {
	return nil, nil
}

func (r *personalSettingsUpdatesRepository) ListAPITokens(context.Context, string) ([]access.APIToken, error) {
	return r.tokens, nil
}

func TestPersonalTokenUpdatesPreserveOnlyCurrentMountedSecret(t *testing.T) {
	const created = "2026-10-01T00:00:00Z"
	const modified = "2026-10-02T00:00:00Z"
	const mounted = `{"personalSettings":{"active":"api-tokens","profile":{"id":"principal-1"},"tokens":{"newToken":"browser-only-secret","items":[{"id":"token-1","createdAt":"` + created + `","modifiedAt":"` + modified + `"}]}}}`
	for _, test := range []struct {
		name, section, signals string
		changeToken            func(*access.APIToken)
		preserve               bool
	}{
		{name: "fresh bootstrap", section: "api-tokens"},
		{name: "same mounted principal and token", section: "api-tokens", signals: mounted, preserve: true},
		{name: "created token route reconnect", section: "api-token-new", signals: mounted, preserve: true},
		{name: "rotated token route reconnect", section: "api-token-edit", signals: mounted, preserve: true},
		{name: "already cleared remains cleared", section: "api-tokens", signals: strings.Replace(mounted, `"browser-only-secret"`, `null`, 1), preserve: true},
		{name: "secret input is ignored", section: "api-tokens", signals: strings.Replace(mounted, `"browser-only-secret"`, `{"untrusted":"never decode this secret"}`, 1), preserve: true},
		{name: "different principal", section: "api-tokens", signals: strings.Replace(mounted, "principal-1", "principal-2", 1)},
		{name: "different mounted surface", section: "api-tokens", signals: strings.Replace(mounted, "api-tokens", "profile", 1)},
		{name: "different requested surface", section: "profile", signals: mounted},
		{name: "revoked token", section: "api-tokens", signals: mounted, changeToken: func(token *access.APIToken) { token.RevokedAt = modified }},
		{name: "expired token", section: "api-tokens", signals: mounted, changeToken: func(token *access.APIToken) { token.ExpiresAt = "2000-01-01T00:00:00Z" }},
		{name: "replaced token", section: "api-tokens", signals: mounted, changeToken: func(token *access.APIToken) { token.ID = "replacement" }},
		{name: "changed token revision", section: "api-tokens", signals: mounted, changeToken: func(token *access.APIToken) { token.ModifiedAt = "2026-10-03T00:00:00Z" }},
		{name: "missing mounted token", section: "api-tokens", signals: `{"personalSettings":{"active":"api-tokens","profile":{"id":"principal-1"},"tokens":{"items":[]}}}`},
		{name: "malformed mounted signals", section: "api-tokens", signals: `{`},
	} {
		t.Run(test.name, func(t *testing.T) {
			token := access.APIToken{ID: "token-1", Name: "Current token", CreatedAt: created, ModifiedAt: modified}
			if test.changeToken != nil {
				test.changeToken(&token)
			}
			repository := &personalSettingsUpdatesRepository{tokens: []access.APIToken{token}}
			handler := Handler{PersonalSettings: &personalsettings.Handler{
				Service:          &personalsettings.Service{Repository: repository},
				CurrentPrincipal: func(*http.Request) (string, bool) { return "principal-1", true },
			}}
			query := url.Values{"route": {"admin"}, "section": {test.section}, "datastar": {test.signals}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			recorder := &personalSettingsUpdatesRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
			handler.BootstrapUpdates(recorder, httptest.NewRequest(http.MethodGet, "/updates?"+query.Encode(), nil).WithContext(ctx))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			var patch struct {
				Settings struct {
					Profile struct {
						ID     string          `json:"id"`
						Avatar json.RawMessage `json:"avatarUrl"`
					} `json:"profile"`
					Tokens map[string]json.RawMessage `json:"tokens"`
				} `json:"personalSettings"`
			}
			found := false
			for _, line := range strings.Split(recorder.Body.String(), "\n") {
				if data, ok := strings.CutPrefix(line, "data: signals "); ok {
					if err := json.Unmarshal([]byte(data), &patch); err != nil {
						t.Fatal(err)
					}
					found = true
				}
			}
			if !found || patch.Settings.Profile.ID != "principal-1" || string(patch.Settings.Profile.Avatar) != "null" {
				t.Fatalf("missing authorized settings refresh: %s", recorder.Body.String())
			}
			secret, present := patch.Settings.Tokens["newToken"]
			if test.preserve && present {
				t.Fatalf("reconnect overwrote the browser-only secret with %s", secret)
			}
			if !test.preserve && string(secret) != "null" {
				t.Fatalf("fresh or stale state must clear the secret, got %s", secret)
			}
			if strings.Contains(recorder.Body.String(), "browser-only-secret") || strings.Contains(recorder.Body.String(), "never decode this secret") {
				t.Fatal("updates reflected the browser's one-time secret")
			}
		})
	}
}
