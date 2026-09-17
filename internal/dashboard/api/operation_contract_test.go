package api_test

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestGeneratedPrivateDashboardAuthoringAuthorizationMatrix(t *testing.T) {
	type expectation struct {
		action   string
		resolver string
		scope    string
	}
	expected := map[string]expectation{
		"listDashboardAuthoringCatalog":          {scope: ""}, // project locator; catalog remains authenticated-only.
		"executeDashboardAuthoringCommand":       {scope: ""}, // closed body union chooses update/publish/delete below the generated route.
		"getDashboardAuthoringDashboard":         {action: string(access.ActionDashboardRead), resolver: string(access.TypedOperationResolverDashboard), scope: "dashboard"},
		"getDashboardAuthoringDraft":             {action: string(access.ActionDashboardUpdate), resolver: string(access.TypedOperationResolverDashboard), scope: "dashboard"},
		"getDashboardAuthoringDraftRevision":     {action: string(access.ActionDashboardUpdate), resolver: string(access.TypedOperationResolverDashboard), scope: "dashboard"},
		"getDashboardAuthoringPublishedRevision": {action: string(access.ActionDashboardRead), resolver: string(access.TypedOperationResolverDashboard), scope: "dashboard"},
		"createDashboardAuthoringDraft":          {action: string(access.ActionDashboardCreate), resolver: string(access.TypedOperationResolverProject), scope: "project"},
		"forkDashboardAuthoringDraft":            {action: string(access.ActionDashboardCreate), resolver: string(access.TypedOperationResolverProject), scope: "project"},
		"previewDashboardAuthoringDraft":         {action: string(access.ActionDashboardUpdate), resolver: string(access.TypedOperationResolverDashboard), scope: "dashboard"},
		"exportDashboardAuthoringSource":         {action: string(access.ActionDashboardRead), resolver: string(access.TypedOperationResolverDashboard), scope: "dashboard"},
	}
	contracts := dashboardgen.GetAPIGenOperationContracts()
	if len(expected) != 10 {
		t.Fatalf("authoring matrix has %d rows, want 10", len(expected))
	}
	for operationID, want := range expected {
		contract, ok := contracts[operationID]
		if !ok {
			t.Fatalf("missing generated authoring operation %q", operationID)
		}
		if !contract.Protected || contract.Authz == nil {
			t.Fatalf("%s protection = %t/%#v, want generated protection", operationID, contract.Protected, contract.Authz)
		}
		if want.action == "" {
			if contract.Authz.Mode != "authenticated" || contract.Authz.Action != "" || contract.Authz.Resolver != "" {
				t.Errorf("%s authz = %#v, want authenticated dynamic/aggregate boundary", operationID, contract.Authz)
			}
			continue
		}
		if contract.Authz.Mode != "privilege" || contract.Authz.Action != want.action || contract.Authz.Resolver != want.resolver {
			t.Errorf("%s authz = %#v, want privilege %s/%s", operationID, contract.Authz, want.action, want.resolver)
		}
		if scope, _ := contract.Extensions["x-leapview-object-scope"].(string); scope != want.scope {
			t.Errorf("%s object scope = %q, want %q", operationID, scope, want.scope)
		}
	}

	// The preview route is a dashboard update operation, while its runtime
	// query shape independently expands to semantic.query plus semantic.consume
	// for each exact participating model.
	service := access.NewTypedOperationRequirementService()
	requirement, err := service.Requirement(access.ActionSemanticQuery, string(access.TypedOperationResolverSemanticModel))
	if err != nil {
		t.Fatal(err)
	}
	model, err := access.NewResourceRef(projectgraph.ResourceID("semantic_sales"), projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := requirement.ResolvePairs(projectgraph.ResourceID("project_sales"), model)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 || pairs[0].Action != access.ActionSemanticQuery || pairs[1].Action != access.ActionSemanticConsume {
		t.Fatalf("preview semantic dependency pairs = %#v, want query and consume", pairs)
	}
}

func TestGeneratedDashboardPublicationOperationClassifications(t *testing.T) {
	contracts := dashboardgen.GetAPIGenOperationContracts()
	commands := map[string]string{
		"suspendDashboardPublication": "dashboard_publication.suspended",
		"resumeDashboardPublication":  "dashboard_publication.resumed",
		"rotateDashboardPublication":  "dashboard_publication.rotated",
	}
	for operationID, auditAction := range commands {
		contract, ok := contracts[operationID]
		if !ok || contract.Command == nil {
			t.Fatalf("%s command contract = %#v", operationID, contract.Command)
		}
		command := contract.Command
		if contract.Namespace != "LeapViewAPI.Dashboard" || command.Owner != contract.Namespace || command.AuthzMode != "privilege" || command.Privilege != "RESOURCE_PUBLISH" {
			t.Errorf("%s ownership/authz = %#v", operationID, command)
		}
		if !command.Audit.Required || command.Audit.SuccessAction != auditAction || command.Audit.Guarantee != "transactional" {
			t.Errorf("%s audit = %#v", operationID, command.Audit)
		}
		if command.Target == nil || command.Target.Parameter != "project" || command.Target.Type != "project" {
			t.Errorf("%s target = %#v", operationID, command.Target)
		}
		if command.Idempotency != "required" || command.Concurrency != "if-match" || len(command.AdditionalExposures) != 1 || command.AdditionalExposures[0] != "ui" {
			t.Errorf("%s policies/exposures = %#v", operationID, command)
		}
		if command.Execution != nil {
			t.Errorf("synchronous publication command %s has execution contract %#v", operationID, command.Execution)
		}
	}

	for _, operationID := range []string{"listDashboardPublications", "getDashboardPublication"} {
		if contract := contracts[operationID]; contract.Command != nil {
			t.Errorf("query %s has command contract %#v", operationID, contract.Command)
		}
	}
}
