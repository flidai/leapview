package app

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
	projectsqlite "github.com/flidai/leapview/internal/project/sqlite"
	"github.com/flidai/leapview/internal/workspace"
)

var localAuthCSRFPattern = regexp.MustCompile(`<meta name="csrf-token" content="([^"]*)"`)

func TestLocalAuthBrowserEndToEnd(t *testing.T) {
	h, repo := newLocalAuthHarness(t)
	ctx := context.Background()
	adminToken := localAuthAdminToken(t, ctx, repo)

	created := localAuthCreateUser(t, h, adminToken, "local-analyst@example.com")
	localAuthCreateRoleBinding(t, h, adminToken, created.Principal.ID, access.RoleViewer)

	browser := newLocalAuthBrowser(t, h)
	status, location, body := browser.postCSRFForm("/auth/local/login", url.Values{
		"email":    {created.Principal.Email},
		"password": {created.TemporaryPassword},
	})
	if status != http.StatusFound || location != "/login" {
		t.Fatalf("temporary-password login status=%d location=%q body=%s", status, location, body)
	}
	if browser.cookie("lv_session") == "" {
		t.Fatal("temporary-password login did not create lv_session")
	}

	status, _, body = browser.get("/")
	if status != http.StatusForbidden {
		t.Fatalf("must-change protected API status=%d want=403 body=%s", status, body)
	}

	status, location, body = browser.postCSRFForm("/auth/local/password", url.Values{
		"currentPassword": {created.TemporaryPassword},
		"newPassword":     {"changed-local-password"},
	})
	if status != http.StatusFound || location != "/" {
		t.Fatalf("password change status=%d location=%q body=%s", status, location, body)
	}

	status, _, body = browser.get("/")
	if status != http.StatusOK {
		t.Fatalf("dashboard list with local session status=%d body=%s", status, body)
	}

	status, _, body = browser.get("/api/v1/me/sessions")
	if status != http.StatusUnauthorized {
		t.Fatalf("public API accepted browser session status=%d body=%s", status, body)
	}

	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{PrincipalID: created.Principal.ID, Limit: 20})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if !localAuthHasAuditAction(events, "sign_in") || !localAuthHasAuditAction(events, "session.created") || !localAuthHasAuditAction(events, "password.changed") {
		t.Fatalf("local auth audit actions = %#v, want sign_in/session.created/password.changed", events)
	}
}

func TestLocalAuthPasswordResetEndToEnd(t *testing.T) {
	h, repo := newLocalAuthHarness(t)
	ctx := context.Background()
	adminToken := localAuthAdminToken(t, ctx, repo)

	created := localAuthCreateUser(t, h, adminToken, "local-reset@example.com")
	browser := newLocalAuthBrowser(t, h)
	status, _, body := browser.postCSRFForm("/auth/local/login", url.Values{
		"email":    {created.Principal.Email},
		"password": {created.TemporaryPassword},
	})
	if status != http.StatusFound {
		t.Fatalf("initial login status=%d body=%s", status, body)
	}
	status, _, body = browser.postCSRFForm("/auth/local/password", url.Values{
		"currentPassword": {created.TemporaryPassword},
		"newPassword":     {"changed-before-reset"},
	})
	if status != http.StatusFound {
		t.Fatalf("initial password change status=%d body=%s", status, body)
	}

	reset := localAuthResetPassword(t, h, adminToken, created.Principal.ID)
	if reset.TemporaryPassword == "" || reset.TemporaryPassword == "changed-before-reset" {
		t.Fatalf("reset temporary password = %q", reset.TemporaryPassword)
	}
	credential, err := repo.LocalCredential(ctx, created.Principal.ID)
	if err != nil {
		t.Fatalf("local credential after reset: %v", err)
	}
	if !credential.MustChangePassword {
		t.Fatal("reset credential must_change_password = false, want true")
	}

	oldPasswordBrowser := newLocalAuthBrowser(t, h)
	status, _, body = oldPasswordBrowser.postCSRFForm("/auth/local/login", url.Values{
		"email":    {created.Principal.Email},
		"password": {"changed-before-reset"},
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("old password login status=%d want=401 body=%s", status, body)
	}
	if oldPasswordBrowser.cookie("lv_session") != "" {
		t.Fatal("old password login unexpectedly created lv_session")
	}

	resetBrowser := newLocalAuthBrowser(t, h)
	status, location, body := resetBrowser.postCSRFForm("/auth/local/login", url.Values{
		"email":    {created.Principal.Email},
		"password": {reset.TemporaryPassword},
	})
	if status != http.StatusFound || location != "/login" {
		t.Fatalf("reset-password login status=%d location=%q body=%s", status, location, body)
	}
	status, _, body = resetBrowser.get("/")
	if status != http.StatusForbidden {
		t.Fatalf("reset must-change protected API status=%d want=403 body=%s", status, body)
	}
}

func newLocalAuthHarness(t *testing.T) (*harness, *accesssqlite.Repository) {
	t.Helper()
	h, metrics, catalogPath := newHarnessWithMetrics(t)
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	workspaceID := metrics.Catalog().Workspace.ID
	if workspaceID == "" {
		t.Fatal("local-auth runtime catalog has no workspace ID")
	}
	if err := projectsqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{
		ID:          workspace.WorkspaceID(workspaceID),
		Title:       metrics.Catalog().Workspace.Title,
		Description: metrics.Catalog().Workspace.Description,
	}); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	seedIntegrationActiveDeployment(t, store, workspaceID, catalogPath)
	repo := accesssqlite.NewRepository(store.SQLDB())
	auth := NewAuth(repo, AuthConfig{LocalAuth: true, CSRFKey: strings.Repeat("l", 32)})
	server := assembleRuntime(metrics, testStoreOptions(store, assemblyConfig{Auth: auth, WorkspaceID: workspaceID}))
	h.store = store
	h.handler = server.Routes()
	h.server = httptest.NewServer(h.handler)
	t.Cleanup(h.server.Close)
	h.workspaceID = workspaceID
	return h, repo
}

func localAuthAdminToken(t *testing.T, ctx context.Context, repo *accesssqlite.Repository) string {
	t.Helper()
	admin := authSpecPrincipal(t, ctx, repo, "local-admin@example.com")
	authSpecGrant(t, ctx, repo, access.WorkspaceObject("sales"), access.SubjectPrincipal, admin.ID, access.PrivilegeManageGrants)
	return authSpecToken(t, ctx, repo, access.APITokenInput{
		PrincipalID: admin.ID,
		WorkspaceID: "sales",
		Name:        "local-admin",
		Privileges:  []access.Privilege{access.PrivilegeManageGrants},
	})
}

type localAuthUserResponse struct {
	Principal struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"principal"`
	TemporaryPassword string `json:"temporaryPassword"`
}

func localAuthCreateUser(t *testing.T, h *harness, token, email string) localAuthUserResponse {
	t.Helper()
	status, body := h.authSpecDo(t, http.MethodPost, "/api/v1/principals", token, `{"email":"`+email+`","displayName":"`+email+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("create local user status=%d body=%s", status, body)
	}
	var created localAuthUserResponse
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode local user response: %v body=%s", err, body)
	}
	if created.Principal.ID == "" || created.Principal.Email != email || created.TemporaryPassword == "" {
		t.Fatalf("created local user = %#v", created)
	}
	return created
}

func localAuthResetPassword(t *testing.T, h *harness, token, principalID string) localAuthUserResponse {
	t.Helper()
	status, body := h.authSpecDo(t, http.MethodPost, "/api/v1/principals/"+principalID+"/password-reset", token, "")
	if status != http.StatusOK {
		t.Fatalf("reset local user password status=%d body=%s", status, body)
	}
	var reset localAuthUserResponse
	if err := json.Unmarshal([]byte(body), &reset); err != nil {
		t.Fatalf("decode reset response: %v body=%s", err, body)
	}
	return reset
}

func localAuthCreateRoleBinding(t *testing.T, h *harness, token, principalID, role string) {
	t.Helper()
	body := `{"subjectType":"principal","subjectId":"` + principalID + `","role":"` + role + `"}`
	status, response := h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/role-bindings", token, body)
	if status != http.StatusCreated {
		t.Fatalf("create local user role binding status=%d body=%s", status, response)
	}
}

type localAuthBrowser struct {
	t       *testing.T
	h       *harness
	client  *http.Client
	cookies map[string]*http.Cookie
}

func newLocalAuthBrowser(t *testing.T, h *harness) *localAuthBrowser {
	t.Helper()
	return &localAuthBrowser{
		t: t,
		h: h,
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		cookies: map[string]*http.Cookie{},
	}
}

func (b *localAuthBrowser) postCSRFForm(path string, form url.Values) (int, string, string) {
	b.t.Helper()
	token := b.csrfToken()
	form = cloneValues(form)
	form.Set("gorilla.csrf.Token", token)
	return b.do(http.MethodPost, path, "application/x-www-form-urlencoded", bytes.NewBufferString(form.Encode()))
}

func (b *localAuthBrowser) csrfToken() string {
	b.t.Helper()
	status, _, body := b.get("/login")
	if status != http.StatusOK {
		b.t.Fatalf("GET /login status=%d body=%s", status, body)
	}
	matches := localAuthCSRFPattern.FindStringSubmatch(html.UnescapeString(body))
	if len(matches) != 2 || strings.TrimSpace(matches[1]) == "" {
		b.t.Fatalf("login page missing csrf meta token:\n%s", body)
	}
	return matches[1]
}

func (b *localAuthBrowser) get(path string) (int, string, string) {
	b.t.Helper()
	return b.do(http.MethodGet, path, "", nil)
}

func (b *localAuthBrowser) do(method, path, contentType string, body io.Reader) (int, string, string) {
	b.t.Helper()
	req, err := http.NewRequest(method, b.h.serverURL(b.t)+path, body)
	if err != nil {
		b.t.Fatalf("create %s %s: %v", method, path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, cookie := range b.cookies {
		req.AddCookie(cookie)
	}
	res, err := b.client.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	for _, cookie := range res.Cookies() {
		if cookie.MaxAge < 0 || cookie.Value == "" {
			delete(b.cookies, cookie.Name)
			continue
		}
		b.cookies[cookie.Name] = cookie
	}
	bytes, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header.Get("Location"), string(bytes)
}

func (b *localAuthBrowser) cookie(name string) string {
	if cookie := b.cookies[name]; cookie != nil {
		return cookie.Value
	}
	return ""
}

func cloneValues(values url.Values) url.Values {
	out := url.Values{}
	for key, items := range values {
		out[key] = append([]string{}, items...)
	}
	return out
}

func localAuthHasAuditAction(events []access.AuditEvent, action string) bool {
	for _, event := range events {
		if event.Action == action {
			return true
		}
	}
	return false
}
