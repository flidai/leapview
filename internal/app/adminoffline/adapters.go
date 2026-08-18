package adminoffline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/flidai/leapview/internal/access"
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
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/internal/platform/locking"
	storagemaintenance "github.com/flidai/leapview/internal/servingstate/retention"
	servingstatesqlite "github.com/flidai/leapview/internal/servingstate/sqlite"
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
	store, err := platform.Open(ctx, repair.dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	delivery := deploymentsqlite.NewRepositoryWithHooks(store.SQLDB(), deploymentsqlite.ActivationHooks{})
	compatibilityDigest, err := delivery.DeliveryRootCompatibilityDigest(ctx, root)
	if err != nil {
		return err
	}
	pools := physicalpoolsqlite.NewRepository(store.SQLDB())
	admission, err := pools.LoadAdmissionContractByCompatibilityDigest(ctx, physicalpool.PoolID(root.PhysicalPoolID), compatibilityDigest)
	if err != nil {
		return fmt.Errorf("load admitted physical pool: %w", err)
	}
	contract := &analyticsducklake.PoolContract{Pool: admission.Pool, Tuple: admission.Pool.Compatibility, Admission: admission.Admission, Evidence: admission.Evidence}
	poolStore, err := gcadapter.NewPoolStore(ctx, contract, repair.s3)
	if err != nil {
		return err
	}
	inspector := gcadapter.Inspector{Store: poolStore, PoolContract: contract, StagingRoot: repair.stagingRoot}
	runner, err := gcadapter.NewProductionRunner(delivery, poolStore, inspector, gc.Config{PhysicalPoolID: root.PhysicalPoolID, HolderID: "repair"})
	if err != nil {
		return err
	}
	err = runner.Repair(ctx, root, func(ctx context.Context, verified deployment.DeliveryRoot) error {
		if !request.Apply {
			return nil
		}
		const reason = "operator_repair_quarantine"
		if err := delivery.QuarantineRootWithActor(ctx, verified, reason, "offline-admin", time.Now().UTC()); err != nil {
			return err
		}
		return nil
	})
	return err
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
		QueryEventsDeleted:                  result.QueryEventsDeleted,
		ArchivedAgentConversationsDeleted:   result.ArchivedAgentConversationsDeleted,
		ExpiredOAuthStatesDeleted:           result.ExpiredOAuthStatesDeleted,
		StaleSessionsDeleted:                result.StaleSessionsDeleted,
		StaleAPITokensDeleted:               result.StaleAPITokensDeleted,
		StaleServicePrincipalSecretsDeleted: result.StaleServicePrincipalSecretsDeleted,
	}, err
}

type storageCleaner struct {
	dbPath      string
	home        string
	catalogPath string
	dataPath    string
}

func (cleaner storageCleaner) Cleanup(ctx context.Context, environment string, dryRun bool, out io.Writer) error {
	store, err := platform.Open(ctx, cleaner.dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	snapshots, err := analyticsducklake.Open(ctx, analyticsducklake.Config{
		RootDir: cleaner.home, CatalogPath: cleaner.catalogPath, DataPath: cleaner.dataPath,
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
	platformOptions := platform.InstanceBackupOptions{
		HomeDir: archive.home, DBPath: archive.dbPath, OutPath: options.Path,
		ExcludeRelativePaths: options.ExcludeRelativePaths,
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
		TargetHomeDir:        archive.home,
		BackupPath:           options.Path,
		CurrentBackupOut:     current,
		DiscardCurrentBackup: options.DiscardCurrentBackup,
		ExpectedEnvironment:  options.ExpectedEnvironment,
		PreserveRelativeFile: instancelock.FileName,
		ResetRelativePaths:   options.ResetRelativePaths,
	}
	if options.Reader != nil {
		platformOptions.BackupPath = ""
		return platform.RestoreInstanceFromReader(ctx, platformOptions, options.Reader)
	}
	return platform.RestoreInstance(ctx, platformOptions)
}
