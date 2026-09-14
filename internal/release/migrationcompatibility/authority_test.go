package migrationcompatibility

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
)

type gooseStub struct {
	evidence ControlEvidence
	err      error
}

func (s *gooseStub) ResolveGooseCompatibility(context.Context, Query) (ControlEvidence, error) {
	return s.evidence, s.err
}

type riverStub struct {
	evidence RiverEvidence
	err      error
}

func (s *riverStub) ResolveRiverCompatibility(context.Context, Query) (RiverEvidence, error) {
	return s.evidence, s.err
}

type duckLakeStub struct {
	evidence DuckLakeEvidence
	err      error
}

func (s *duckLakeStub) ResolveDuckLakeCompatibility(context.Context, Query) (DuckLakeEvidence, error) {
	return s.evidence, s.err
}

type physicalPoolStub struct {
	evidence PhysicalPoolEvidence
	err      error
}

func (s *physicalPoolStub) ResolvePhysicalPoolCompatibility(context.Context, Query) (PhysicalPoolEvidence, error) {
	return s.evidence, s.err
}

func TestResolverProducesCanonicalOwnerBackedProjection(t *testing.T) {
	resolver, target, predecessor, candidate := fixture(t)
	first, err := resolver.Resolve(t.Context(), target, predecessor, candidate)
	if err != nil {
		t.Fatalf("resolve compatibility: %v", err)
	}
	second, err := resolver.Resolve(t.Context(), target, predecessor, candidate)
	if err != nil {
		t.Fatalf("repeat compatibility: %v", err)
	}
	if first.Digest != second.Digest || string(first.CanonicalBytes) != string(second.CanonicalBytes) {
		t.Fatal("identical owner state did not produce identical evidence")
	}
	if first.Projection.OverallCompatibility != transitionpreflight.CompatibilityBackwardCompatible {
		t.Fatalf("overall compatibility = %q", first.Projection.OverallCompatibility)
	}
	parsed, err := ParseCanonical(first.CanonicalBytes)
	if err != nil {
		t.Fatalf("parse canonical projection: %v", err)
	}
	parsedDigest, err := parsed.Digest()
	if err != nil || parsedDigest != first.Digest {
		t.Fatalf("readback digest = %q, %v", parsedDigest, err)
	}

	const golden = "sha256:f22edfc4597fb8a6b6ee6ae8e911588173fb8816d052deade9df92431a021935"
	if first.Digest != golden {
		t.Fatalf("compatibility digest changed: got %s want %s\ncanonical=%s", first.Digest, golden, first.CanonicalBytes)
	}
}

func TestResolverRejectsMissingAndConflictingOwnerEvidence(t *testing.T) {
	tests := map[string]struct {
		mutate func(*Resolver)
		want   error
	}{
		"missing Goose evidence":   {func(r *Resolver) { r.goose.(*gooseStub).err = errors.New("not found") }, ErrOwnerEvidenceMissing},
		"River candidate mismatch": {func(r *Resolver) { r.river.(*riverStub).evidence.Binding.CandidateArtifactDigest = digest('9') }, ErrOwnerConflict},
		"stale candidate":          {func(r *Resolver) { r.duckLake.(*duckLakeStub).evidence.Binding.CandidateArtifactDigest = digest('8') }, ErrOwnerConflict},
		"conflicting subsystem tuples": {func(r *Resolver) {
			r.physicalPool.(*physicalPoolStub).evidence.Projection.Candidate.ObjectNamingContract = "uuidv7:v2"
		}, ErrOwnerConflict},
		"unsupported migration version": {func(r *Resolver) { r.goose.(*gooseStub).evidence.Version = "goose-compatibility/v2" }, ErrUnsupportedVersion},
		"ambiguous compatibility": {func(r *Resolver) {
			r.river.(*riverStub).evidence.Projection.SchemaCompatibility = transitionpreflight.CompatibilityUnknown
		}, ErrOwnerConflict},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			resolver, target, predecessor, candidate := fixture(t)
			tc.mutate(resolver)
			if _, err := resolver.Resolve(t.Context(), target, predecessor, candidate); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestResolverPreservesAuthoritativeIncompatibility(t *testing.T) {
	for _, domain := range []string{"ducklake", "physical-pool"} {
		t.Run(domain, func(t *testing.T) {
			resolver, target, predecessor, candidate := fixture(t)
			switch domain {
			case "ducklake":
				resolver.duckLake.(*duckLakeStub).evidence.Projection.Compatibility = transitionpreflight.CompatibilityIncompatible
			case "physical-pool":
				resolver.physicalPool.(*physicalPoolStub).evidence.Projection.Compatibility = transitionpreflight.CompatibilityIncompatible
			}
			result, err := resolver.Resolve(t.Context(), target, predecessor, candidate)
			if err != nil {
				t.Fatalf("resolve incompatibility: %v", err)
			}
			if result.Projection.OverallCompatibility != transitionpreflight.CompatibilityIncompatible {
				t.Fatalf("overall compatibility = %q", result.Projection.OverallCompatibility)
			}
		})
	}
}

func TestResolverRequiresAllOwnersAndExactArtifacts(t *testing.T) {
	resolver, target, predecessor, candidate := fixture(t)
	resolver.goose = (*gooseStub)(nil)
	if _, err := resolver.Resolve(t.Context(), target, predecessor, candidate); !errors.Is(err, ErrOwnerUnavailable) {
		t.Fatalf("typed nil owner error = %v", err)
	}
	resolver, target, predecessor, candidate = fixture(t)
	candidate = predecessor
	if _, err := resolver.Resolve(t.Context(), target, predecessor, candidate); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("same artifact error = %v", err)
	}
}

func TestCanonicalProjectionRejectsMutation(t *testing.T) {
	resolver, target, predecessor, candidate := fixture(t)
	result, err := resolver.Resolve(t.Context(), target, predecessor, candidate)
	if err != nil {
		t.Fatal(err)
	}
	mutated := append([]byte(nil), result.CanonicalBytes...)
	mutated = []byte(strings.Replace(string(mutated), "goose/v15", "goose/v14", 1))
	parsed, err := ParseCanonical(mutated)
	if err != nil {
		t.Fatalf("parse structurally valid mutation: %v", err)
	}
	digestAfter, err := parsed.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if digestAfter == result.Digest {
		t.Fatal("content mutation retained compatibility digest")
	}
}

func fixture(t *testing.T) (*Resolver, string, transitionpreflight.ArtifactIdentity, transitionpreflight.ArtifactIdentity) {
	t.Helper()
	predecessor := artifact("predecessor", 'a', '1')
	candidate := artifact("candidate", 'b', '2')
	predDigest, err := predecessor.Digest()
	if err != nil {
		t.Fatal(err)
	}
	candidateDigest, err := candidate.Digest()
	if err != nil {
		t.Fatal(err)
	}
	target := digest('c')
	binding := Binding{PredecessorArtifactDigest: predDigest, CandidateArtifactDigest: candidateDigest, TargetIdentityDigest: target}
	tuple := physicalpool.Compatibility{DuckDBRuntime: "duckdb:1.4", DuckLakeExtension: "ducklake:0.3", CatalogFormat: "ducklake:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	control := transitionpreflight.PostgreSQLControlProjection{Compatibility: transitionpreflight.CompatibilityBackwardCompatible, PredecessorSchemaVersion: "goose/v14", CandidateSchemaVersion: "goose/v15", TargetIdentityDigest: target}
	riverProjection := transitionpreflight.RiverJobProjection{SchemaCompatibility: transitionpreflight.CompatibilityBackwardCompatible, JobHistoryCompatibility: transitionpreflight.CompatibilityBackwardCompatible, ExistingSchemaVersion: "river/v1", RequiredSchemaVersion: "river/v1", ExistingJobHistoryVersion: "jobs/v1", RequiredJobHistoryVersion: "jobs/v1", TargetIdentityDigest: target}
	duckProjection := transitionpreflight.DuckLakeProjection{Compatibility: transitionpreflight.CompatibilityBackwardCompatible, Predecessor: tuple, Candidate: tuple, TargetIdentityDigest: target}
	poolProjection := PhysicalPoolProjection{Compatibility: transitionpreflight.CompatibilityBackwardCompatible, Predecessor: tuple, Candidate: tuple, TargetIdentityDigest: target}
	return NewResolver(
		&gooseStub{evidence: ControlEvidence{Version: GooseOwnerVersion, Binding: binding, Owner: transitionpreflight.OwnerLeapView, Projection: control}},
		&riverStub{evidence: RiverEvidence{Version: RiverOwnerVersion, Binding: binding, OperationalSchemaOwner: transitionpreflight.OwnerRiver, JobHistoryOwner: transitionpreflight.OwnerLeapView, Projection: riverProjection}},
		&duckLakeStub{evidence: DuckLakeEvidence{Version: DuckLakeOwnerVersion, Binding: binding, Owner: transitionpreflight.OwnerLeapView, Projection: duckProjection}},
		&physicalPoolStub{evidence: PhysicalPoolEvidence{Version: PhysicalPoolOwnerVersion, Binding: binding, Owner: transitionpreflight.OwnerLeapView, Projection: poolProjection}},
	), target, predecessor, candidate
}

func artifact(name string, imageByte, evidenceByte byte) transitionpreflight.ArtifactIdentity {
	return transitionpreflight.ArtifactIdentity{
		Release: compatibility.ReleaseIdentity{
			ReleaseID: name, Version: "1.0.0", SourceRevision: strings.Repeat(string(imageByte), 40),
			Image:        "registry.example.com/flidai/leapview@sha256:" + strings.Repeat(string(imageByte), 64),
			Distribution: "linux", Platform: "linux/amd64",
		},
		ArchitectureMarker:      transitionpreflight.ArchitecturePostgreSQL,
		ArtifactAdmissionDigest: digest(evidenceByte),
	}
}

func digest(b byte) string { return "sha256:" + strings.Repeat(string(b), 64) }
