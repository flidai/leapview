package run

import (
	"context"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/workload"
)

var dispatcherIdentity = projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_a"}

func dispatcherJob(kind string) JobRecord {
	return JobRecord{ID: "job_1", Identity: dispatcherIdentity, SemanticModelID: "semantic_sales", PipelineID: "pipeline_sales", PrincipalID: "principal:test", EstimatedMemoryBytes: 64 << 20, RunID: "run_1", Kind: kind, TargetType: TargetRefreshPipeline, TargetID: "pipeline_sales", TriggerType: TriggerManual}
}

func TestDispatcherMarksUnsupportedJobFailed(t *testing.T) {
	ctx := context.Background()
	queue := &fakeQueueRepository{jobs: []JobRecord{dispatcherJob("unknown")}}

	Dispatcher{
		Runs: queue,
		Admitter: func() workload.Admitter {
			controller, err := workload.New(workload.Config{MaxRunning: 1, MaximumQueued: 1, Classes: map[workload.Class]workload.Policy{workload.Refresh: {MaximumRunning: 1, MaximumQueued: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			return controller
		}(),
		Owner:        "test-owner",
		LeaseTimeout: time.Minute,
	}.Run(ctx)

	if queue.failedRun != "run_1" {
		t.Fatalf("failed run = %q, want run_1", queue.failedRun)
	}
	if queue.failedMessage == "" {
		t.Fatal("failed message is empty")
	}
}

func TestDispatcherAdmissionRejectionLeavesDurableJobRetryable(t *testing.T) {
	queue := &fakeQueueRepository{jobs: []JobRecord{dispatcherJob(JobKindRefreshPipeline)}}
	controller, err := workload.New(workload.Config{MaxRunning: 1, Classes: map[workload.Class]workload.Policy{
		workload.Interactive: {MaximumRunning: 1}, workload.Refresh: {MaximumRunning: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	held, err := controller.Acquire(context.Background(), workload.Request{Class: workload.Interactive, PrincipalID: "sales", Operation: "hold", EstimatedMemoryBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	Dispatcher{Runs: queue, Admitter: controller, Identity: dispatcherIdentity, Owner: "test-owner", LeaseTimeout: time.Minute}.Run(context.Background())
	held.Release()
	if len(queue.jobs) != 1 || queue.claimOwner != "" {
		t.Fatalf("rejected job was claimed: %#v", queue)
	}
	if queue.failedRun != "" {
		t.Fatalf("rejected job was failed: %#v", queue)
	}
}

func TestDispatcherReleasesRefreshPermitBeforeRunFinished(t *testing.T) {
	queue := &fakeQueueRepository{jobs: []JobRecord{dispatcherJob("unknown")}}
	controller, err := workload.New(workload.Config{MaxRunning: 1, Classes: map[workload.Class]workload.Policy{
		workload.Refresh: {MaximumRunning: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runningAtCallback := -1
	Dispatcher{
		Runs: queue, Admitter: controller, Identity: dispatcherIdentity, Owner: "test-owner", LeaseTimeout: time.Minute,
		RunFinished: func(context.Context, JobRecord) { runningAtCallback = controller.Stats().Running },
	}.Run(context.Background())
	if runningAtCallback != 0 {
		t.Fatalf("running permits at completion callback = %d, want 0", runningAtCallback)
	}
}

type fakeQueueRepository struct {
	jobs          []JobRecord
	claimOwner    string
	renewedJob    string
	failedRun     string
	failedMessage string
}

func (r *fakeQueueRepository) ListExecutableJobs(context.Context, projectgraph.ServingIdentity, int) ([]JobRecord, error) {
	if len(r.jobs) == 0 {
		return nil, nil
	}
	return append([]JobRecord(nil), r.jobs...), nil
}

func (r *fakeQueueRepository) ClaimExecutableJob(_ context.Context, candidate JobRecord, owner string, _ time.Duration) (JobRecord, bool, error) {
	r.claimOwner = owner
	for index, job := range r.jobs {
		if job.ID != candidate.ID {
			continue
		}
		r.jobs = append(r.jobs[:index], r.jobs[index+1:]...)
		job.LeaseOwner = owner
		job.LeaseRevision++
		return job, true, nil
	}
	return JobRecord{}, false, nil
}

func (r *fakeQueueRepository) RenewJobLease(context.Context, JobRecord, time.Duration) error {
	return nil
}

func (r *fakeQueueRepository) JobQueueStats(context.Context, projectgraph.ServingIdentity) (JobQueueStats, error) {
	return JobQueueStats{}, nil
}

func (r *fakeQueueRepository) CreateRun(context.Context, RunInput) (RunRecord, error) {
	return RunRecord{}, nil
}

func (r *fakeQueueRepository) ListChildRuns(context.Context, projectgraph.ServingIdentity, string) ([]RunRecord, error) {
	return nil, nil
}

func (r *fakeQueueRepository) MarkRunRunning(context.Context, projectgraph.ServingIdentity, string) (RunRecord, error) {
	return RunRecord{}, nil
}

func (r *fakeQueueRepository) MarkRunSucceeded(context.Context, projectgraph.ServingIdentity, string) (RunRecord, error) {
	return RunRecord{}, nil
}

func (r *fakeQueueRepository) MarkRunFailed(_ context.Context, _ projectgraph.ServingIdentity, runID, message string) (RunRecord, error) {
	r.failedRun = runID
	r.failedMessage = message
	return RunRecord{ID: runID, Status: RunStatusFailed, Error: message}, nil
}

func (r *fakeQueueRepository) MarkRunSucceededClaimed(ctx context.Context, job JobRecord) (RunRecord, error) {
	return r.MarkRunSucceeded(ctx, job.Identity, job.RunID)
}

func (r *fakeQueueRepository) MarkRunFailedClaimed(ctx context.Context, job JobRecord, message string) (RunRecord, error) {
	return r.MarkRunFailed(ctx, job.Identity, job.RunID, message)
}

func (r *fakeQueueRepository) MarkRunTreeFailedClaimed(ctx context.Context, job JobRecord, message string) error {
	_, err := r.MarkRunFailedClaimed(ctx, job, message)
	return err
}

func (r *fakeQueueRepository) MarkRunPrepared(context.Context, JobRecord) (RunRecord, error) {
	return RunRecord{Status: RunStatusPrepared}, nil
}

func (r *fakeQueueRepository) RunMayPublish(context.Context, JobRecord) (bool, error) {
	return true, nil
}
