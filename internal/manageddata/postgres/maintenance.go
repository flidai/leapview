package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	managedmaintenance "github.com/flidai/leapview/internal/manageddata/maintenance"
	manageddb "github.com/flidai/leapview/internal/manageddata/postgres/internal/db"
	"github.com/flidai/leapview/internal/manageddata/storage"
	"github.com/jackc/pgx/v5"
)

// ReachabilitySource is the PostgreSQL maintenance adapter for managed-data
// object retention.  It deliberately reads only durable rows that are
// capable of retaining bytes (ready revisions and non-terminal uploads).
// The database transaction used by WithStableSnapshot holds table-level share
// locks, fencing concurrent lifecycle writes for the duration of the callback.
type ReachabilitySource struct {
	db DBTX
}

var _ managedmaintenance.ReachabilitySource = (*ReachabilitySource)(nil)

// NewReachabilitySource constructs a source over a caller-owned PostgreSQL
// database handle. Snapshot can run over any DBTX (including a transaction);
// WithStableSnapshot additionally requires a pool/connection implementing
// Begin because nested transactions are not supported by pgx.
func NewReachabilitySource(db DBTX) (*ReachabilitySource, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: PostgreSQL database is required", managedmaintenance.ErrInvalidMaintenance)
	}
	return &ReachabilitySource{db: db}, nil
}

func (s *ReachabilitySource) Snapshot(ctx context.Context) (managedmaintenance.ReachabilitySnapshot, error) {
	if s == nil || s.db == nil {
		return managedmaintenance.ReachabilitySnapshot{}, fmt.Errorf("%w: PostgreSQL database is required", managedmaintenance.ErrInvalidMaintenance)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return managedmaintenance.ReachabilitySnapshot{}, err
	}
	return readReachabilitySnapshot(ctx, manageddb.New(s.db))
}

func (s *ReachabilitySource) WithStableSnapshot(
	ctx context.Context,
	expectedGeneration uint64,
	use func(managedmaintenance.ReachabilitySnapshot) error,
) (returnErr error) {
	if s == nil || s.db == nil {
		return fmt.Errorf("%w: PostgreSQL database is required", managedmaintenance.ErrInvalidMaintenance)
	}
	if use == nil {
		return fmt.Errorf("%w: stable snapshot callback is required", managedmaintenance.ErrInvalidMaintenance)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b, ok := s.db.(beginner)
	if !ok {
		return fmt.Errorf("%w: PostgreSQL database must support transactions", managedmaintenance.ErrInvalidMaintenance)
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return sourceError(ctx, "begin stable PostgreSQL snapshot", err)
	}
	active := true
	defer func() {
		if !active {
			return
		}
		if rollbackErr := tx.Rollback(context.Background()); rollbackErr != nil && returnErr == nil {
			returnErr = sourceError(context.Background(), "rollback stable PostgreSQL snapshot", rollbackErr)
		}
	}()

	// REPEATABLE READ gives one MVCC view and READ ONLY prevents accidental
	// writes; table SHARE locks additionally block lifecycle writes (which
	// acquire ROW EXCLUSIVE locks) while the callback performs physical
	// deletion. This closes the race between the initial candidate scan and
	// final reachability check.
	queries := manageddb.New(tx)
	if err := queries.ConfigureStableReachabilitySnapshot(ctx); err != nil {
		return sourceError(ctx, "configure stable PostgreSQL snapshot", err)
	}
	if err := queries.LockStableReachabilitySnapshot(ctx); err != nil {
		return sourceError(ctx, "lock managed-data reachability", err)
	}
	snapshot, err := readReachabilitySnapshot(ctx, queries)
	if err != nil {
		return err
	}
	if snapshot.Generation != expectedGeneration {
		return managedmaintenance.ErrReachabilityChanged
	}
	if err := use(snapshot); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return sourceError(ctx, "commit stable PostgreSQL snapshot", err)
	}
	active = false
	return nil
}

type durableManifest struct {
	sourceType     string
	id             string
	status         string
	revisionDigest string
	manifestJSON   string
	fileCount      int64
	sizeBytes      int64
}

func readReachabilitySnapshot(ctx context.Context, queries *manageddb.Queries) (managedmaintenance.ReachabilitySnapshot, error) {
	rows, err := queries.ListManagedDataReachabilitySources(ctx)
	if err != nil {
		return managedmaintenance.ReachabilitySnapshot{}, sourceError(ctx, "query managed-data reachability", err)
	}
	digests := make(map[string]struct{})
	generation := sha256.New()
	for _, source := range rows {
		row := durableManifest{
			sourceType: source.SourceType, id: source.SourceID, status: source.SourceStatus,
			revisionDigest: source.RevisionDigest, manifestJSON: source.Manifest,
			fileCount: source.FileCount, sizeBytes: source.SizeBytes,
		}
		manifest, canonical, err := validateDurableManifest(row)
		if err != nil {
			return managedmaintenance.ReachabilitySnapshot{}, err
		}
		writeGenerationRecord(generation, row, canonical)
		for _, file := range manifest.Files {
			digests[file.SHA256] = struct{}{}
		}
	}
	sha256s := make([]string, 0, len(digests))
	for digest := range digests {
		sha256s = append(sha256s, digest)
	}
	sort.Strings(sha256s)
	sum := generation.Sum(nil)
	return managedmaintenance.ReachabilitySnapshot{Generation: binary.BigEndian.Uint64(sum[:8]), SHA256s: sha256s}, nil
}

func validateDurableManifest(row durableManifest) (manageddata.Manifest, []byte, error) {
	if row.id == "" || row.fileCount < 0 || row.sizeBytes < 0 {
		return manageddata.Manifest{}, nil, integrityError("invalid durable manifest metadata")
	}
	switch row.sourceType {
	case "revision":
		if row.status != string(manageddata.RevisionStatusReady) {
			return manageddata.Manifest{}, nil, integrityError("invalid ready revision status")
		}
	case "upload":
		if row.status != string(manageddata.UploadStatusOpen) && row.status != string(manageddata.UploadStatusCommitting) {
			return manageddata.Manifest{}, nil, integrityError("invalid nonterminal upload status")
		}
	default:
		return manageddata.Manifest{}, nil, integrityError("invalid durable manifest source")
	}
	decoder := json.NewDecoder(strings.NewReader(row.manifestJSON))
	decoder.DisallowUnknownFields()
	var manifest manageddata.Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return manageddata.Manifest{}, nil, integrityError("invalid durable manifest JSON")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return manageddata.Manifest{}, nil, integrityError("invalid durable manifest JSON")
	}
	canonical, err := manifest.CanonicalJSON()
	// PostgreSQL JSONB may normalize insignificant whitespace and object key
	// order when rendered as text. Compare parsed values to the canonical
	// manifest rather than requiring byte-for-byte JSON text as SQLite does.
	if err != nil || !sameJSON(canonical, []byte(row.manifestJSON)) {
		return manageddata.Manifest{}, nil, integrityError("noncanonical durable manifest")
	}
	var totalSize int64
	for _, file := range manifest.Files {
		if storage.ValidateSHA256(file.SHA256) != nil || file.Size < 0 || totalSize > (1<<63-1)-file.Size {
			return manageddata.Manifest{}, nil, integrityError("invalid durable file digest or size")
		}
		totalSize += file.Size
	}
	if int64(len(manifest.Files)) != row.fileCount || totalSize != row.sizeBytes {
		return manageddata.Manifest{}, nil, integrityError("durable manifest totals do not match")
	}
	if row.sourceType == "revision" {
		if row.revisionDigest != manifest.RevisionID() {
			return manageddata.Manifest{}, nil, integrityError("durable revision digest does not match manifest")
		}
	} else if row.revisionDigest != "" {
		return manageddata.Manifest{}, nil, integrityError("upload has an unexpected revision digest")
	}
	return manifest, canonical, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("trailing JSON value")
	}
	return err
}

func writeGenerationRecord(target hash.Hash, row durableManifest, canonical []byte) {
	writeFramed(target, row.sourceType)
	writeFramed(target, row.id)
	writeFramed(target, row.status)
	writeFramed(target, row.revisionDigest)
	writeFramed(target, string(canonical))
	var numeric [16]byte
	binary.BigEndian.PutUint64(numeric[:8], uint64(row.fileCount))
	binary.BigEndian.PutUint64(numeric[8:], uint64(row.sizeBytes))
	_, _ = target.Write(numeric[:])
}

func writeFramed(target hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = target.Write(length[:])
	_, _ = target.Write([]byte(value))
}

func integrityError(operation string) error {
	return fmt.Errorf("%w: %s", storage.ErrIntegrity, operation)
}

func sourceError(ctx context.Context, operation string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return fmt.Errorf("%w: %s", storage.ErrBackend, strings.TrimSpace(operation))
}

// RetentionRoot records durable reachability for an admitted managed-data
// revision. DuckLake snapshot roots are owned by the DuckLake capability.
// Root state is monotonic: live -> retiring -> expired.
type RetentionRoot struct {
	RootID, ProjectID, Environment, RevisionID string
	State                                      string
	Evidence                                   json.RawMessage
	CreatedAt, UpdatedAt                       time.Time
}

type ReconciliationEvidence struct {
	EvidenceID                                               int64
	ProjectID, Environment, ObjectKey, ObservedState, Action string
	Evidence                                                 json.RawMessage
	ObservedAt                                               time.Time
}

func boundedJSON(v json.RawMessage, max int) ([]byte, error) {
	if len(v) == 0 {
		v = []byte(`{}`)
	}
	if len(v) > max || !json.Valid(v) {
		return nil, ErrInvalid
	}
	return append([]byte(nil), v...), nil
}

func boundedObject(v json.RawMessage, max int) ([]byte, error) {
	if len(v) == 0 {
		return nil, ErrInvalid
	}
	b, err := boundedJSON(v, max)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(b, &object); err != nil || object == nil || len(object) == 0 {
		return nil, ErrInvalid
	}
	return b, nil
}

func (r *Repository) RecordRetentionRoot(ctx context.Context, root RetentionRoot) (RetentionRoot, error) {
	db, err := requireDB(r)
	if err != nil {
		return RetentionRoot{}, err
	}
	// DuckLake snapshot retention/root state has its own capability-owned
	// authority.  Managed-data roots intentionally admit revisions only; a
	// cross-database snapshot tuple would otherwise be unverifiable here.
	if root.RootID == "" || root.ProjectID == "" || root.Environment == "" || root.RevisionID == "" {
		return RetentionRoot{}, ErrInvalid
	}
	if root.State == "" {
		root.State = "live"
	}
	if root.State != "live" && root.State != "retiring" && root.State != "expired" {
		return RetentionRoot{}, ErrInvalid
	}
	evidence, err := boundedObject(root.Evidence, 65536)
	if err != nil {
		return RetentionRoot{}, err
	}
	err = manageddb.New(db).InsertRetentionRoot(contextOrBackground(ctx), manageddb.InsertRetentionRootParams{RootID: root.RootID, ProjectID: root.ProjectID, Environment: root.Environment, RevisionID: &root.RevisionID, State: root.State, Evidence: evidence})
	if err != nil {
		return RetentionRoot{}, err
	}
	stored, err := r.RetentionRootByID(ctx, root.RootID)
	if err != nil {
		return RetentionRoot{}, err
	}
	if stored.ProjectID != root.ProjectID || stored.Environment != root.Environment || stored.RevisionID != root.RevisionID || stored.State != root.State || !sameJSON(stored.Evidence, evidence) {
		return RetentionRoot{}, ErrConflict
	}
	return stored, nil
}

func (r *Repository) RetentionRootByID(ctx context.Context, id string) (RetentionRoot, error) {
	db, err := requireDB(r)
	if err != nil {
		return RetentionRoot{}, err
	}
	row, err := manageddb.New(db).GetRetentionRoot(contextOrBackground(ctx), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return RetentionRoot{}, ErrNotFound
	}
	if err != nil {
		return RetentionRoot{}, err
	}
	out := RetentionRoot{RootID: row.RootID, ProjectID: row.ProjectID, Environment: row.Environment, State: row.State, Evidence: append([]byte(nil), row.Evidence...), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
	if row.RevisionID != nil {
		out.RevisionID = *row.RevisionID
	}
	return out, nil
}

func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func (r *Repository) TransitionRetentionRoot(ctx context.Context, id, target string) (RetentionRoot, error) {
	db, err := requireDB(r)
	if err != nil {
		return RetentionRoot{}, err
	}
	if target != "retiring" && target != "expired" {
		return RetentionRoot{}, ErrInvalid
	}
	tag, err := manageddb.New(db).TransitionRetentionRoot(contextOrBackground(ctx), manageddb.TransitionRetentionRootParams{RootID: id, State: target})
	if err != nil {
		return RetentionRoot{}, err
	}
	if tag.RowsAffected() != 1 {
		root, e := r.RetentionRootByID(ctx, id)
		if e != nil {
			return RetentionRoot{}, e
		}
		if root.State == target {
			return root, nil
		}
		return RetentionRoot{}, ErrConflict
	}
	return r.RetentionRootByID(ctx, id)
}

func (r *Repository) RecordReconciliationEvidence(ctx context.Context, evidence ReconciliationEvidence) (ReconciliationEvidence, error) {
	db, err := requireDB(r)
	if err != nil {
		return ReconciliationEvidence{}, err
	}
	if evidence.ProjectID == "" || evidence.Environment == "" || evidence.ObjectKey == "" || evidence.Action == "" {
		return ReconciliationEvidence{}, ErrInvalid
	}
	b, err := boundedObject(evidence.Evidence, 65536)
	if err != nil {
		return ReconciliationEvidence{}, err
	}
	row, err := manageddb.New(db).InsertReconciliationEvidence(contextOrBackground(ctx), manageddb.InsertReconciliationEvidenceParams{ProjectID: evidence.ProjectID, Environment: evidence.Environment, ObjectKey: evidence.ObjectKey, ObservedState: evidence.ObservedState, Action: evidence.Action, Evidence: b})
	if err != nil {
		return evidence, err
	}
	evidence.EvidenceID, evidence.ProjectID, evidence.Environment, evidence.ObjectKey, evidence.ObservedState, evidence.Action, evidence.Evidence, evidence.ObservedAt = row.EvidenceID, row.ProjectID, row.Environment, row.ObjectKey, row.ObservedState, row.Action, append([]byte(nil), row.Evidence...), row.ObservedAt.Time
	return evidence, nil
}
