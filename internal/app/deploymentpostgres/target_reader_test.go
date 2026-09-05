package deploymentpostgres

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
)

type targetReaderRepositoryFake struct {
	target nativepostgres.DeliveryTarget
	err    error
	ctx    context.Context
	id     string
}

func (f *targetReaderRepositoryFake) Target(ctx context.Context, id string) (nativepostgres.DeliveryTarget, error) {
	f.ctx, f.id = ctx, id
	if f.err != nil {
		return nativepostgres.DeliveryTarget{}, f.err
	}
	return f.target, nil
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

func TestTargetReaderFailsClosedWhenUnconfigured(t *testing.T) {
	var nilReader *TargetReader
	if _, err := nilReader.DeliveryTargetRevision(t.Context(), "target-prod"); !errors.Is(err, nativepostgres.ErrInvalid) {
		t.Fatalf("nil reader error = %v, want native ErrInvalid", err)
	}
	if _, err := NewTargetReader(nil).DeliveryTargetRevision(t.Context(), "target-prod"); !errors.Is(err, nativepostgres.ErrInvalid) {
		t.Fatalf("nil repository error = %v, want native ErrInvalid", err)
	}
}
