package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
)

type grantRepositoryStub struct {
	access.Repository
	input access.AuthorizationGrantInput
	audit access.AuditEventInput
	calls int
}
type grantTransactionStub struct {
	access.Repository
	parent *grantRepositoryStub
}

func (tx *grantTransactionStub) UpsertAuthorizationGrant(_ context.Context, in access.AuthorizationGrantInput) (access.AuthorizationPolicy, error) {
	tx.parent.input = in
	tx.parent.calls++
	return access.AuthorizationPolicy{Scope: in.Scope, Revision: in.ExpectedRevision + 1, Digest: "sha256:" + strings.Repeat("a", 64), Grants: []access.AuthorizationGrant{in.Grant}}, nil
}
func (s *grantRepositoryStub) RunAuditedMutation(_ context.Context, fn func(access.Repository) (access.AuditEventInput, error)) error {
	event, err := fn(&grantTransactionStub{parent: s})
	s.audit = event
	return err
}
func TestGrantCommandUsesTransactionalPolicyAndServerScope(t *testing.T) {
	repo := &grantRepositoryStub{}
	handler := Handler{Repository: func() (access.Repository, error) { return repo, nil }, AuthorizationPolicyTargetID: "server-target", AuthorizationPolicyEnvironment: "prod"}
	body := `{"id":"demo-read","resourceKind":"dashboard","resourceId":"dashboard:sales","subjectType":"principal","subjectId":"demo","capability":"RESOURCE_READ","expectedRevision":4}`
	request := withProjectRoute(httptest.NewRequest(http.MethodPost, "/api/v1/projects/project:demo/grants", strings.NewReader(body)), "project:demo")
	ctx, guard, err := accessgen.BeginGenCreateGrantCommand(request.Context(), accessgen.GenCreateGrantCommandInvocation{Surface: apigencommand.SurfaceAPI, Project: "project:demo", IdempotencyKey: "grant-1"})
	if err != nil {
		t.Fatal(err)
	}
	request = request.WithContext(ctx)
	recorder := httptest.NewRecorder()
	NewAPIGenDispatcher(handler).CreateGrant(recorder, request, "project:demo", accessgen.GenCreateGrantHeaders{IdempotencyKey: "grant-1"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if !guard.Completed() {
		t.Fatal("grant command did not complete generated transactional contract")
	}
	if repo.calls != 1 || repo.input.IdempotencyKey != "grant-1" || repo.input.ExpectedRevision != 4 || repo.input.Scope.TargetID != "server-target" || repo.input.Scope.Environment != "prod" {
		t.Fatalf("wrong policy command: %+v", repo.input)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(repo.audit.MetadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	payload, ok := metadata["payload"].(map[string]any)
	if !ok || payload["resourceId"] != "dashboard:sales" || payload["subjectId"] != "demo" {
		t.Fatalf("missing audit payload: %v", metadata)
	}
	for _, invalid := range []string{strings.Replace(body, `,"expectedRevision":4`, "", 1), strings.Replace(body, "RESOURCE_READ", "PROJECT_ADMIN", 1), strings.Replace(body, `"id":"demo-read"`, `"id":"demo-read","targetId":"attacker"`, 1)} {
		req := withProjectRoute(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(invalid)), "project:demo")
		rec := httptest.NewRecorder()
		handler.CreateGrant(rec, req)
		if rec.Code != 400 || repo.calls != 1 {
			t.Fatalf("invalid request changed policy: %d %s", rec.Code, rec.Body.String())
		}
	}
}
