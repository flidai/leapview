package run

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/refresh/artifact"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/flidai/leapview/pkg/permissions"
)

func connectionAuthorityFixture(t *testing.T) (Service, *fakeRepo, *artifact.Definition, QueuePipelineInput) {
	t.Helper()
	repo := newFakeRepo()
	definition := refreshTestDefinition()
	definition.ConnectionIDs = map[string]string{"warehouse": "connection:warehouse"}
	definition.Models["sales"].Connections = map[string]semanticmodel.Connection{"warehouse": {Kind: "postgres"}}
	definition.Models["sales"].Sources = map[string]semanticmodel.Source{"source:orders": {Connection: "warehouse"}}
	for name, table := range definition.ModelTables {
		table.Execution.Source = "source:orders"
		definition.ModelTables[name] = table
	}
	service := canonicalQueueService(repo)
	service.Artifacts = fakeArtifactLoader{definition: definition}
	service.RequireAuthority = true
	service.AuthorityRevalidator = jobs.AuthorityRevalidatorFunc(func(context.Context, jobs.AuthorityEnvelope) error { return nil })
	pair, err := permissions.NewExactPair("leapview.permissions/v1", "pipeline.run", "project", "pipeline", "sales-refresh")
	if err != nil {
		t.Fatal(err)
	}
	return service, repo, definition, QueuePipelineInput{
		Identity: serviceIdentity, PrincipalID: "principal:test", EstimatedMemoryBytes: 1,
		PipelineID: "sales-refresh", TriggerType: TriggerManual,
		Authority: jobs.AuthorityEnvelope{
			Profile: jobs.AuthorityEnvelopeProfile, Mode: jobs.CallerAuthorityMode,
			ActorPrincipalID: "principal:test", ExecutionPrincipalID: "principal:test",
			Credential:  &jobs.CredentialEvidence{Class: jobs.CredentialClassSession, ID: "session:test", Fingerprint: "fingerprint:test", ExpiresAt: time.Now().UTC().Add(time.Hour)},
			Target:      jobs.AuthorityTarget{InstanceID: "instance:test", ProjectID: "project", Environment: "dev", ResourceKind: "pipeline", ResourceID: "sales-refresh"},
			Permissions: []permissions.Pair{pair},
		},
	}
}

func connectionUsePair(t *testing.T, id string) permissions.Pair {
	t.Helper()
	pair, err := permissions.NewExactPair("leapview.permissions/v1", "connection.use", "project", "connection", id)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func TestQueueRefreshCapturesAndAuthorizesExactConnectionsBeforePersistence(t *testing.T) {
	service, repo, _, input := connectionAuthorityFixture(t)
	original := append([]permissions.Pair(nil), input.Authority.Permissions...)
	want := append(append([]permissions.Pair(nil), original...), connectionUsePair(t, "connection:warehouse"))
	calls := 0
	service.AuthorityRevalidator = jobs.AuthorityRevalidatorFunc(func(_ context.Context, authority jobs.AuthorityEnvelope) error {
		calls++
		if len(repo.createdRuns) != 0 {
			t.Fatal("authority was first checked after persistence")
		}
		if !reflect.DeepEqual(authority.Permissions, want) {
			t.Fatalf("checked permissions = %#v, want %#v", authority.Permissions, want)
		}
		return nil
	})
	if _, err := service.QueuePipelineRefresh(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(repo.createdRuns) == 0 || !reflect.DeepEqual(repo.createdRuns[0].Authority.Permissions, want) {
		t.Fatalf("checks=%d, persisted runs=%#v", calls, repo.createdRuns)
	}
	if !reflect.DeepEqual(input.Authority.Permissions, original) {
		t.Fatal("capture mutated caller envelope")
	}
}

func TestQueueRefreshDeniesConnectionAuthorityBeforePersistence(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "missing revalidator"}[unavailable], func(t *testing.T) {
			service, repo, _, input := connectionAuthorityFixture(t)
			service.AuthorityRevalidator = jobs.AuthorityRevalidatorFunc(func(context.Context, jobs.AuthorityEnvelope) error { return errors.New("denied") })
			if unavailable {
				service.AuthorityRevalidator = nil
			}
			if _, err := service.QueuePipelineRefresh(t.Context(), input); err == nil {
				t.Fatal("queued without current connection authority")
			}
			if len(repo.createdRuns) != 0 {
				t.Fatal("unauthorized work was persisted")
			}
		})
	}
}

func connectionAuthorityJob(t *testing.T, service Service, repo *fakeRepo, input QueuePipelineInput) JobRecord {
	t.Helper()
	if _, err := service.QueuePipelineRefresh(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	root := repo.createdRuns[0]
	return JobRecord{ID: "job:connections", Identity: root.Identity, PrincipalID: root.PrincipalID, EstimatedMemoryBytes: 1,
		RunID: "run_root", SemanticModelID: root.SemanticModelID, PipelineID: root.PipelineID, PipelinePlan: root.PipelinePlan,
		TargetType: root.TargetType, TargetID: root.TargetID, TriggerType: root.TriggerType, InvocationSource: root.InvocationSource,
		Kind: JobKindRefreshPipeline, LeaseOwner: "worker", LeaseRevision: 1, Authority: root.Authority}
}

func TestRefreshCannotGainUncapturedConnectionAuthority(t *testing.T) {
	for _, change := range []string{"missing captured pair", "extra captured pair", "binding drift", "generation drift"} {
		t.Run(change, func(t *testing.T) {
			service, repo, definition, input := connectionAuthorityFixture(t)
			job := connectionAuthorityJob(t, service, repo, input)
			switch change {
			case "missing captured pair":
				job.Authority.Permissions = job.Authority.Permissions[:1]
			case "extra captured pair":
				job.Authority.Permissions = append(job.Authority.Permissions, connectionUsePair(t, "connection:other"))
			case "binding drift":
				definition.ConnectionIDs["warehouse"] = "connection:other"
			case "generation drift":
				repo.activeDeployment.ID = "generation:other"
				repo.activeArtifact.ServingStateID = repo.activeDeployment.ID
			}
			executed := false
			service.CanonicalExecutor = func(context.Context, JobRecord) (CanonicalRefreshResult, error) {
				executed = true
				return CanonicalRefreshResult{}, nil
			}
			if err := service.ExecuteClaimedJob(t.Context(), job); err == nil {
				t.Fatal("queued authority drift accepted")
			}
			if executed {
				t.Fatal("executor ran after authority drift")
			}
		})
	}
}

func TestDelegatedRefreshRequiresCapturedConnectionUse(t *testing.T) {
	service, repo, definition, input := connectionAuthorityFixture(t)
	input.PrincipalID = "workload:refresh"
	input.Authority = testDelegatedAuthority(testDelegatedPipelinePlan(t, repo, definition))
	if _, err := service.QueuePipelineRefresh(t.Context(), input); err == nil || len(repo.createdRuns) != 0 {
		t.Fatal("delegated grant without connection.use authorized source use")
	}
	input.Authority.Permissions = append(input.Authority.Permissions, connectionUsePair(t, "connection:warehouse"))
	if _, err := service.QueuePipelineRefresh(t.Context(), input); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshOutputRechecksAuthorityAfterOwnGenerationCutover(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		for _, revoke := range []bool{false, true} {
			t.Run(map[bool]string{false: "caller", true: "delegated"}[delegated]+"/"+map[bool]string{false: "active", true: "revoked"}[revoke], func(t *testing.T) {
				service, repo, definition, input := connectionAuthorityFixture(t)
				if delegated {
					input.PrincipalID = "workload:refresh"
					input.Authority = testDelegatedAuthority(testDelegatedPipelinePlan(t, repo, definition))
					input.Authority.Permissions = append(input.Authority.Permissions, connectionUsePair(t, "connection:warehouse"))
				}
				job := connectionAuthorityJob(t, service, repo, input)
				cutover := false
				service.AuthorityRevalidator = jobs.AuthorityRevalidatorFunc(func(_ context.Context, authority jobs.AuthorityEnvelope) error {
					if cutover && revoke {
						return errors.New("connection authority revoked")
					}
					return nil
				})
				service.CanonicalExecutor = func(context.Context, JobRecord) (CanonicalRefreshResult, error) {
					return CanonicalRefreshResult{PlanID: "plan:next", ServingStateID: "generation:next"}, nil
				}
				service.Publication = fakePublication{repo: repo}
				service.CanonicalCompletionCoordinator = func(_ context.Context, _ JobRecord, result CanonicalRefreshResult, complete func() error) error {
					if err := complete(); err != nil {
						return err
					}
					repo.activeDeployment.ID = "generation:next"
					repo.activeArtifact.ServingStateID = repo.activeDeployment.ID
					cutover = true
					return nil
				}
				output := false
				service.CanonicalResultReconciler = func(context.Context, JobRecord, CanonicalRefreshResult) error { output = true; return nil }
				err := service.ExecuteClaimedJob(t.Context(), job)
				if !cutover || (err != nil) != revoke || output == revoke {
					t.Fatalf("cutover=%v output=%v revoke=%v err=%v", cutover, output, revoke, err)
				}
			})
		}
	}
}
