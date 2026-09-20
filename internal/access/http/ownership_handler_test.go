package http

import (
	"context"
	"database/sql"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/go-chi/chi/v5"
)

type ownershipResolutionRepository struct {
	access.Repository
	principals  map[string]access.Principal
	admin       bool
	transfer    bool
	tombstone   bool
	audit       access.AuditEventInput
	ownedReport access.OwnershipReport
}

func (r *ownershipResolutionRepository) PrincipalByID(_ context.Context, id string) (access.Principal, error) {
	principal, ok := r.principals[id]
	if !ok {
		return access.Principal{}, sql.ErrNoRows
	}
	return principal, nil
}

func (r *ownershipResolutionRepository) RunAuditedMutation(_ context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	event, err := mutation(r)
	if err == nil {
		r.audit = event
	}
	return err
}

func (r *ownershipResolutionRepository) TransferOwnedObjects(context.Context, string, string) (access.OwnershipReport, error) {
	r.transfer = true
	return r.ownedReport, nil
}

func (r *ownershipResolutionRepository) TombstoneOwnedObjects(context.Context, string) (access.OwnershipReport, error) {
	r.tombstone = true
	return r.ownedReport, nil
}

func ownershipResolutionTestHandler(repository access.Repository) Handler {
	return Handler{
		Repository: func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "principal_admin", Kind: access.PrincipalKindUser}, true
		},
		PlatformAdmin: func(context.Context, string) (bool, error) { return true, nil },
	}
}

func ownershipResolutionRequest(body string) *stdhttp.Request {
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/principals/principal_owner/ownership", strings.NewReader(body))
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("principal", "principal_owner")
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, ctx))
}

func TestResolvePrincipalOwnershipTransfersAndAudits(t *testing.T) {
	repository := &ownershipResolutionRepository{
		principals: map[string]access.Principal{
			"principal_owner":  {ID: "principal_owner", Kind: access.PrincipalKindUser},
			"principal_target": {ID: "principal_target", Kind: access.PrincipalKindUser},
		},
		ownedReport: access.OwnershipReport{PrincipalID: "principal_owner", Objects: []access.OwnedObject{{Kind: "dashboard", ID: "project:dashboard", OwnerPrincipalID: "principal_owner", Lifecycle: "draft", Transferable: true}}},
	}
	request := ownershipResolutionRequest(`{"action":"transfer","targetPrincipalId":"principal_target"}`)
	request.Header.Set("Idempotency-Key", "ownership-transfer-1")
	response := httptest.NewRecorder()

	ownershipResolutionTestHandler(repository).ResolvePrincipalOwnership(response, request)

	if response.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if !repository.transfer || repository.tombstone {
		t.Fatalf("mutation flags transfer=%v tombstone=%v", repository.transfer, repository.tombstone)
	}
	var body accessgen.OwnershipResolutionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Action != "transfer" || body.PrincipalId != "principal_owner" || len(body.Objects) != 1 {
		t.Fatalf("response = %#v", body)
	}
	if repository.audit.Action != "principal.ownership.resolved" || !strings.Contains(repository.audit.MetadataJSON, "OwnershipResolutionAuditPayload") || !strings.Contains(repository.audit.MetadataJSON, `"objectCount":1`) {
		t.Fatalf("audit = %#v", repository.audit)
	}
}

func TestResolvePrincipalOwnershipTombstonesWithoutTarget(t *testing.T) {
	repository := &ownershipResolutionRepository{
		principals:  map[string]access.Principal{"principal_owner": {ID: "principal_owner", Kind: access.PrincipalKindUser}},
		ownedReport: access.OwnershipReport{PrincipalID: "principal_owner", Objects: []access.OwnedObject{{Kind: "agent_conversation", ID: "conversation-1", OwnerPrincipalID: "principal_owner", Lifecycle: "active", TombstoneOnOffboard: true}}},
	}
	response := httptest.NewRecorder()
	ownershipResolutionTestHandler(repository).ResolvePrincipalOwnership(response, ownershipResolutionRequest(`{"action":"tombstone","targetPrincipalId":""}`))

	if response.Code != stdhttp.StatusOK || !repository.tombstone || repository.transfer {
		t.Fatalf("status=%d transfer=%v tombstone=%v body=%s", response.Code, repository.transfer, repository.tombstone, response.Body.String())
	}
}

func TestResolvePrincipalOwnershipRejectsInvalidRequestAndUnavailableMutation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "unknown action", body: `{"action":"delete","targetPrincipalId":""}`, want: stdhttp.StatusUnprocessableEntity},
		{name: "transfer target missing", body: `{"action":"transfer","targetPrincipalId":""}`, want: stdhttp.StatusUnprocessableEntity},
		{name: "tombstone target supplied", body: `{"action":"tombstone","targetPrincipalId":"principal_target"}`, want: stdhttp.StatusUnprocessableEntity},
		{name: "malformed body", body: `{"action":`, want: stdhttp.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &ownershipResolutionRepository{principals: map[string]access.Principal{"principal_owner": {ID: "principal_owner", Kind: access.PrincipalKindUser}}}
			response := httptest.NewRecorder()
			ownershipResolutionTestHandler(repository).ResolvePrincipalOwnership(response, ownershipResolutionRequest(test.body))
			if response.Code != test.want {
				t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), test.want)
			}
			if repository.transfer || repository.tombstone {
				t.Fatal("invalid request reached mutation")
			}
		})
	}

	readOnly := &ownershipReadOnlyRepository{principal: access.Principal{ID: "principal_owner", Kind: access.PrincipalKindUser}}
	response := httptest.NewRecorder()
	ownershipResolutionTestHandler(readOnly).ResolvePrincipalOwnership(response, ownershipResolutionRequest(`{"action":"tombstone","targetPrincipalId":""}`))
	if response.Code != stdhttp.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d body=%s, want 503", response.Code, response.Body.String())
	}
}

type ownershipReadOnlyRepository struct {
	access.Repository
	principal access.Principal
}

func (r *ownershipReadOnlyRepository) PrincipalByID(context.Context, string) (access.Principal, error) {
	return r.principal, nil
}
