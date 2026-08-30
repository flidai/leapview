package deploymentpostgres

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
)

type startupNativeFake struct {
	target      nativepostgres.DeliveryTarget
	generation  nativepostgres.DeliveryGeneration
	publication nativepostgres.DeliveryPublication
	seal        nativepostgres.SnapshotSeal
	err         error
}

func (f startupNativeFake) Target(context.Context, string) (nativepostgres.DeliveryTarget, error) {
	return f.target, f.err
}

func (f startupNativeFake) Generation(context.Context, string) (nativepostgres.DeliveryGeneration, error) {
	return f.generation, f.err
}

func (f startupNativeFake) Publication(context.Context, string) (nativepostgres.DeliveryPublication, error) {
	return f.publication, f.err
}

func (f startupNativeFake) SnapshotSeal(context.Context, string) (nativepostgres.SnapshotSeal, error) {
	return f.seal, f.err
}

func TestStartupReaderProjectsNativeEvidence(t *testing.T) {
	fake := startupNativeFake{
		target:      nativepostgres.DeliveryTarget{TargetID: "target", ProjectID: "project", Environment: "prod", TargetRevision: 3, ActiveGenerationID: "generation", ActivePublicationID: "publication"},
		generation:  nativepostgres.DeliveryGeneration{GenerationID: "generation", TargetID: "target", CandidateID: "candidate", SnapshotSealID: "seal", PlanID: "plan", PlanDigest: "plan-digest", ArtifactRoot: "artifact-root", ArtifactRootDigest: "root-digest", ServingArtifactDigest: "artifact-digest", CompiledGraphDigest: "graph-digest", CompiledConfigDigest: "config-digest", SecurityDomainFingerprint: "security-digest"},
		publication: nativepostgres.DeliveryPublication{PublicationID: "publication", TargetID: "target", GenerationID: "generation", CandidateID: "candidate", SnapshotSealID: "seal", State: "committed", ExpectedTargetRevision: 2, ResultTargetRevision: 3},
		seal:        nativepostgres.SnapshotSeal{SealID: "seal", CandidateID: "candidate", PhysicalPoolID: "pool", CompatibilityDigest: "compatibility-digest", DuckLakeSnapshotID: 42, PlanDigest: "plan-digest", ServingArtifactID: "artifact", ServingArtifactDigest: "artifact-digest", CompiledGraphDigest: "graph-digest", CompiledConfigDigest: "config-digest", SecurityDomainFingerprint: "security-digest", ArtifactRoot: "artifact-root", ArtifactRootDigest: "root-digest"},
	}
	reader := newStartupReader(fake)
	target, err := reader.Target(t.Context(), "target")
	if err != nil || target.TargetID != fake.target.TargetID || target.ActiveGenerationID != fake.target.ActiveGenerationID || target.TargetRevision != fake.target.TargetRevision {
		t.Fatalf("target projection = %#v, %v", target, err)
	}
	generation, err := reader.Generation(t.Context(), "generation")
	if err != nil || generation.GenerationID != fake.generation.GenerationID || generation.SecurityDomainFingerprint != fake.generation.SecurityDomainFingerprint {
		t.Fatalf("generation projection = %#v, %v", generation, err)
	}
	publication, err := reader.Publication(t.Context(), "publication")
	if err != nil || publication.PublicationID != fake.publication.PublicationID || publication.ResultTargetRevision != fake.publication.ResultTargetRevision {
		t.Fatalf("publication projection = %#v, %v", publication, err)
	}
	seal, err := reader.SnapshotSeal(t.Context(), "seal")
	if err != nil || seal.SealID != fake.seal.SealID || seal.ServingArtifactID != fake.seal.ServingArtifactID || seal.DuckLakeSnapshotID != fake.seal.DuckLakeSnapshotID {
		t.Fatalf("seal projection = %#v, %v", seal, err)
	}
}

func TestStartupReaderNormalizesNativeNotFound(t *testing.T) {
	reader := newStartupReader(startupNativeFake{err: nativepostgres.ErrNotFound})
	if _, err := reader.Target(t.Context(), "missing"); !errors.Is(err, deployment.ErrNotFound) || errors.Is(err, nativepostgres.ErrNotFound) {
		t.Fatalf("not-found projection = %v", err)
	}
}
