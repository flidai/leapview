package deploymentpostgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
)

type targetReaderRepositoryFake struct {
	target     nativepostgres.DeliveryTarget
	generation nativepostgres.DeliveryGeneration
	err        error
	ctx        context.Context
	id         string
}

func (f *targetReaderRepositoryFake) Target(ctx context.Context, id string) (nativepostgres.DeliveryTarget, error) {
	f.ctx, f.id = ctx, id
	if f.err != nil {
		return nativepostgres.DeliveryTarget{}, f.err
	}
	return f.target, nil
}

func (f *targetReaderRepositoryFake) ActiveGeneration(ctx context.Context, id string) (nativepostgres.DeliveryGeneration, error) {
	f.ctx, f.id = ctx, id
	if f.err != nil {
		return nativepostgres.DeliveryGeneration{}, f.err
	}
	return f.generation, nil
}

func TestTargetReaderMapsCompleteTargetFence(t *testing.T) {
	repository := &targetReaderRepositoryFake{target: nativepostgres.DeliveryTarget{
		TargetID:            "target-prod",
		ProjectID:           "project-finance",
		Environment:         "production",
		TargetRevision:      42,
		ActiveGenerationID:  "generation-42",
		ActivePublicationID: "publication-42",
	}}
	reader := newTargetReader(repository)
	ctx := context.WithValue(t.Context(), struct{}{}, "request")

	got, err := reader.DeliveryTargetRevision(ctx, "target-prod")
	if err != nil {
		t.Fatal(err)
	}
	want := deployment.DeliveryTarget{
		TargetID:            "target-prod",
		ProjectID:           "project-finance",
		Environment:         "production",
		TargetRevision:      42,
		ActiveGenerationID:  "generation-42",
		ActivePublicationID: "publication-42",
	}
	if got != want {
		t.Fatalf("mapped target = %+v, want %+v", got, want)
	}
	if repository.ctx != ctx || repository.id != "target-prod" {
		t.Fatalf("Target call = context %p, id %q; want context %p, id %q", repository.ctx, repository.id, ctx, "target-prod")
	}
}

func TestTargetReaderForwardsTargetError(t *testing.T) {
	wantErr := errors.New("control plane unavailable")
	reader := newTargetReader(&targetReaderRepositoryFake{err: wantErr})

	_, err := reader.DeliveryTargetRevision(t.Context(), "target-prod")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapped %v", err, wantErr)
	}
}

func TestTargetReaderResolvesDeliveryTarget(t *testing.T) {
	repository := &targetReaderRepositoryFake{target: nativepostgres.DeliveryTarget{
		TargetID: "target-prod", ProjectID: "project-finance", Environment: "production", TargetRevision: 42,
	}}
	reader := newTargetReader(repository)
	got, err := reader.ResolveDeliveryTarget(t.Context(), "target-prod")
	if err != nil {
		t.Fatal(err)
	}
	if got.TargetID != repository.target.TargetID || got.ProjectID != repository.target.ProjectID || got.Environment != repository.target.Environment || got.TargetRevision != repository.target.TargetRevision {
		t.Fatalf("resolved target = %#v, want %#v", got, repository.target)
	}
}

func TestTargetReaderResolvesExactActiveGeneration(t *testing.T) {
	repository := &targetReaderRepositoryFake{
		target: nativepostgres.DeliveryTarget{
			TargetID: "target-prod", ProjectID: "project-finance", Environment: "production", TargetRevision: 42,
			ActiveGenerationID: "0198f2c0-7c7a-7f00-8a11-000000000105",
		},
		generation: nativepostgres.DeliveryGeneration{
			GenerationID: "0198f2c0-7c7a-7f00-8a11-000000000105", TargetID: "target-prod",
			CandidateID: "0198f2c0-7c7a-7f00-8a11-000000000102", PlanID: "0198f2c0-7c7a-7f00-8a11-000000000101",
			PlanDigest: "sha256:" + strings.Repeat("a", 64), ServingArtifactDigest: "sha256:" + strings.Repeat("b", 64),
		},
	}
	reader := newTargetReader(repository)
	got, err := reader.ActiveDeliveryGenerationForTarget(t.Context(), "target-prod", "project-finance", "production")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != repository.generation.GenerationID || got.ServingStateID != got.ID || got.TargetID != repository.target.TargetID || got.ProjectID.String() != repository.target.ProjectID || got.Environment != repository.target.Environment || got.CandidateID != repository.generation.CandidateID || got.PlanID != repository.generation.PlanID || got.PlanDigest != repository.generation.PlanDigest || got.ServingArtifactDigest != repository.generation.ServingArtifactDigest || got.Status != deployment.DeliveryGenerationActive {
		t.Fatalf("active generation = %#v, want native identity mapped to active domain state", got)
	}
}

func TestTargetReaderRejectsActiveGenerationScopeOrPointerDrift(t *testing.T) {
	base := &targetReaderRepositoryFake{
		target: nativepostgres.DeliveryTarget{
			TargetID: "target-prod", ProjectID: "project-finance", Environment: "production", TargetRevision: 1,
			ActiveGenerationID: "0198f2c0-7c7a-7f00-8a11-000000000105",
		},
		generation: nativepostgres.DeliveryGeneration{
			GenerationID: "0198f2c0-7c7a-7f00-8a11-000000000106", TargetID: "target-prod",
		},
	}
	for name, scope := range map[string][2]string{
		"project":     [2]string{"project-other", "production"},
		"environment": [2]string{"project-finance", "staging"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newTargetReader(base).ActiveDeliveryGenerationForTarget(t.Context(), "target-prod", scope[0], scope[1])
			if !errors.Is(err, deployment.ErrDeliveryConflict) {
				t.Fatalf("scope drift error = %v, want deployment conflict", err)
			}
		})
	}
	if _, err := newTargetReader(base).ActiveDeliveryGenerationForTarget(t.Context(), "target-prod", "project-finance", "production"); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("pointer drift error = %v, want deployment conflict", err)
	}
}

func TestTargetReaderMapsMissingActiveGeneration(t *testing.T) {
	repository := &targetReaderRepositoryFake{target: nativepostgres.DeliveryTarget{TargetID: "target-prod", ProjectID: "project-finance", Environment: "production", TargetRevision: 1}}
	_, err := newTargetReader(repository).ActiveDeliveryGenerationForTarget(t.Context(), "target-prod", "project-finance", "production")
	if !errors.Is(err, deployment.ErrNotFound) || errors.Is(err, nativepostgres.ErrNotFound) {
		t.Fatalf("missing active generation error = %v, want neutral not found", err)
	}
}

func TestTargetReaderFailsClosedWhenUnconfigured(t *testing.T) {
	var nilReader *TargetReader
	if _, err := nilReader.DeliveryTargetRevision(t.Context(), "target-prod"); !errors.Is(err, nativepostgres.ErrInvalid) {
		t.Fatalf("nil reader error = %v, want native ErrInvalid", err)
	}
	if _, err := NewTargetReader(nil).DeliveryTargetRevision(t.Context(), "target-prod"); !errors.Is(err, nativepostgres.ErrInvalid) {
		t.Fatalf("nil repository error = %v, want native ErrInvalid", err)
	}
}
