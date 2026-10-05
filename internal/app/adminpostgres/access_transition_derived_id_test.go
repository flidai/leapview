package adminpostgres

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/project/graph"
)

func TestValidateTransitionDerivedGrantIDs(t *testing.T) {
	request := accessTransitionPostgresRequest("project:test", 1, "sha256:"+strings.Repeat("d", 64), "principal_reviewer", "principal_publisher")
	request.Intent.RoleBindings = nil
	request.Intent.Grants = []admincli.AccessTransitionGrantIntent{{
		GrantID: "multi-connection", Principal: request.Intent.PublisherPrincipalID,
		ResourceID: "connection:finance", ResourceKind: string(graph.KindConnection),
		Actions: []string{string(access.ActionConnectionUse), string(access.ActionConnectionManage)},
	}}
	plan, err := request.Plan()
	if err != nil {
		t.Fatal(err)
	}
	var useGrant access.AuthorizationGrant
	for _, grant := range plan.Grants {
		if grant.Permissions[0].Action == access.ActionConnectionUse {
			useGrant = grant
		}
	}
	if useGrant.ID == "" {
		t.Fatal("plan did not emit a connection.use grant")
	}

	t.Run("absent derived ID", func(t *testing.T) {
		if err := validateTransitionDerivedGrantIDs(request, plan, access.AuthorizationPolicy{}); err != nil {
			t.Fatalf("absent derived ID rejected: %v", err)
		}
	})
	t.Run("matching existing authority is replayable", func(t *testing.T) {
		baseline := access.AuthorizationPolicy{Grants: []access.AuthorizationGrant{useGrant}}
		if err := validateTransitionDerivedGrantIDs(request, plan, baseline); err != nil {
			t.Fatalf("exact preexisting derived authority rejected: %v", err)
		}
	})
	t.Run("different existing authority is rejected", func(t *testing.T) {
		conflicting := useGrant
		conflicting.Subject.ID = "principal_someone_else"
		baseline := access.AuthorizationPolicy{Grants: []access.AuthorizationGrant{conflicting}}
		if err := validateTransitionDerivedGrantIDs(request, plan, baseline); err == nil {
			t.Fatal("accepted a derived grant ID already owned by different authority")
		}
	})
	t.Run("explicit single action ID retains upsert semantics", func(t *testing.T) {
		explicit := request
		explicit.Intent.Grants = []admincli.AccessTransitionGrantIntent{{
			GrantID: "operator-selected-id", Principal: request.Intent.PublisherPrincipalID,
			ResourceID: "connection:finance", ResourceKind: string(graph.KindConnection),
			Actions: []string{string(access.ActionConnectionUse)},
		}}
		explicitPlan, err := explicit.Plan()
		if err != nil {
			t.Fatal(err)
		}
		prior := explicitPlan.Grants[0]
		prior.Subject.ID = "principal_someone_else"
		baseline := access.AuthorizationPolicy{Grants: []access.AuthorizationGrant{prior}}
		if err := validateTransitionDerivedGrantIDs(explicit, explicitPlan, baseline); err != nil {
			t.Fatalf("explicit single-action ID behavior changed: %v", err)
		}
	})
}
