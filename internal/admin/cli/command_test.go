package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

type fakeOperations struct {
	called                  string
	options                 Options
	upgradeRequest          CatalogUpgradeRequest
	recoveryPrepareRequest  RecoveryPrepareRequest
	recoveryValidateRequest RecoveryValidateRequest
	recoveryPublishRequest  RecoveryPublishRequest
	recoveryPrepareResult   RecoveryPrepareResult
	recoveryValidateResult  RecoveryValidateResult
	recoveryPublishResult   RecoveryPublishResult
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
func (operations *fakeOperations) UpgradePhysicalPoolCatalog(_ context.Context, request CatalogUpgradeRequest, _ io.Writer) error {
	operations.called = "pool-upgrade"
	operations.upgradeRequest = request
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

func (operations *fakeOperations) PrepareRecovery(_ context.Context, request RecoveryPrepareRequest) (RecoveryPrepareResult, error) {
	operations.called = "recovery-prepare"
	operations.recoveryPrepareRequest = request
	return operations.recoveryPrepareResult, nil
}

func (operations *fakeOperations) ValidateRecovery(_ context.Context, request RecoveryValidateRequest) (RecoveryValidateResult, error) {
	operations.called = "recovery-validate"
	operations.recoveryValidateRequest = request
	return operations.recoveryValidateResult, nil
}

func (operations *fakeOperations) PublishRecovery(_ context.Context, request RecoveryPublishRequest) (RecoveryPublishResult, error) {
	operations.called = "recovery-publish"
	operations.recoveryPublishRequest = request
	return operations.recoveryPublishResult, nil
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

func TestCommandRoutesCatalogUpgradeWithTargetArtifacts(t *testing.T) {
	poolPath, evidencePath, identity, evidence := writeCatalogUpgradeArtifacts(t)
	operations := &fakeOperations{}
	command := Command(context.Background(), operations)
	command.SetArgs([]string{
		"delivery", "pool", "upgrade", "--pool", poolPath, "--evidence", evidencePath,
		"--migration-id", "0198f2c0-7c7a-7f00-8a11-000000000001",
		"--catalog-schema-version", "ducklake-schema-v2", "--recovery-decision", "forward_recovery",
		"--drain-verified", "--backup-verified", "--apply",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if operations.called != "pool-upgrade" {
		t.Fatalf("called = %q", operations.called)
	}
	request := operations.upgradeRequest
	if request.Pool != identity || request.Evidence.Digest != evidence.Digest ||
		request.MigrationID != "0198f2c0-7c7a-7f00-8a11-000000000001" ||
		request.CatalogSchemaVersion != "ducklake-schema-v2" ||
		request.RecoveryDecision != CatalogUpgradeRecoveryForwardRecovery ||
		!request.DrainVerified || !request.BackupVerified || !request.Apply {
		t.Fatalf("upgrade request = %#v", request)
	}
}

func TestCommandDispatchesCatalogUpgradeDryRun(t *testing.T) {
	poolPath, evidencePath, _, _ := writeCatalogUpgradeArtifacts(t)
	operations := &fakeOperations{}
	command := Command(context.Background(), operations)
	command.SetArgs([]string{
		"delivery", "pool", "upgrade", "--pool", poolPath, "--evidence", evidencePath,
		"--migration-id", "0198f2c0-7c7a-7f00-8a11-000000000002",
		"--catalog-schema-version", "ducklake-schema-v2", "--recovery-decision", "rollback",
		"--drain-verified", "--backup-verified",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if operations.called != "pool-upgrade" || operations.upgradeRequest.Apply {
		t.Fatalf("dry-run dispatch = called %q request %#v", operations.called, operations.upgradeRequest)
	}
}

func TestCommandValidatesCatalogUpgradeContract(t *testing.T) {
	poolPath, evidencePath, _, _ := writeCatalogUpgradeArtifacts(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing target artifacts", args: []string{"--migration-id", "0198f2c0-7c7a-7f00-8a11-000000000000", "--catalog-schema-version", "v2", "--recovery-decision", "rollback", "--drain-verified", "--backup-verified"}, want: "--pool and --evidence are required"},
		{name: "missing migration id", args: []string{"--pool", poolPath, "--evidence", evidencePath, "--catalog-schema-version", "v2", "--recovery-decision", "rollback", "--drain-verified", "--backup-verified"}, want: "--migration-id is required"},
		{name: "missing schema version", args: []string{"--pool", poolPath, "--evidence", evidencePath, "--migration-id", "0198f2c0-7c7a-7f00-8a11-000000000003", "--recovery-decision", "rollback", "--drain-verified", "--backup-verified"}, want: "--catalog-schema-version is required"},
		{name: "invalid recovery decision", args: []string{"--pool", poolPath, "--evidence", evidencePath, "--migration-id", "0198f2c0-7c7a-7f00-8a11-000000000004", "--catalog-schema-version", "v2", "--recovery-decision", "retry", "--drain-verified", "--backup-verified"}, want: "--recovery-decision must be"},
		{name: "missing verification", args: []string{"--pool", poolPath, "--evidence", evidencePath, "--migration-id", "0198f2c0-7c7a-7f00-8a11-000000000005", "--catalog-schema-version", "v2", "--recovery-decision", "rollback"}, want: "--drain-verified and --backup-verified are required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := Command(context.Background(), &fakeOperations{})
			command.SetArgs(append([]string{"delivery", "pool", "upgrade"}, test.args...))
			err := command.Execute()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func writeCatalogUpgradeArtifacts(t *testing.T) (poolPath, evidencePath string, identity physicalpool.PoolIdentity, evidence physicalpool.Evidence) {
	t.Helper()
	compatibility := physicalpool.Compatibility{
		DuckDBRuntime: "duckdb:1.0", DuckLakeExtension: "ducklake:managed", CatalogFormat: "ducklake-catalog:v1",
		StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1",
	}
	identity = physicalpool.PoolIdentity{
		StorageLocation: "/var/lib/leapview/data", StorageNamespace: "delivery", Region: "local", Tenant: "prod",
		EncryptionDomain: "local", IsolationBoundary: "prod", RetentionAuthority: "prod",
		RetentionPolicy: physicalpool.RetentionPolicy{ReaderGracePeriodSeconds: 1800, OrphanGracePeriodSeconds: 3600, BuildGracePeriodSeconds: 3600},
		Compatibility:   compatibility,
	}
	var err error
	evidence, err = physicalpool.NewEvidence(physicalpool.EvidenceInput{
		Compatibility: compatibility, ConformanceVersion: "test/v1",
		Checks: []physicalpool.EvidenceCheck{{ID: "check", Passed: true, ObservationDigest: "sha256:0000000000000000000000000000000000000000000000000000000000000000"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	poolBytes, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	evidenceBytes, err := physicalpool.MarshalEvidenceArtifact(evidence)
	if err != nil {
		t.Fatal(err)
	}
	poolPath = filepath.Join(t.TempDir(), "pool.json")
	evidencePath = filepath.Join(filepath.Dir(poolPath), "evidence.json")
	if err := os.WriteFile(poolPath, poolBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, evidenceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return poolPath, evidencePath, identity, evidence
}
