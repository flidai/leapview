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
	attached    map[string]string
	released    []string
}

func (repository *schedulerRepository) Reconcile(context.Context, ReconcileInput) error { return nil }
func (repository *schedulerRepository) ClaimDue(_ context.Context, identity projectgraph.ServingIdentity, now time.Time) ([]Occurrence, error) {
	repository.environment = identity.Environment
	repository.claimed = now
	return repository.due, nil
}
func (repository *schedulerRepository) AttachRun(_ context.Context, occurrence Occurrence, runID string) error {
	if repository.attached == nil {
		repository.attached = map[string]string{}
	}
	repository.attached[occurrence.PipelineID.String()] = runID
	return nil
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
		Identity:   projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_a"},
		Trigger: func(_ context.Context, occurrence Occurrence) (string, error) {
			if occurrence.PipelineID == "pipeline_broken" {
				return "", errors.New("queue unavailable")
			}
			return "run_healthy", nil
		},
	}
	if err := scheduler.DispatchDue(context.Background()); err == nil {
		t.Fatal("DispatchDue() error = nil, want aggregate error")
	}
	if repository.attached["pipeline_healthy"] != "run_healthy" {
		t.Fatalf("attached = %#v, want healthy occurrence attached", repository.attached)
	}
	if len(repository.released) != 1 || repository.released[0] != "pipeline_broken" {
		t.Fatalf("released = %#v, want broken occurrence released", repository.released)
	}
}

func TestSchedulerUsesInjectedClockAndAttachesCreatedRun(t *testing.T) {
	now := time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)
	repository := &schedulerRepository{due: []Occurrence{{Identity: projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_a"}, PipelineID: "pipeline_daily", ScheduledAt: now}}}
	triggered := 0
	scheduler := Scheduler{
		Repository: repository,
		Clock:      fixedClock{now: now},
		Identity:   projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_a"},
		Trigger: func(_ context.Context, occurrence Occurrence) (string, error) {
			triggered++
			return "run_1", nil
		},
	}
	if err := scheduler.DispatchDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.environment != "prod" || !repository.claimed.Equal(now) || triggered != 1 || repository.attached["pipeline_daily"] != "run_1" {
		t.Fatalf("environment=%q claimed=%s triggered=%d attached=%#v", repository.environment, repository.claimed, triggered, repository.attached)
	}
}
