package deploymentpostgres

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	physicalpool "github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/deployment"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

func assemblerDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func validNativeSealAssemblerInput(t *testing.T) NativeSealEvidenceAssemblerInput {
	t.Helper()
	const (
		attemptID   = "0198f2c0-7c7a-7f00-8a11-000000001103"
		planID      = "0198f2c0-7c7a-7f00-8a11-000000001101"
		candidateID = "0198f2c0-7c7a-7f00-8a11-000000001102"
		generation  = "0198f2c0-7c7a-7f00-8a11-000000001105"
		sealID      = "0198f2c0-7c7a-7f00-8a11-000000001104"
		leaseID     = "0198f2c0-7c7a-7f00-8a11-000000001107"
		catalogUUID = "0198f2c0-7c7a-7f00-8a11-000000001108"
	)
	const (
		projectID = projectgraph.ResourceID("project-native-assembler")
		poolRoot  = "/tmp/native-assembler-objects"
	)
	requestDigest, sourceDigest := assemblerDigest('f'), assemblerDigest('0')
	artifactDigest := assemblerDigest('e')
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: projectID, Kind: projectgraph.KindProject, Name: "project"},
		{ID: "dashboard-assembler", Kind: projectgraph.KindDashboard, Name: "dashboard"},
	}, []projectgraph.Edge{{From: projectID, To: "dashboard-assembler", Relation: "contains"}})
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := deployment.DeriveRelationNamespace(deployment.RelationNamespaceInput{CandidateID: candidateID, AttemptID: attemptID, FencingEpoch: 3})
	if err != nil {
		t.Fatal(err)
	}
	tuple := physicalpool.Compatibility{DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "ducklake-catalog:v1", StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1"}
	identity := physicalpool.PoolIdentity{StorageLocation: "/tmp", StorageNamespace: "native-assembler-objects", Region: "us-east", Tenant: "tenant-assembler", EncryptionDomain: "encryption-assembler", IsolationBoundary: "isolation-assembler", RetentionAuthority: "retention-assembler", Compatibility: tuple}
	pool, err := physicalpool.NewPhysicalPool(identity)
	if err != nil {
		t.Fatal(err)
	}
	checks := make([]physicalpool.EvidenceCheck, 0, len(ducklake.SharedPoolConformanceChecks))
	for _, check := range ducklake.SharedPoolConformanceChecks {
		checks = append(checks, physicalpool.EvidenceCheck{ID: check, Passed: true, ObservationDigest: assemblerDigest('a')})
	}
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{Compatibility: tuple, ConformanceVersion: ducklake.SharedPoolConformanceVersion, Checks: checks})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := pool.Admit(evidence)
	if err != nil {
		t.Fatal(err)
	}
	pool, err = pool.ApplyAdmission(admission)
	if err != nil {
		t.Fatal(err)
	}
	poolID := pool.ID.String()

	now := time.Date(2099, 1, 2, 12, 0, 0, 0, time.UTC)
	plan, err := deployment.NewDeliveryPlan(deployment.DeliveryPlan{
		ID: planID, TargetID: "target-assembler", ProjectID: projectID, Environment: "prod", Operation: deployment.DeliveryOperationCodeChange, SourceDigest: sourceDigest,
		Execution:  deployment.DeliveryExecutionInputs{SourceArtifactDigest: sourceDigest, CompilerDigest: assemblerDigest('1'), ExecutableDigest: assemblerDigest('2'), DependencyDigest: assemblerDigest('3'), ConfigDigest: assemblerDigest('c'), BindingDigest: assemblerDigest('4'), RuntimeDigest: assemblerDigest('5'), CapabilityDigest: assemblerDigest('6')},
		Provenance: deployment.DeliveryProvenance{Builder: "assembler-test"},
		Governance: deployment.DeliveryGovernance{PolicyDigest: assemblerDigest('7'), AuthorizationDigest: assemblerDigest('d'), QualificationDigest: assemblerDigest('8'), ApprovalPolicyRevision: 1, ExpiresAt: now.Add(time.Hour), ObservedInputsAllowed: false},
		Evidence:   deployment.DeliveryPlanEvidence{ImpactStatement: "impact", PhysicalWorkStatement: "refresh", ReuseStatement: "none", Qualification: deployment.DeliveryQualificationEvidence{Policy: "exact", Steps: []deployment.DeliveryQualificationStep{{ID: "schema", Kind: "contract", Description: "schema", Required: true, Blocking: true}}}, StalePolicy: deployment.DeliveryStalePolicy{Mode: "reject"}, Rollback: deployment.DeliveryRollbackEvidence{Class: deployment.DeliveryServingSafe}},
		CreatedAt:  now,
	})
	if err != nil {
		t.Fatal(err)
	}
	marker := catalogartifact.CommitMarker{SchemaVersion: catalogartifact.CommitMarkerSchemaVersion, DeliveryID: "delivery-assembler", GenerationID: generation, AttemptID: attemptID, LeaseEpoch: 3, RequestDigest: requestDigest, PlanDigest: plan.Digest, Project: projectID.String(), Environment: "prod", PhysicalPoolID: poolID}
	markerJSON, err := marker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	relations := []ducklake.BaseTable{{Schema: namespace, Table: "orders"}}
	relationJSON, _ := json.Marshal(struct {
		RelationNamespace string               `json:"relation_namespace"`
		Relations         []ducklake.BaseTable `json:"relations"`
	}{namespace, relations})
	closureJSON := []byte(`{"objects":[]}`)
	closure := ducklake.NativeSnapshotClosureEvidence{
		CatalogID: "catalog-assembler", SnapshotID: 42, ObjectRoot: poolRoot, RelationNamespace: namespace, Relations: relations, Objects: []ducklake.NativeSnapshotObject{},
		RelationManifestJSON: relationJSON, ClosureJSON: closureJSON, RelationManifestDigest: assemblerDigest('1'), ClosureDigest: assemblerDigest('8'), ObjectRootDigest: assemblerDigest('6'),
	}
	// BuildNativePhysicalEvidence is normally produced by BuildNativePhysical,
	// which supplies these canonical digests. Recompute the tiny fixture using
	// the same constructor path so the assembler verifies real closure evidence.
	closure = nativeAssemblerClosure(t, "catalog-assembler", poolRoot, namespace, relations)
	build := NativePhysicalBuildEvidence{AttemptID: attemptID, CatalogID: "catalog-assembler", ObjectRoot: poolRoot, SnapshotID: 42, Marker: marker, CanonicalMarkerJSON: json.RawMessage(markerJSON), Seal: ducklake.PostgresSnapshotSealEvidence{CatalogType: "postgres", MetadataSchema: ducklake.MetadataSchemaForPool(poolID), DataPath: poolRoot, ExtensionVersion: "1", CatalogVersion: "1.0", SnapshotID: 42, CommitMarker: markerJSON}, Closure: closure}
	artifact := release.CandidateGenerationArtifact{Identity: projectgraph.ServingIdentity{ProjectID: projectID, Environment: "prod", GenerationID: generation}, ServingArtifactID: "artifact-" + strings.TrimPrefix(artifactDigest, "sha256:"), ArtifactDigest: artifactDigest, BundleManifestJSON: `{"version":1}`, NativeArtifact: release.NativeArtifactObjectEvidence{Locator: "serving-artifacts/" + strings.TrimPrefix(artifactDigest, "sha256:") + ".tar.gz", StorageSecurityDomain: "runtime", ContentType: projectbundle.BundleContentType, MetadataDigest: assemblerDigest('9'), SizeBytes: 1}, AccessPolicyJSON: `{}`, DashboardPublicationsJSON: `{}`, DashboardAppearancesJSON: `{}`, DataMode: release.GenerationDataRefreshSources}
	attempt := deploymentnative.DeliveryBuildAttempt{AttemptID: attemptID, PlanID: plan.ID, CandidateID: candidateID, OwnerID: "builder-assembler", PhysicalPoolID: poolID, FencingEpoch: 3, RequestDigest: requestDigest, PlanDigest: plan.Digest, Namespace: namespace, State: deploymentnative.AttemptRunning, LeaseExpiresAt: now.Add(time.Hour)}
	lease := deploymentnative.DeliveryLease{LeaseID: leaseID, TargetID: "target-assembler", OwnerID: attempt.OwnerID, FencingEpoch: 3, State: "active", ExpiresAt: now.Add(time.Hour), AcquiredAt: now}
	attemptAdmission := CandidateBuildAttemptAdmissionResult{Attempt: attempt, Lease: lease, Artifact: deploymentnative.BuildArtifactBinding{AttemptID: attemptID, ServingArtifactID: artifact.ServingArtifactID, ServingArtifactDigest: artifact.ArtifactDigest, ServingStateID: generation, BoundAt: now}, DuckLakeAttempt: ducklakepostgres.AttemptEvidence{AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: plan.Digest, PhysicalPoolID: poolID, CatalogID: build.CatalogID, OwnerID: attempt.OwnerID, FencingEpoch: 3, State: ducklakepostgres.AttemptRunning, SessionIdentity: "session-assembler", LeaseExpiresAt: now.Add(time.Hour)}}
	compatDigest, err := tuple.Digest()
	if err != nil {
		t.Fatal(err)
	}
	gateEvidence, err := (release.GateEvidence{Version: 1, CandidateID: candidateID, SourceDigest: sourceDigest, BindingGeneration: plan.Execution.BindingDigest, RuntimeVersion: "runtime-assembler", DuckDBVersion: tuple.DuckDBRuntime, Bounds: release.GateBounds{MaxRows: 10, MaxQueries: 5, MaxMillis: 1000}, Outcome: release.GateSuccess, EvaluatedAt: now}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	qualification := NativeQualificationEvidence{SchemaVersion: NativeQualificationSchemaVersion, CandidateID: candidateID, AttemptID: attemptID, PhysicalPoolID: poolID, CatalogID: build.CatalogID, SnapshotID: build.SnapshotID, ObjectRoot: build.ObjectRoot, RelationNamespace: namespace, RelationManifestDigest: build.Closure.RelationManifestDigest, ClosureDigest: build.Closure.ClosureDigest, Runtime: NativeRuntimeCompatibilityEvidence{SnapshotID: build.SnapshotID, CatalogType: "postgres", DataPath: build.ObjectRoot, MetadataSchema: ducklake.MetadataSchemaForPool(poolID), DuckDBRuntime: tuple.DuckDBRuntime, DuckLakeExtension: tuple.DuckLakeExtension, CatalogFormat: "1", CompatibilityDigest: compatDigest, CatalogSchemaVersion: "schema-v1"}, Gates: gateEvidence}
	qualificationJSON, _, err := qualification.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(qualificationJSON, &qualification); err != nil {
		t.Fatal(err)
	}
	return NativeSealEvidenceAssemblerInput{Build: build, AttemptAdmission: attemptAdmission, PoolContract: &ducklake.PoolContract{Pool: pool, Tuple: tuple, Admission: admission, Evidence: evidence}, CatalogIdentity: ducklakepostgres.CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: build.CatalogID, CatalogUUID: catalogUUID, MetadataSchema: ducklake.MetadataSchemaForPool(poolID), CompatibilityDigest: compatDigest, CatalogSchemaVersion: "schema-v1"}, Compatibility: ducklakepostgres.RuntimeCompatibility{RuntimeTuple: ducklakepostgres.RuntimeTuple{DuckDBRuntime: tuple.DuckDBRuntime, DuckLakeExtension: tuple.DuckLakeExtension, CatalogFormat: tuple.CatalogFormat}, CompatibilityDigest: compatDigest, CatalogSchemaVersion: "schema-v1"}, Plan: plan, Artifacts: release.CandidateArtifactSet{Artifact: release.ProjectArtifactProvenance{SourceDigest: sourceDigest, ProjectDigest: assemblerDigest('b'), ContentDigest: artifactDigest, CompilerVersion: "compiler", SchemaVersion: 1}, AuthorizationFingerprint: assemblerDigest('d'), Generation: artifact, Compiler: release.CandidateCompilerEvidence{Graph: graph}}, RuntimeVersion: "runtime-assembler", Qualification: qualification, SealID: sealID, GenerationID: generation, TenantDomain: identity.Tenant, EncryptionDomain: "encryption-assembler", ObjectNamespace: "objects/assembler"}
}

func nativeAssemblerClosure(t *testing.T, catalogID, root, namespace string, relations []ducklake.BaseTable) ducklake.NativeSnapshotClosureEvidence {
	t.Helper()
	// Keep this fixture in lock-step with the package's canonical constructor by
	// deriving digests through the same JSON shapes used by Verify.
	relationJSON, err := json.Marshal(struct {
		RelationNamespace string               `json:"relation_namespace"`
		Relations         []ducklake.BaseTable `json:"relations"`
	}{namespace, relations})
	if err != nil {
		t.Fatal(err)
	}
	closureJSON := []byte(`{"objects":[]}`)
	digest := func(value []byte) string { return deployment.CanonicalDeliveryDigest(value) }
	rootDigest := digest([]byte(root))
	canonical, err := json.Marshal(struct {
		SchemaVersion          int                             `json:"schema_version"`
		CatalogID              string                          `json:"catalog_id"`
		SnapshotID             int64                           `json:"snapshot_id"`
		ObjectRoot             string                          `json:"object_root"`
		RelationNamespace      string                          `json:"relation_namespace"`
		Relations              []ducklake.BaseTable            `json:"relations"`
		Objects                []ducklake.NativeSnapshotObject `json:"objects"`
		RelationManifestDigest string                          `json:"relation_manifest_digest"`
		ClosureDigest          string                          `json:"closure_digest"`
		ObjectRootDigest       string                          `json:"object_root_digest"`
	}{2, catalogID, 42, root, namespace, relations, []ducklake.NativeSnapshotObject{}, digest(relationJSON), digest(closureJSON), rootDigest})
	if err != nil {
		t.Fatal(err)
	}
	return ducklake.NativeSnapshotClosureEvidence{CatalogID: catalogID, SnapshotID: 42, ObjectRoot: root, RelationNamespace: namespace, Relations: relations, Objects: []ducklake.NativeSnapshotObject{}, RelationManifestJSON: relationJSON, ClosureJSON: closureJSON, CanonicalJSON: canonical, RelationManifestDigest: digest(relationJSON), ClosureDigest: digest(closureJSON), ObjectRootDigest: rootDigest}
}

func TestAssembleNativeGenerationAdmissionInputAcceptsExactEvidence(t *testing.T) {
	input := validNativeSealAssemblerInput(t)
	input.Artifacts.Generation.ManagedDataPins = []release.ManagedDataPin{{ConnectionID: "connection-orders", RevisionID: assemblerDigest('a')}}
	if input.Build.Seal.CatalogVersion != "1.0" || input.Compatibility.CatalogFormat != "ducklake-catalog:v1" {
		t.Fatalf("fixture does not represent raw DuckLake v1.0 evidence: seal=%q tuple=%q", input.Build.Seal.CatalogVersion, input.Compatibility.CatalogFormat)
	}
	got, err := AssembleNativeGenerationAdmissionInput(input)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if got.Generation.GenerationID != input.GenerationID || got.Seal.CatalogVersion != 1 || got.Seal.DuckDBVersion != input.Compatibility.DuckDBRuntime || got.Seal.DuckLakeExtensionVersion != input.Compatibility.DuckLakeExtension || got.Seal.DuckLakeSpecVersion != "1" {
		t.Fatalf("assembled identity = %#v", got)
	}
	if !got.CandidateExpiresAt.Equal(input.Plan.Governance.ExpiresAt.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("assembled candidate retention expiry = %v, want %v", got.CandidateExpiresAt, input.Plan.Governance.ExpiresAt)
	}
	if got.Bundle.ArtifactLocator != input.Artifacts.Generation.NativeArtifact.Locator || got.Seal.ClosureDigest != input.Build.Closure.ClosureDigest {
		t.Fatalf("assembled artifact/closure = %#v / %#v", got.Bundle, got.Seal)
	}
	if len(got.ManagedDataPins) != 1 || got.ManagedDataPins[0] != input.Artifacts.Generation.ManagedDataPins[0] {
		t.Fatalf("assembled managed-data pins = %#v", got.ManagedDataPins)
	}
	got.ManagedDataPins[0].ConnectionID = "mutated"
	if input.Artifacts.Generation.ManagedDataPins[0].ConnectionID != "connection-orders" {
		t.Fatal("assembler did not clone managed-data pins")
	}
}

func TestAssembleNativeGenerationAdmissionInputRejectsIdentityDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*NativeSealEvidenceAssemblerInput)
	}{
		{"pool", func(in *NativeSealEvidenceAssemblerInput) { in.CatalogIdentity.PhysicalPoolID = "other" }},
		{"catalog", func(in *NativeSealEvidenceAssemblerInput) { in.Build.CatalogID = "other" }},
		{"runtime", func(in *NativeSealEvidenceAssemblerInput) { in.Compatibility.DuckDBRuntime = "duckdb:v2" }},
		{"artifact", func(in *NativeSealEvidenceAssemblerInput) {
			in.Artifacts.Generation.NativeArtifact.Locator = "serving-artifacts/other.tar.gz"
		}},
		{"qualification", func(in *NativeSealEvidenceAssemblerInput) {
			in.Qualification.ClosureDigest = assemblerDigest('4')
		}},
		{"base reuse", func(in *NativeSealEvidenceAssemblerInput) {
			in.Artifacts.Generation.DataMode = release.GenerationDataReuseBase
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validNativeSealAssemblerInput(t)
			test.mutate(&input)
			if _, err := AssembleNativeGenerationAdmissionInput(input); err == nil {
				t.Fatal("assemble unexpectedly accepted drift")
			} else if !errors.Is(err, deploymentnative.ErrConflict) && !errors.Is(err, deploymentnative.ErrInvalid) {
				t.Fatalf("error = %v, want native conflict/invalid", err)
			}
		})
	}
}

func recoveredNativeSealAssemblerInput(t *testing.T) NativeRecoveredSealEvidenceAssemblerInput {
	t.Helper()
	input := validNativeSealAssemblerInput(t)
	input.AttemptAdmission.Attempt.State = deploymentnative.AttemptIndeterminate
	input.AttemptAdmission.DuckLakeAttempt.State = ducklakepostgres.AttemptIndeterminate
	input.AttemptAdmission.Lease.State = "released"
	input.AttemptAdmission.Lease.ReleasedAt = input.AttemptAdmission.Lease.ExpiresAt.Add(time.Minute)
	now := input.AttemptAdmission.Lease.AcquiredAt
	input.AttemptAdmission.Attempt.SessionIdentity = input.AttemptAdmission.DuckLakeAttempt.SessionIdentity
	input.AttemptAdmission.Attempt.CreatedAt = now
	input.AttemptAdmission.Attempt.UpdatedAt = now
	input.AttemptAdmission.Attempt.FinishedAt = now.Add(time.Second)
	input.AttemptAdmission.DuckLakeAttempt.CreatedAt = now
	input.AttemptAdmission.DuckLakeAttempt.UpdatedAt = now
	input.AttemptAdmission.DuckLakeAttempt.TerminalAt = now.Add(time.Second)
	evidence := json.RawMessage(`{"reason":"recovered"}`)
	input.AttemptAdmission.Attempt.TerminationEvidence = append(json.RawMessage(nil), evidence...)
	input.AttemptAdmission.DuckLakeAttempt.TerminationEvidence = append(json.RawMessage(nil), evidence...)
	return NativeRecoveredSealEvidenceAssemblerInput(input)
}

func TestAssembleRecoveredNativeGenerationAdmissionInputAcceptsExactEvidence(t *testing.T) {
	input := recoveredNativeSealAssemblerInput(t)
	got, err := AssembleRecoveredNativeGenerationAdmissionInput(input)
	if err != nil {
		t.Fatalf("assemble recovered: %v", err)
	}
	if got.Generation.GenerationID != input.GenerationID || got.Commit.AttemptID != input.Build.AttemptID || got.Seal.ClosureDigest != input.Build.Closure.ClosureDigest {
		t.Fatalf("assembled recovered identity = %#v", got)
	}
	// The normalized admission value intentionally carries only the fence;
	// the later atomic transaction will reconcile both indeterminate ledgers.
	if got.Fence.LeaseID != input.AttemptAdmission.Lease.LeaseID || got.Fence.FencingEpoch != input.AttemptAdmission.Attempt.FencingEpoch {
		t.Fatalf("assembled recovery fence = %#v", got.Fence)
	}
}

func TestAssembleRecoveredNativeGenerationAdmissionInputRejectsFreshStates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*NativeRecoveredSealEvidenceAssemblerInput)
	}{
		{"active lease", func(in *NativeRecoveredSealEvidenceAssemblerInput) {
			in.AttemptAdmission.Lease.State = "active"
		}},
		{"expired lease", func(in *NativeRecoveredSealEvidenceAssemblerInput) {
			in.AttemptAdmission.Lease.State = "expired"
		}},
		{"other lease state", func(in *NativeRecoveredSealEvidenceAssemblerInput) {
			in.AttemptAdmission.Lease.State = "pending"
		}},
		{"running delivery attempt", func(in *NativeRecoveredSealEvidenceAssemblerInput) {
			in.AttemptAdmission.Attempt.State = deploymentnative.AttemptRunning
		}},
		{"terminal delivery attempt", func(in *NativeRecoveredSealEvidenceAssemblerInput) {
			in.AttemptAdmission.Attempt.State = deploymentnative.AttemptCommitted
		}},
		{"running DuckLake attempt", func(in *NativeRecoveredSealEvidenceAssemblerInput) {
			in.AttemptAdmission.DuckLakeAttempt.State = ducklakepostgres.AttemptRunning
		}},
		{"terminal DuckLake attempt", func(in *NativeRecoveredSealEvidenceAssemblerInput) {
			in.AttemptAdmission.DuckLakeAttempt.State = ducklakepostgres.AttemptCommitted
		}},
		{"missing release timestamp", func(in *NativeRecoveredSealEvidenceAssemblerInput) {
			in.AttemptAdmission.Lease.ReleasedAt = time.Time{}
		}},
		{"artifact binding attempt drift", func(in *NativeRecoveredSealEvidenceAssemblerInput) {
			in.AttemptAdmission.Artifact.AttemptID = "0198f2c0-7c7a-7f00-8a11-000000009999"
		}},
		{"attempt expiry drift", func(in *NativeRecoveredSealEvidenceAssemblerInput) {
			in.AttemptAdmission.Attempt.LeaseExpiresAt = in.AttemptAdmission.Attempt.LeaseExpiresAt.Add(time.Second)
		}},
		{"termination evidence drift", func(in *NativeRecoveredSealEvidenceAssemblerInput) {
			in.AttemptAdmission.DuckLakeAttempt.TerminationEvidence = json.RawMessage(`{"reason":"different"}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := recoveredNativeSealAssemblerInput(t)
			test.mutate(&input)
			if _, err := AssembleRecoveredNativeGenerationAdmissionInput(input); err == nil {
				t.Fatal("recovery assembler unexpectedly accepted invalid precondition")
			} else if !errors.Is(err, deploymentnative.ErrConflict) && !errors.Is(err, deploymentnative.ErrInvalid) {
				t.Fatalf("error = %v, want native conflict/invalid", err)
			}
		})
	}
}

func TestAssembleRecoveredNativeGenerationAdmissionInputAllowsUnboundArtifactReplay(t *testing.T) {
	input := recoveredNativeSealAssemblerInput(t)
	input.AttemptAdmission.Artifact.BoundAt = time.Time{}
	if _, err := AssembleRecoveredNativeGenerationAdmissionInput(input); err != nil {
		t.Fatalf("recovery assembler rejected value-only artifact identity before binding: %v", err)
	}
}

func TestAssembleNativeGenerationAdmissionInputRemainsStrictForRecoveryEvidence(t *testing.T) {
	input := recoveredNativeSealAssemblerInput(t)
	if _, err := AssembleNativeGenerationAdmissionInput(NativeSealEvidenceAssemblerInput(input)); err == nil {
		t.Fatal("fresh assembler accepted indeterminate attempts and released lease")
	}
}

func TestCanonicalNumericCatalogVersion(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int64
	}{
		{"1", 1}, {"v1", 1}, {"1.0", 1}, {"ducklake:v1", 1}, {"ducklake-catalog:1", 1}, {"ducklake:1.0", 1},
	} {
		if got, err := canonicalNumericCatalogVersion(test.value); err != nil || got != test.want {
			t.Fatalf("canonicalNumericCatalogVersion(%q) = %d, %v", test.value, got, err)
		}
	}
	for _, value := range []string{"", "0", "01", "duckdb:v1", "ducklake:v1.1"} {
		if _, err := canonicalNumericCatalogVersion(value); err == nil {
			t.Fatalf("canonicalNumericCatalogVersion(%q) unexpectedly succeeded", value)
		}
	}
}

func TestCanonicalRuntimeComponentPreservesTuplePrefix(t *testing.T) {
	for _, test := range []struct {
		prefix, value, want string
	}{
		{prefix: "duckdb", value: "v1.5.4", want: "duckdb:1.5.4"},
		{prefix: "duckdb", value: "duckdb:v1.5.4", want: "duckdb:1.5.4"},
		{prefix: "ducklake", value: "d318a545", want: "ducklake:d318a545"},
		{prefix: "ducklake", value: "ducklake:d318a545", want: "ducklake:d318a545"},
	} {
		got, err := canonicalRuntimeComponent(test.prefix, test.value)
		if err != nil || got != test.want {
			t.Fatalf("canonicalRuntimeComponent(%q, %q) = %q, %v; want %q", test.prefix, test.value, got, err, test.want)
		}
	}
	for _, test := range []struct{ prefix, value string }{
		{prefix: "duckdb", value: "ducklake:v1"},
		{prefix: "ducklake", value: "duckdb:v1"},
	} {
		if _, err := canonicalRuntimeComponent(test.prefix, test.value); err == nil {
			t.Fatalf("canonicalRuntimeComponent(%q, %q) unexpectedly succeeded", test.prefix, test.value)
		}
	}
}
