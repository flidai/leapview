package http

import (
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestPlatformRoleApprovalInvalidRequestPersistsScopedDenialEvidence(t *testing.T) {
	ctx := context.Background()
	store := openAccessHTTPTestStore(t)
	repo := store.repository
	admin, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "approval-audit-admin@example.test"})
	if err == nil {
		_, err = repo.SetPlatformRole(ctx, access.PlatformRoleInput{PrincipalID: admin.ID, Email: admin.Email, Role: access.PlatformRoleAdmin})
	}
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{
		Repository: func() (access.Repository, error) { return repo, nil },
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: admin.ID, Kind: access.PrincipalKindUser}, true
		},
	}
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/platform-role-approvals", nil)
	request.Header.Set("Idempotency-Key", "approval-invalid-1")
	request.Header.Set("X-Request-ID", "approval-request-1")
	request.Header.Set("X-Correlation-ID", "approval-correlation-1")
	response := httptest.NewRecorder()
	handler.RequestPlatformRoleApproval(response, request)
	if response.Code != stdhttp.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want bad request", response.Code, response.Body.String())
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: "platform_role_approval.requested"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("denial events=%#v, want one", events)
	}
	event := events[0]
	if event.PrincipalID != admin.ID || event.ResourceID != request.URL.Path || event.Status != "denied" || event.RequestID != "approval-request-1" || event.CorrelationID != "approval-correlation-1" {
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
	if metadata.Outcome != "denied" || metadata.Reason != string(access.AuditReasonInvalidRequest) || metadata.IdempotencyKey != "approval-invalid-1" {
		t.Fatalf("denial metadata=%#v", metadata)
	}
}
