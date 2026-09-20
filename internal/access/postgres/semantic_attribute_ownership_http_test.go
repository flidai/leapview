package postgres

import (
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesshttp "github.com/flidai/leapview/internal/access/http"
	accessownership "github.com/flidai/leapview/internal/access/ownership"
	"github.com/flidai/leapview/internal/semanticvalue"
	"github.com/go-chi/chi/v5"
)

func TestResolveSemanticAttributeOwnershipHTTPReturnsEvidenceAndAuditsPostgreSQL18(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	admin, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "semantic-http-admin@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "semantic-http-owner@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "semantic-http-target@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{PrincipalID: admin.ID, Email: admin.Email, Role: access.PlatformRoleAdmin}); err != nil {
		t.Fatal(err)
	}
	definition, err := repo.RegisterSemanticAttribute(ctx, access.RegisterSemanticAttributeInput{
		Name: "ownership_http_region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar,
		Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerPrincipal, ID: owner.ID}, DisplayName: "Ownership HTTP region"},
		Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: admin.ID, RequestID: "semantic-http-register"},
	})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := accessownership.New(NewSemanticAttributeOwnershipAuthority(db.runtime))
	if err != nil {
		t.Fatal(err)
	}
	repo.SetOwnershipGuard(inventory)
	handler := accesshttp.Handler{
		Repository: func() (access.Repository, error) { return repo, nil },
		CurrentPrincipal: func(*stdhttp.Request) (accesshttp.Principal, bool) {
			return accesshttp.Principal{ID: admin.ID, Kind: access.PrincipalKindUser}, true
		},
	}
	resolve := func(key string) (int, accessgenOwnershipResolutionResponse, string) {
		req := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/principals/"+owner.ID+"/ownership", strings.NewReader(`{"action":"transfer","targetPrincipalId":"`+target.ID+`"}`))
		req.Header.Set("Idempotency-Key", key)
		route := chi.NewRouteContext()
		route.URLParams.Add("principal", owner.ID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		response := httptest.NewRecorder()
		handler.ResolvePrincipalOwnership(response, req)
		var body accessgenOwnershipResolutionResponse
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil && response.Code == stdhttp.StatusOK {
			t.Fatalf("decode ownership response: %v; body=%s", err, response.Body.String())
		}
		return response.Code, body, response.Body.String()
	}

	status, first, raw := resolve("semantic-http-transfer-1")
	if status != stdhttp.StatusOK || first.Action != "transfer" || first.PrincipalID != owner.ID || len(first.Objects) != 1 || first.Objects[0].ID != definition.ID {
		t.Fatalf("first ownership resolution status=%d body=%s response=%#v", status, raw, first)
	}
	status, second, raw := resolve("semantic-http-transfer-2")
	if status != stdhttp.StatusOK || second.Action != "transfer" || len(second.Objects) != 0 {
		t.Fatalf("retry ownership resolution status=%d body=%s response=%#v", status, raw, second)
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{PrincipalID: admin.ID, Action: "principal.ownership.resolved", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || !strings.Contains(events[0].MetadataJSON, `"objectCount": 0`) || !strings.Contains(events[1].MetadataJSON, `"objectCount": 1`) {
		t.Fatalf("ownership audit events = %#v", events)
	}
	if err := repo.DeletePrincipal(ctx, owner.ID); err != nil {
		t.Fatalf("delete source after HTTP transfer: %v", err)
	}
}

type accessgenOwnershipResolutionResponse struct {
	PrincipalID string `json:"principalId"`
	Action      string `json:"action"`
	Objects     []struct {
		ID string `json:"id"`
	} `json:"objects"`
}
