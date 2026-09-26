package deployment

import (
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func compoundAuthorityFixture(t *testing.T, pairs ...access.PermissionPair) (DeliveryAuthorizationPlan, accesssnapshot.AuthorizationSnapshot, access.SubjectRef) {
	t.Helper()
	projectID := projectgraph.ResourceID("project_delivery")
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "model_orders", Kind: projectgraph.KindModel, Name: "orders"},
		{ID: "source_orders", Kind: projectgraph.KindSource, Name: "orders_source"},
		{ID: "source_other", Kind: projectgraph.KindSource, Name: "other_source"},
		{ID: "connection_warehouse", Kind: projectgraph.KindConnection, Name: "warehouse"},
		{ID: "dashboard_sales", Kind: projectgraph.KindDashboard, Name: "sales"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal_delivery"}
	grant, err := accesssnapshot.NewTypedGrant("grant_delivery", "delivery", subject, pairs)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, []accesssnapshot.Grant{grant}, nil)
	if err != nil {
		t.Fatal(err)
	}
	model, _ := access.NewResourceRef("model_orders", projectgraph.KindModel)
	source, _ := access.NewResourceRef("source_orders", projectgraph.KindSource)
	connection, _ := access.NewResourceRef("connection_warehouse", projectgraph.KindConnection)
	plan, err := NewDeliveryAuthorizationPlan(projectID, "target_prod", []DeliveryChangedResource{{Action: access.ActionModelUpdate, Resource: model}}, []DeliveryDependency{{Use: DeliveryDependencyRead, Resource: source}}, []DeliveryConnectionBinding{{BindingID: "binding_warehouse", Connection: connection, EvidenceDigest: testDeliveryAuthorityDigest('b')}}, []DeliveryTransition{{Action: access.ActionDeliveryPlan}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = plan.BindSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return plan, snapshot, subject
}

func TestDeliveryAuthorizationPreservesPairsAndSeparatesChangedDependencies(t *testing.T) {
	projectID := projectgraph.ResourceID("project_delivery")
	model, _ := access.NewResourceRef("model_orders", projectgraph.KindModel)
	source, _ := access.NewResourceRef("source_orders", projectgraph.KindSource)
	connection, _ := access.NewResourceRef("connection_warehouse", projectgraph.KindConnection)
	modelUpdate, _ := access.NewExactPermissionPair(access.ActionModelUpdate, projectID, model)
	sourceRead, _ := access.NewExactPermissionPair(access.ActionSourceRead, projectID, source)
	connectionUse, _ := access.NewExactPermissionPair(access.ActionConnectionUse, projectID, connection)
	deliveryPlan, _ := access.NewProjectPermissionPair(access.ActionDeliveryPlan, projectID)
	plan, snapshot, subject := compoundAuthorityFixture(t, modelUpdate, sourceRead, connectionUse, deliveryPlan)
	if err := plan.Authorize(snapshot, []access.SubjectRef{subject}); err != nil {
		t.Fatalf("compound paired authority was denied: %v", err)
	}

	// A source update grant must not satisfy the source-read dependency. An
	// evaluator that split actions from resources could incorrectly combine
	// this with the model update or another source identity.
	sourceUpdate, _ := access.NewExactPermissionPair(access.ActionSourceUpdate, projectID, source)
	badPlan, badSnapshot, badSubject := compoundAuthorityFixture(t, modelUpdate, sourceUpdate, connectionUse, deliveryPlan)
	if err := badPlan.Authorize(badSnapshot, []access.SubjectRef{badSubject}); !errors.Is(err, ErrDeliveryAuthorityDenied) {
		t.Fatalf("source update authority unexpectedly satisfied dependency: %v", err)
	}
}

func TestDeliveryAuthorizationRejectsUndeclaredDependencyDrift(t *testing.T) {
	projectID := projectgraph.ResourceID("project_delivery")
	model, _ := access.NewResourceRef("model_orders", projectgraph.KindModel)
	source, _ := access.NewResourceRef("source_orders", projectgraph.KindSource)
	connection, _ := access.NewResourceRef("connection_warehouse", projectgraph.KindConnection)
	modelUpdate, _ := access.NewExactPermissionPair(access.ActionModelUpdate, projectID, model)
	sourceRead, _ := access.NewExactPermissionPair(access.ActionSourceRead, projectID, source)
	connectionUse, _ := access.NewExactPermissionPair(access.ActionConnectionUse, projectID, connection)
	deliveryPlan, _ := access.NewProjectPermissionPair(access.ActionDeliveryPlan, projectID)
	plan, snapshot, subject := compoundAuthorityFixture(t, modelUpdate, sourceRead, connectionUse, deliveryPlan)
	evidence, err := EvaluateDeliveryAuthorizationPlan(plan, snapshot, []access.SubjectRef{subject})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := access.NewResourceRef("source_other", projectgraph.KindSource)
	evidence.Dependencies = append(evidence.Dependencies, DeliveryDependency{Use: DeliveryDependencyRead, Resource: other})
	if err := plan.ValidateExecution(evidence); !errors.Is(err, ErrDeliveryUndeclaredDependency) {
		t.Fatalf("undeclared dependency was accepted: %v", err)
	}
}

func TestDeliveryAuthorizationRejectsCoherentSnapshotChange(t *testing.T) {
	projectID := projectgraph.ResourceID("project_delivery")
	model, _ := access.NewResourceRef("model_orders", projectgraph.KindModel)
	source, _ := access.NewResourceRef("source_orders", projectgraph.KindSource)
	connection, _ := access.NewResourceRef("connection_warehouse", projectgraph.KindConnection)
	modelUpdate, _ := access.NewExactPermissionPair(access.ActionModelUpdate, projectID, model)
	sourceRead, _ := access.NewExactPermissionPair(access.ActionSourceRead, projectID, source)
	connectionUse, _ := access.NewExactPermissionPair(access.ActionConnectionUse, projectID, connection)
	deliveryPlan, _ := access.NewProjectPermissionPair(access.ActionDeliveryPlan, projectID)
	plan, snapshot, subject := compoundAuthorityFixture(t, modelUpdate, sourceRead, connectionUse, deliveryPlan)
	identity := snapshot.Identity()
	graph := snapshot.Project()
	changedDashboard, _ := access.NewResourceRef("dashboard_sales", projectgraph.KindDashboard)
	dashboardRead, _ := access.NewExactPermissionPair(access.ActionDashboardRead, projectID, changedDashboard)
	changedGrant, err := accesssnapshot.NewTypedGrant("grant_changed", "new unrelated grant", subject, []access.PermissionPair{dashboardRead})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, append(snapshot.Grants(), changedGrant), nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotDigest, changedDigest := mustSnapshotDigest(t, snapshot), mustSnapshotDigest(t, changed); snapshotDigest == changedDigest {
		t.Fatal("mutating typed snapshot did not change its evidence digest")
	}
	if err := plan.Authorize(changed, []access.SubjectRef{subject}); !errors.Is(err, ErrDeliveryAuthoritySnapshotDrift) {
		t.Fatalf("changed coherent snapshot was accepted: %v", err)
	}
}

func TestDeliveryAuthorizationPlanFromBundleMapsChangedAndDependencyKinds(t *testing.T) {
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "model_orders", Kind: projectgraph.KindModel, Name: "orders"},
		{ID: "source_orders", Kind: projectgraph.KindSource, Name: "orders_source"},
		{ID: "source_other", Kind: projectgraph.KindSource, Name: "other_source"},
	}, []projectgraph.Edge{{From: "model_orders", To: "source_other", Relation: "reads_source"}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := DeliveryAuthorizationPlanFromBundle("project_delivery", "target_prod", graph, projectcompiler.BundlePlan{
		Changes:           []projectcompiler.BundlePlanChange{{Action: "change", ID: "model_orders", Type: "model"}},
		DependencyChanges: []projectcompiler.BundlePlanDependencyChange{{Action: "add", From: "model_orders", To: "source_orders", Type: "reads_source", ResourceKind: "source"}},
	}, []access.Action{access.ActionDeliveryBuild})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ChangedResources) != 1 || plan.ChangedResources[0].Action != access.ActionModelUpdate || len(plan.Dependencies) != 2 || plan.Dependencies[0].Action != access.ActionSourceRead || plan.Dependencies[1].Action != access.ActionSourceRead || len(plan.DeliveryTransitions) != 1 || plan.DeliveryTransitions[0].Action != access.ActionDeliveryBuild {
		t.Fatalf("bundle authority mapping = %#v", plan)
	}
	if plan.Dependencies[0].Resource.ID().String() != "source_orders" || plan.Dependencies[1].Resource.ID().String() != "source_other" {
		t.Fatalf("dependency closure omitted unchanged dependency: %#v", plan.Dependencies)
	}
}

func TestDeliveryAuthorizationPlanBindsRefreshUseToSemanticModel(t *testing.T) {
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "pipeline_sales", Kind: projectgraph.KindPipeline, Name: "sales_refresh"},
		{ID: "semantic_sales", Kind: projectgraph.KindSemanticModel, Name: "sales"},
	}, []projectgraph.Edge{{From: "pipeline_sales", To: "semantic_sales", Relation: "refreshes"}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := DeliveryAuthorizationPlanFromBundle("project_delivery", "target_prod", graph, projectcompiler.BundlePlan{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Dependencies) != 1 {
		t.Fatalf("refresh dependencies = %#v", plan.Dependencies)
	}
	dependency := plan.Dependencies[0]
	if dependency.Use != DeliveryDependencyUse || dependency.Action != access.ActionSemanticConsume || dependency.Resource.ID().String() != "semantic_sales" || dependency.Resource.Kind() != projectgraph.KindSemanticModel {
		t.Fatalf("refresh dependency = %#v, want semantic.consume on semantic_sales", dependency)
	}
}

func mustSnapshotDigest(t *testing.T, snapshot accesssnapshot.AuthorizationSnapshot) string {
	t.Helper()
	digest, err := snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func testDeliveryAuthorityDigest(value byte) string {
	return "sha256:" + strings.Repeat(string(value), 64)
}
