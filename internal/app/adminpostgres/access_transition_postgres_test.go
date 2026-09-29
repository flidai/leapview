package adminpostgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

func TestOperatorAccessTransitionPreservesLegacyHistoryAndAuditsTypedBatch(t *testing.T) {
	db := postgrestest.Open(t, accesspostgres.ApplySchema)
	repo, err := accesspostgres.NewAccess(db, accesspostgres.FingerprintConfig{Key: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	legacyUser, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "legacy-transition@example.com", DisplayName: "Legacy"})
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "viewer-transition@example.com", DisplayName: "Viewer"})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "runner-transition@example.com", DisplayName: "Runner"})
	if err != nil {
		t.Fatal(err)
	}
	scope := access.AuthorizationPolicyScope{TargetID: "target:test", ProjectID: "project:test", Environment: "evaluation"}
	legacyBinding := access.RoleBinding{
		ID: "legacy-deployer", Name: "legacy deployer",
		Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: legacyUser.Principal.ID},
		Role:    access.ProjectRoleDeployer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleDeployer),
	}
	initial, err := repo.UpsertAuthorizationRoleBinding(t.Context(), access.AuthorizationRoleBindingInput{Scope: scope, Binding: legacyBinding, IdempotencyKey: "legacy-bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	legacyResource, err := access.NewResourceRef(graph.ResourceID("dashboard:legacy"), graph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	legacyGrant := access.AuthorizationGrant{ID: "legacy-dashboard-read", Subject: legacyBinding.Subject, Resource: legacyResource, Capability: access.CapabilityResourceRead}
	base, err := repo.UpsertAuthorizationGrant(t.Context(), access.AuthorizationGrantInput{Scope: scope, Grant: legacyGrant, ExpectedRevision: initial.Revision, IdempotencyKey: "legacy-grant"})
	if err != nil {
		t.Fatal(err)
	}
	historical, err := repo.AuthorizationPolicyRevision(t.Context(), scope, base.Revision)
	if err != nil {
		t.Fatal(err)
	}
	request := accessTransitionPostgresRequest(scope.ProjectID, base.Revision, base.Digest, viewer.Principal.ID, runner.Principal.ID)
	plan, err := request.Plan()
	if err != nil {
		t.Fatal(err)
	}
	staged, err := stageOperatorAccessTransition(t.Context(), repo, scope, request, plan)
	if err != nil {
		t.Fatal(err)
	}
	if staged.Revision != base.Revision+2 || len(staged.RoleBindings) != 2 || len(staged.Grants) != 2 {
		t.Fatalf("batch did not append both typed assignments while preserving legacy rows: revision=%d roles=%d grants=%d", staged.Revision, len(staged.RoleBindings), len(staged.Grants))
	}
	if staged.RoleBindings[0].TypedRoleBinding() == staged.RoleBindings[1].TypedRoleBinding() {
		t.Fatal("transition did not retain separate legacy and typed role representations")
	}
	if historical.Revision != base.Revision || historical.Digest != base.Digest || historical.RoleBindings[0].TypedRoleBinding() || historical.Grants[0].PermissionProfile != "" {
		t.Fatalf("transition rewrote legacy immutable history: %+v", historical)
	}
	if staged.Grants[1].PermissionProfile != access.PermissionCatalogProfile || len(staged.Grants[1].Permissions) != 1 || staged.Grants[1].Permissions[0].Target.IncludeFuture {
		t.Fatalf("transition grant is not typed exact-resource authority: %+v", staged.Grants[1])
	}
	if events, err := repo.ListAuditEvents(t.Context(), access.AuditEventFilter{ProjectID: scope.ProjectID, Action: "access.transition.staged", Limit: 10}); err != nil || len(events) != 1 {
		t.Fatalf("expected one durable transition audit event, count=%d error=%v", len(events), err)
	} else if events[0].PrincipalID != "" || !strings.Contains(events[0].MetadataJSON, request.MaintenanceOperationDigest) || !strings.Contains(events[0].MetadataJSON, plan.IntentDigest) || !strings.Contains(events[0].MetadataJSON, "pipeline.run") {
		t.Fatalf("audit did not bind operator intent: %+v", events[0])
	}
	replayed, err := stageOperatorAccessTransition(t.Context(), repo, scope, request, plan)
	if err != nil || replayed.Revision != staged.Revision || replayed.Digest != staged.Digest {
		t.Fatalf("identical transition retry changed policy identity: policy=%+v err=%v", replayed, err)
	}
	if _, err := db.Exec(t.Context(), `ALTER TABLE audit.audit_event ADD CONSTRAINT test_reject_access_transition CHECK (action <> 'access.transition.staged') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	nextRequest := accessTransitionPostgresRequest(scope.ProjectID, staged.Revision, staged.Digest, viewer.Principal.ID, runner.Principal.ID)
	nextRequest.OperationID = "access-transition-failed"
	nextRequest.Intent.RoleBindings[0].BindingID = "second-viewer"
	nextRequest.Intent.RoleBindings[0].Principal = legacyUser.Principal.ID
	nextPlan, err := nextRequest.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stageOperatorAccessTransition(t.Context(), repo, scope, nextRequest, nextPlan); err == nil {
		t.Fatal("audit failure accepted a typed access transition")
	}
	unchanged, err := repo.AuthorizationPolicy(t.Context(), scope)
	if err != nil || unchanged.Revision != staged.Revision || unchanged.Digest != staged.Digest {
		t.Fatalf("audit failure committed a partial batch: policy=%+v err=%v", unchanged, err)
	}

	unrelatedBinding, err := access.NewTypedRoleBinding(
		"unrelated-release-operator", "unrelated release operator",
		access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: legacyUser.Principal.ID},
		access.PermissionRoleReleaseOperator, graph.ResourceID(scope.ProjectID),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.UpsertAuthorizationRoleBinding(t.Context(), access.AuthorizationRoleBindingInput{
		Scope: scope, ExpectedRevision: staged.Revision, IdempotencyKey: "unrelated-policy-change",
		Binding: unrelatedBinding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stageOperatorAccessTransition(t.Context(), repo, scope, request, plan); !errors.Is(err, access.ErrAuthorizationPolicyStaleRevision) {
		t.Fatalf("historical transition replay after an unrelated policy mutation = %v, want stale policy revision", err)
	}
	if events, err := repo.ListAuditEvents(t.Context(), access.AuditEventFilter{ProjectID: scope.ProjectID, Action: "access.transition.staged", Limit: 10}); err != nil || len(events) != 2 {
		t.Fatalf("expected initial apply and resumable retry audit events, count=%d error=%v", len(events), err)
	}
}

func TestOperatorAccessTransitionInstallsMultiActionGrantsAsSinglePermissionRows(t *testing.T) {
	db := postgrestest.Open(t, accesspostgres.ApplySchema)
	repo, err := accesspostgres.NewAccess(db, accesspostgres.FingerprintConfig{Key: []byte(strings.Repeat("m", 32))})
	if err != nil {
		t.Fatal(err)
	}
	legacyUser, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "legacy-multi-transition@example.com", DisplayName: "Legacy"})
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "reviewer-multi-transition@example.com", DisplayName: "Reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "publisher-multi-transition@example.com", DisplayName: "Publisher"})
	if err != nil {
		t.Fatal(err)
	}
	scope := access.AuthorizationPolicyScope{TargetID: "target:test", ProjectID: "project:test", Environment: "evaluation"}
	legacyBinding := access.RoleBinding{
		ID: "legacy-multi-deployer", Name: "legacy deployer",
		Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: legacyUser.Principal.ID},
		Role:    access.ProjectRoleDeployer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleDeployer),
	}
	initial, err := repo.UpsertAuthorizationRoleBinding(t.Context(), access.AuthorizationRoleBindingInput{Scope: scope, Binding: legacyBinding, IdempotencyKey: "legacy-multi-bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	legacyResource, err := access.NewResourceRef(graph.ResourceID("dashboard:legacy"), graph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	legacyGrant := access.AuthorizationGrant{ID: "legacy-multi-dashboard-read", Subject: legacyBinding.Subject, Resource: legacyResource, Capability: access.CapabilityResourceRead}
	base, err := repo.UpsertAuthorizationGrant(t.Context(), access.AuthorizationGrantInput{Scope: scope, Grant: legacyGrant, ExpectedRevision: initial.Revision, IdempotencyKey: "legacy-multi-grant"})
	if err != nil {
		t.Fatal(err)
	}
	request := accessTransitionPostgresRequest(scope.ProjectID, base.Revision, base.Digest, reviewer.Principal.ID, publisher.Principal.ID)
	request.Intent.RoleBindings = nil
	request.Intent.Grants = []admincli.AccessTransitionGrantIntent{{
		GrantID: "publisher-finance-connection", Name: "finance connection access",
		Principal: publisher.Principal.ID, ResourceID: "connection:finance", ResourceKind: string(graph.KindConnection),
		Actions: []string{string(access.ActionConnectionUse), string(access.ActionConnectionManage)},
	}}
	plan, err := request.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Grants) != 2 || len(plan.Grants[0].Permissions) != 1 || len(plan.Grants[1].Permissions) != 1 {
		t.Fatalf("normalized transition grants = %+v, want two one-pair grants", plan.Grants)
	}
	staged, err := stageOperatorAccessTransition(t.Context(), repo, scope, request, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(staged.Grants) != 3 {
		t.Fatalf("staged policy has %d grants, want preserved legacy grant and two typed grants", len(staged.Grants))
	}
	project, err := graph.NewProjectGraph([]graph.Resource{
		{ID: "dashboard:legacy", Kind: graph.KindDashboard, Name: "legacy"},
		{ID: "connection:finance", Kind: graph.KindConnection, Name: "finance"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := projectmanifest.AccessPolicyFromAuthorizationPolicy(staged)
	if err != nil {
		t.Fatal(err)
	}
	identity := graph.ServingIdentity{ProjectID: graph.ResourceID(scope.ProjectID), Environment: scope.Environment, GenerationID: "generation_multi_action"}
	snapshot, err := projectmanifest.CompileAuthorizationSnapshot(identity, project, policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.InstallAuthorizationSnapshot(t.Context(), snapshot); err != nil {
		t.Fatalf("install activated authorization snapshot: %v", err)
	}

	var preservedLegacy int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM access.authorization_grant
		WHERE project_id=$1 AND environment=$2 AND generation_id=$3
		AND id=$4 AND permission_profile IS NULL AND permissions IS NULL
		AND resource_id=$5 AND resource_kind='dashboard' AND capability='RESOURCE_READ'`,
		identity.ProjectID.String(), identity.Environment, identity.GenerationID, legacyGrant.ID, legacyResource.ID().String()).Scan(&preservedLegacy); err != nil {
		t.Fatal(err)
	}
	if preservedLegacy != 1 {
		t.Fatalf("preserved legacy grant rows = %d, want 1", preservedLegacy)
	}

	wantIDByAction := make(map[string]string, len(plan.Grants))
	for _, grant := range plan.Grants {
		wantIDByAction[string(grant.Permissions[0].Action)] = grant.ID
	}
	rows, err := db.Query(t.Context(), `SELECT id, permissions -> 0 ->> 'action', jsonb_array_length(permissions),
		resource_id IS NULL, resource_kind IS NULL, capability IS NULL
		FROM access.authorization_grant
		WHERE project_id=$1 AND environment=$2 AND generation_id=$3 AND permission_profile=$4
		ORDER BY id`, identity.ProjectID.String(), identity.Environment, identity.GenerationID, access.PermissionCatalogProfile)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seenActions := make(map[string]bool, len(wantIDByAction))
	for rows.Next() {
		var id, action string
		var pairCount int
		var resourceIDNull, resourceKindNull, capabilityNull bool
		if err := rows.Scan(&id, &action, &pairCount, &resourceIDNull, &resourceKindNull, &capabilityNull); err != nil {
			t.Fatal(err)
		}
		if expectedID := wantIDByAction[action]; expectedID == "" || id != expectedID {
			t.Errorf("installed grant %q for action %q; want exact normalized grant ID %q", id, action, expectedID)
		}
		if seenActions[action] {
			t.Errorf("action %q was installed more than once", action)
		}
		seenActions[action] = true
		if pairCount != 1 || !resourceIDNull || !resourceKindNull || !capabilityNull {
			t.Errorf("typed row %q violates one-pair/no-legacy-columns shape: pairs=%d resource_id_null=%t resource_kind_null=%t capability_null=%t", id, pairCount, resourceIDNull, resourceKindNull, capabilityNull)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seenActions) != 2 || !seenActions[string(access.ActionConnectionUse)] || !seenActions[string(access.ActionConnectionManage)] {
		t.Fatalf("installed typed actions = %v, want exactly connection.use and connection.manage", seenActions)
	}

	publisherSubject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: publisher.Principal.ID}
	connection, err := access.NewResourceRef(graph.ResourceID("connection:finance"), graph.KindConnection)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []access.Action{access.ActionConnectionUse, access.ActionConnectionManage} {
		pair, err := access.NewExactPermissionPair(action, identity.ProjectID, connection)
		if err != nil {
			t.Fatal(err)
		}
		allowed, err := snapshot.AllowsTyped(publisherSubject, pair)
		if err != nil || !allowed {
			t.Fatalf("normalized action %q allowed=%t err=%v", action, allowed, err)
		}
	}
	read, err := access.NewExactPermissionPair(access.ActionConnectionRead, identity.ProjectID, connection)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := snapshot.AllowsTyped(publisherSubject, read)
	if err != nil || allowed {
		t.Fatalf("unrequested connection.read authority allowed=%t err=%v", allowed, err)
	}
}

func accessTransitionPostgresRequest(projectID string, revision int64, policyDigest, viewerID, runnerID string) admincli.StageAccessTransitionRequest {
	return admincli.StageAccessTransitionRequest{
		Intent: admincli.AccessTransitionIntent{
			TargetID: "target:test", Environment: "evaluation",
			ProjectID:              projectID,
			ExpectedPolicyRevision: revision, ExpectedPolicyDigest: policyDigest,
			ExpectedServingGeneration: "generation_legacy", ExpectedServingPolicyDigest: "sha256:" + strings.Repeat("c", 64),
			PublisherPrincipalID: runnerID, ReviewerPrincipalID: viewerID,
			RoleBindings: []admincli.AccessTransitionRoleIntent{{BindingID: "typed-viewer", Name: "viewer", Principal: viewerID, Role: string(access.PermissionRoleViewer)}},
			Grants:       []admincli.AccessTransitionGrantIntent{{GrantID: "pipeline-run", Principal: runnerID, ResourceID: "pipeline:nightly", ResourceKind: "pipeline", Actions: []string{string(access.ActionPipelineRun)}}},
		},
		MaintenanceOperationID:     "upgrade-42",
		MaintenanceOperationDigest: "sha256:" + strings.Repeat("a", 64),
		OperationID:                "access-transition-42",
		Apply:                      true,
	}
}
