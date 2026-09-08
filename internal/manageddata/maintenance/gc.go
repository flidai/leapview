// Package maintenance provides conservative managed-data capacity and garbage
// collection services. It does not own scheduling or persistence wiring.
package maintenance

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/manageddata/storage"
)

var (
	ErrInvalidMaintenance  = errors.New("invalid managed-data maintenance configuration")
	ErrReachabilityChanged = errors.New("managed-data reachability changed")
)

// ReachabilitySnapshot must contain every digest that is reachable from
// durable metadata at Generation.
type ReachabilitySnapshot struct {
	Generation uint64
	SHA256s    []string
}

// ReachabilitySource supplies complete reachability snapshots. The callback
// passed to WithStableSnapshot must execute while the implementation prevents
// the generation from changing. Returning ErrReachabilityChanged is expected
// when expectedGeneration cannot be held stable.
type ReachabilitySource interface {
	Snapshot(context.Context) (ReachabilitySnapshot, error)
	WithStableSnapshot(context.Context, uint64, func(ReachabilitySnapshot) error) error
}

type BlobGCConfig struct {
	GraceAge  time.Duration
	BatchSize int
	Now       func() time.Time
}

type BlobGCResult struct {
	Candidates     int
	Deleted        int
	ReclaimedBytes int64
	Deferred       bool
}

type BlobCollector struct {
	inventory    storage.BlobInventory
	reachability ReachabilitySource
	graceAge     time.Duration
	batchSize    int
	now          func() time.Time
}

func NewBlobCollector(inventory storage.BlobInventory, reachability ReachabilitySource, config BlobGCConfig) (*BlobCollector, error) {
	if inventory == nil || reachability == nil || config.GraceAge <= 0 || config.BatchSize < 0 || config.BatchSize > 1000 {
		return nil, ErrInvalidMaintenance
	}
	batchSize := config.BatchSize
	if batchSize == 0 {
		batchSize = 100
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &BlobCollector{inventory: inventory, reachability: reachability, graceAge: config.GraceAge, batchSize: batchSize, now: now}, nil
}

func (c *BlobCollector) Run(ctx context.Context) (BlobGCResult, error) {
	if err := ctx.Err(); err != nil {
		return BlobGCResult{}, err
	}
	initial, err := c.reachability.Snapshot(ctx)
	if err != nil {
		return BlobGCResult{}, sanitizeMaintenanceError(ctx, "snapshot reachability", err)
	}
	if err := validateReachabilitySnapshot(initial); err != nil {
		return BlobGCResult{}, err
	}
	sort.Strings(initial.SHA256s)
	cutoff := c.now().UTC().Add(-c.graceAge)
	// Keep only one bounded candidate batch at a time. Retaining every old blob
	// in a process-sized map made GC memory grow with object history.
	candidateBatch := make([]storage.BlobMetadata, 0, c.batchSize)
	var lastInventoryBlob storage.BlobMetadata
	seenInventoryDigest := false
	deferred := false
	stopWalk := errors.New("managed-data GC candidate batch limit reached")
	var batchErr error
	result := BlobGCResult{}
	flush := func() error {
		if len(candidateBatch) == 0 {
			return nil
		}
		batch := append([]storage.BlobMetadata(nil), candidateBatch...)
		candidateBatch = candidateBatch[:0]
		result.Candidates += len(batch)
		err := c.reachability.WithStableSnapshot(ctx, initial.Generation, func(current ReachabilitySnapshot) error {
			if current.Generation != initial.Generation {
				return ErrReachabilityChanged
			}
			if err := validateReachabilitySnapshot(current); err != nil {
				return err
			}
			sort.Strings(current.SHA256s)
			digests := make([]string, 0, len(batch))
			var reclaimable int64
			for _, blob := range batch {
				if !isReachable(current.SHA256s, blob.SHA256) {
					if blob.Size > math.MaxInt64-reclaimable {
						return fmt.Errorf("%w: blob inventory size overflow", storage.ErrIntegrity)
					}
					reclaimable += blob.Size
					digests = append(digests, blob.SHA256)
				}
			}
			if len(digests) == 0 {
				return nil
			}
			if err := c.inventory.DeleteBlobs(ctx, digests); err != nil {
				return err
			}
			result.Deleted += len(digests)
			if reclaimable > math.MaxInt64-result.ReclaimedBytes {
				return fmt.Errorf("%w: reclaimed blob size overflow", storage.ErrIntegrity)
			}
			result.ReclaimedBytes += reclaimable
			return nil
		})
		if errors.Is(err, ErrReachabilityChanged) {
			deferred = true
			return stopWalk
		}
		return err
	}
	err = c.inventory.WalkBlobs(ctx, func(blob storage.BlobMetadata) error {
		if err := validateBlobMetadata(blob); err != nil {
			return err
		}
		// The filesystem and S3 inventories enumerate canonical keys in stable
		// lexicographic order. Adjacent duplicate detection preserves the
		// integrity check without an unbounded global seen set.
		if seenInventoryDigest && blob.SHA256 == lastInventoryBlob.SHA256 {
			if blob != lastInventoryBlob {
				return fmt.Errorf("%w: duplicate blob inventory metadata", storage.ErrIntegrity)
			}
			return nil
		}
		seenInventoryDigest = true
		lastInventoryBlob = blob
		if !isReachable(initial.SHA256s, blob.SHA256) && !blob.LastModified.After(cutoff) {
			candidateBatch = append(candidateBatch, blob)
			if len(candidateBatch) == c.batchSize {
				if err := flush(); err != nil {
					batchErr = err
					return stopWalk
				}
			}
		}
		return nil
	})
	if errors.Is(err, stopWalk) {
		if deferred {
			result.Deferred = true
			return result, nil
		}
		if batchErr != nil {
			return result, sanitizeMaintenanceError(ctx, "delete unreachable blobs", batchErr)
		}
		return result, nil
	}
	if err != nil {
		return BlobGCResult{}, sanitizeMaintenanceError(ctx, "enumerate blobs", err)
	}
	if err := flush(); err != nil {
		if errors.Is(err, stopWalk) && deferred {
			result.Deferred = true
			return result, nil
		}
		return result, sanitizeMaintenanceError(ctx, "delete unreachable blobs", err)
	}
	return result, nil
}

func validateReachabilitySnapshot(snapshot ReachabilitySnapshot) error {
	for _, digest := range snapshot.SHA256s {
		if err := storage.ValidateSHA256(digest); err != nil {
			return fmt.Errorf("%w: reachability contains an invalid digest", storage.ErrIntegrity)
		}
	}
	return nil
}

func isReachable(digests []string, target string) bool {
	index := sort.SearchStrings(digests, target)
	return index < len(digests) && digests[index] == target
}

func validateBlobMetadata(blob storage.BlobMetadata) error {
	if storage.ValidateSHA256(blob.SHA256) != nil || blob.Size < 0 || blob.LastModified.IsZero() {
		return fmt.Errorf("%w: backend returned invalid blob metadata", storage.ErrIntegrity)
	}
	return nil
}

func sanitizeMaintenanceError(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	for _, sentinel := range []error{storage.ErrInvalid, storage.ErrIntegrity, storage.ErrNotFound, storage.ErrBackend, ErrReachabilityChanged} {
		if errors.Is(err, sentinel) {
			return fmt.Errorf("%w: %s", sentinel, operation)
		}
	}
	return fmt.Errorf("%w: %s", storage.ErrBackend, strings.TrimSpace(operation))
}
