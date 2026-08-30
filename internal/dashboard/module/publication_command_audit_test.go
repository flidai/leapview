package module

import (
	"context"
	"errors"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/dashboard/publication"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestPublicationAuditIntentIsStableAndSecretSafe(t *testing.T) {
	input := publicationCommandAuditInput{
		operationID: dashboardgen.GenOperationSuspendDashboardPublication, projectID: projectgraph.ResourceID("project_1"),
		principalID: "principal-a", targetID: "executive", requestID: "request-a", correlationID: "correlation-a",
		surface: "cli", idempotencyKey: "018f4f2e-0000-7000-8000-000000000001", aggregateKey: "dashboard_publication:project_1:executive",
	}
	first, err := buildPublicationAuditIntent(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildPublicationAuditIntent(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.EventID == "" || first.EventID != second.EventID || first.AggregateKey != second.AggregateKey {
		t.Fatalf("intent identity is not stable: first=%#v second=%#v", first, second)
	}
	if strings.Contains(first.MetadataJSON, "018f4f2e-0000-7000-8000-000000000001") || strings.Contains(first.MetadataJSON, "principal-a") {
		t.Fatalf("audit metadata leaked request identity: %s", first.MetadataJSON)
	}
	if first.MetadataJSON == "" || first.MetadataJSON == "{}" {
		t.Fatalf("generated audit metadata is empty: %s", first.MetadataJSON)
	}
	canonical, err := first.Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	if canonical.MetadataJSON != first.MetadataJSON {
		t.Fatalf("metadata was not canonical: %q != %q", canonical.MetadataJSON, first.MetadataJSON)
	}
}

func TestPublicationAuditIntentRejectsSecretMetadata(t *testing.T) {
	if _, err := canonicalPublicationAuditMetadata(`{"secret":"token"}`); err == nil {
		t.Fatal("secret metadata was accepted")
	}
}

func TestPublicationAuditIntentRequiresIdempotencyKey(t *testing.T) {
	_, err := buildPublicationAuditIntent(publicationCommandAuditInput{
		operationID: dashboardgen.GenOperationSuspendDashboardPublication, projectID: projectgraph.ResourceID("project_1"),
		targetID: "executive", surface: "api",
	})
	if err == nil || !strings.Contains(err.Error(), "idempotency key") {
		t.Fatalf("missing key error = %v", err)
	}
}

func TestPublicationCommandContractRequiresTransactionalAudit(t *testing.T) {
	if err := validatePublicationCommandAuditContracts(); err != nil {
		t.Fatal(err)
	}
}

func TestBeginGeneratedPublicationInvocationRejectsMissingIdempotency(t *testing.T) {
	ctx, err := beginGeneratedPublicationInvocation(context.Background(), publication.ActionSuspend, projectgraph.ResourceID("project_1"), publication.CommandInvocation{
		Surface: string(apigencommand.SurfaceUI),
	})
	if !errors.Is(err, apigencommand.ErrIdempotencyRequired) {
		t.Fatalf("begin error = %v", err)
	}
	if ctx == nil {
		t.Fatal("begin returned nil context")
	}
}

func TestPublicationAuditIntentFieldsUseAccessContract(t *testing.T) {
	intent, err := buildPublicationAuditIntent(publicationCommandAuditInput{
		operationID: dashboardgen.GenOperationRotateDashboardPublication, projectID: projectgraph.ResourceID("project_1"),
		principalID: "principal-a", targetID: "executive", requestID: "request-a", correlationID: "request-a",
		surface: "api", idempotencyKey: "018f4f2e-0000-7000-8000-000000000002",
	})
	if err != nil {
		t.Fatal(err)
	}
	if intent.ResourceKind != "project" || intent.ResourceID != "project_1" || intent.Capability != access.CapabilityResourcePublish || intent.Outcome != "success" {
		t.Fatalf("intent = %#v", intent)
	}
}
