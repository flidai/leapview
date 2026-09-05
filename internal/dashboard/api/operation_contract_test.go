package api_test

import (
	"testing"

	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
)

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
