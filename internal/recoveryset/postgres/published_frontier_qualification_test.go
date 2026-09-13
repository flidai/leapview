//go:build fai518qualification

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	releasetransitionapp "github.com/flidai/leapview/internal/app/releasetransitionpreflight"
	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
)

func TestFAI518AuthoritativeTransitionPreflightQualification(t *testing.T) {
	db := recoverySetDB(t)
	repository := New(db)
	set := recoverySetFixture(t)
	created, err := repository.Create(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 9, 13, 1, 0, 0, 0, time.UTC)
	attempt := recoveryset.ValidationAttempt{
		AttemptID: "018f3f83-7b2f-7b37-9f9e-000000000500",
		SetID:     created.ID, OwnerID: "qualification-validator", FenceEpoch: created.FenceEpoch,
		AuditIdentity: created.AuditIdentity, Status: recoveryset.ValidationRunning, StartedAt: started,
	}
	if _, err := repository.BeginValidation(t.Context(), attempt); err != nil {
		t.Fatal(err)
	}
	envelope, err := recoveryset.NewValidationEvidenceEnvelope(created, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := recoveryset.NewValidationResult(envelope, started.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordValidationResult(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	attempt.Status, attempt.ResultDigest, attempt.CompletedAt = recoveryset.ValidationPassed, result.ResultDigest, started.Add(2*time.Second)
	if err := repository.CompleteValidation(t.Context(), attempt); err != nil {
		t.Fatal(err)
	}
	published, err := repository.Publish(t.Context(), created.ID, "qualification-publisher", created.FenceEpoch, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}

	target := transitionpreflight.TargetIdentity{TargetID: published.Delivery.TargetID, TargetRevision: published.Delivery.TargetRevision}
	targetDigest, err := target.Digest()
	if err != nil {
		t.Fatal(err)
	}
	ref := transitionpreflight.RecoveryFrontierRef{SetID: published.ID, Digest: published.FrontierDigest}
	authority := releasetransitionapp.NewPublishedRecoveryFrontierAuthority(repository)
	resolved, err := authority.ResolveRecoveryFrontier(t.Context(), ref, target)
	if err != nil {
		t.Fatalf("resolve published frontier: %v", err)
	}
	if resolved.SetID != published.ID || resolved.Digest != published.FrontierDigest || resolved.TargetIdentityDigest != targetDigest {
		t.Fatalf("resolved frontier = %#v, want set=%s digest=%s target=%s", resolved, published.ID, published.FrontierDigest, targetDigest)
	}
	resolver, request := qualificationTransitionResolver(t, target, ref, authority)
	first, err := resolver.ResolveAndEvaluate(t.Context(), request)
	if err != nil {
		t.Fatalf("authoritative preflight: %v", err)
	}
	second, err := resolver.ResolveAndEvaluate(t.Context(), request)
	if err != nil {
		t.Fatalf("authoritative preflight replay: %v", err)
	}
	if first.Evidence.Decision != transitionpreflight.DecisionBinaryRollbackCompatible || first.EvidenceDigest != second.EvidenceDigest || string(first.CanonicalEvidence) != string(second.CanonicalEvidence) {
		t.Fatalf("preflight replay differs: first=%#v second=%#v", first, second)
	}

	prepared := recoverySetFixture(t)
	prepared.ID = "018f3f83-7b2f-7b37-9f9e-000000000501"
	preparedCreated, err := repository.Create(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	preparedTarget := transitionpreflight.TargetIdentity{TargetID: preparedCreated.Delivery.TargetID, TargetRevision: preparedCreated.Delivery.TargetRevision}
	preparedTargetDigest, err := preparedTarget.Digest()
	if err != nil {
		t.Fatal(err)
	}
	preparedRef := transitionpreflight.RecoveryFrontierRef{SetID: preparedCreated.ID, Digest: preparedCreated.FrontierDigest, TargetIdentityDigest: preparedTargetDigest}
	if _, err := authority.ResolveRecoveryFrontier(t.Context(), preparedRef, preparedTarget); !errors.Is(err, transitionpreflight.ErrStaleFrontier) {
		t.Fatalf("prepared frontier error = %v, want ErrStaleFrontier", err)
	}

	if err := repository.Supersede(t.Context(), published.ID, published.FenceEpoch); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ResolveRecoveryFrontier(t.Context(), ref, target); !errors.Is(err, transitionpreflight.ErrStaleFrontier) {
		t.Fatalf("superseded frontier error = %v, want ErrStaleFrontier", err)
	}
}

func TestFAI518PublishedRecoveryFrontierRejectsTargetAndTamperedValidation(t *testing.T) {
	db := recoverySetDB(t)
	repository := New(db)
	set := recoverySetFixture(t)
	created, attempt, result := qualificationPublishedFrontier(t, repository, set)
	target := transitionpreflight.TargetIdentity{TargetID: created.Delivery.TargetID, TargetRevision: created.Delivery.TargetRevision}
	targetDigest, err := target.Digest()
	if err != nil {
		t.Fatal(err)
	}
	ref := transitionpreflight.RecoveryFrontierRef{SetID: created.ID, Digest: created.FrontierDigest, TargetIdentityDigest: targetDigest}
	authority := releasetransitionapp.NewPublishedRecoveryFrontierAuthority(repository)

	wrongTarget := target
	wrongTarget.TargetID = "other-target"
	if _, err := authority.ResolveRecoveryFrontier(t.Context(), ref, wrongTarget); !errors.Is(err, transitionpreflight.ErrResolutionMismatch) {
		t.Fatalf("target mismatch error = %v, want ErrResolutionMismatch", err)
	}

	tampered := result
	tampered.Evidence = append([]byte(nil), tampered.Evidence...)
	tampered.Evidence[len(tampered.Evidence)/2] = 'x'
	reader := qualificationRecoveryFrontierReader{Repository: repository, tamperedResult: &tampered, attempt: attempt}
	tamperedAuthority := releasetransitionapp.NewPublishedRecoveryFrontierAuthority(reader)
	if _, err := tamperedAuthority.ResolveRecoveryFrontier(t.Context(), ref, target); !errors.Is(err, transitionpreflight.ErrResolutionMismatch) {
		t.Fatalf("tampered validation error = %v, want ErrResolutionMismatch", err)
	}
}

func qualificationPublishedFrontier(t *testing.T, repository *Repository, set recoveryset.RecoverySet) (recoveryset.RecoverySet, recoveryset.ValidationAttempt, recoveryset.ValidationResult) {
	t.Helper()
	created, err := repository.Create(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 9, 13, 2, 0, 0, 0, time.UTC)
	attempt := recoveryset.ValidationAttempt{
		AttemptID: "018f3f83-7b2f-7b37-9f9e-000000000502", SetID: created.ID,
		OwnerID: "qualification-validator", FenceEpoch: created.FenceEpoch, AuditIdentity: created.AuditIdentity,
		Status: recoveryset.ValidationRunning, StartedAt: started,
	}
	if _, err := repository.BeginValidation(t.Context(), attempt); err != nil {
		t.Fatal(err)
	}
	envelope, err := recoveryset.NewValidationEvidenceEnvelope(created, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := recoveryset.NewValidationResult(envelope, started.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordValidationResult(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	attempt.Status, attempt.ResultDigest, attempt.CompletedAt = recoveryset.ValidationPassed, result.ResultDigest, started.Add(2*time.Second)
	if err := repository.CompleteValidation(t.Context(), attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Publish(t.Context(), created.ID, "qualification-publisher", created.FenceEpoch, attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	return createdWithPublishedStatus(t, repository, created.ID), attempt, result
}

func createdWithPublishedStatus(t *testing.T, repository *Repository, setID string) recoveryset.RecoverySet {
	t.Helper()
	set, err := repository.ReadExact(t.Context(), setID)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

type qualificationRecoveryFrontierReader struct {
	*Repository
	tamperedResult *recoveryset.ValidationResult
	attempt        recoveryset.ValidationAttempt
}

func (r qualificationRecoveryFrontierReader) ReadRecoveryFrontierSnapshot(ctx context.Context, setID string) (recoveryset.RecoverySet, recoveryset.ValidationAttempt, recoveryset.ValidationResult, error) {
	set, _, _, err := r.Repository.ReadRecoveryFrontierSnapshot(ctx, setID)
	if err != nil {
		return recoveryset.RecoverySet{}, recoveryset.ValidationAttempt{}, recoveryset.ValidationResult{}, err
	}
	return set, r.attempt, *r.tamperedResult, nil
}

func qualificationTransitionResolver(t *testing.T, target transitionpreflight.TargetIdentity, frontier transitionpreflight.RecoveryFrontierRef, frontierAuthority transitionpreflight.RecoveryFrontierAuthority) (*transitionpreflight.Resolver, transitionpreflight.ResolutionRequest) {
	t.Helper()
	pred := transitionpreflight.ArtifactIdentity{Release: compatibility.ReleaseIdentity{
		ReleaseID: "v1.0.0", Version: "1.0.0", SourceRevision: strings.Repeat("1", 40),
		Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64), Distribution: "public", Platform: "linux/amd64",
	}, ArchitectureMarker: transitionpreflight.ArchitecturePostgreSQL, ArtifactAdmissionDigest: "sha256:" + strings.Repeat("d", 64)}
	candidate := transitionpreflight.ArtifactIdentity{Release: compatibility.ReleaseIdentity{
		ReleaseID: "v1.1.0", Version: "1.1.0", SourceRevision: strings.Repeat("2", 40),
		Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64), Distribution: "public", Platform: "linux/amd64",
	}, ArchitectureMarker: transitionpreflight.ArchitecturePostgreSQL, ArtifactAdmissionDigest: "sha256:" + strings.Repeat("e", 64)}
	predDigest, err := pred.Digest()
	if err != nil {
		t.Fatal(err)
	}
	candidateDigest, err := candidate.Digest()
	if err != nil {
		t.Fatal(err)
	}
	targetDigest, err := target.Digest()
	if err != nil {
		t.Fatal(err)
	}
	policy := transitionpreflight.ReleasePolicy{Version: transitionpreflight.ReleasePolicyVersion, Rules: []transitionpreflight.ReleasePolicyRule{{
		PredecessorArtifactDigest: predDigest, CandidateArtifactDigest: candidateDigest,
		RollbackFromArtifactDigest: candidateDigest, RollbackToArtifactDigest: predDigest,
		Decision: transitionpreflight.DecisionBinaryRollbackCompatible,
	}}}
	policy.Digest, err = policy.ContentDigest()
	if err != nil {
		t.Fatal(err)
	}
	tuple := physicalpool.Compatibility{DuckDBRuntime: "duckdb:1.5.4", DuckLakeExtension: "ducklake:0.3.0", CatalogFormat: "ducklake-catalog:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	artifacts := transitionpreflight.ArtifactAuthorityFunc(func(_ context.Context, ref string) (transitionpreflight.ArtifactIdentity, error) {
		if ref == "predecessor" {
			return pred, nil
		}
		if ref == "candidate" {
			return candidate, nil
		}
		return transitionpreflight.ArtifactIdentity{}, transitionpreflight.ErrResolutionNotFound
	})
	targets := transitionpreflight.TargetAuthorityFunc(func(_ context.Context, ref string) (transitionpreflight.TargetIdentity, error) {
		if ref != "target" {
			return transitionpreflight.TargetIdentity{}, transitionpreflight.ErrResolutionNotFound
		}
		return target, nil
	})
	migrations := transitionpreflight.MigrationAuthorityFunc(func(context.Context, transitionpreflight.TargetIdentity, transitionpreflight.ArtifactIdentity, transitionpreflight.ArtifactIdentity) (transitionpreflight.MigrationResolution, error) {
		return transitionpreflight.MigrationResolution{
			PredecessorArtifactDigest: predDigest, CandidateArtifactDigest: candidateDigest,
			Ownership: transitionpreflight.MigrationOwnership{GooseControlSchemaOwner: transitionpreflight.OwnerLeapView, RiverOperationalSchemaOwner: transitionpreflight.OwnerRiver, RiverJobHistoryOwner: transitionpreflight.OwnerLeapView}, DuckLakeOwner: transitionpreflight.OwnerLeapView,
			Control:  transitionpreflight.PostgreSQLControlProjection{Compatibility: transitionpreflight.CompatibilityBackwardCompatible, PredecessorSchemaVersion: "goose/v12", CandidateSchemaVersion: "goose/v13", TargetIdentityDigest: targetDigest},
			River:    transitionpreflight.RiverJobProjection{SchemaCompatibility: transitionpreflight.CompatibilityBackwardCompatible, JobHistoryCompatibility: transitionpreflight.CompatibilityBackwardCompatible, ExistingSchemaVersion: "river/v6", RequiredSchemaVersion: "river/v7", ExistingJobHistoryVersion: "jobs/v12", RequiredJobHistoryVersion: "jobs/v13", TargetIdentityDigest: targetDigest},
			DuckLake: transitionpreflight.DuckLakeProjection{Compatibility: transitionpreflight.CompatibilityBackwardCompatible, Predecessor: tuple, Candidate: tuple, TargetIdentityDigest: targetDigest},
		}, nil
	})
	policies := transitionpreflight.ReleasePolicyAuthorityFunc(func(context.Context, transitionpreflight.ArtifactIdentity, transitionpreflight.ArtifactIdentity) (transitionpreflight.ReleasePolicy, error) {
		return policy, nil
	})
	resolver := transitionpreflight.NewResolver(artifacts, targets, migrations, policies, frontierAuthority)
	request := transitionpreflight.ResolutionRequest{PredecessorRef: "predecessor", CandidateRef: "candidate", TargetRef: "target", RecoveryFrontier: frontier}
	return resolver, request
}
