package providerrestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/refresh/recovery"
)

func TestCoordinatorOrdersProvidersPublishesAndCompletesLedger(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	report, err := fixture.coordinator.Run(t.Context(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != StatusSucceeded || report.Admission.PublishedStatus != recoveryset.StatusPublished || !fixture.ledger.completed {
		t.Fatalf("report=%#v completed=%v", report, fixture.ledger.completed)
	}
	want := []string{"database:control", "database:ducklake", "object:ducklake", "object:serving-artifact", "verify"}
	if fmt.Sprint(fixture.calls) != fmt.Sprint(want) {
		t.Fatalf("provider order = %v, want %v", fixture.calls, want)
	}
	if len(fixture.ledger.phases) != 2 || fixture.ledger.phases[0] != "restore/started" || fixture.ledger.phases[1] != "restore/completed" {
		t.Fatalf("ledger phases = %v", fixture.ledger.phases)
	}
	if len(fixture.ledger.result.Evidence) != 1 || fixture.ledger.result.Evidence[0].SHA256 == "" {
		t.Fatalf("ledger evidence = %#v", fixture.ledger.result.Evidence)
	}
	if report.Verification.ControlStateDigest != report.Databases[0].StateDigest || report.Verification.DuckLakeStateDigest != report.Databases[1].StateDigest {
		t.Fatalf("verification digests are not bound to provider results: report=%#v", report)
	}
}

func TestCoordinatorResumeSkipsDurableProviderCheckpoint(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	point := fixture.set.CanonicalPoints()[0]
	control := matchingDatabaseResult(point, fixture.set.Catalog, fixture.now())
	fixture.seedCheckpoint(Report{SchemaVersion: ReportSchemaVersion, Kind: ReportKind, Status: StatusRunning, OccurrenceID: fixture.request.OccurrenceID, Fence: fixture.request.Fence, RecoverySetID: fixture.set.ID, FrontierDigest: fixture.set.FrontierDigest, TargetID: fixture.request.TargetID, Databases: []DatabaseResult{control}, StartedAt: fixture.now()})
	fixture.ledger.occurrence.RestoreStartedAt = fixture.now()
	if _, err := fixture.coordinator.Run(t.Context(), fixture.request); err != nil {
		t.Fatal(err)
	}
	for _, call := range fixture.calls {
		if call == "database:control" {
			t.Fatalf("durably checkpointed control restore was executed again: %v", fixture.calls)
		}
	}
}

func TestCoordinatorResumesAfterDurableCheckpointResponseLoss(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	fixture.ledger.checkpointErrAfterCommitCall = 2
	if _, err := fixture.coordinator.Run(t.Context(), fixture.request); !errors.Is(err, ErrIndeterminate) {
		t.Fatalf("lost checkpoint response error = %v", err)
	}
	checkpoint := fixture.mustCheckpoint(t)
	if len(checkpoint.Databases) != 1 || checkpoint.Databases[0].DatabaseRole != recoveryset.DatabaseControl {
		t.Fatalf("durable provider checkpoint = %#v", checkpoint.Databases)
	}
	fixture.calls = nil
	if _, err := fixture.coordinator.Run(t.Context(), fixture.request); err != nil {
		t.Fatal(err)
	}
	for _, call := range fixture.calls {
		if call == "database:control" {
			t.Fatalf("durable provider checkpoint was replayed after response loss: %v", fixture.calls)
		}
	}
}

func TestCoordinatorReadsBackCompletionAfterLostLedgerResponse(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	fixture.ledger.completeErrAfterCommit = errors.New("completion response lost")
	report, err := fixture.coordinator.Run(t.Context(), fixture.request)
	if !errors.Is(err, ErrIndeterminate) || report.Status != StatusSucceeded {
		t.Fatalf("lost completion response report=%#v err=%v", report, err)
	}
	if fixture.ledger.occurrence.Status != recovery.StatusSucceeded || fixture.ledger.completeCalls != 1 {
		t.Fatalf("durable completion status=%q calls=%d", fixture.ledger.occurrence.Status, fixture.ledger.completeCalls)
	}
	fixture.calls = nil
	report, err = fixture.coordinator.Run(t.Context(), fixture.request)
	if err != nil || report.Status != StatusSucceeded {
		t.Fatalf("completion readback report=%#v err=%v", report, err)
	}
	if len(fixture.calls) != 0 || fixture.ledger.completeCalls != 1 {
		t.Fatalf("completed retry replayed work: calls=%v completeCalls=%d", fixture.calls, fixture.ledger.completeCalls)
	}
}

func TestCoordinatorRejectsWrongTargetBeforeProviderEffects(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	fixture.request.TargetID = "target-other"
	if _, err := fixture.coordinator.Run(t.Context(), fixture.request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong target error = %v", err)
	}
	if len(fixture.calls) != 0 {
		t.Fatalf("wrong target ran provider effects: %v", fixture.calls)
	}
}

func TestCoordinatorFailsClosedOnObjectVersionMismatch(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	fixture.objects.mismatch = true
	report, err := fixture.coordinator.Run(t.Context(), fixture.request)
	if !errors.Is(err, ErrInconsistent) {
		t.Fatalf("object mismatch error = %v", err)
	}
	if report.Status != StatusFailed || !fixture.ledger.failed || fixture.ledger.completed {
		t.Fatalf("report=%#v failed=%v completed=%v", report, fixture.ledger.failed, fixture.ledger.completed)
	}
}

func TestCoordinatorClassifiesProviderErrorAsIndeterminate(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	fixture.databases.err = errors.New("provider response lost")
	report, err := fixture.coordinator.Run(t.Context(), fixture.request)
	if !errors.Is(err, ErrIndeterminate) || report.Status != StatusIndeterminate || !fixture.ledger.failed {
		t.Fatalf("report=%#v err=%v ledger.failed=%v", report, err, fixture.ledger.failed)
	}
}

func TestCoordinatorDoesNotCheckpointProviderEffectAfterFenceLoss(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	fixture.databases.afterRestore = func() {
		fixture.ledger.occurrence.Fence = recovery.Fence{Owner: "worker-successor", Generation: 2}
	}
	report, err := fixture.coordinator.Run(t.Context(), fixture.request)
	if !errors.Is(err, ErrIndeterminate) {
		t.Fatalf("fence-loss error = %v", err)
	}
	if len(report.Databases) != 0 || len(fixture.ledger.occurrence.Evidence) != 1 {
		t.Fatalf("stale provider effect became authoritative: report=%#v", report.Databases)
	}
	if fixture.ledger.completed || fixture.ledger.failed {
		t.Fatal("stale owner changed terminal ledger state")
	}
}

func TestCoordinatorUsesStableProviderIdempotencyKeys(t *testing.T) {
	first := newCoordinatorFixture(t)
	if _, err := first.coordinator.Run(t.Context(), first.request); err != nil {
		t.Fatal(err)
	}
	second := newCoordinatorFixture(t)
	if _, err := second.coordinator.Run(t.Context(), second.request); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(first.databases.keys) != fmt.Sprint(second.databases.keys) || fmt.Sprint(first.objects.keys) != fmt.Sprint(second.objects.keys) {
		t.Fatalf("provider keys changed across replay: databases %v/%v objects %v/%v", first.databases.keys, second.databases.keys, first.objects.keys, second.objects.keys)
	}
	for _, key := range append(first.databases.keys, first.objects.keys...) {
		if len(key) != len("fai981-")+64 {
			t.Fatalf("provider idempotency key %q is not canonical", key)
		}
	}
}

func TestCoordinatorRejectsCheckpointWrittenAfterFenceLoss(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	oldFence := fixture.request.Fence
	successorFence := recovery.Fence{Owner: "worker-successor", Generation: oldFence.Generation + 1}
	fixture.store.afterSave = func(report Report) {
		if len(report.Databases) > 0 {
			// Simulate lease expiry recovery and a successor ClaimNext between
			// the coordinator's post-effect check and the fenced pointer update.
			fixture.ledger.occurrence.LeaseExpiresAt = fixture.clock
			fixture.ledger.occurrence.Fence = successorFence
			fixture.ledger.occurrence.LeaseExpiresAt = fixture.clock.Add(time.Hour)
		}
	}
	if _, err := fixture.coordinator.Run(t.Context(), fixture.request); !errors.Is(err, ErrIndeterminate) {
		t.Fatalf("stale checkpoint error = %v", err)
	}
	checkpoint := fixture.mustCheckpoint(t)
	if checkpoint.Fence != oldFence || len(checkpoint.Databases) != 0 {
		t.Fatalf("stale provider checkpoint became authoritative: %#v", checkpoint)
	}

	fixture.store.afterSave = nil
	fixture.calls = nil
	fixture.request.Fence = successorFence
	report, err := fixture.coordinator.Run(t.Context(), fixture.request)
	if err != nil || report.Status != StatusSucceeded {
		t.Fatalf("successor report=%#v err=%v", report, err)
	}
	if report.Fence != successorFence || len(fixture.calls) == 0 || fixture.calls[0] != "database:control" {
		t.Fatalf("successor accepted stale checkpoint: report fence=%#v calls=%v", report.Fence, fixture.calls)
	}
}

func TestCoordinatorReconcilesCommittedPublicationAfterResponseLoss(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	fixture.sets.publishErrAfterCommit = errors.New("publication readback response lost")
	report, err := fixture.coordinator.Run(t.Context(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != StatusSucceeded || report.Admission.PublishedStatus != recoveryset.StatusPublished || !fixture.ledger.completed || fixture.ledger.failed {
		t.Fatalf("report=%#v completed=%v failed=%v", report, fixture.ledger.completed, fixture.ledger.failed)
	}
}

func TestCoordinatorRejectsVerificationDigestMismatch(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	fixture.verifier.mismatch = true
	report, err := fixture.coordinator.Run(t.Context(), fixture.request)
	if !errors.Is(err, ErrInconsistent) {
		t.Fatalf("verification mismatch error = %v", err)
	}
	if report.Status != StatusFailed || fixture.ledger.completed {
		t.Fatalf("inconsistent verification completed recovery: %#v", report)
	}
}

func TestCoordinatorRestoresSharedClusterOnceAcrossRestart(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	points := fixture.set.CanonicalPoints()
	points[1].ClusterIdentity = points[0].ClusterIdentity
	points[1].RecoveryIdentity = points[0].RecoveryIdentity
	fixture.set.ClusterPoints = points
	fixture.set.FrontierDigest = ""
	normalized, err := fixture.set.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	normalized.FrontierDigest, err = normalized.Digest()
	if err != nil {
		t.Fatal(err)
	}
	fixture.set, fixture.sets.set = normalized, normalized
	fixture.ledger.checkpointErrAfterCommitCall = 2
	if _, err := fixture.coordinator.Run(t.Context(), fixture.request); !errors.Is(err, ErrIndeterminate) {
		t.Fatalf("checkpoint response-loss error = %v", err)
	}
	if fixture.databases.clusterCalls != 1 || len(fixture.databases.keys) != 1 {
		t.Fatalf("shared cluster restore calls=%d keys=%v", fixture.databases.clusterCalls, fixture.databases.keys)
	}
	restarted, err := New(Dependencies{Ledger: fixture.ledger, Sets: fixture.sets, Databases: fixture.databases, Objects: fixture.objects, Verifier: fixture.verifier, Evidence: fixture.store, Now: fixture.now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Run(t.Context(), fixture.request); err != nil {
		t.Fatal(err)
	}
	if fixture.databases.clusterCalls != 1 || len(fixture.databases.keys) != 1 {
		t.Fatalf("restart repeated shared PITR: calls=%d keys=%v", fixture.databases.clusterCalls, fixture.databases.keys)
	}
}

func TestCoordinatorRejectsMutatedCompletedEvidence(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	store := FileEvidenceStore{Root: t.TempDir()}
	coordinator, err := New(Dependencies{Ledger: fixture.ledger, Sets: fixture.sets, Databases: fixture.databases, Objects: fixture.objects, Verifier: fixture.verifier, Evidence: store, Now: fixture.now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Run(t.Context(), fixture.request); err != nil {
		t.Fatal(err)
	}
	if len(fixture.ledger.occurrence.Evidence) != 1 {
		t.Fatalf("completion evidence = %#v", fixture.ledger.occurrence.Evidence)
	}
	reference := fixture.ledger.occurrence.Evidence[0]
	parsed, err := url.Parse(reference.URI)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(parsed.Path)
	if err != nil {
		t.Fatal(err)
	}
	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	report.TargetID += "-modified"
	modified, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	modified = append(modified, '\n')
	if err := os.WriteFile(parsed.Path, modified, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Run(t.Context(), fixture.request); !errors.Is(err, ErrInconsistent) {
		t.Fatalf("mutated completed evidence error = %v", err)
	}
}

type coordinatorFixture struct {
	coordinator *Coordinator
	request     Request
	set         recoveryset.RecoverySet
	ledger      *fakeLedger
	sets        *fakeSets
	store       *memoryEvidence
	databases   *fakeDatabases
	objects     *fakeObjects
	verifier    *fakeVerifier
	calls       []string
	clock       time.Time
}

func newCoordinatorFixture(t *testing.T) *coordinatorFixture {
	t.Helper()
	fixture := &coordinatorFixture{clock: time.Date(2026, 9, 21, 14, 0, 0, 0, time.UTC)}
	fixture.set = providerRestoreSet(t)
	fixture.request = Request{OccurrenceID: "recovery-occurrence-test", Fence: recovery.Fence{Owner: "worker-a", Generation: 1}, RecoverySetID: fixture.set.ID, TargetID: fixture.set.Delivery.TargetID, ValidationAttemptID: "018f3f83-7b2f-7b37-9f9e-000000009981", Validator: "fai981-validator", Publisher: "fai981-publisher"}
	fixture.ledger = &fakeLedger{occurrence: recovery.Occurrence{ID: fixture.request.OccurrenceID, Operation: recovery.OperationRestore, TargetScope: fixture.request.TargetID, Status: recovery.StatusRunning, Fence: fixture.request.Fence, PlannedAt: fixture.clock.Add(-time.Hour)}}
	fixture.sets = &fakeSets{set: fixture.set}
	fixture.store = &memoryEvidence{}
	fixture.databases = &fakeDatabases{calls: &fixture.calls, now: fixture.now}
	fixture.objects = &fakeObjects{calls: &fixture.calls, now: fixture.now}
	fixture.verifier = &fakeVerifier{calls: &fixture.calls, now: fixture.now}
	coordinator, err := New(Dependencies{Ledger: fixture.ledger, Sets: fixture.sets, Databases: fixture.databases, Objects: fixture.objects, Verifier: fixture.verifier, Evidence: fixture.store, Now: fixture.now})
	if err != nil {
		t.Fatal(err)
	}
	fixture.coordinator = coordinator
	return fixture
}

func (fixture *coordinatorFixture) now() time.Time {
	fixture.clock = fixture.clock.Add(time.Millisecond)
	return fixture.clock
}

func (fixture *coordinatorFixture) seedCheckpoint(report Report) {
	reference, err := fixture.store.Save(context.Background(), report)
	if err != nil {
		panic(err)
	}
	fixture.ledger.occurrence.Evidence = []recovery.EvidenceReference{reference}
}

func (fixture *coordinatorFixture) mustCheckpoint(t *testing.T) Report {
	t.Helper()
	if len(fixture.ledger.occurrence.Evidence) != 1 {
		t.Fatalf("checkpoint references = %#v", fixture.ledger.occurrence.Evidence)
	}
	report, err := fixture.store.Load(t.Context(), fixture.ledger.occurrence.Evidence[0])
	if err != nil {
		t.Fatal(err)
	}
	return report
}

type fakeLedger struct {
	recovery.Repository
	occurrence                   recovery.Occurrence
	phases                       []string
	result                       recovery.Result
	completed                    bool
	failed                       bool
	completeCalls                int
	completeErrAfterCommit       error
	checkpointCalls              int
	checkpointErrAfterCommitCall int
}

func (ledger *fakeLedger) Occurrence(context.Context, string) (recovery.Occurrence, error) {
	return ledger.occurrence, nil
}
func (ledger *fakeLedger) RecordPhase(_ context.Context, _ string, _ recovery.Fence, phase, event string, at time.Time) error {
	ledger.phases = append(ledger.phases, phase+"/"+event)
	if phase == "restore" && event == "started" {
		ledger.occurrence.RestoreStartedAt = at
	}
	if phase == "restore" && event == "completed" {
		ledger.occurrence.RestoreCompletedAt = at
	}
	return nil
}
func (ledger *fakeLedger) RecordCheckpoint(_ context.Context, id string, fence recovery.Fence, at time.Time, reference recovery.EvidenceReference) error {
	ledger.checkpointCalls++
	if ledger.occurrence.ID != id || ledger.occurrence.Fence != fence || ledger.occurrence.Status != recovery.StatusRunning || (!ledger.occurrence.LeaseExpiresAt.IsZero() && !at.Before(ledger.occurrence.LeaseExpiresAt)) {
		return recovery.ErrFenced
	}
	ledger.occurrence.Evidence = []recovery.EvidenceReference{reference}
	if ledger.checkpointErrAfterCommitCall == ledger.checkpointCalls {
		return errors.New("checkpoint response lost")
	}
	return nil
}
func (ledger *fakeLedger) Complete(_ context.Context, _ string, _ recovery.Fence, _ time.Time, result recovery.Result) error {
	ledger.completeCalls++
	ledger.completed, ledger.result = true, result
	ledger.occurrence.Status = recovery.StatusSucceeded
	ledger.occurrence.Evidence = append([]recovery.EvidenceReference(nil), result.Evidence...)
	if ledger.completeErrAfterCommit != nil {
		err := ledger.completeErrAfterCommit
		ledger.completeErrAfterCommit = nil
		return err
	}
	return nil
}
func (ledger *fakeLedger) Fail(_ context.Context, _ string, _ recovery.Fence, _ time.Time, result recovery.Result, _ error) error {
	ledger.failed, ledger.result = true, result
	ledger.occurrence.Status = recovery.StatusFailed
	ledger.occurrence.Evidence = append([]recovery.EvidenceReference(nil), result.Evidence...)
	return nil
}

type fakeSets struct {
	RecoverySetAuthority
	set                   recoveryset.RecoverySet
	attempt               recoveryset.ValidationAttempt
	result                recoveryset.ValidationResult
	publishErrAfterCommit error
}

func (sets *fakeSets) ReadExact(context.Context, string) (recoveryset.RecoverySet, error) {
	return sets.set, nil
}
func (sets *fakeSets) ValidationResult(context.Context, string) (recoveryset.ValidationResult, error) {
	return sets.result, nil
}
func (sets *fakeSets) BeginValidation(_ context.Context, attempt recoveryset.ValidationAttempt) (recoveryset.ValidationAttempt, error) {
	sets.attempt = attempt
	return attempt, nil
}
func (sets *fakeSets) RecordValidationResult(_ context.Context, result recoveryset.ValidationResult) error {
	sets.result = result
	return nil
}
func (sets *fakeSets) CompleteValidation(_ context.Context, attempt recoveryset.ValidationAttempt) error {
	sets.attempt = attempt
	return nil
}
func (sets *fakeSets) Publish(_ context.Context, _ string, _ string, _ int64, attemptID string) (recoveryset.RecoverySet, error) {
	sets.set.Status = recoveryset.StatusPublished
	sets.set.PublishedValidationAttemptID = attemptID
	if sets.publishErrAfterCommit != nil {
		return recoveryset.RecoverySet{}, sets.publishErrAfterCommit
	}
	return sets.set, nil
}

type memoryEvidence struct {
	reports   map[string]Report
	saves     int
	afterSave func(Report)
}

func (store *memoryEvidence) Load(_ context.Context, reference recovery.EvidenceReference) (Report, error) {
	report, ok := store.reports[reference.URI]
	if !ok {
		return Report{}, os.ErrNotExist
	}
	return report, nil
}
func (store *memoryEvidence) Save(_ context.Context, report Report) (recovery.EvidenceReference, error) {
	if store.reports == nil {
		store.reports = map[string]Report{}
	}
	store.saves++
	reference := recovery.EvidenceReference{Kind: "provider-restore", URI: fmt.Sprintf("file:///evidence/provider-restore-%d.json", store.saves), SHA256: fmt.Sprintf("%064x", store.saves)}
	store.reports[reference.URI] = report
	if store.afterSave != nil {
		store.afterSave(report)
	}
	return reference, nil
}

type fakeDatabases struct {
	calls        *[]string
	now          func() time.Time
	err          error
	keys         []string
	clusterCalls int
	afterRestore func()
}

func (provider *fakeDatabases) RestoreCluster(_ context.Context, request DatabaseRequest) ([]DatabaseResult, error) {
	provider.clusterCalls++
	provider.keys = append(provider.keys, request.IdempotencyKey)
	if provider.err != nil {
		return nil, provider.err
	}
	results := make([]DatabaseResult, 0, len(request.Points))
	for _, point := range request.Points {
		*provider.calls = append(*provider.calls, "database:"+string(point.DatabaseRole))
		results = append(results, matchingDatabaseResult(point, request.Catalog, provider.now()))
	}
	if provider.afterRestore != nil {
		provider.afterRestore()
	}
	return results, nil
}

func matchingDatabaseResult(point recoveryset.ClusterRecoveryPoint, catalog recoveryset.CatalogCommit, now time.Time) DatabaseResult {
	result := DatabaseResult{Provider: "postgresql", OperationID: "postgres-operation-" + string(point.DatabaseRole), DatabaseRole: point.DatabaseRole, ClusterIdentity: point.ClusterIdentity, DatabaseIdentity: point.DatabaseIdentity, RecoveryIdentity: point.RecoveryIdentity, StateDigest: "sha256:" + strings64("b"), StartedAt: now, CompletedAt: now.Add(time.Millisecond)}
	if point.DatabaseRole == recoveryset.DatabaseDuckLake {
		result.Catalog = &catalog
	}
	return result
}

type fakeObjects struct {
	calls    *[]string
	now      func() time.Time
	mismatch bool
	keys     []string
}

func (provider *fakeObjects) RestoreObject(_ context.Context, request ObjectRequest) (ObjectResult, error) {
	*provider.calls = append(*provider.calls, "object:"+request.Root.Kind)
	provider.keys = append(provider.keys, request.IdempotencyKey)
	version := request.Root.VersionID
	if provider.mismatch {
		version = "wrong-version"
	}
	now := provider.now()
	return ObjectResult{Provider: "s3", OperationID: "s3-operation-" + request.Root.Kind, Kind: request.Root.Kind, URI: request.Root.URI, RequiredVersionID: request.Root.VersionID, ObservedVersionID: version, Digest: request.Root.Digest, StartedAt: now, CompletedAt: now.Add(time.Millisecond)}, nil
}

type fakeVerifier struct {
	calls    *[]string
	now      func() time.Time
	mismatch bool
}

func (verifier *fakeVerifier) Verify(_ context.Context, request VerificationRequest) (VerificationResult, error) {
	*verifier.calls = append(*verifier.calls, "verify")
	digests := map[recoveryset.DatabaseRole]string{}
	for _, database := range request.Databases {
		digests[database.DatabaseRole] = database.StateDigest
	}
	if verifier.mismatch {
		digests[recoveryset.DatabaseDuckLake] = "sha256:" + strings64("d")
	}
	return VerificationResult{ProviderOperationID: "verification-operation", ControlStateDigest: digests[recoveryset.DatabaseControl], DuckLakeStateDigest: digests[recoveryset.DatabaseDuckLake], Catalog: request.Set.Catalog, ObjectsConsistent: true, Ready: true, VerifiedAt: verifier.now()}, nil
}

func providerRestoreSet(t *testing.T) recoveryset.RecoverySet {
	t.Helper()
	compatibility := physicalpool.Compatibility{DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "ducklake:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	compatibilityDigest, err := compatibility.Digest()
	if err != nil {
		t.Fatal(err)
	}
	set := recoveryset.RecoverySet{
		ID: "018f3f83-7b2f-7b37-9f9e-000000009980", SchemaVersion: recoveryset.SchemaVersion,
		ClusterPoints: []recoveryset.ClusterRecoveryPoint{{DatabaseRole: recoveryset.DatabaseControl, ClusterIdentity: "postgres-provider-a", DatabaseIdentity: "control-restored", RecoveryIdentity: "backup:provider-point-1"}, {DatabaseRole: recoveryset.DatabaseDuckLake, ClusterIdentity: "postgres-provider-b", DatabaseIdentity: "ducklake-restored", RecoveryIdentity: "backup:provider-point-1"}},
		Delivery:      recoveryset.DeliveryPointer{TargetID: "target-production-a", GenerationID: "018f3f83-7b2f-7b37-9f9e-000000009982", PublicationID: "018f3f83-7b2f-7b37-9f9e-000000009983", TargetRevision: 7},
		Serving:       recoveryset.SnapshotSeal{SealID: "018f3f83-7b2f-7b37-9f9e-000000009984", PhysicalPoolID: "pool-a", TenantDomain: "tenant-a", Region: "eu-central", EncryptionDomain: "kms-key-reference-a", ObjectNamespace: "objects/target-a", CatalogDatabase: "ducklake-restored", CatalogID: "catalog-a", CatalogUUID: "catalog-uuid-a", CatalogVersion: 9, DuckLakeSnapshotID: 42, RelationManifestDigest: "sha256:" + strings64("1"), RelationNamespace: "candidate/7", ClosureDigest: "sha256:" + strings64("2"), ObjectRoot: "s3://provider-bucket/ducklake/root.json", ObjectRootDigest: "sha256:" + strings64("3"), ArtifactRoot: "s3://provider-bucket/artifacts/root.json", ArtifactRootDigest: "sha256:" + strings64("4"), ServingArtifactID: "artifact-a", ServingArtifactDigest: "sha256:" + strings64("5"), CompiledGraphDigest: "sha256:" + strings64("6"), CompiledConfigDigest: "sha256:" + strings64("7"), SecurityDomainFingerprint: "sha256:" + strings64("8"), RequestDigest: "sha256:" + strings64("9"), PlanDigest: "sha256:" + strings64("a"), CompatibilityDigest: compatibilityDigest, DuckDBVersion: "1", RuntimeVersion: "1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1"},
		Catalog:       recoveryset.CatalogCommit{CatalogID: "catalog-a", CatalogDatabase: "ducklake-restored", CatalogUUID: "catalog-uuid-a", CatalogVersion: 9, SnapshotID: 42},
		ObjectRoots:   []recoveryset.ObjectRoot{{Kind: recoveryset.ObjectRootDuckLake, URI: "s3://provider-bucket/ducklake/root.json", VersionID: "ducklake-version-1", Digest: "sha256:" + strings64("3"), ProviderRecoveryFrontier: "version:ducklake-version-1"}, {Kind: recoveryset.ObjectRootServingArtifact, URI: "s3://provider-bucket/artifacts/root.json", VersionID: "artifact-version-1", Digest: "sha256:" + strings64("4"), ProviderRecoveryFrontier: "version:artifact-version-1"}},
		Compatibility: compatibility, FenceEpoch: 1, AuditIdentity: "fai981-audit", Status: recoveryset.StatusPrepared, CreatedBy: "fai981-operator", CreatedAt: time.Date(2026, 9, 21, 13, 0, 0, 0, time.UTC),
	}
	normalized, err := set.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	normalized.FrontierDigest, err = normalized.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func strings64(value string) string {
	result := ""
	for len(result) < 64 {
		result += value
	}
	return result[:64]
}
