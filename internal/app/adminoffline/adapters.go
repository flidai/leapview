package adminoffline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	adminsqlite "github.com/flidai/leapview/internal/admin/sqlite"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	physicalpoolsqlite "github.com/flidai/leapview/internal/analytics/physicalpool/sqlite"
	"github.com/flidai/leapview/internal/app/gcadapter"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/gc"
	deploymentsqlite "github.com/flidai/leapview/internal/deployment/sqlite"
	"github.com/flidai/leapview/internal/extension"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/internal/platform/locking"
	"github.com/flidai/leapview/internal/platform/ociref"
	storagemaintenance "github.com/flidai/leapview/internal/servingstate/retention"
	servingstatesqlite "github.com/flidai/leapview/internal/servingstate/sqlite"
	_ "modernc.org/sqlite"
)

type lockHandle struct {
	lock *instancelock.Lock
}

func (handle lockHandle) Release() error { return handle.lock.Release() }

type instanceLocker struct {
	home string
}

func (locker instanceLocker) Acquire(context.Context) (adminoffline.Lock, error) {
	lock, err := instancelock.Acquire(locker.home)
	if err != nil {
		return nil, err
	}
	return lockHandle{lock: lock}, nil
}

type instanceState struct {
	dbPath string
}

type physicalPoolBootstrap struct {
	dbPath string
	s3     gcadapter.S3Config
}

type deliveryRepair struct {
	dbPath      string
	home        string
	stagingRoot string
	s3          gcadapter.S3Config
}

// RepairDeliveryRoot is the production offline/admin adapter for the bounded
// control-plane quarantine action. It reconstructs the admitted pool contract
// from SQLite, uses gcadapter's read-only Inspector, and exposes no raw object
// deletion or catalog mutation capability.
func (repair deliveryRepair) RepairDeliveryRoot(ctx context.Context, request adminoffline.DeliveryRepairRequest, out io.Writer) error {
	if request.Action != "quarantine" {
		return fmt.Errorf("unsupported delivery repair action %q", request.Action)
	}
	root := request.Root
	if root.PhysicalPoolID == "" || root.Kind == "" || root.SourceID == "" {
		return fmt.Errorf("delivery repair root identity is incomplete")
	}
	readOnlyDB, err := openDeliveryAuditDB(ctx, repair.dbPath)
	if err != nil {
		return err
	}
	defer readOnlyDB.Close()
	delivery := deploymentsqlite.NewRepositoryWithHooks(readOnlyDB, deploymentsqlite.ActivationHooks{})
	pools := physicalpoolsqlite.NewRepository(readOnlyDB)
	inspector, err := repair.inspectorForRoot(ctx, delivery, pools, root)
	if err != nil {
		return err
	}
	runner, err := gcadapter.NewProductionRunner(delivery, inspector.Store, inspector, gc.Config{PhysicalPoolID: root.PhysicalPoolID, HolderID: "repair"})
	if err != nil {
		return err
	}
	err = runner.RepairAtRevision(ctx, root, func(ctx context.Context, verified deployment.DeliveryRoot, rootRevision int64) error {
		if !request.Apply {
			return nil
		}
		if err := readOnlyDB.Close(); err != nil {
			return err
		}
		store, err := platform.Open(ctx, repair.dbPath)
		if err != nil {
			return err
		}
		defer store.Close()
		mutableDelivery := deploymentsqlite.NewRepositoryWithHooks(store.SQLDB(), deploymentsqlite.ActivationHooks{})
		const reason = "operator_repair_quarantine"
		if err := mutableDelivery.QuarantineRootWithActorAtRevision(ctx, verified, rootRevision, reason, "offline-admin", time.Now().UTC()); err != nil {
			return err
		}
		return nil
	})
	return err
}

func openDeliveryAuditDB(ctx context.Context, path string) (*sql.DB, error) {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	db, err := sql.Open("sqlite", path+separator+"mode=ro&_pragma=foreign_keys(1)&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open read-only delivery control database: %w", err)
	}
	return db, nil
}

// AuditDeliveryRoots enumerates the exact durable roots for one physical pool
// and verifies each immutable catalog plus its read-only DuckLake closure. It
// deliberately does not acquire the offline lock or construct any mutation
// callback; a failed check returns before emitting a successful audit result.
func (repair deliveryRepair) AuditDeliveryRoots(ctx context.Context, request adminoffline.DeliveryAuditRequest) (adminoffline.DeliveryAuditResult, error) {
	if request.PhysicalPoolID == "" {
		return adminoffline.DeliveryAuditResult{}, fmt.Errorf("delivery audit requires a physical-pool identity")
	}
	readOnlyDB, err := openDeliveryAuditDB(ctx, repair.dbPath)
	if err != nil {
		return adminoffline.DeliveryAuditResult{}, err
	}
	defer readOnlyDB.Close()
	delivery := deploymentsqlite.NewRepositoryWithHooks(readOnlyDB, deploymentsqlite.ActivationHooks{})
	pools := physicalpoolsqlite.NewRepository(readOnlyDB)
	now := time.Now().UTC()
	set, err := delivery.EnumerateRoots(ctx, request.PhysicalPoolID, now)
	if err != nil {
		return adminoffline.DeliveryAuditResult{}, fmt.Errorf("enumerate durable roots: %w", err)
	}
	if set.PhysicalPoolID != request.PhysicalPoolID {
		return adminoffline.DeliveryAuditResult{}, fmt.Errorf("enumerated roots belong to physical pool %q, not %q", set.PhysicalPoolID, request.PhysicalPoolID)
	}
	evidenceRows := make([]adminoffline.DeliveryRootAuditResult, 0, len(set.Roots))
	seen := make(map[string]struct{}, len(set.Roots))
	for _, root := range set.Roots {
		if root.PhysicalPoolID != request.PhysicalPoolID || root.Kind == "" || root.SourceID == "" || root.CatalogDigest == "" || root.ObjectKey == "" || root.Status == "" || root.CreatedAt.IsZero() || root.CreatedAt.Location() != time.UTC {
			return adminoffline.DeliveryAuditResult{}, fmt.Errorf("root identity is incomplete or non-UTC for %s/%s", root.Kind, root.SourceID)
		}
		key := root.Kind + "\x00" + root.SourceID
		if _, ok := seen[key]; ok {
			return adminoffline.DeliveryAuditResult{}, fmt.Errorf("ambiguous durable root %s/%s", root.Kind, root.SourceID)
		}
		seen[key] = struct{}{}
		inspector, inspectErr := repair.inspectorForRoot(ctx, delivery, pools, root)
		if inspectErr != nil {
			return adminoffline.DeliveryAuditResult{}, fmt.Errorf("verify root %s/%s: %w", root.Kind, root.SourceID, inspectErr)
		}
		reach, inspectErr := inspector.Inspect(ctx, root)
		if inspectErr != nil {
			return adminoffline.DeliveryAuditResult{}, fmt.Errorf("verify root %s/%s: %w", root.Kind, root.SourceID, inspectErr)
		}
		if reach.CatalogKey != root.ObjectKey || reach.CatalogDigest != root.CatalogDigest {
			return adminoffline.DeliveryAuditResult{}, fmt.Errorf("verify root %s/%s: inspector returned a different artifact identity", root.Kind, root.SourceID)
		}
		evidenceRows = append(evidenceRows, adminoffline.DeliveryRootAuditResult{Root: root, DataFiles: len(reach.DataFiles), DeleteFiles: len(reach.DeleteFiles)})
	}
	finalSet, err := delivery.EnumerateRoots(ctx, request.PhysicalPoolID, time.Now().UTC())
	if err != nil {
		return adminoffline.DeliveryAuditResult{}, fmt.Errorf("re-enumerate durable roots: %w", err)
	}
	if finalSet.Revision != set.Revision || !sameDeliveryRootSet(set.Roots, finalSet.Roots) {
		return adminoffline.DeliveryAuditResult{}, fmt.Errorf("durable roots changed during verification")
	}
	return adminoffline.DeliveryAuditResult{PhysicalPoolID: request.PhysicalPoolID, RootRevision: set.Revision, Roots: evidenceRows}, nil
}

func sameDeliveryRootSet(left, right []deployment.DeliveryRoot) bool {
	if len(left) != len(right) {
		return false
	}
	identity := func(root deployment.DeliveryRoot) string {
		return strings.Join([]string{
			root.PhysicalPoolID, root.Kind, root.SourceID, root.CandidateID,
			root.GenerationID, root.LeaseID, root.CatalogDigest, root.ObjectKey,
			root.Status, root.CreatedAt.Format(time.RFC3339Nano), root.ExpiresAt.Format(time.RFC3339Nano),
		}, "\x00")
	}
	seen := make(map[string]int, len(left))
	for _, root := range left {
		seen[identity(root)]++
	}
	for _, root := range right {
		key := identity(root)
		if seen[key] == 0 {
			return false
		}
		seen[key]--
	}
	return true
}

func (repair deliveryRepair) inspectorForRoot(ctx context.Context, delivery *deploymentsqlite.Repository, pools *physicalpoolsqlite.Repository, root deployment.DeliveryRoot) (gcadapter.Inspector, error) {
	compatibilityDigest, err := delivery.DeliveryRootCompatibilityDigest(ctx, root)
	if err != nil {
		return gcadapter.Inspector{}, err
	}
	admission, err := pools.LoadAdmissionContractByCompatibilityDigest(ctx, physicalpool.PoolID(root.PhysicalPoolID), compatibilityDigest)
	if err != nil {
		return gcadapter.Inspector{}, fmt.Errorf("load admitted physical pool: %w", err)
	}
	contract := &analyticsducklake.PoolContract{Pool: admission.Pool, Tuple: admission.Pool.Compatibility, Admission: admission.Admission, Evidence: admission.Evidence}
	credentialBootstrap, err := gcadapter.NewPoolCredentialBootstrap(contract, repair.s3)
	if err != nil {
		return gcadapter.Inspector{}, err
	}
	poolStore, err := gcadapter.NewPoolStore(ctx, contract, repair.s3)
	if err != nil {
		return gcadapter.Inspector{}, err
	}
	return gcadapter.Inspector{Store: poolStore, PoolContract: contract, StagingRoot: repair.stagingRoot, ExtensionAdmission: repair.s3.ExtensionAdmission, CredentialBootstrap: credentialBootstrap}, nil
}

func (physicalPoolBootstrap) ValidateEvidence(evidence physicalpool.Evidence) error {
	return (&analyticsducklake.SharedPoolConformance{Compatibility: evidence.Compatibility}).ValidateEvidence(evidence)
}

func (bootstrap physicalPoolBootstrap) Bootstrap(ctx context.Context, request adminoffline.PhysicalPoolBootstrapRequest) (adminoffline.PhysicalPoolBootstrapResult, error) {
	if err := request.Pool.Validate(); err != nil {
		return adminoffline.PhysicalPoolBootstrapResult{}, err
	}
	evidence := request.Evidence
	if err := (&analyticsducklake.SharedPoolConformance{Compatibility: evidence.Compatibility}).ValidateEvidence(evidence); err != nil {
		return adminoffline.PhysicalPoolBootstrapResult{}, err
	}
	store, err := platform.Open(ctx, bootstrap.dbPath)
	if err != nil {
		return adminoffline.PhysicalPoolBootstrapResult{}, err
	}
	defer store.Close()
	repository := physicalpoolsqlite.NewRepository(store.SQLDB())
	ownerID, err := store.InstanceID(ctx)
	if err != nil {
		return adminoffline.PhysicalPoolBootstrapResult{}, fmt.Errorf("read durable instance identity: %w", err)
	}
	physicalPool := mustPhysicalPool(request.Pool)
	admission, err := physicalPool.Admit(evidence)
	if err != nil {
		return adminoffline.PhysicalPoolBootstrapResult{}, err
	}
	contract := &analyticsducklake.PoolContract{Pool: physicalPool, Tuple: admission.Compatibility, Admission: admission, Evidence: evidence}
	if admission.Compatibility.StorageImplementation == "local" || admission.Compatibility.StorageImplementation == "filesystem" {
		path, pathErr := physicalPool.DataPath()
		if pathErr != nil {
			return adminoffline.PhysicalPoolBootstrapResult{}, pathErr
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return adminoffline.PhysicalPoolBootstrapResult{}, fmt.Errorf("create local physical-pool namespace: %w", err)
		}
	}
	storeAdapter, err := gcadapter.NewPoolStore(ctx, contract, bootstrap.s3)
	if err != nil {
		return adminoffline.PhysicalPoolBootstrapResult{}, fmt.Errorf("physical-pool ownership marker store: %w", err)
	}
	marker, ok := storeAdapter.(physicalpool.NamespaceOwnership)
	if !ok {
		return adminoffline.PhysicalPoolBootstrapResult{}, fmt.Errorf("physical-pool store does not support ownership markers")
	}
	pool, admission, err := repository.CreateAndAdmitWithOwnership(ctx, physicalPool, evidence, ownerID, marker)
	if err != nil {
		return adminoffline.PhysicalPoolBootstrapResult{}, err
	}
	return adminoffline.PhysicalPoolBootstrapResult{
		PoolID: string(pool.ID), CompatibilityDigest: admission.CompatibilityDigest,
		EvidenceDigest: admission.EvidenceDigest, ConformanceVersion: admission.ConformanceVersion, Applied: true,
	}, nil
}

func mustPhysicalPool(identity physicalpool.PoolIdentity) physicalpool.PhysicalPool {
	pool, _ := physicalpool.NewPhysicalPool(identity)
	return pool
}

func (state instanceState) withStore(ctx context.Context, operation func(*platform.Store) error) error {
	store, err := platform.Open(ctx, state.dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	return operation(store)
}

func (state instanceState) Environment(ctx context.Context) (string, error) {
	var environment string
	err := state.withStore(ctx, func(store *platform.Store) error {
		var err error
		environment, err = store.InstanceEnvironment(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return adminoffline.ErrStateNotFound
		}
		return err
	})
	return environment, err
}

func (state instanceState) ExistingEnvironment(ctx context.Context) (string, bool, error) {
	if _, err := os.Stat(state.dbPath); err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	environment, err := state.Environment(ctx)
	if errors.Is(err, adminoffline.ErrStateNotFound) {
		return "", true, err
	}
	return environment, err == nil, err
}

func (state instanceState) BindEnvironment(ctx context.Context, environment string) error {
	return state.withStore(ctx, func(store *platform.Store) error {
		return store.BindInstanceEnvironment(ctx, environment)
	})
}

func (state instanceState) Initialized(ctx context.Context) (bool, error) {
	initialized := false
	err := state.withStore(ctx, func(store *platform.Store) error {
		_, err := store.GetSetting(ctx, access.InstanceInitializedSetting)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		initialized = err == nil
		return err
	})
	return initialized, err
}

type instanceInitializer struct {
	dbPath string
}

func (initializer instanceInitializer) Initialize(
	ctx context.Context,
	input adminoffline.InitializationInput,
	prepare func(adminoffline.InitialCredentials) error,
) (adminoffline.InitialCredentials, error) {
	store, err := platform.Open(ctx, initializer.dbPath)
	if err != nil {
		return adminoffline.InitialCredentials{}, err
	}
	defer store.Close()
	repository := accesssqlite.NewRepository(store.SQLDB())
	result, err := repository.InitializeInstance(ctx, access.InstanceInitializationInput{
		Email: input.Email, Environment: input.Environment, Now: input.Now,
		EvaluationDataIngest: input.Environment == "evaluation",
	}, func(credentials access.InitialInstanceCredentials) error {
		return prepare(adminoffline.InitialCredentials{
			Email:                   credentials.Email,
			TemporaryPassword:       credentials.TemporaryPassword,
			PublisherToken:          credentials.PublisherToken,
			PublisherTokenExpiresAt: credentials.PublisherTokenExpiresAt.Format(time.RFC3339),
		})
	})
	if errors.Is(err, access.ErrInstanceAlreadyInitialized) {
		err = adminoffline.ErrInstanceAlreadyInitialized
	}
	return adminoffline.InitialCredentials{
		Email:                   result.Email,
		TemporaryPassword:       result.TemporaryPassword,
		PublisherToken:          result.PublisherToken,
		PublisherTokenExpiresAt: result.PublisherTokenExpiresAt.Format(time.RFC3339),
	}, err
}

type credentialRecovery struct {
	path string
}

func (recovery credentialRecovery) Read() ([]byte, error) {
	return securefs.ReadPrivateFile(recovery.path)
}

func (recovery credentialRecovery) Write(contents []byte) error {
	return securefs.WritePrivateFileAtomic(recovery.path, contents)
}

func (recovery credentialRecovery) Remove() error {
	return os.Remove(recovery.path)
}

type operationalRetention struct {
	dbPath string
}

type auditOutboxControl struct {
	dbPath string
}

func (control auditOutboxControl) Status(ctx context.Context) (adminoffline.AuditOutboxStatus, error) {
	store, err := platform.Open(ctx, control.dbPath)
	if err != nil {
		return adminoffline.AuditOutboxStatus{}, err
	}
	defer store.Close()
	operator := access.AuditOutboxOperator(accessmodule.NewSQLiteAuditStore(store.SQLDB()))
	inspection, err := operator.InspectAuditOutbox(ctx, time.Now().UTC(), access.MaxAuditOutboxInspectionRows)
	if err != nil {
		return adminoffline.AuditOutboxStatus{}, err
	}
	terminals := make([]adminoffline.AuditOutboxTerminalIntent, 0, len(inspection.Terminals))
	for _, item := range inspection.Terminals {
		terminals = append(terminals, adminoffline.AuditOutboxTerminalIntent{
			EventID: item.EventID, State: string(item.State), AttemptCount: item.AttemptCount,
			LastErrorCode: item.LastErrorCode, PayloadDigest: item.PayloadDigest,
			AggregateKey: item.AggregateKey, AggregateSequence: item.AggregateSequence,
			LeaseGeneration: item.LeaseGeneration, CreatedAt: item.CreatedAt,
		})
	}
	stats := inspection.Stats
	return adminoffline.AuditOutboxStatus{
		Pending: stats.Pending, Retry: stats.Retry, Leased: stats.Leased, Delivered: stats.Delivered,
		Poison: stats.Poison, Quarantined: stats.Quarantined, OldestUndeliveredAge: stats.OldestUndeliveredAge,
		AttemptCount: stats.AttemptCount, Capacity: stats.Capacity, CapacityRemaining: stats.CapacityRemaining,
		Terminals: terminals, TerminalsTruncated: inspection.Truncated,
	}, nil
}

func (control auditOutboxControl) RequeueExact(ctx context.Context, request adminoffline.AuditOutboxRequest) error {
	store, err := platform.Open(ctx, control.dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	attempts := request.ExpectedAttempts
	operator := access.AuditOutboxOperator(accessmodule.NewSQLiteAuditStore(store.SQLDB()))
	return operator.RequeueAuditIntentExact(ctx, access.AuditOutboxRequeueRequest{
		EventID: request.RequeueEventID, ExpectedState: access.AuditIntentState(request.ExpectedState),
		ExpectedAttempts: attempts, ExpectedFailureCode: request.ExpectedFailureCode,
		ExpectedPayloadDigest: request.ExpectedPayloadDigest,
	})
}

func (retention operationalRetention) Prune(ctx context.Context, policy adminoffline.RetentionPolicy) (adminoffline.RetentionResult, error) {
	store, err := platform.Open(ctx, retention.dbPath)
	if err != nil {
		return adminoffline.RetentionResult{}, err
	}
	defer store.Close()
	result, err := adminsqlite.PruneOperationalHistory(ctx, store.SQLDB(), adminsqlite.RetentionOptions{
		AuditEventsMaxAge:             policy.AuditEventsMaxAge,
		QueryEventsMaxAge:             policy.QueryEventsMaxAge,
		ArchivedAgentConversationsAge: policy.ArchivedAgentConversationsAge,
		AuthStateMaxAge:               policy.AuthStateMaxAge,
		DryRun:                        policy.DryRun,
	})
	return adminoffline.RetentionResult{
		AuditEventsDeleted:                  result.AuditEventsDeleted,
		DeliveredAuditIntentsDeleted:        result.DeliveredAuditIntentsDeleted,
		QueryEventsDeleted:                  result.QueryEventsDeleted,
		ArchivedAgentConversationsDeleted:   result.ArchivedAgentConversationsDeleted,
		ExpiredOAuthStatesDeleted:           result.ExpiredOAuthStatesDeleted,
		StaleSessionsDeleted:                result.StaleSessionsDeleted,
		StaleAPITokensDeleted:               result.StaleAPITokensDeleted,
		StaleServicePrincipalSecretsDeleted: result.StaleServicePrincipalSecretsDeleted,
	}, err
}

type storageCleaner struct {
	dbPath             string
	home               string
	catalogPath        string
	dataPath           string
	extensionAdmission extension.Admission
}

func (cleaner storageCleaner) Cleanup(ctx context.Context, environment string, dryRun bool, out io.Writer) error {
	store, err := platform.Open(ctx, cleaner.dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	snapshots, err := analyticsducklake.Open(ctx, analyticsducklake.Config{
		RootDir: cleaner.home, CatalogPath: cleaner.catalogPath, DataPath: cleaner.dataPath,
		ExtensionAdmission: cleaner.extensionAdmission,
	})
	if err != nil {
		return err
	}
	defer snapshots.Close()
	_, err = storagemaintenance.Run(ctx, servingstatesqlite.NewRepository(store.SQLDB()), storagemaintenance.Options{
		Environment: environment,
		Snapshots:   snapshots,
		CatalogPath: cleaner.catalogPath,
		DataPath:    cleaner.dataPath,
		DryRun:      dryRun,
		Out:         out,
	})
	return err
}

type instanceArchive struct {
	home   string
	dbPath string
}

var (
	loadArchiveReleaseIdentity  = runtimeArchiveReleaseIdentity
	loadArchiveTransitionPolicy = runtimeArchiveTransitionPolicy
)

func (archive instanceArchive) BackupDatabase(ctx context.Context, options adminoffline.BackupOptions) error {
	path := options.Path
	if options.Writer != nil {
		var err error
		path, err = securefs.UnusedTemporaryPath(filepath.Dir(archive.home), "leapview-backup-*.db")
		if err != nil {
			return err
		}
		defer os.Remove(path)
	}
	store, err := platform.Open(ctx, archive.dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Backup(ctx, path); err != nil {
		return err
	}
	if options.Writer != nil {
		return securefs.CopyFile(options.Writer, path)
	}
	return nil
}

func (archive instanceArchive) BackupInstance(ctx context.Context, options adminoffline.BackupOptions) error {
	releaseIdentity, err := loadArchiveReleaseIdentity()
	if err != nil {
		return err
	}
	policy, err := loadArchiveTransitionPolicy()
	if err != nil {
		return err
	}
	external := make([]platform.InstanceBackupExternalStoreReference, len(options.StorageTopology.ExternalStores))
	for index, reference := range options.StorageTopology.ExternalStores {
		external[index] = platform.InstanceBackupExternalStoreReference{
			Role: reference.Role, Provider: reference.Provider, Endpoint: reference.Endpoint,
			Region: reference.Region, Bucket: reference.Bucket, Prefix: reference.Prefix,
			RecoveryPoint: reference.RecoveryPoint, EvidenceKey: reference.EvidenceKey,
		}
	}
	platformOptions := platform.InstanceBackupOptions{
		HomeDir: archive.home, DBPath: archive.dbPath, OutPath: options.Path,
		ExcludeRelativePaths: options.ExcludeRelativePaths, Environment: options.Environment,
		ReleaseIdentity: releaseIdentity, TransitionPolicy: policy,
		StorageTopology: platform.InstanceBackupStorageTopology{
			ControlPlane: options.StorageTopology.ControlPlane, ManagedData: options.StorageTopology.ManagedData,
			DuckLake: options.StorageTopology.DuckLake, ExternalStores: external,
		},
	}
	if options.Writer != nil {
		return platform.BackupInstanceToWriter(ctx, platformOptions, options.Writer)
	}
	return platform.BackupInstance(ctx, platformOptions)
}

func (archive instanceArchive) RestoreDatabase(ctx context.Context, options adminoffline.RestoreOptions) error {
	path := options.Path
	if options.Reader != nil {
		var err error
		path, err = securefs.CopyPrivateTemporaryFile(options.Reader, filepath.Dir(archive.home), "leapview-restore-*.db")
		if err != nil {
			return err
		}
		defer os.Remove(path)
	}
	current := options.CurrentBackup
	if options.DiscardCurrentBackup {
		var err error
		current, err = securefs.UnusedTemporaryPath(filepath.Dir(archive.home), platform.InstanceRestoreCheckpointPattern)
		if err != nil {
			return err
		}
		defer os.Remove(current)
	}
	if err := platform.ValidateDatabaseInstanceEnvironment(ctx, path, options.ExpectedEnvironment); err != nil {
		return err
	}
	return platform.Restore(ctx, archive.dbPath, path, current)
}

func (archive instanceArchive) RestoreInstance(ctx context.Context, options adminoffline.RestoreOptions) error {
	releaseIdentity, err := loadArchiveReleaseIdentity()
	if err != nil {
		return err
	}
	policy, err := loadArchiveTransitionPolicy()
	if err != nil {
		return err
	}
	current := options.CurrentBackup
	if options.DiscardCurrentBackup {
		var err error
		current, err = securefs.UnusedTemporaryPath(filepath.Dir(archive.home), platform.InstanceRestoreCheckpointPattern)
		if err != nil {
			return err
		}
		defer os.Remove(current)
	}
	platformOptions := platform.InstanceRestoreOptions{
		TargetHomeDir:          archive.home,
		BackupPath:             options.Path,
		CurrentBackupOut:       current,
		DiscardCurrentBackup:   options.DiscardCurrentBackup,
		ExpectedEnvironment:    options.ExpectedEnvironment,
		PreserveRelativeFile:   instancelock.FileName,
		ResetRelativePaths:     options.ResetRelativePaths,
		ExclusiveLockHeld:      true,
		RequireExclusiveLock:   true,
		TargetReleaseIdentity:  releaseIdentity,
		ExternalEvidence:       options.ExternalEvidence,
		TargetStorageTopology:  platformStorageTopology(options.TargetStorageTopology),
		CurrentStorageTopology: platformStorageTopology(options.CurrentStorageTopology),
		TransitionPolicy:       policy,
	}
	if options.Reader != nil {
		platformOptions.BackupPath = ""
		return platform.RestoreInstanceFromReader(ctx, platformOptions, options.Reader)
	}
	return platform.RestoreInstance(ctx, platformOptions)
}

func (archive instanceArchive) PreflightInstance(ctx context.Context, options adminoffline.RestoreOptions) (adminoffline.RestorePreflightResult, error) {
	releaseIdentity, err := loadArchiveReleaseIdentity()
	if err != nil {
		return adminoffline.RestorePreflightResult{}, err
	}
	policy, err := loadArchiveTransitionPolicy()
	if err != nil {
		return adminoffline.RestorePreflightResult{}, err
	}
	path := options.Path
	if options.Reader != nil {
		temporary, err := securefs.CopyPrivateTemporaryFile(options.Reader, os.TempDir(), "leapview-preflight-*.tar.gz")
		if err != nil {
			return adminoffline.RestorePreflightResult{}, err
		}
		defer os.Remove(temporary)
		path = temporary
	}
	current := options.CurrentBackup
	if options.DiscardCurrentBackup {
		current, err = securefs.UnusedTemporaryPath(filepath.Dir(archive.home), platform.InstanceRestoreCheckpointPattern)
		if err != nil {
			return adminoffline.RestorePreflightResult{}, err
		}
	}
	plan, preflightErr := platform.PreflightInstanceRestore(ctx, platform.InstanceRestorePreflightOptions{
		ArchivePath: path, TargetHomeDir: archive.home, ExpectedEnvironment: options.ExpectedEnvironment,
		PreserveRelativeFile: instancelock.FileName, ResetRelativePaths: options.ResetRelativePaths,
		ExclusiveLockHeld: true, RequireExclusiveLock: true,
		CurrentBackupOut: current, DiscardCurrentBackup: options.DiscardCurrentBackup,
		TargetReleaseIdentity: releaseIdentity, ExternalEvidence: options.ExternalEvidence,
		TargetStorageTopology:  platformStorageTopology(options.TargetStorageTopology),
		CurrentStorageTopology: platformStorageTopology(options.CurrentStorageTopology),
		TransitionPolicy:       policy,
	})
	document, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return adminoffline.RestorePreflightResult{}, err
	}
	document = append(document, '\n')
	return adminoffline.RestorePreflightResult{Document: document}, preflightErr
}

func platformStorageTopology(topology adminoffline.BackupStorageTopology) platform.InstanceBackupStorageTopology {
	external := make([]platform.InstanceBackupExternalStoreReference, len(topology.ExternalStores))
	for index, reference := range topology.ExternalStores {
		external[index] = platform.InstanceBackupExternalStoreReference{
			Role: reference.Role, Provider: reference.Provider, Endpoint: reference.Endpoint,
			Region: reference.Region, Bucket: reference.Bucket, Prefix: reference.Prefix,
			RecoveryPoint: reference.RecoveryPoint, EvidenceKey: reference.EvidenceKey,
		}
	}
	return platform.InstanceBackupStorageTopology{
		ControlPlane: topology.ControlPlane, ManagedData: topology.ManagedData,
		DuckLake: topology.DuckLake, ExternalStores: external,
	}
}

func runtimeArchiveReleaseIdentity() (compatibility.ReleaseIdentity, error) {
	build := buildinfo.Current()
	if build.Development || build.Dirty || build.Version == buildinfo.DevelopmentVersion || build.Revision == buildinfo.UnknownValue {
		return compatibility.ReleaseIdentity{}, fmt.Errorf("backup/restore requires exact released build provenance")
	}
	image := strings.TrimSpace(os.Getenv("LEAPVIEW_IMAGE"))
	if err := ociref.ValidateImmutable(image); err != nil {
		return compatibility.ReleaseIdentity{}, fmt.Errorf("backup/restore release image identity: %w", err)
	}
	return compatibility.ReleaseIdentity{
		ReleaseID: "v" + strings.TrimPrefix(build.Version, "v"), Version: strings.TrimPrefix(build.Version, "v"),
		SourceRevision: build.Revision, Image: image, Distribution: "public", Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}, nil
}

func runtimeArchiveTransitionPolicy() (*compatibility.Policy, error) {
	const policyPath = "/run/leapview/release-transition-policy.json"
	if _, err := os.Stat(policyPath); err == nil {
		policy, _, err := compatibility.LoadPolicy(policyPath)
		return policy, err
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return compatibility.EmbeddedPolicy()
}
