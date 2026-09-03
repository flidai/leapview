package runtimefactory

import (
	"context"
	"fmt"
	"time"

	analyticsl3 "github.com/flidai/leapview/internal/analytics/cache/l3"
	cachepostgres "github.com/flidai/leapview/internal/analytics/cache/postgres"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/app/gcadapter"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

// TargetL3CacheConfig contains the non-identity policy for one target's
// result cache. Namespace and all storage identity are bound by the caller's
// admitted target/serving state; this helper does not infer them from routes.
type TargetL3CacheConfig struct {
	Namespace            cachepostgres.Namespace
	OriginSnapshotSealID string
	Enabled              bool
	Prefix               string
	MaxObjectBytes       int64
	GracePeriod          time.Duration
	GCLeaseDuration      time.Duration
	GCBatchSize          int
	GCOperationTimeout   time.Duration
	Now                  func() time.Time
}

// NewTargetL3Cache composes the PostgreSQL cache authority and the admitted
// physical-pool object store into one target-scoped L3 capability. The pool's
// stable identity digest is the sole storage security domain; callers cannot
// provide a sibling domain or arbitrary object prefix. Disabled caches return
// a capability without touching credentials, the database, or object storage.
func NewTargetL3Cache(ctx context.Context, contract *ducklake.PoolContract, authority *cachepostgres.Repository, storage gcadapter.S3Config, config TargetL3CacheConfig) (*analyticsl3.Cache, error) {
	if !config.Enabled {
		return analyticsl3.New(analyticsl3.Config{Enabled: false})
	}
	if authority == nil {
		return nil, fmt.Errorf("%w: PostgreSQL cache authority is required", analyticsl3.ErrInvalid)
	}
	if contract == nil || contract.Validate() != nil {
		return nil, fmt.Errorf("%w: admitted physical-pool contract is required", analyticsl3.ErrInvalid)
	}
	domain := string(contract.Pool.ID)
	if platformdigest.ValidateSHA256Identity(domain) != nil {
		return nil, fmt.Errorf("%w: admitted physical-pool security domain is invalid", analyticsl3.ErrInvalid)
	}
	store, err := NewL3ObjectStore(ctx, contract, storage)
	if err != nil {
		return nil, fmt.Errorf("construct target L3 object store: %w", err)
	}
	return analyticsl3.New(analyticsl3.Config{
		Authority:            authority,
		Store:                store,
		Namespace:            config.Namespace,
		OriginSnapshotSealID: config.OriginSnapshotSealID,
		SecurityDomain:       domain,
		Prefix:               config.Prefix,
		Enabled:              true,
		MaxObjectBytes:       config.MaxObjectBytes,
		GracePeriod:          config.GracePeriod,
		GCLeaseDuration:      config.GCLeaseDuration,
		GCBatchSize:          config.GCBatchSize,
		GCOperationTimeout:   config.GCOperationTimeout,
		Now:                  config.Now,
	})
}
