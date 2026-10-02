package run

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/pkg/jobs"
	"github.com/flidai/leapview/pkg/permissions"
)

func TestServiceExecuteClaimedJobRevalidatesProtectedBoundaries(t *testing.T) {
	repo := newFakeRepo()
	pair, err := permissions.NewExactPair("leapview.permissions/v1", permissions.Action("pipeline.run"), "project", "pipeline", "sales-refresh")
	if err != nil {
		t.Fatal(err)
	}
	var boundaries []string
	authorityCalls := 0
	service := Service{
		Runs: repo,
		AuthorityRevalidator: jobs.AuthorityRevalidatorFunc(func(_ context.Context, _ jobs.AuthorityEnvelope) error {
			authorityCalls++
			return nil
		}),
		CanonicalExecutor: func(context.Context, JobRecord) (CanonicalRefreshResult, error) {
			boundaries = append(boundaries, "executor")
			return CanonicalRefreshResult{PlanID: "plan-refresh", ServingStateID: "generation-refresh"}, nil
		},
		Publication: fakePublication{repo: repo},
		CanonicalResultReconciler: func(context.Context, JobRecord, CanonicalRefreshResult) error {
			boundaries = append(boundaries, "output")
			return nil
		},
	}
	job := JobRecord{
		ID: "job-boundary", Identity: serviceIdentity, PrincipalID: "principal:test", EstimatedMemoryBytes: 64 << 20,
		RunID: "run_root", SemanticModelID: "sales", PipelineID: "sales-refresh", PipelinePlan: testPipelinePlan(serviceIdentity, "sales-refresh", "sales"),
		TargetType: TargetRefreshPipeline, TargetID: "sales-refresh", TriggerType: TriggerManual, Kind: JobKindRefreshPipeline, LeaseOwner: "worker", LeaseRevision: 1,
		Authority: jobs.AuthorityEnvelope{
			Profile: jobs.AuthorityEnvelopeProfile, Mode: jobs.CallerAuthorityMode, ActorPrincipalID: "principal:test", ExecutionPrincipalID: "principal:test",
			Credential: &jobs.CredentialEvidence{Class: jobs.CredentialClassAPIToken, ID: "token:test", Fingerprint: "fingerprint:test", ExpiresAt: time.Now().UTC().Add(time.Hour)},
			Target:     jobs.AuthorityTarget{ProjectID: "project", Environment: "dev", ResourceKind: "pipeline", ResourceID: "sales-refresh"}, Permissions: []permissions.Pair{pair},
		},
	}
	if err := service.ExecuteClaimedJob(t.Context(), job); err != nil {
		t.Fatalf("execute claimed job: %v", err)
	}
	if len(boundaries) != 2 || boundaries[0] != "executor" || boundaries[1] != "output" {
		t.Fatalf("protected boundary execution = %#v", boundaries)
	}
	if authorityCalls != 4 {
		t.Fatalf("authority boundary calls = %d, want prepare/execute/publish/output", authorityCalls)
	}
}

func TestServiceExecuteClaimedJobFailsClosedWithoutBoundaryRevalidator(t *testing.T) {
	service := Service{Runs: newFakeRepo(), RequireAuthority: true, CanonicalExecutor: func(context.Context, JobRecord) (CanonicalRefreshResult, error) { return CanonicalRefreshResult{}, nil }}
	err := service.ExecuteClaimedJob(t.Context(), JobRecord{ID: "job-no-authority", Identity: serviceIdentity, PrincipalID: "principal:test", EstimatedMemoryBytes: 1, RunID: "run_root", SemanticModelID: "sales", PipelineID: "sales-refresh", PipelinePlan: testPipelinePlan(serviceIdentity, "sales-refresh", "sales"), TargetType: TargetRefreshPipeline, TargetID: "sales-refresh", TriggerType: TriggerManual, Kind: JobKindRefreshPipeline, LeaseOwner: "worker", LeaseRevision: 1})
	if err == nil || !strings.Contains(err.Error(), "authority is required before prepare boundary") {
		t.Fatalf("missing boundary authority error = %v", err)
	}
}

func TestPipelineAuthorityTargetMustMatchQueuedRun(t *testing.T) {
	pair, err := permissions.NewExactPair("leapview.permissions/v1", permissions.Action("pipeline.run"), "project", "pipeline", "sales-refresh")
	if err != nil {
		t.Fatal(err)
	}
	authority := jobs.AuthorityEnvelope{
		Profile: jobs.AuthorityEnvelopeProfile, Mode: jobs.CallerAuthorityMode, ActorPrincipalID: "principal:test", ExecutionPrincipalID: "principal:test",
		Credential: &jobs.CredentialEvidence{Class: jobs.CredentialClassAPIToken, ID: "token:test", Fingerprint: "fingerprint:test", ExpiresAt: time.Now().UTC().Add(time.Hour)},
		Target:     jobs.AuthorityTarget{ProjectID: "project", Environment: "dev", ResourceKind: "pipeline", ResourceID: "sales-refresh"}, Permissions: []permissions.Pair{pair},
	}
	if err := validatePipelineAuthorityTarget(authority, serviceIdentity, "sales-refresh", "principal:test"); err != nil {
		t.Fatalf("matching target rejected: %v", err)
	}
	for name, mutate := range map[string]func(*jobs.AuthorityEnvelope){
		"project":     func(a *jobs.AuthorityEnvelope) { a.Target.ProjectID = "other" },
		"environment": func(a *jobs.AuthorityEnvelope) { a.Target.Environment = "prod" },
		"pipeline":    func(a *jobs.AuthorityEnvelope) { a.Target.ResourceID = "other" },
		"principal":   func(a *jobs.AuthorityEnvelope) { a.ExecutionPrincipalID = "principal:other" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := authority
			mutate(&candidate)
			if err := validatePipelineAuthorityTarget(candidate, serviceIdentity, "sales-refresh", "principal:test"); err == nil {
				t.Fatal("mismatched queue authority was accepted")
			}
		})
	}
}

func TestServiceExecuteClaimedJobRevalidatesOutputWithoutReconciler(t *testing.T) {
	repo := newFakeRepo()
	pair, err := permissions.NewExactPair("leapview.permissions/v1", permissions.Action("pipeline.run"), "project", "pipeline", "sales-refresh")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	service := Service{
		Runs:                 repo,
		AuthorityRevalidator: jobs.AuthorityRevalidatorFunc(func(context.Context, jobs.AuthorityEnvelope) error { calls++; return nil }),
		CanonicalExecutor: func(context.Context, JobRecord) (CanonicalRefreshResult, error) {
			return CanonicalRefreshResult{PlanID: "plan-refresh", ServingStateID: "generation-refresh"}, nil
		},
		Publication: fakePublication{repo: repo},
	}
	job := JobRecord{
		ID: "job-output", Identity: serviceIdentity, PrincipalID: "principal:test", EstimatedMemoryBytes: 1,
		RunID: "run_root", SemanticModelID: "sales", PipelineID: "sales-refresh", PipelinePlan: testPipelinePlan(serviceIdentity, "sales-refresh", "sales"),
		TargetType: TargetRefreshPipeline, TargetID: "sales-refresh", TriggerType: TriggerManual, Kind: JobKindRefreshPipeline, LeaseOwner: "worker", LeaseRevision: 1,
		Authority: jobs.AuthorityEnvelope{
			Profile: jobs.AuthorityEnvelopeProfile, Mode: jobs.CallerAuthorityMode, ActorPrincipalID: "principal:test", ExecutionPrincipalID: "principal:test",
			Credential: &jobs.CredentialEvidence{Class: jobs.CredentialClassAPIToken, ID: "token:test", Fingerprint: "fingerprint:test", ExpiresAt: time.Now().UTC().Add(time.Hour)},
			Target:     jobs.AuthorityTarget{ProjectID: "project", Environment: "dev", ResourceKind: "pipeline", ResourceID: "sales-refresh"}, Permissions: []permissions.Pair{pair},
		},
	}
	if err := service.ExecuteClaimedJob(t.Context(), job); err != nil {
		t.Fatalf("execute claimed job: %v", err)
	}
	if calls != 4 {
		t.Fatalf("authority boundary calls = %d, want prepare/execute/publish/output", calls)
	}
}
