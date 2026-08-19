package runtimefactory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	physicalpoolsqlite "github.com/flidai/leapview/internal/analytics/physicalpool/sqlite"
	"github.com/flidai/leapview/internal/app/gcadapter"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/gc"
	deploymentsqlite "github.com/flidai/leapview/internal/deployment/sqlite"
)

type ProductionGCRunConfig struct {
	Database      *sql.DB
	TargetID      string
	ProjectID     string
	Environment   string
	OwnerID       string
	HolderID      string
	StagingRoot   string
	PoolS3        gcadapter.S3Config
	LeaseDuration time.Duration
	BuildGrace    time.Duration
	OrphanGrace   time.Duration
	ReaderGrace   time.Duration
}

// RunSQLiteProductionGC resolves the currently active delivery root and runs
// one global mark-and-sweep pass for its admitted physical pool. A missing
// active generation is a normal pre-deployment no-op; all storage and catalog
// errors are returned for degraded health and retry.
func RunSQLiteProductionGC(ctx context.Context, config ProductionGCRunConfig) error {
	if config.Database == nil || config.TargetID == "" || config.Environment == "" || config.OwnerID == "" || config.HolderID == "" {
		return fmt.Errorf("production GC database, target, scope, and holder are required")
	}
	delivery := deploymentsqlite.NewRepositoryWithHooks(config.Database, deploymentsqlite.ActivationHooks{})
	pools := physicalpoolsqlite.NewRepository(config.Database)
	poolIDs, err := durableGCPoolIDs(ctx, config.Database)
	if err != nil {
		return err
	}
	for _, poolID := range poolIDs {
		digests, err := durablePoolCompatibilityDigests(ctx, config.Database, poolID)
		if err != nil {
			return err
		}
		if len(digests) == 0 {
			continue
		}
		var stores gc.PoolStore
		var ownership physicalpool.NamespaceOwnership
		var deletionLease physicalpool.NamespaceDeletionLease
		claims := make([]physicalpool.OwnershipClaim, 0, len(digests))
		inspectors := make(map[string]gc.Inspector, len(digests))
		for _, compatibilityDigest := range digests {
			admission, err := pools.LoadAdmissionContractByCompatibilityDigest(ctx, physicalpool.PoolID(poolID), compatibilityDigest)
			if err != nil {
				return fmt.Errorf("load physical-pool admission %s/%s: %w", poolID, compatibilityDigest, err)
			}
			contract := &ducklake.PoolContract{Pool: admission.Pool, Tuple: admission.Pool.Compatibility, Admission: admission.Admission, Evidence: admission.Evidence}
			store, err := gcadapter.NewPoolStore(ctx, contract, config.PoolS3)
			if err != nil {
				return err
			}
			credentialBootstrap, err := gcadapter.NewPoolCredentialBootstrap(contract, config.PoolS3)
			if err != nil {
				return err
			}
			if stores == nil {
				stores = store
				var ok bool
				ownership, ok = store.(physicalpool.NamespaceOwnership)
				if !ok {
					return fmt.Errorf("physical-pool store does not support ownership markers")
				}
				deletionLease, ok = store.(physicalpool.NamespaceDeletionLease)
				if !ok {
					return fmt.Errorf("physical-pool store does not support deletion leases")
				}
			}
			claims = append(claims, physicalpool.OwnershipClaim{
				PoolID:              physicalpool.PoolID(poolID),
				CompatibilityDigest: admission.Admission.CompatibilityDigest,
				EvidenceDigest:      admission.Admission.EvidenceDigest,
				OwnerID:             config.OwnerID,
			})
			inspectors[compatibilityDigest] = gcadapter.Inspector{Store: store, PoolContract: contract, StagingRoot: config.StagingRoot, ExtensionAdmission: config.PoolS3.ExtensionAdmission, CredentialBootstrap: credentialBootstrap}
		}
		inspector := compatibilityInspector{db: config.Database, poolID: poolID, inspectors: inspectors}
		runner, err := gcadapter.NewProductionRunner(delivery, stores, inspector, gc.Config{
			PhysicalPoolID: poolID, HolderID: boundedGCHolderID(config.HolderID, poolID),
			LeaseDuration: config.LeaseDuration, BuildGrace: config.BuildGrace,
			OrphanGrace: config.OrphanGrace, ReaderGrace: config.ReaderGrace,
			Ownership: ownership, OwnershipClaims: claims, RequireOwnership: true,
			DeletionLease: deletionLease, LeaseOwnerID: config.OwnerID, RequireLease: true,
		})
		if err != nil {
			return err
		}
		if _, err := runner.Run(ctx); err != nil {
			return err
		}
	}
	return nil
}

func boundedGCHolderID(holderID, poolID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(holderID) + "\x00" + strings.TrimSpace(poolID)))
	return "gc-holder-" + hex.EncodeToString(digest[:])
}

// compatibilityInspector dispatches each rooted catalog through the exact
// persisted admission tuple that produced it, while the collector holds one
// pool fence and computes one union mark before sweeping shared storage.
type compatibilityInspector struct {
	db         *sql.DB
	poolID     string
	inspectors map[string]gc.Inspector
}

func (i compatibilityInspector) Inspect(ctx context.Context, root deployment.DeliveryRoot) (gc.CatalogReachability, error) {
	digest, err := rootCompatibilityDigest(ctx, i.db, i.poolID, root)
	if err != nil {
		return gc.CatalogReachability{}, err
	}
	inspector := i.inspectors[digest]
	if inspector == nil {
		return gc.CatalogReachability{}, fmt.Errorf("no admitted compatibility contract for rooted catalog %s/%s", root.Kind, root.SourceID)
	}
	return inspector.Inspect(ctx, root)
}

func rootCompatibilityDigest(ctx context.Context, db *sql.DB, poolID string, root deployment.DeliveryRoot) (string, error) {
	if db == nil || root.PhysicalPoolID != poolID {
		return "", fmt.Errorf("root compatibility lookup is outside physical pool")
	}
	var digest string
	var err error
	switch root.Kind {
	case "build":
		err = db.QueryRowContext(ctx, `SELECT compatibility_digest FROM delivery_catalog_seals WHERE id=? AND physical_pool_id=?`, root.SourceID, poolID).Scan(&digest)
	case "candidate":
		err = db.QueryRowContext(ctx, `SELECT compatibility_digest FROM delivery_candidates WHERE id=? AND physical_pool_id=?`, root.SourceID, poolID).Scan(&digest)
	case "published", "rollback":
		err = db.QueryRowContext(ctx, `SELECT compatibility_digest FROM delivery_generations WHERE id=? AND physical_pool_id=?`, root.GenerationID, poolID).Scan(&digest)
	case "lease", "retained", "quarantined":
		if root.CandidateID != "" {
			err = db.QueryRowContext(ctx, `SELECT compatibility_digest FROM delivery_candidates WHERE id=? AND physical_pool_id=?`, root.CandidateID, poolID).Scan(&digest)
		} else if root.GenerationID != "" {
			err = db.QueryRowContext(ctx, `SELECT compatibility_digest FROM delivery_generations WHERE id=? AND physical_pool_id=?`, root.GenerationID, poolID).Scan(&digest)
		} else {
			err = fmt.Errorf("root has no candidate or generation binding")
		}
	default:
		err = fmt.Errorf("unsupported root kind %q", root.Kind)
	}
	if err != nil {
		return "", fmt.Errorf("resolve root compatibility %s/%s: %w", root.Kind, root.SourceID, err)
	}
	if err := deployment.ValidateDeliveryDigest(digest); err != nil {
		return "", fmt.Errorf("root compatibility digest: %w", err)
	}
	return digest, nil
}

func durableGCPoolIDs(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT physical_pool_id FROM (
SELECT id AS physical_pool_id FROM physical_pools
UNION SELECT pool_id AS physical_pool_id FROM physical_pool_admissions
UNION SELECT physical_pool_id FROM delivery_candidates
UNION SELECT physical_pool_id FROM delivery_generations
UNION SELECT physical_pool_id FROM delivery_catalog_seals
UNION SELECT physical_pool_id FROM delivery_root_registry)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if strings.TrimSpace(id) != "" {
			result = append(result, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(result)
	return result, nil
}

func durablePoolCompatibilityDigests(ctx context.Context, db *sql.DB, poolID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT compatibility_digest FROM physical_pool_admissions WHERE pool_id=? AND compatibility_digest<>''`, poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil {
			return nil, err
		}
		result = append(result, digest)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(result)
	return result, nil
}
