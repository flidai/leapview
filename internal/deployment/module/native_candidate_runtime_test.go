package module

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
)

type nativeCandidateRecoveryFunc func(context.Context, release.CandidateArtifactRecoveryRequest) (release.CandidateArtifactSet, error)

func (f nativeCandidateRecoveryFunc) RecoverCandidateArtifacts(ctx context.Context, request release.CandidateArtifactRecoveryRequest) (release.CandidateArtifactSet, error) {
	return f(ctx, request)
}

type nativeCandidateRuntimePreparerFunc func(context.Context, deployment.CandidateRuntimeRequest) (deployment.CandidateRuntimeReceipt, error)

func (f nativeCandidateRuntimePreparerFunc) Prepare(ctx context.Context, request deployment.CandidateRuntimeRequest) (deployment.CandidateRuntimeReceipt, error) {
	return f(ctx, request)
}

func TestDecodeNativePreviewGateEvidenceBindsCanonicalEnvelope(t *testing.T) {
	const candidateID = "0198f2c0-7c7a-7f00-8a11-000000000203"
	gate, err := (release.GateEvidence{
		Version: 1, CandidateID: candidateID, SourceDigest: nativeReadDigest('a'),
		BindingGeneration: nativeReadDigest('b'), RuntimeVersion: "runtime-v1", DuckDBVersion: "duckdb-v1",
		Bounds: release.GateBounds{MaxRows: 1, MaxQueries: 1, MaxMillis: 1}, Outcome: release.GateSuccess,
		EvaluatedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
	}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	envelope := nativeQualificationEvidenceEnvelope{
		SchemaVersion: 1, CandidateID: candidateID, AttemptID: "0198f2c0-7c7a-7f00-8a11-000000000202",
		PhysicalPoolID: "pool", CatalogID: "catalog", SnapshotID: 42, ObjectRoot: "objects/root",
		RelationNamespace: "candidate/relation", RelationManifestDigest: nativeReadDigest('c'), ClosureDigest: nativeReadDigest('d'),
		Runtime: nativePreviewRuntimeEvidence{SnapshotID: 42, CatalogType: "postgres", DataPath: "objects/root", MetadataSchema: "metadata", DuckDBRuntime: "duckdb-v1", DuckLakeExtension: "ducklake-v1", CatalogFormat: "catalog-v1", CompatibilityDigest: nativeReadDigest('e'), CatalogSchemaVersion: "schema-v1"},
		Gates:   gate,
	}
	raw := marshalNativePreviewEnvelope(t, envelope)
	decoded, err := decodeNativePreviewGateEvidence(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Digest == "" || decoded.Gates.Digest != gate.Digest || decoded.CandidateID != envelope.CandidateID {
		t.Fatalf("decoded envelope = %#v", decoded)
	}

	tamperedDigest := append([]byte(nil), raw...)
	var tampered nativeQualificationEvidenceEnvelope
	if err := json.Unmarshal(tamperedDigest, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.Digest = nativeReadDigest('f')
	if _, err := decodeNativePreviewGateEvidence(mustJSON(t, tampered)); err == nil {
		t.Fatal("tampered envelope digest unexpectedly accepted")
	}
	unknown := append([]byte(`{"unknown":true,`), raw[1:]...)
	if _, err := decodeNativePreviewGateEvidence(unknown); err == nil {
		t.Fatal("unknown envelope field unexpectedly accepted")
	}
}

func TestNativePreviewManagedDataPinsUsesExactPinnedInputs(t *testing.T) {
	plan := deployment.DeliveryPlan{Execution: deployment.DeliveryExecutionInputs{DataInputs: []deployment.DeliveryDataInput{
		{ID: "source-artifact", Mode: deployment.DeliveryDataPinned, Revision: nativeReadDigest('a')},
		{ID: "orders", Mode: deployment.DeliveryDataPinned, Revision: "revision-42"},
		{ID: "events", Mode: deployment.DeliveryDataBounded, Bound: "watermark"},
		{ID: "observed", Mode: deployment.DeliveryDataObserved},
	}}}
	pins, err := nativePreviewManagedDataPins(plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 1 || pins[0] != (release.ManagedDataPin{ConnectionID: "orders", RevisionID: "revision-42"}) {
		t.Fatalf("pins = %#v", pins)
	}
	plan.Execution.DataInputs = append(plan.Execution.DataInputs, deployment.DeliveryDataInput{ID: "orders", Mode: deployment.DeliveryDataPinned, Revision: "revision-43"})
	if _, err := nativePreviewManagedDataPins(plan); err == nil {
		t.Fatal("duplicate managed-data pin unexpectedly accepted")
	}
}

func TestValidateRecoveredNativePreviewArtifactsBindsSealedSecurityAndRoots(t *testing.T) {
	set, identity, candidate, plan, seal, gate := nativeRecoveredArtifactFixture(t)
	if err := validateRecoveredNativePreviewArtifacts(set, identity, candidate, plan, seal, gate); err != nil {
		t.Fatalf("valid recovered artifact rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*release.CandidateArtifactSet, *nativepostgres.SnapshotSeal)
	}{
		{name: "security fingerprint", mutate: func(set *release.CandidateArtifactSet, _ *nativepostgres.SnapshotSeal) {
			set.AuthorizationFingerprint = nativeReadDigest('f')
		}},
		{name: "compiled graph", mutate: func(set *release.CandidateArtifactSet, _ *nativepostgres.SnapshotSeal) {
			set.Compiler.Graph = graph.ProjectGraph{}
		}},
		{name: "artifact root locator", mutate: func(set *release.CandidateArtifactSet, _ *nativepostgres.SnapshotSeal) {
			set.Generation.NativeArtifact.Locator = "artifacts/forged"
		}},
		{name: "artifact root digest", mutate: func(set *release.CandidateArtifactSet, _ *nativepostgres.SnapshotSeal) {
			set.Generation.ArtifactDigest = nativeReadDigest('f')
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tampered := set
			tamperedSeal := seal
			test.mutate(&tampered, &tamperedSeal)
			if err := validateRecoveredNativePreviewArtifacts(tampered, identity, candidate, plan, tamperedSeal, gate); err == nil {
				t.Fatal("tampered recovered artifact unexpectedly accepted")
			}
		})
	}
}

func TestValidateRecoveredNativePreviewArtifactsAcceptsPre017PlanAndSeal(t *testing.T) {
	set, identity, candidate, plan, seal, gate := nativeRecoveredArtifactFixture(t)
	plan.Governance.PolicyRevision = 0
	plan.Governance.PolicyDigest = plan.Governance.AuthorizationDigest
	set.AuthorizationPolicyRevision = 0
	set.AuthorizationPolicyDigest = ""
	seal.AuthorizationPolicyRevision = 0
	seal.AuthorizationPolicyDigest = ""
	if err := validateRecoveredNativePreviewArtifacts(set, identity, candidate, plan, seal, gate); err != nil {
		t.Fatalf("pre-017 qualified candidate preview rejected: %v", err)
	}

	seal.AuthorizationPolicyRevision = 1
	seal.AuthorizationPolicyDigest = nativeReadDigest('4')
	if err := validateRecoveredNativePreviewArtifacts(set, identity, candidate, plan, seal, gate); err == nil {
		t.Fatal("pre-017 preview accepted mixed legacy/current policy evidence")
	}
}

func TestEnsureNativeCandidateRuntimeOpensPre017QualifiedCandidate(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	plan, err := rows.plan.RichPlan()
	if err != nil {
		t.Fatal(err)
	}
	plan.Governance.PolicyDigest = plan.Governance.AuthorizationDigest
	plan.Digest = ""
	plan, err = deployment.NewDeliveryPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	rows.plan.PlanDocument = mustJSON(t, plan)
	rows.plan.PlanDigest = plan.Digest
	rows.attempt.PlanDigest = plan.Digest
	rows.seal.PlanDigest = plan.Digest
	rows.generation.PlanDigest = plan.Digest
	project, err := graph.NewProjectGraph(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := graph.NewServingIdentity(plan.ProjectID, plan.Environment, rows.generation.GenerationID)
	if err != nil {
		t.Fatal(err)
	}
	rows.seal.CompiledConfigDigest = plan.Execution.ConfigDigest
	rows.seal.CompiledGraphDigest = project.Digest()
	rows.seal.SecurityDomainFingerprint = plan.Governance.AuthorizationDigest
	rows.seal.ArtifactRoot = "serving-artifacts/legacy.tar.gz"
	rows.seal.ArtifactRootDigest = rows.seal.ServingArtifactDigest
	rows.seal.DuckLakeSnapshotID = 42
	rows.seal.ObjectRoot = "objects/legacy/42"
	rows.seal.RelationNamespace = "candidate/legacy"
	rows.seal.CatalogID = "legacy-catalog"
	rows.seal.DuckDBVersion = "duckdb-v1"
	rows.seal.RuntimeVersion = "runtime-v1"
	rows.seal.DuckLakeExtensionVersion = "ducklake-v1"
	rows.seal.DuckLakeSpecVersion = "catalog-v1"
	rows.seal.CatalogSchemaVersion = "schema-v1"
	rows.generation.ArtifactRoot = rows.seal.ArtifactRoot
	rows.generation.ArtifactRootDigest = rows.seal.ArtifactRootDigest
	rows.generation.CompiledGraphDigest = rows.seal.CompiledGraphDigest
	rows.generation.CompiledConfigDigest = rows.seal.CompiledConfigDigest
	rows.generation.SecurityDomainFingerprint = rows.seal.SecurityDomainFingerprint

	gate, err := (release.GateEvidence{
		Version: 1, CandidateID: rows.candidate.CandidateID, SourceDigest: plan.SourceDigest,
		BindingGeneration: nativeReadDigest('a'), RuntimeVersion: rows.seal.RuntimeVersion, DuckDBVersion: rows.seal.DuckDBVersion,
		Bounds: release.GateBounds{MaxRows: 1, MaxQueries: 1, MaxMillis: 1}, Outcome: release.GateSuccess,
		EvaluatedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
	}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	envelope := nativeQualificationEvidenceEnvelope{
		SchemaVersion: 1, CandidateID: rows.candidate.CandidateID, AttemptID: rows.attempt.AttemptID,
		PhysicalPoolID: rows.seal.PhysicalPoolID, CatalogID: rows.seal.CatalogID, SnapshotID: rows.seal.DuckLakeSnapshotID,
		ObjectRoot: rows.seal.ObjectRoot, RelationNamespace: rows.seal.RelationNamespace,
		RelationManifestDigest: rows.seal.RelationManifestDigest, ClosureDigest: rows.seal.ClosureDigest,
		Runtime: nativePreviewRuntimeEvidence{
			SnapshotID: rows.seal.DuckLakeSnapshotID, CatalogType: "postgres", DataPath: rows.seal.ObjectRoot, MetadataSchema: "metadata-legacy",
			DuckDBRuntime: rows.seal.DuckDBVersion, DuckLakeExtension: rows.seal.DuckLakeExtensionVersion,
			CatalogFormat: rows.seal.DuckLakeSpecVersion, CompatibilityDigest: rows.seal.CompatibilityDigest,
			CatalogSchemaVersion: rows.seal.CatalogSchemaVersion,
		},
		Gates: gate,
	}
	rows.seal.QualificationEvidence = marshalNativePreviewEnvelope(t, envelope)
	decoded, err := decodeNativePreviewGateEvidence(rows.seal.QualificationEvidence)
	if err != nil {
		t.Fatal(err)
	}
	rows.candidate.QualificationDigest = decoded.Digest

	recovered := release.CandidateArtifactSet{
		AuthorizationFingerprint: plan.Governance.AuthorizationDigest,
		Artifact:                 release.ProjectArtifactProvenance{SourceDigest: plan.SourceDigest, ContentDigest: rows.seal.ServingArtifactDigest},
		Generation: release.CandidateGenerationArtifact{
			Identity: identity, ServingArtifactID: rows.seal.ServingArtifactID, ArtifactDigest: rows.seal.ServingArtifactDigest,
			DataRevision: "sources:legacy", NativeArtifact: release.NativeArtifactObjectEvidence{Locator: rows.seal.ArtifactRoot},
		},
		Compiler: release.CandidateCompilerEvidence{Graph: project},
	}
	recoveryCalled, prepareCalled := false, false
	m := nativeReadModule(rows)
	m.instanceEnvironment = servingstate.Environment("prod")
	m.nativeMetadataSchema = func(string) string { return "metadata-legacy" }
	m.candidateAdmission = CandidatePreparationAdmitterFunc(func(ctx context.Context) (CandidatePreparationLease, error) {
		return nativeDeliveryTestPreparationLease{ctx: ctx}, nil
	})
	m.candidateArtifactRecovery = nativeCandidateRecoveryFunc(func(_ context.Context, request release.CandidateArtifactRecoveryRequest) (release.CandidateArtifactSet, error) {
		recoveryCalled = true
		if request.AuthorizationPolicyRevision != 0 || request.AuthorizationPolicyDigest != "" || request.AuthorizationFingerprint != plan.Governance.AuthorizationDigest {
			t.Fatalf("pre-017 recovery request = %#v", request)
		}
		return recovered, nil
	})
	m.candidateRuntimes = nativeCandidateRuntimePreparerFunc(func(_ context.Context, request deployment.CandidateRuntimeRequest) (deployment.CandidateRuntimeReceipt, error) {
		prepareCalled = true
		if request.AuthorizationFingerprint != plan.Governance.AuthorizationDigest || request.Generation.Identity != identity {
			t.Fatalf("pre-017 runtime request = %#v", request)
		}
		return deployment.CandidateRuntimeReceipt{}, nil
	})
	if err := m.EnsureNativeCandidateRuntime(t.Context(), rows.candidate.CandidateID, rows.attempt.OwnerID); err != nil {
		t.Fatalf("open pre-017 qualified candidate: %v", err)
	}
	if !recoveryCalled || !prepareCalled {
		t.Fatalf("pre-017 preview recovery=%t prepare=%t", recoveryCalled, prepareCalled)
	}

	t.Run("rejects mismatched policy digest before recovery", func(t *testing.T) {
		mismatchedRows := rows
		mismatchedPlan := plan
		mismatchedPlan.Governance.PolicyDigest = nativeReadDigest('4')
		mismatchedPlan.Digest = ""
		mismatchedPlan, err = deployment.NewDeliveryPlan(mismatchedPlan)
		if err != nil {
			t.Fatal(err)
		}
		mismatchedRows.plan.PlanDocument = mustJSON(t, mismatchedPlan)
		mismatchedRows.plan.PlanDigest = mismatchedPlan.Digest
		mismatchedRows.attempt.PlanDigest = mismatchedPlan.Digest
		mismatchedRows.seal.PlanDigest = mismatchedPlan.Digest
		mismatchedRows.generation.PlanDigest = mismatchedPlan.Digest
		m.nativeDeliveryReader = mismatchedRows
		recoveryCalled, prepareCalled = false, false

		err := m.EnsureNativeCandidateRuntime(t.Context(), mismatchedRows.candidate.CandidateID, mismatchedRows.attempt.OwnerID)
		if !errors.Is(err, deployment.ErrCandidateUnavailable) {
			t.Fatalf("mismatched pre-017 preview error = %v, want candidate unavailable", err)
		}
		if recoveryCalled || prepareCalled {
			t.Fatalf("mismatched pre-017 preview recovery=%t prepare=%t, want neither", recoveryCalled, prepareCalled)
		}
	})
}

func TestValidateNativePreviewPlanBindsCompiledConfigDigest(t *testing.T) {
	rows := nativeReadRowsFixture(t, "target")
	plan, err := rows.plan.RichPlan()
	if err != nil {
		t.Fatal(err)
	}
	rows.seal.CompiledConfigDigest = plan.Execution.ConfigDigest
	m := nativeReadModule(rows)
	if err := validateNativePreviewPlan(m, rows.candidate, rows.attempt, rows.seal, plan); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
	rows.seal.CompiledConfigDigest = nativeReadDigest('e')
	if err := validateNativePreviewPlan(m, rows.candidate, rows.attempt, rows.seal, plan); err == nil {
		t.Fatal("mismatched compiled config digest unexpectedly accepted")
	}
}

func nativeRecoveredArtifactFixture(t *testing.T) (release.CandidateArtifactSet, graph.ServingIdentity, nativepostgres.DeliveryCandidate, deployment.DeliveryPlan, nativepostgres.SnapshotSeal, release.GateEvidence) {
	t.Helper()
	projectID, err := graph.NewResourceID("finance")
	if err != nil {
		t.Fatal(err)
	}
	project, err := graph.NewProjectGraph(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := graph.NewServingIdentity(projectID, "prod", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	servingDigest := nativeReadDigest('a')
	securityDigest := nativeReadDigest('b')
	seal := nativepostgres.SnapshotSeal{
		SecurityDomainFingerprint: securityDigest, ServingArtifactID: "artifact-1", ServingArtifactDigest: servingDigest,
		ArtifactRoot: "artifacts/root", ArtifactRootDigest: servingDigest, CompiledGraphDigest: project.Digest(),
	}
	policyDigest := nativeReadDigest('4')
	seal.AuthorizationPolicyRevision = 1
	seal.AuthorizationPolicyDigest = policyDigest
	plan := deployment.DeliveryPlan{
		ProjectID: projectID, Environment: "prod", SourceDigest: nativeReadDigest('c'),
		Governance: deployment.DeliveryGovernance{PolicyRevision: 1, PolicyDigest: policyDigest, AuthorizationDigest: securityDigest},
	}
	candidate := nativepostgres.DeliveryCandidate{CandidateID: "0198f2c0-7c7a-7f00-8a11-000000000203", ArtifactDigest: servingDigest}
	gate := release.GateEvidence{SourceDigest: plan.SourceDigest}
	set := release.CandidateArtifactSet{
		AuthorizationFingerprint:    securityDigest,
		AuthorizationPolicyRevision: 1,
		AuthorizationPolicyDigest:   policyDigest,
		Artifact:                    release.ProjectArtifactProvenance{SourceDigest: plan.SourceDigest, ContentDigest: servingDigest},
		Generation: release.CandidateGenerationArtifact{
			Identity: identity, ServingArtifactID: seal.ServingArtifactID, ArtifactDigest: servingDigest, DataRevision: "sources:revision",
			NativeArtifact: release.NativeArtifactObjectEvidence{Locator: seal.ArtifactRoot},
		},
		Compiler: release.CandidateCompilerEvidence{Graph: project},
	}
	return set, identity, candidate, plan, seal, gate
}

func marshalNativePreviewEnvelope(t *testing.T, envelope nativeQualificationEvidenceEnvelope) []byte {
	t.Helper()
	envelope.Digest = ""
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	envelope.Digest = "sha256:" + hex.EncodeToString(sum[:])
	return mustJSON(t, envelope)
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
