package module

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

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
	project, err := graph.NewProjectGraph([]graph.Resource{{ID: projectID, Kind: graph.KindProject, Name: "project"}}, nil)
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
	plan := deployment.DeliveryPlan{ProjectID: projectID, Environment: "prod", SourceDigest: nativeReadDigest('c')}
	candidate := nativepostgres.DeliveryCandidate{CandidateID: "0198f2c0-7c7a-7f00-8a11-000000000203", ArtifactDigest: servingDigest}
	gate := release.GateEvidence{SourceDigest: plan.SourceDigest}
	set := release.CandidateArtifactSet{
		AuthorizationFingerprint: securityDigest,
		Artifact:                 release.ProjectArtifactProvenance{SourceDigest: plan.SourceDigest, ContentDigest: servingDigest},
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
