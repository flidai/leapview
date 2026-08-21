package schedule

import (
	"context"
	"errors"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

type schedulerRepository struct {
	due         []Occurrence
	claimed     time.Time
	environment string
	released    []string
}

func (repository *schedulerRepository) Reconcile(context.Context, ReconcileInput) error { return nil }
func (repository *schedulerRepository) ClaimDue(_ context.Context, identity projectgraph.ServingIdentity, now time.Time) ([]Occurrence, error) {
	repository.environment = identity.Environment
	repository.claimed = now
	return repository.due, nil
}
func (repository *schedulerRepository) ReleaseOccurrence(_ context.Context, occurrence Occurrence) error {
	repository.released = append(repository.released, occurrence.PipelineID.String())
	return nil
}
func (*schedulerRepository) NextRun(context.Context, projectgraph.ServingIdentity, projectgraph.ResourceID) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (*schedulerRepository) SaveDataVersion(context.Context, DataVersion) error { return nil }
func (*schedulerRepository) DataVersion(context.Context, projectgraph.ServingIdentity, projectgraph.ResourceID) (DataVersion, bool, error) {
	return DataVersion{}, false, nil
}

func TestSchedulerContinuesAfterOnePipelineCannotBeQueued(t *testing.T) {
	now := time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)
	repository := &schedulerRepository{due: []Occurrence{
		{Identity: projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_a"}, PipelineID: "pipeline_broken", ScheduledAt: now},
		{Identity: projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_a"}, PipelineID: "pipeline_healthy", ScheduledAt: now},
	}}
	scheduler := Scheduler{
		Repository: repository,
		Clock:      fixedClock{now: now},
		ResolveIdentity: func(context.Context) (projectgraph.ServingIdentity, error) {
			return projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_a"}, nil
		},
		Trigger: func(_ context.Context, occurrence Occurrence) error {
			if occurrence.PipelineID == "pipeline_broken" {
				return errors.New("queue unavailable")
			}
			return nil
		},
	}
	if err := scheduler.DispatchDue(context.Background()); err == nil {
		t.Fatal("DispatchDue() error = nil, want aggregate error")
	}
	if len(repository.released) != 1 || repository.released[0] != "pipeline_broken" {
		t.Fatalf("released = %#v, want broken occurrence released", repository.released)
	}
}

func TestSchedulerUsesInjectedClockAndTrustsAtomicTrigger(t *testing.T) {
	now := time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)
	repository := &schedulerRepository{due: []Occurrence{{Identity: projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_a"}, PipelineID: "pipeline_daily", ScheduledAt: now}}}
	triggered := 0
	scheduler := Scheduler{
		Repository: repository,
		Clock:      fixedClock{now: now},
		ResolveIdentity: func(context.Context) (projectgraph.ServingIdentity, error) {
			return projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_a"}, nil
		},
		Trigger: func(_ context.Context, occurrence Occurrence) error {
			triggered++
			return nil
		},
	}
	if err := scheduler.DispatchDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.environment != "prod" || !repository.claimed.Equal(now) || triggered != 1 {
		t.Fatalf("environment=%q claimed=%s triggered=%d", repository.environment, repository.claimed, triggered)
	}
}

func TestSchedulerDoesNotRetryPolicySkippedOccurrence(t *testing.T) {
	now := time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)
	repository := &schedulerRepository{due: []Occurrence{{Identity: projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_a"}, PipelineID: "pipeline_daily", MatchingScheduleIDs: []string{"weekdays"}, SemanticModelID: "semantic_sales", ArtifactDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Timezone: "UTC", ScheduledAt: now}}}
	scheduler := Scheduler{
		Repository: repository, Clock: fixedClock{now: now},
		ResolveIdentity: func(context.Context) (projectgraph.ServingIdentity, error) {
			return projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_a"}, nil
		},
		Trigger: func(context.Context, Occurrence) error {
			return ErrOccurrenceSkipped
		},
	}
	if err := scheduler.DispatchDue(context.Background()); err != nil {
		t.Fatalf("DispatchDue() error = %v", err)
	}
	if len(repository.released) != 0 {
		t.Fatalf("skipped occurrence released = %#v", repository.released)
	}
}
