package refreshpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/sourcework"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/flidai/leapview/pkg/permissions"
	"github.com/google/uuid"
)

const (
	nativeExecutorTarget       = "target-prod"
	nativeExecutorProject      = "project-prod"
	nativeExecutorEnvironment  = "prod"
	nativeExecutorBase         = "0198f2c0-7c7a-7f00-8a11-000000000101"
	nativeExecutorPlan         = "0198f2c0-7c7a-7f00-8a11-000000000102"
	nativeExecutorBuild        = "0198f2c0-7c7a-7f00-8a11-000000000103"
	nativeExecutorCandidate    = "0198f2c0-7c7a-7f00-8a11-000000000104"
	nativeExecutorSeal         = "0198f2c0-7c7a-7f00-8a11-000000000105"
	nativeExecutorResult       = "0198f2c0-7c7a-7f00-8a11-000000000106"
	nativeExecutorLease        = "0198f2c0-7c7a-7f00-8a11-000000000107"
	nativeExecutorSourceDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	nativeExecutorAttestation  = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	nativeExecutorPlanDigest   = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	nativeExecutorArtifact     = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

func TestPostgresNativeRefreshExecutorBuildsAndRecoversExactSeal(t *testing.T) {
	job := nativeExecutorJob()
	basePlan := nativeExecutorBasePlan(t)
	var events []string
	reader := &nativeExecutorReader{
		snapshot: deploymentnative.DeliveryOperatorSnapshot{TargetID: nativeExecutorTarget, ProjectID: nativeExecutorProject, Environment: nativeExecutorEnvironment, TargetRevision: 9, ActiveGenerationID: nativeExecutorBase},
		plans:    map[string]deploymentnative.DeliveryPlan{nativeExecutorPlan: basePlan},
		events:   &events,
		generations: map[string]deploymentnative.DeliveryGeneration{
			nativeExecutorBase:   {GenerationID: nativeExecutorBase, TargetID: nativeExecutorTarget, PlanID: nativeExecutorPlan, PlanDigest: basePlan.PlanDigest, CandidateID: nativeExecutorCandidate, SnapshotSealID: nativeExecutorSeal},
			nativeExecutorResult: {GenerationID: nativeExecutorResult, TargetID: nativeExecutorTarget, PlanID: nativeExecutorPlan, PlanDigest: nativeExecutorPlanDigest, CandidateID: nativeExecutorCandidate, SnapshotSealID: nativeExecutorSeal, ServingArtifactDigest: nativeExecutorArtifact},
		},
		attempt:   deploymentnative.DeliveryBuildAttempt{AttemptID: nativeExecutorBuild, PlanID: nativeExecutorPlan, CandidateID: nativeExecutorCandidate, State: deploymentnative.AttemptCommitted, SnapshotID: 42},
		seal:      deploymentnative.SnapshotSeal{SealID: nativeExecutorSeal, AttemptID: nativeExecutorBuild, CandidateID: nativeExecutorCandidate, PlanDigest: nativeExecutorPlanDigest, DuckLakeSnapshotID: 42},
		candidate: deploymentnative.DeliveryCandidate{CandidateID: nativeExecutorCandidate, TargetID: nativeExecutorTarget, PlanID: nativeExecutorPlan, AttemptID: nativeExecutorBuild, SnapshotSealID: nativeExecutorSeal, Status: "qualified"},
	}
	mutations := &nativeExecutorMutations{
		events: &events,
		plan:   deploymentmodule.NativeDeliveryPlan{ID: uuid.MustParse(nativeExecutorPlan), ProjectID: job.Identity.ProjectID, TargetID: nativeExecutorTarget, Environment: nativeExecutorEnvironment, Operation: string(deployment.DeliveryOperationRestatement), SourceDigest: nativeExecutorSourceDigest, SourceAttestationDigest: nativeExecutorAttestation, BaseGenerationID: uuid.MustParse(nativeExecutorBase), BaseTargetRevision: 9, PlanDigest: nativeExecutorPlanDigest, Status: "planned"},
		build:  deploymentmodule.NativeDeliveryBuild{ID: uuid.MustParse(nativeExecutorBuild), PlanID: uuid.MustParse(nativeExecutorPlan), PlanDigest: nativeExecutorPlanDigest, SourceDigest: nativeExecutorSourceDigest, BaseGenerationID: uuid.MustParse(nativeExecutorBase), ServingArtifactDigest: nativeExecutorArtifact, WriterLeaseID: uuid.MustParse(nativeExecutorLease), ServingStateID: uuid.MustParse(nativeExecutorResult), SealID: uuid.MustParse(nativeExecutorSeal), CandidateID: uuid.MustParse(nativeExecutorCandidate), Status: "sealed"},
	}
	var received []jobs.AuthorityEnvelope
	var baseCredentialChecks int
	var checkedIdentity projectgraph.ServingIdentity
	var checkedAuthority jobs.AuthorityEnvelope
	encodedWantAuthority, err := jobs.MarshalAuthority(job.Authority)
	if err != nil {
		t.Fatal(err)
	}
	wantAuthority, err := jobs.UnmarshalAuthority(encodedWantAuthority)
	if err != nil {
		t.Fatal(err)
	}
	revalidator := jobs.AuthorityRevalidatorFunc(func(_ context.Context, authority jobs.AuthorityEnvelope) error {
		events = append(events, "authority")
		received = append(received, authority)
		return nil
	})
	checkBaseCredentials := func(_ context.Context, got refreshrun.JobRecord) error {
		events = append(events, "base-credentials")
		baseCredentialChecks++
		checkedIdentity = got.Identity
		checkedAuthority = got.Authority
		if got.Identity != job.Identity || !reflect.DeepEqual(got.Authority, wantAuthority) {
			t.Fatalf("base credential preflight job = %#v, want exact base identity and captured authority", got)
		}
		if got.Authority.Credential == job.Authority.Credential || len(got.Authority.Permissions) == 0 || &got.Authority.Permissions[0] == &job.Authority.Permissions[0] {
			t.Fatal("base credential preflight authority aliases caller-owned credential or permissions")
		}
		return nil
	}
	factory := &nativeExecutorMutationFactory{mutations: mutations, events: &events}
	executor, err := NewPostgresNativeRefreshExecutor(factory, reader, nativeExecutorTarget, revalidator, checkBaseCredentials)
	if err != nil {
		t.Fatalf("construct native refresh executor: %v", err)
	}
	result, err := executor.Execute(t.Context(), job)
	if err != nil {
		t.Fatalf("execute native refresh: %v", err)
	}
	if result != (refreshrun.CanonicalRefreshResult{PlanID: nativeExecutorPlan, ServingStateID: nativeExecutorResult, NativeGenerationID: nativeExecutorResult, SnapshotID: 42}) {
		t.Fatalf("result = %#v", result)
	}
	if mutations.planRequest.IdempotencyKey != "refresh-plan-"+job.RunID || mutations.buildRequest.IdempotencyKey != "refresh-build-"+job.RunID {
		t.Fatalf("idempotency keys = %q/%q", mutations.planRequest.IdempotencyKey, mutations.buildRequest.IdempotencyKey)
	}
	if mutations.planRequest.SourceOwnerID != "source-owner" || mutations.planRequest.SourceDigest != nativeExecutorSourceDigest || mutations.planRequest.SourceAttestationDigest != nativeExecutorAttestation {
		t.Fatalf("plan request source evidence = %#v", mutations.planRequest)
	}
	if !mutations.planCompleted || !mutations.buildCompleted {
		t.Fatalf("native command completion calls = plan:%t build:%t", mutations.planCompleted, mutations.buildCompleted)
	}
	if len(received) != 2 || !reflect.DeepEqual(received[0], job.Authority) || !reflect.DeepEqual(received[1], job.Authority) {
		t.Fatalf("revalidated authorities = %#v, want the exact job authority at both boundaries", received)
	}
	if baseCredentialChecks != 2 || checkedIdentity != job.Identity || !reflect.DeepEqual(checkedAuthority, wantAuthority) {
		t.Fatalf("base credential checks = %d, identity=%#v authority=%#v", baseCredentialChecks, checkedIdentity, checkedAuthority)
	}
	if len(factory.jobs) != 1 {
		t.Fatalf("mutation factory calls = %d, want exactly one job scope", len(factory.jobs))
	}
	mutationJob := factory.jobs[0]
	if mutationJob.PipelinePlan == job.PipelinePlan || mutationJob.PipelinePlan == nil || mutationJob.Authority.Credential == job.Authority.Credential || len(mutationJob.Authority.Permissions) == 0 || &mutationJob.Authority.Permissions[0] == &job.Authority.Permissions[0] {
		t.Fatal("mutation factory received aliased pipeline or queued authority evidence")
	}
	wantEvents := []string{"snapshot", "load-generation", "load-plan", "authority", "base-credentials", "scope-mutations", "create-plan", "complete-plan", "authority", "base-credentials", "build-plan", "complete-build"}
	if len(events) < len(wantEvents) || !reflect.DeepEqual(events[:len(wantEvents)], wantEvents) {
		t.Fatalf("native execution order = %#v, want prefix %#v", events, wantEvents)
	}
}

func TestPostgresNativeRefreshExecutorBaseCredentialPreflightDenialStopsBeforeMutation(t *testing.T) {
	job := nativeExecutorJob()
	reader := nativeExecutorReaderFixture(nativeExecutorBasePlan(t))
	var events []string
	reader.events = &events
	mutations := &nativeExecutorMutations{events: &events}
	denied := errors.New("local credential pin is unsupported with private diagnostics")
	var authorityChecks int
	revalidator := jobs.AuthorityRevalidatorFunc(func(_ context.Context, got jobs.AuthorityEnvelope) error {
		events = append(events, "authority")
		authorityChecks++
		if !reflect.DeepEqual(got, job.Authority) {
			t.Fatalf("revalidated authority = %#v, want exact queued authority", got)
		}
		return nil
	})
	preflight := func(_ context.Context, got refreshrun.JobRecord) error {
		events = append(events, "base-credentials")
		if got.Identity != job.Identity || !reflect.DeepEqual(got.Authority, job.Authority) {
			t.Fatalf("preflight job identity/authority = %#v/%#v, want queued values", got.Identity, got.Authority)
		}
		return denied
	}
	executor, err := NewPostgresNativeRefreshExecutor(nativeExecutorMutationFactoryFor(mutations), reader, nativeExecutorTarget, revalidator, preflight)
	if err != nil {
		t.Fatalf("construct native refresh executor: %v", err)
	}
	_, err = executor.Execute(t.Context(), job)
	if !errors.Is(err, denied) || !errors.Is(err, ErrNativeRefreshBaseCredentialCheck) || strings.Contains(err.Error(), "private diagnostics") {
		t.Fatalf("preflight error = %v, want fixed wrapped denial with matching causes", err)
	}
	if authorityChecks != 1 || mutations.planRequest.IdempotencyKey != "" || mutations.buildRequest.IdempotencyKey != "" || mutations.planCompleted || mutations.buildCompleted {
		t.Fatalf("preflight denial reached native work: authority=%d mutations=%#v", authorityChecks, mutations)
	}
	wantEvents := []string{"snapshot", "load-generation", "load-plan", "authority", "base-credentials"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
}

func TestPostgresNativeRefreshExecutorRechecksBaseCredentialsBeforeBuild(t *testing.T) {
	job := nativeExecutorJob()
	reader := nativeExecutorReaderFixture(nativeExecutorBasePlan(t))
	var events []string
	reader.events = &events
	mutations := &nativeExecutorMutations{
		events: &events,
		plan: deploymentmodule.NativeDeliveryPlan{
			ID: uuid.MustParse(nativeExecutorPlan), ProjectID: job.Identity.ProjectID,
			TargetID: nativeExecutorTarget, Environment: nativeExecutorEnvironment,
			Operation: string(deployment.DeliveryOperationRestatement), SourceDigest: nativeExecutorSourceDigest,
			SourceAttestationDigest: nativeExecutorAttestation, BaseGenerationID: uuid.MustParse(nativeExecutorBase),
			BaseTargetRevision: job.TargetRevision, PlanDigest: nativeExecutorPlanDigest, Status: "planned",
		},
	}
	buildReached := errors.New("build reached without credential preflight")
	mutations.beforeBuild = func(context.Context) error { return buildReached }
	var authorityChecks, credentialChecks int
	revalidator := jobs.AuthorityRevalidatorFunc(func(_ context.Context, got jobs.AuthorityEnvelope) error {
		events = append(events, "authority")
		authorityChecks++
		if !reflect.DeepEqual(got, job.Authority) {
			t.Fatalf("revalidated authority = %#v, want exact queued authority", got)
		}
		return nil
	})
	credentialDrift := errors.New("base credential evidence changed with private diagnostics")
	preflight := func(_ context.Context, got refreshrun.JobRecord) error {
		events = append(events, "base-credentials")
		credentialChecks++
		if got.Identity != job.Identity || !reflect.DeepEqual(got.Authority, job.Authority) {
			t.Fatalf("preflight job identity/authority = %#v/%#v, want queued values", got.Identity, got.Authority)
		}
		if credentialChecks == 2 {
			return credentialDrift
		}
		return nil
	}
	executor, err := NewPostgresNativeRefreshExecutor(nativeExecutorMutationFactoryFor(mutations), reader, nativeExecutorTarget, revalidator, preflight)
	if err != nil {
		t.Fatalf("construct native refresh executor: %v", err)
	}
	_, err = executor.Execute(t.Context(), job)
	if !errors.Is(err, credentialDrift) || !errors.Is(err, ErrNativeRefreshBaseCredentialCheck) || strings.Contains(err.Error(), "private diagnostics") {
		t.Fatalf("second preflight error = %v, want fixed wrapped drift denial", err)
	}
	if authorityChecks != 2 || credentialChecks != 2 || !mutations.planCompleted || mutations.planRequest.IdempotencyKey != "refresh-plan-"+job.RunID {
		t.Fatalf("plan-side state after build-boundary drift: authority=%d credential=%d plan=%#v completed=%t", authorityChecks, credentialChecks, mutations.planRequest, mutations.planCompleted)
	}
	if mutations.buildRequest.IdempotencyKey != "" || mutations.buildCompleted {
		t.Fatalf("credential drift reached native build: request=%#v completed=%t", mutations.buildRequest, mutations.buildCompleted)
	}
	wantEvents := []string{"snapshot", "load-generation", "load-plan", "authority", "base-credentials", "create-plan", "complete-plan", "authority", "base-credentials"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
}

func TestPostgresNativeRefreshExecutorRequiresBaseCredentialCheck(t *testing.T) {
	reader := nativeExecutorReaderFixture(nativeExecutorBasePlan(t))
	mutations := &nativeExecutorMutations{}
	if _, err := NewPostgresNativeRefreshExecutor(nativeExecutorMutationFactoryFor(mutations), reader, nativeExecutorTarget, nativeExecutorAllowAuthority(), nil); !errors.Is(err, ErrNativeRefreshBaseCredentialCheck) {
		t.Fatalf("constructor with missing base credential check = %v, want required-check error", err)
	}
	var events []string
	reader.events = &events
	mutations.events = &events
	executor := &PostgresNativeRefreshExecutor{MutationFactory: nativeExecutorMutationFactoryFor(mutations), Reader: reader, TargetID: nativeExecutorTarget, AuthorityRevalidator: nativeExecutorAllowAuthority()}
	if _, err := executor.Execute(t.Context(), nativeExecutorJob()); !errors.Is(err, ErrNativeRefreshBaseCredentialCheck) {
		t.Fatalf("direct executor with missing base credential check = %v, want required-check error", err)
	}
	if len(events) != 0 || mutations.planRequest.IdempotencyKey != "" || mutations.buildRequest.IdempotencyKey != "" {
		t.Fatalf("missing check performed work: events=%#v mutations=%#v", events, mutations)
	}
}

func TestPostgresNativeRefreshExecutorRequiresMutationFactory(t *testing.T) {
	reader := nativeExecutorReaderFixture(nativeExecutorBasePlan(t))
	if _, err := NewPostgresNativeRefreshExecutor(nil, reader, nativeExecutorTarget, nativeExecutorAllowAuthority(), nativeExecutorAllowBaseCredentials); err == nil {
		t.Fatal("constructor accepted missing job-scoped mutation factory")
	}
	var typedNil *nativeExecutorMutationFactory
	if _, err := NewPostgresNativeRefreshExecutor(typedNil, reader, nativeExecutorTarget, nativeExecutorAllowAuthority(), nativeExecutorAllowBaseCredentials); err == nil {
		t.Fatal("constructor accepted typed-nil job-scoped mutation factory")
	}
}

func TestPostgresNativeRefreshExecutorMutationFactoryDenialStopsBeforePlan(t *testing.T) {
	reader := nativeExecutorReaderFixture(nativeExecutorBasePlan(t))
	mutations := &nativeExecutorMutations{}
	factoryErr := errors.New("private job-scope diagnostics")
	factory := &nativeExecutorMutationFactory{mutations: mutations, err: factoryErr}
	executor, err := NewPostgresNativeRefreshExecutor(factory, reader, nativeExecutorTarget, nativeExecutorAllowAuthority(), nativeExecutorAllowBaseCredentials)
	if err != nil {
		t.Fatalf("construct native refresh executor: %v", err)
	}
	_, err = executor.Execute(t.Context(), nativeExecutorJob())
	if !errors.Is(err, factoryErr) || mutations.planRequest.IdempotencyKey != "" {
		t.Fatalf("scope failure = %v; plan request = %#v", err, mutations.planRequest)
	}
}

func TestPostgresNativeRefreshExecutorRejectsChangedActiveBase(t *testing.T) {
	job := nativeExecutorJob()
	reader := &nativeExecutorReader{snapshot: deploymentnative.DeliveryOperatorSnapshot{TargetID: nativeExecutorTarget, ProjectID: nativeExecutorProject, Environment: nativeExecutorEnvironment, TargetRevision: 9, ActiveGenerationID: nativeExecutorResult}}
	mutations := &nativeExecutorMutations{}
	executor, err := NewPostgresNativeRefreshExecutor(nativeExecutorMutationFactoryFor(mutations), reader, nativeExecutorTarget, nativeExecutorAllowAuthority(), nativeExecutorAllowBaseCredentials)
	if err != nil {
		t.Fatalf("construct native refresh executor: %v", err)
	}
	_, err = executor.Execute(t.Context(), job)
	if !errors.Is(err, refreshrun.ErrRunStale) {
		t.Fatalf("error = %v, want stale run", err)
	}
	if mutations.planRequest.IdempotencyKey != "" || mutations.buildRequest.IdempotencyKey != "" {
		t.Fatal("changed active base reached native mutation")
	}
}

func TestPostgresNativeRefreshExecutorRejectsChangedTargetFence(t *testing.T) {
	job := nativeExecutorJob()
	reader := &nativeExecutorReader{snapshot: deploymentnative.DeliveryOperatorSnapshot{TargetID: nativeExecutorTarget, ProjectID: nativeExecutorProject, Environment: nativeExecutorEnvironment, TargetRevision: job.TargetRevision + 1, ActiveGenerationID: nativeExecutorBase}}
	mutations := &nativeExecutorMutations{}
	executor, err := NewPostgresNativeRefreshExecutor(nativeExecutorMutationFactoryFor(mutations), reader, nativeExecutorTarget, nativeExecutorAllowAuthority(), nativeExecutorAllowBaseCredentials)
	if err != nil {
		t.Fatalf("construct native refresh executor: %v", err)
	}
	_, err = executor.Execute(t.Context(), job)
	if !errors.Is(err, refreshrun.ErrRunStale) {
		t.Fatalf("error = %v, want stale run", err)
	}
	if mutations.planRequest.IdempotencyKey != "" {
		t.Fatal("changed target revision reached native planning")
	}
}

func TestPostgresNativeRefreshExecutorPropagatesPlanCompletionFailure(t *testing.T) {
	job := nativeExecutorJob()
	basePlan := nativeExecutorBasePlan(t)
	reader := nativeExecutorReaderFixture(basePlan)
	mutations := &nativeExecutorMutations{plan: deploymentmodule.NativeDeliveryPlan{ID: uuid.MustParse(nativeExecutorPlan), ProjectID: job.Identity.ProjectID, TargetID: nativeExecutorTarget, Environment: nativeExecutorEnvironment, Operation: string(deployment.DeliveryOperationRestatement), SourceDigest: nativeExecutorSourceDigest, SourceAttestationDigest: nativeExecutorAttestation, BaseGenerationID: uuid.MustParse(nativeExecutorBase), BaseTargetRevision: job.TargetRevision, PlanDigest: nativeExecutorPlanDigest, Status: "planned"}, planCompletionErr: errors.New("plan evidence mismatch")}
	executor, err := NewPostgresNativeRefreshExecutor(nativeExecutorMutationFactoryFor(mutations), reader, nativeExecutorTarget, nativeExecutorAllowAuthority(), nativeExecutorAllowBaseCredentials)
	if err != nil {
		t.Fatalf("construct native refresh executor: %v", err)
	}
	_, err = executor.Execute(t.Context(), job)
	if err == nil || !strings.Contains(err.Error(), "plan evidence mismatch") {
		t.Fatalf("error = %v, want plan completion error", err)
	}
	if mutations.buildRequest.IdempotencyKey != "" {
		t.Fatal("plan completion failure reached native build")
	}
}

func TestPostgresNativeRefreshExecutorRevalidatesBeforePlanAndStopsOnDenial(t *testing.T) {
	job := nativeExecutorJob()
	reader := nativeExecutorReaderFixture(nativeExecutorBasePlan(t))
	var events []string
	reader.events = &events
	mutations := &nativeExecutorMutations{events: &events}
	denied := errors.New("authority revoked")
	var received jobs.AuthorityEnvelope
	revalidator := jobs.AuthorityRevalidatorFunc(func(_ context.Context, authority jobs.AuthorityEnvelope) error {
		events = append(events, "authority")
		received = authority
		return denied
	})
	executor, err := NewPostgresNativeRefreshExecutor(nativeExecutorMutationFactoryFor(mutations), reader, nativeExecutorTarget, revalidator, nativeExecutorAllowBaseCredentials)
	if err != nil {
		t.Fatalf("construct native refresh executor: %v", err)
	}
	_, err = executor.Execute(t.Context(), job)
	if mutations.planRequest.IdempotencyKey != "" || mutations.buildRequest.IdempotencyKey != "" || mutations.planCompleted || mutations.buildCompleted {
		t.Fatalf("plan denial reached mutations: plan=%q build=%q completed=%t/%t", mutations.planRequest.IdempotencyKey, mutations.buildRequest.IdempotencyKey, mutations.planCompleted, mutations.buildCompleted)
	}
	if !errors.Is(err, denied) || !strings.Contains(err.Error(), "plan") || strings.Contains(err.Error(), "fingerprint-refresh") {
		t.Fatalf("execute error = %v, want plan-boundary denial", err)
	}
	if !reflect.DeepEqual(received, job.Authority) {
		t.Fatalf("revalidated authority = %#v, want exact job authority", received)
	}
	wantEvents := []string{"snapshot", "load-generation", "load-plan", "authority"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
}

func TestPostgresNativeRefreshExecutorRevalidatesAgainBeforeBuild(t *testing.T) {
	job := nativeExecutorJob()
	basePlan := nativeExecutorBasePlan(t)
	reader := nativeExecutorReaderFixture(basePlan)
	var events []string
	reader.events = &events
	mutations := &nativeExecutorMutations{
		events: &events,
		plan:   deploymentmodule.NativeDeliveryPlan{ID: uuid.MustParse(nativeExecutorPlan), ProjectID: job.Identity.ProjectID, TargetID: nativeExecutorTarget, Environment: nativeExecutorEnvironment, Operation: string(deployment.DeliveryOperationRestatement), SourceDigest: nativeExecutorSourceDigest, SourceAttestationDigest: nativeExecutorAttestation, BaseGenerationID: uuid.MustParse(nativeExecutorBase), BaseTargetRevision: job.TargetRevision, PlanDigest: nativeExecutorPlanDigest, Status: "planned"},
	}
	denied := errors.New("authority revoked after planning")
	var received []jobs.AuthorityEnvelope
	revalidator := jobs.AuthorityRevalidatorFunc(func(_ context.Context, authority jobs.AuthorityEnvelope) error {
		events = append(events, "authority")
		received = append(received, authority)
		if len(received) == 2 {
			return denied
		}
		return nil
	})
	executor, err := NewPostgresNativeRefreshExecutor(nativeExecutorMutationFactoryFor(mutations), reader, nativeExecutorTarget, revalidator, nativeExecutorAllowBaseCredentials)
	if err != nil {
		t.Fatalf("construct native refresh executor: %v", err)
	}
	_, err = executor.Execute(t.Context(), job)
	if !errors.Is(err, denied) || !strings.Contains(err.Error(), "build") {
		t.Fatalf("execute error = %v, want build-boundary denial", err)
	}
	if len(received) != 2 || !reflect.DeepEqual(received[0], job.Authority) || !reflect.DeepEqual(received[1], job.Authority) {
		t.Fatalf("revalidated authorities = %#v, want exact job authority twice", received)
	}
	if !mutations.planCompleted || mutations.buildRequest.IdempotencyKey != "" || mutations.buildCompleted {
		t.Fatalf("build denial mutation state: build=%q completed=%t/%t", mutations.buildRequest.IdempotencyKey, mutations.planCompleted, mutations.buildCompleted)
	}
	wantEvents := []string{"snapshot", "load-generation", "load-plan", "authority", "create-plan", "complete-plan", "authority"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
}

func TestPostgresNativeRefreshExecutorChecksCancellationAroundRevalidation(t *testing.T) {
	t.Run("already canceled before revalidation", func(t *testing.T) {
		job := nativeExecutorJob()
		reader := nativeExecutorReaderFixture(nativeExecutorBasePlan(t))
		mutations := &nativeExecutorMutations{}
		authorityCalls := 0
		revalidator := jobs.AuthorityRevalidatorFunc(func(context.Context, jobs.AuthorityEnvelope) error {
			authorityCalls++
			return nil
		})
		executor, err := NewPostgresNativeRefreshExecutor(nativeExecutorMutationFactoryFor(mutations), reader, nativeExecutorTarget, revalidator, nativeExecutorAllowBaseCredentials)
		if err != nil {
			t.Fatalf("construct native refresh executor: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err = executor.Execute(ctx, job)
		if !errors.Is(err, context.Canceled) || authorityCalls != 0 || mutations.planRequest.IdempotencyKey != "" {
			t.Fatalf("canceled execution = %v, calls=%d, plan=%q", err, authorityCalls, mutations.planRequest.IdempotencyKey)
		}
	})

	t.Run("canceled by revalidator that ignores context", func(t *testing.T) {
		job := nativeExecutorJob()
		reader := nativeExecutorReaderFixture(nativeExecutorBasePlan(t))
		mutations := &nativeExecutorMutations{}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		revalidator := jobs.AuthorityRevalidatorFunc(func(context.Context, jobs.AuthorityEnvelope) error {
			cancel()
			return nil
		})
		executor, err := NewPostgresNativeRefreshExecutor(nativeExecutorMutationFactoryFor(mutations), reader, nativeExecutorTarget, revalidator, nativeExecutorAllowBaseCredentials)
		if err != nil {
			t.Fatalf("construct native refresh executor: %v", err)
		}
		_, err = executor.Execute(ctx, job)
		if !errors.Is(err, context.Canceled) || mutations.planRequest.IdempotencyKey != "" {
			t.Fatalf("execution after revalidator cancellation = %v, plan=%q", err, mutations.planRequest.IdempotencyKey)
		}
	})
}

func TestPostgresNativeRefreshExecutorRequiresAuthorityRevalidator(t *testing.T) {
	reader := nativeExecutorReaderFixture(nativeExecutorBasePlan(t))
	mutations := &nativeExecutorMutations{}
	if _, err := NewPostgresNativeRefreshExecutor(nativeExecutorMutationFactoryFor(mutations), reader, nativeExecutorTarget, nil, nativeExecutorAllowBaseCredentials); !errors.Is(err, jobs.ErrAuthorityRevalidator) {
		t.Fatalf("constructor with missing revalidator = %v, want required-authority error", err)
	}
	var typedNil jobs.AuthorityRevalidator = jobs.AuthorityRevalidatorFunc(nil)
	if _, err := NewPostgresNativeRefreshExecutor(nativeExecutorMutationFactoryFor(mutations), reader, nativeExecutorTarget, typedNil, nativeExecutorAllowBaseCredentials); !errors.Is(err, jobs.ErrAuthorityRevalidator) {
		t.Fatalf("constructor with typed-nil revalidator = %v, want required-authority error", err)
	}
	for name, revalidator := range map[string]jobs.AuthorityRevalidator{"missing": nil, "typed nil": typedNil} {
		t.Run(name, func(t *testing.T) {
			var events []string
			localReader := nativeExecutorReaderFixture(nativeExecutorBasePlan(t))
			localReader.events = &events
			localMutations := &nativeExecutorMutations{events: &events}
			executor := &PostgresNativeRefreshExecutor{MutationFactory: nativeExecutorMutationFactoryFor(localMutations), Reader: localReader, TargetID: nativeExecutorTarget, AuthorityRevalidator: revalidator, BaseCredentialCheck: nativeExecutorAllowBaseCredentials}
			_, err := executor.Execute(t.Context(), nativeExecutorJob())
			if !errors.Is(err, jobs.ErrAuthorityRevalidator) {
				t.Fatalf("direct executor error = %v, want required-authority error", err)
			}
			if len(events) != 0 || localMutations.planRequest.IdempotencyKey != "" {
				t.Fatalf("unconfigured executor performed work: events=%#v mutations=%#v", events, localMutations)
			}
		})
	}
}

func TestPostgresNativeRefreshExecutorRejectsAuthorityForAnotherInstance(t *testing.T) {
	job := nativeExecutorJob()
	job.Authority.Target.InstanceID = "another-instance"
	reader := nativeExecutorReaderFixture(nativeExecutorBasePlan(t))
	mutations := &nativeExecutorMutations{}
	executor, err := NewPostgresNativeRefreshExecutor(nativeExecutorMutationFactoryFor(mutations), reader, nativeExecutorTarget, nativeExecutorAllowAuthority(), nativeExecutorAllowBaseCredentials)
	if err != nil {
		t.Fatalf("construct native refresh executor: %v", err)
	}
	_, err = executor.Execute(t.Context(), job)
	if !errors.Is(err, deployment.ErrDeliveryInvalid) {
		t.Fatalf("execute error = %v, want invalid authority target", err)
	}
	if mutations.planRequest.IdempotencyKey != "" || mutations.buildRequest.IdempotencyKey != "" {
		t.Fatal("wrong-instance authority reached native mutations")
	}
}

func nativeExecutorJob() refreshrun.JobRecord {
	plan, _ := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline-plan", PipelineID: "pipeline-prod", ProjectID: nativeExecutorProject, Environment: nativeExecutorEnvironment, SemanticModelID: "semantic-prod", ServingGenerationID: nativeExecutorBase,
		ArtifactDigest: nativeExecutorSourceDigest, SelectionDigest: nativeExecutorPlanDigest, MaterializationScope: []string{"model-prod"}, QualificationChecks: []string{"compatibility"},
	})
	return refreshrun.JobRecord{ID: "job-refresh", Identity: projectgraph.ServingIdentity{ProjectID: nativeExecutorProject, Environment: nativeExecutorEnvironment, GenerationID: nativeExecutorBase}, SemanticModelID: "semantic-prod", PipelineID: "pipeline-prod", PipelinePlan: &plan, PrincipalID: "principal-refresh", RunID: "run-refresh", TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline-prod", TargetRevision: 9, TriggerType: refreshrun.TriggerManual, Kind: refreshrun.JobKindRefreshPipeline, EstimatedMemoryBytes: 1, Authority: nativeExecutorAuthority()}
}

func nativeExecutorAuthority() jobs.AuthorityEnvelope {
	pair, err := permissions.NewExactPair("leapview.permissions/v1", permissions.Action("pipeline.run"), nativeExecutorProject, "pipeline", "pipeline-prod")
	if err != nil {
		panic(err)
	}
	return jobs.AuthorityEnvelope{
		Profile: jobs.AuthorityEnvelopeProfile, Mode: jobs.CallerAuthorityMode,
		ActorPrincipalID: "principal-refresh", ExecutionPrincipalID: "principal-refresh",
		Credential:  &jobs.CredentialEvidence{Class: jobs.CredentialClassAPIToken, ID: "token-refresh", Fingerprint: "fingerprint-refresh", ExpiresAt: time.Now().UTC().Add(time.Hour)},
		Target:      jobs.AuthorityTarget{InstanceID: nativeExecutorTarget, ProjectID: nativeExecutorProject, Environment: nativeExecutorEnvironment, ResourceKind: "pipeline", ResourceID: "pipeline-prod"},
		Permissions: []permissions.Pair{pair},
	}
}

func nativeExecutorAllowAuthority() jobs.AuthorityRevalidator {
	return jobs.AuthorityRevalidatorFunc(func(context.Context, jobs.AuthorityEnvelope) error { return nil })
}

func nativeExecutorAllowBaseCredentials(context.Context, refreshrun.JobRecord) error {
	return nil
}

func nativeExecutorBasePlan(t *testing.T) deploymentnative.DeliveryPlan {
	now := time.Now().UTC().Truncate(time.Microsecond)
	rich, err := deployment.NewDeliveryPlan(deployment.DeliveryPlan{
		ID: nativeExecutorPlan, ActorID: "source-owner", SourceOwnerID: "source-owner", TargetID: nativeExecutorTarget, ProjectID: nativeExecutorProject, Environment: nativeExecutorEnvironment,
		Operation: deployment.DeliveryOperationCodeChange, SourceDigest: nativeExecutorSourceDigest, ServingArtifactDigest: nativeExecutorArtifact, BaseGenerationID: nativeExecutorBase, BaseTargetRevision: 9,
		Execution:  deployment.DeliveryExecutionInputs{SourceArtifactDigest: nativeExecutorSourceDigest, CompilerDigest: nativeExecutorPlanDigest, ExecutableDigest: nativeExecutorPlanDigest, DependencyDigest: nativeExecutorPlanDigest, ConfigDigest: nativeExecutorPlanDigest, BindingDigest: nativeExecutorPlanDigest, RuntimeDigest: nativeExecutorPlanDigest, CapabilityDigest: nativeExecutorPlanDigest},
		Provenance: deployment.DeliveryProvenance{AttestationDigest: nativeExecutorAttestation},
		Governance: deployment.DeliveryGovernance{PolicyDigest: nativeExecutorPlanDigest, AuthorizationDigest: nativeExecutorPlanDigest, QualificationDigest: nativeExecutorPlanDigest, ApprovalPolicyRevision: 1, ExpiresAt: now.Add(time.Hour)},
		Evidence:   deployment.DeliveryPlanEvidence{ImpactStatement: "impact", PhysicalWorkStatement: "physical", ReuseStatement: "reuse", Qualification: deployment.DeliveryQualificationEvidence{Policy: "default", Steps: []deployment.DeliveryQualificationStep{{ID: "compatibility", Kind: "compatibility", Description: "compatibility"}}}, StalePolicy: deployment.DeliveryStalePolicy{Mode: "reject"}, Rollback: deployment.DeliveryRollbackEvidence{Class: deployment.DeliveryServingSafe}},
		CreatedAt:  now,
	})
	if err != nil {
		t.Fatalf("construct base plan: %v", err)
	}
	document, err := json.Marshal(rich)
	if err != nil {
		t.Fatal(err)
	}
	return deploymentnative.DeliveryPlan{PlanID: rich.ID, TargetID: nativeExecutorTarget, PlanDigest: rich.Digest, CompiledConfigDigest: rich.Execution.ConfigDigest, SecurityDomainFingerprint: rich.Governance.AuthorizationDigest, ArtifactDigest: rich.ServingArtifactDigest, QualificationDigest: rich.Governance.QualificationDigest, ApprovalRequired: rich.Governance.RequiresApproval, ApprovalPolicyRevision: rich.Governance.ApprovalPolicyRevision, PlanDocument: document}
}

type nativeExecutorMutations struct {
	plan                                  deploymentmodule.NativeDeliveryPlan
	build                                 deploymentmodule.NativeDeliveryBuild
	planRequest                           deploymentmodule.NativeDeliveryPlanRequest
	buildRequest                          deploymentmodule.NativeDeliveryBuildRequest
	planCompleted, buildCompleted         bool
	planCompletionErr, buildCompletionErr error
	events                                *[]string
	beforeBuild                           func(context.Context) error
}

type nativeExecutorMutationFactory struct {
	mutations NativeRefreshDeliveryMutations
	jobs      []refreshrun.JobRecord
	events    *[]string
	err       error
}

func (factory *nativeExecutorMutationFactory) ForRefresh(_ context.Context, job refreshrun.JobRecord) (NativeRefreshDeliveryMutations, error) {
	if factory.events != nil {
		*factory.events = append(*factory.events, "scope-mutations")
	}
	factory.jobs = append(factory.jobs, job)
	return factory.mutations, factory.err
}

func nativeExecutorMutationFactoryFor(mutations NativeRefreshDeliveryMutations) *nativeExecutorMutationFactory {
	return &nativeExecutorMutationFactory{mutations: mutations}
}

func (m *nativeExecutorMutations) CreatePlan(_ context.Context, request deploymentmodule.NativeDeliveryPlanRequest) (deploymentmodule.NativeDeliveryPlan, error) {
	if m.events != nil {
		*m.events = append(*m.events, "create-plan")
	}
	m.planRequest = request
	return m.plan, nil
}

func (m *nativeExecutorMutations) BuildPlan(ctx context.Context, request deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuild, error) {
	if m.events != nil {
		*m.events = append(*m.events, "build-plan")
	}
	m.buildRequest = request
	if m.beforeBuild != nil {
		if err := m.beforeBuild(ctx); err != nil {
			return deploymentmodule.NativeDeliveryBuild{}, err
		}
	}
	return m.build, nil
}

func (m *nativeExecutorMutations) CompleteNativePlanCommand(context.Context, deploymentmodule.NativeDeliveryPlan) error {
	if m.events != nil {
		*m.events = append(*m.events, "complete-plan")
	}
	m.planCompleted = true
	return m.planCompletionErr
}

func (m *nativeExecutorMutations) CompleteNativeBuildCommand(context.Context, deploymentmodule.NativeDeliveryBuild) error {
	if m.events != nil {
		*m.events = append(*m.events, "complete-build")
	}
	m.buildCompleted = true
	return m.buildCompletionErr
}

type nativeExecutorReader struct {
	snapshot    deploymentnative.DeliveryOperatorSnapshot
	plans       map[string]deploymentnative.DeliveryPlan
	generations map[string]deploymentnative.DeliveryGeneration
	attempt     deploymentnative.DeliveryBuildAttempt
	seal        deploymentnative.SnapshotSeal
	candidate   deploymentnative.DeliveryCandidate
	events      *[]string
}

func (r *nativeExecutorReader) OperatorSnapshot(context.Context, string) (deploymentnative.DeliveryOperatorSnapshot, error) {
	if r.events != nil {
		*r.events = append(*r.events, "snapshot")
	}
	return r.snapshot, nil
}
func (r *nativeExecutorReader) LoadPlan(_ context.Context, id string) (deploymentnative.DeliveryPlan, error) {
	if r.events != nil {
		*r.events = append(*r.events, "load-plan")
	}
	plan, ok := r.plans[id]
	if !ok {
		return deploymentnative.DeliveryPlan{}, deploymentnative.ErrNotFound
	}
	return plan, nil
}
func (r *nativeExecutorReader) LoadBuildAttempt(context.Context, string) (deploymentnative.DeliveryBuildAttempt, error) {
	return r.attempt, nil
}
func (r *nativeExecutorReader) LoadSnapshotSeal(context.Context, string) (deploymentnative.SnapshotSeal, error) {
	return r.seal, nil
}
func (r *nativeExecutorReader) LoadCandidate(context.Context, string) (deploymentnative.DeliveryCandidate, error) {
	return r.candidate, nil
}
func (r *nativeExecutorReader) LoadGeneration(_ context.Context, id string) (deploymentnative.DeliveryGeneration, error) {
	if r.events != nil {
		*r.events = append(*r.events, "load-generation")
	}
	generation, ok := r.generations[id]
	if !ok {
		return deploymentnative.DeliveryGeneration{}, deploymentnative.ErrNotFound
	}
	return generation, nil
}

var _ NativeRefreshDeliveryReader = (*nativeExecutorReader)(nil)

func nativeExecutorReaderFixture(basePlan deploymentnative.DeliveryPlan) *nativeExecutorReader {
	return &nativeExecutorReader{
		snapshot: deploymentnative.DeliveryOperatorSnapshot{TargetID: nativeExecutorTarget, ProjectID: nativeExecutorProject, Environment: nativeExecutorEnvironment, TargetRevision: 9, ActiveGenerationID: nativeExecutorBase},
		plans:    map[string]deploymentnative.DeliveryPlan{nativeExecutorPlan: basePlan},
		generations: map[string]deploymentnative.DeliveryGeneration{
			nativeExecutorBase: {GenerationID: nativeExecutorBase, TargetID: nativeExecutorTarget, PlanID: nativeExecutorPlan, PlanDigest: basePlan.PlanDigest, CandidateID: nativeExecutorCandidate, SnapshotSealID: nativeExecutorSeal},
		},
	}
}

func TestNativeExecutorConstantsRemainCanonical(t *testing.T) {
	for _, id := range []string{nativeExecutorBase, nativeExecutorPlan, nativeExecutorBuild, nativeExecutorCandidate, nativeExecutorSeal, nativeExecutorResult, nativeExecutorLease} {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.String() != strings.TrimSpace(id) || parsed.Version() != 7 {
			t.Fatalf("id %q is not UUIDv7: %v", id, err)
		}
	}
}

func TestPostgresNativeRefreshExecutorRevalidatesCapturedAuthorityInsideSourceWork(t *testing.T) {
	job := nativeExecutorJob()
	encoded, err := jobs.MarshalAuthority(job.Authority)
	if err != nil {
		t.Fatal(err)
	}
	wantAuthority, err := jobs.UnmarshalAuthority(encoded)
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("revoked authority with private diagnostics")
	var calls int
	checker := jobs.AuthorityRevalidatorFunc(func(_ context.Context, got jobs.AuthorityEnvelope) error {
		calls++
		if !reflect.DeepEqual(got, wantAuthority) {
			t.Fatal("source work must recheck the exact captured job authority")
		}
		if calls > 2 {
			return denied
		}
		return nil
	})
	mutations := &nativeExecutorMutations{
		plan: deploymentmodule.NativeDeliveryPlan{ID: uuid.MustParse(nativeExecutorPlan), ProjectID: job.Identity.ProjectID, TargetID: nativeExecutorTarget, Environment: nativeExecutorEnvironment, Operation: string(deployment.DeliveryOperationRestatement), SourceDigest: nativeExecutorSourceDigest, SourceAttestationDigest: nativeExecutorAttestation, BaseGenerationID: uuid.MustParse(nativeExecutorBase), BaseTargetRevision: job.TargetRevision, PlanDigest: nativeExecutorPlanDigest, Status: "planned"},
	}
	executor, err := NewPostgresNativeRefreshExecutor(nativeExecutorMutationFactoryFor(mutations), nativeExecutorReaderFixture(nativeExecutorBasePlan(t)), nativeExecutorTarget, checker, nativeExecutorAllowBaseCredentials)
	if err != nil {
		t.Fatal(err)
	}
	mutations.beforeBuild = func(ctx context.Context) error {
		// Neither caller-owned envelope aliases nor a reassigned executor checker
		// may replace the authority captured for this build's source work.
		job.Authority.Credential.ID = "replacement-token"
		job.Authority.Permissions[0].Action = "connection.use"
		executor.AuthorityRevalidator = nativeExecutorAllowAuthority()
		return sourcework.Revalidate(ctx)
	}
	_, err = executor.Execute(t.Context(), job)
	if !errors.Is(err, denied) || calls != 3 {
		t.Fatalf("source boundary error = %v, checks = %d; want captured authority denial after plan/build checks", err, calls)
	}
	if strings.Contains(err.Error(), "private diagnostics") || mutations.buildCompleted {
		t.Fatalf("source denial leaked diagnostics or completed build: %v", err)
	}
}
