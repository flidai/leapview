package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These adapters intentionally live in the module's PostgreSQL conformance
// test. Production composition owns the equivalent mappings; the module only
// sees its capability-neutral projections.
type nativePGEventPort struct {
	repo *eventspostgres.Repository
	err  error
}

func (p *nativePGEventPort) AppendDeliveryEvent(ctx context.Context, tx deploymentpostgres.Tx, in NativeDeliveryEventInput) (deploymentpostgres.Event, error) {
	if p.err != nil {
		return deploymentpostgres.Event{}, p.err
	}
	e, err := p.repo.AppendEvent(ctx, tx, eventspostgres.EventInput{EventID: in.EventID, ScopeID: in.ScopeID, AggregateType: in.AggregateType, AggregateID: in.AggregateID, EventType: in.EventType, SchemaVersion: in.SchemaVersion, CorrelationID: in.CorrelationID, Payload: in.Payload})
	if err != nil {
		return deploymentpostgres.Event{}, err
	}
	return deploymentpostgres.Event{EventID: e.EventID, ScopeID: e.ScopeID, AggregateType: e.AggregateType, AggregateID: e.AggregateID, AggregateVersion: e.AggregateVersion, EventType: e.EventType, SchemaVersion: e.SchemaVersion, OccurredAt: e.OccurredAt, CorrelationID: e.CorrelationID, Payload: e.Payload}, nil
}

type nativePGAuditPort struct {
	repo   *accesspostgres.AuditRepository
	err    error
	tamper bool
}

func (p *nativePGAuditPort) AppendMutationAudit(ctx context.Context, tx deploymentpostgres.Tx, in NativeDeliveryAuditInput) (deploymentpostgres.AuditEvent, error) {
	if p.err != nil {
		return deploymentpostgres.AuditEvent{}, p.err
	}
	stored, err := p.repo.RecordAuditEvent(ctx, tx, access.AuditIntent{EventID: in.AuditID, DomainEventID: in.DomainEventID, ScopeID: in.ScopeID, ActorID: in.ActorID, Source: "deployment", Operation: "publication", Action: in.Action, ResourceKind: in.ResourceKind, ResourceID: in.ResourceID, Outcome: "success", RequestDigest: in.RequestDigest, CorrelationID: in.CorrelationID, AggregateKey: in.AggregateKey, AggregateSequence: in.AggregateSequence, MetadataJSON: string(in.Metadata)})
	if err != nil {
		return deploymentpostgres.AuditEvent{}, err
	}
	result := deploymentpostgres.AuditEvent{AuditID: stored.AuditID, EventID: stored.DomainEventID, ScopeID: stored.ScopeID, ActorID: stored.ActorID, Action: stored.Action, ResourceKind: stored.ResourceKind, ResourceID: stored.ResourceID, Outcome: in.Outcome, RequestDigest: stored.RequestDigest, Metadata: []byte(stored.MetadataJSON), OccurredAt: stored.OccurredAt}
	if p.tamper {
		result.Action = "tampered"
	}
	return result, nil
}

type nativePGWorkflowPort struct {
	repo *jobspostgres.Repository
	err  error
}

func (p *nativePGWorkflowPort) RecordWorkflow(ctx context.Context, tx deploymentpostgres.Tx, intent jobs.WorkflowIntent) error {
	if p.err != nil {
		return p.err
	}
	return p.repo.RecordWorkflow(ctx, tx, intent)
}

type nativePGOperationPort struct {
	repo           *operationpostgres.Repository
	acquireError   error
	completeError  error
	statusOverride NativeOperationStatus
}

func (p *nativePGOperationPort) AcquireTx(ctx context.Context, tx NativeOperationTx, in NativeOperationAcquireInput) (NativeOperationAcquireResult, error) {
	if p.acquireError != nil {
		return NativeOperationAcquireResult{}, p.acquireError
	}
	if p.statusOverride != "" {
		return NativeOperationAcquireResult{Status: p.statusOverride}, nil
	}
	got, err := p.repo.AcquireTx(ctx, tx, operationpostgres.AcquireInput{Scope: in.Scope, OperationType: in.OperationType, IdempotencyKey: in.IdempotencyKey, RequestDigest: in.RequestDigest, OwnerID: in.OwnerID})
	if err != nil {
		return NativeOperationAcquireResult{}, mapTestOperationError(err)
	}
	return NativeOperationAcquireResult{Status: NativeOperationStatus(got.Status), Operation: NativeOperationRecord{Scope: got.Operation.Scope, OperationType: got.Operation.OperationType, IdempotencyKey: got.Operation.IdempotencyKey, RequestDigest: got.Operation.RequestDigest, OwnerID: got.Operation.OwnerID, OperationID: got.Operation.OperationID, Outcome: got.Operation.Outcome}, Lease: NativeOperationLease{Scope: got.Lease.Scope, IdempotencyKey: got.Lease.IdempotencyKey, OperationID: got.Lease.OperationID, OwnerID: got.Lease.OwnerID, FencingGeneration: got.Lease.FencingGeneration, LeaseExpiresAt: got.Lease.LeaseExpiresAt, AttemptID: got.Lease.AttemptID, AttemptIdentity: got.Lease.AttemptIdentity}}, nil
}

func (p *nativePGOperationPort) CompleteTx(ctx context.Context, tx NativeOperationTx, lease NativeOperationLease, outcome json.RawMessage) error {
	if p.completeError != nil {
		return p.completeError
	}
	return mapTestOperationError(p.repo.CompleteTx(ctx, tx, operationpostgres.Lease{Scope: lease.Scope, IdempotencyKey: lease.IdempotencyKey, OperationID: lease.OperationID, OwnerID: lease.OwnerID, FencingGeneration: lease.FencingGeneration, LeaseExpiresAt: lease.LeaseExpiresAt, AttemptID: lease.AttemptID, AttemptIdentity: lease.AttemptIdentity}, outcome))
}

func mapTestOperationError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, operationpostgres.ErrConflict):
		return fmt.Errorf("%w: %v", ErrNativeOperationConflict, err)
	case errors.Is(err, operationpostgres.ErrBusy):
		return fmt.Errorf("%w: %v", ErrNativeOperationBusy, err)
	case errors.Is(err, operationpostgres.ErrStaleFence):
		return fmt.Errorf("%w: %v", ErrNativeOperationStaleFence, err)
	case errors.Is(err, operationpostgres.ErrLeaseExpired):
		return fmt.Errorf("%w: %v", ErrNativeOperationLeaseExpired, err)
	case errors.Is(err, operationpostgres.ErrAlreadyTerminal):
		return fmt.Errorf("%w: %v", ErrNativeOperationAlreadyTerminal, err)
	case errors.Is(err, operationpostgres.ErrInvalid):
		return fmt.Errorf("%w: %v", ErrNativeOperationInvalid, err)
	case errors.Is(err, operationpostgres.ErrNotFound):
		return fmt.Errorf("%w: %v", ErrNativeOperationNotFound, err)
	default:
		return err
	}
}

type nativePGActivationAudit struct {
	repo *accesspostgres.AuditRepository
}

func (p nativePGActivationAudit) AppendActivationAudit(ctx context.Context, tx deploymentpostgres.Tx, in deploymentpostgres.ActivationAuditInput) (deploymentpostgres.AuditEvent, error) {
	stored, err := p.repo.RecordAuditEvent(ctx, tx, access.AuditIntent{EventID: in.EventID, DomainEventID: in.DomainEventID, ScopeID: in.ScopeID, ActorID: in.ActorID, Source: "deployment", Operation: "activate", Action: in.Action, ResourceKind: in.ResourceKind, ResourceID: in.ResourceID, Outcome: "success", RequestDigest: in.RequestDigest, CorrelationID: in.CorrelationID, AggregateKey: in.AggregateKey, AggregateSequence: in.AggregateSequence, MetadataJSON: string(in.Metadata)})
	if err != nil {
		return deploymentpostgres.AuditEvent{}, err
	}
	return deploymentpostgres.AuditEvent{AuditID: stored.AuditID, EventID: stored.DomainEventID, ScopeID: stored.ScopeID, ActorID: stored.ActorID, Action: stored.Action, ResourceKind: stored.ResourceKind, ResourceID: stored.ResourceID, Outcome: in.Outcome, RequestDigest: stored.RequestDigest, Metadata: []byte(stored.MetadataJSON), OccurredAt: stored.OccurredAt}, nil
}

func (p nativePGActivationAudit) GetActivationAudit(ctx context.Context, tx deploymentpostgres.Tx, in deploymentpostgres.ActivationAuditInput) (deploymentpostgres.AuditEvent, error) {
	stored, err := p.repo.GetAuditEvent(ctx, tx, in.EventID)
	if err != nil {
		return deploymentpostgres.AuditEvent{}, err
	}
	return deploymentpostgres.AuditEvent{AuditID: stored.AuditID, EventID: stored.DomainEventID, ScopeID: stored.ScopeID, ActorID: stored.ActorID, Action: stored.Action, ResourceKind: stored.ResourceKind, ResourceID: stored.ResourceID, Outcome: in.Outcome, RequestDigest: stored.RequestDigest, Metadata: []byte(stored.MetadataJSON), OccurredAt: stored.OccurredAt}, nil
}

type nativePGFixture struct {
	db          *pgxpool.Pool
	repo        *deploymentpostgres.Repository
	coordinator *nativeCoordinator
	events      *nativePGEventPort
	audit       *nativePGAuditPort
	workflow    *nativePGWorkflowPort
	operations  *nativePGOperationPort
	targetID    string
	generation  string
}

func newNativePGFixture(t *testing.T) *nativePGFixture {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "deployment_native_coordinator_test")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{accesspostgres.SchemaSQL(), eventspostgres.SchemaSQL(), jobspostgres.SchemaSQL(), operationpostgres.SchemaSQL(), deploymentpostgres.SchemaSQL()} {
		if _, err := tx.Exec(t.Context(), schema); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	accessAudit := accesspostgres.New()
	repo := deploymentpostgres.NewWithOptions(db, deploymentpostgres.Options{ActivationAudit: nativePGActivationAudit{repo: accessAudit}})
	targetID := "target_native_" + strings.ReplaceAll(uuid.New().String()[:8], "-", "")
	ids := make([]string, 7)
	for i := range ids {
		ids[i] = uuid.New().String()
	}
	planID, candidateID, attemptID, sealID, generationID := ids[0], ids[1], ids[2], ids[3], ids[4]
	digest := func(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }
	ctx := t.Context()
	if _, err := repo.CreateTarget(ctx, deploymentpostgres.TargetInput{TargetID: targetID, ProjectID: "project_sales", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreatePlan(ctx, deploymentpostgres.PlanInput{PlanID: planID, TargetID: targetID, PlanRevision: 1, PlanDigest: digest('a'), CompiledGraphDigest: digest('b'), CompiledConfigDigest: digest('c'), SecurityDomainFingerprint: digest('d'), ArtifactDigest: digest('e'), QualificationDigest: digest('3'), Evidence: []byte(`{"qualification":"none"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateCandidate(ctx, deploymentpostgres.CandidateInput{CandidateID: candidateID, TargetID: targetID, PlanID: planID, CandidateRevision: 1, ArtifactDigest: digest('e')}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.BeginBuildAttempt(ctx, deploymentpostgres.BuildAttemptInput{AttemptID: attemptID, PlanID: planID, CandidateID: candidateID, OwnerID: "builder", PhysicalPoolID: "pool", FencingEpoch: 1, RequestDigest: digest('f'), PlanDigest: digest('a'), Namespace: "candidate/attempt", SessionIdentity: "session", LeaseExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	commitMarker := ducklake.CommitMarker{SchemaVersion: ducklake.CommitMarkerSchemaVersion, DeliveryID: "delivery-native", GenerationID: generationID, AttemptID: attemptID, LeaseEpoch: 1, RequestDigest: digest('f'), PlanDigest: digest('a'), Project: "project_sales", Environment: "prod", PhysicalPoolID: "pool"}
	markerJSON, err := commitMarker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	marker := []byte(markerJSON)
	if _, err := repo.BindBuildArtifact(ctx, deploymentpostgres.BuildArtifactBindingInput{AttemptID: attemptID, ServingArtifactID: "artifact-native", ServingArtifactDigest: digest('e'), ServingStateID: generationID, OwnerID: "builder", FencingEpoch: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitBuildAttempt(ctx, deploymentpostgres.CommitAttemptInput{AttemptID: attemptID, OwnerID: "builder", FencingEpoch: 1, SnapshotID: 42, CommitMarker: marker}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateSnapshotSeal(ctx, deploymentpostgres.SnapshotSealInput{SealID: sealID, AttemptID: attemptID, CandidateID: candidateID, PhysicalPoolID: "pool", TenantDomain: "tenant", Region: "us-east", EncryptionDomain: "enc", ObjectNamespace: "objects", CatalogDatabase: "ducklake", CatalogID: "catalog", CatalogUUID: uuid.New().String(), CatalogVersion: 1, DuckLakeSnapshotID: 42, RelationNamespace: "candidate/attempt", RelationManifestDigest: digest('1'), ClosureDigest: digest('8'), ObjectRoot: "objects/42", ObjectRootDigest: digest('6'), ArtifactRoot: "artifacts/" + digest('e'), ArtifactRootDigest: digest('7'), CompiledGraphDigest: digest('b'), CompiledConfigDigest: digest('c'), SecurityDomainFingerprint: digest('d'), RequestDigest: digest('f'), PlanDigest: digest('a'), CompatibilityDigest: digest('2'), ServingArtifactID: "artifact-native", ServingArtifactDigest: digest('e'), DuckDBVersion: "1", RuntimeVersion: "runtime-v1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1", QualificationEvidence: []byte(`{"checks":["schema"]}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.QualifyCandidate(ctx, candidateID, sealID, digest('3')); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApproveCandidate(ctx, deploymentpostgres.DeliveryApproval{ApprovalID: uuid.New().String(), CandidateID: candidateID, Decision: "approved", Evidence: json.RawMessage(`{"review":"ok"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateGeneration(ctx, deploymentpostgres.GenerationInput{GenerationID: generationID, TargetID: targetID, CandidateID: candidateID, SnapshotSealID: sealID, PlanID: planID, PlanDigest: digest('a'), ArtifactRoot: "artifacts/" + digest('e'), ArtifactRootDigest: digest('7'), ServingArtifactDigest: digest('e'), CompiledGraphDigest: digest('b'), CompiledConfigDigest: digest('c'), SecurityDomainFingerprint: digest('d'), GenerationRevision: 1}); err != nil {
		t.Fatal(err)
	}
	events := &nativePGEventPort{repo: eventspostgres.New()}
	audit := &nativePGAuditPort{repo: accessAudit}
	workflow := &nativePGWorkflowPort{repo: jobspostgres.NewRepository(db)}
	operations := &nativePGOperationPort{repo: operationpostgres.New(db)}
	coordinator, err := newNativeCoordinator(repo, targetID, "prod", nativeCoordinatorCapabilities{events: events, audit: audit, workflow: workflow, operations: operations})
	if err != nil {
		t.Fatal(err)
	}
	return &nativePGFixture{db: db, repo: repo, coordinator: coordinator.(*nativeCoordinator), events: events, audit: audit, workflow: workflow, operations: operations, targetID: targetID, generation: generationID}
}

func nativeCreateRequest(f *nativePGFixture, key string) apiadapter.CreateRequest {
	return apiadapter.CreateRequest{Project: "project_sales", Environment: "prod", GenerationID: f.generation, ArtifactDigest: "sha256:" + strings.Repeat("e", 64), Actor: "operator", IdempotencyKey: key, Workflow: func(id string) (jobs.WorkflowIntent, error) {
		return jobs.WorkflowIntent{Event: jobs.EventInput{Key: "deployment-created-" + id, ResourceKind: "deployment", ResourceID: id, EventType: "deployment.created", Data: []byte(`{"ok":true}`)}}, nil
	}}
}

func TestNativeCoordinatorPostgresLifecycleReplayAndScope(t *testing.T) {
	f := newNativePGFixture(t)
	ctx := t.Context()
	first, err := f.coordinator.Create(ctx, nativeCreateRequest(f, "create-1"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != apiadapter.StatusPending || first.ID == "" {
		t.Fatalf("create = %#v", first)
	}
	replay, err := f.coordinator.Create(ctx, nativeCreateRequest(f, "create-1"))
	if err != nil || replay.ID != first.ID || replay.Status != first.Status {
		t.Fatalf("create replay = %#v, %v", replay, err)
	}
	changed := nativeCreateRequest(f, "create-1")
	changed.Actor = "different-actor"
	if _, err := f.coordinator.Create(ctx, changed); !errors.Is(err, deployment.ErrConflict) {
		t.Fatalf("changed-key request error = %v", err)
	}
	got, err := f.coordinator.Get(ctx, apiadapter.Scope{Project: "project_sales", DeploymentID: first.ID})
	if err != nil || got.ID != first.ID {
		t.Fatalf("get = %#v, %v", got, err)
	}
	if _, err := f.coordinator.Get(ctx, apiadapter.Scope{Project: "other_project", DeploymentID: first.ID}); !errors.Is(err, deployment.ErrNotFound) {
		t.Fatalf("cross-project get = %v", err)
	}
	cancelled, err := f.coordinator.CancelRequest(ctx, apiadapter.CancelRequest{Scope: apiadapter.Scope{Project: "project_sales", DeploymentID: first.ID}, Actor: "operator", IdempotencyKey: "cancel-1", Workflow: func(id string) (jobs.WorkflowIntent, error) {
		return jobs.WorkflowIntent{Event: jobs.EventInput{Key: "deployment-cancelled-" + id, ResourceKind: "deployment", ResourceID: id, EventType: "deployment.cancelled", Data: []byte(`{"ok":true}`)}}, nil
	}})
	if err != nil || cancelled.Status != apiadapter.StatusCancelled {
		t.Fatalf("cancel = %#v, %v", cancelled, err)
	}
	cancelReplay, err := f.coordinator.CancelRequest(ctx, apiadapter.CancelRequest{Scope: apiadapter.Scope{Project: "project_sales", DeploymentID: first.ID}, Actor: "operator", IdempotencyKey: "cancel-1"})
	if err != nil || cancelReplay.ID != cancelled.ID || cancelReplay.Status != apiadapter.StatusCancelled {
		t.Fatalf("cancel replay = %#v, %v", cancelReplay, err)
	}
}

func TestNativeCoordinatorPostgresActivationReplayAndCancelCommittedConflict(t *testing.T) {
	f := newNativePGFixture(t)
	created, err := f.coordinator.Create(t.Context(), nativeCreateRequest(f, "activate-create"))
	if err != nil {
		t.Fatal(err)
	}
	active, err := f.coordinator.Activate(t.Context(), apiadapter.ActivateRequest{Scope: apiadapter.Scope{Project: "project_sales", DeploymentID: created.ID}, Actor: "operator", IdempotencyKey: "activate-1"})
	if err != nil || active.Status != apiadapter.StatusActive {
		t.Fatalf("activate = %#v, %v", active, err)
	}
	activeReplay, err := f.coordinator.Activate(t.Context(), apiadapter.ActivateRequest{Scope: apiadapter.Scope{Project: "project_sales", DeploymentID: created.ID}, Actor: "operator", IdempotencyKey: "activate-1"})
	if err != nil || activeReplay.ID != active.ID || activeReplay.Status != apiadapter.StatusActive {
		t.Fatalf("activate replay = %#v, %v", activeReplay, err)
	}
	if _, err := f.coordinator.CancelRequest(t.Context(), apiadapter.CancelRequest{Scope: apiadapter.Scope{Project: "project_sales", DeploymentID: created.ID}, Actor: "operator", IdempotencyKey: "cancel-after-activate"}); !errors.Is(err, deployment.ErrConflict) {
		t.Fatalf("cancel committed publication = %v", err)
	}
}

func TestNativeCoordinatorPostgresMutationFailuresRollbackSourceAndOperation(t *testing.T) {
	tests := []struct {
		name  string
		fail  func(*nativePGFixture)
		clear func(*nativePGFixture)
	}{
		{"event", func(f *nativePGFixture) { f.events.err = errors.New("event failure") }, func(f *nativePGFixture) { f.events.err = nil }},
		{"audit", func(f *nativePGFixture) { f.audit.err = errors.New("audit failure") }, func(f *nativePGFixture) { f.audit.err = nil }},
		{"tampered audit projection", func(f *nativePGFixture) { f.audit.tamper = true }, func(f *nativePGFixture) { f.audit.tamper = false }},
		{"workflow", func(f *nativePGFixture) { f.workflow.err = errors.New("workflow failure") }, func(f *nativePGFixture) { f.workflow.err = nil }},
		{"operation acquire", func(f *nativePGFixture) { f.operations.acquireError = ErrNativeOperationConflict }, func(f *nativePGFixture) { f.operations.acquireError = nil }},
		{"operation indeterminate", func(f *nativePGFixture) { f.operations.statusOverride = NativeOperationIndeterminate }, func(f *nativePGFixture) { f.operations.statusOverride = "" }},
		{"operation complete", func(f *nativePGFixture) { f.operations.completeError = errors.New("complete failure") }, func(f *nativePGFixture) { f.operations.completeError = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativePGFixture(t)
			tc.fail(f)
			req := nativeCreateRequest(f, "failure-"+strings.ReplaceAll(tc.name, " ", "-"))
			if _, err := f.coordinator.Create(t.Context(), req); err == nil {
				t.Fatal("failure injection unexpectedly succeeded")
			}
			var publications int
			if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM delivery.delivery_publication WHERE target_id=$1`, f.targetID).Scan(&publications); err != nil {
				t.Fatal(err)
			}
			if publications != 0 {
				t.Fatalf("failed mutation left %d source publication rows", publications)
			}
			tc.clear(f)
			created, err := f.coordinator.Create(t.Context(), req)
			if err != nil {
				t.Fatalf("retry after rollback = %v", err)
			}
			if _, err := f.repo.Publication(t.Context(), created.ID); err != nil {
				t.Fatalf("source publication missing after retry: %v", err)
			}
		})
	}
}
