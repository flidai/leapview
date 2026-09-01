package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

type fakeOperations struct {
	called  string
	options Options
}

func (operations *fakeOperations) Initialize(context.Context, adminoffline.InitializeRequest, io.Writer) error {
	operations.called = "initialize"
	return nil
}
func (operations *fakeOperations) AcknowledgeInitialCredentials(context.Context) error {
	operations.called = "acknowledge"
	return nil
}
func (operations *fakeOperations) Maintenance(_ context.Context, request MaintenanceRequest, _ io.Writer) error {
	operations.called = "maintenance"
	operations.options = Options{
		Apply: request.Apply, AuditDays: request.AuditDays, QueryDays: request.QueryDays,
		ArchivedAgentDays: request.ArchivedAgentDays, AuthStateDays: request.AuthStateDays,
	}
	return nil
}
func (operations *fakeOperations) BootstrapPhysicalPool(context.Context, adminoffline.PhysicalPoolBootstrapRequest, io.Writer) error {
	operations.called = "pool-bootstrap"
	return nil
}
func (operations *fakeOperations) QualificationPoolArtifacts(context.Context) (adminoffline.QualificationPoolArtifacts, error) {
	operations.called = "qualification-local-pool-artifacts"
	compatibility := physicalpool.Compatibility{DuckDBRuntime: "duckdb:1.0", DuckLakeExtension: "ducklake:managed", CatalogFormat: "ducklake-catalog:v1", StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1"}
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{
		Compatibility: compatibility, ConformanceVersion: "test/v1",
		Checks: []physicalpool.EvidenceCheck{{ID: "check", Passed: true, ObservationDigest: "sha256:0000000000000000000000000000000000000000000000000000000000000000"}},
	})
	if err != nil {
		return adminoffline.QualificationPoolArtifacts{}, err
	}
	return adminoffline.QualificationPoolArtifacts{
		SchemaVersion: adminoffline.QualificationPoolArtifactsSchemaVersion,
		Pool: physicalpool.PoolIdentity{
			StorageLocation: "/var/lib/leapview/data", StorageNamespace: "delivery", Region: "local", Tenant: "qualification", EncryptionDomain: "local",
			IsolationBoundary: "qualification", RetentionAuthority: "qualification",
			RetentionPolicy: physicalpool.RetentionPolicy{ReaderGracePeriodSeconds: 1800, OrphanGracePeriodSeconds: 3600, BuildGracePeriodSeconds: 3600},
			Compatibility:   compatibility,
		},
		Evidence: physicalpool.EvidenceArtifact{SchemaVersion: physicalpool.EvidenceArtifactSchemaVersion, Evidence: evidence},
	}, nil
}

func TestCommandOwnsMaintenanceFlags(t *testing.T) {
	operations := &fakeOperations{}
	command := Command(context.Background(), operations)
	command.SetArgs([]string{"maintenance", "--apply", "--audit-days", "10", "--query-days", "11", "--archived-agent-days", "12", "--auth-state-days", "13"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if operations.called != "maintenance" {
		t.Fatalf("called = %q", operations.called)
	}
	if !operations.options.Apply || operations.options.AuditDays != 10 || operations.options.QueryDays != 11 ||
		operations.options.ArchivedAgentDays != 12 || operations.options.AuthStateDays != 13 {
		t.Fatalf("options = %#v", operations.options)
	}
}

func TestCommandRequiresOperations(t *testing.T) {
	command := Command(context.Background(), nil)
	command.SetArgs([]string{"initialize"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "operations are required") {
		t.Fatalf("error = %v", err)
	}
}

func TestCommandRejectsInvalidNestedSubcommandsWithoutUsage(t *testing.T) {
	command := Command(context.Background(), &fakeOperations{})
	var output strings.Builder
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"delivery", "not-a-command"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(output.String(), "Usage:") {
		t.Fatalf("invalid command emitted usage: %q", output.String())
	}
}

func TestCommandRoutesQualificationLocalPoolArtifactExportWithoutApply(t *testing.T) {
	operations := &fakeOperations{}
	command := Command(context.Background(), operations)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"delivery", "pool", "qualify"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if operations.called != "qualification-local-pool-artifacts" {
		t.Fatalf("called = %q", operations.called)
	}
	if _, err := adminoffline.UnmarshalQualificationPoolArtifacts(output.Bytes()); err != nil {
		t.Fatalf("qualification output = %q: %v", output.String(), err)
	}
}
