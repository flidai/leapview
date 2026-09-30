package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSealedCandidateEvidenceAllowsPrecommitLocalPin(t *testing.T) {
	fixture := newSealedCandidateEvidenceFixture(t)
	evidence, err := fixture.source.SealedCandidateResultIdentityEvidence(
		t.Context(), fixture.provenance.Plan.Identity, fixture.candidate.CandidateID,
		fixture.seal.SealID, fixture.candidate.ArtifactDigest,
	)
	require.NoError(t, err)
	require.Equal(t, fixture.provenance.Plan.RuntimeVersion, evidence.RuntimeVersion)
	require.Equal(t, release.BindingFingerprint(fixture.provenance.Plan.Bindings), evidence.BindingFingerprint)
	require.Equal(t, map[string]string{"warehouse": "postgres"}, evidence.BindingKinds)
	require.Equal(t, fixture.candidate.CandidateID, fixture.delivery.resolveCandidateID)
	require.Equal(t, fixture.candidate.CandidateID, fixture.delivery.candidateID)
	require.Equal(t, fixture.seal.SealID, fixture.delivery.sealID)
	require.Equal(t, 1, fixture.delivery.resolveCalls)
	require.Equal(t, 1, fixture.delivery.candidateCalls)
	require.Equal(t, 1, fixture.delivery.sealCalls)
	require.Equal(t, fixture.provenance.Plan.Identity.ProjectID, fixture.provenanceReader.projectID)
	require.Equal(t, fixture.candidate.CandidateID, fixture.provenanceReader.candidateID)
	require.Equal(t, fixture.candidate.CandidateRevision, fixture.provenanceReader.revision)
	require.Equal(t, 1, fixture.provenanceReader.calls)
	// This metadata-only precommit proof has no committed-publication reader or
	// credential reader; it never requires the target pointer to move first.
}

func TestSealedCandidateEvidenceAcceptsAdmittedCandidate(t *testing.T) {
	fixture := newSealedCandidateEvidenceFixture(t)
	fixture.delivery.candidate.Status = "admitted"
	fixture.delivery.resolution.Status = "admitted"
	_, err := fixture.source.SealedCandidateResultIdentityEvidence(
		t.Context(), fixture.provenance.Plan.Identity, fixture.candidate.CandidateID,
		fixture.seal.SealID, fixture.candidate.ArtifactDigest,
	)
	require.NoError(t, err)
}

func TestSealedCandidateEvidenceRejectsMalformedRequestBeforeLookup(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*sealedCandidateEvidenceFixture) (projectgraph.ServingIdentity, string, string, string)
	}{
		{"invalid identity", func(f *sealedCandidateEvidenceFixture) (projectgraph.ServingIdentity, string, string, string) {
			return projectgraph.ServingIdentity{}, f.candidate.CandidateID, f.seal.SealID, f.candidate.ArtifactDigest
		}},
		{"wrong target environment", func(f *sealedCandidateEvidenceFixture) (projectgraph.ServingIdentity, string, string, string) {
			identity := f.provenance.Plan.Identity
			identity.Environment = "staging"
			return identity, f.candidate.CandidateID, f.seal.SealID, f.candidate.ArtifactDigest
		}},
		{"noncanonical candidate", func(f *sealedCandidateEvidenceFixture) (projectgraph.ServingIdentity, string, string, string) {
			return f.provenance.Plan.Identity, " " + f.candidate.CandidateID, f.seal.SealID, f.candidate.ArtifactDigest
		}},
		{"noncanonical seal", func(f *sealedCandidateEvidenceFixture) (projectgraph.ServingIdentity, string, string, string) {
			return f.provenance.Plan.Identity, f.candidate.CandidateID, " " + f.seal.SealID, f.candidate.ArtifactDigest
		}},
		{"invalid artifact digest", func(f *sealedCandidateEvidenceFixture) (projectgraph.ServingIdentity, string, string, string) {
			return f.provenance.Plan.Identity, f.candidate.CandidateID, f.seal.SealID, "sha256:bad"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSealedCandidateEvidenceFixture(t)
			identity, candidateID, sealID, digest := test.mutate(fixture)
			_, err := fixture.source.SealedCandidateResultIdentityEvidence(t.Context(), identity, candidateID, sealID, digest)
			require.ErrorIs(t, err, release.ErrProvenanceInvalid)
			require.Zero(t, fixture.delivery.resolveCalls)
			require.Zero(t, fixture.delivery.candidateCalls)
			require.Zero(t, fixture.delivery.sealCalls)
			require.Zero(t, fixture.provenanceReader.calls)
		})
	}
}

func TestSealedCandidateEvidenceStopsOnMissingMetadata(t *testing.T) {
	for _, test := range []struct {
		name                string
		resolutionErr       bool
		candidateErr        bool
		sealErr             bool
		provenanceErr       bool
		wantCandidateCalls  int
		wantSealCalls       int
		wantProvenanceCalls int
	}{
		{name: "missing generation", resolutionErr: true},
		{name: "missing candidate", candidateErr: true, wantCandidateCalls: 1},
		{name: "missing seal", sealErr: true, wantCandidateCalls: 1, wantSealCalls: 1},
		{name: "missing exact provenance", provenanceErr: true, wantCandidateCalls: 1, wantSealCalls: 1, wantProvenanceCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSealedCandidateEvidenceFixture(t)
			if test.resolutionErr {
				fixture.delivery.resolveErr = errors.New("private metadata diagnostic")
			}
			if test.candidateErr {
				fixture.delivery.candidateErr = errors.New("private metadata diagnostic")
			}
			if test.sealErr {
				fixture.delivery.sealErr = errors.New("private metadata diagnostic")
			}
			if test.provenanceErr {
				fixture.provenanceReader.err = errors.New("private metadata diagnostic")
			}
			_, err := fixture.source.SealedCandidateResultIdentityEvidence(
				t.Context(), fixture.provenance.Plan.Identity, fixture.candidate.CandidateID,
				fixture.seal.SealID, fixture.candidate.ArtifactDigest,
			)
			require.ErrorIs(t, err, release.ErrProvenanceInvalid)
			require.NotContains(t, err.Error(), "private metadata diagnostic")
			require.Equal(t, 1, fixture.delivery.resolveCalls)
			require.Equal(t, test.wantCandidateCalls, fixture.delivery.candidateCalls)
			require.Equal(t, test.wantSealCalls, fixture.delivery.sealCalls)
			require.Equal(t, test.wantProvenanceCalls, fixture.provenanceReader.calls)
		})
	}
}

func TestSealedCandidateEvidenceRejectsSubstitutedTuple(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*sealedCandidateEvidenceFixture)
	}{
		{"candidate target", func(f *sealedCandidateEvidenceFixture) { f.delivery.resolution.TargetID = "other-target" }},
		{"project", func(f *sealedCandidateEvidenceFixture) { f.delivery.resolution.ProjectID = "other-project" }},
		{"environment", func(f *sealedCandidateEvidenceFixture) { f.delivery.resolution.Environment = "staging" }},
		{"generation", func(f *sealedCandidateEvidenceFixture) { f.delivery.resolution.GenerationID = uuid.NewString() }},
		{"generation cardinality", func(f *sealedCandidateEvidenceFixture) { f.delivery.resolution.GenerationCount = 2 }},
		{"resolution seal", func(f *sealedCandidateEvidenceFixture) { f.delivery.resolution.SnapshotSealID = uuid.NewString() }},
		{"resolution artifact", func(f *sealedCandidateEvidenceFixture) {
			f.delivery.resolution.ArtifactDigest = activeResultIdentityDigest('9')
		}},
		{"resolution status", func(f *sealedCandidateEvidenceFixture) { f.delivery.resolution.Status = "ready" }},
		{"resolved candidate ID", func(f *sealedCandidateEvidenceFixture) { f.delivery.resolution.CandidateID = uuid.NewString() }},
		{"candidate revision", func(f *sealedCandidateEvidenceFixture) { f.delivery.candidate.CandidateRevision++ }},
		{"candidate status", func(f *sealedCandidateEvidenceFixture) { f.delivery.candidate.Status = "retired" }},
		{"candidate attempt", func(f *sealedCandidateEvidenceFixture) { f.delivery.candidate.AttemptID = uuid.NewString() }},
		{"candidate qualification digest", func(f *sealedCandidateEvidenceFixture) {
			f.delivery.candidate.QualificationDigest = activeResultIdentityDigest('8')
		}},
		{"seal candidate", func(f *sealedCandidateEvidenceFixture) { f.delivery.seal.CandidateID = uuid.NewString() }},
		{"seal attempt", func(f *sealedCandidateEvidenceFixture) { f.delivery.seal.AttemptID = uuid.NewString() }},
		{"seal artifact", func(f *sealedCandidateEvidenceFixture) {
			f.delivery.seal.ServingArtifactDigest = activeResultIdentityDigest('7')
		}},
		{"seal identity", func(f *sealedCandidateEvidenceFixture) { f.delivery.seal.SealID = uuid.NewString() }},
		{"qualification gate tampering", func(f *sealedCandidateEvidenceFixture) {
			var qualification appdeploymentpostgres.NativeQualificationEvidence
			require.NoError(t, json.Unmarshal(f.delivery.seal.QualificationEvidence, &qualification))
			qualification.Gates.BindingGeneration = activeResultIdentityDigest('6')
			qualification.Gates.Digest = ""
			gate, err := qualification.Gates.Canonical()
			require.NoError(t, err)
			qualification.Gates = gate
			qualification.Digest = ""
			raw, digest, err := qualification.Canonical()
			require.NoError(t, err)
			f.delivery.seal.QualificationEvidence = raw
			f.delivery.candidate.QualificationDigest = digest
		}},
		{"valid provenance from another candidate revision", func(f *sealedCandidateEvidenceFixture) {
			provenance := f.provenance
			provenance.Candidate.Revision++
			f.provenanceReader.provenance = rebuildSealedCandidateProvenance(t, provenance)
		}},
		{"valid provenance from another candidate ID", func(f *sealedCandidateEvidenceFixture) {
			provenance := f.provenance
			provenance.Candidate.ID = uuid.NewString()
			gate := *provenance.Plan.GateEvidence
			gate.CandidateID = provenance.Candidate.ID
			gate, err := gate.Canonical()
			require.NoError(t, err)
			provenance.Plan.GateEvidence = &gate
			f.provenanceReader.provenance = rebuildSealedCandidateProvenance(t, provenance)
		}},
		{"valid provenance from another generation", func(f *sealedCandidateEvidenceFixture) {
			provenance := f.provenance
			provenance.Plan.Identity.GenerationID = uuid.NewString()
			f.provenanceReader.provenance = rebuildSealedCandidateProvenance(t, provenance)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSealedCandidateEvidenceFixture(t)
			test.mutate(fixture)
			_, err := fixture.source.SealedCandidateResultIdentityEvidence(
				t.Context(), fixture.provenance.Plan.Identity, fixture.candidate.CandidateID,
				fixture.seal.SealID, fixture.candidate.ArtifactDigest,
			)
			require.ErrorIs(t, err, release.ErrProvenanceInvalid)
		})
	}
}

func TestSealedCandidateEvidenceRejectsAmbiguousLocalConnection(t *testing.T) {
	for _, overlap := range []string{"duplicate binding", "authored", "managed"} {
		t.Run(overlap, func(t *testing.T) {
			fixture := newSealedCandidateEvidenceFixture(t)
			provenance := fixture.provenance
			switch overlap {
			case "duplicate binding":
				duplicate := provenance.Plan.Bindings[0]
				duplicate.BindingID = "binding:second"
				duplicate.CredentialVersionID = ""
				duplicate.ValidatedVersion = "provider:v1"
				provenance.Plan.Bindings = append(provenance.Plan.Bindings, duplicate)
			case "authored":
				provenance.Plan.AuthoredConnections = append(provenance.Plan.AuthoredConnections, release.AuthoredConnectionEvidence{ConnectionID: "warehouse", ConnectorKind: "http"})
			case "managed":
				provenance.Plan.ManagedDataPins = append(provenance.Plan.ManagedDataPins, release.ManagedDataPin{ConnectionID: "warehouse", RevisionID: "revision:one"})
			}
			gate := *provenance.Plan.GateEvidence
			gate.BindingGeneration = release.BindingFingerprint(provenance.Plan.Bindings)
			gate, err := gate.Canonical()
			require.NoError(t, err)
			provenance.Plan.GateEvidence = &gate
			fixture.provenance = rebuildSealedCandidateProvenance(t, provenance)
			fixture.provenanceReader.provenance = fixture.provenance
			fixture.refreshQualification(t)
			_, err = fixture.source.SealedCandidateResultIdentityEvidence(
				t.Context(), fixture.provenance.Plan.Identity, fixture.candidate.CandidateID,
				fixture.seal.SealID, fixture.candidate.ArtifactDigest,
			)
			require.ErrorIs(t, err, release.ErrProvenanceInvalid)
		})
	}
}

type sealedCandidateEvidenceFixture struct {
	source           sealedCandidateEvidenceSource
	delivery         *sealedCandidateEvidenceDeliveryStub
	provenanceReader *sealedCandidateEvidenceProvenanceStub
	provenance       release.Provenance
	candidate        appdeploymentpostgres.DeliveryCandidate
	seal             appdeploymentpostgres.SnapshotSeal
}

func newSealedCandidateEvidenceFixture(t *testing.T) *sealedCandidateEvidenceFixture {
	t.Helper()
	active, provenance, _ := activeCredentialPinFixture(t)
	qual := appdeploymentpostgres.NativeQualificationEvidence{
		SchemaVersion: appdeploymentpostgres.NativeQualificationSchemaVersion,
		CandidateID:   provenance.Candidate.ID, AttemptID: uuid.NewString(),
		PhysicalPoolID: "pool", CatalogID: "catalog", SnapshotID: 42,
		ObjectRoot: "/objects", RelationNamespace: "_candidate_attempt",
		RelationManifestDigest: activeResultIdentityDigest('1'), ClosureDigest: activeResultIdentityDigest('2'),
		Runtime: appdeploymentpostgres.NativeRuntimeCompatibilityEvidence{
			SnapshotID: 42, CatalogType: "postgres", DataPath: "/objects",
			MetadataSchema: ducklake.MetadataSchemaForPool("pool"), DuckDBRuntime: "duckdb:test",
			DuckLakeExtension: "ducklake:test", CatalogFormat: "1",
			CompatibilityDigest: activeResultIdentityDigest('3'), CatalogSchemaVersion: "schema-v1",
		},
		Gates: *provenance.Plan.GateEvidence,
	}
	raw, qualificationDigest, err := qual.Canonical()
	require.NoError(t, err)
	provenanceReader := &sealedCandidateEvidenceProvenanceStub{provenance: provenance}
	candidate := appdeploymentpostgres.DeliveryCandidate{
		CandidateID: provenance.Candidate.ID, TargetID: active.targetID,
		PlanID: uuid.NewString(), AttemptID: qual.AttemptID, SnapshotSealID: uuid.NewString(),
		Status: "qualified", CandidateRevision: provenance.Candidate.Revision,
		ArtifactDigest: provenance.Artifact.ContentDigest, QualificationDigest: qualificationDigest,
		CreatedAt: time.Now().UTC(), QualifiedAt: time.Now().UTC(),
	}
	seal := appdeploymentpostgres.SnapshotSeal{
		SealID: candidate.SnapshotSealID, CandidateID: candidate.CandidateID, AttemptID: candidate.AttemptID,
		PhysicalPoolID: qual.PhysicalPoolID, CatalogID: qual.CatalogID, DuckLakeSnapshotID: qual.SnapshotID,
		ObjectRoot: qual.ObjectRoot, RelationNamespace: qual.RelationNamespace,
		RelationManifestDigest: qual.RelationManifestDigest, ClosureDigest: qual.ClosureDigest,
		PlanDigest: activeResultIdentityDigest('4'), ServingArtifactDigest: candidate.ArtifactDigest,
		RuntimeVersion: provenance.Plan.RuntimeVersion, DuckDBVersion: qual.Runtime.DuckDBRuntime,
		DuckLakeExtensionVersion: qual.Runtime.DuckLakeExtension, DuckLakeSpecVersion: qual.Runtime.CatalogFormat,
		CompatibilityDigest: qual.Runtime.CompatibilityDigest, CatalogSchemaVersion: qual.Runtime.CatalogSchemaVersion,
		CatalogVersion: 1, QualificationEvidence: raw, QualifiedAt: time.Now().UTC(),
	}
	delivery := &sealedCandidateEvidenceDeliveryStub{
		resolution: appdeploymentpostgres.CandidateGenerationResolution{
			CandidateID: candidate.CandidateID, TargetID: candidate.TargetID, PlanID: candidate.PlanID,
			SnapshotSealID: seal.SealID, Status: candidate.Status,
			CandidateRevision: candidate.CandidateRevision, ArtifactDigest: candidate.ArtifactDigest,
			ProjectID: provenance.Plan.Identity.ProjectID.String(), Environment: provenance.Plan.Identity.Environment,
			GenerationCount: 1, GenerationID: provenance.Plan.Identity.GenerationID,
		},
		candidate: candidate, seal: seal,
	}
	return &sealedCandidateEvidenceFixture{
		source:   sealedCandidateEvidenceSource{delivery: delivery, releases: provenanceReader, targetID: active.targetID, environment: active.environment},
		delivery: delivery, provenanceReader: provenanceReader, provenance: provenance,
		candidate: candidate, seal: seal,
	}
}

func (fixture *sealedCandidateEvidenceFixture) refreshQualification(t *testing.T) {
	t.Helper()
	var qualification appdeploymentpostgres.NativeQualificationEvidence
	require.NoError(t, json.Unmarshal(fixture.delivery.seal.QualificationEvidence, &qualification))
	qualification.Gates = *fixture.provenance.Plan.GateEvidence
	qualification.Digest = ""
	raw, digest, err := qualification.Canonical()
	require.NoError(t, err)
	fixture.delivery.seal.QualificationEvidence = raw
	fixture.delivery.candidate.QualificationDigest = digest
}

func rebuildSealedCandidateProvenance(t *testing.T, provenance release.Provenance) release.Provenance {
	t.Helper()
	rebuilt, err := release.NewProvenance(release.ProvenanceInput{
		Artifact: provenance.Artifact, Candidate: provenance.Candidate,
		SourceRevision: provenance.SourceRevision, Plan: provenance.Plan,
	})
	require.NoError(t, err)
	return rebuilt
}

type sealedCandidateEvidenceDeliveryStub struct {
	resolution         appdeploymentpostgres.CandidateGenerationResolution
	candidate          appdeploymentpostgres.DeliveryCandidate
	seal               appdeploymentpostgres.SnapshotSeal
	resolveErr         error
	candidateErr       error
	sealErr            error
	resolveCandidateID string
	candidateID        string
	sealID             string
	resolveCalls       int
	candidateCalls     int
	sealCalls          int
}

func (stub *sealedCandidateEvidenceDeliveryStub) ResolveCandidateGeneration(_ context.Context, candidateID string) (appdeploymentpostgres.CandidateGenerationResolution, error) {
	stub.resolveCalls++
	stub.resolveCandidateID = candidateID
	return stub.resolution, stub.resolveErr
}

func (stub *sealedCandidateEvidenceDeliveryStub) Candidate(_ context.Context, candidateID string) (appdeploymentpostgres.DeliveryCandidate, error) {
	stub.candidateCalls++
	stub.candidateID = candidateID
	return stub.candidate, stub.candidateErr
}

func (stub *sealedCandidateEvidenceDeliveryStub) SnapshotSeal(_ context.Context, sealID string) (appdeploymentpostgres.SnapshotSeal, error) {
	stub.sealCalls++
	stub.sealID = sealID
	return stub.seal, stub.sealErr
}

type sealedCandidateEvidenceProvenanceStub struct {
	provenance  release.Provenance
	projectID   projectgraph.ResourceID
	candidateID string
	revision    int64
	calls       int
	err         error
}

func (stub *sealedCandidateEvidenceProvenanceStub) CandidateProvenance(_ context.Context, projectID projectgraph.ResourceID, candidateID string, revision int64) (release.Provenance, error) {
	stub.projectID, stub.candidateID, stub.revision = projectID, candidateID, revision
	stub.calls++
	return stub.provenance, stub.err
}
