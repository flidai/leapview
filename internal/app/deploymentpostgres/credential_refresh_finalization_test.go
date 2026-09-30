package deploymentpostgres

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	apprefreshpostgres "github.com/flidai/leapview/internal/app/refreshpostgres"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	postgresmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// This fixture starts at the prepared refresh/result boundary. Physical build
// and canonical artifact verification are deterministic seams; credential-pin
// admission, native finalization, refresh persistence and job completion all
// use their real PostgreSQL implementations in the same transaction.
type credentialRefreshFixture struct {
	credentialPublicationFixture
	input       GenerationAdmissionInput
	refresh     *refreshpostgres.Repository
	queue       *refreshmodule.PostgresJobsAdapter
	finalizer   *apprefreshpostgres.PostgresNativeRefreshFinalizerAdapter
	job         refreshrun.JobRecord
	result      refreshrun.CanonicalRefreshResult
	evidence    refreshpostgres.PublicationInput
	publication string
}

type credentialRefreshEvidence struct {
	job    refreshrun.JobRecord
	result refreshrun.CanonicalRefreshResult
	input  refreshpostgres.PublicationInput
}

func (e credentialRefreshEvidence) VerifyCanonicalRefreshTx(_ context.Context, _ refreshpostgres.Tx, job refreshrun.JobRecord, result refreshrun.CanonicalRefreshResult) (refreshpostgres.PublicationInput, error) {
	if !reflect.DeepEqual(job, e.job) || result != e.result {
		return refreshpostgres.PublicationInput{}, refreshpostgres.ErrConflict
	}
	return e.input, nil
}

func (f credentialRefreshFixture) persistence(t *testing.T, queue refreshmodule.PostgresJobsAuthority) refreshmodule.Persistence {
	t.Helper()
	audit, err := apprefreshpostgres.NewPostgresCancelAuditWriterAdapter(accesspostgres.New())
	if err != nil {
		t.Fatal(err)
	}
	persistence, err := refreshmodule.NewPostgresPersistence(f.refresh, refreshmodule.PostgresPersistenceConfig{
		SchedulerOwner: "credential-refresh-test", Jobs: queue, NativeFinalizer: f.finalizer, CancelAuditWriter: audit,
		CanonicalVerifier: credentialRefreshEvidence{job: f.job, result: f.result, input: f.evidence},
		PublicationIdentityResolver: refreshmodule.PostgresPublicationIdentityResolverFunc(func(_ context.Context, _ refreshpostgres.Tx, request refreshmodule.PostgresPublicationIdentityRequest) (refreshmodule.PostgresPublicationIdentity, error) {
			if request.GenerationID != f.result.ServingStateID || request.ProjectID != f.job.Identity.ProjectID.String() ||
				request.Environment != f.job.Identity.Environment || request.SemanticModelID != f.job.SemanticModelID.String() ||
				request.PipelineID != f.job.PipelineID.String() || request.RunID != f.job.RunID || request.TargetRevision != f.job.TargetRevision || request.Source != "refresh" {
				return refreshmodule.PostgresPublicationIdentity{}, refreshpostgres.ErrConflict
			}
			// Leave snapshot comparison to real completion/replay validation;
			// this seam supplies only the physical namespace, not result approval.
			return refreshmodule.PostgresPublicationIdentity{PhysicalPoolID: f.evidence.PhysicalPoolID, CatalogID: f.evidence.CatalogID}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return persistence
}

func newCredentialRefreshFixture(t *testing.T, change func(*release.BindingEvidence)) credentialRefreshFixture {
	t.Helper()
	f := credentialRefreshFixture{credentialPublicationFixture: newCredentialPublicationFixtureWithOptions(t, credentialPublicationFixtureOptions{candidateVersionMatches: true, localPin: true})}
	activateCredentialPinnedBaseForContinuityTest(t, f.credentialPublicationFixture)
	pin := f.generationInput.Provenance.Plan.Bindings[0]
	if change != nil {
		change(&pin)
	}
	f.input = admitCredentialPublicationSuccessor(t, f.credentialPublicationFixture, []release.BindingEvidence{pin})
	if err := postgresmigrations.ApplyRiver(t.Context(), f.db); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	for _, apply := range []func(context.Context, refreshpostgres.Tx) error{
		func(ctx context.Context, tx refreshpostgres.Tx) error { return jobspostgres.ApplySchema(ctx, tx) },
		func(ctx context.Context, tx refreshpostgres.Tx) error { return operationpostgres.ApplySchema(ctx, tx) },
		func(ctx context.Context, tx refreshpostgres.Tx) error { return refreshpostgres.ApplySchema(ctx, tx) },
	} {
		if err := apply(t.Context(), tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.refresh = refreshpostgres.New(f.db)
	f.queue = refreshmodule.NewPostgresJobsAdapter(jobspostgres.New(f.db), f.refresh)
	f.finalizer, err = apprefreshpostgres.NewPostgresNativeRefreshFinalizer(f.refresh, ordinaryCredentialPublicationRepository(t, f.credentialPublicationFixture), admissionInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	identity := projectgraph.ServingIdentity{ProjectID: f.input.Bundle.ProjectID, Environment: "prod", GenerationID: f.generationInput.Generation.GenerationID}
	pipeline, err := projectpipelineplan.New(projectpipelineplan.Plan{
		ID: "credential-refresh-plan", PipelineID: "pipeline:credential-refresh", SemanticModelID: "semantic:credential-refresh",
		ProjectID: identity.ProjectID.String(), Environment: identity.Environment, ServingGenerationID: identity.GenerationID,
		ArtifactDigest: admissionDigest('e'), SelectionDigest: admissionDigest('f'), MaterializationScope: []string{"accounts"},
		ModelExecutionOrder: []string{"accounts"}, QualificationChecks: []string{"snapshot"}, InvocationSource: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.result = refreshrun.CanonicalRefreshResult{PlanID: f.input.Generation.PlanID, ServingStateID: f.input.Generation.GenerationID, NativeGenerationID: f.input.Generation.GenerationID, SnapshotID: f.input.Seal.DuckLakeSnapshotID}
	persistence := f.persistence(t, f.queue)
	root, _, err := persistence.Runs.CreateRunTree(t.Context(), refreshrun.RunTreeInput{Root: refreshrun.RunInput{
		RunID: "credential-refresh-run", Identity: identity, SemanticModelID: projectgraph.ResourceID(pipeline.SemanticModelID), PipelineID: projectgraph.ResourceID(pipeline.PipelineID), PipelinePlan: &pipeline,
		InvocationSource: "manual", PrincipalID: f.activation.ActorID, EstimatedMemoryBytes: 1, TargetRevision: 2,
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: projectgraph.ResourceID(pipeline.PipelineID), TriggerType: refreshrun.TriggerManual, JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: `{}`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Model the River-owned claim, following the existing native finalizer
	// fixture; this is not a second application queue claimant.
	jobID := "refresh-job-" + root.ID
	const owner = "credential-refresh-worker"
	var riverID int64
	if err := f.db.QueryRow(t.Context(), `SELECT river_job_id FROM jobs.job_history WHERE id=$1`, jobID).Scan(&riverID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(t.Context(), `UPDATE public.river_job SET state='running',attempt=1,attempted_by=array_append(attempted_by,$2) WHERE id=$1`, riverID, owner); err != nil {
		t.Fatal(err)
	}
	riverJob := &river.Job[jobspostgres.RefreshPipelineArgs]{JobRow: &rivertype.JobRow{ID: riverID, Attempt: 1, State: rivertype.JobStateRunning, AttemptedBy: []string{owner}}}
	executionCtx := jobspostgres.ContextWithRiverExecution(t.Context(), riverJob, owner, time.Minute)
	history, err := f.queue.Jobs.MarkRunning(executionCtx, jobID, 1)
	if err != nil {
		t.Fatal(err)
	}
	history.LeaseOwner, history.LeaseGeneration = owner, 1
	f.job, err = f.queue.ClaimRiverJob(t.Context(), history, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := persistence.Runs.MarkRunPrepared(t.Context(), f.job); err != nil {
		t.Fatal(err)
	}
	f.evidence = refreshpostgres.PublicationInput{RunID: f.job.RunID, BaseGenerationID: identity.GenerationID, ResultGenerationID: f.result.ServingStateID, ExpectedTargetRevision: 2, ResultTargetRevision: 3, PhysicalPoolID: f.input.Seal.PhysicalPoolID, CatalogID: f.input.Seal.CatalogID}
	f.publication, _, _, _ = apprefreshpostgres.NativeRefreshIdentities(f.job, f.result, f.evidence)
	return f
}

type credentialRefreshLateFailure struct {
	*refreshmodule.PostgresJobsAdapter
	err    error
	called bool
}

func (q *credentialRefreshLateFailure) CompleteJobTx(ctx context.Context, tx refreshpostgres.Tx, job refreshrun.JobRecord) error {
	if err := q.PostgresJobsAdapter.CompleteJobTx(ctx, tx, job); err != nil {
		return err
	}
	q.called = true
	return q.err
}

func TestCredentialRefreshFinalizationRollsBackAndReplays(t *testing.T) {
	f := newCredentialRefreshFixture(t, nil)
	failure := errors.New("injected failure after job completion")
	queue := &credentialRefreshLateFailure{PostgresJobsAdapter: f.queue, err: failure}
	failed := f.persistence(t, queue).Publication.(refreshrun.CanonicalPublicationUnitOfWork)
	if err := failed.CompleteCanonicalRefresh(t.Context(), f.job, f.result); !errors.Is(err, failure) || !queue.called {
		t.Fatalf("late completion = %v, called=%v", err, queue.called)
	}
	f.assertOutcome(t, false)
	publication := f.persistence(t, f.queue).Publication.(refreshrun.CanonicalPublicationUnitOfWork)
	if err := publication.CompleteCanonicalRefresh(t.Context(), f.job, f.result); err != nil {
		t.Fatalf("retry unchanged-pin refresh: %v", err)
	}
	f.assertOutcome(t, true)
	// Lost acknowledgement: a fresh persistence adapter accepts the exact
	// committed result after the worker lease has been released.
	publication = f.persistence(t, f.queue).Publication.(refreshrun.CanonicalPublicationUnitOfWork)
	if err := publication.CompleteCanonicalRefresh(t.Context(), f.job, f.result); err != nil {
		t.Fatalf("replay committed refresh: %v", err)
	}
	f.assertOutcome(t, true)
	altered := f.result
	altered.SnapshotID++
	if err := publication.CompleteCanonicalRefresh(t.Context(), f.job, altered); !errors.Is(err, refreshpostgres.ErrConflict) {
		t.Fatalf("different snapshot replay = %v, want conflict", err)
	}
	f.assertOutcome(t, true)
}

func TestCredentialRefreshFinalizationRejectsChangedPins(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*release.BindingEvidence)
	}{
		{"version", func(pin *release.BindingEvidence) { pin.CredentialVersionID = credentialPublicationUUID(t) }},
		{"endpoint", func(pin *release.BindingEvidence) { pin.EndpointConfigHash = admissionDigest('b') }},
		{"local to provider", func(pin *release.BindingEvidence) { pin.CredentialVersionID, pin.ValidatedVersion = "", "postgres-v1" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCredentialRefreshFixture(t, test.change)
			publication := f.persistence(t, f.queue).Publication.(refreshrun.CanonicalPublicationUnitOfWork)
			if err := publication.CompleteCanonicalRefresh(t.Context(), f.job, f.result); !errors.Is(err, deploymentnative.ErrConflict) {
				t.Fatalf("changed-pin refresh = %v, want conflict", err)
			}
			f.assertOutcome(t, false)
		})
	}
}

func (f credentialRefreshFixture) assertOutcome(t *testing.T, committed bool) {
	t.Helper()
	wantGeneration, wantPublication, wantRevision := f.job.Identity.GenerationID, f.credentialPublicationFixture.publication.PublicationID, int64(2)
	wantRun, wantJob, wantCount := refreshrun.RunStatusPrepared, jobs.StatusRunning, 0
	if committed {
		wantGeneration, wantPublication, wantRevision = f.result.ServingStateID, f.publication, 3
		wantRun, wantJob, wantCount = refreshrun.RunStatusSucceeded, jobs.StatusSucceeded, 1
	}
	target, err := f.delivery.Target(t.Context(), admissionInstanceID)
	if err != nil || target.ActiveGenerationID != wantGeneration || target.ActivePublicationID != wantPublication || target.TargetRevision != wantRevision {
		t.Fatalf("target committed=%v: %#v, %v", committed, target, err)
	}
	native, err := f.delivery.Publication(t.Context(), f.publication)
	if committed {
		if err != nil || native.State != "committed" || native.GenerationID != f.result.ServingStateID || native.ExpectedBaseGenerationID != f.job.Identity.GenerationID || native.ResultTargetRevision != 3 {
			t.Fatalf("native publication: %#v, %v", native, err)
		}
	} else if !errors.Is(err, deploymentnative.ErrNotFound) {
		t.Fatalf("native publication after rollback: %#v, %v", native, err)
	}
	run, err := f.refresh.LookupRun(t.Context(), f.job.RunID)
	if err != nil || run.Status != wantRun {
		t.Fatalf("refresh run: %#v, %v", run, err)
	}
	history, err := f.queue.Jobs.Get(t.Context(), f.job.ID)
	if err != nil || history.Status != wantJob {
		t.Fatalf("refresh job: %#v, %v", history, err)
	}
	version, found, err := f.refresh.DataVersion(t.Context(), f.job.Identity.ProjectID.String(), f.job.Identity.Environment, f.job.SemanticModelID.String(), f.result.ServingStateID)
	if err != nil || found != committed || (found && (version.SnapshotID != f.result.SnapshotID || version.TargetRevision != 3 || version.RunID != f.job.RunID || version.PhysicalPoolID != f.evidence.PhysicalPoolID || version.CatalogID != f.evidence.CatalogID)) {
		t.Fatalf("data version: %#v, found=%v, err=%v", version, found, err)
	}
	for _, check := range []struct{ name, query, id string }{
		{"refresh link", `SELECT count(*) FROM refresh.publication_link WHERE run_id=$1`, f.job.RunID},
		{"activation event", `SELECT count(*) FROM event.event_log WHERE event_id=$1::uuid`, f.publication},
		{"activation audit", `SELECT count(*) FROM audit.audit_event WHERE audit_id=$1::uuid`, f.publication},
		{"generation retention", `SELECT count(*) FROM delivery.delivery_retention_root WHERE generation_id=$1::uuid AND root_kind='generation'`, f.result.ServingStateID},
	} {
		var count int
		if err := f.db.QueryRow(t.Context(), check.query, check.id).Scan(&count); err != nil || count != wantCount {
			t.Fatalf("%s count=%d, want=%d, err=%v", check.name, count, wantCount, err)
		}
	}
	if committed {
		candidate, err := f.delivery.Candidate(t.Context(), f.input.Generation.CandidateID)
		if err != nil {
			t.Fatal(err)
		}
		provenance, err := f.provenance.CandidateProvenance(t.Context(), f.input.Bundle.ProjectID, candidate.CandidateID, candidate.CandidateRevision)
		if err != nil || !reflect.DeepEqual(provenance.Plan.Bindings, f.generationInput.Provenance.Plan.Bindings) {
			t.Fatalf("committed credential pins changed: %#v, %v", provenance.Plan.Bindings, err)
		}
	}
}
