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

type targetReaderFenceRepositoryFake struct {
	targetReaderRepositoryFake
	fenceErr         error
	fenceTargetID    string
	fenceProjectID   string
	fenceEnvironment string
	fenceContext     context.Context
	callbackCalled   bool
}

func (f *targetReaderFenceRepositoryFake) WithUnpublishedTarget(ctx context.Context, targetID, projectID, environment string, callback func(context.Context) error) error {
	f.fenceContext, f.fenceTargetID, f.fenceProjectID, f.fenceEnvironment = ctx, targetID, projectID, environment
	if f.fenceErr != nil {
		return f.fenceErr
	}
	f.callbackCalled = true
	return callback(ctx)
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

func TestTargetReaderWithUnpublishedTargetPreservesActiveSentinelAndCallbackError(t *testing.T) {
	active := &targetReaderFenceRepositoryFake{fenceErr: nativepostgres.ErrAlreadyActive}
	reader := newTargetReader(active)
	called := false
	err := reader.WithUnpublishedTarget(t.Context(), "target-prod", "project-finance", "prod", func(context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrTargetAlreadyPublished) || called || active.callbackCalled {
		t.Fatalf("active-target result = %v, called=%v, repository callback=%v", err, called, active.callbackCalled)
	}

	denial := errors.New("access policy denied")
	callbackContext := context.WithValue(t.Context(), struct{}{}, "callback")
	denying := &targetReaderFenceRepositoryFake{}
	denyingReader := newTargetReader(denying)
	err = denyingReader.WithUnpublishedTarget(callbackContext, "target-prod", "project-finance", "prod", func(ctx context.Context) error {
		if ctx != callbackContext {
			t.Fatal("callback context was not forwarded")
		}
		return denial
	})
	if !errors.Is(err, denial) {
		t.Fatalf("callback error = %v, want %v", err, denial)
	}
	if denying.fenceContext != callbackContext || denying.fenceTargetID != "target-prod" || denying.fenceProjectID != "project-finance" || denying.fenceEnvironment != "prod" || !denying.callbackCalled {
		t.Fatalf("fence call = context %p target %q project %q environment %q callback=%v", denying.fenceContext, denying.fenceTargetID, denying.fenceProjectID, denying.fenceEnvironment, denying.callbackCalled)
	}
	if !errors.Is(ErrTargetAlreadyPublished, nativepostgres.ErrAlreadyActive) {
		t.Fatalf("adapter sentinel = %v, want native ErrAlreadyActive", ErrTargetAlreadyPublished)
	}
}
