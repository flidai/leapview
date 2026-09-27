package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func validAccessTransitionRequest() StageAccessTransitionRequest {
	return StageAccessTransitionRequest{
		Intent: AccessTransitionIntent{
			TargetID: "target_demo", Environment: "production",
			ProjectID:              "project_demo",
			ExpectedPolicyRevision: 32, ExpectedPolicyDigest: "sha256:" + strings.Repeat("b", 64),
			ExpectedServingGeneration: "generation_legacy", ExpectedServingPolicyDigest: "sha256:" + strings.Repeat("c", 64),
			PublisherPrincipalID: "principal_release", ReviewerPrincipalID: "principal_reviewer",
			RoleBindings: []AccessTransitionRoleIntent{{BindingID: "release-operator", Name: "release operator", Principal: "principal_release", Role: string(access.PermissionRoleReleaseOperator)}},
			Grants:       []AccessTransitionGrantIntent{{GrantID: "dashboard-viewer", Principal: "principal_cfo", ResourceID: "dashboard_revenue", ResourceKind: "dashboard", Actions: []string{string(access.ActionDashboardRead)}}},
		},
		MaintenanceOperationID:     "upgrade-42",
		MaintenanceOperationDigest: "sha256:" + strings.Repeat("a", 64),
		OperationID:                "access-transition-42",
	}
}

func TestAccessTransitionPlanBuildsOnlyTypedRoleAndExactGrants(t *testing.T) {
	request := validAccessTransitionRequest()
	plan, err := request.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.RoleBindings) != 1 || !plan.RoleBindings[0].TypedRoleBinding() || plan.RoleBindings[0].PermissionRole != access.PermissionRoleReleaseOperator || len(plan.RoleBindings[0].Capabilities) != 0 {
		t.Fatalf("role plan is not typed catalog authority: %+v", plan.RoleBindings)
	}
	if len(plan.Grants) != 1 || plan.Grants[0].Capability != "" || plan.Grants[0].PermissionProfile != access.PermissionCatalogProfile || len(plan.Grants[0].Permissions) != 1 {
		t.Fatalf("grant plan is not typed authority: %+v", plan.Grants)
	}
	if plan.Grants[0].ID != "dashboard-viewer" {
		t.Fatalf("single-action grant ID changed: %q", plan.Grants[0].ID)
	}
	pair := plan.Grants[0].Permissions[0]
	if pair.Action != access.ActionDashboardRead || pair.Target.IncludeFuture || pair.Target.ResourceID != "dashboard_revenue" || pair.Target.ProjectID != "project_demo" {
		t.Fatalf("grant is not exact dashboard.read: %+v", pair)
	}
	if !canonicalTransitionDigest(plan.IntentDigest) {
		t.Fatalf("intent digest is not canonical: %q", plan.IntentDigest)
	}
	if plan.PublisherPrincipalID != "principal_release" || plan.ReviewerPrincipalID != "principal_reviewer" {
		t.Fatalf("publication principals are not bound: %+v", plan)
	}
}

func TestAccessTransitionPlanSplitsMultiActionGrantsDeterministically(t *testing.T) {
	request := validAccessTransitionRequest()
	request.Intent.Grants = []AccessTransitionGrantIntent{{
		GrantID: "publisher-connection", Name: "connection access",
		Principal: request.Intent.PublisherPrincipalID, ResourceID: "connection_finance", ResourceKind: "connection",
		Actions: []string{string(access.ActionConnectionUse), string(access.ActionConnectionManage)},
	}}
	first, err := request.Intent.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Grants) != 2 {
		t.Fatalf("multi-action input produced %d persisted grants, want 2", len(first.Grants))
	}
	idsByAction := make(map[access.Action]string, len(first.Grants))
	for _, grant := range first.Grants {
		if grant.ID == "publisher-connection" || len(grant.ID) == 0 || len(grant.ID) > 255 {
			t.Errorf("multi-action grant ID is not a bounded derived ID: %q", grant.ID)
		}
		if grant.PermissionProfile != access.PermissionCatalogProfile || len(grant.Permissions) != 1 {
			t.Fatalf("expanded grant must have one typed permission: %+v", grant)
		}
		pair := grant.Permissions[0]
		if pair.Target.ProjectID != "project_demo" || pair.Target.ResourceID != "connection_finance" || pair.Target.ResourceKind != "connection" || pair.Target.IncludeFuture {
			t.Errorf("expanded pair is not exact: %+v", pair)
		}
		if _, duplicate := idsByAction[pair.Action]; duplicate {
			t.Fatalf("action %q was emitted more than once", pair.Action)
		}
		idsByAction[pair.Action] = grant.ID
	}
	if len(idsByAction) != 2 || idsByAction[access.ActionConnectionUse] == "" || idsByAction[access.ActionConnectionManage] == "" {
		t.Fatalf("expanded actions = %v, want exact use and manage", idsByAction)
	}

	request.Intent.Grants[0].Actions = []string{string(access.ActionConnectionManage), string(access.ActionConnectionUse)}
	reordered, err := request.Intent.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if reordered.IntentDigest != first.IntentDigest {
		t.Fatalf("action order changed intent digest: %q != %q", reordered.IntentDigest, first.IntentDigest)
	}
	for _, grant := range reordered.Grants {
		if len(grant.Permissions) != 1 || idsByAction[grant.Permissions[0].Action] != grant.ID {
			t.Fatalf("action reordering changed its emitted grant ID: %+v", grant)
		}
	}
}

func TestAccessTransitionPlanIsStableAndFenced(t *testing.T) {
	request := validAccessTransitionRequest()
	first, err := request.Plan()
	if err != nil {
		t.Fatal(err)
	}
	request.Intent.Grants[0].Actions = []string{string(access.ActionDashboardRead)}
	second, err := request.Intent.Plan()
	if err != nil || second.IntentDigest != first.IntentDigest {
		t.Fatalf("same intent changed digest: %q %q err=%v", first.IntentDigest, second.IntentDigest, err)
	}
	request.MaintenanceOperationID = "different-outer-operation"
	request.MaintenanceOperationDigest = "sha256:" + strings.Repeat("d", 64)
	request.OperationID = "different-command"
	outerIndependent, err := request.Intent.Plan()
	if err != nil || outerIndependent.IntentDigest != first.IntentDigest {
		t.Fatalf("maintenance envelope changed semantic digest: %q %q err=%v", first.IntentDigest, outerIndependent.IntentDigest, err)
	}
	request.Intent.ExpectedPolicyRevision++
	third, err := request.Intent.Plan()
	if err != nil || third.IntentDigest == first.IntentDigest {
		t.Fatalf("expected revision was not bound into intent: %q %q err=%v", first.IntentDigest, third.IntentDigest, err)
	}
}

func TestAccessTransitionPlanDoesNotReorderCallerIntent(t *testing.T) {
	request := validAccessTransitionRequest()
	request.Intent.Grants[0].Actions = []string{string(access.ActionDashboardUpdate), string(access.ActionDashboardRead)}
	before := append([]string(nil), request.Intent.Grants[0].Actions...)
	if _, err := request.Intent.Plan(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(request.Intent.Grants[0].Actions, ",") != strings.Join(before, ",") {
		t.Fatalf("planning mutated caller action order: got %v, want %v", request.Intent.Grants[0].Actions, before)
	}
}

func TestAccessTransitionPlanAllowsOnlyNarrowExactConnectionManageException(t *testing.T) {
	request := validAccessTransitionRequest()
	request.Intent.Grants = []AccessTransitionGrantIntent{{
		GrantID: "publisher-connection", Principal: request.Intent.PublisherPrincipalID,
		ResourceID: "connection_finance", ResourceKind: "connection",
		Actions: []string{string(access.ActionConnectionManage)},
	}}
	plan, err := request.Intent.Plan()
	if err != nil {
		t.Fatalf("explicit exact connection.manage intent rejected: %v", err)
	}
	if len(plan.Grants) != 1 || len(plan.Grants[0].Permissions) != 1 || plan.Grants[0].Permissions[0].Action != access.ActionConnectionManage || plan.Grants[0].Permissions[0].Target.IncludeFuture {
		t.Fatalf("connection grant is not one exact typed pair: %+v", plan.Grants)
	}
	request.Intent.Grants[0].ResourceKind = "dashboard"
	if _, err := request.Intent.Plan(); err == nil {
		t.Fatal("connection.manage accepted for a non-connection resource")
	}
	request.Intent.Grants[0].ResourceKind = "connection"
	request.Intent.Grants[0].Principal = "principal_cfo"
	if _, err := request.Intent.Plan(); err == nil {
		t.Fatal("connection.manage accepted for a non-publisher principal")
	}
}

func TestAccessTransitionPlanRejectsAmbiguousOrBroadIntent(t *testing.T) {
	tests := map[string]func(*StageAccessTransitionRequest){
		"missing existing policy revision": func(r *StageAccessTransitionRequest) { r.Intent.ExpectedPolicyRevision = 0 },
		"missing maintenance digest":       func(r *StageAccessTransitionRequest) { r.MaintenanceOperationDigest = "" },
		"missing expected policy digest":   func(r *StageAccessTransitionRequest) { r.Intent.ExpectedPolicyDigest = "" },
		"missing serving identity":         func(r *StageAccessTransitionRequest) { r.Intent.ExpectedServingGeneration = "" },
		"same publisher and reviewer":      func(r *StageAccessTransitionRequest) { r.Intent.ReviewerPrincipalID = r.Intent.PublisherPrincipalID },
		"legacy role name":                 func(r *StageAccessTransitionRequest) { r.Intent.RoleBindings[0].Role = "legacy_admin" },
		"project-wide grant action": func(r *StageAccessTransitionRequest) {
			r.Intent.Grants[0].Actions = []string{string(access.ActionProjectAccessManage)}
		},
		"nondelegable action": func(r *StageAccessTransitionRequest) {
			r.Intent.Grants[0].Actions = []string{string(access.ActionWorkloadDelegate)}
		},
		"wildcard resource": func(r *StageAccessTransitionRequest) { r.Intent.Grants[0].ResourceID = "*" },
		"duplicate grant action": func(r *StageAccessTransitionRequest) {
			r.Intent.Grants[0].Actions = []string{string(access.ActionDashboardRead), string(access.ActionDashboardRead)}
		},
		"multi-action grant with empty ID": func(r *StageAccessTransitionRequest) {
			r.Intent.Grants[0].GrantID = ""
			r.Intent.Grants[0].Actions = []string{string(access.ActionDashboardRead), string(access.ActionDashboardUpdate)}
		},
		"multi-action grant with oversized ID": func(r *StageAccessTransitionRequest) {
			r.Intent.Grants[0].GrantID = strings.Repeat("g", 256)
			r.Intent.Grants[0].Actions = []string{string(access.ActionDashboardRead), string(access.ActionDashboardUpdate)}
		},
		"duplicate binding identity": func(r *StageAccessTransitionRequest) {
			r.Intent.RoleBindings = append(r.Intent.RoleBindings, r.Intent.RoleBindings[0])
		},
		"no assignments": func(r *StageAccessTransitionRequest) { r.Intent.RoleBindings, r.Intent.Grants = nil, nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := validAccessTransitionRequest()
			mutate(&request)
			if _, err := request.Plan(); err == nil {
				t.Fatal("accepted invalid or ambiguous transition intent")
			}
		})
	}
}

func TestAccessTransitionPlanRejectsEmittedGrantIDCollisions(t *testing.T) {
	request := validAccessTransitionRequest()
	request.Intent.Grants = []AccessTransitionGrantIntent{{
		GrantID: "multi-action", Principal: request.Intent.PublisherPrincipalID,
		ResourceID: "connection_finance", ResourceKind: "connection",
		Actions: []string{string(access.ActionConnectionUse), string(access.ActionConnectionManage)},
	}}
	first, err := request.Intent.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Grants) != 2 {
		t.Fatalf("multi-action input produced %d grants, want 2", len(first.Grants))
	}
	generatedID := first.Grants[0].ID
	request.Intent.Grants = append(request.Intent.Grants, AccessTransitionGrantIntent{
		GrantID: generatedID, Principal: "principal_other", ResourceID: "dashboard_revenue", ResourceKind: "dashboard",
		Actions: []string{string(access.ActionDashboardRead)},
	})
	if _, err := request.Intent.Plan(); err == nil {
		t.Fatal("accepted collision between a derived multi-action ID and an explicit grant ID")
	}
}

func TestAccessTransitionPlanRejectsDuplicateAuthorityAndAssignmentOverflow(t *testing.T) {
	t.Run("duplicate role authority", func(t *testing.T) {
		request := validAccessTransitionRequest()
		duplicate := request.Intent.RoleBindings[0]
		duplicate.BindingID = "second-viewer-binding"
		request.Intent.RoleBindings = append(request.Intent.RoleBindings, duplicate)
		if _, err := request.Intent.Plan(); err == nil {
			t.Fatal("accepted the same principal and role under two binding IDs")
		}
	})

	t.Run("duplicate typed pair authority", func(t *testing.T) {
		request := validAccessTransitionRequest()
		duplicate := request.Intent.Grants[0]
		duplicate.GrantID = "second-dashboard-grant"
		request.Intent.Grants = append(request.Intent.Grants, duplicate)
		if _, err := request.Intent.Plan(); err == nil {
			t.Fatal("accepted the same principal and exact action/resource under two grant IDs")
		}
	})

	t.Run("same pair for different principals", func(t *testing.T) {
		request := validAccessTransitionRequest()
		separate := request.Intent.Grants[0]
		separate.GrantID = "other-viewer"
		separate.Principal = "principal_other"
		request.Intent.Grants = append(request.Intent.Grants, separate)
		plan, err := request.Intent.Plan()
		if err != nil {
			t.Fatalf("different principals should retain separate authority: %v", err)
		}
		if len(plan.Grants) != 2 {
			t.Fatalf("same pair for separate principals produced %d grants, want 2", len(plan.Grants))
		}
	})

	t.Run("expanded action count is bounded", func(t *testing.T) {
		request := validAccessTransitionRequest()
		request.Intent.RoleBindings = nil
		request.Intent.Grants = make([]AccessTransitionGrantIntent, 43)
		for i := range request.Intent.Grants {
			request.Intent.Grants[i] = AccessTransitionGrantIntent{
				GrantID:      fmt.Sprintf("connection-%02d", i),
				Principal:    request.Intent.PublisherPrincipalID,
				ResourceID:   fmt.Sprintf("connection_resource_%02d", i),
				ResourceKind: "connection",
				Actions:      []string{string(access.ActionConnectionRead), string(access.ActionConnectionUse), string(access.ActionConnectionManage)},
			}
		}
		if _, err := request.Intent.Plan(); err == nil {
			t.Fatal("accepted 129 expanded assignments above the transition bound")
		}
	})
}
