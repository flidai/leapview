package app

// Production DuckLake retention composition.  The worker deliberately keeps
// the control ledger and external catalog authorities separate: the ledger is
// backed by the control maintenance pool while each native phase receives a
// one-connection DuckDB session built from the dedicated DuckLake maintenance
// URL/role.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/app/config"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/extension"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingstatepostgres "github.com/flidai/leapview/internal/servingstate/postgres"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
	"github.com/google/uuid"
)

const (
	defaultDuckLakeRetentionInterval = time.Hour
	defaultDuckLakeRetentionGrace    = 24 * time.Hour
	duckLakeRetentionRootBatchLimit  = 1000
)

// duckLakeRetentionPolicy is resolved from the admitted physical-pool
// contract for every pass. Reader grace protects serving-state leases while
// orphan grace governs the separate DuckLake catalog scan.
type duckLakeRetentionPolicy struct {
	ReaderGracePeriod time.Duration
	OrphanGracePeriod time.Duration
}

// runDuckLakeRetentionPass sequences the control-plane safety phase ahead of
// any physical catalog mutation. Keeping the phase boundary as a small
// function makes the ordering explicit and testable without opening either
// production database in unit tests.
func runDuckLakeRetentionPass(
	ctx context.Context,
	releaseExpiredReaderLeases func(context.Context) error,
	drainDeliveryRoots func(context.Context) error,
	physicalMaintenance func(context.Context) error,
) error {
	if releaseExpiredReaderLeases == nil || drainDeliveryRoots == nil || physicalMaintenance == nil {
		return errors.New("DuckLake retention pass dependencies are incomplete")
	}
	if err := releaseExpiredReaderLeases(ctx); err != nil {
		return fmt.Errorf("release expired serving-state reader leases: %w", err)
	}
	if err := drainDeliveryRoots(ctx); err != nil {
		return fmt.Errorf("drain delivery retention roots: %w", err)
	}
	return physicalMaintenance(ctx)
}

func duckLakeRetentionOperationID(now time.Time, interval time.Duration, physicalPoolID, catalogID string) string {
	if interval <= 0 {
		interval = defaultDuckLakeRetentionInterval
	}
	bucket := now.UTC().Truncate(interval)
	digest := sha256.Sum256([]byte(fmt.Sprintf("ducklake-retention:%s:%s:%d", physicalPoolID, catalogID, bucket.UnixNano())))
	var id uuid.UUID
	copy(id[:], digest[:16])
	// UUIDv7 stores a 48-bit Unix-millisecond timestamp followed by 74 bits
	// of deterministic entropy. Keep the bucket timestamp for sortable,
	// replay-stable operation identities and derive the remaining bits from
	// the pool/catalog identity hash.
	millis := uint64(bucket.UnixMilli()) & ((uint64(1) << 48) - 1)
	id[0] = byte(millis >> 40)
	id[1] = byte(millis >> 32)
	id[2] = byte(millis >> 24)
	id[3] = byte(millis >> 16)
	id[4] = byte(millis >> 8)
	id[5] = byte(millis)
	id[6] = (id[6] & 0x0f) | 0x70
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}

// duckLakeRetentionWorker is an in-process bounded scheduler. A failed pass
// (including workload saturation) is observable in logs but never escapes the
// lifecycle component and therefore cannot make readiness fail.
type duckLakeRetentionWorker struct {
	interval time.Duration
	logger   *slog.Logger
	acquire  func(context.Context) (workloadmodule.Lease, error)
	pass     func(context.Context) error

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

type duckLakeRetentionWorkerConfig struct {
	Interval time.Duration
	Acquire  func(context.Context) (workloadmodule.Lease, error)
	Pass     func(context.Context) error
	Logger   *slog.Logger
}

func newDuckLakeRetentionWorker(config duckLakeRetentionWorkerConfig) *duckLakeRetentionWorker {
	interval := config.Interval
	if interval <= 0 {
		interval = defaultDuckLakeRetentionInterval
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &duckLakeRetentionWorker{interval: interval, acquire: config.Acquire, pass: config.Pass, logger: logger}
}

func (w *duckLakeRetentionWorker) Start(ctx context.Context) error {
	if w == nil || w.pass == nil || w.acquire == nil {
		return nil
	}
	// Worker startup is a lifecycle boundary: normalize the parent context
	// before deriving the cancellation scope used by the goroutine and ticker.
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	if w.cancel != nil {
		w.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	w.cancel, w.done = cancel, done
	w.mu.Unlock()

	go func() {
		defer close(done)
		w.runPass(runCtx)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				w.runPass(runCtx)
			}
		}
	}()
	return nil
}

func (w *duckLakeRetentionWorker) Stop(ctx context.Context) error {
	if w == nil {
		return nil
	}
	// Worker shutdown is a lifecycle boundary: a nil caller context still needs
	// a bounded wait context while cancellation drains the maintenance goroutine.
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	if cancel == nil {
		w.mu.Unlock()
		return nil
	}
	cancel()
	w.mu.Unlock()
	select {
	case <-done:
		w.mu.Lock()
		if w.done == done {
			w.cancel, w.done = nil, nil
		}
		w.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *duckLakeRetentionWorker) runPass(ctx context.Context) {
	lease, err := w.acquire(ctx)
	if err != nil {
		w.logger.DebugContext(ctx, "DuckLake retention maintenance skipped", "error", err)
		return
	}
	if lease == nil {
		w.logger.DebugContext(ctx, "DuckLake retention maintenance skipped", "error", errors.New("workload lease is nil"))
		return
	}
	defer lease.Release()
	passCtx := lease.Context()
	if passCtx == nil {
		passCtx = ctx
	}
	// A workload lease may carry an execution context independent from the
	// worker's lifecycle context. Bridge both cancellation sources so shutdown
	// always interrupts a blocking maintenance pass.
	combinedCtx, cancel := context.WithCancel(ctx)
	stopLeaseWatch := context.AfterFunc(passCtx, cancel)
	defer func() {
		stopLeaseWatch()
		cancel()
	}()
	if err := w.pass(combinedCtx); err != nil {
		w.logger.WarnContext(ctx, "DuckLake retention maintenance failed", "error", err)
	}
}

// postgresDuckLakeRetentionWorker wires the concrete coordinator and session
// factory for production. The control repository intentionally uses the
// existing control maintenance pool; no graph.Retention facade is reused.
func postgresDuckLakeRetentionWorker(
	cfg config.Config,
	control *analyticsmodule.PostgresDuckLakeRepository,
	maintenancePool *platformpostgres.Pool,
	extensionAdmission extension.Admission,
	physicalPoolID string,
	ownerID string,
	environment string,
	servingStateMaintenance *servingstatepostgres.Repository,
	deliveryMaintenance *deploymentpostgres.Maintenance,
	acquire func(context.Context) (workloadmodule.Lease, error),
	policyFor func(context.Context) (duckLakeRetentionPolicy, error),
) (*duckLakeRetentionWorker, error) {
	if control == nil || !control.Configured() || !control.TransactionCapable() {
		return nil, errors.New("DuckLake retention control maintenance repository is unavailable")
	}
	if maintenancePool == nil || maintenancePool.PoolConfig().MinConns != 1 || maintenancePool.PoolConfig().MaxConns != 1 {
		return nil, errors.New("DuckLake retention DuckLake maintenance pool is unavailable")
	}
	physicalPoolID = strings.TrimSpace(physicalPoolID)
	ownerID = strings.TrimSpace(ownerID)
	environment = strings.TrimSpace(environment)
	if physicalPoolID == "" || ownerID == "" || environment == "" || extensionAdmission == nil || servingStateMaintenance == nil || !servingStateMaintenance.Configured() || deliveryMaintenance == nil || acquire == nil || policyFor == nil {
		return nil, errors.New("DuckLake retention worker configuration is incomplete")
	}
	if err := servingstate.ValidateEnvironment(servingstate.Environment(environment)); err != nil {
		return nil, fmt.Errorf("DuckLake retention worker environment is invalid: %w", err)
	}

	coordinator := &analyticsmodule.PostgresDuckLakeRetentionCoordinator{Control: control}
	coordinator.Orphans = &analyticsmodule.PostgresDuckLakeSnapshotOrphanCoordinator{
		Control: control,
		OpenScannerFor: func(ctx context.Context, request analyticsmodule.PostgresDuckLakeSnapshotOrphanMaintenanceRequest) (analyticsmodule.PostgresDuckLakeSnapshotCatalogPageScanner, error) {
			catalog, err := control.LoadCatalog(ctx, request.PhysicalPoolID)
			if err != nil {
				return nil, err
			}
			if catalog.PhysicalPoolID != request.PhysicalPoolID || catalog.CatalogID != request.CatalogID {
				return nil, errors.New("DuckLake orphan scanner catalog identity changed")
			}
			return analyticsmodule.NewPostgresDuckLakeSnapshotCatalogPageScanner(maintenancePool, catalog.MetadataSchema)
		},
	}
	coordinator.OpenSessionFor = func(ctx context.Context, input analyticsmodule.PostgresDuckLakeRetentionCatalogSessionInput) (analyticsmodule.PostgresDuckLakeRetentionCatalogSession, error) {
		catalog, err := control.LoadCatalog(ctx, input.Request.PhysicalPoolID)
		if err != nil {
			return nil, err
		}
		if catalog.PhysicalPoolID != input.Request.PhysicalPoolID || catalog.CatalogID != input.Request.CatalogID {
			return nil, errors.New("DuckLake retention catalog identity changed")
		}
		maintenanceConfig := cfg.PostgresDuckLakeMaintenanceConfig()
		maintenanceCatalog := postgresDuckLakeRetentionCatalogConfig(input.Request.PhysicalPoolID, catalog.MetadataSchema)
		sessionConfig := ducklake.PostgresCatalogMaintenanceSessionConfig{
			Catalog:            maintenanceCatalog,
			PostgresURL:        maintenanceConfig.URL,
			MaintenanceRole:    maintenanceConfig.RuntimeRole,
			RuntimeRole:        cfg.PostgresDuckLakeRuntimeRole,
			MigratorRole:       cfg.PostgresDuckLakeMigratorRole,
			RuntimeURL:         cfg.PostgresDuckLakeURL,
			MigratorURL:        cfg.PostgresDuckLakeMigratorURL,
			MemoryMaxBytes:     cfg.DuckDBNodeMemoryMaxBytes,
			TempMaxBytes:       cfg.DuckDBNodeTempMaxBytes,
			MaxThreads:         cfg.DuckDBNodeMaxThreads,
			TempDir:            cfg.DuckDBTempDirPath(),
			DataPath:           cfg.DuckLakeDataDir(),
			ExtensionAdmission: extensionAdmission,
		}
		contract := ducklake.PostgresCatalogMaintenanceContract{
			Catalog:           maintenanceCatalog,
			CatalogAlias:      "lake",
			CatalogID:         input.Request.CatalogID,
			PhysicalPoolID:    input.Request.PhysicalPoolID,
			MetadataSchema:    catalog.MetadataSchema,
			DataPath:          cfg.DuckLakeDataDir(),
			MaintenanceRole:   maintenanceConfig.RuntimeRole,
			RuntimeRole:       cfg.PostgresDuckLakeRuntimeRole,
			SharedRuntimePool: false,
			Lease: ducklake.PostgresCatalogMaintenanceLease{
				LeaseID:   input.Request.MaintenanceID,
				OwnerID:   input.Fence.OwnerID,
				ExpiresAt: input.Fence.LeaseExpiresAt,
			},
			Fence: ducklake.PostgresCatalogMaintenanceFence{
				OwnerID:        input.Fence.OwnerID,
				FencingEpoch:   input.Fence.FencingEpoch,
				LeaseExpiresAt: input.Fence.LeaseExpiresAt,
				Verify: func(verifyCtx context.Context) error {
					return control.CheckRetentionMaintenanceFence(verifyCtx, input.Fence)
				},
			},
		}
		return analyticsmodule.OpenPostgresDuckLakeRetentionCatalogSession(ctx, sessionConfig, contract)
	}

	grace := cfg.DuckLakeRetentionFileGracePeriod
	if grace < time.Microsecond {
		grace = defaultDuckLakeRetentionGrace
	}
	return newDuckLakeRetentionWorker(duckLakeRetentionWorkerConfig{
		Interval: cfg.DuckLakeRetentionInterval,
		Acquire:  acquire,
		Pass: func(ctx context.Context) error {
			catalog, err := control.LoadCatalog(ctx, physicalPoolID)
			if err != nil {
				return err
			}
			now := time.Now()
			maintenanceID := duckLakeRetentionOperationID(now, cfg.DuckLakeRetentionInterval, physicalPoolID, catalog.CatalogID)
			policy, policyErr := policyFor(ctx)
			if policyErr != nil {
				return policyErr
			}
			if policy.ReaderGracePeriod < 0 {
				return fmt.Errorf("admitted DuckLake reader grace %s is negative", policy.ReaderGracePeriod)
			}
			orphanGrace := policy.OrphanGracePeriod
			if orphanGrace < time.Microsecond || orphanGrace > analyticsmodule.MaxPostgresDuckLakeSnapshotOrphanScanGrace {
				return fmt.Errorf("admitted DuckLake orphan grace %s is outside (0,%s]", orphanGrace, analyticsmodule.MaxPostgresDuckLakeSnapshotOrphanScanGrace)
			}
			request := analyticsmodule.PostgresDuckLakeRetentionMaintenanceRequest{
				MaintenanceID:     maintenanceID,
				PhysicalPoolID:    physicalPoolID,
				CatalogID:         catalog.CatalogID,
				OwnerID:           ownerID,
				FileGrace:         grace,
				OrphanGracePeriod: orphanGrace,
				OrphanScanID:      analyticsmodule.SnapshotOrphanScanIDForMaintenance(maintenanceID, physicalPoolID, catalog.CatalogID),
				Evidence:          json.RawMessage(`{"source":"lifecycle"}`),
			}
			return runDuckLakeRetentionPass(
				ctx,
				func(phaseCtx context.Context) error {
					return servingStateMaintenance.ReleaseExpiredQuerySnapshotLeases(phaseCtx, environment)
				},
				func(phaseCtx context.Context) error {
					_, drainErr := deliveryMaintenance.Drain(phaseCtx, physicalPoolID, catalog.CatalogID, policy.ReaderGracePeriod, duckLakeRetentionRootBatchLimit)
					return drainErr
				},
				func(physicalCtx context.Context) error {
					_, runErr := coordinator.Run(physicalCtx, request)
					return runErr
				},
			)
		},
		Logger: slog.Default(),
	}), nil
}

// postgresDuckLakeRetentionCatalogConfig is the single attach identity shared
// by the maintenance-session bootstrap and physical-maintenance contract.
// Keeping the dedicated secret names explicit here prevents session-local
// defaults from diverging from the fail-closed contract validation boundary.
func postgresDuckLakeRetentionCatalogConfig(physicalPoolID, metadataSchema string) ducklake.PostgresCatalogConfig {
	return ducklake.PostgresCatalogConfig{
		PhysicalPoolID: physicalPoolID,
		DuckLakeSecret: ducklake.DefaultDuckLakeCatalogMaintenanceSecret,
		PostgresSecret: ducklake.DefaultPostgresCatalogMaintenanceSecret,
		MetadataSchema: metadataSchema,
		Mode:           ducklake.PostgresCatalogWriter,
	}
}
