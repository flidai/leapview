package api_test

import (
	"strings"
	"testing"
)

func TestProjectDeploymentAPIContract(t *testing.T) {
	spec := managedDataOpenAPISpec(t)
	paths := openAPIMap(t, spec, "paths")
	base := "/api/v1/projects/{project}/deployments"

	list := openAPIOperation(t, paths, base, "get")
	if list["operationId"] != "listDeployments" {
		t.Fatalf("list deployment operation = %#v", list)
	}
	create := openAPIOperation(t, paths, base, "post")
	if create["operationId"] != "createDeployment" || !operationHasParameter(create, "path", "project") || !operationHasParameter(create, "header", "Idempotency-Key") {
		t.Fatalf("create deployment operation = %#v", create)
	}
	if _, ok := openAPIMap(t, create, "responses")["202"]; !ok {
		t.Fatal("create deployment must return 202")
	}
	if privilege := openAPIMap(t, create, "x-authz")["privilege"]; privilege != "RESOURCE_PUBLISH" {
		t.Fatalf("deployment privilege = %#v", privilege)
	}
	for suffix, operationID := range map[string]string{
		"": "getDeployment", "/events": "listDeploymentEvents",
		"/cancel": "cancelDeployment", "/retry": "retryDeployment",
		"/rollback": "rollbackDeployment",
	} {
		method := "get"
		if suffix == "/cancel" || suffix == "/retry" || suffix == "/rollback" {
			method = "post"
		}
		operation := openAPIOperation(t, paths, base+"/{deployment}"+suffix, method)
		if operation["operationId"] != operationID {
			t.Fatalf("%s operation = %#v", operationID, operation)
		}
		if suffix == "" || suffix == "/events" {
			if privilege := openAPIMap(t, operation, "x-authz")["privilege"]; privilege != "RESOURCE_READ" {
				t.Fatalf("%s privilege = %#v, want RESOURCE_READ", operationID, privilege)
			}
		} else if suffix == "/cancel" || suffix == "/retry" {
			if privilege := openAPIMap(t, operation, "x-authz")["privilege"]; privilege != "RESOURCE_PUBLISH" {
				t.Fatalf("%s privilege = %#v, want RESOURCE_PUBLISH", operationID, privilege)
			}
		}
	}
	activate := openAPIOperation(t, paths, base+"/{deployment}/activate", "post")
	if activate["operationId"] != "activateDeployment" ||
		openAPIMap(t, activate, "x-authz")["privilege"] != "PROJECT_ADMIN" {
		t.Fatalf("activate deployment operation = %#v", activate)
	}
	requestApproval := openAPIOperation(t, paths, base+"/{deployment}/approval-requests", "post")
	if requestApproval["operationId"] != "requestDeploymentApproval" ||
		openAPIMap(t, requestApproval, "x-authz")["privilege"] != "RESOURCE_PUBLISH" {
		t.Fatalf("request approval operation = %#v", requestApproval)
	}
	approvalItem := base + "/{deployment}/approval-requests/{approval}"
	for suffix, operationID := range map[string]string{
		"/approve": "approveDeployment",
		"/deny":    "denyDeploymentApproval",
		"/revoke":  "revokeDeploymentApproval",
	} {
		operation := openAPIOperation(t, paths, approvalItem+suffix, "post")
		if operation["operationId"] != operationID ||
			openAPIMap(t, operation, "x-authz")["privilege"] != "PROJECT_ADMIN" {
			t.Fatalf("%s operation = %#v", operationID, operation)
		}
	}

	schemas := openAPIMap(t, openAPIMap(t, spec, "components"), "schemas")
	response := openAPISchema(t, schemas, "DeploymentResponse")
	for _, field := range []string{
		"id", "projectId", "releaseId", "environment", "generationId", "artifactDigest",
		"requestDigest", "evidence", "status", "createdBy", "createdAt",
	} {
		_ = schemaProperty(t, response, field)
	}
	evidence := openAPISchema(t, schemas, "DeploymentPublishEvidence")
	for _, field := range []string{
		"releaseDigest", "artifactContentDigest", "artifactProvenanceDigest", "planDigest", "candidateId",
		"candidateRevision", "targetId", "environment", "generationId", "runtimeVersion",
		"policyDigest",
	} {
		_ = schemaProperty(t, evidence, field)
	}
	assertEnum(t, openAPISchema(t, schemas, "DeploymentStatus"), "queued", "running", "active", "failed", "cancelled", "superseded")
	assertEnum(t, openAPISchema(t, schemas, "DeploymentApprovalStatus"), "pending", "approved", "denied", "revoked", "expired")

	for path := range paths {
		if strings.Contains(path, "/rollouts") || strings.Contains(path, "/deployment-candidates") {
			t.Fatalf("legacy deployment route remains: %s", path)
		}
	}
}

func TestPrivateProjectCandidateAPIContract(t *testing.T) {
	spec := managedDataOpenAPISpec(t)
	paths := openAPIMap(t, spec, "paths")
	base := "/api/v1/projects/{project}/candidates"

	start := openAPIOperation(t, paths, base, "post")
	if start["operationId"] != "startProjectCandidate" ||
		!operationHasParameter(start, "path", "project") ||
		!operationHasParameter(start, "header", "Idempotency-Key") {
		t.Fatalf("start candidate operation = %#v", start)
	}
	if _, ok := openAPIMap(t, start, "responses")["201"]; !ok {
		t.Fatal("start candidate must return 201")
	}
	if privilege := openAPIMap(t, start, "x-authz")["privilege"]; privilege != "RESOURCE_EDIT" {
		t.Fatalf("candidate privilege = %#v", privilege)
	}

	item := base + "/{candidate}"
	for suffix, operationID := range map[string]string{
		"":          "getProjectCandidate",
		"/artifact": "replaceProjectCandidateArtifact",
		"/retry":    "retryProjectCandidate",
		"/cancel":   "cancelProjectCandidate",
	} {
		method := "get"
		if suffix == "/artifact" {
			method = "put"
		} else if suffix != "" {
			method = "post"
		}
		operation := openAPIOperation(t, paths, item+suffix, method)
		if operation["operationId"] != operationID {
			t.Fatalf("%s operation = %#v", operationID, operation)
		}
		if suffix != "" && !operationHasParameter(operation, "header", "Idempotency-Key") {
			t.Fatalf("%s is missing Idempotency-Key", operationID)
		}
		if privilege := openAPIMap(t, operation, "x-authz")["privilege"]; privilege != "RESOURCE_EDIT" {
			t.Fatalf("%s privilege = %#v, want RESOURCE_EDIT", operationID, privilege)
		}
	}

	schemas := openAPIMap(t, openAPIMap(t, spec, "components"), "schemas")
	response := openAPISchema(t, schemas, "CandidateResponse")
	for _, field := range []string{
		"id", "projectId", "targetId", "environment", "ownerId", "baseGeneration",
		"artifactDigest", "status", "previewUrl", "expiresAt", "createdAt", "updatedAt", "revision",
	} {
		_ = schemaProperty(t, response, field)
	}
	assertEnum(t, openAPISchema(t, schemas, "CandidateStatus"), "preparing", "ready", "failed", "cancelled", "expired")
	for _, forbidden := range []string{"credential", "secret", "infisical", "providerReference"} {
		if _, exists := openAPIMap(t, response, "properties")[forbidden]; exists {
			t.Fatalf("candidate response exposes forbidden field %q", forbidden)
		}
	}
}

func TestPrivateProjectCandidateSynchronizationContract(t *testing.T) {
	spec := managedDataOpenAPISpec(t)
	paths := openAPIMap(t, spec, "paths")
	base := "/api/v1/projects/{project}/candidate-sync"
	for suffix, operation := range map[string][2]string{
		"/plan":   {"post", "planProjectCandidateSynchronization"},
		"/commit": {"post", "commitProjectCandidateSynchronization"},
	} {
		contract := openAPIOperation(t, paths, base+suffix, operation[0])
		if contract["operationId"] != operation[1] ||
			!operationHasParameter(contract, "path", "project") {
			t.Fatalf("%s operation = %#v", operation[1], contract)
		}
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
}
