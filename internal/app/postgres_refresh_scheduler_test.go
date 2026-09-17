package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshartifact "github.com/flidai/leapview/internal/refresh/artifact"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	refreshplan "github.com/flidai/leapview/internal/refresh/plan"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/pkg/jobs"
)

type postgresScheduledAuthorityFixture struct {
	grant          access.ExecutionGrant
	selectedID     string
	selectedTarget struct {
		instanceID, projectID, pipelineID string
	}
}

func (f *postgresScheduledAuthorityFixture) SelectCurrentExecutionGrantID(_ context.Context, instanceID string, projectID, pipelineID projectgraph.ResourceID) (string, error) {
	f.selectedTarget = struct {
		instanceID, projectID, pipelineID string
	}{instanceID, projectID.String(), pipelineID.String()}
	return f.selectedID, nil
}

func (f *postgresScheduledAuthorityFixture) CurrentExecutionGrant(_ context.Context, id, recipient string) (access.ExecutionGrant, error) {
	if id != f.grant.ID || recipient != "" {
		return access.ExecutionGrant{}, errors.New("unexpected current grant lookup")
	}
	return f.grant, nil
}

type postgresScheduledStateFixture struct {
	state    servingstate.State
	artifact servingstate.Artifact
}

func (f postgresScheduledStateFixture) ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	return f.state, f.artifact, nil
}
func (f postgresScheduledStateFixture) ByID(context.Context, servingstate.ID) (servingstate.State, error) {
	return f.state, nil
}
func (f postgresScheduledStateFixture) ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error) {
	return f.artifact, nil
}

type postgresScheduledArtifactFixture struct {
	definition *refreshartifact.Definition
}

func (f postgresScheduledArtifactFixture) Load(context.Context, servingstate.Artifact) (refreshrun.LoadedArtifact, error) {
	return refreshrun.LoadedArtifact{Definition: f.definition}, nil
}

// Embedding the module contract keeps this focused composition fixture small;
// only queue admission and its two optional checks are exercised here.
type postgresScheduledRunFixture struct {
	refreshmodule.RunPersistence
	queued chan refreshrun.RunInput
}

func (f *postgresScheduledRunFixture) CheckInvocationAdmission(context.Context, projectgraph.ServingIdentity, projectgraph.ResourceID, string) error {
	return nil
}
func (f *postgresScheduledRunFixture) CheckScheduledInvocationAdmission(context.Context, refreshschedule.Occurrence) error {
	return nil
}
func (f *postgresScheduledRunFixture) CreateRunTree(_ context.Context, tree refreshrun.RunTreeInput) (refreshrun.RunRecord, []refreshrun.RunRecord, error) {
	f.queued <- tree.Root
	return refreshrun.RunRecord{ID: "run-scheduled", Identity: tree.Root.Identity, PipelineID: tree.Root.PipelineID, TargetID: tree.Root.TargetID, TargetType: tree.Root.TargetType, Status: refreshrun.RunStatusQueued, PrincipalID: tree.Root.PrincipalID, TriggerType: tree.Root.TriggerType}, nil, nil
}

type postgresScheduledScheduleFixture struct {
	occurrence refreshschedule.Occurrence
	claimed    bool
}

func (f *postgresScheduledScheduleFixture) Reconcile(context.Context, refreshschedule.ReconcileInput) error {
	return nil
}
func (f *postgresScheduledScheduleFixture) ClaimDue(_ context.Context, _ projectgraph.ServingIdentity, _ time.Time) ([]refreshschedule.Occurrence, error) {
	if f.claimed {
		return nil, nil
	}
	f.claimed = true
	return []refreshschedule.Occurrence{f.occurrence}, nil
}
func (f *postgresScheduledScheduleFixture) ReleaseOccurrence(context.Context, refreshschedule.Occurrence) error {
	return nil
}
func (f *postgresScheduledScheduleFixture) NextRun(context.Context, projectgraph.ServingIdentity, projectgraph.ResourceID) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (f *postgresScheduledScheduleFixture) SaveDataVersion(context.Context, refreshschedule.DataVersion) error {
	return nil
}
func (f *postgresScheduledScheduleFixture) DataVersion(context.Context, projectgraph.ServingIdentity, projectgraph.ResourceID) (refreshschedule.DataVersion, bool, error) {
	return refreshschedule.DataVersion{}, false, nil
}

type postgresScheduledPublicationFixture struct{}

func (postgresScheduledPublicationFixture) CompleteCanonicalRefresh(context.Context, refreshrun.JobRecord, refreshrun.CanonicalRefreshResult) error {
	return nil
}

func TestPostgresScheduledRefreshCompositionCarriesOccurrenceGrantIntoQueuedAuthority(t *testing.T) {
	identity := projectgraph.ServingIdentity{ProjectID: "project:sales", Environment: "prod", GenerationID: "generation:one"}
	pipelineID := projectgraph.ResourceID("pipeline:daily")
	resource, err := access.NewResourceRef(pipelineID, projectgraph.KindPipeline)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionPipelineRun, identity.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &postgresScheduledAuthorityFixture{
		selectedID: "grant:daily",
		grant: access.ExecutionGrant{
			ID: "grant:daily", Profile: access.DurableGrantProfile,
			Target:               access.DurableGrantTarget{InstanceID: "instance:prod", ProjectID: identity.ProjectID, ResourceUID: "00000000-0000-7000-8000-000000000001", ResourceID: pipelineID, ResourceKind: projectgraph.KindPipeline},
			Issuer:               access.GrantIssuerEvidence{PrincipalID: "00000000-0000-7000-8000-000000000010", Credential: access.GrantCredentialEvidence{Class: access.GrantCredentialClassAPIToken, ID: "token:issuer", Fingerprint: strings.Repeat("a", 32)}},
			ExecutionPrincipalID: "00000000-0000-7000-8000-000000000012", Permissions: []access.PermissionPair{pair},
			WorkflowID: "workflow:daily", WorkflowRevision: "revision:one", ClosureDigest: "sha256:" + strings.Repeat("1", 64), BindingDigest: "sha256:" + strings.Repeat("2", 64), DestinationDigest: "sha256:" + strings.Repeat("3", 64), TriggerDigest: "sha256:" + strings.Repeat("4", 64),
			IssuedAt: time.Now().UTC().Add(-time.Minute), ExpiresAt: time.Now().UTC().Add(time.Hour), Fingerprint: "sha256:" + strings.Repeat("5", 64),
		},
	}
	resolver := postgresScheduledExecutionGrantIDResolver(fixture, fixture.grant.Target.InstanceID)
	if resolver == nil {
		t.Fatal("PostgreSQL scheduled grant selector was not composed")
	}
	occurrence := refreshschedule.Occurrence{OccurrenceID: "occurrence:daily", Identity: identity, PipelineID: pipelineID, MatchingScheduleIDs: []string{"daily"}, ScheduledAt: time.Now().UTC()}
	grantID, err := resolver(t.Context(), occurrence)
	if err != nil {
		t.Fatalf("select grant for occurrence: %v", err)
	}
	if grantID != fixture.grant.ID || fixture.selectedTarget.instanceID != "instance:prod" || fixture.selectedTarget.projectID != identity.ProjectID.String() || fixture.selectedTarget.pipelineID != pipelineID.String() {
		t.Fatalf("selected grant target = %#v, id=%q", fixture.selectedTarget, grantID)
	}

	state := servingstate.State{ID: servingstate.ID(identity.GenerationID), ProjectID: identity.ProjectID, Environment: servingstate.Environment(identity.Environment), Status: servingstate.StatusActive}
	artifact := servingstate.Artifact{ServingStateID: state.ID, Digest: "sha256:" + strings.Repeat("a", 64)}
	queued := make(chan refreshrun.RunInput, 1)
	runs := &postgresScheduledRunFixture{queued: queued}
	schedules := &postgresScheduledScheduleFixture{occurrence: occurrence}
	definition := &refreshartifact.Definition{
		Pipelines:   map[string]refreshschedule.Definition{pipelineID.String(): {ID: pipelineID, SemanticModelID: "semantic:sales", SelectionDigest: "sha256:" + strings.Repeat("b", 64), ConcurrencyPolicy: refreshschedule.ConcurrencyForbid, Schedules: []refreshschedule.Schedule{{ID: "daily", Expression: "0 6 * * *"}}}},
		Models:      map[string]*semanticmodel.Model{"semantic:sales": {Name: "semantic:sales", Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}, Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders", GrainEntity: "order_id", Entities: map[string]semanticmodel.EntityDefinition{"order_id": {Type: "primary", Fields: []string{"order_id"}}}}}}},
		ModelTables: map[string]semanticmodel.Table{"orders": {ModelDependencies: nil}},
	}
	grantPlan, err := refreshplan.ForPipeline(definition, identity.ProjectID, pipelineID)
	if err != nil {
		t.Fatal(err)
	}
	grantPlan, err = grantPlan.BindGeneration(identity, artifact.Digest)
	if err != nil {
		t.Fatal(err)
	}
	approvedPlan, err := grantPlan.DeliveryPipelinePlan(refreshplan.InvocationPolicy{
		InvocationSource:        refreshrun.TriggerSchedule,
		MatchingScheduleIDs:     occurrence.MatchingScheduleIDs,
		ConcurrencyPolicy:       refreshschedule.ConcurrencyForbid,
		StartingDeadlineSeconds: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.grant.ClosureDigest = approvedPlan.Digest
	service := refreshrun.Service{
		ServingStates: postgresScheduledStateFixture{state: state, artifact: artifact},
		Artifacts:     postgresScheduledArtifactFixture{definition: definition},
		ResolveActive: func(context.Context, projectgraph.ServingIdentity) (refreshrun.ServingState, error) {
			return refreshrun.ServingState{State: state, Artifact: artifact}, nil
		},
		ResolveTargetRevision: func(context.Context, projectgraph.ServingIdentity) (int64, error) { return 1, nil },
		ResolveSourceDigest:   func(context.Context, projectgraph.ServingIdentity) (string, error) { return artifact.Digest, nil },
		CanonicalExecutor: func(context.Context, refreshrun.JobRecord) (refreshrun.CanonicalRefreshResult, error) {
			return refreshrun.CanonicalRefreshResult{}, nil
		},
	}
	m, err := refreshmodule.Build(t.Context(), refreshmodule.Config{
		Persistence: &refreshmodule.Persistence{Runs: runs, Schedules: schedules, Publication: postgresScheduledPublicationFixture{}},
		Service:     service, Authorization: refreshmodule.AuthorizationConfig{AuthorizeObject: func(context.Context, string, access.Capability, access.ResourceRef) (bool, error) { return true, nil }}, RequireAuthority: true, ExecutionGrants: fixture, InstanceID: fixture.grant.Target.InstanceID,
		EnableScheduler: true, ResolveScheduledExecutionGrantID: resolver, ResolveIdentity: func(context.Context) (projectgraph.ServingIdentity, error) { return identity, nil }, ReconcileSchedules: func(context.Context) error { return nil }, ScheduleInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("build scheduled refresh module: %v", err)
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(context.Background())
	select {
	case input := <-queued:
		if input.Authority.Mode != jobs.DelegatedWorkloadMode || input.Authority.ExecutionGrant == nil || input.Authority.ExecutionGrant.ID != fixture.grant.ID || input.PrincipalID != fixture.grant.ExecutionPrincipalID {
			t.Fatalf("queued scheduled authority = %#v, principal=%q", input.Authority, input.PrincipalID)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduled occurrence was not queued")
	}
}
