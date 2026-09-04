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
	projectgraph "github.com/flidai/leapview/internal/project/graph"
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

// nativePGActivationLineage is a strict test-only verifier for the native
// coordinator's activation transaction. It keeps the fixture independent of
// application composition while enforcing the canonical target/project/
// generation tuple at the repository boundary.
type nativePGActivationLineage struct {
	expected deploymentpostgres.ActivationLineageInput
}

func (v *nativePGActivationLineage) VerifyActivationLineage(_ context.Context, tx deploymentpostgres.Tx, input deploymentpostgres.ActivationLineageInput) error {
	if v == nil || tx == nil {
		return errors.New("activation lineage verifier requires a transaction")
	}
	if input.TargetID == "" || input.ProjectID == "" || input.GenerationID == "" {
		return errors.New("activation lineage identity is incomplete")
	}
	if input != v.expected {
		return errors.New("activation lineage identity mismatch")
	}
	return nil
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
	candidate   string
	plan        string
	seal        string
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
	targetID := "target_native_" + strings.ReplaceAll(uuid.New().String()[:8], "-", "")
	ids := make([]string, 7)
	for i := range ids {
		ids[i] = uuid.New().String()
	}
	planID, candidateID, attemptID, sealID, generationID := ids[0], ids[1], ids[2], ids[3], ids[4]
	lineage := &nativePGActivationLineage{expected: deploymentpostgres.ActivationLineageInput{TargetID: targetID, ProjectID: "project_sales", GenerationID: generationID}}
	repo := deploymentpostgres.NewWithOptions(db, deploymentpostgres.Options{ActivationAudit: nativePGActivationAudit{repo: accessAudit}, Lineage: lineage})
	digest := func(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }
	ctx := t.Context()
	if _, err := repo.CreateTarget(ctx, deploymentpostgres.TargetInput{TargetID: targetID, ProjectID: "project_sales", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	richPlan, err := deployment.NewDeliveryPlan(deployment.DeliveryPlan{
		ID: planID, TargetID: targetID, ProjectID: "project_sales", Environment: "prod",
		Operation: deployment.DeliveryOperationCodeChange, SourceDigest: digest('e'), ServingArtifactDigest: digest('e'),
		Execution:  deployment.DeliveryExecutionInputs{SourceArtifactDigest: digest('e'), CompilerDigest: digest('b'), ExecutableDigest: digest('4'), DependencyDigest: digest('5'), ConfigDigest: digest('c'), BindingDigest: digest('d'), RuntimeDigest: digest('0'), CapabilityDigest: digest('9')},
		Provenance: deployment.DeliveryProvenance{Builder: "native-coordinator-test"},
		Governance: deployment.DeliveryGovernance{PolicyDigest: digest('2'), AuthorizationDigest: digest('d'), QualificationDigest: digest('3'), ApprovalPolicyRevision: 1, ExpiresAt: createdAt.Add(time.Hour)},
		Evidence:   deployment.DeliveryPlanEvidence{ImpactStatement: "native coordinator fixture", PhysicalWorkStatement: "seal fixture snapshot", ReuseStatement: "no fixture reuse", Qualification: deployment.DeliveryQualificationEvidence{Policy: "exact native snapshot", Steps: []deployment.DeliveryQualificationStep{{ID: "snapshot", Kind: "contract", Description: "verify native snapshot", Required: true, Blocking: true}}}, StalePolicy: deployment.DeliveryStalePolicy{Mode: "reject"}, Rollback: deployment.DeliveryRollbackEvidence{Class: deployment.DeliveryServingSafe}},
		CreatedAt:  createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	planDocument, err := json.Marshal(richPlan)
	if err != nil {
		t.Fatal(err)
	}
	planDigest := richPlan.Digest
	if _, err := repo.CreatePlan(ctx, deploymentpostgres.PlanInput{PlanID: planID, TargetID: targetID, PlanRevision: 1, PlanDigest: planDigest, CompiledGraphDigest: digest('b'), CompiledConfigDigest: digest('c'), SecurityDomainFingerprint: digest('d'), ArtifactDigest: digest('e'), QualificationDigest: digest('3'), QualificationRequired: false, ApprovalRequired: false, ApprovalPolicyRevision: 1, PlanDocument: planDocument, Evidence: []byte(`{"qualification":"none"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateCandidate(ctx, deploymentpostgres.CandidateInput{CandidateID: candidateID, TargetID: targetID, PlanID: planID, CandidateRevision: 1, ArtifactDigest: digest('e')}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.BeginBuildAttempt(ctx, deploymentpostgres.BuildAttemptInput{AttemptID: attemptID, PlanID: planID, CandidateID: candidateID, OwnerID: "builder", PhysicalPoolID: "pool", CatalogID: "catalog", FencingEpoch: 1, RequestDigest: digest('f'), PlanDigest: planDigest, Namespace: "candidate/attempt", SessionIdentity: "session", LeaseExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	commitMarker := ducklake.CommitMarker{SchemaVersion: ducklake.CommitMarkerSchemaVersion, DeliveryID: "delivery-native", GenerationID: generationID, AttemptID: attemptID, LeaseEpoch: 1, RequestDigest: digest('f'), PlanDigest: planDigest, Project: "project_sales", Environment: "prod", PhysicalPoolID: "pool"}
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
	if _, err := repo.CreateSnapshotSeal(ctx, deploymentpostgres.SnapshotSealInput{SealID: sealID, AttemptID: attemptID, CandidateID: candidateID, PhysicalPoolID: "pool", TenantDomain: "tenant", Region: "us-east", EncryptionDomain: "enc", ObjectNamespace: "objects", CatalogDatabase: "ducklake", CatalogID: "catalog", CatalogUUID: uuid.New().String(), CatalogVersion: 1, DuckLakeSnapshotID: 42, RelationNamespace: "candidate/attempt", RelationManifestDigest: digest('1'), ClosureDigest: digest('8'), ObjectRoot: "objects/42", ObjectRootDigest: digest('6'), ArtifactRoot: "artifacts/" + digest('e'), ArtifactRootDigest: digest('7'), CompiledGraphDigest: digest('b'), CompiledConfigDigest: digest('c'), SecurityDomainFingerprint: digest('d'), RequestDigest: digest('f'), PlanDigest: planDigest, CompatibilityDigest: digest('2'), ServingArtifactID: "artifact-native", ServingArtifactDigest: digest('e'), DuckDBVersion: "1", RuntimeVersion: "runtime-v1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1", QualificationEvidence: []byte(`{"checks":["schema"]}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.QualifyCandidate(ctx, candidateID, sealID, digest('3')); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateGeneration(ctx, deploymentpostgres.GenerationInput{GenerationID: generationID, TargetID: targetID, CandidateID: candidateID, SnapshotSealID: sealID, PlanID: planID, PlanDigest: planDigest, ArtifactRoot: "artifacts/" + digest('e'), ArtifactRootDigest: digest('7'), ServingArtifactDigest: digest('e'), CompiledGraphDigest: digest('b'), CompiledConfigDigest: digest('c'), SecurityDomainFingerprint: digest('d'), GenerationRevision: 1}); err != nil {
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
	return &nativePGFixture{db: db, repo: repo, coordinator: coordinator.(*nativeCoordinator), events: events, audit: audit, workflow: workflow, operations: operations, targetID: targetID, candidate: candidateID, plan: planID, seal: sealID, generation: generationID}
}

func nativePublishRequest(f *nativePGFixture, key string) NativeDeliveryPublishRequest {
	project, _ := projectgraph.NewResourceID("project_sales")
	return NativeDeliveryPublishRequest{ProjectID: project, TargetID: f.targetID, Environment: "prod", CandidateID: uuid.MustParse(f.candidate), PrincipalID: "operator", IdempotencyKey: key}
}

func nativeRollbackRequest(f *nativePGFixture, key string) NativeDeliveryRollbackRequest {
	project, _ := projectgraph.NewResourceID("project_sales")
	return NativeDeliveryRollbackRequest{ProjectID: project, TargetID: f.targetID, Environment: "prod", GenerationID: uuid.MustParse(f.generation), PrincipalID: "operator", IdempotencyKey: key}
}

func isNativeDeliveryConflict(err error) bool {
	return errors.Is(err, deployment.ErrConflict) || errors.Is(err, deployment.ErrDeliveryConflict)
}

func TestNativeCoordinatorPostgresPublishCandidatePersistsEvidenceAndReplays(t *testing.T) {
	f := newNativePGFixture(t)
	ctx := t.Context()
	request := nativePublishRequest(f, "publish-native-1")
	first, err := f.coordinator.PublishCandidate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "pending" || first.ID == uuid.Nil || first.OperationID != first.ID || first.CandidateID != request.CandidateID || first.GenerationID != uuid.MustParse(f.generation) || first.ExpectedTargetRevision != 1 || first.ResultTargetRevision != 0 {
		t.Fatalf("publish projection = %#v", first)
	}
	stored, err := f.repo.Publication(ctx, first.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != "pending" || stored.PublicationID != first.ID.String() || stored.TargetID != f.targetID || stored.CandidateID != f.candidate || stored.GenerationID != f.generation || stored.ActorID != request.PrincipalID || stored.RequestDigest != first.RequestDigest {
		t.Fatalf("stored publication = %#v", stored)
	}

	var operationType, operationState, operationOwner, operationDigest string
	var operationOutcome []byte
	if err := f.db.QueryRow(ctx, `SELECT operation_type,state,owner_id,request_digest,outcome::text FROM platform.operation WHERE operation_id=$1`, first.ID).Scan(&operationType, &operationState, &operationOwner, &operationDigest, &operationOutcome); err != nil {
		t.Fatal(err)
	}
	if operationType != "delivery.publication.create" || operationState != "completed" || operationOwner != request.PrincipalID || operationDigest != first.RequestDigest {
		t.Fatalf("operation = %q %q %q %q", operationType, operationState, operationOwner, operationDigest)
	}
	var operationEvidence nativeMutationOutcome
	if err := json.Unmarshal(operationOutcome, &operationEvidence); err != nil {
		t.Fatal(err)
	}
	if operationEvidence.PublicationID != first.ID.String() || operationEvidence.EventID != first.EventID.String() || operationEvidence.AuditID != first.AuditID.String() {
		t.Fatalf("operation outcome = %#v", operationEvidence)
	}

	var eventScope, aggregateType, aggregateID, eventType, correlationID string
	var aggregateVersion, schemaVersion int64
	var eventPayload []byte
	if err := f.db.QueryRow(ctx, `SELECT scope_id,aggregate_type,aggregate_id,event_type,aggregate_version,schema_version,correlation_id::text,payload::text FROM event.event_log WHERE event_id=$1`, first.EventID).Scan(&eventScope, &aggregateType, &aggregateID, &eventType, &aggregateVersion, &schemaVersion, &correlationID, &eventPayload); err != nil {
		t.Fatal(err)
	}
	if eventScope != f.targetID || aggregateType != "delivery_publication" || aggregateID != first.ID.String() || eventType != "delivery.publication.requested" || aggregateVersion != 1 || schemaVersion != 1 || correlationID != first.EventID.String() {
		t.Fatalf("event identity = %q %q %q %q v%d schema%d correlation=%q", eventScope, aggregateType, aggregateID, eventType, aggregateVersion, schemaVersion, correlationID)
	}
	var expectedPayload map[string]any
	if err := json.Unmarshal(eventPayload, &expectedPayload); err != nil {
		t.Fatal(err)
	}
	if expectedPayload["publication_id"] != first.ID.String() || expectedPayload["generation_id"] != first.GenerationID.String() || expectedPayload["target_revision"] != float64(first.ExpectedTargetRevision) {
		t.Fatalf("event payload = %s", eventPayload)
	}

	var auditEventID, auditScope, auditActor, auditAction, auditKind, auditResource, auditOutcome, auditDigest string
	var auditMetadata []byte
	if err := f.db.QueryRow(ctx, `SELECT event_id::text,scope_id,actor_id,action,resource_kind,resource_id,outcome,request_digest,metadata::text FROM audit.audit_event WHERE audit_id=$1`, first.AuditID).Scan(&auditEventID, &auditScope, &auditActor, &auditAction, &auditKind, &auditResource, &auditOutcome, &auditDigest, &auditMetadata); err != nil {
		t.Fatal(err)
	}
	if auditEventID != first.EventID.String() || auditScope != f.targetID || auditActor != request.PrincipalID || auditAction != "delivery.publication.requested" || auditKind != "publication" || auditResource != first.ID.String() || auditOutcome != "success" || auditDigest != first.RequestDigest {
		t.Fatalf("audit identity = %q %q %q %q %q %q %q %q", auditEventID, auditScope, auditActor, auditAction, auditKind, auditResource, auditOutcome, auditDigest)
	}
	var auditPayload map[string]any
	if err := json.Unmarshal(auditMetadata, &auditPayload); err != nil {
		t.Fatal(err)
	}
	if auditPayload["publication_id"] != first.ID.String() || auditPayload["generation_id"] != first.GenerationID.String() || auditPayload["target_revision"] != float64(first.ExpectedTargetRevision) {
		t.Fatalf("audit metadata = %s", auditMetadata)
	}

	replay, err := f.coordinator.PublishCandidate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != first.ID || replay.EventID != first.EventID || replay.AuditID != first.AuditID || replay.RequestDigest != first.RequestDigest || replay.Status != first.Status {
		t.Fatalf("publish replay = %#v (first %#v)", replay, first)
	}
	changed := request
	changed.ProjectID, _ = projectgraph.NewResourceID("other_project")
	changed.IdempotencyKey = "publish-native-project-conflict"
	if _, err := f.coordinator.PublishCandidate(ctx, changed); !isNativeDeliveryConflict(err) {
		t.Fatalf("project isolation error = %v", err)
	}
	changed = request
	changed.TargetID = "other_target"
	changed.IdempotencyKey = "publish-native-target-conflict"
	if _, err := f.coordinator.PublishCandidate(ctx, changed); !isNativeDeliveryConflict(err) {
		t.Fatalf("target isolation error = %v", err)
	}
	changed = request
	changed.IdempotencyKey = request.IdempotencyKey
	changed.PrincipalID = "different-actor"
	if _, err := f.coordinator.PublishCandidate(ctx, changed); !isNativeDeliveryConflict(err) {
		t.Fatalf("same-key different-request error = %v", err)
	}
}

func TestNativeCoordinatorPostgresRollbackRequiresRetainedGeneration(t *testing.T) {
	t.Run("live root succeeds and replays", func(t *testing.T) {
		f := newNativePGFixture(t)
		ctx := t.Context()
		if _, err := f.repo.CreateRetentionRoot(ctx, deploymentpostgres.DeliveryRetentionRoot{RootID: f.generation, TargetID: f.targetID, CandidateID: f.candidate, GenerationID: f.generation, SnapshotSealID: f.seal, RootKind: "generation", State: "live", ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		request := nativeRollbackRequest(f, "rollback-native-1")
		first, err := f.coordinator.RollbackGeneration(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		if first.Status != "pending" || first.ID == uuid.Nil || first.GenerationID != request.GenerationID || first.CandidateID != uuid.MustParse(f.candidate) || first.ExpectedTargetRevision != 1 {
			t.Fatalf("rollback projection = %#v", first)
		}
		var rootTarget, rootCandidate, rootGeneration, rootSeal, rootKind, rootState string
		var rootExpiresNull bool
		if err := f.db.QueryRow(ctx, `SELECT target_id,candidate_id::text,generation_id::text,snapshot_seal_id::text,root_kind,state,expires_at IS NULL FROM delivery.delivery_retention_root WHERE root_id=$1`, first.ID).Scan(&rootTarget, &rootCandidate, &rootGeneration, &rootSeal, &rootKind, &rootState, &rootExpiresNull); err != nil {
			t.Fatal(err)
		}
		if rootTarget != f.targetID || rootCandidate != f.candidate || rootGeneration != f.generation || rootSeal != f.seal || rootKind != "rollback" || rootState != "live" || !rootExpiresNull {
			t.Fatalf("rollback retention root = %q %q %q %q %q %q expires_null=%t", rootTarget, rootCandidate, rootGeneration, rootSeal, rootKind, rootState, rootExpiresNull)
		}
		var eventType, operationType string
		if err := f.db.QueryRow(ctx, `SELECT event_type FROM event.event_log WHERE event_id=$1`, first.EventID).Scan(&eventType); err != nil {
			t.Fatal(err)
		}
		if eventType != "delivery.rollback.requested" {
			t.Fatalf("rollback event type = %q", eventType)
		}
		if err := f.db.QueryRow(ctx, `SELECT operation_type FROM platform.operation WHERE operation_id=$1`, first.ID).Scan(&operationType); err != nil {
			t.Fatal(err)
		}
		if operationType != "delivery.rollback.create" {
			t.Fatalf("rollback operation type = %q", operationType)
		}
		replay, err := f.coordinator.RollbackGeneration(ctx, request)
		if err != nil || replay.ID != first.ID || replay.EventID != first.EventID || replay.AuditID != first.AuditID {
			t.Fatalf("rollback replay = %#v, %v", replay, err)
		}
	})
	t.Run("missing root fails closed", func(t *testing.T) {
		f := newNativePGFixture(t)
		if _, err := f.coordinator.RollbackGeneration(t.Context(), nativeRollbackRequest(f, "rollback-missing-root")); !isNativeDeliveryConflict(err) {
			t.Fatalf("missing root error = %v", err)
		}
	})
	t.Run("expired root fails closed", func(t *testing.T) {
		f := newNativePGFixture(t)
		if _, err := f.repo.CreateRetentionRoot(t.Context(), deploymentpostgres.DeliveryRetentionRoot{RootID: f.generation, TargetID: f.targetID, CandidateID: f.candidate, GenerationID: f.generation, SnapshotSealID: f.seal, RootKind: "generation", State: "live", ExpiresAt: time.Now().UTC().Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.coordinator.RollbackGeneration(t.Context(), nativeRollbackRequest(f, "rollback-expired-root")); !isNativeDeliveryConflict(err) {
			t.Fatalf("expired root error = %v", err)
		}
	})
}

func TestNativeCoordinatorPostgresRollbackRootRetiresAtTerminalOutcome(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		f := newNativePGFixture(t)
		ctx := t.Context()
		if _, err := f.repo.CreateRetentionRoot(ctx, deploymentpostgres.DeliveryRetentionRoot{RootID: f.generation, TargetID: f.targetID, CandidateID: f.candidate, GenerationID: f.generation, SnapshotSealID: f.seal, RootKind: "generation", State: "live"}); err != nil {
			t.Fatal(err)
		}
		request := nativeRollbackRequest(f, "rollback-root-cancel")
		pending, err := f.coordinator.RollbackGeneration(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		cancelled, err := f.coordinator.CancelRequest(ctx, apiadapter.CancelRequest{Scope: apiadapter.Scope{Project: "project_sales", DeploymentID: pending.ID.String()}, Actor: request.PrincipalID, IdempotencyKey: "rollback-root-cancel-terminal"})
		if err != nil || cancelled.Status != apiadapter.StatusCancelled {
			t.Fatalf("cancel rollback publication = %#v, %v", cancelled, err)
		}
		assertRetiringRollbackRoot(t, f, pending.ID.String())

		// Both terminal replay paths are idempotent and preserve the retiring
		// state; neither re-admits the temporary root as live.
		replayedCancel, err := f.coordinator.CancelRequest(ctx, apiadapter.CancelRequest{Scope: apiadapter.Scope{Project: "project_sales", DeploymentID: pending.ID.String()}, Actor: request.PrincipalID, IdempotencyKey: "rollback-root-cancel-terminal"})
		if err != nil || replayedCancel.Status != apiadapter.StatusCancelled {
			t.Fatalf("cancel replay = %#v, %v", replayedCancel, err)
		}
		replayedRollback, err := f.coordinator.RollbackGeneration(ctx, request)
		if err != nil || replayedRollback.ID != pending.ID || replayedRollback.Status != "rejected" {
			t.Fatalf("rollback replay after cancellation = %#v, %v", replayedRollback, err)
		}
		assertRetiringRollbackRoot(t, f, pending.ID.String())
	})

	t.Run("activation", func(t *testing.T) {
		f := newNativePGFixture(t)
		ctx := t.Context()
		if _, err := f.repo.CreateRetentionRoot(ctx, deploymentpostgres.DeliveryRetentionRoot{RootID: f.generation, TargetID: f.targetID, CandidateID: f.candidate, GenerationID: f.generation, SnapshotSealID: f.seal, RootKind: "generation", State: "live"}); err != nil {
			t.Fatal(err)
		}
		request := nativeRollbackRequest(f, "rollback-root-activate")
		pending, err := f.coordinator.RollbackGeneration(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		active, err := f.coordinator.Activate(ctx, apiadapter.ActivateRequest{Scope: apiadapter.Scope{Project: "project_sales", DeploymentID: pending.ID.String()}, Actor: request.PrincipalID, IdempotencyKey: "rollback-root-activate-terminal"})
		if err != nil || active.Status != apiadapter.StatusActive {
			t.Fatalf("activate rollback publication = %#v, %v", active, err)
		}
		assertRetiringRollbackRoot(t, f, pending.ID.String())

		// Replaying the publication and activation after commit must not require
		// the temporary root to remain live.
		replayedRollback, err := f.coordinator.RollbackGeneration(ctx, request)
		if err != nil || replayedRollback.ID != pending.ID || replayedRollback.Status != "committed" {
			t.Fatalf("rollback replay after activation = %#v, %v", replayedRollback, err)
		}
		replayedActive, err := f.coordinator.Activate(ctx, apiadapter.ActivateRequest{Scope: apiadapter.Scope{Project: "project_sales", DeploymentID: pending.ID.String()}, Actor: request.PrincipalID, IdempotencyKey: "rollback-root-activate-terminal"})
		if err != nil || replayedActive.Status != apiadapter.StatusActive {
			t.Fatalf("activation replay = %#v, %v", replayedActive, err)
		}
		assertRetiringRollbackRoot(t, f, pending.ID.String())
	})
}

func assertRetiringRollbackRoot(t *testing.T, f *nativePGFixture, rootID string) {
	t.Helper()
	var state string
	var retiredAt, expiredAt *time.Time
	if err := f.db.QueryRow(t.Context(), `SELECT state,retired_at,expired_at FROM delivery.delivery_retention_root WHERE root_id=$1`, rootID).Scan(&state, &retiredAt, &expiredAt); err != nil {
		t.Fatal(err)
	}
	if state != "retiring" || retiredAt == nil || expiredAt != nil {
		t.Fatalf("rollback root lifecycle = state=%q retired_at=%v expired_at=%v", state, retiredAt, expiredAt)
	}
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

func TestNativeCoordinatorPostgresActivationPreCommitHookRollsBack(t *testing.T) {
	f := newNativePGFixture(t)
	created, err := f.coordinator.Create(t.Context(), nativeCreateRequest(f, "activation-hook-create"))
	if err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("qualification activation interrupted")
	hookCalls := 0
	f.coordinator.beforeActivationCommit = func(_ context.Context, publication deploymentpostgres.DeliveryPublication) error {
		hookCalls++
		if publication.PublicationID != created.ID || publication.State != "pending" {
			t.Fatalf("pre-commit publication = %#v", publication)
		}
		return interrupted
	}
	request := apiadapter.ActivateRequest{Scope: apiadapter.Scope{Project: "project_sales", DeploymentID: created.ID}, Actor: "operator", IdempotencyKey: "activation-hook-1"}
	if _, err := f.coordinator.Activate(t.Context(), request); !errors.Is(err, interrupted) {
		t.Fatalf("interrupted activation = %v, want %v", err, interrupted)
	}
	if hookCalls != 1 {
		t.Fatalf("pre-commit hook calls = %d, want 1", hookCalls)
	}
	pending, err := f.coordinator.Get(t.Context(), apiadapter.Scope{Project: "project_sales", DeploymentID: created.ID})
	if err != nil || pending.Status != apiadapter.StatusPending {
		t.Fatalf("publication after interruption = %#v, %v", pending, err)
	}
	target, err := f.repo.Target(t.Context(), f.targetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.TargetRevision != 1 || target.ActiveGenerationID != "" || target.ActivePublicationID != "" {
		t.Fatalf("target changed before activation commit = %#v", target)
	}

	f.coordinator.beforeActivationCommit = nil
	active, err := f.coordinator.Activate(t.Context(), request)
	if err != nil || active.Status != apiadapter.StatusActive {
		t.Fatalf("activation retry = %#v, %v", active, err)
	}
}

func TestActivationPreCommitHookAdapterPreservesContextAndFailure(t *testing.T) {
	if adaptActivationPreCommitHook(nil) != nil {
		t.Fatal("nil module hook produced a PostgreSQL hook")
	}
	wantErr := errors.New("qualification interrupted")
	ctx := t.Context()
	calls := 0
	hook := adaptActivationPreCommitHook(func(got context.Context) error {
		calls++
		if got != ctx {
			t.Fatal("activation hook context changed at the module boundary")
		}
		return wantErr
	})
	if err := hook(ctx, deploymentpostgres.DeliveryPublication{PublicationID: "publication-private"}); !errors.Is(err, wantErr) {
		t.Fatalf("adapted activation hook error = %v, want %v", err, wantErr)
	}
	if calls != 1 {
		t.Fatalf("activation hook calls = %d, want 1", calls)
	}
}

func TestNativeCoordinatorApprovedActivationRejectsTamperedPublicationActor(t *testing.T) {
	f := newNativePGFixture(t)
	created, err := f.coordinator.Create(t.Context(), nativeCreateRequest(f, "tampered-actor-create"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.coordinator.ActivateApprovedPublication(t.Context(), created.ID, "forged-publication-actor", "approval-decision-1"); !errors.Is(err, deployment.ErrNotFound) {
		t.Fatalf("tampered publication actor error = %v, want not found", err)
	}
	row, err := f.coordinator.Get(t.Context(), apiadapter.Scope{Project: "project_sales", DeploymentID: created.ID})
	if err != nil || row.Status != apiadapter.StatusPending {
		t.Fatalf("tampered actor changed publication = %#v, %v", row, err)
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
