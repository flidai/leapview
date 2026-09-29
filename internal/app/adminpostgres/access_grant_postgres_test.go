package adminpostgres

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/project/graph"
)

func TestOperatorGrantPostgresAuditFailureRollsBackPolicy(t *testing.T) {
	db := postgrestest.Open(t, accesspostgres.ApplySchema)
	repo, err := accesspostgres.NewAccess(db, accesspostgres.FingerprintConfig{Key: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	user, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "operator-recipient@example.com", DisplayName: "Recipient"})
	if err != nil {
		t.Fatal(err)
	}
	scope := access.AuthorizationPolicyScope{TargetID: "target:test", ProjectID: "project:test", Environment: "evaluation"}
	binding, err := access.NewTypedRoleBinding("owner", "owner", access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: user.Principal.ID}, access.PermissionRoleProjectAdmin, graph.ResourceID(scope.ProjectID))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := repo.UpsertAuthorizationRoleBinding(t.Context(), access.AuthorizationRoleBindingInput{Scope: scope, Binding: binding, IdempotencyKey: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	request := admincli.StageAccessGrantRequest{ProjectID: scope.ProjectID, GrantID: "run", PrincipalID: user.Principal.ID, ResourceID: "pipeline:test", ResourceKind: "pipeline", Actions: []string{"pipeline.run"}, ExpectedRevision: policy.Revision, OperationID: "stage-1", Apply: true}
	grant, err := request.Grant()
	if err != nil {
		t.Fatal(err)
	}
	staged, err := stageOperatorGrant(t.Context(), repo, scope, grant, request)
	if err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListAuditEvents(t.Context(), access.AuditEventFilter{ProjectID: scope.ProjectID, Action: "grant.staged", Limit: 10})
	if err != nil || len(events) != 1 {
		t.Fatalf("missing stage audit: count=%d error=%v", len(events), err)
	}
	if events[0].PrincipalID != "" || !strings.Contains(events[0].MetadataJSON, "offline_operator") {
		t.Fatal("host operator was impersonated as an application user")
	}
	replayed, err := stageOperatorGrant(t.Context(), repo, scope, grant, request)
	if err != nil || replayed.Revision != staged.Revision || replayed.Digest != staged.Digest {
		t.Fatalf("retry changed policy: %v", err)
	}
	if _, err := db.Exec(t.Context(), `ALTER TABLE audit.audit_event ADD CONSTRAINT test_reject_stage CHECK (action <> 'grant.staged') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	request.GrantID, request.OperationID, request.ExpectedRevision = "run-second", "stage-2", staged.Revision
	request.ResourceID = "pipeline:second"
	grant, err = request.Grant()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stageOperatorGrant(t.Context(), repo, scope, grant, request); err == nil {
		t.Fatal("audit failure accepted")
	}
	unchanged, err := repo.AuthorizationPolicy(t.Context(), scope)
	if err != nil || unchanged.Digest != staged.Digest || unchanged.Revision != staged.Revision {
		t.Fatalf("failed audit committed grant: %v", err)
	}
}
