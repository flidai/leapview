package api_test

import (
	"strings"
	"testing"
)

func TestProjectDeliveryAPIContract(t *testing.T) {
	spec := managedDataOpenAPISpec(t)
	paths := openAPIMap(t, spec, "paths")
	base := "/api/v1/projects/{project}/delivery"
	assertOperation := func(path, method, operationID, privilege string) map[string]any {
		t.Helper()
		operation := openAPIOperation(t, paths, path, method)
		if operation["operationId"] != operationID || openAPIMap(t, operation, "x-authz")["privilege"] != privilege {
			t.Fatalf("%s operation = %#v", operationID, operation)
		}
		if !operationHasParameter(operation, "path", "project") {
			t.Fatalf("%s is missing project path parameter", operationID)
		}
		return operation
	}
	create := assertOperation(base, "post", "createDeliveryPlan", "RESOURCE_READ")
	if _, ok := openAPIMap(t, create, "responses")["201"]; !ok {
		t.Fatal("create delivery plan must return 201")
	}
	for _, operationSpec := range []struct {
		path, method, id, privilege string
	}{
		{base + "/plans/{plan}/build", "post", "buildDeliveryPlan", "RESOURCE_USE"},
		{base + "/candidates/{candidate}/publish", "post", "publishDeliveryCandidate", "RESOURCE_PUBLISH"},
		{base + "/publications/{publication}/approval-requests", "post", "requestDeliveryPublicationApproval", "RESOURCE_PUBLISH"},
		{base + "/publications/{publication}/approval-requests/{approval}/approve", "post", "approveDeliveryPublicationApproval", "PROJECT_ADMIN"},
		{base + "/publications/{publication}/approval-requests/{approval}/deny", "post", "denyDeliveryPublicationApproval", "PROJECT_ADMIN"},
		{base + "/publications/{publication}/approval-requests/{approval}/revoke", "post", "revokeDeliveryPublicationApproval", "PROJECT_ADMIN"},
		{base + "/generations/{generation}/rollback", "post", "rollbackDeliveryGeneration", "PROJECT_ADMIN"},
	} {
		operation := assertOperation(operationSpec.path, operationSpec.method, operationSpec.id, operationSpec.privilege)
		if !operationHasParameter(operation, "header", "Idempotency-Key") {
			t.Fatalf("%s is missing Idempotency-Key", operationSpec.id)
		}
	}
	for _, operation := range []struct {
		path, id, privilege string
	}{
		{base + "/plans/{plan}", "getDeliveryPlanPreview", "RESOURCE_READ"},
		{base + "/builds/{build}", "getDeliveryBuildStatus", "RESOURCE_READ"},
		{base + "/seals/{seal}", "getDeliverySealStatus", "RESOURCE_READ"},
		{base + "/candidates/{candidate}", "getDeliveryCandidateStatus", "RESOURCE_READ"},
		{base + "/generations/{generation}", "getDeliveryGenerationStatus", "RESOURCE_READ"},
		{base + "/publications/{publication}", "getDeliveryPublicationEvidence", "RESOURCE_READ"},
		{base + "/publications/{publication}/approval-requests/{approval}", "getDeliveryPublicationApproval", "RESOURCE_READ"},
		{base + "/operator", "getDeliveryOperatorSnapshot", "PROJECT_ADMIN"},
	} {
		assertOperation(operation.path, "get", operation.id, operation.privilege)
	}
	schemas := openAPIMap(t, openAPIMap(t, spec, "components"), "schemas")
	for schema, fields := range map[string][]string{
		"DeliveryPlanPreviewResponse":     {"id", "projectId", "targetId", "environment", "sourceDigest", "planDigest", "status", "evidence"},
		"DeliveryBuildStatusResponse":     {"id", "planId", "sourceDigest", "status", "revision"},
		"DeliveryCandidateStatusResponse": {"id", "planId", "projectId", "targetId", "environment", "status"},
	} {
		response := openAPISchema(t, schemas, schema)
		for _, field := range fields {
			_ = schemaProperty(t, response, field)
		}
	}
	for path := range paths {
		if strings.Contains(path, "/deployments") || strings.Contains(path, "/rollouts") || strings.Contains(path, "/deployment-candidates") {
			t.Fatalf("legacy deployment route remains: %s", path)
		}
	}
}

func TestPrivateProjectCandidateSynchronizationContract(t *testing.T) {
	spec := managedDataOpenAPISpec(t)
	paths := openAPIMap(t, spec, "paths")
	base := "/api/v1/projects/{project}/candidate-sync"
	for suffix, operation := range map[string][2]string{
		"/plan":   {"post", "planProjectCandidateSynchronization"},
		"/source": {"post", "retainProjectCandidateSource"},
	} {
		contract := openAPIOperation(t, paths, base+suffix, operation[0])
		if contract["operationId"] != operation[1] ||
			!operationHasParameter(contract, "path", "project") {
			t.Fatalf("%s operation = %#v", operation[1], contract)
		}
		if !operationHasParameter(contract, "header", "Idempotency-Key") {
			t.Fatalf("%s is missing Idempotency-Key", operation[1])
		}
	}
	source := openAPIOperation(t, paths, base+"/source", "post")
	if !operationHasParameter(source, "header", "Source-Synchronization-Plan") {
		t.Fatal("source retention is missing Source-Synchronization-Plan")
	}
	upload := openAPIOperation(t, paths, base+"/blobs/{digest}", "put")
	if upload["operationId"] != "uploadProjectCandidateSourceBlob" ||
		!operationHasParameter(upload, "path", "digest") {
		t.Fatalf("candidate source upload operation = %#v", upload)
	}
	if manual, _ := upload["x-apigen-manual"].(bool); !manual {
		t.Fatal("candidate source blob upload must retain streaming body control")
	}

	schemas := openAPIMap(t, openAPIMap(t, spec, "components"), "schemas")
	request := openAPISchema(t, schemas, "CandidateSynchronizationRequest")
	for _, field := range []string{"projectFile", "artifactDigest", "artifacts"} {
		_ = schemaProperty(t, request, field)
	}
	reference := openAPISchema(t, schemas, "CandidateSourceArtifact")
	for _, field := range []string{"path", "digest"} {
		_ = schemaProperty(t, reference, field)
	}
	plan := openAPISchema(t, schemas, "CandidateSynchronizationPlanResponse")
	_ = schemaProperty(t, plan, "missingDigests")
	for path := range paths {
		if strings.Contains(path, "/projects/{project}/candidates") {
			t.Fatalf("legacy private candidate route remains: %s", path)
		}
	}
}
