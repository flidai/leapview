package productsettings

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/admin/product"
	"github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

func TestHandlerBootstrapAndManagePlatformGuard(t *testing.T) {
	service, err := testProductService()
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(HTTPConfig{ReadModel: ReadModel{Service: service}, CanManage: func(*http.Request) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/admin/settings", nil)
	signal, err := handler.Bootstrap(request, "authentication")
	if err != nil {
		t.Fatal(err)
	}
	if signal.Active != "authentication" || signal.CanManage {
		t.Fatalf("bootstrap = %#v", signal)
	}
	response := httptest.NewRecorder()
	handler.Command(response, httptest.NewRequest(http.MethodPost, "/admin/settings/command", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("guard status = %d body=%s", response.Code, response.Body.String())
	}
}

type platformSettingsTestRepository struct {
	access.Repository
	state     access.PlatformAdministratorState
	principal access.Principal
	audit     access.AuditEventInput
}

func (r *platformSettingsTestRepository) ListPlatformAdministrators(_ context.Context) (access.PlatformAdministratorState, error) {
	return r.state, nil
}

func (r *platformSettingsTestRepository) ListPlatformAdminAuthorities(_ context.Context) (access.PlatformAdministratorState, error) {
	return r.state, nil
}

func (r *platformSettingsTestRepository) RunAuditedMutation(ctx context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	audit, err := mutation(r)
	if err == nil {
		r.audit = audit
	}
	return err
}

func (r *platformSettingsTestRepository) GrantPlatformAdmin(_ context.Context, input access.PlatformAdminGrantInput) (access.PlatformAdminGrantResult, error) {
	if input.ExpectedRevision != r.state.Revision {
		return access.PlatformAdminGrantResult{}, access.ErrPlatformAdminStaleRevision
	}
	r.state.Administrators = append(r.state.Administrators, access.PlatformAdministrator{BindingID: "binding-new", Principal: r.principal, Role: access.PlatformRoleAdmin, CreatedAt: "2026-09-18T00:00:00Z"})
	revision, err := access.PlatformAdministratorRevision(r.state.Administrators)
	if err != nil {
		return access.PlatformAdminGrantResult{}, err
	}
	r.state.Revision = revision
	return access.PlatformAdminGrantResult{Administrator: r.state.Administrators[len(r.state.Administrators)-1], State: r.state}, nil
}

func (r *platformSettingsTestRepository) RevokePlatformAdmin(_ context.Context, input access.PlatformAdminRevokeInput) (access.PlatformAdministratorState, error) {
	if input.ExpectedRevision != r.state.Revision {
		return access.PlatformAdministratorState{}, access.ErrPlatformAdminStaleRevision
	}
	return r.state, nil
}

func platformSettingsCommandRequest(t *testing.T, action, principalID, revision string) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{"productSettingsCommand": map[string]any{
		"action": action, "principalId": principalID, "expectedRevision": revision,
	}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/product-settings/command?section=authentication", bytes.NewReader(body))
	request.Header.Set("Idempotency-Key", "ui-command-1")
	request.Header.Set(uicommand.HeaderOperationID, accessgen.GenUIActionGrantPlatformAdministrator().OperationID())
	return request
}

func newPlatformSettingsHandler(t *testing.T, repository *platformSettingsTestRepository, authenticatedAt time.Time, credential access.APICredential, credentialOK bool) *Handler {
	t.Helper()
	service, err := testProductService()
	if err != nil {
		t.Fatal(err)
	}
	return mustHandler(t, HTTPConfig{
		ReadModel:              ReadModel{Service: service, PlatformAdministration: repository},
		CanManage:              func(*http.Request) bool { return true },
		CurrentPrincipal:       func(*http.Request) (product.Principal, bool) { return product.Principal{ID: "actor"}, true },
		PlatformAdministration: repository, PlatformWriter: repository, AuditRepository: repository,
		CurrentCredential:         func(*http.Request) (access.APICredential, bool) { return credential, credentialOK },
		InteractiveAuthentication: func(*http.Request) (time.Time, bool) { return authenticatedAt, !authenticatedAt.IsZero() },
		Now:                       func() time.Time { return time.Date(2026, 9, 18, 0, 10, 0, 0, time.UTC) },
		PlatformCommands: map[string]uicommand.Binding{
			"grant_platform_administrator":  accessgen.GenUIActionGrantPlatformAdministrator(),
			"revoke_platform_administrator": accessgen.GenUIActionRevokePlatformAdministrator(),
		},
	})
}

func mustHandler(t *testing.T, config HTTPConfig) *Handler {
	t.Helper()
	handler, err := NewHandler(config)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func decodePlatformProblem(t *testing.T, response *httptest.ResponseRecorder) transport.ProblemDetails {
	t.Helper()
	var problem transport.ProblemDetails
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v body=%s", err, response.Body.String())
	}
	return problem
}

func TestPlatformAdministratorSettingsRequiresRecentInteractiveAuthentication(t *testing.T) {
	initial := access.PlatformAdministrator{BindingID: "binding-admin", Principal: access.Principal{ID: "admin"}, Role: access.PlatformRoleAdmin, CreatedAt: "2026-09-17T00:00:00Z"}
	revision, err := access.PlatformAdministratorRevision([]access.PlatformAdministrator{initial})
	if err != nil {
		t.Fatal(err)
	}
	repository := &platformSettingsTestRepository{state: access.PlatformAdministratorState{Administrators: []access.PlatformAdministrator{initial}, Revision: revision}, principal: access.Principal{ID: "target"}}
	tests := []struct {
		name         string
		at           time.Time
		credential   access.APICredential
		credentialOK bool
		wantStatus   int
		wantCode     string
	}{
		{name: "stale browser session", at: time.Date(2026, 9, 17, 23, 0, 0, 0, time.UTC), wantStatus: http.StatusUnauthorized, wantCode: "RECENT_AUTHENTICATION_REQUIRED"},
		{name: "service token", at: time.Date(2026, 9, 18, 0, 9, 0, 0, time.UTC), credential: access.APICredential{Token: access.APIToken{ID: "service-token"}}, credentialOK: true, wantStatus: http.StatusForbidden, wantCode: "INTERACTIVE_AUTHENTICATION_REQUIRED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newPlatformSettingsHandler(t, repository, test.at, test.credential, test.credentialOK)
			response := httptest.NewRecorder()
			handler.Command(response, platformSettingsCommandRequest(t, "grant_platform_administrator", "target", revision))
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), test.wantStatus)
			}
			problem := decodePlatformProblem(t, response)
			if problem.Code != test.wantCode {
				t.Fatalf("problem code=%q, want %q", problem.Code, test.wantCode)
			}
		})
	}
}

func TestPlatformAdministratorSettingsRequiresApprovalWhenConfigured(t *testing.T) {
	revision, err := access.PlatformAdministratorRevision(nil)
	if err != nil {
		t.Fatal(err)
	}
	repository := &platformSettingsTestRepository{state: access.PlatformAdministratorState{Revision: revision}, principal: access.Principal{ID: "target"}}
	handler := newPlatformSettingsHandler(t, repository, time.Date(2026, 9, 18, 0, 9, 0, 0, time.UTC), access.APICredential{}, false)
	handler.config.RequirePlatformRoleApproval = true
	response := httptest.NewRecorder()
	handler.Command(response, platformSettingsCommandRequest(t, "grant_platform_administrator", "target", revision))
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), http.StatusConflict)
	}
	problem := decodePlatformProblem(t, response)
	if problem.Code != "PLATFORM_ADMIN_APPROVAL_REQUIRED" {
		t.Fatalf("problem code=%q, want PLATFORM_ADMIN_APPROVAL_REQUIRED", problem.Code)
	}
	if repository.audit.Action != "" || len(repository.state.Administrators) != 0 {
		t.Fatalf("approval-required command mutated state: audit=%#v state=%#v", repository.audit, repository.state)
	}
}

func TestPlatformAdministratorSettingsUsesServerFreshnessAndAuditedCASMutation(t *testing.T) {
	revision, err := access.PlatformAdministratorRevision(nil)
	if err != nil {
		t.Fatal(err)
	}
	repository := &platformSettingsTestRepository{state: access.PlatformAdministratorState{Administrators: []access.PlatformAdministrator{}, Revision: revision}, principal: access.Principal{ID: "target", Email: "target@example.test", DisplayName: "Target"}}
	handler := newPlatformSettingsHandler(t, repository, time.Date(2026, 9, 18, 0, 9, 0, 0, time.UTC), access.APICredential{}, false)
	request := platformSettingsCommandRequest(t, "grant_platform_administrator", "target", revision)
	request.Header.Set(uicommand.HeaderOperationID, accessgen.GenUIActionGrantPlatformAdministrator().OperationID())
	response := httptest.NewRecorder()
	handler.Command(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", response.Code, response.Body.String())
	}
	if repository.audit.Action != "platform_admin.granted" {
		t.Fatalf("audit action=%q, want platform_admin.granted", repository.audit.Action)
	}
	if len(repository.state.Administrators) != 1 {
		t.Fatalf("administrators=%#v, want one", repository.state.Administrators)
	}
}
