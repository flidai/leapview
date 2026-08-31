package offline

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
)

type fakeLock struct {
	released int
}

func (lock *fakeLock) Release() error {
	lock.released++
	return nil
}

type fakeLocker struct {
	acquired int
	lock     fakeLock
}

func (locker *fakeLocker) Acquire(context.Context) (Lock, error) {
	locker.acquired++
	return &locker.lock, nil
}

type fakeState struct {
	environment    string
	environmentErr error
	existing       bool
	initialized    bool
	bound          string
}

func (state *fakeState) Environment(context.Context) (string, error) {
	return state.environment, state.environmentErr
}

func (state *fakeState) ExistingEnvironment(context.Context) (string, bool, error) {
	return state.environment, state.existing, state.environmentErr
}

func (state *fakeState) BindEnvironment(_ context.Context, environment string) error {
	state.environment, state.bound, state.environmentErr = environment, environment, nil
	return nil
}

func (state *fakeState) Initialized(context.Context) (bool, error) {
	return state.initialized, nil
}

type memoryRecovery struct {
	contents    []byte
	removed     int
	removeErr   error
	removeErrAt int
}

func (recovery *memoryRecovery) Read() ([]byte, error) {
	if recovery.contents == nil {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), recovery.contents...), nil
}

func (recovery *memoryRecovery) Write(contents []byte) error {
	recovery.contents = append([]byte(nil), contents...)
	return nil
}

func (recovery *memoryRecovery) Remove() error {
	recovery.removed++
	if recovery.removeErr != nil && (recovery.removeErrAt == 0 || recovery.removed == recovery.removeErrAt) {
		return recovery.removeErr
	}
	if recovery.contents == nil {
		return os.ErrNotExist
	}
	recovery.contents = nil
	return nil
}

type fakeInitializer struct {
	calls  int
	input  InitializationInput
	result InitialCredentials
	err    error
}

func (initializer *fakeInitializer) Initialize(
	_ context.Context,
	input InitializationInput,
	prepare func(InitialCredentials) error,
) (InitialCredentials, error) {
	initializer.calls++
	initializer.input = input
	if err := prepare(initializer.result); err != nil {
		return InitialCredentials{}, err
	}
	return initializer.result, initializer.err
}

type fakeRetention struct {
	calls  int
	policy RetentionPolicy
	result RetentionResult
}

type fakeAuditOutbox struct {
	status   AuditOutboxStatus
	requeued string
}

func (outbox *fakeAuditOutbox) Status(context.Context) (AuditOutboxStatus, error) {
	return outbox.status, nil
}

func (outbox *fakeAuditOutbox) Requeue(_ context.Context, eventID string) error {
	outbox.requeued = eventID
	return nil
}

func (outbox *fakeAuditOutbox) RequeueExact(ctx context.Context, request AuditOutboxRequest) error {
	return outbox.Requeue(ctx, request.RequeueEventID)
}

func (retention *fakeRetention) Prune(_ context.Context, policy RetentionPolicy) (RetentionResult, error) {
	retention.calls++
	retention.policy = policy
	return retention.result, nil
}

type fakeStorage struct {
	environment string
	dryRun      bool
}

func (storage *fakeStorage) Cleanup(_ context.Context, environment string, dryRun bool, _ io.Writer) error {
	storage.environment, storage.dryRun = environment, dryRun
	return nil
}

type fakeArchive struct {
	backupDatabase  BackupOptions
	backupInstance  BackupOptions
	restoreDatabase RestoreOptions
	restoreInstance RestoreOptions
	preflight       RestoreOptions
	preflightResult RestorePreflightResult
	preflightErr    error
}

type fakeDeliveryRepair struct {
	calls      int
	request    DeliveryRepairRequest
	auditCalls int
	audit      DeliveryAuditRequest
}

func (repair *fakeDeliveryRepair) RepairDeliveryRoot(_ context.Context, request DeliveryRepairRequest, _ io.Writer) error {
	repair.calls++
	repair.request = request
	return nil
}

func (repair *fakeDeliveryRepair) AuditDeliveryRoots(_ context.Context, request DeliveryAuditRequest) (DeliveryAuditResult, error) {
	repair.auditCalls++
	repair.audit = request
	return DeliveryAuditResult{PhysicalPoolID: request.PhysicalPoolID}, nil
}

func (archive *fakeArchive) BackupDatabase(_ context.Context, options BackupOptions) error {
	archive.backupDatabase = options
	return nil
}
func (archive *fakeArchive) BackupInstance(_ context.Context, options BackupOptions) error {
	archive.backupInstance = options
	return nil
}
func (archive *fakeArchive) RestoreDatabase(_ context.Context, options RestoreOptions) error {
	archive.restoreDatabase = options
	return nil
}
func (archive *fakeArchive) RestoreInstance(_ context.Context, options RestoreOptions) error {
	archive.restoreInstance = options
	return nil
}
func (archive *fakeArchive) PreflightInstance(_ context.Context, options RestoreOptions) (RestorePreflightResult, error) {
	archive.preflight = options
	return archive.preflightResult, archive.preflightErr
}

func TestInitializeOwnsValidationRecoveryAndAccessSequencing(t *testing.T) {
	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	locker := &fakeLocker{}
	state := &fakeState{environmentErr: ErrStateNotFound}
	recovery := &memoryRecovery{}
	initializer := &fakeInitializer{result: InitialCredentials{
		Email: "owner@example.com", TemporaryPassword: "temporary",
		PublisherToken: "publisher", PublisherTokenExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339),
	}}
	service := New(Config{
		HomeDir: "/instance", Production: true, BootstrapEmail: "owner@example.com", Environment: "prod",
	}, Dependencies{
		Locker: locker, State: state, Recovery: recovery, Initializer: initializer,
		Now: func() time.Time { return now },
	})
	var out bytes.Buffer
	if err := service.Initialize(context.Background(), InitializeRequest{Format: "json"}, &out); err != nil {
		t.Fatal(err)
	}
	if locker.acquired != 1 || locker.lock.released != 1 {
		t.Fatalf("lock lifecycle = acquired %d released %d", locker.acquired, locker.lock.released)
	}
	if state.bound != "prod" {
		t.Fatalf("bound environment = %q", state.bound)
	}
	if initializer.calls != 1 || initializer.input.Email != "owner@example.com" ||
		initializer.input.Environment != "prod" || !initializer.input.Now.Equal(now) {
		t.Fatalf("initialization input = %#v calls=%d", initializer.input, initializer.calls)
	}
	if !bytes.Equal(out.Bytes(), recovery.contents) {
		t.Fatalf("delivered credentials and recovery differ:\nout=%s\nrecovery=%s", out.Bytes(), recovery.contents)
	}
	if _, err := DecodeInitialCredentials(out.Bytes()); err != nil {
		t.Fatalf("decode initialized credentials: %v", err)
	}
}

func TestInitializeReplaysPreparedCredentialsWithoutMutatingAccess(t *testing.T) {
	contents := []byte(`{"email":"owner@example.com","temporaryPassword":"temporary","publisherToken":"publisher","publisherTokenExpiresAt":"2026-07-30T07:00:00Z"}` + "\n")
	locker := &fakeLocker{}
	initializer := &fakeInitializer{}
	service := New(Config{Production: true, BootstrapEmail: "owner@example.com"}, Dependencies{
		Locker:      locker,
		State:       &fakeState{environment: "prod", initialized: true},
		Recovery:    &memoryRecovery{contents: contents},
		Initializer: initializer,
	})
	var out bytes.Buffer
	if err := service.Initialize(context.Background(), InitializeRequest{Format: "json"}, &out); err != nil {
		t.Fatal(err)
	}
	if initializer.calls != 0 || !bytes.Equal(out.Bytes(), contents) {
		t.Fatalf("initializer calls=%d output=%q", initializer.calls, out.String())
	}
}

func TestInitializeReportsCredentialCleanupFailureAfterMutationFailure(t *testing.T) {
	mutationErr := errors.New("commit failed")
	cleanupErr := errors.New("remove denied")
	recovery := &memoryRecovery{removeErr: cleanupErr, removeErrAt: 2}
	service := New(Config{Production: true, BootstrapEmail: "owner@example.com", Environment: "prod"}, Dependencies{
		Locker:   &fakeLocker{},
		State:    &fakeState{environment: "prod", existing: true},
		Recovery: recovery,
		Initializer: &fakeInitializer{result: InitialCredentials{
			Email: "owner@example.com", TemporaryPassword: "temporary", PublisherToken: "publisher",
			PublisherTokenExpiresAt: "2026-07-30T07:00:00Z",
		}, err: mutationErr},
	})

	err := service.Initialize(context.Background(), InitializeRequest{Format: "json"}, io.Discard)
	if !errors.Is(err, mutationErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("initialization cleanup error = %v, want mutation and cleanup errors", err)
	}
	if recovery.removed != 2 || len(recovery.contents) == 0 {
		t.Fatalf("failed cleanup evidence removed=%d contents=%q", recovery.removed, recovery.contents)
	}
}

func TestMaintenanceOwnsPolicyAndExclusiveApplySemantics(t *testing.T) {
	locker := &fakeLocker{}
	retention := &fakeRetention{result: RetentionResult{
		AuditEventsDeleted: 1, DeliveredAuditIntentsDeleted: 8, QueryEventsDeleted: 2, ArchivedAgentConversationsDeleted: 3,
		ExpiredOAuthStatesDeleted: 4, StaleSessionsDeleted: 5,
		StaleAPITokensDeleted: 6, StaleServicePrincipalSecretsDeleted: 7,
	}}
	service := New(Config{}, Dependencies{Locker: locker, Retention: retention})
	var out bytes.Buffer
	request := MaintenanceRequest{AuditDays: 10, QueryDays: 11, ArchivedAgentDays: 12, AuthStateDays: 13}
	if err := service.Maintenance(context.Background(), request, &out); err != nil {
		t.Fatal(err)
	}
	if locker.acquired != 0 || !retention.policy.DryRun ||
		retention.policy.AuditEventsMaxAge != 10*24*time.Hour ||
		retention.policy.AuthStateMaxAge != 13*24*time.Hour {
		t.Fatalf("dry-run policy = %#v lock=%d", retention.policy, locker.acquired)
	}
	if !strings.Contains(out.String(), "mode: dry-run") || !strings.Contains(out.String(), "delivered audit intents: 8") || !strings.Contains(out.String(), "stale service principal secrets: 7") {
		t.Fatalf("maintenance output = %q", out.String())
	}
	request.Apply = true
	if err := service.Maintenance(context.Background(), request, io.Discard); err != nil {
		t.Fatal(err)
	}
	if locker.acquired != 1 || locker.lock.released != 1 || retention.policy.DryRun {
		t.Fatalf("apply policy = %#v lock=%d/%d", retention.policy, locker.acquired, locker.lock.released)
	}
}

func TestAuditOutboxStatusAndExactRecovery(t *testing.T) {
	locker := &fakeLocker{}
	outbox := &fakeAuditOutbox{status: AuditOutboxStatus{Pending: 2, Poison: 1, OldestUndeliveredAge: 3 * time.Minute}}
	service := New(Config{}, Dependencies{Locker: locker, AuditOutbox: outbox})
	var out bytes.Buffer
	if err := service.AuditOutbox(context.Background(), AuditOutboxRequest{}, &out); err != nil {
		t.Fatal(err)
	}
	if locker.acquired != 0 || !strings.Contains(out.String(), "pending: 2") || !strings.Contains(out.String(), "poison: 1") {
		t.Fatalf("status output=%q lock=%d", out.String(), locker.acquired)
	}
	if err := service.AuditOutbox(context.Background(), AuditOutboxRequest{RequeueEventID: "event-1"}, io.Discard); err == nil {
		t.Fatal("requeue without apply succeeded")
	}
	if err := service.AuditOutbox(context.Background(), AuditOutboxRequest{RequeueEventID: " event-1 ", Apply: true}, &out); err != nil {
		t.Fatal(err)
	}
	if locker.acquired != 1 || outbox.requeued != "event-1" || !strings.Contains(out.String(), "requeued event: event-1") {
		t.Fatalf("requeue=%q lock=%d output=%q", outbox.requeued, locker.acquired, out.String())
	}
}

func TestStorageCleanupResolvesEnvironmentAndLocksOnlyForApply(t *testing.T) {
	locker := &fakeLocker{}
	storage := &fakeStorage{}
	service := New(Config{Environment: "prod"}, Dependencies{
		Locker: locker, State: &fakeState{environment: "prod"}, Storage: storage,
	})
	if err := service.StorageCleanup(context.Background(), StorageCleanupRequest{}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if storage.environment != "prod" || !storage.dryRun || locker.acquired != 0 {
		t.Fatalf("dry-run cleanup = environment %q dry=%v lock=%d", storage.environment, storage.dryRun, locker.acquired)
	}
	if err := service.StorageCleanup(context.Background(), StorageCleanupRequest{Apply: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if storage.dryRun || locker.acquired != 1 || locker.lock.released != 1 {
		t.Fatalf("apply cleanup = dry=%v lock=%d/%d", storage.dryRun, locker.acquired, locker.lock.released)
	}
}

func TestDeliveryRepairLocksOnlyForApplyAndPassesBoundedAction(t *testing.T) {
	locker := &fakeLocker{}
	repair := &fakeDeliveryRepair{}
	root := deployment.DeliveryRoot{PhysicalPoolID: "pool", Kind: "candidate", SourceID: "candidate", CatalogDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ObjectKey: "catalogs/a.ducklake"}
	service := New(Config{}, Dependencies{Locker: locker, DeliveryRepair: repair})
	request := DeliveryRepairRequest{Root: root, Action: "quarantine"}
	if err := service.RepairDeliveryRoot(context.Background(), request, io.Discard); err != nil {
		t.Fatal(err)
	}
	if repair.calls != 1 || repair.request.Apply || locker.acquired != 0 {
		t.Fatalf("dry-run repair calls=%d apply=%v locks=%d", repair.calls, repair.request.Apply, locker.acquired)
	}
	request.Apply = true
	if err := service.RepairDeliveryRoot(context.Background(), request, io.Discard); err != nil {
		t.Fatal(err)
	}
	if repair.calls != 2 || !repair.request.Apply || locker.acquired != 1 || locker.lock.released != 1 {
		t.Fatalf("apply repair calls=%d apply=%v locks=%d/%d", repair.calls, repair.request.Apply, locker.acquired, locker.lock.released)
	}
	request.Action = "delete_object"
	if err := service.RepairDeliveryRoot(context.Background(), request, io.Discard); err == nil {
		t.Fatal("unbounded repair action unexpectedly accepted")
	}
}

func TestDeliveryAuditIsReadOnlyAndRequiresPool(t *testing.T) {
	locker := &fakeLocker{}
	repair := &fakeDeliveryRepair{}
	service := New(Config{}, Dependencies{Locker: locker, DeliveryRepair: repair})
	if err := service.AuditDeliveryRoots(context.Background(), DeliveryAuditRequest{PhysicalPoolID: "pool"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if repair.auditCalls != 1 || repair.audit.PhysicalPoolID != "pool" || locker.acquired != 0 {
		t.Fatalf("audit calls=%d pool=%q locks=%d", repair.auditCalls, repair.audit.PhysicalPoolID, locker.acquired)
	}
	if err := service.AuditDeliveryRoots(context.Background(), DeliveryAuditRequest{}, io.Discard); err == nil {
		t.Fatal("audit without pool unexpectedly accepted")
	}
}

func TestArchiveUseCasesOwnLayoutAndOutputMapping(t *testing.T) {
	home := t.TempDir()
	locker := &fakeLocker{}
	archive := &fakeArchive{}
	service := New(Config{
		HomeDir: home, DBPath: filepath.Join(home, "leapview.db"),
		DuckLakeCatalog:    filepath.Join(home, "ducklake", "catalog.duckdb"),
		DuckLakeData:       filepath.Join(home, "ducklake", "data"),
		ArtifactDir:        filepath.Join(home, "artifacts"),
		RuntimeDir:         filepath.Join(home, "runtime"),
		ManagedDataDir:     filepath.Join(home, "managed-data"),
		ManagedDataBackend: "local",
	}, Dependencies{
		Locker: locker, State: &fakeState{environment: "prod", existing: true}, Archive: archive,
	})
	var out bytes.Buffer
	backupPath := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := service.Backup(context.Background(), BackupRequest{Out: backupPath}, &out); err != nil {
		t.Fatal(err)
	}
	if archive.backupInstance.Path != backupPath ||
		len(archive.backupInstance.ExcludeRelativePaths) != 1 ||
		!strings.Contains(out.String(), "instance backup written: "+backupPath) {
		t.Fatalf("backup options=%#v output=%q", archive.backupInstance, out.String())
	}
	out.Reset()
	evidence := map[string]string{"managed-data-version": "version-42"}
	if err := service.Restore(context.Background(), RestoreRequest{
		From: backupPath, CurrentBackup: "-", Confirm: true, ExternalEvidence: evidence,
	}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if !archive.restoreInstance.DiscardCurrentBackup ||
		archive.restoreInstance.ExpectedEnvironment != "prod" ||
		archive.restoreInstance.ExternalEvidence["managed-data-version"] != "version-42" ||
		!strings.Contains(out.String(), "instance restored from: "+backupPath) {
		t.Fatalf("restore options=%#v output=%q", archive.restoreInstance, out.String())
	}
}

func TestS3BackupRequiresAndRecordsExactExternalRecoveryPoint(t *testing.T) {
	home := t.TempDir()
	archive := &fakeArchive{}
	service := New(Config{
		HomeDir: home, DBPath: filepath.Join(home, "leapview.db"),
		DuckLakeCatalog: filepath.Join(home, "ducklake", "catalog.duckdb"),
		DuckLakeData:    filepath.Join(home, "ducklake", "data"),
		ArtifactDir:     filepath.Join(home, "artifacts"), RuntimeDir: filepath.Join(home, "runtime"),
		ManagedDataDir: filepath.Join(home, "managed-data"), ManagedDataBackend: "s3",
		ManagedDataS3Endpoint: "https://objects.example.test/", ManagedDataS3Region: "eu-west-1",
		ManagedDataS3Bucket: "recovery-bucket", ManagedDataS3Prefix: "/tenant-a/managed-data/",
	}, Dependencies{Locker: &fakeLocker{}, State: &fakeState{environment: "prod", existing: true}, Archive: archive})
	if err := service.Backup(context.Background(), BackupRequest{Out: filepath.Join(t.TempDir(), "missing.tar.gz")}, io.Discard); err == nil || !strings.Contains(err.Error(), "exact external recovery point") {
		t.Fatalf("backup without recovery point error = %v", err)
	}
	request := BackupRequest{
		Out: filepath.Join(t.TempDir(), "backup.tar.gz"),
		ExternalRecoveryPoints: []ExternalRecoveryPoint{{
			Role: "managed-data", RecoveryPoint: "version-42", EvidenceKey: "managed-data-version",
		}},
	}
	if err := service.Backup(context.Background(), request, io.Discard); err != nil {
		t.Fatal(err)
	}
	topology := archive.backupInstance.StorageTopology
	if topology.ControlPlane != "local" || topology.ManagedData != "external" || topology.DuckLake != "local" || len(topology.ExternalStores) != 1 {
		t.Fatalf("storage topology = %#v", topology)
	}
	reference := topology.ExternalStores[0]
	if reference.Role != "managed-data" || reference.Provider != "s3-compatible" || reference.Endpoint != "https://objects.example.test" ||
		reference.Region != "eu-west-1" || reference.Bucket != "recovery-bucket" || reference.Prefix != "tenant-a/managed-data" ||
		reference.RecoveryPoint != "version-42" || reference.EvidenceKey != "managed-data-version" {
		t.Fatalf("external recovery reference = %#v", reference)
	}
	if err := service.Restore(context.Background(), RestoreRequest{
		From: request.Out, CurrentBackup: filepath.Join(t.TempDir(), "current.tar.gz"), Confirm: true,
		ExternalEvidence:              map[string]string{"managed-data-version": "version-42"},
		CurrentExternalRecoveryPoints: request.ExternalRecoveryPoints,
	}, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := archive.restoreInstance.TargetStorageTopology.ExternalStores[0]; got.Provider != reference.Provider || got.Endpoint != reference.Endpoint || got.Region != reference.Region ||
		got.Bucket != reference.Bucket || got.Prefix != reference.Prefix || got.RecoveryPoint != "" || got.EvidenceKey != "" {
		t.Fatalf("restore target storage identity = %#v", got)
	}
	if got := archive.restoreInstance.CurrentStorageTopology.ExternalStores[0]; got != reference {
		t.Fatalf("restore checkpoint topology = %#v, want %#v", got, reference)
	}
}

func TestRestorePreflightWritesMachinePlanWithoutRestore(t *testing.T) {
	home := t.TempDir()
	locker := &fakeLocker{}
	archive := &fakeArchive{preflightResult: RestorePreflightResult{Document: []byte(`{"schemaVersion":1,"allowed":true}`)}}
	service := New(Config{
		HomeDir: home, DBPath: filepath.Join(home, "leapview.db"), Environment: "prod",
		DuckLakeCatalog: filepath.Join(home, "ducklake", "catalog.duckdb"), DuckLakeData: filepath.Join(home, "ducklake", "data"),
		ArtifactDir: filepath.Join(home, "artifacts"), RuntimeDir: filepath.Join(home, "runtime"),
		ManagedDataDir: filepath.Join(home, "managed-data"), ManagedDataBackend: "local",
	}, Dependencies{Locker: locker, State: &fakeState{environment: "prod", existing: true}, Archive: archive})
	var out bytes.Buffer
	if err := service.Restore(context.Background(), RestoreRequest{From: "backup.tar.gz", PreflightOnly: true}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if archive.restoreInstance.Path != "" || archive.preflight.Path != "backup.tar.gz" ||
		!strings.Contains(out.String(), `"allowed":true`) || locker.acquired != 1 || locker.lock.released != 1 {
		t.Fatalf("preflight=%#v restore=%#v output=%q locks=%d/%d", archive.preflight, archive.restoreInstance, out.String(), locker.acquired, locker.lock.released)
	}
}

func TestMaintenanceRejectsNegativeRetentionBeforeDependencies(t *testing.T) {
	service := New(Config{}, Dependencies{})
	err := service.Maintenance(context.Background(), MaintenanceRequest{AuditDays: -1}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "zero or greater") {
		t.Fatalf("maintenance error = %v", err)
	}
}

func TestResolveEnvironmentPropagatesUnexpectedStateFailures(t *testing.T) {
	service := New(Config{}, Dependencies{State: &fakeState{environmentErr: errors.New("broken")}})
	if _, err := service.resolveEnvironment(context.Background()); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("resolve environment error = %v", err)
	}
	service = New(Config{}, Dependencies{State: &fakeState{environmentErr: sql.ErrNoRows}})
	if _, err := service.resolveEnvironment(context.Background()); !strings.Contains(err.Error(), sql.ErrNoRows.Error()) {
		t.Fatalf("raw sql sentinel must not cross the port: %v", err)
	}
}
