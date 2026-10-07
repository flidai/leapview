package adminpostgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	admincli "github.com/flidai/leapview/internal/admin/cli"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
)

type stageGrantRepository struct {
	access.Repository
	principal access.Principal
	policy    access.AuthorizationPolicy
	writeErr  error
	writes    int
	audits    []access.AuditEventInput
}

func (r *stageGrantRepository) PrincipalByID(context.Context, string) (access.Principal, error) {
	return r.principal, nil
}
func (r *stageGrantRepository) AuthorizationPolicy(context.Context, access.AuthorizationPolicyScope) (access.AuthorizationPolicy, error) {
	return r.policy, nil
}
func (r *stageGrantRepository) AuthorizationPolicyRevision(context.Context, access.AuthorizationPolicyScope, int64) (access.AuthorizationPolicy, error) {
	return r.policy, nil
}
func (r *stageGrantRepository) UpsertAuthorizationGrant(_ context.Context, input access.AuthorizationGrantInput) (access.AuthorizationPolicy, error) {
	r.writes++
	if r.writeErr != nil {
		return access.AuthorizationPolicy{}, r.writeErr
	}
	if input.ExpectedRevision != r.policy.Revision {
		return access.AuthorizationPolicy{}, access.ErrAuthorizationPolicyStaleRevision
	}
	result := r.policy
	result.Revision++
	result.Grants = []access.AuthorizationGrant{input.Grant}
	return result, nil
}
func (r *stageGrantRepository) RunAuditedMutation(_ context.Context, fn func(access.Repository) (access.AuditEventInput, error)) error {
	event, err := fn(r)
	if err == nil {
		r.audits = append(r.audits, event)
	}
	return err
}

func TestStageOperatorGrantRequiresClaimedNamespace(t *testing.T) {
	claim := platformbootstrap.ProjectClaim{ProjectID: "project:test", Environment: "evaluation", ClaimedBy: "owner", ClaimedAt: time.Now()}
	if err := validateGrantClaim(claim, "project:test", "evaluation", "evaluation"); err != nil {
		t.Fatal(err)
	}
	for _, values := range [][3]string{{"project:other", "evaluation", "evaluation"}, {"project:test", "production", "evaluation"}, {"project:test", "evaluation", "production"}} {
		if err := validateGrantClaim(claim, values[0], values[1], values[2]); err == nil {
			t.Fatal("accepted mismatched namespace")
		}
	}
}

func TestStageOperatorGrantPreviewAndAuditedCAS(t *testing.T) {
	request := admincli.StageAccessGrantRequest{ProjectID: "project:test", GrantID: "run", PrincipalID: "principal", ResourceID: "pipeline:test", ResourceKind: "pipeline", Actions: []string{"pipeline.run"}, ExpectedRevision: 3, OperationID: "stage-1"}
	grant, err := request.Grant()
	if err != nil {
		t.Fatal(err)
	}
	scope := access.AuthorizationPolicyScope{TargetID: "target", ProjectID: request.ProjectID, Environment: "evaluation"}
	repo := &stageGrantRepository{principal: access.Principal{ID: "principal", Kind: access.PrincipalKindUser}, policy: access.AuthorizationPolicy{Scope: scope, Revision: 3}}
	if _, err := stageOperatorGrant(t.Context(), repo, scope, grant, request); err != nil {
		t.Fatal(err)
	}
	if repo.writes != 0 || len(repo.audits) != 0 {
		t.Fatal("preview mutated authority")
	}
	request.ExpectedRevision = 2
	if _, err := stageOperatorGrant(t.Context(), repo, scope, grant, request); !errors.Is(err, access.ErrAuthorizationPolicyStaleRevision) {
		t.Fatalf("preview accepted stale revision: %v", err)
	}
	request.ExpectedRevision, request.Apply = 3, true
	policy, err := stageOperatorGrant(t.Context(), repo, scope, grant, request)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Revision != 4 || len(repo.audits) != 1 || repo.audits[0].ProjectID != scope.ProjectID || repo.audits[0].PrincipalID != "" || repo.audits[0].Action != "grant.staged" {
		t.Fatalf("incorrect staged audit: %+v %+v", policy, repo.audits)
	}
	repo.writeErr = access.ErrAuthorizationPolicyStaleRevision
	if _, err := stageOperatorGrant(t.Context(), repo, scope, grant, request); !errors.Is(err, access.ErrAuthorizationPolicyStaleRevision) {
		t.Fatalf("CAS failure lost: %v", err)
	}
	if len(repo.audits) != 1 {
		t.Fatal("failed stage emitted success audit")
	}
	repo.principal.DisabledAt = "2026-09-26T00:00:00Z"
	if _, err := stageOperatorGrant(t.Context(), repo, scope, grant, request); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("disabled recipient accepted: %v", err)
	}
}
