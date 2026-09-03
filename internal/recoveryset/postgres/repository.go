// Package postgres persists the recoveryset domain in the control database.
// All writes use generated sqlc leaves and caller-owned transactions.
package postgres

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/recoveryset"
	recoverydb "github.com/flidai/leapview/internal/recoveryset/postgres/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Tx interface {
	DBTX
	Commit(context.Context) error
	Rollback(context.Context) error
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

//go:embed schema.sql
var schemaFS embed.FS

var schemaSQL = func() string {
	b, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		panic(err)
	}
	return string(b)
}()

func SchemaSQL() string { return schemaSQL }

func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return recoveryset.ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// sqlc-exception: schema-ddl. Capability-owned schema, triggers and ACLs.
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

type Repository struct{ db DBTX }

func New(db DBTX) *Repository          { return &Repository{db: db} }
func (r *Repository) Configured() bool { return r != nil && r.db != nil }

// Create persists a complete frontier in one transaction. Retries with the
// same set ID are exact and idempotent; any identity or metadata drift is a
// conflict and never appends child evidence to the existing row.
func (r *Repository) Create(ctx context.Context, set recoveryset.RecoverySet) (recoveryset.RecoverySet, error) {
	if r == nil || r.db == nil {
		return recoveryset.RecoverySet{}, recoveryset.ErrInvalid
	}
	normalized, err := set.Normalize()
	if err != nil {
		return recoveryset.RecoverySet{}, err
	}
	if normalized.Status != recoveryset.StatusPrepared || normalized.PublishedValidationAttemptID != "" {
		return recoveryset.RecoverySet{}, recoveryset.ErrInvalid
	}
	digest, err := normalized.Digest()
	if err != nil {
		return recoveryset.RecoverySet{}, err
	}
	normalized.FrontierDigest = digest
	var out recoveryset.RecoverySet
	err = r.withTx(ctx, func(tx DBTX) error {
		id, e := insertSet(ctx, tx, normalized)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if e == nil && id != "" {
			if e = insertChildren(ctx, tx, normalized); e != nil {
				return e
			}
			out = normalized
			return nil
		}
		stored, e := readExact(ctx, tx, normalized.ID)
		if e != nil {
			return e
		}
		if !stored.IdentityEqual(normalized) || !stored.FrontierEqual(normalized) {
			return recoveryset.ErrConflict
		}
		out = stored
		return nil
	})
	if err != nil {
		return recoveryset.RecoverySet{}, err
	}
	return out, nil
}

// CreateTx composes frontier creation with another control-plane mutation.
func (r *Repository) CreateTx(ctx context.Context, tx Tx, set recoveryset.RecoverySet) (recoveryset.RecoverySet, error) {
	if tx == nil {
		return recoveryset.RecoverySet{}, recoveryset.ErrInvalid
	}
	n, err := set.Normalize()
	if err != nil {
		return recoveryset.RecoverySet{}, err
	}
	if n.Status != recoveryset.StatusPrepared || n.PublishedValidationAttemptID != "" {
		return recoveryset.RecoverySet{}, recoveryset.ErrInvalid
	}
	n.FrontierDigest, err = n.Digest()
	if err != nil {
		return recoveryset.RecoverySet{}, err
	}
	id, err := insertSet(ctx, tx, n)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return recoveryset.RecoverySet{}, err
	}
	if err == nil && id != "" {
		if err := insertChildren(ctx, tx, n); err != nil {
			return recoveryset.RecoverySet{}, err
		}
		return n, nil
	}
	stored, err := readExact(ctx, tx, n.ID)
	if err != nil {
		return recoveryset.RecoverySet{}, err
	}
	if !stored.IdentityEqual(n) || !stored.FrontierEqual(n) {
		return recoveryset.RecoverySet{}, recoveryset.ErrConflict
	}
	return stored, nil
}

func insertSet(ctx context.Context, tx DBTX, s recoveryset.RecoverySet) (string, error) {
	id, _ := uuid.Parse(s.ID)
	gid, _ := uuid.Parse(s.Delivery.GenerationID)
	pid, _ := uuid.Parse(s.Delivery.PublicationID)
	sid, _ := uuid.Parse(s.Serving.SealID)
	return recoverydb.New(tx).InsertRecoverySet(ctx, recoverydb.InsertRecoverySetParams{
		SetID: pgtype.UUID{Bytes: id, Valid: true}, SchemaVersion: s.SchemaVersion,
		ExpectedObjectRoots: int32(len(s.ObjectRoots)), TargetID: s.Delivery.TargetID,
		GenerationID: pgtype.UUID{Bytes: gid, Valid: true}, PublicationID: pgtype.UUID{Bytes: pid, Valid: true}, TargetRevision: s.Delivery.TargetRevision,
		SnapshotSealID: pgtype.UUID{Bytes: sid, Valid: true}, PhysicalPoolID: s.Serving.PhysicalPoolID, TenantDomain: s.Serving.TenantDomain, Region: s.Serving.Region, EncryptionDomain: s.Serving.EncryptionDomain, ObjectNamespace: s.Serving.ObjectNamespace,
		CatalogDatabase: s.Serving.CatalogDatabase, CatalogID: s.Serving.CatalogID, CatalogUuid: s.Serving.CatalogUUID, CatalogVersion: s.Serving.CatalogVersion, DucklakeSnapshotID: s.Serving.DuckLakeSnapshotID, RelationNamespace: s.Serving.RelationNamespace, RelationManifestDigest: s.Serving.RelationManifestDigest, ClosureDigest: s.Serving.ClosureDigest, ObjectRoot: s.Serving.ObjectRoot, ObjectRootDigest: s.Serving.ObjectRootDigest, ArtifactRoot: s.Serving.ArtifactRoot, ArtifactRootDigest: s.Serving.ArtifactRootDigest, ServingArtifactID: s.Serving.ServingArtifactID, ServingArtifactDigest: s.Serving.ServingArtifactDigest, CompiledGraphDigest: s.Serving.CompiledGraphDigest, CompiledConfigDigest: s.Serving.CompiledConfigDigest, SecurityDomainFingerprint: s.Serving.SecurityDomainFingerprint, RequestDigest: s.Serving.RequestDigest, PlanDigest: s.Serving.PlanDigest, CompatibilityDigest: s.Serving.CompatibilityDigest, DuckdbVersion: s.Serving.DuckDBVersion, RuntimeVersion: s.Serving.RuntimeVersion, DucklakeExtensionVersion: s.Serving.DuckLakeExtensionVersion, DucklakeSpecVersion: s.Serving.DuckLakeSpecVersion, CatalogSchemaVersion: s.Serving.CatalogSchemaVersion,
		DuckdbRuntime: s.Compatibility.DuckDBRuntime, DucklakeExtension: s.Compatibility.DuckLakeExtension, CatalogFormat: s.Compatibility.CatalogFormat, StorageImplementation: s.Compatibility.StorageImplementation, ObjectNamingContract: s.Compatibility.ObjectNamingContract,
		FenceEpoch: s.FenceEpoch, AuditIdentity: s.AuditIdentity, Status: string(s.Status), CreatedBy: s.CreatedBy, CreatedAt: s.CreatedAt.UTC(), FrontierDigest: s.FrontierDigest,
	})
}

func insertChildren(ctx context.Context, tx DBTX, s recoveryset.RecoverySet) error {
	q := recoverydb.New(tx)
	id, _ := uuid.Parse(s.ID)
	key := pgtype.UUID{Bytes: id, Valid: true}
	for _, p := range s.CanonicalPoints() {
		if err := q.InsertRecoveryClusterPoint(ctx, recoverydb.InsertRecoveryClusterPointParams{SetID: key, DatabaseRole: string(p.DatabaseRole), ClusterIdentity: p.ClusterIdentity, DatabaseIdentity: p.DatabaseIdentity, RecoveryIdentity: p.RecoveryIdentity}); err != nil {
			return err
		}
	}
	for _, root := range s.ObjectRoots {
		if err := q.InsertRecoveryObjectRoot(ctx, recoverydb.InsertRecoveryObjectRootParams{SetID: key, RootKind: root.Kind, RootUri: root.URI, VersionID: root.VersionID, Digest: root.Digest, ProviderRecoveryFrontier: root.ProviderRecoveryFrontier}); err != nil {
			return err
		}
	}
	return nil
}

// ReadExact reads only the requested set ID and its exact child evidence.
func (r *Repository) ReadExact(ctx context.Context, setID string) (recoveryset.RecoverySet, error) {
	if r == nil || r.db == nil {
		return recoveryset.RecoverySet{}, recoveryset.ErrInvalid
	}
	if !isCanonicalUUID(setID) {
		return recoveryset.RecoverySet{}, recoveryset.ErrInvalid
	}
	return readExact(ctx, r.db, setID)
}

func readExact(ctx context.Context, db DBTX, setID string) (recoveryset.RecoverySet, error) {
	id, _ := uuid.Parse(setID)
	q := recoverydb.New(db)
	row, err := q.GetRecoverySet(ctx, pgtype.UUID{Bytes: id, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return recoveryset.RecoverySet{}, recoveryset.ErrNotFound
	}
	if err != nil {
		return recoveryset.RecoverySet{}, err
	}
	points, err := q.ListRecoveryClusterPoints(ctx, pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		return recoveryset.RecoverySet{}, err
	}
	roots, err := q.ListRecoveryObjectRoots(ctx, pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		return recoveryset.RecoverySet{}, err
	}
	if len(points) != int(row.ExpectedClusterPoints) || len(roots) != int(row.ExpectedObjectRoots) {
		return recoveryset.RecoverySet{}, fmt.Errorf("%w: recovery-set child evidence is incomplete", recoveryset.ErrInvalid)
	}
	out := recoveryset.RecoverySet{ID: row.SetID, SchemaVersion: row.SchemaVersion, Delivery: recoveryset.DeliveryPointer{TargetID: row.TargetID, GenerationID: row.GenerationID, PublicationID: row.PublicationID, TargetRevision: row.TargetRevision}, Serving: recoveryset.SnapshotSeal{SealID: row.SnapshotSealID, PhysicalPoolID: row.PhysicalPoolID, TenantDomain: row.TenantDomain, Region: row.Region, EncryptionDomain: row.EncryptionDomain, ObjectNamespace: row.ObjectNamespace, CatalogDatabase: row.CatalogDatabase, CatalogID: row.CatalogID, CatalogUUID: row.CatalogUuid, CatalogVersion: row.CatalogVersion, DuckLakeSnapshotID: row.DucklakeSnapshotID, RelationNamespace: row.RelationNamespace, RelationManifestDigest: row.RelationManifestDigest, ClosureDigest: row.ClosureDigest, ObjectRoot: row.ObjectRoot, ObjectRootDigest: row.ObjectRootDigest, ArtifactRoot: row.ArtifactRoot, ArtifactRootDigest: row.ArtifactRootDigest, ServingArtifactID: row.ServingArtifactID, ServingArtifactDigest: row.ServingArtifactDigest, CompiledGraphDigest: row.CompiledGraphDigest, CompiledConfigDigest: row.CompiledConfigDigest, SecurityDomainFingerprint: row.SecurityDomainFingerprint, RequestDigest: row.RequestDigest, PlanDigest: row.PlanDigest, CompatibilityDigest: row.CompatibilityDigest, DuckDBVersion: row.DuckdbVersion, RuntimeVersion: row.RuntimeVersion, DuckLakeExtensionVersion: row.DucklakeExtensionVersion, DuckLakeSpecVersion: row.DucklakeSpecVersion, CatalogSchemaVersion: row.CatalogSchemaVersion}, Catalog: recoveryset.CatalogCommit{CatalogID: row.CatalogID, CatalogDatabase: row.CatalogDatabase, CatalogUUID: row.CatalogUuid, CatalogVersion: row.CatalogVersion, SnapshotID: row.DucklakeSnapshotID}, Compatibility: recoveryset.CompatibilityTuple{DuckDBRuntime: row.DuckdbRuntime, DuckLakeExtension: row.DucklakeExtension, CatalogFormat: row.CatalogFormat, StorageImplementation: row.StorageImplementation, ObjectNamingContract: row.ObjectNamingContract}, FenceEpoch: row.FenceEpoch, AuditIdentity: row.AuditIdentity, Status: recoveryset.Status(row.Status), PublishedValidationAttemptID: row.PublishedValidationAttemptID, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, FrontierDigest: row.FrontierDigest}
	for _, p := range points {
		out.ClusterPoints = append(out.ClusterPoints, recoveryset.ClusterRecoveryPoint{DatabaseRole: recoveryset.DatabaseRole(p.DatabaseRole), ClusterIdentity: p.ClusterIdentity, DatabaseIdentity: p.DatabaseIdentity, RecoveryIdentity: p.RecoveryIdentity})
	}
	for _, root := range roots {
		out.ObjectRoots = append(out.ObjectRoots, recoveryset.ObjectRoot{Kind: root.RootKind, URI: root.RootUri, VersionID: root.VersionID, Digest: root.Digest, ProviderRecoveryFrontier: root.ProviderRecoveryFrontier})
	}
	if err := out.Validate(); err != nil {
		return recoveryset.RecoverySet{}, err
	}
	return out, nil
}

// Publish transitions prepared→published under the exact fence epoch. A
// replay by the same publisher returns the existing row; a stale or foreign
// fence is rejected without changing the frontier.
func (r *Repository) Publish(ctx context.Context, setID, publisher string, fenceEpoch int64, validationAttemptID string) (recoveryset.RecoverySet, error) {
	if r == nil || r.db == nil || !isCanonicalUUID(setID) || publisher == "" || fenceEpoch <= 0 || !isCanonicalUUID(validationAttemptID) {
		return recoveryset.RecoverySet{}, recoveryset.ErrInvalid
	}
	id, _ := uuid.Parse(setID)
	validationID, _ := uuid.Parse(validationAttemptID)
	row, err := recoverydb.New(r.db).PublishRecoverySet(ctx, recoverydb.PublishRecoverySetParams{SetID: pgtype.UUID{Bytes: id, Valid: true}, PublishedBy: &publisher, FenceEpoch: fenceEpoch, ValidationAttemptID: pgtype.UUID{Bytes: validationID, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return recoveryset.RecoverySet{}, recoveryset.ErrFenced
	}
	if err != nil {
		return recoveryset.RecoverySet{}, err
	}
	return r.ReadExact(ctx, row.SetID)
}

func (r *Repository) Supersede(ctx context.Context, setID string, fenceEpoch int64) error {
	if r == nil || r.db == nil || !isCanonicalUUID(setID) || fenceEpoch <= 0 {
		return recoveryset.ErrInvalid
	}
	id, _ := uuid.Parse(setID)
	n, err := recoverydb.New(r.db).SupersedeRecoverySet(ctx, recoverydb.SupersedeRecoverySetParams{SetID: pgtype.UUID{Bytes: id, Valid: true}, FenceEpoch: fenceEpoch})
	if err != nil {
		return err
	}
	if n != 1 {
		return recoveryset.ErrFenced
	}
	return nil
}

func (r *Repository) BeginValidation(ctx context.Context, attempt recoveryset.ValidationAttempt) (recoveryset.ValidationAttempt, error) {
	if r == nil || r.db == nil {
		return recoveryset.ValidationAttempt{}, recoveryset.ErrInvalid
	}
	if attempt.Status != "" && attempt.Status != recoveryset.ValidationRunning {
		return recoveryset.ValidationAttempt{}, recoveryset.ErrInvalid
	}
	attempt.Status = recoveryset.ValidationRunning
	attempt.StartedAt = attempt.StartedAt.UTC().Truncate(time.Microsecond)
	if err := attempt.Validate(); err != nil {
		return recoveryset.ValidationAttempt{}, err
	}
	ai, _ := uuid.Parse(attempt.AttemptID)
	si, _ := uuid.Parse(attempt.SetID)
	id, err := recoverydb.New(r.db).InsertValidationAttempt(ctx, recoverydb.InsertValidationAttemptParams{AttemptID: pgtype.UUID{Bytes: ai, Valid: true}, SetID: pgtype.UUID{Bytes: si, Valid: true}, OwnerID: attempt.OwnerID, FenceEpoch: attempt.FenceEpoch, AuditIdentity: attempt.AuditIdentity, StartedAt: attempt.StartedAt.UTC().Truncate(time.Microsecond)})
	if err == nil && id != "" {
		attempt.StartedAt = attempt.StartedAt.UTC().Truncate(time.Microsecond)
		return attempt, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return recoveryset.ValidationAttempt{}, err
	}
	stored, err := r.ValidationAttempt(ctx, attempt.AttemptID)
	if err != nil {
		return recoveryset.ValidationAttempt{}, err
	}
	if stored.SetID != attempt.SetID || stored.OwnerID != attempt.OwnerID || stored.FenceEpoch != attempt.FenceEpoch || stored.AuditIdentity != attempt.AuditIdentity || !stored.StartedAt.Equal(attempt.StartedAt) {
		return recoveryset.ValidationAttempt{}, recoveryset.ErrConflict
	}
	return stored, nil
}

func (r *Repository) CompleteValidation(ctx context.Context, attempt recoveryset.ValidationAttempt) error {
	if r == nil || r.db == nil {
		return recoveryset.ErrInvalid
	}
	if attempt.Status != recoveryset.ValidationPassed && attempt.Status != recoveryset.ValidationFailed {
		return recoveryset.ErrInvalid
	}
	if attempt.CompletedAt.IsZero() {
		attempt.CompletedAt = time.Now().UTC()
	}
	attempt.StartedAt = attempt.StartedAt.UTC().Truncate(time.Microsecond)
	attempt.CompletedAt = attempt.CompletedAt.UTC().Truncate(time.Microsecond)
	if err := attempt.Validate(); err != nil {
		return err
	}
	stored, err := r.ValidationAttempt(ctx, attempt.AttemptID)
	if err != nil {
		return err
	}
	if stored.Status != recoveryset.ValidationRunning || stored.SetID != attempt.SetID || stored.OwnerID != attempt.OwnerID || stored.FenceEpoch != attempt.FenceEpoch || stored.AuditIdentity != attempt.AuditIdentity || !stored.StartedAt.Equal(attempt.StartedAt) {
		return recoveryset.ErrFenced
	}
	if attempt.ResultDigest != "" {
		result, resultErr := r.ValidationResult(ctx, attempt.AttemptID)
		if resultErr != nil {
			return resultErr
		}
		if result.ResultDigest != attempt.ResultDigest {
			return recoveryset.ErrConflict
		}
	}
	ai, _ := uuid.Parse(attempt.AttemptID)
	n, err := recoverydb.New(r.db).CompleteValidationAttempt(ctx, recoverydb.CompleteValidationAttemptParams{Status: string(attempt.Status), ResultDigest: attempt.ResultDigest, Error: attempt.Error, CompletedAt: attempt.CompletedAt, AttemptID: pgtype.UUID{Bytes: ai, Valid: true}, FenceEpoch: attempt.FenceEpoch})
	if err != nil {
		return err
	}
	if n != 1 {
		return recoveryset.ErrFenced
	}
	return nil
}

func (r *Repository) RecordValidationResult(ctx context.Context, result recoveryset.ValidationResult) error {
	if r == nil || r.db == nil {
		return recoveryset.ErrInvalid
	}
	normalized, err := result.Normalize()
	if err != nil {
		return err
	}
	ai, _ := uuid.Parse(normalized.AttemptID)
	id, err := recoverydb.New(r.db).InsertValidationResult(ctx, recoverydb.InsertValidationResultParams{AttemptID: pgtype.UUID{Bytes: ai, Valid: true}, ResultDigest: normalized.ResultDigest, Evidence: normalized.Evidence, RecordedAt: normalized.RecordedAt})
	if err == nil && id != "" {
		return nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	stored, readErr := r.ValidationResult(ctx, normalized.AttemptID)
	if errors.Is(readErr, recoveryset.ErrNotFound) {
		return recoveryset.ErrFenced
	}
	if readErr != nil {
		return readErr
	}
	if stored.ResultDigest != normalized.ResultDigest || !bytes.Equal(stored.Evidence, normalized.Evidence) || !stored.RecordedAt.Equal(normalized.RecordedAt) {
		return recoveryset.ErrConflict
	}
	return nil
}

func (r *Repository) ValidationResult(ctx context.Context, attemptID string) (recoveryset.ValidationResult, error) {
	if r == nil || r.db == nil || !isCanonicalUUID(attemptID) {
		return recoveryset.ValidationResult{}, recoveryset.ErrInvalid
	}
	ai, _ := uuid.Parse(attemptID)
	row, err := recoverydb.New(r.db).GetValidationResult(ctx, pgtype.UUID{Bytes: ai, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return recoveryset.ValidationResult{}, recoveryset.ErrNotFound
	}
	if err != nil {
		return recoveryset.ValidationResult{}, err
	}
	out, err := (recoveryset.ValidationResult{AttemptID: row.AttemptID, ResultDigest: row.ResultDigest, Evidence: row.Evidence, RecordedAt: row.RecordedAt.UTC()}).Normalize()
	if err != nil {
		return recoveryset.ValidationResult{}, err
	}
	return out, nil
}

func (r *Repository) ValidationAttempt(ctx context.Context, attemptID string) (recoveryset.ValidationAttempt, error) {
	if r == nil || r.db == nil || !isCanonicalUUID(attemptID) {
		return recoveryset.ValidationAttempt{}, recoveryset.ErrInvalid
	}
	ai, _ := uuid.Parse(attemptID)
	row, err := recoverydb.New(r.db).GetValidationAttempt(ctx, pgtype.UUID{Bytes: ai, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return recoveryset.ValidationAttempt{}, recoveryset.ErrNotFound
	}
	if err != nil {
		return recoveryset.ValidationAttempt{}, err
	}
	out := recoveryset.ValidationAttempt{AttemptID: row.AttemptID, SetID: row.SetID, OwnerID: row.OwnerID, FenceEpoch: row.FenceEpoch, AuditIdentity: row.AuditIdentity, Status: recoveryset.ValidationStatus(row.Status), ResultDigest: row.ResultDigest, Error: row.Error, StartedAt: row.StartedAt.UTC()}
	if row.CompletedAt.Valid && !row.CompletedAt.Time.Equal(time.Unix(0, 0).UTC()) {
		out.CompletedAt = row.CompletedAt.Time.UTC()
	}
	if err := out.Validate(); err != nil {
		return recoveryset.ValidationAttempt{}, err
	}
	return out, nil
}

func (r *Repository) withTx(ctx context.Context, fn func(DBTX) error) error {
	b, ok := r.db.(beginner)
	if !ok {
		// A caller-owned pgx transaction is already atomic; every other DBTX
		// (including a pool-like fake without Begin) is rejected so a complete
		// frontier can never be split across independent statements.
		if _, callerOwned := r.db.(Tx); !callerOwned {
			return errors.New("recovery-set repository requires a transaction-capable PostgreSQL handle")
		}
		return fn(r.db)
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

func isCanonicalUUID(value string) bool {
	u, err := uuid.Parse(value)
	return err == nil && u.String() == value
}
