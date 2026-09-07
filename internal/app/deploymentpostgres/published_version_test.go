package deploymentpostgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type publishedVersionReaderFake struct {
	operator    nativepostgres.DeliveryOperatorSnapshot
	generation  nativepostgres.DeliveryGeneration
	candidate   nativepostgres.DeliveryCandidate
	seal        nativepostgres.SnapshotSeal
	attempt     nativepostgres.DeliveryBuildAttempt
	publication nativepostgres.DeliveryPublication
	err         error
}

func (f publishedVersionReaderFake) OperatorSnapshot(context.Context, string) (nativepostgres.DeliveryOperatorSnapshot, error) {
	if f.err != nil {
		return nativepostgres.DeliveryOperatorSnapshot{}, f.err
	}
	return f.operator, nil
}
func (f publishedVersionReaderFake) LoadGeneration(context.Context, string) (nativepostgres.DeliveryGeneration, error) {
	if f.err != nil {
		return nativepostgres.DeliveryGeneration{}, f.err
	}
	return f.generation, nil
}
func (f publishedVersionReaderFake) LoadCandidate(context.Context, string) (nativepostgres.DeliveryCandidate, error) {
	if f.err != nil {
		return nativepostgres.DeliveryCandidate{}, f.err
	}
	return f.candidate, nil
}
func (f publishedVersionReaderFake) LoadSnapshotSeal(context.Context, string) (nativepostgres.SnapshotSeal, error) {
	if f.err != nil {
		return nativepostgres.SnapshotSeal{}, f.err
	}
	return f.seal, nil
}
func (f publishedVersionReaderFake) LoadBuildAttempt(context.Context, string) (nativepostgres.DeliveryBuildAttempt, error) {
	if f.err != nil {
		return nativepostgres.DeliveryBuildAttempt{}, f.err
	}
	return f.attempt, nil
}
func (f publishedVersionReaderFake) LoadPublication(context.Context, string) (nativepostgres.DeliveryPublication, error) {
	if f.err != nil {
		return nativepostgres.DeliveryPublication{}, f.err
	}
	return f.publication, nil
}

func publishedVersionFixture(t *testing.T) (publishedVersionReaderFake, projectgraph.ServingIdentity, time.Time) {
	t.Helper()
	projectID, err := projectgraph.NewResourceID("project:finance")
	if err != nil {
		t.Fatal(err)
	}
	identity := projectgraph.ServingIdentity{ProjectID: projectID, Environment: "production", GenerationID: "generation-42"}
	committedAt := time.Date(2026, 8, 31, 12, 15, 56, 123000000, time.UTC)
	planDigest := "sha256:" + strings.Repeat("a", 64)
	return publishedVersionReaderFake{
		operator: nativepostgres.DeliveryOperatorSnapshot{
			ProjectID: "project:finance", Environment: "production", TargetID: "target-prod", TargetRevision: 8,
			ActiveGenerationID: "generation-42", ActivePublicationID: "publication-42",
		},
		generation: nativepostgres.DeliveryGeneration{
			GenerationID: "generation-42", TargetID: "target-prod", CandidateID: "candidate-42", SnapshotSealID: "seal-42", PlanID: "plan-42", PlanDigest: planDigest,
		},
		candidate: nativepostgres.DeliveryCandidate{
			CandidateID: "candidate-42", TargetID: "target-prod", PlanID: "plan-42", AttemptID: "attempt-42", SnapshotSealID: "seal-42", Status: "qualified",
		},
		seal: nativepostgres.SnapshotSeal{
			SealID: "seal-42", AttemptID: "attempt-42", CandidateID: "candidate-42", PlanDigest: planDigest, DuckLakeSnapshotID: 84, QualifiedAt: committedAt.Add(-time.Minute),
		},
		attempt: nativepostgres.DeliveryBuildAttempt{
			AttemptID: "attempt-42", PlanID: "plan-42", CandidateID: "candidate-42", PlanDigest: planDigest, State: nativepostgres.AttemptCommitted, SnapshotID: 84,
		},
		publication: nativepostgres.DeliveryPublication{
			PublicationID: "publication-42", TargetID: "target-prod", GenerationID: "generation-42", CandidateID: "candidate-42", SnapshotSealID: "seal-42",
			ExpectedTargetRevision: 7, ResultTargetRevision: 8, State: "committed", CommittedAt: committedAt,
		},
	}, identity, committedAt
}

func TestNativePublishedDataVersionUsesActiveCommittedPublication(t *testing.T) {
	reader, identity, committedAt := publishedVersionFixture(t)
	resolve := NewNativePublishedDataVersionResolver(reader, "target-prod")

	version, found, err := resolve(t.Context(), identity)
	if err != nil || !found {
		t.Fatalf("resolve native published data version = %#v, %t, %v", version, found, err)
	}
	if version.SnapshotID != 84 || !version.RefreshedAt.Equal(committedAt) {
		t.Fatalf("published data version = %#v, want snapshot 84 at %s", version, committedAt)
	}
}

func TestNativePublishedDataVersionRejectsEvidenceDrift(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*publishedVersionReaderFake)
	}{
		{name: "target scope", mutate: func(f *publishedVersionReaderFake) { f.operator.Environment = "staging" }},
		{name: "generation", mutate: func(f *publishedVersionReaderFake) { f.generation.CandidateID = "candidate-other" }},
		{name: "candidate", mutate: func(f *publishedVersionReaderFake) { f.candidate.SnapshotSealID = "seal-other" }},
		{name: "seal", mutate: func(f *publishedVersionReaderFake) { f.seal.DuckLakeSnapshotID = 85 }},
		{name: "attempt", mutate: func(f *publishedVersionReaderFake) { f.attempt.State = nativepostgres.AttemptAborted }},
		{name: "publication", mutate: func(f *publishedVersionReaderFake) { f.publication.State = "pending" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader, identity, _ := publishedVersionFixture(t)
			tc.mutate(&reader)
			_, found, err := NewNativePublishedDataVersionResolver(reader, "target-prod")(t.Context(), identity)
			if err == nil || found {
				t.Fatalf("resolver = found %t, err %v; want fail-closed evidence error", found, err)
			}
		})
	}
}

func TestNativePublishedDataVersionForwardsReaderError(t *testing.T) {
	reader, identity, _ := publishedVersionFixture(t)
	wantErr := errors.New("control plane unavailable")
	reader.err = wantErr
	_, found, err := NewNativePublishedDataVersionResolver(reader, "target-prod")(t.Context(), identity)
	if found || !errors.Is(err, wantErr) {
		t.Fatalf("resolver = found %t, err %v; want wrapped %v", found, err, wantErr)
	}
}
