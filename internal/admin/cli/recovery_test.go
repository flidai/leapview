package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/recoveryset"
)

func TestCommandRoutesRecoveryPrepare(t *testing.T) {
	set := recoveryCLISet(t)
	setPath := filepath.Join(t.TempDir(), "set.json")
	encoded, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(setPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	rootID := "018f3f83-7b2f-7b37-9f9e-000000000020"
	expiresAt := "2026-10-01T12:00:00Z"
	operations := &fakeOperations{recoveryPrepareResult: RecoveryPrepareResult{Set: set, RootID: rootID}}
	command := Command(context.Background(), operations)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"recovery", "prepare", "--set", setPath, "--retain-root-id", rootID, "--expires-at", expiresAt})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	request := operations.recoveryPrepareRequest
	if operations.called != "recovery-prepare" || request.Set.ID != set.ID || request.RootID != rootID || !request.ExpiresAt.Equal(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("prepare request = %#v", request)
	}
	var rendered map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &rendered); err != nil {
		t.Fatal(err)
	}
	if _, ok := rendered["expiresAt"]; ok {
		t.Fatal("zero expiry should be omitted from prepare output")
	}
}

func TestCommandRoutesRecoveryValidateWithProviderEvidence(t *testing.T) {
	evidence := []byte(`{"schema_version":1,"provider":"external"}`)
	evidencePath := filepath.Join(t.TempDir(), "evidence.json")
	if err := os.WriteFile(evidencePath, evidence, 0o600); err != nil {
		t.Fatal(err)
	}
	const setID = "018f3f83-7b2f-7b37-9f9e-000000000010"
	const attemptID = "018f3f83-7b2f-7b37-9f9e-000000000020"
	operations := &fakeOperations{}
	command := Command(context.Background(), operations)
	command.SetArgs([]string{"recovery", "validate", "--set-id", setID, "--attempt-id", attemptID, "--validator", "operator", "--evidence", evidencePath})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	request := operations.recoveryValidateRequest
	if operations.called != "recovery-validate" || request.SetID != setID || request.AttemptID != attemptID || request.Validator != "operator" || !bytes.Equal(request.Evidence, evidence) {
		t.Fatalf("validate request = %#v", request)
	}
}

func TestCommandRoutesRecoveryPublish(t *testing.T) {
	const setID = "018f3f83-7b2f-7b37-9f9e-000000000010"
	const attemptID = "018f3f83-7b2f-7b37-9f9e-000000000020"
	operations := &fakeOperations{}
	command := Command(context.Background(), operations)
	command.SetArgs([]string{"recovery", "publish", "--set-id", setID, "--publisher", "operator", "--fence-epoch", "42", "--validation-attempt-id", attemptID})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	request := operations.recoveryPublishRequest
	if operations.called != "recovery-publish" || request.SetID != setID || request.Publisher != "operator" || request.FenceEpoch != 42 || request.ValidationAttemptID != attemptID {
		t.Fatalf("publish request = %#v", request)
	}
}

func TestCommandRejectsInvalidRecoveryFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "prepare missing set", args: []string{"recovery", "prepare"}},
		{name: "prepare invalid root id", args: []string{"recovery", "prepare", "--set", "missing.json", "--retain-root-id", "not-a-uuid"}},
		{name: "validate missing evidence", args: []string{"recovery", "validate", "--set-id", "018f3f83-7b2f-7b37-9f9e-000000000010", "--attempt-id", "018f3f83-7b2f-7b37-9f9e-000000000020"}},
		{name: "validate missing validator", args: []string{"recovery", "validate", "--set-id", "018f3f83-7b2f-7b37-9f9e-000000000010", "--attempt-id", "018f3f83-7b2f-7b37-9f9e-000000000020", "--evidence", "missing.json"}},
		{name: "validate noncanonical set id", args: []string{"recovery", "validate", "--set-id", " 018f3f83-7b2f-7b37-9f9e-000000000010", "--attempt-id", "018f3f83-7b2f-7b37-9f9e-000000000020", "--validator", "operator", "--evidence", "missing.json"}},
		{name: "prepare missing expiry", args: []string{"recovery", "prepare", "--set", "missing.json"}},
		{name: "publish zero fence", args: []string{"recovery", "publish", "--set-id", "018f3f83-7b2f-7b37-9f9e-000000000010", "--publisher", "operator", "--fence-epoch", "0", "--validation-attempt-id", "018f3f83-7b2f-7b37-9f9e-000000000020"}},
		{name: "publish negative fence", args: []string{"recovery", "publish", "--set-id", "018f3f83-7b2f-7b37-9f9e-000000000010", "--publisher", "operator", "--fence-epoch", "-1", "--validation-attempt-id", "018f3f83-7b2f-7b37-9f9e-000000000020"}},
		{name: "publish whitespace publisher", args: []string{"recovery", "publish", "--set-id", "018f3f83-7b2f-7b37-9f9e-000000000010", "--publisher", " operator ", "--fence-epoch", "1", "--validation-attempt-id", "018f3f83-7b2f-7b37-9f9e-000000000020"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := Command(context.Background(), &fakeOperations{})
			command.SetArgs(test.args)
			if err := command.Execute(); err == nil {
				t.Fatal("invalid recovery flags accepted")
			}
		})
	}
}

func TestRecoveryCommandsBoundInputFiles(t *testing.T) {
	tooLargeEvidence := filepath.Join(t.TempDir(), "evidence.json")
	if err := os.WriteFile(tooLargeEvidence, bytes.Repeat([]byte("x"), 65537), 0o600); err != nil {
		t.Fatal(err)
	}
	operations := &fakeOperations{}
	command := Command(context.Background(), operations)
	command.SetArgs([]string{"recovery", "validate", "--set-id", "018f3f83-7b2f-7b37-9f9e-000000000010", "--attempt-id", "018f3f83-7b2f-7b37-9f9e-000000000020", "--validator", "operator", "--evidence", tooLargeEvidence})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "65536-byte limit") {
		t.Fatalf("oversized evidence error = %v", err)
	}
	if operations.called != "" {
		t.Fatalf("oversized evidence dispatched operation %q", operations.called)
	}

	tooLargeSet := filepath.Join(t.TempDir(), "set.json")
	if err := os.WriteFile(tooLargeSet, bytes.Repeat([]byte("x"), 1<<20+1), 0o600); err != nil {
		t.Fatal(err)
	}
	command = Command(context.Background(), &fakeOperations{})
	command.SetArgs([]string{"recovery", "prepare", "--set", tooLargeSet, "--expires-at", "2026-10-01T12:00:00Z"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "1048576-byte limit") {
		t.Fatalf("oversized set error = %v", err)
	}
}

func recoveryCLISet(t *testing.T) recoveryset.RecoverySet {
	t.Helper()
	compatibility := physicalpool.Compatibility{
		DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "ducklake:v1",
		StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1",
	}
	compatibilityDigest, err := compatibility.Digest()
	if err != nil {
		t.Fatal(err)
	}
	digest := func(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }
	set := recoveryset.RecoverySet{
		ID: "018f3f83-7b2f-7b37-9f9e-000000000010", SchemaVersion: recoveryset.SchemaVersion,
		ClusterPoints: []recoveryset.ClusterRecoveryPoint{
			{DatabaseRole: recoveryset.DatabaseControl, ClusterIdentity: "cluster", DatabaseIdentity: "control", RecoveryIdentity: "lsn:0/100"},
			{DatabaseRole: recoveryset.DatabaseDuckLake, ClusterIdentity: "cluster", DatabaseIdentity: "ducklake", RecoveryIdentity: "lsn:0/100"},
		},
		Delivery: recoveryset.DeliveryPointer{TargetID: "target/prod", GenerationID: "018f3f83-7b2f-7b37-9f9e-000000000011", PublicationID: "018f3f83-7b2f-7b37-9f9e-000000000012", TargetRevision: 1},
		Serving: recoveryset.SnapshotSeal{
			SealID: "018f3f83-7b2f-7b37-9f9e-000000000013", PhysicalPoolID: "pool", TenantDomain: "tenant", Region: "region", EncryptionDomain: "encryption", ObjectNamespace: "objects/prod",
			CatalogDatabase: "ducklake", CatalogID: "catalog", CatalogUUID: "catalog-uuid", CatalogVersion: 1, DuckLakeSnapshotID: 1,
			RelationManifestDigest: digest('a'), RelationNamespace: "candidate", ClosureDigest: digest('b'), ObjectRoot: "objects/prod", ObjectRootDigest: digest('c'), ArtifactRoot: "artifacts/prod", ArtifactRootDigest: digest('d'),
			ServingArtifactID: "018f3f83-7b2f-7b37-9f9e-000000000014", ServingArtifactDigest: digest('e'), CompiledGraphDigest: digest('f'), CompiledConfigDigest: digest('0'), SecurityDomainFingerprint: digest('1'), RequestDigest: digest('2'), PlanDigest: digest('3'), CompatibilityDigest: compatibilityDigest,
			DuckDBVersion: "1", RuntimeVersion: "runtime", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1",
		},
		Catalog: recoveryset.CatalogCommit{CatalogID: "catalog", CatalogDatabase: "ducklake", CatalogUUID: "catalog-uuid", CatalogVersion: 1, SnapshotID: 1},
		ObjectRoots: []recoveryset.ObjectRoot{
			{Kind: recoveryset.ObjectRootDuckLake, URI: "s3://bucket/catalog", VersionID: "v1", Digest: digest('4'), ProviderRecoveryFrontier: "s3-version:v1"},
			{Kind: recoveryset.ObjectRootServingArtifact, URI: "artifacts/prod", VersionID: "v1", Digest: digest('5')},
		},
		Compatibility: compatibility, FenceEpoch: 1, AuditIdentity: "audit", Status: recoveryset.StatusPrepared, CreatedBy: "operator", CreatedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	}
	if err := set.Validate(); err != nil {
		t.Fatal(err)
	}
	return set
}
