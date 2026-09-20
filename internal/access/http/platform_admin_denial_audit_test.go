package http

import (
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestPlatformAdministratorStaleAttemptPersistsScopedDenialEvidence(t *testing.T) {
	ctx := context.Background()
	store := openAccessHTTPTestStore(t)
	repo := store.repository
	wrapped := &platformAdministratorDenialRepository{Repository: repo, revision: "revision-1"}
	admin, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "audit-admin@example.test", DisplayName: "audit-admin"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "audit-target@example.test", DisplayName: "audit-target"})
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{
		Repository: func() (access.Repository, error) { return wrapped, nil },
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: admin.ID, Kind: access.PrincipalKindUser}, true
		},
		PlatformAdmin: func(context.Context, string) (bool, error) { return true, nil },
	}
	request := requestWithRouteParam(stdhttp.MethodPut, "/api/v1/platform-administrators/"+target.ID, "principal", target.ID)
	request.Header.Set("If-Match", `"stale"`)
	request.Header.Set("Idempotency-Key", "audit-stale-1")
	request.Header.Set("X-Request-ID", "audit-request-1")
	request.Header.Set("X-Correlation-ID", "audit-correlation-1")
	response := httptest.NewRecorder()
	handler.GrantPlatformAdministrator(response, request)
	if response.Code != stdhttp.StatusPreconditionFailed {
		t.Fatalf("status=%d body=%s, want stale precondition", response.Code, response.Body.String())
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: "platform_admin.granted"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("denial events=%#v, want one", events)
	}
	event := events[0]
	if event.PrincipalID != admin.ID || event.ResourceID != target.ID || event.Status != "denied" || event.RequestID != "audit-request-1" || event.CorrelationID != "audit-correlation-1" {
		t.Fatalf("scoped denial evidence=%#v", event)
	}
	var metadata struct {
		Outcome        string `json:"outcome"`
		Reason         string `json:"reason"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := json.Unmarshal([]byte(event.MetadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Outcome != "denied" || metadata.Reason != string(access.AuditReasonPreconditionFailed) || metadata.IdempotencyKey != "audit-stale-1" {
		t.Fatalf("denial metadata=%#v", metadata)
	}
}

type platformAdministratorDenialRepository struct {
	access.Repository
	revision string
}

func (r *platformAdministratorDenialRepository) ListPlatformAdministrators(context.Context) (access.PlatformAdministratorState, error) {
	return access.PlatformAdministratorState{Revision: r.revision}, nil
}

func (r *platformAdministratorDenialRepository) IsPlatformAdmin(context.Context, string) (bool, error) {
	return true, nil
}

func (r *platformAdministratorDenialRepository) RunAuditedMutation(ctx context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	_, err := mutation(r)
	return err
}

func (r *platformAdministratorDenialRepository) GrantPlatformAdmin(context.Context, access.PlatformAdminGrantInput) (access.PlatformAdminGrantResult, error) {
	return access.PlatformAdminGrantResult{}, access.ErrPlatformAdminStaleRevision
}

func (r *platformAdministratorDenialRepository) RevokePlatformAdmin(context.Context, access.PlatformAdminRevokeInput) (access.PlatformAdministratorState, error) {
	return access.PlatformAdministratorState{}, access.ErrPlatformAdminStaleRevision
}

func TestPlatformAuthorizationDenialPersistsActorAndTargetEvidence(t *testing.T) {
	ctx := context.Background()
	store := openAccessHTTPTestStore(t)
	repo := store.repository
	actor, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "audit-non-admin@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{
		Repository: func() (access.Repository, error) { return repo, nil },
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: actor.ID, Kind: access.PrincipalKindUser}, true
		},
	}
	request := httptest.NewRequest(stdhttp.MethodGet, "/api/v1/platform-administrators", nil)
	request.Header.Set("Idempotency-Key", "audit-authz-1")
	request.Header.Set("X-Request-ID", "audit-request-2")
	request.Header.Set("X-Correlation-ID", "audit-correlation-2")
	response := httptest.NewRecorder()
	handler.ListPlatformAdministrators(response, request)
	if response.Code != stdhttp.StatusForbidden {
		t.Fatalf("status=%d body=%s, want forbidden", response.Code, response.Body.String())
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: "authorization.denied"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].PrincipalID != actor.ID || events[0].ResourceID != request.URL.Path || events[0].RequestID != "audit-request-2" || events[0].CorrelationID != "audit-correlation-2" {
		t.Fatalf("authorization denial events=%#v", events)
	}
	if events[0].MetadataJSON == "" || !containsAuditReason(events[0].MetadataJSON, access.AuditReasonAuthorizationDenied) {
		t.Fatalf("authorization denial metadata=%s", events[0].MetadataJSON)
	}
}

func containsAuditReason(metadata string, reason access.AuditDenialReason) bool {
	var payload map[string]any
	if json.Unmarshal([]byte(metadata), &payload) != nil {
		return false
	}
	value, _ := payload["reason"].(string)
	return value == string(reason)
}
