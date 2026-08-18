package sealedcontrol

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type fakePublicationStore struct {
	requested   int
	activated   int
	publication deployment.PublicationIntent
	err         error
	requestErr  error
	activateErr error
}

func (s *fakePublicationStore) RequestPublication(_ context.Context, p deployment.PublicationIntent, _ ...deployment.CatalogRoot) (deployment.PublicationIntent, error) {
	s.requested++
	if s.requestErr != nil {
		return deployment.PublicationIntent{}, s.requestErr
	}
	if s.err != nil {
		return deployment.PublicationIntent{}, s.err
	}
	if s.publication.ID != "" {
		return s.publication, nil
	}
	s.publication = p
	return p, nil
}

func (s *fakePublicationStore) ActivatePublication(_ context.Context, id string, now time.Time) (deployment.PublicationIntent, error) {
	s.activated++
	if s.activateErr != nil {
		return deployment.PublicationIntent{}, s.activateErr
	}
	if s.err != nil {
		return deployment.PublicationIntent{}, s.err
	}
	p := s.publication
	p.Status = deployment.DeliveryPublicationCommitted
	p.ResultTargetRevision = p.ExpectedTargetRevision + 1
	p.CompletedAt = now
	if p.ID != id {
		return deployment.PublicationIntent{}, errors.New("wrong publication")
	}
	s.publication = p
	return p, nil
}

type fakeRollbackStore struct {
	got deployment.RollbackRequest
	err error
}

func (s *fakeRollbackStore) Rollback(_ context.Context, request deployment.RollbackRequest) (deployment.RollbackResult, error) {
	s.got = request
	if s.err != nil {
		return deployment.RollbackResult{}, s.err
	}
	return deployment.RollbackResult{RequestDigest: request.RequestDigest, TargetID: request.TargetID, GenerationID: request.GenerationID, TargetRevision: request.ExpectedTargetRevision + 1, CatalogDigest: request.VerifiedSeal.CatalogDigest, CatalogObjectKey: request.VerifiedSeal.CatalogObjectKey, Status: string(deployment.DeliveryPublicationCommitted), CompletedAt: request.CreatedAt}, nil
}

func coordinatorDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func coordinatorSeal() deployment.VerifiedSeal {
	return deployment.VerifiedSeal{SealID: "seal-1", CatalogDigest: coordinatorDigest('a'), CatalogObjectKey: "catalogs/sha256/" + strings.Repeat("a", 64) + ".ducklake", ObjectSize: 1, PhysicalPoolID: "pool-1", CompatibilityDigest: coordinatorDigest('e'), ClosureDigest: coordinatorDigest('b'), QualificationDigest: coordinatorDigest('c'), ServingArtifactID: "artifact-1", ServingArtifactDigest: coordinatorDigest('f')}
}

func coordinatorGeneration(seal deployment.VerifiedSeal) deployment.CatalogRoot {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	generation, _ := deployment.NewCatalogRoot(deployment.CatalogRoot{ID: "generation-1", CandidateID: "candidate-1", PlanID: "plan-1", PlanDigest: coordinatorDigest('d'), TargetID: "target-1", ProjectID: projectgraph.ResourceID("project-1"), Environment: "prod", CatalogDigest: seal.CatalogDigest, CatalogObjectKey: seal.CatalogObjectKey, PhysicalPoolID: seal.PhysicalPoolID, ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest, ServingStateID: "state-1", CompatibilityDigest: seal.CompatibilityDigest, RollbackClass: deployment.DeliveryRollbackSafe, CreatedAt: now})
	return generation
}

func coordinatorPublication() deployment.PublicationIntent {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	p, _ := deployment.NewPublicationIntent(deployment.PublicationIntent{ID: "publication-1", RequestDigest: coordinatorDigest('e'), TargetID: "target-1", ProjectID: projectgraph.ResourceID("project-1"), Environment: "prod", PlanID: "plan-1", PlanDigest: coordinatorDigest('d'), CandidateID: "candidate-1", GenerationID: "generation-1", ExpectedTargetRevision: 0, CreatedAt: now})
	return p
}

func TestPublishRequiresAuthorizationAndBindsExactSeal(t *testing.T) {
	seal := coordinatorSeal()
	store := &fakePublicationStore{}
	called := 0
	coordinator := &Coordinator{Publications: store, VerifySeal: func(context.Context, SealBinding) error { called++; return nil }, Authorize: func(_ context.Context, binding SealBinding) error {
		if binding.Operation != "publish" {
			t.Fatalf("publish operation=%q", binding.Operation)
		}
		return nil
	}, Now: func() time.Time { return time.Date(2026, 8, 17, 12, 1, 0, 0, time.UTC) }}
	if _, err := coordinator.Publish(t.Context(), PublishRequest{Publication: coordinatorPublication(), Generation: coordinatorGeneration(seal), Seal: seal}); err != nil {
		t.Fatal(err)
	}
	if called != 1 || store.requested != 1 || store.activated != 1 {
		t.Fatalf("calls verifier=%d requested=%d activated=%d", called, store.requested, store.activated)
	}
	coordinator.Authorize = nil
	if _, err := coordinator.Publish(t.Context(), PublishRequest{Publication: coordinatorPublication(), Generation: coordinatorGeneration(seal), Seal: seal}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing authorization = %v, want ErrInvalidRequest", err)
	}
	coordinator.Authorize = func(context.Context, SealBinding) error { return nil }
	bad := seal
	bad.CatalogDigest = coordinatorDigest('f')
	if _, err := coordinator.Publish(t.Context(), PublishRequest{Publication: coordinatorPublication(), Generation: coordinatorGeneration(seal), Seal: bad}); !errors.Is(err, ErrSealUnverified) {
		t.Fatalf("wrong seal binding = %v, want ErrSealUnverified", err)
	}
	publication := coordinatorPublication()
	publication.CandidateID = "candidate-other"
	if _, err := coordinator.Publish(t.Context(), PublishRequest{Publication: publication, Generation: coordinatorGeneration(seal), Seal: seal}); !errors.Is(err, ErrSealUnverified) {
		t.Fatalf("candidate substitution = %v, want ErrSealUnverified", err)
	}
}

func TestPublishReconcilesCommittedLostResponseWithoutSecondActivation(t *testing.T) {
	seal := coordinatorSeal()
	store := &fakePublicationStore{publication: coordinatorPublication()}
	store.publication.Status = deployment.DeliveryPublicationCommitted
	store.publication.ResultTargetRevision = 1
	store.publication.CompletedAt = time.Date(2026, 8, 17, 12, 1, 0, 0, time.UTC)
	coordinator := &Coordinator{Publications: store, VerifySeal: func(context.Context, SealBinding) error { return nil }, Authorize: func(context.Context, SealBinding) error { return nil }}
	got, err := coordinator.Publish(t.Context(), PublishRequest{Publication: coordinatorPublication(), Generation: coordinatorGeneration(seal), Seal: seal})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != deployment.DeliveryPublicationCommitted || store.activated != 0 {
		t.Fatalf("reconciled publication=%#v activation calls=%d", got, store.activated)
	}
}

func TestPublishCommittedRequestResponseStillVerifiesSealWithoutApproval(t *testing.T) {
	seal := coordinatorSeal()
	store := &fakePublicationStore{publication: coordinatorPublication()}
	store.publication.Status = deployment.DeliveryPublicationCommitted
	var sealChecks, approvalChecks int
	coordinator := &Coordinator{
		Publications: store,
		VerifySeal: func(context.Context, SealBinding) error {
			sealChecks++
			return errors.New("remote seal changed")
		},
		Authorize: func(context.Context, SealBinding) error { return nil },
		ApprovalVerifier: func(context.Context, SealBinding, deployment.PublicationIntent) error {
			approvalChecks++
			return errors.New("fresh approval must be skipped")
		},
	}
	got, err := coordinator.Publish(t.Context(), PublishRequest{Publication: coordinatorPublication(), Generation: coordinatorGeneration(seal), Seal: seal})
	if !errors.Is(err, ErrSealUnverified) {
		t.Fatalf("committed publication seal failure = %v, want ErrSealUnverified", err)
	}
	if got.Status != deployment.DeliveryPublicationCommitted || sealChecks != 1 || approvalChecks != 0 {
		t.Fatalf("committed publication=%#v sealChecks=%d approvalChecks=%d, want committed/1/0", got, sealChecks, approvalChecks)
	}
}

func TestPublishPersistsPendingBeforeApprovalOrSealPreflight(t *testing.T) {
	seal := coordinatorSeal()
	store := &fakePublicationStore{}
	sealChecks := 0
	coordinator := &Coordinator{
		Publications: store,
		VerifySeal:   func(context.Context, SealBinding) error { sealChecks++; return nil },
		Authorize:    func(context.Context, SealBinding) error { return nil },
		ApprovalVerifier: func(context.Context, SealBinding, deployment.PublicationIntent) error {
			return deployment.ErrApprovalRequired
		},
	}
	got, err := coordinator.Publish(t.Context(), PublishRequest{Publication: coordinatorPublication(), Generation: coordinatorGeneration(seal), Seal: seal})
	if !errors.Is(err, ErrUnauthorized) || !errors.Is(err, deployment.ErrApprovalRequired) {
		t.Fatalf("approval failure = %v, want ErrUnauthorized and ErrApprovalRequired", err)
	}
	if got.ID == "" || store.requested != 1 || store.activated != 0 || sealChecks != 0 {
		t.Fatalf("pending publication=%#v requested=%d activated=%d sealChecks=%d", got, store.requested, store.activated, sealChecks)
	}
}

func TestPublishWithActivationEnforcesSingleCommitAndErrorPropagation(t *testing.T) {
	seal := coordinatorSeal()
	request := PublishRequest{Publication: coordinatorPublication(), Generation: coordinatorGeneration(seal), Seal: seal}
	tests := []struct {
		name       string
		storeErr   error
		activate   PublicationActivation
		wantErr    error
		wantActive int
	}{
		{name: "skipped callback", activate: func(context.Context, func() error) error { return nil }, wantErr: ErrActivationProtocol},
		{name: "double callback", activate: func(ctx context.Context, commit func() error) error { _ = commit(); _ = commit(); return nil }, wantErr: ErrActivationProtocol, wantActive: 1},
		{name: "swallowed commit failure", storeErr: errors.New("CAS failed"), activate: func(_ context.Context, commit func() error) error { _ = commit(); return nil }, wantErr: errors.New("CAS failed"), wantActive: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakePublicationStore{activateErr: test.storeErr}
			coordinator := &Coordinator{Publications: store, VerifySeal: func(context.Context, SealBinding) error { return nil }, Authorize: func(context.Context, SealBinding) error { return nil }}
			_, err := coordinator.PublishWithActivation(t.Context(), request, test.activate)
			if test.wantErr == ErrActivationProtocol {
				if !errors.Is(err, ErrActivationProtocol) {
					t.Fatalf("error = %v, want activation protocol error", err)
				}
			} else if test.wantErr != nil && (err == nil || !strings.Contains(err.Error(), test.wantErr.Error())) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if store.activated != test.wantActive {
				t.Fatalf("activation calls = %d, want %d", store.activated, test.wantActive)
			}
		})
	}
}

func TestRollbackRequiresAuthorizationAndUsesControlStore(t *testing.T) {
	seal := coordinatorSeal()
	request := deployment.RollbackRequest{ID: "rollback-1", RequestDigest: coordinatorDigest('f'), TargetID: "target-1", ProjectID: projectgraph.ResourceID("project-1"), Environment: "prod", GenerationID: "generation-1", CandidateID: "candidate-1", ExpectedTargetRevision: 0, VerifiedSeal: seal, CreatedAt: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	store := &fakeRollbackStore{}
	coordinator := &Coordinator{Rollbacks: store, VerifySeal: func(context.Context, SealBinding) error { return nil }, Authorize: func(_ context.Context, binding SealBinding) error {
		if binding.Operation != "rollback" {
			t.Fatalf("rollback operation=%q", binding.Operation)
		}
		return nil
	}}
	if _, err := coordinator.Rollback(t.Context(), RollbackRequest{Request: request}); err != nil {
		t.Fatal(err)
	}
	if store.got.ID != request.ID {
		t.Fatalf("rollback store request=%#v", store.got)
	}
	coordinator.Authorize = func(context.Context, SealBinding) error { return errors.New("denied") }
	if _, err := coordinator.Rollback(t.Context(), RollbackRequest{Request: request}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("denied rollback=%v, want ErrUnauthorized", err)
	}
}

func TestRollbackWithActivationRequiresCommit(t *testing.T) {
	seal := coordinatorSeal()
	request := deployment.RollbackRequest{ID: "rollback-1", RequestDigest: coordinatorDigest('f'), TargetID: "target-1", ProjectID: projectgraph.ResourceID("project-1"), Environment: "prod", GenerationID: "generation-1", CandidateID: "candidate-1", ExpectedTargetRevision: 0, VerifiedSeal: seal, CreatedAt: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	store := &fakeRollbackStore{}
	coordinator := &Coordinator{Rollbacks: store, VerifySeal: func(context.Context, SealBinding) error { return nil }, Authorize: func(context.Context, SealBinding) error { return nil }}
	_, err := coordinator.RollbackWithActivation(t.Context(), RollbackRequest{Request: request}, func(context.Context, func() error) error { return nil })
	if !errors.Is(err, ErrActivationProtocol) {
		t.Fatalf("skipped rollback commit = %v, want activation protocol error", err)
	}
	if store.got.ID != "" {
		t.Fatalf("rollback store called before activation commit: %#v", store.got)
	}
	_, err = coordinator.RollbackWithActivation(t.Context(), RollbackRequest{Request: request}, func(_ context.Context, commit func() error) error { _ = commit(); _ = commit(); return nil })
	if !errors.Is(err, ErrActivationProtocol) {
		t.Fatalf("double rollback commit = %v, want activation protocol error", err)
	}
	if store.got.ID != request.ID {
		t.Fatalf("rollback store request = %#v", store.got)
	}
}
