package l3

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	cachepostgres "github.com/flidai/leapview/internal/analytics/cache/postgres"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

const (
	DefaultGracePeriod        = time.Hour
	DefaultGCLeaseDuration    = time.Minute
	DefaultGCBatchSize        = 128
	MaxGCBatchSize            = 1000
	MinGCLeaseDuration        = time.Second
	DefaultGCOperationTimeout = 5 * time.Minute
	MaxGCOperationTimeout     = 24 * time.Hour
)

// MaintenanceAuthority is deliberately pool-scoped rather than target
// scoped. Reachability unions every production and candidate namespace in the
// storage-security domain, while the exact object fence excludes concurrent
// object-first publishers.
type MaintenanceAuthority interface {
	ObjectFenceAuthority
	PrepareL3ObjectGC(context.Context, cachepostgres.L3ObjectFence) (bool, error)
}

type CollectorConfig struct {
	Authority        MaintenanceAuthority
	Store            MaintenanceStore
	SecurityDomain   string
	Prefix           string
	GracePeriod      time.Duration
	FenceLease       time.Duration
	BatchSize        int
	OperationTimeout time.Duration
	Now              func() time.Time
}

// Collector owns one physical-pool L3 namespace. It carries no target or
// serving-generation identity, so it cannot accidentally interpret another
// target's live manifest through a narrower namespace view.
type Collector struct {
	authority        MaintenanceAuthority
	store            MaintenanceStore
	securityDomain   string
	objectPrefix     string
	gracePeriod      time.Duration
	fenceLease       time.Duration
	batchSize        int
	operationTimeout time.Duration
	now              func() time.Time
}

func NewCollector(config CollectorConfig) (*Collector, error) {
	if config.Authority == nil || config.Store == nil || platformdigest.ValidateSHA256Identity(config.SecurityDomain) != nil {
		return nil, fmt.Errorf("%w: collector authority, store, and security domain are required", ErrInvalid)
	}
	prefix, err := normalizePrefix(config.Prefix)
	if err != nil {
		return nil, err
	}
	grace := config.GracePeriod
	if grace <= 0 {
		grace = DefaultGracePeriod
	}
	fenceLease := config.FenceLease
	if fenceLease <= 0 {
		fenceLease = DefaultGCLeaseDuration
	}
	if fenceLease < MinObjectFenceLease || fenceLease > MaxGCOperationTimeout {
		return nil, fmt.Errorf("%w: collector fence lease is out of bounds", ErrInvalid)
	}
	batch := config.BatchSize
	if batch <= 0 {
		batch = DefaultGCBatchSize
	}
	if batch > MaxGCBatchSize {
		return nil, fmt.Errorf("%w: collector batch exceeds limit", ErrInvalid)
	}
	timeout := config.OperationTimeout
	if timeout <= 0 {
		timeout = DefaultGCOperationTimeout
	}
	if timeout < MinGCLeaseDuration || timeout > MaxGCOperationTimeout {
		return nil, fmt.Errorf("%w: collector operation timeout is out of bounds", ErrInvalid)
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Collector{authority: config.Authority, store: config.Store, securityDomain: config.SecurityDomain,
		objectPrefix: prefix + "sd/" + config.SecurityDomain + "/", gracePeriod: grace,
		fenceLease: fenceLease, batchSize: batch, operationTimeout: timeout, now: now}, nil
}

type GCResult struct {
	Scanned    int
	Deleted    int
	Skipped    int
	NextCursor string
}

func (c *Collector) GC(ctx context.Context) (GCResult, error) { return c.GCPage(ctx, "") }

func (c *Collector) GCPage(ctx context.Context, after string) (GCResult, error) {
	if c == nil {
		return GCResult{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if after != "" && (!strings.HasPrefix(after, c.objectPrefix) || len(after) > 2048 || strings.TrimSpace(after) != after) {
		return GCResult{}, fmt.Errorf("%w: collector cursor escaped object prefix", ErrInvalid)
	}
	listCtx, cancelList := context.WithTimeout(ctx, c.operationTimeout)
	objects, nextCursor, err := c.store.List(listCtx, c.objectPrefix, after, c.batchSize)
	cancelList()
	if err != nil {
		return GCResult{}, err
	}
	if len(objects) > c.batchSize {
		return GCResult{}, fmt.Errorf("%w: object-store list exceeded page bound", ErrInvalid)
	}
	if nextCursor != "" && (!strings.HasPrefix(nextCursor, c.objectPrefix) || len(nextCursor) > 2048 || strings.TrimSpace(nextCursor) != nextCursor) {
		return GCResult{}, fmt.Errorf("%w: object-store cursor escaped object prefix", ErrInvalid)
	}
	previous := after
	for _, object := range objects {
		if object.Key == "" || !strings.HasPrefix(object.Key, c.objectPrefix) || (previous != "" && object.Key <= previous) {
			return GCResult{}, fmt.Errorf("%w: object-store page is not strictly ordered within prefix", ErrInvalid)
		}
		previous = object.Key
	}
	if nextCursor != "" && (len(objects) == 0 || nextCursor != objects[len(objects)-1].Key) {
		return GCResult{}, fmt.Errorf("%w: object-store cursor does not match page boundary", ErrInvalid)
	}
	result := GCResult{Scanned: len(objects), NextCursor: nextCursor}
	for _, object := range objects {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if object.Key == "" || !strings.HasPrefix(object.Key, c.objectPrefix) || object.SecurityDomain != c.securityDomain ||
			object.CreatedAt.IsZero() || c.now().Sub(object.CreatedAt) < c.gracePeriod {
			result.Skipped++
			continue
		}
		parts := strings.Split(strings.TrimPrefix(object.Key, c.objectPrefix), "/")
		if len(parts) != 2 || platformdigest.ValidateSHA256Identity(parts[0]) != nil || platformdigest.ValidateSHA256Identity(parts[1]) != nil {
			result.Skipped++
			continue
		}
		ownerID := "l3-gc-" + strings.TrimPrefix(digestBytes([]byte(object.Key)), "sha256:")
		acquireCtx, cancelAcquire := context.WithTimeout(ctx, c.operationTimeout)
		fence, acquireErr := c.authority.AcquireL3ObjectFence(acquireCtx, cachepostgres.AcquireL3ObjectFenceInput{
			StorageSecurityDomain: c.securityDomain, ObjectKey: object.Key, OwnerID: ownerID, Lease: c.fenceLease,
		})
		cancelAcquire()
		if acquireErr != nil {
			if errors.Is(acquireErr, cachepostgres.ErrBusy) {
				result.Skipped++
				continue
			}
			return result, acquireErr
		}
		guard := newObjectFenceGuard(ctx, c.authority, fence, c.fenceLease, c.operationTimeout)
		stopFence := func() error { return guard.stopAndRelease(ctx) }
		eligible, prepareErr := c.authority.PrepareL3ObjectGC(guard.ctx, fence)
		if prepareErr != nil {
			_ = stopFence()
			return result, prepareErr
		}
		if err := guard.failure(); err != nil {
			_ = stopFence()
			return result, err
		}
		if !eligible {
			if err := stopFence(); err != nil {
				return result, err
			}
			result.Skipped++
			continue
		}
		if err := guard.renew(); err != nil {
			_ = stopFence()
			return result, err
		}
		if err := c.store.DeleteExact(guard.ctx, object); err != nil {
			_ = stopFence()
			return result, err
		}
		if err := guard.renew(); err != nil {
			_ = stopFence()
			return result, err
		}
		if err := guard.failure(); err != nil {
			_ = stopFence()
			return result, err
		}
		if err := stopFence(); err != nil {
			return result, err
		}
		result.Deleted++
	}
	return result, nil
}

type objectFenceGuard struct {
	authority ObjectFenceAuthority
	fence     cachepostgres.L3ObjectFence
	ctx       context.Context
	cancel    context.CancelFunc
	stop      chan struct{}
	renewErr  chan error
	duration  time.Duration
	renewWait time.Duration
}

func newObjectFenceGuard(parent context.Context, authority ObjectFenceAuthority, fence cachepostgres.L3ObjectFence, duration, timeout time.Duration) *objectFenceGuard {
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	guard := &objectFenceGuard{authority: authority, fence: fence, ctx: ctx, cancel: cancel, stop: make(chan struct{}), renewErr: make(chan error, 1), duration: duration}
	interval := duration / 3
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	guard.renewWait = interval
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := guard.renew(); err != nil {
					select {
					case guard.renewErr <- err:
					default:
					}
					cancel()
					return
				}
			case <-guard.stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return guard
}

func (g *objectFenceGuard) renew() error {
	renewCtx, cancel := context.WithTimeout(g.ctx, g.renewWait)
	defer cancel()
	return g.authority.RenewL3ObjectFence(renewCtx, g.fence, g.duration)
}

func (g *objectFenceGuard) failure() error {
	select {
	case err := <-g.renewErr:
		return err
	case <-g.ctx.Done():
		return g.ctx.Err()
	default:
		return nil
	}
}

func (g *objectFenceGuard) stopAndRelease(parent context.Context) error {
	select {
	case <-g.stop:
	default:
		close(g.stop)
	}
	g.cancel()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	return g.authority.ReleaseL3ObjectFence(cleanupCtx, g.fence)
}
