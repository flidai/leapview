package productsettings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/admin/product"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

func TestSettingsPlatformAdministratorStaleAttemptPersistsScopedDenial(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := accesssqlite.NewRepository(store.SQLDB())
	actor, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "settings-audit-actor", Kind: access.PrincipalKindUser, Email: "settings-audit@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &settingsDenialRepository{Repository: repo, state: access.PlatformAdministratorState{Revision: "settings-revision-1"}}
	service, err := testProductService()
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(HTTPConfig{
		ReadModel:              ReadModel{Service: service, PlatformAdministration: wrapped},
		CanManage:              func(*http.Request) bool { return true },
		CurrentPrincipal:       func(*http.Request) (product.Principal, bool) { return product.Principal{ID: actor.ID}, true },
		PlatformAdministration: wrapped,
		PlatformWriter:         wrapped,
		AuditRepository:        wrapped,
		InteractiveAuthentication: func(*http.Request) (time.Time, bool) {
			return time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC), true
		},
		Now: func() time.Time { return time.Date(2026, 9, 18, 0, 1, 0, 0, time.UTC) },
		PlatformCommands: map[string]uicommand.Binding{
			"grant_platform_administrator": accessgen.GenUIActionGrantPlatformAdministrator(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := platformSettingsCommandRequest(t, "grant_platform_administrator", "settings-target", wrapped.state.Revision)
	request.Header.Set("X-Request-ID", "settings-request-1")
	request.Header.Set("X-Correlation-ID", "settings-correlation-1")
	response := httptest.NewRecorder()
	handler.Command(response, request)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("status=%d body=%s, want precondition failure", response.Code, response.Body.String())
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: "platform_admin.denied"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("denial events=%#v, want one", events)
	}
	event := events[0]
	if event.PrincipalID != actor.ID || event.ResourceID != "settings-target" || event.RequestID != "settings-request-1" || event.CorrelationID != "settings-correlation-1" || event.Status != "denied" {
		t.Fatalf("scoped denial evidence=%#v", event)
	}
	var payload struct {
		Outcome        string `json:"outcome"`
		Reason         string `json:"reason"`
		TargetID       string `json:"targetId"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := json.Unmarshal([]byte(event.MetadataJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Outcome != "denied" || payload.Reason != string(access.AuditReasonPreconditionFailed) || payload.TargetID != "settings-target" || payload.IdempotencyKey != "ui-command-1" {
		t.Fatalf("denial payload=%#v", payload)
	}
}

type settingsDenialRepository struct {
	access.Repository
	state access.PlatformAdministratorState
}

func (r *settingsDenialRepository) ListPlatformAdminAuthorities(context.Context) (access.PlatformAdministratorState, error) {
	return r.state, nil
}

func (r *settingsDenialRepository) RunAuditedMutation(_ context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	_, err := mutation(r)
	return err
}

func (r *settingsDenialRepository) GrantPlatformAdmin(context.Context, access.PlatformAdminGrantInput) (access.PlatformAdminGrantResult, error) {
	return access.PlatformAdminGrantResult{}, access.ErrPlatformAdminStaleRevision
}

func (r *settingsDenialRepository) RevokePlatformAdmin(context.Context, access.PlatformAdminRevokeInput) (access.PlatformAdministratorState, error) {
	return access.PlatformAdministratorState{}, access.ErrPlatformAdminStaleRevision
}
