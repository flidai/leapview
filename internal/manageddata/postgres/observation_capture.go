package postgres

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	manageddb "github.com/flidai/leapview/internal/manageddata/postgres/internal/db"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	providerObservationPageSize          int32 = 128
	providerObservationMaxDuration             = 30 * time.Second
	providerObservationMaxManifestBytes        = 1 << 20
	providerObservationRollbackTimeout         = time.Second
	providerObservationMaxRevisions            = 4096
	providerObservationMaxRevisionFiles        = 4096
	providerObservationMaxObjects              = 16384
	providerObservationMaxInventoryBytes       = 8 << 20
)

type capturedProjectionFile = manageddata.CapturedProjectionFile
type capturedProjectionRevision = manageddata.CapturedProjectionRevision

type observationTxBeginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

var _ manageddata.ProjectionCaptureSource = (*Repository)(nil)

// CaptureManagedProjection obtains one primary PostgreSQL restore marker and
// the complete set of ready managed-data revisions under one locked snapshot.
// It performs no object-provider I/O and accepts no caller-provided evidence.
func (r *Repository) CaptureManagedProjection(ctx context.Context) (manageddata.CapturedProjection, error) {
	db, err := requireDB(r)
	if err != nil {
		return manageddata.CapturedProjection{}, err
	}
	return captureManagedProjection(ctx, db)
}

func captureManagedProjection(ctx context.Context, db DBTX) (manageddata.CapturedProjection, error) {
	if ctx == nil || db == nil {
		return manageddata.CapturedProjection{}, fmt.Errorf("%w: database connection is required", ErrInvalid)
	}
	captureCtx, cancel := context.WithTimeout(ctx, providerObservationMaxDuration)
	defer cancel()
	beginner, ok := db.(observationTxBeginner)
	if !ok {
		return manageddata.CapturedProjection{}, fmt.Errorf("%w: provider observation capture requires a PostgreSQL connection or pool with BeginTx", ErrInvalid)
	}
	tx, err := beginner.BeginTx(captureCtx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return manageddata.CapturedProjection{}, fmt.Errorf("begin provider observation capture: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), providerObservationRollbackTimeout)
			defer rollbackCancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	queries := manageddb.New(tx)
	// LOCK is the first statement in the transaction. Repeatable-read's MVCC
	// snapshot is established only by the first source read below, after all
	// authority locks have been acquired.
	if err := queries.LockProviderObservationTables(captureCtx); err != nil {
		return manageddata.CapturedProjection{}, fmt.Errorf("lock managed-data observation tables: %w", err)
	}
	fsync, err := queries.GetProviderObservationFsync(captureCtx)
	if err != nil {
		return manageddata.CapturedProjection{}, fmt.Errorf("read PostgreSQL fsync setting: %w", err)
	}
	if err := validateProviderObservationFsync(fsync); err != nil {
		return manageddata.CapturedProjection{}, err
	}
	epochBefore, err := queries.GetProviderObservationEpoch(captureCtx)
	if err != nil {
		return manageddata.CapturedProjection{}, fmt.Errorf("read managed-data reachability epoch: %w", err)
	}
	if epochBefore.InRecovery {
		return manageddata.CapturedProjection{}, fmt.Errorf("%w: provider observation requires a writable primary", ErrInvalid)
	}

	revisions, err := captureProviderObservationInventory(captureCtx, queries)
	if err != nil {
		return manageddata.CapturedProjection{}, err
	}
	epochAfter, err := queries.GetProviderObservationEpoch(captureCtx)
	if err != nil {
		return manageddata.CapturedProjection{}, fmt.Errorf("read managed-data reachability epoch after inventory: %w", err)
	}
	if epochAfter.InRecovery {
		return manageddata.CapturedProjection{}, fmt.Errorf("%w: provider observation primary changed during capture", ErrInvalid)
	}
	if epochAfter.Epoch != epochBefore.Epoch {
		return manageddata.CapturedProjection{}, fmt.Errorf("%w: managed-data reachability epoch changed during capture", ErrInvalid)
	}

	name, err := providerObservationRestorePointName()
	if err != nil {
		return manageddata.CapturedProjection{}, err
	}
	point, err := queries.CreateProviderObservationRestorePoint(captureCtx, name)
	if err != nil {
		return manageddata.CapturedProjection{}, fmt.Errorf("create PostgreSQL restore point: %w", err)
	}
	timeline, err := providerObservationTimeline(point.WalFile)
	if err != nil {
		return manageddata.CapturedProjection{}, err
	}
	// XLogRestorePoint inserts a WAL record but does not itself flush it on
	// PostgreSQL 18. Poll this exact marker on the same transaction connection
	// before releasing the table locks or committing, so a failover cannot make
	// a different timeline appear durable.
	if err := waitForProviderObservationWALFlush(captureCtx, tx, point.Lsn, point.SystemIdentity, timeline); err != nil {
		return manageddata.CapturedProjection{}, err
	}
	if err := tx.Commit(captureCtx); err != nil {
		return manageddata.CapturedProjection{}, fmt.Errorf("commit provider observation capture: %w", err)
	}
	committed = true
	return manageddata.CapturedProjection{
		DatabaseIdentity: point.DatabaseIdentity,
		SystemIdentity:   point.SystemIdentity,
		Timeline:         timeline,
		LSN:              point.Lsn,
		RestorePointName: name,
		Revisions:        revisions,
	}, nil
}

func waitForProviderObservationWALFlush(ctx context.Context, db DBTX, markerLSN, systemIdentity string, timeline uint32) error {
	marker, err := providerObservationLSN(markerLSN)
	if err != nil {
		return fmt.Errorf("%w: restore point returned invalid LSN: %v", ErrInvalid, err)
	}
	queries := manageddb.New(db)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		flushed, queryErr := queries.GetProviderObservationWALFlushLSN(ctx)
		if queryErr != nil {
			return fmt.Errorf("read PostgreSQL WAL flush LSN: %w", queryErr)
		}
		if err := validateProviderObservationFsync(flushed.Fsync); err != nil {
			return err
		}
		if flushed.InRecovery || flushed.SystemIdentity != systemIdentity {
			return fmt.Errorf("%w: PostgreSQL primary identity changed while waiting for WAL flush", ErrInvalid)
		}
		flushedTimeline, timelineErr := providerObservationTimeline(flushed.WalFile)
		if timelineErr != nil {
			return fmt.Errorf("validate PostgreSQL WAL flush timeline: %w", timelineErr)
		}
		if flushedTimeline != timeline {
			return fmt.Errorf("%w: PostgreSQL WAL timeline changed while waiting for WAL flush", ErrInvalid)
		}
		flush, parseErr := providerObservationLSN(flushed.FlushLsn)
		if parseErr != nil {
			return fmt.Errorf("%w: PostgreSQL returned invalid WAL flush LSN: %v", ErrInvalid, parseErr)
		}
		if flush >= marker {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for PostgreSQL WAL flush through restore point %s: %w", markerLSN, ctx.Err())
		case <-ticker.C:
		}
	}
}

func validateProviderObservationFsync(value string) error {
	if !strings.EqualFold(strings.TrimSpace(value), "on") {
		return fmt.Errorf("%w: PostgreSQL fsync is %q; trusted observation capture requires fsync=on", ErrInvalid, value)
	}
	return nil
}

func providerObservationLSN(value string) (uint64, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, fmt.Errorf("LSN %q is not two hexadecimal components", value)
	}
	high, err := strconv.ParseUint(parts[0], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("LSN %q high component: %w", value, err)
	}
	low, err := strconv.ParseUint(parts[1], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("LSN %q low component: %w", value, err)
	}
	return (high << 32) | low, nil
}

func captureProviderObservationInventory(ctx context.Context, queries *manageddb.Queries) ([]capturedProjectionRevision, error) {
	inventory := make([]capturedProjectionRevision, 0)
	after := ""
	totalFiles := 0
	// Include a conservative lower bound for the enclosing inventory object;
	// every revision is checked against this budget before it is appended.
	inventoryBytes := len(`{"schema_version":1,"revisions":[]}`)
	for {
		rows, err := queries.ListProviderObservationRevisions(ctx, manageddb.ListProviderObservationRevisionsParams{
			AfterRevisionID: after,
			PageSize:        providerObservationPageSize,
		})
		if err != nil {
			return nil, fmt.Errorf("list ready managed-data revisions: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			if len(inventory) >= providerObservationMaxRevisions {
				return nil, fmt.Errorf("%w: ready revision count exceeds capture limit", ErrInvalid)
			}
			if row.FileCount > int64(providerObservationMaxRevisionFiles) {
				return nil, fmt.Errorf("%w: revision %q file count exceeds capture limit", ErrInvalid, row.RevisionID)
			}
			revision, count, err := captureProviderObservationRevision(ctx, queries, row)
			if err != nil {
				return nil, err
			}
			totalFiles += count
			if totalFiles > providerObservationMaxObjects {
				return nil, fmt.Errorf("%w: managed-data file count exceeds capture limit", ErrInvalid)
			}
			revisionBytes := providerObservationRevisionBytes(revision)
			if revisionBytes > providerObservationMaxInventoryBytes-inventoryBytes {
				return nil, fmt.Errorf("%w: managed-data inventory exceeds capture limit", ErrInvalid)
			}
			inventory = append(inventory, revision)
			inventoryBytes += revisionBytes
			after = row.RevisionID
		}
		if len(rows) < int(providerObservationPageSize) {
			break
		}
	}
	return inventory, nil
}

func captureProviderObservationRevision(ctx context.Context, queries *manageddb.Queries, row manageddb.ListProviderObservationRevisionsRow) (capturedProjectionRevision, int, error) {
	if _, err := manageddata.ParseRevisionID(row.RevisionID); err != nil {
		return capturedProjectionRevision{}, 0, fmt.Errorf("%w: revision %q has invalid identity: %v", ErrInvalid, row.RevisionID, err)
	}
	if row.FileCount < 0 || row.SizeBytes < 0 {
		return capturedProjectionRevision{}, 0, fmt.Errorf("%w: revision %q has negative stored count or size", ErrInvalid, row.RevisionID)
	}
	var manifest manageddata.Manifest
	if err := strictjson.DecodeWithOptions([]byte(row.Manifest), &manifest, strictjson.Options{
		MaxBytes:           providerObservationMaxManifestBytes,
		MaxDepth:           32,
		DuplicateKeys:      strictjson.CaseFoldedKeys,
		AllowUnknownFields: false,
	}); err != nil {
		return capturedProjectionRevision{}, 0, fmt.Errorf("%w: decode revision %q manifest: %v", ErrInvalid, row.RevisionID, err)
	}
	canonicalManifest, err := manifest.CanonicalJSON()
	if err != nil {
		return capturedProjectionRevision{}, 0, fmt.Errorf("%w: validate revision %q manifest: %v", ErrInvalid, row.RevisionID, err)
	}
	if got := digestBytes(canonicalManifest); got != row.Digest {
		return capturedProjectionRevision{}, 0, fmt.Errorf("%w: revision %q manifest digest %q does not match canonical digest %q", ErrInvalid, row.RevisionID, row.Digest, got)
	}
	if len(manifest.Files) != int(row.FileCount) {
		return capturedProjectionRevision{}, 0, fmt.Errorf("%w: revision %q manifest file count does not match stored count", ErrInvalid, row.RevisionID)
	}
	files, err := queries.ListProviderObservationRevisionFiles(ctx, row.RevisionID)
	if err != nil {
		return capturedProjectionRevision{}, 0, fmt.Errorf("list files for ready revision %q: %w", row.RevisionID, err)
	}
	if len(files) != len(manifest.Files) || len(files) != int(row.FileCount) {
		return capturedProjectionRevision{}, 0, fmt.Errorf("%w: ready revision %q stored file count is not exact", ErrInvalid, row.RevisionID)
	}
	manifestByPath := make(map[string]manageddata.File, len(manifest.Files))
	for _, file := range manifest.Files {
		manifestByPath[file.Path] = file
	}
	observed := make([]capturedProjectionFile, 0, len(files))
	var totalSize int64
	for _, file := range files {
		if file.RevisionID != row.RevisionID {
			return capturedProjectionRevision{}, 0, fmt.Errorf("%w: revision file returned under wrong revision", ErrInvalid)
		}
		admitted, ok := manifestByPath[file.LogicalPath]
		if !ok || admitted.Size != file.SizeBytes || admitted.SHA256 != file.Sha256 || file.StorageKey == "" {
			return capturedProjectionRevision{}, 0, fmt.Errorf("%w: ready revision %q file %q does not match its manifest", ErrInvalid, row.RevisionID, file.LogicalPath)
		}
		if file.SizeBytes > 0 && totalSize > int64(^uint64(0)>>1)-file.SizeBytes {
			return capturedProjectionRevision{}, 0, fmt.Errorf("%w: ready revision %q file size overflows int64", ErrInvalid, row.RevisionID)
		}
		totalSize += file.SizeBytes
		observed = append(observed, capturedProjectionFile{Path: file.LogicalPath, SHA256: file.Sha256, StorageKey: file.StorageKey, Size: file.SizeBytes})
	}
	if totalSize != row.SizeBytes {
		return capturedProjectionRevision{}, 0, fmt.Errorf("%w: ready revision %q stored size is not exact", ErrInvalid, row.RevisionID)
	}
	if len(observed) != len(manifestByPath) {
		return capturedProjectionRevision{}, 0, fmt.Errorf("%w: ready revision %q contains duplicate or missing file paths", ErrInvalid, row.RevisionID)
	}
	return capturedProjectionRevision{RevisionID: row.RevisionID, ManifestDigest: row.Digest, Files: observed}, len(observed), nil
}

func providerObservationRevisionBytes(revision capturedProjectionRevision) int {
	// Fixed JSON field names, punctuation, and quoting are deliberately
	// rounded up. Six bytes per source byte covers JSON \u00XX escaping, so
	// this guard rejects before accumulating beyond the bounded source
	// document even when source keys contain escapable characters.
	n := 6*(len(revision.RevisionID)+len(revision.ManifestDigest)) + 128
	for _, file := range revision.Files {
		n += 6*(len(file.Path)+len(file.SHA256)+len(file.StorageKey)) + 128
	}
	return n
}

func providerObservationRestorePointName() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate provider observation restore point name: %w", err)
	}
	return "provider_observation_" + id.String(), nil
}

func providerObservationTimeline(walFile string) (uint32, error) {
	if len(walFile) != 24 {
		return 0, fmt.Errorf("%w: PostgreSQL restore point returned no WAL file", ErrInvalid)
	}
	if _, err := hex.DecodeString(walFile); err != nil {
		return 0, fmt.Errorf("%w: PostgreSQL restore point returned invalid WAL file %q", ErrInvalid, walFile)
	}
	timeline, err := strconv.ParseUint(walFile[:8], 16, 32)
	if err != nil || timeline == 0 {
		return 0, fmt.Errorf("%w: PostgreSQL restore point returned invalid WAL timeline", ErrInvalid)
	}
	return uint32(timeline), nil
}
