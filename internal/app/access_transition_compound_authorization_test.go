package app

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/deployment"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestAccessTransitionPublisherNeedsExactRetainedGraphDependencies(t *testing.T) {
	const (
		targetID    = "target_historical"
		projectID   = "project:historical-transition"
		environment = "prod"
		generation  = "generation_typed"
		publisherID = "principal_release"
		reviewerID  = "principal_reviewer"
		connection  = "connection:finance_files"
		source      = "source:finance.financials"
		model       = "model:financial_performance"
		semantic    = "semantic-model:finance"
		dashboard   = "dashboard:cfo-command-center"
	)
	identity, err := projectgraph.NewServingIdentity(projectID, environment, generation)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: connection, Kind: projectgraph.KindConnection, Name: "finance_files"},
		{ID: source, Kind: projectgraph.KindSource, Name: "finance.financials"},
		{ID: model, Kind: projectgraph.KindModel, Name: "financial_performance"},
		{ID: semantic, Kind: projectgraph.KindSemanticModel, Name: "finance"},
		{ID: dashboard, Kind: projectgraph.KindDashboard, Name: "cfo-command-center"},
		{ID: "source:unrelated", Kind: projectgraph.KindSource, Name: "unrelated"},
	}, []projectgraph.Edge{
		{From: model, To: source, Relation: "reads_source"},
		{From: source, To: connection, Relation: "uses_connection"},
		{From: semantic, To: model, Relation: "uses_model"},
		{From: dashboard, To: semantic, Relation: "uses_semantic_model"},
	})
	if err != nil {
		t.Fatal(err)
	}

	makeIntent := func(includeSourceRead bool) accessmodule.AccessTransitionIntent {
		grants := []accessmodule.AccessTransitionGrantIntent{
			{GrantID: "publisher-model-read", Principal: publisherID, ResourceID: model, ResourceKind: string(projectgraph.KindModel), Actions: []string{string(access.ActionModelRead)}},
			{GrantID: "publisher-semantic-consume", Principal: publisherID, ResourceID: semantic, ResourceKind: string(projectgraph.KindSemanticModel), Actions: []string{string(access.ActionSemanticConsume)}},
			{GrantID: "publisher-finance-connection", Principal: publisherID, ResourceID: connection, ResourceKind: string(projectgraph.KindConnection), Actions: []string{string(access.ActionConnectionManage), string(access.ActionConnectionUse)}},
		}
		if includeSourceRead {
			grants = append(grants, accessmodule.AccessTransitionGrantIntent{
				GrantID: "publisher-source-read", Principal: publisherID, ResourceID: source,
				ResourceKind: string(projectgraph.KindSource), Actions: []string{string(access.ActionSourceRead)},
			})
		}
		return accessmodule.AccessTransitionIntent{
			TargetID: targetID, Environment: environment, ProjectID: projectID,
			ExpectedPolicyRevision: 7, ExpectedPolicyDigest: "sha256:" + strings.Repeat("a", 64),
			ExpectedServingGeneration: generation, ExpectedServingPolicyDigest: "sha256:" + strings.Repeat("b", 64),
			PublisherPrincipalID: publisherID, ReviewerPrincipalID: reviewerID,
			RoleBindings: []accessmodule.AccessTransitionRoleIntent{
				{BindingID: "transition-publisher", Principal: publisherID, Role: string(access.PermissionRoleReleaseOperator)},
				{BindingID: "transition-reviewer", Principal: reviewerID, Role: string(access.PermissionRoleReleaseApprover)},
			},
			Grants: grants,
		}
	}

	assertPlanAuthorization := func(t *testing.T, intent accessmodule.AccessTransitionIntent, action access.Action, wantErr bool) accesssnapshot.AuthorizationSnapshot {
		t.Helper()
		transition, err := intent.Plan()
		if err != nil {
			t.Fatal(err)
		}
		if err := validateAccessTransitionOperators(transition); err != nil {
			t.Fatal(err)
		}
		snapshotGrants := make([]accesssnapshot.Grant, 0, len(transition.Grants))
		for _, grant := range transition.Grants {
			snapshotGrant, err := accesssnapshot.NewTypedGrant(grant.ID, grant.Name, grant.Subject, grant.Permissions)
			if err != nil {
				t.Fatal(err)
			}
			snapshotGrants = append(snapshotGrants, snapshotGrant)
		}
		snapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, graph, transition.RoleBindings, snapshotGrants, nil)
		if err != nil {
			t.Fatal(err)
		}
		connectionRef, err := access.NewResourceRef(connection, projectgraph.KindConnection)
		if err != nil {
			t.Fatal(err)
		}
		authority, err := deployment.DeliveryAuthorizationPlanFromBundleWithBindings(
			identity.ProjectID, targetID, graph, projectcompiler.BundlePlan{},
			[]deployment.DeliveryConnectionBinding{{BindingID: "finance-files", Connection: connectionRef, EvidenceDigest: "sha256:" + strings.Repeat("c", 64)}},
			[]access.Action{action},
		)
		if err != nil {
			t.Fatal(err)
		}
		authority, err = authority.BindSnapshot(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		err = authority.Authorize(snapshot, []access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: publisherID}})
		if wantErr {
			if err == nil || !strings.Contains(err.Error(), string(access.ActionSourceRead)) {
				t.Fatalf("authorization error = %v, want missing exact source.read", err)
			}
		} else if err != nil {
			t.Fatalf("complete exact transition grants did not authorize %s: %v", action, err)
		}
		return snapshot
	}

	for _, action := range []access.Action{access.ActionDeliveryPlan, access.ActionDeliveryBuild} {
		t.Run(string(action)+" requires source.read", func(t *testing.T) {
			assertPlanAuthorization(t, makeIntent(false), action, true)
		})
		t.Run(string(action)+" accepts exact graph grants only", func(t *testing.T) {
			snapshot := assertPlanAuthorization(t, makeIntent(true), action, false)
			permissions, err := snapshot.EffectiveTypedPermissions([]access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: publisherID}})
			if err != nil {
				t.Fatal(err)
			}
			unrelatedSource, err := access.NewResourceRef("source:unrelated", projectgraph.KindSource)
			if err != nil {
				t.Fatal(err)
			}
			unrelatedRead, err := access.NewExactPermissionPair(access.ActionSourceRead, identity.ProjectID, unrelatedSource)
			if err != nil {
				t.Fatal(err)
			}
			if access.PermissionSetAllows(permissions, unrelatedRead) {
				t.Fatal("publisher received source.read outside the retained graph")
			}
			futureSourceRead, err := access.NewFutureProjectPermissionPair(access.ActionSourceRead, identity.ProjectID, projectgraph.KindSource)
			if err != nil {
				t.Fatal(err)
			}
			if access.PermissionSetAllows(permissions, futureSourceRead) {
				t.Fatal("publisher received future-project source.read authority")
			}
		})
	}
}
