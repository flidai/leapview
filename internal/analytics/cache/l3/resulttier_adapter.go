package l3

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	analyticscache "github.com/flidai/leapview/internal/analytics/cache"
	cachepostgres "github.com/flidai/leapview/internal/analytics/cache/postgres"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resulttier"
	"github.com/flidai/leapview/pkg/arrowresult"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
)

const (
	// defaultResultTierLeaseDuration bounds the authority fence held while an
	// immutable Arrow result is uploaded and admitted. The object-first path is
	// intentionally bounded even when a caller supplies an unbounded context.
	defaultResultTierLeaseDuration    = 5 * time.Minute
	defaultResultTierOperationTimeout = 4 * time.Minute
	resultTierReleaseTimeout          = 5 * time.Second
	resultTierObjectFormatVersion     = 1
)

// ResultTier adapts the domain-scoped L3 coordinator to the resulttier.Tier
// contract. It borrows values supplied to Store and transfers ownership of a
// freshly decoded Arrow result to callers of Lookup.
type ResultTier struct {
	cache *Cache
}

var _ resulttier.Tier = (*ResultTier)(nil)

// NewResultTier constructs an adapter for one L3 capability. A nil or
// disabled capability remains safe to use: lookups are misses and stores
// return ErrDisabled.
func NewResultTier(cache *Cache) *ResultTier {
	return &ResultTier{cache: cache}
}

// Lookup resolves the durable manifest for key and decodes its canonical
// Arrow IPC stream. A miss is represented by found=false with a nil error;
// authority/object misses are never confused with decode or infrastructure
// failures. The returned Result is caller-owned and must be released.
func (t *ResultTier) Lookup(ctx context.Context, key analyticscache.Key) (*arrowresult.Result, resultcache.Metadata, resulttier.Admission, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil || t.cache == nil || !t.cache.enabled {
		return nil, resultcache.Metadata{}, nil, false, nil
	}
	manifestKey, err := cachepostgres.ManifestKeyFromIdentity(key)
	if err != nil {
		return nil, resultcache.Metadata{}, nil, false, err
	}
	read, err := t.cache.LookupAndRead(ctx, manifestKey)
	if err != nil {
		// ErrMiss is an expected negative result for tier callers. Preserve all
		// other errors so authorization, storage, and reconciliation failures
		// remain observable.
		if errors.Is(err, ErrMiss) {
			return nil, resultcache.Metadata{}, nil, false, nil
		}
		return nil, resultcache.Metadata{}, nil, false, err
	}
	if !read.Hit {
		return nil, resultcache.Metadata{}, nil, false, nil
	}
	metadata, err := decodeResultTierMetadata(read.Metadata)
	if err != nil {
		if _, rejectErr := t.cache.Reject(ctx, read.Admission, "result-tier metadata is invalid"); rejectErr != nil {
			return nil, resultcache.Metadata{}, nil, false, rejectErr
		}
		return nil, resultcache.Metadata{}, nil, false, nil
	}
	decoded, err := arrowresult.DecodeIPCWithLimit(read.Body, t.cache.maxObjectBytes)
	if err != nil {
		if errors.Is(err, arrowresult.ErrInvalidIPC) || errors.Is(err, arrowresult.ErrIPCTooLarge) {
			if _, rejectErr := t.cache.Reject(ctx, read.Admission, "result-tier Arrow IPC is invalid"); rejectErr != nil {
				return nil, resultcache.Metadata{}, nil, false, rejectErr
			}
			return nil, resultcache.Metadata{}, nil, false, nil
		}
		return nil, resultcache.Metadata{}, nil, false, err
	}
	if err := validateResultTierMetadata(metadata, decoded.Rows()); err != nil {
		decoded.Release()
		if _, rejectErr := t.cache.Reject(ctx, read.Admission, "result-tier metadata is inconsistent with Arrow result"); rejectErr != nil {
			return nil, resultcache.Metadata{}, nil, false, rejectErr
		}
		return nil, resultcache.Metadata{}, nil, false, nil
	}
	return decoded, metadata, resultAdmission{cache: t.cache, snapshot: read.Admission}, true, nil
}

// resultAdmission binds semantic rejection to the exact manifest admitted by
// Lookup. It intentionally exposes no key or namespace mutators to callers.
type resultAdmission struct {
	cache    *Cache
	snapshot AdmissionSnapshot
}

func (a resultAdmission) Reject(ctx context.Context, reason string) error {
	if a.cache == nil {
		return ErrDisabled
	}
	_, err := a.cache.Reject(ctx, a.snapshot, reason)
	return err
}

// Store serializes a borrowed Arrow result into canonical Arrow IPC, uploads
// it object-first, and admits at most one manifest under a
// generated authority fill fence. The caller retains ownership of result;
// this method only holds a temporary lease while encoding.
func (t *ResultTier) Store(ctx context.Context, key analyticscache.Key, result *arrowresult.Result, metadata resultcache.Metadata) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil || t.cache == nil || !t.cache.enabled {
		return ErrDisabled
	}
	if result == nil {
		return fmt.Errorf("%w: Arrow result is required", ErrInvalid)
	}
	manifestKey, err := cachepostgres.ManifestKeyFromIdentity(key)
	if err != nil {
		return err
	}
	// SQL is request/diagnostic context and must never survive into durable L3
	// metadata. Keep the input borrowed and detach warning storage so metadata
	// canonicalization cannot mutate caller-owned memory.
	metadata.SQL = ""
	metadata.Warnings = append([]string(nil), metadata.Warnings...)
	if err := validateResultTierMetadata(metadata, result.Rows()); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	resultLease, err := result.Acquire()
	if err != nil {
		return err
	}
	body, encodeErr := arrowresult.EncodeIPCWithLimit(resultLease, t.cache.maxObjectBytes)
	resultLease.Release()
	if encodeErr != nil {
		return encodeErr
	}
	// Acquire the distributed fence only after local serialization so the
	// lease bounds object upload and admission rather than CPU-only encoding.
	operationCtx, cancel := context.WithTimeout(ctx, defaultResultTierOperationTimeout)
	defer cancel()
	lease, err := t.cache.AcquireFill(operationCtx, manifestKey, uuid.NewString(), defaultResultTierLeaseDuration)
	if err != nil {
		return err
	}
	release := func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), resultTierReleaseTimeout)
		defer cleanupCancel()
		_ = t.cache.ReleaseFill(cleanupCtx, lease)
	}

	// Publish performs the object-first PUT, exact read-back verification, and
	// fence-aware manifest admission. On every path where Publish did not
	// successfully commit, release the lease so a failed producer cannot hold
	// the key indefinitely. A successful Publish atomically releases it.
	_, publishErr := t.cache.Publish(operationCtx, PublishInput{
		Key:      manifestKey,
		Lease:    lease,
		Body:     bytes.NewReader(body),
		Metadata: encodeResultTierMetadata(metadata),
	})
	if publishErr != nil {
		release()
		return publishErr
	}
	return nil
}

// resultTierMetadata is deliberately independent of serving generation. SQL
// is omitted from the wire shape; it is request-specific diagnostic context.
// The version marker allows future metadata evolution without changing Arrow
// object identity or cache keys.
type resultTierMetadata struct {
	Version        int      `json:"version"`
	TotalRows      int      `json:"totalRows"`
	TotalRowsKnown bool     `json:"totalRowsKnown"`
	Warnings       []string `json:"warnings,omitempty"`
}

func encodeResultTierMetadata(metadata resultcache.Metadata) json.RawMessage {
	value, err := json.Marshal(resultTierMetadata{Version: resultTierObjectFormatVersion, TotalRows: metadata.TotalRows, TotalRowsKnown: metadata.TotalRowsKnown, Warnings: append([]string(nil), metadata.Warnings...)})
	if err != nil {
		// The fields above are all JSON-safe primitive values. Keep a defensive
		// fallback so the object-first publish path never emits invalid metadata.
		return json.RawMessage(`{"version":1,"totalRows":0,"totalRowsKnown":false}`)
	}
	return value
}

func decodeResultTierMetadata(raw json.RawMessage) (resultcache.Metadata, error) {
	if len(raw) == 0 {
		return resultcache.Metadata{}, fmt.Errorf("result-tier metadata is empty")
	}
	var value resultTierMetadata
	if err := strictjson.DecodeWithOptions(raw, &value, strictjson.Options{MaxBytes: maxMetadataBytes, DuplicateKeys: strictjson.CaseSensitiveKeys}); err != nil {
		return resultcache.Metadata{}, err
	}
	if value.Version != resultTierObjectFormatVersion {
		return resultcache.Metadata{}, fmt.Errorf("unsupported result-tier metadata version %d", value.Version)
	}
	return resultcache.Metadata{TotalRows: value.TotalRows, TotalRowsKnown: value.TotalRowsKnown, Warnings: append([]string(nil), value.Warnings...)}, nil
}

func validateResultTierMetadata(metadata resultcache.Metadata, rows int64) error {
	if rows < 0 || metadata.TotalRows < 0 {
		return fmt.Errorf("negative row count")
	}
	if !metadata.TotalRowsKnown && metadata.TotalRows != 0 {
		return fmt.Errorf("unknown total rows must be zero")
	}
	if metadata.TotalRowsKnown && int64(metadata.TotalRows) < rows {
		return fmt.Errorf("known total rows are smaller than the Arrow result")
	}
	return nil
}
