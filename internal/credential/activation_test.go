package credential

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type activationAuthorityFake struct {
	record                   ActivationRecord
	events                   *[]string
	commitLost, completeLost bool
	denied                   bool
}

func (a *activationAuthorityFake) AuthorizeMutation(context.Context, string, Resource) error {
	if a.denied {
		return ErrForbidden
	}
	return nil
}
func (a *activationAuthorityFake) Prepare(_ context.Context, _ string, r Resource, q ActivationRequest) (ActivationRecord, error) {
	a.record = ActivationRecord{Resource: r, Request: q, Status: ActivationStatus{OperationID: q.OperationID, VersionID: q.VersionID, State: "prepared", BindingRevision: q.ExpectedBindingRevision}}
	return a.record, nil
}
func (a *activationAuthorityFake) Read(context.Context, string, Resource, string) (ActivationRecord, error) {
	return a.record, nil
}
func (a *activationAuthorityFake) Pending(context.Context) (ActivationRecord, error) {
	if a.record.Status.State == "" || a.record.Status.State == "completed" || a.record.Status.State == "aborted" {
		return ActivationRecord{}, ErrNotFound
	}
	return a.record, nil
}
func (a *activationAuthorityFake) Switch(_ context.Context, _ string, _ ActivationRecord, receipt string) (ActivationRecord, error) {
	*a.events = append(*a.events, "switch")
	a.record.Status.State = "switching"
	return a.record, nil
}
func (a *activationAuthorityFake) Commit(context.Context, string, ActivationRecord) (ActivationRecord, error) {
	*a.events = append(*a.events, "commit")
	a.record.Status.State = "committed"
	if a.commitLost {
		a.commitLost = false
		return ActivationRecord{}, errors.New("commit acknowledgment lost")
	}
	return a.record, nil
}
func (a *activationAuthorityFake) Complete(context.Context, ActivationRecord) (ActivationRecord, error) {
	*a.events = append(*a.events, "complete")
	a.record.Status.State = "completed"
	if a.completeLost {
		a.completeLost = false
		return ActivationRecord{}, errors.New("completion acknowledgment lost")
	}
	return a.record, nil
}
func (a *activationAuthorityFake) Abort(context.Context, string, ActivationRecord) (ActivationRecord, error) {
	*a.events = append(*a.events, "abort")
	a.record.Status.State = "aborted"
	return a.record, nil
}

type activationRuntimeFake struct {
	events   *[]string
	fail     bool
	versions []string
}

func (r *activationRuntimeFake) InstallCommitted(_ context.Context, record ActivationRecord) error {
	*r.events = append(*r.events, "install")
	r.versions = append(r.versions, record.Status.VersionID)
	if r.fail {
		return ErrUnavailable
	}
	return nil
}
func (r *activationRuntimeFake) RestoreCurrent(context.Context) error {
	*r.events = append(*r.events, "restore")
	return nil
}

func activationFixture(t *testing.T) (*ActivationCoordinator, *activationAuthorityFake, *activationRuntimeFake) {
	t.Helper()
	events := []string{}
	authority := &activationAuthorityFake{events: &events}
	runtime := &activationRuntimeFake{events: &events}
	service, err := NewActivationCoordinator(authority, runtime, NewProviderAdmission(), 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	resource := Resource{ScopeKind: "connection", TargetID: "target", ProjectID: "project", Environment: "production", ResourceID: "connection"}
	request := ActivationRequest{OperationID: uuid.NewString(), VersionID: uuid.NewString(), ReceiptID: uuid.NewString(), ExpectedBindingRevision: 1}
	if _, err := service.StartActivation(t.Context(), "actor", resource, request); err != nil {
		t.Fatal(err)
	}
	return service, authority, runtime
}

func retryActivation(t *testing.T, s *ActivationCoordinator, a *activationAuthorityFake) (ActivationStatus, error) {
	t.Helper()
	return s.RetryActivation(t.Context(), "actor", a.record.Resource, a.record.Request.OperationID, uuid.NewString())
}

func TestActivationRecoveryKeepsExactCommittedVersionAfterLostAcknowledgment(t *testing.T) {
	for _, boundary := range []string{"commit", "completion"} {
		t.Run(boundary, func(t *testing.T) {
			s, a, r := activationFixture(t)
			a.commitLost = boundary == "commit"
			a.completeLost = boundary == "completion"
			if _, err := retryActivation(t, s, a); err == nil {
				t.Fatal("lost acknowledgment accepted")
			}
			if s.admission.Ready() {
				t.Fatal("uncertain operation admitted work")
			}
			version := a.record.Request.VersionID
			fresh, err := NewActivationCoordinator(a, r, NewProviderAdmission(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			status, err := fresh.RetryActivation(t.Context(), "actor", a.record.Resource, a.record.Request.OperationID, "")
			if err != nil {
				t.Fatal(err)
			}
			if status.State != "completed" || !status.RuntimeReady {
				t.Fatalf("recovery status=%+v", status)
			}
			for _, used := range r.versions {
				if used != version {
					t.Fatal("recovery substituted credential")
				}
			}
			commits, completions := 0, 0
			for _, event := range *a.events {
				if event == "commit" {
					commits++
				}
				if event == "complete" {
					completions++
				}
			}
			if commits != 1 || completions != 1 {
				t.Fatalf("duplicate durable transitions: %v", *a.events)
			}
		})
	}
}

func TestActivationDrainMustObserveReleaseBeforeCommit(t *testing.T) {
	s, a, _ := activationFixture(t)
	if err := s.admission.Resume(); err != nil {
		t.Fatal(err)
	}
	work, release, err := s.admission.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := retryActivation(t, s, a); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain error=%v", err)
	}
	if work.Err() == nil || s.admission.Ready() || a.record.Status.State != "switching" {
		t.Fatal("unreleased work allowed publication")
	}
	release()
	if _, err := retryActivation(t, s, a); err != nil {
		t.Fatal(err)
	}
}

func TestActivationReadinessFailureRemainsClosedAndAbortCannotUndoCommit(t *testing.T) {
	s, a, r := activationFixture(t)
	r.fail = true
	if _, err := retryActivation(t, s, a); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("readiness error=%v", err)
	}
	if s.admission.Ready() || a.record.Status.State != "committed" {
		t.Fatal("failed replacement was admitted or commitment lost")
	}
	if _, err := s.AbortActivation(t.Context(), "actor", a.record.Resource, a.record.Request.OperationID); !errors.Is(err, ErrConflict) {
		t.Fatalf("abort committed=%v", err)
	}
	r.fail = false
	if _, err := retryActivation(t, s, a); err != nil {
		t.Fatal(err)
	}
}

func TestActivationCompletedRetryStillRequiresMutationAuthority(t *testing.T) {
	s, a, _ := activationFixture(t)
	if _, err := retryActivation(t, s, a); err != nil {
		t.Fatal(err)
	}
	a.denied = true
	if _, err := retryActivation(t, s, a); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked completed retry=%v", err)
	}
}

func TestActivationStartupPreparedRequiresExplicitRecovery(t *testing.T) {
	s, a, r := activationFixture(t)
	if err := s.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if s.admission.Ready() || len(r.versions) != 0 {
		t.Fatal("startup implicitly resumed uncommitted operation")
	}
	if _, err := s.AbortActivation(t.Context(), "actor", a.record.Resource, a.record.Request.OperationID); err != nil {
		t.Fatal(err)
	}
	if !s.admission.Ready() || a.record.Status.State != "aborted" {
		t.Fatal("safe abort did not restore work")
	}
}
