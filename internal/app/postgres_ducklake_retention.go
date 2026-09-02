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
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/extension"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
	"github.com/google/uuid"
)

const (
	defaultDuckLakeRetentionInterval = time.Hour
	defaultDuckLakeRetentionGrace    = 24 * time.Hour
)

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
	control *ducklakepostgres.Repository,
	maintenancePool *platformpostgres.Pool,
	extensionAdmission extension.Admission,
	physicalPoolID string,
	ownerID string,
	acquire func(context.Context) (workloadmodule.Lease, error),
	policyFor func(context.Context) (time.Duration, error),
) (*duckLakeRetentionWorker, error) {
	if control == nil || !control.Configured() || !control.TransactionCapable() {
		return nil, errors.New("DuckLake retention control maintenance repository is unavailable")
	}
	if maintenancePool == nil || maintenancePool.PoolConfig().MinConns != 1 || maintenancePool.PoolConfig().MaxConns != 1 {
		return nil, errors.New("DuckLake retention DuckLake maintenance pool is unavailable")
	}
	physicalPoolID = strings.TrimSpace(physicalPoolID)
	ownerID = strings.TrimSpace(ownerID)
	if physicalPoolID == "" || ownerID == "" || extensionAdmission == nil || acquire == nil || policyFor == nil {
		return nil, errors.New("DuckLake retention worker configuration is incomplete")
	}

	coordinator := &ducklakepostgres.RetentionCoordinator{Control: control}
	coordinator.Orphans = &ducklakepostgres.SnapshotOrphanCoordinator{
		Control: control,
		OpenScannerFor: func(ctx context.Context, request ducklakepostgres.SnapshotOrphanMaintenanceRequest) (ducklakepostgres.SnapshotCatalogPageScanner, error) {
			catalog, err := control.LoadCatalog(ctx, request.PhysicalPoolID)
			if err != nil {
				return nil, err
			}
			if catalog.PhysicalPoolID != request.PhysicalPoolID || catalog.CatalogID != request.CatalogID {
				return nil, errors.New("DuckLake orphan scanner catalog identity changed")
			}
			return ducklakepostgres.NewPostgresSnapshotCatalogPageScanner(maintenancePool, catalog.MetadataSchema)
		},
	}
	coordinator.OpenSessionFor = func(ctx context.Context, input ducklakepostgres.RetentionCatalogSessionInput) (ducklakepostgres.RetentionCatalogSession, error) {
		catalog, err := control.LoadCatalog(ctx, input.Request.PhysicalPoolID)
		if err != nil {
			return nil, err
		}
		if catalog.PhysicalPoolID != input.Request.PhysicalPoolID || catalog.CatalogID != input.Request.CatalogID {
			return nil, errors.New("DuckLake retention catalog identity changed")
		}
		maintenanceConfig := cfg.PostgresDuckLakeMaintenanceConfig()
		sessionConfig := ducklake.PostgresCatalogMaintenanceSessionConfig{
			Catalog: ducklake.PostgresCatalogConfig{
				PhysicalPoolID: input.Request.PhysicalPoolID,
				MetadataSchema: catalog.MetadataSchema,
				Mode:           ducklake.PostgresCatalogWriter,
			},
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
			Catalog:           sessionConfig.Catalog,
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
		return ducklakepostgres.OpenPostgresRetentionCatalogSession(ctx, sessionConfig, contract)
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
			orphanGrace, policyErr := policyFor(ctx)
			if policyErr != nil {
				return policyErr
			}
			if orphanGrace < time.Microsecond || orphanGrace > ducklakepostgres.MaxSnapshotOrphanScanGrace {
				return fmt.Errorf("admitted DuckLake orphan grace %s is outside (0,%s]", orphanGrace, ducklakepostgres.MaxSnapshotOrphanScanGrace)
			}
			request := ducklakepostgres.RetentionMaintenanceRequest{
				MaintenanceID:     maintenanceID,
				PhysicalPoolID:    physicalPoolID,
				CatalogID:         catalog.CatalogID,
				OwnerID:           ownerID,
				FileGrace:         grace,
				OrphanGracePeriod: orphanGrace,
				OrphanScanID:      ducklakepostgres.SnapshotOrphanScanIDForMaintenance(maintenanceID, physicalPoolID, catalog.CatalogID),
				Evidence:          json.RawMessage(`{"source":"lifecycle"}`),
			}
			_, err = coordinator.Run(ctx, request)
			return err
		},
		Logger: slog.Default(),
	}), nil
}
