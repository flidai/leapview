package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectdb "github.com/flidai/leapview/internal/project/postgres/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	maxSourceBlobBytes        int64 = 16 << 20
	maxSourceSnapshotBytes    int64 = 64 << 20
	maxSourceSnapshotFiles          = 10_000
	maxSourceAttestationBytes       = 16 << 10
	maxObjectKeyBytes               = 2048
)

var (
	ErrSourceInvalid     = ErrInvalid
	ErrSourceConflict    = ErrConflict
	ErrSourceNotFound    = ErrNotFound
	ErrSourceExpired     = errors.New("project source synchronization plan expired")
	ErrSourceWrongOwner  = errors.New("project source synchronization plan owner mismatch")
	ErrSourceUnsolicited = errors.New("project source blob was not solicited by an open synchronization plan")
)

// SourceBlobInput is a verified object reference. No source bytes or object
// store client are accepted by this repository.
type SourceBlobInput struct {
	ProjectID             string
	StorageSecurityDomain string
	Digest                string
	SizeBytes             int64
	ObjectKey             string
	ContentType           string
	MetadataDigest        string
	PlanID                uuid.UUID
	OwnerID               string
}

type SourceBlob struct {
	ProjectID, StorageSecurityDomain string
	Digest                           string
	SizeBytes                        int64
	ObjectKey, ContentType           string
	MetadataDigest                   string
	CreatedAt                        time.Time
}

type SourceSyncPlanEntryInput struct {
	Path      string
	Digest    string
	SizeBytes int64
	Ordinal   int
}

// SourceTx is the strict caller-owned transaction boundary for source
// admission and snapshot sealing. A pool cannot satisfy it accidentally.
type SourceTx interface {
	DBTX
	Commit(context.Context) error
	Rollback(context.Context) error
}

// SyncPlanInput creates one short-lived caller-owned synchronization plan.
type SyncPlanInput struct {
	PlanID                uuid.UUID
	OperationID           uuid.UUID
	ProjectID             string
	StorageSecurityDomain string
	OwnerID               string
	CandidateKey          string
	SourceDigest          string
	ProjectFile           string
	RequestDigest         string
	ExpiresAt             time.Time
	Entries               []SourceSyncPlanEntryInput
}

type SourceSyncPlanEntry struct {
	PlanID    uuid.UUID
	Path      string
	Digest    string
	SizeBytes int64
	Ordinal   int
}

type SyncPlan struct {
	PlanID, OperationID   uuid.UUID
	ProjectID             string
	StorageSecurityDomain string
	OwnerID               string
	CandidateKey          string
	SourceDigest          string
	ProjectFile           string
	RequestDigest         string
	State                 string
	ExpiresAt, CreatedAt  time.Time
	CommittedAt           *time.Time
	Entries               []SourceSyncPlanEntry
}

type SourceSnapshotEntryInput struct {
	Path      string
	Digest    string
	SizeBytes int64
	Ordinal   int
}

type SourceSnapshotEntry struct {
	SnapshotID            uuid.UUID
	ProjectID             string
	StorageSecurityDomain string
	Path                  string
	Digest                string
	SizeBytes             int64
	Ordinal               int
}

// SourceSnapshotObjectRef identifies one source file in a sealed snapshot
// and its immutable object-store reference. Path is only the authored source
// identity; callers must use ObjectKey for object-store access.
type SourceSnapshotObjectRef struct {
	SnapshotID            uuid.UUID
	ProjectID             string
	StorageSecurityDomain string
	Path                  string
	Digest                string
	SizeBytes             int64
	Ordinal               int
	ObjectKey             string
	ContentType           string
	MetadataDigest        string
}

// CommitSnapshotInput is the complete immutable snapshot identity. Object
// keys are references only and are never opened while the transaction is held.
type CommitSnapshotInput struct {
	PlanID                   uuid.UUID
	OwnerID                  string
	SnapshotID               uuid.UUID
	ProjectID                string
	StorageSecurityDomain    string
	SourceDigest             string
	ProjectFile              string
	ProjectDigest            string
	ProjectArtifactObjectKey string
	ProjectArtifactDigest    string
	ProjectArtifactSizeBytes int64
	ManifestObjectKey        string
	ManifestObjectDigest     string
	ManifestObjectSizeBytes  int64
	CompilerVersion          string
	SchemaVersion            int64
	Entries                  []SourceSnapshotEntryInput
	Attestation              SourceAttestationInput
}

type SourceSnapshot struct {
	SnapshotID               uuid.UUID
	ProjectID                string
	StorageSecurityDomain    string
	SourceDigest             string
	ProjectFile              string
	ProjectDigest            string
	ProjectArtifactObjectKey string
	ProjectArtifactDigest    string
	ProjectArtifactSizeBytes int64
	ManifestObjectKey        string
	ManifestObjectDigest     string
	ManifestObjectSizeBytes  int64
	CompilerVersion          string
	SchemaVersion            int64
	CreatedAt                time.Time
}

type SourceAttestationInput struct {
	AttestationID     uuid.UUID
	SnapshotID        uuid.UUID
	SourceDigest      string
	AttestationDigest string
	Payload           json.RawMessage
	Revision          string
	Repository        string
	Ref               string
	ChangeID          string
}

type SourceAttestation struct {
	AttestationID, SnapshotID       uuid.UUID
	SourceDigest, AttestationDigest string
	Payload                         json.RawMessage
	Revision, Repository            string
	Ref, ChangeID                   string
	CreatedAt                       time.Time
}

func (r *Repository) CreateSyncPlanTx(ctx context.Context, tx SourceTx, input SyncPlanInput) (SyncPlan, error) {
	if tx == nil {
		return SyncPlan{}, ErrSourceInvalid
	}
	normalized, entries, err := normalizeSyncPlan(input)
	if err != nil {
		return SyncPlan{}, err
	}
	q := projectdb.New(tx)
	if err := q.InsertSourceSyncPlan(contextOrBackground(ctx), projectdb.InsertSourceSyncPlanParams{
		PlanID: dbUUID(normalized.PlanID), OperationID: dbUUID(normalized.OperationID), ProjectID: normalized.ProjectID,
		StorageSecurityDomain: normalized.StorageSecurityDomain, OwnerID: normalized.OwnerID, CandidateKey: normalized.CandidateKey,
		SourceDigest: normalized.SourceDigest, ProjectFile: normalized.ProjectFile, RequestDigest: normalized.RequestDigest,
		ExpiresAt: pgtype.Timestamptz{Time: normalized.ExpiresAt, Valid: true},
	}); err != nil {
		return SyncPlan{}, err
	}
	stored, err := q.GetSourceSyncPlan(contextOrBackground(ctx), dbUUID(normalized.PlanID))
	if errors.Is(err, pgx.ErrNoRows) {
		return SyncPlan{}, fmt.Errorf("%w: synchronization plan identity already exists", ErrSourceConflict)
	}
	if err != nil {
		return SyncPlan{}, err
	}
	if !samePlanIdentity(stored, normalized) {
		return SyncPlan{}, ErrSourceConflict
	}
	existingEntries, err := q.ListSourceSyncPlanEntries(contextOrBackground(ctx), dbUUID(normalized.PlanID))
	if err != nil {
		return SyncPlan{}, err
	}
	if len(existingEntries) != 0 && len(existingEntries) != len(entries) {
		return SyncPlan{}, ErrSourceConflict
	}
	for i, existing := range existingEntries {
		if existing.Path != entries[i].Path || existing.Digest != entries[i].Digest || existing.SizeBytes != entries[i].SizeBytes || int(existing.Ordinal) != entries[i].Ordinal {
			return SyncPlan{}, ErrSourceConflict
		}
	}
	if len(existingEntries) == 0 {
		for _, entry := range entries {
			if err := q.InsertSourceSyncPlanEntry(contextOrBackground(ctx), projectdb.InsertSourceSyncPlanEntryParams{
				PlanID: dbUUID(normalized.PlanID), Path: entry.Path, Digest: entry.Digest, SizeBytes: entry.SizeBytes, Ordinal: int32(entry.Ordinal),
			}); err != nil {
				return SyncPlan{}, err
			}
		}
	}
	return loadPlan(contextOrBackground(ctx), tx, normalized.PlanID)
}

func (r *Repository) SyncPlanForUpdateTx(ctx context.Context, tx SourceTx, planID uuid.UUID) (SyncPlan, error) {
	if tx == nil || planID == uuid.Nil {
		return SyncPlan{}, ErrSourceInvalid
	}
	return loadPlanForUpdate(contextOrBackground(ctx), tx, planID)
}

// ListMissingPlanSourceBlobDigestsTx locks an open caller-owned plan and
// returns its unique missing blob digests.
func (r *Repository) ListMissingPlanSourceBlobDigestsTx(ctx context.Context, tx SourceTx, planID uuid.UUID, ownerID string) ([]string, error) {
	if tx == nil {
		return nil, ErrSourceInvalid
	}
	ctx = contextOrBackground(ctx)
	plan, err := loadPlanForUpdate(ctx, tx, planID)
	if err != nil {
		return nil, err
	}
	if plan.OwnerID != strings.TrimSpace(ownerID) {
		return nil, ErrSourceWrongOwner
	}
	active, err := projectdb.New(tx).SourceSyncPlanActive(ctx, dbUUID(planID))
	if err != nil {
		return nil, err
	}
	if !active.Valid || !active.Bool {
		return nil, ErrSourceExpired
	}
	digests := make([]string, len(plan.Entries))
	for i, entry := range plan.Entries {
		digests[i] = entry.Digest
	}
	return listMissingSourceBlobDigests(ctx, tx, plan.ProjectID, plan.StorageSecurityDomain, digests)
}

// ListMissingSourceBlobDigestsTx checks an explicit project/security-domain
// digest set inside a caller-owned source transaction.
func (r *Repository) ListMissingSourceBlobDigestsTx(ctx context.Context, tx SourceTx, projectID, domain string, digests []string) ([]string, error) {
	if tx == nil {
		return nil, ErrSourceInvalid
	}
	return listMissingSourceBlobDigests(contextOrBackground(ctx), tx, projectID, domain, digests)
}

func listMissingSourceBlobDigests(ctx context.Context, db DBTX, projectID, domain string, digests []string) ([]string, error) {
	projectID, domain, err := normalizeProjectDomain(projectID, domain)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(digests))
	for _, value := range digests {
		value = strings.TrimSpace(value)
		if digest.ValidateSHA256Identity(value) != nil {
			return nil, ErrSourceInvalid
		}
		set[value] = struct{}{}
	}
	digests = digests[:0]
	for value := range set {
		digests = append(digests, value)
	}
	sort.Strings(digests)
	if len(digests) == 0 {
		return []string{}, nil
	}
	rows, err := projectdb.New(db).ListMissingSourceBlobDigests(ctx, projectdb.ListMissingSourceBlobDigestsParams{ProjectID: projectID, StorageSecurityDomain: domain, Column3: digests})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, value := range rows {
		out = append(out, value)
	}
	return out, nil
}

func (r *Repository) InsertSourceBlobTx(ctx context.Context, tx SourceTx, input SourceBlobInput) (SourceBlob, error) {
	if tx == nil {
		return SourceBlob{}, ErrSourceInvalid
	}
	n, err := normalizeBlob(input)
	if err != nil {
		return SourceBlob{}, err
	}
	ctx = contextOrBackground(ctx)
	plan, err := loadPlanForUpdate(ctx, tx, n.PlanID)
	if err != nil {
		return SourceBlob{}, err
	}
	if plan.OwnerID != n.OwnerID {
		return SourceBlob{}, ErrSourceWrongOwner
	}
	active, err := projectdb.New(tx).SourceSyncPlanActive(ctx, dbUUID(plan.PlanID))
	if err != nil {
		return SourceBlob{}, err
	}
	if !active.Valid || !active.Bool {
		return SourceBlob{}, ErrSourceExpired
	}
	expected := false
	for _, entry := range plan.Entries {
		if entry.Digest == n.Digest {
			if entry.SizeBytes != n.SizeBytes {
				return SourceBlob{}, ErrSourceConflict
			}
			expected = true
			break
		}
	}
	if !expected {
		return SourceBlob{}, ErrSourceUnsolicited
	}
	q := projectdb.New(tx)
	if err := q.InsertSourceBlob(ctx, projectdb.InsertSourceBlobParams{ProjectID: n.ProjectID, StorageSecurityDomain: n.StorageSecurityDomain, Digest: n.Digest, SizeBytes: n.SizeBytes, ObjectKey: n.ObjectKey, ContentType: n.ContentType, MetadataDigest: n.MetadataDigest, PlanID: dbUUID(n.PlanID), OwnerID: n.OwnerID}); err != nil {
		return SourceBlob{}, err
	}
	row, err := q.GetSourceBlob(ctx, projectdb.GetSourceBlobParams{ProjectID: n.ProjectID, StorageSecurityDomain: n.StorageSecurityDomain, Digest: n.Digest})
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceBlob{}, ErrSourceUnsolicited
	}
	if err != nil {
		return SourceBlob{}, err
	}
	stored := blobFromModel(row)
	if stored.ProjectID != n.ProjectID || stored.StorageSecurityDomain != n.StorageSecurityDomain || stored.Digest != n.Digest || stored.SizeBytes != n.SizeBytes || stored.ObjectKey != n.ObjectKey || stored.ContentType != n.ContentType || stored.MetadataDigest != n.MetadataDigest {
		return SourceBlob{}, ErrSourceConflict
	}
	return stored, nil
}

// SourceBlob loads one exact source object reference by project, storage
// security domain, and canonical content digest.
func (r *Repository) SourceBlob(ctx context.Context, projectID, storageSecurityDomain, blobDigest string) (SourceBlob, error) {
	if r == nil || r.db == nil {
		return SourceBlob{}, ErrSourceInvalid
	}
	projectID, storageSecurityDomain, err := normalizeSourceReadIdentity(projectID, storageSecurityDomain)
	if err != nil {
		return SourceBlob{}, err
	}
	if digest.ValidateSHA256Identity(blobDigest) != nil {
		return SourceBlob{}, ErrSourceInvalid
	}
	row, err := projectdb.New(r.db).GetSourceBlob(contextOrBackground(ctx), projectdb.GetSourceBlobParams{
		ProjectID: projectID, StorageSecurityDomain: storageSecurityDomain, Digest: blobDigest,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceBlob{}, ErrSourceNotFound
	}
	if err != nil {
		return SourceBlob{}, err
	}
	stored := blobFromModel(row)
	if stored.ProjectID != projectID || stored.StorageSecurityDomain != storageSecurityDomain || stored.Digest != blobDigest {
		return SourceBlob{}, ErrSourceConflict
	}
	return stored, nil
}

func (r *Repository) CommitSnapshotTx(ctx context.Context, tx SourceTx, input CommitSnapshotInput) (SourceSnapshot, error) {
	n, entries, attestation, err := normalizeCommit(input)
	if err != nil || tx == nil {
		if err == nil {
			err = ErrSourceInvalid
		}
		return SourceSnapshot{}, err
	}
	ctx = contextOrBackground(ctx)
	plan, err := loadPlanForUpdate(ctx, tx, n.PlanID)
	if err != nil {
		return SourceSnapshot{}, err
	}
	if plan.OwnerID != n.OwnerID {
		return SourceSnapshot{}, ErrSourceWrongOwner
	}
	if plan.ProjectID != n.ProjectID || plan.StorageSecurityDomain != n.StorageSecurityDomain || plan.SourceDigest != n.SourceDigest || plan.ProjectFile != n.ProjectFile {
		return SourceSnapshot{}, ErrSourceConflict
	}
	if plan.State != "open" && plan.State != "committed" {
		return SourceSnapshot{}, ErrSourceConflict
	}
	if plan.State == "open" {
		active, activeErr := projectdb.New(tx).SourceSyncPlanActive(ctx, dbUUID(plan.PlanID))
		if activeErr != nil {
			return SourceSnapshot{}, activeErr
		}
		if !active.Valid || !active.Bool {
			return SourceSnapshot{}, ErrSourceExpired
		}
	}
	if len(entries) == 0 {
		entries = make([]SourceSnapshotEntryInput, len(plan.Entries))
		for i, e := range plan.Entries {
			entries[i] = SourceSnapshotEntryInput{Path: e.Path, Digest: e.Digest, SizeBytes: e.SizeBytes, Ordinal: i}
		}
	}
	if !entriesMatchPlan(entries, plan.Entries) {
		return SourceSnapshot{}, ErrSourceConflict
	}
	if actual := sourceDigest(n.ProjectID, n.ProjectFile, entries); actual != n.SourceDigest {
		return SourceSnapshot{}, fmt.Errorf("%w: source digest does not match canonical entries", ErrSourceInvalid)
	}
	for _, entry := range entries {
		blob, err := projectdb.New(tx).GetSourceBlob(ctx, projectdb.GetSourceBlobParams{ProjectID: n.ProjectID, StorageSecurityDomain: n.StorageSecurityDomain, Digest: entry.Digest})
		if errors.Is(err, pgx.ErrNoRows) {
			return SourceSnapshot{}, fmt.Errorf("%w: missing source blob %s", ErrSourceConflict, entry.Digest)
		}
		if err != nil {
			return SourceSnapshot{}, err
		}
		if blob.SizeBytes != entry.SizeBytes {
			return SourceSnapshot{}, fmt.Errorf("%w: source blob size differs for %s", ErrSourceConflict, entry.Path)
		}
	}
	q := projectdb.New(tx)
	row, insertErr := q.InsertSourceSnapshot(ctx, projectdb.InsertSourceSnapshotParams{SnapshotID: dbUUID(n.SnapshotID), ProjectID: n.ProjectID, StorageSecurityDomain: n.StorageSecurityDomain, SourceDigest: n.SourceDigest, ProjectFile: n.ProjectFile, ProjectDigest: n.ProjectDigest, ProjectArtifactObjectKey: n.ProjectArtifactObjectKey, ProjectArtifactDigest: n.ProjectArtifactDigest, ProjectArtifactSizeBytes: n.ProjectArtifactSizeBytes, ManifestObjectKey: n.ManifestObjectKey, ManifestObjectDigest: n.ManifestObjectDigest, ManifestObjectSizeBytes: n.ManifestObjectSizeBytes, CompilerVersion: n.CompilerVersion, SchemaVersion: n.SchemaVersion})
	var snapshot SourceSnapshot
	inserted := insertErr == nil
	if errors.Is(insertErr, pgx.ErrNoRows) {
		existing, loadErr := q.GetSourceSnapshot(ctx, projectdb.GetSourceSnapshotParams{ProjectID: n.ProjectID, StorageSecurityDomain: n.StorageSecurityDomain, SourceDigest: n.SourceDigest})
		if loadErr != nil {
			return SourceSnapshot{}, loadErr
		}
		snapshot = snapshotFromGet(existing)
		if snapshot.SnapshotID != n.SnapshotID || !sameSnapshot(snapshot, n) {
			return SourceSnapshot{}, ErrSourceConflict
		}
	} else if insertErr != nil {
		return SourceSnapshot{}, insertErr
	} else {
		snapshot = snapshotFromInsert(row)
	}
	if inserted {
		for _, entry := range entries {
			if err := q.InsertSourceSnapshotEntry(ctx, projectdb.InsertSourceSnapshotEntryParams{SnapshotID: dbUUID(snapshot.SnapshotID), ProjectID: n.ProjectID, StorageSecurityDomain: n.StorageSecurityDomain, Path: entry.Path, Digest: entry.Digest, SizeBytes: entry.SizeBytes, Ordinal: int32(entry.Ordinal)}); err != nil {
				return SourceSnapshot{}, err
			}
		}
	}
	if err := verifySnapshotEntries(ctx, tx, snapshot.SnapshotID, entries); err != nil {
		return SourceSnapshot{}, err
	}
	if err := insertOrReplayAttestation(ctx, tx, snapshot, attestation, inserted); err != nil {
		return SourceSnapshot{}, err
	}
	if inserted {
		changed, err := q.TransitionSourceSnapshotSealed(ctx, dbUUID(snapshot.SnapshotID))
		if err != nil {
			return SourceSnapshot{}, err
		}
		if changed != 1 {
			return SourceSnapshot{}, ErrSourceConflict
		}
	}
	if plan.State == "open" {
		changed, err := q.TransitionSourceSyncPlanCommitted(ctx, projectdb.TransitionSourceSyncPlanCommittedParams{PlanID: dbUUID(plan.PlanID), OwnerID: plan.OwnerID})
		if err != nil {
			return SourceSnapshot{}, err
		}
		if changed != 1 {
			return SourceSnapshot{}, ErrSourceConflict
		}
	}
	return snapshot, nil
}

func (r *Repository) Snapshot(ctx context.Context, projectID, domain, sourceDigest string) (SourceSnapshot, error) {
	if r == nil || r.db == nil {
		return SourceSnapshot{}, ErrSourceInvalid
	}
	projectID, domain, err := normalizeProjectDomain(projectID, domain)
	if err != nil {
		return SourceSnapshot{}, err
	}
	if digest.ValidateSHA256Identity(sourceDigest) != nil {
		return SourceSnapshot{}, ErrSourceInvalid
	}
	row, err := projectdb.New(r.db).GetSourceSnapshot(contextOrBackground(ctx), projectdb.GetSourceSnapshotParams{ProjectID: projectID, StorageSecurityDomain: domain, SourceDigest: sourceDigest})
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceSnapshot{}, ErrSourceNotFound
	}
	if err != nil {
		return SourceSnapshot{}, err
	}
	return snapshotFromGet(row), nil
}

func (r *Repository) SnapshotEntries(ctx context.Context, snapshotID uuid.UUID) ([]SourceSnapshotEntry, error) {
	if r == nil || r.db == nil || snapshotID == uuid.Nil {
		return nil, ErrSourceInvalid
	}
	rows, err := projectdb.New(r.db).ListSourceSnapshotEntries(contextOrBackground(ctx), dbUUID(snapshotID))
	if err != nil {
		return nil, err
	}
	out := make([]SourceSnapshotEntry, len(rows))
	for i, row := range rows {
		out[i] = SourceSnapshotEntry{SnapshotID: uuidFromDB(row.SnapshotID), ProjectID: row.ProjectID, StorageSecurityDomain: row.StorageSecurityDomain, Path: row.Path, Digest: row.Digest, SizeBytes: row.SizeBytes, Ordinal: int(row.Ordinal)}
	}
	return out, nil
}

// SnapshotSourceObjectRefs returns all source object references for one sealed
// snapshot in canonical ordinal order. The SQL leaf performs the complete
// snapshot/entry/blob join, so this method never issues per-entry reads.
func (r *Repository) SnapshotSourceObjectRefs(ctx context.Context, projectID, storageSecurityDomain, sourceDigest string) ([]SourceSnapshotObjectRef, error) {
	if r == nil || r.db == nil {
		return nil, ErrSourceInvalid
	}
	projectID, storageSecurityDomain, err := normalizeSourceReadIdentity(projectID, storageSecurityDomain)
	if err != nil {
		return nil, err
	}
	if digest.ValidateSHA256Identity(sourceDigest) != nil {
		return nil, ErrSourceInvalid
	}
	rows, err := projectdb.New(r.db).ListSealedSourceSnapshotObjectRefs(contextOrBackground(ctx), projectdb.ListSealedSourceSnapshotObjectRefsParams{
		ProjectID: projectID, StorageSecurityDomain: storageSecurityDomain, SourceDigest: sourceDigest,
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrSourceNotFound
	}
	out := make([]SourceSnapshotObjectRef, len(rows))
	var snapshotID uuid.UUID
	for i, row := range rows {
		rowSnapshotID := uuidFromDB(row.SnapshotID)
		if !row.SnapshotID.Valid || rowSnapshotID == uuid.Nil || row.ProjectID != projectID || row.StorageSecurityDomain != storageSecurityDomain || !canonicalSourcePath(row.Path) || digest.ValidateSHA256Identity(row.Digest) != nil || row.SizeBytes < 0 || row.SizeBytes > maxSourceBlobBytes || row.Ordinal != int32(i) || !validObjectKey(row.ObjectKey) || row.ContentType == "" || digest.ValidateSHA256Identity(row.MetadataDigest) != nil {
			return nil, ErrSourceConflict
		}
		if i == 0 {
			snapshotID = rowSnapshotID
		} else if rowSnapshotID != snapshotID {
			return nil, ErrSourceConflict
		}
		if row.Digest != row.BlobDigest || row.SizeBytes != row.BlobSizeBytes {
			return nil, ErrSourceConflict
		}
		out[i] = SourceSnapshotObjectRef{SnapshotID: rowSnapshotID, ProjectID: row.ProjectID, StorageSecurityDomain: row.StorageSecurityDomain, Path: row.Path, Digest: row.Digest, SizeBytes: row.SizeBytes, Ordinal: int(row.Ordinal), ObjectKey: row.ObjectKey, ContentType: row.ContentType, MetadataDigest: row.MetadataDigest}
	}
	return out, nil
}

func (r *Repository) SnapshotAttestation(ctx context.Context, snapshotID uuid.UUID, attestationDigest string) (SourceAttestation, error) {
	if r == nil || r.db == nil || snapshotID == uuid.Nil || digest.ValidateSHA256Identity(attestationDigest) != nil {
		return SourceAttestation{}, ErrSourceInvalid
	}
	row, err := projectdb.New(r.db).GetSourceAttestation(contextOrBackground(ctx), projectdb.GetSourceAttestationParams{SnapshotID: dbUUID(snapshotID), AttestationDigest: attestationDigest})
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceAttestation{}, ErrSourceNotFound
	}
	if err != nil {
		return SourceAttestation{}, err
	}
	return attestationFromModel(row)
}

func insertOrReplayAttestation(ctx context.Context, tx SourceTx, snapshot SourceSnapshot, input SourceAttestationInput, insert bool) error {
	if input.AttestationID == uuid.Nil || input.SnapshotID != uuid.Nil && input.SnapshotID != snapshot.SnapshotID {
		return ErrSourceInvalid
	}
	input.SnapshotID = snapshot.SnapshotID
	canonical, err := canonicalJSON(input.Payload)
	if err != nil {
		return err
	}
	if digest.ValidateSHA256Identity(input.SourceDigest) != nil {
		return ErrSourceInvalid
	}
	if input.SourceDigest != snapshot.SourceDigest {
		return ErrSourceConflict
	}
	if digest.ValidateSHA256Identity(input.AttestationDigest) != nil || sha256Identity(canonical) != input.AttestationDigest {
		return ErrSourceInvalid
	}
	input.Revision = strings.TrimSpace(input.Revision)
	input.Repository = strings.TrimSpace(input.Repository)
	input.Ref = strings.TrimSpace(input.Ref)
	input.ChangeID = strings.TrimSpace(input.ChangeID)
	q := projectdb.New(tx)
	if insert {
		if err := q.InsertSourceAttestation(ctx, projectdb.InsertSourceAttestationParams{AttestationID: dbUUID(input.AttestationID), SnapshotID: dbUUID(snapshot.SnapshotID), SourceDigest: input.SourceDigest, AttestationDigest: input.AttestationDigest, Column5: canonical, Revision: input.Revision, Repository: input.Repository, Ref: input.Ref, ChangeID: input.ChangeID}); err != nil {
			return err
		}
	}
	row, err := q.GetSourceAttestation(ctx, projectdb.GetSourceAttestationParams{SnapshotID: dbUUID(snapshot.SnapshotID), AttestationDigest: input.AttestationDigest})
	if err != nil {
		return err
	}
	stored, err := attestationFromModel(row)
	if err != nil {
		return err
	}
	if stored.AttestationID != input.AttestationID || string(stored.Payload) != string(canonical) || stored.SourceDigest != input.SourceDigest || stored.Revision != input.Revision || stored.Repository != input.Repository || stored.Ref != input.Ref || stored.ChangeID != input.ChangeID {
		return ErrSourceConflict
	}
	return nil
}

func verifySnapshotEntries(ctx context.Context, db DBTX, snapshotID uuid.UUID, expected []SourceSnapshotEntryInput) error {
	rows, err := projectdb.New(db).ListSourceSnapshotEntries(ctx, dbUUID(snapshotID))
	if err != nil {
		return err
	}
	if len(rows) != len(expected) {
		return ErrSourceConflict
	}
	for i, row := range rows {
		if row.Path != expected[i].Path || row.Digest != expected[i].Digest || row.SizeBytes != expected[i].SizeBytes || int(row.Ordinal) != expected[i].Ordinal {
			return ErrSourceConflict
		}
	}
	return nil
}

func loadPlan(ctx context.Context, db DBTX, id uuid.UUID) (SyncPlan, error) {
	row, err := projectdb.New(db).GetSourceSyncPlan(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return SyncPlan{}, ErrSourceNotFound
	}
	if err != nil {
		return SyncPlan{}, err
	}
	return planFromModel(ctx, db, row)
}
func loadPlanForUpdate(ctx context.Context, db DBTX, id uuid.UUID) (SyncPlan, error) {
	row, err := projectdb.New(db).GetSourceSyncPlanForUpdate(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return SyncPlan{}, ErrSourceNotFound
	}
	if err != nil {
		return SyncPlan{}, err
	}
	return planFromModel(ctx, db, row)
}
func planFromModel(ctx context.Context, db DBTX, row projectdb.ProjectSourceSyncPlan) (SyncPlan, error) {
	p := SyncPlan{PlanID: uuidFromDB(row.PlanID), OperationID: uuidFromDB(row.OperationID), ProjectID: row.ProjectID, StorageSecurityDomain: row.StorageSecurityDomain, OwnerID: row.OwnerID, CandidateKey: row.CandidateKey, SourceDigest: row.SourceDigest, ProjectFile: row.ProjectFile, RequestDigest: row.RequestDigest, State: row.State, ExpiresAt: row.ExpiresAt.Time, CreatedAt: row.CreatedAt.Time}
	if row.CommittedAt.Valid {
		t := row.CommittedAt.Time
		p.CommittedAt = &t
	}
	entries, err := projectdb.New(db).ListSourceSyncPlanEntries(ctx, row.PlanID)
	if err != nil {
		return SyncPlan{}, err
	}
	p.Entries = make([]SourceSyncPlanEntry, len(entries))
	for i, e := range entries {
		p.Entries[i] = SourceSyncPlanEntry{PlanID: uuidFromDB(e.PlanID), Path: e.Path, Digest: e.Digest, SizeBytes: e.SizeBytes, Ordinal: int(e.Ordinal)}
	}
	return p, nil
}

func normalizeSyncPlan(input SyncPlanInput) (SyncPlanInput, []SourceSyncPlanEntryInput, error) {
	n := input
	if n.PlanID == uuid.Nil || n.OperationID == uuid.Nil {
		return SyncPlanInput{}, nil, ErrSourceInvalid
	}
	var err error
	n.ProjectID, n.StorageSecurityDomain, err = normalizeProjectDomain(n.ProjectID, n.StorageSecurityDomain)
	if err != nil {
		return SyncPlanInput{}, nil, err
	}
	n.OwnerID = strings.TrimSpace(n.OwnerID)
	n.CandidateKey = strings.TrimSpace(n.CandidateKey)
	n.ProjectFile = strings.TrimSpace(n.ProjectFile)
	if n.OwnerID == "" || n.CandidateKey == "" || !canonicalSourcePath(n.ProjectFile) {
		return SyncPlanInput{}, nil, ErrSourceInvalid
	}
	if digest.ValidateSHA256Identity(n.SourceDigest) != nil || digest.ValidateSHA256Identity(n.RequestDigest) != nil {
		return SyncPlanInput{}, nil, ErrSourceInvalid
	}
	n.ExpiresAt = n.ExpiresAt.UTC()
	if n.ExpiresAt.IsZero() {
		return SyncPlanInput{}, nil, ErrSourceInvalid
	}
	entries := n.Entries
	entries, err = normalizeEntries(entries, maxSourceSnapshotFiles, maxSourceSnapshotBytes)
	if err != nil {
		return SyncPlanInput{}, nil, err
	}
	if len(entries) == 0 || sourceDigest(n.ProjectID, n.ProjectFile, snapshotEntries(entries)) != n.SourceDigest {
		return SyncPlanInput{}, nil, ErrSourceInvalid
	}
	return n, entries, nil
}

func snapshotEntries(entries []SourceSyncPlanEntryInput) []SourceSnapshotEntryInput {
	out := make([]SourceSnapshotEntryInput, len(entries))
	for i, entry := range entries {
		out[i] = SourceSnapshotEntryInput{Path: entry.Path, Digest: entry.Digest, SizeBytes: entry.SizeBytes, Ordinal: entry.Ordinal}
	}
	return out
}
func normalizeEntries(in []SourceSyncPlanEntryInput, maxFiles int, maxBytes int64) ([]SourceSyncPlanEntryInput, error) {
	if len(in) > maxFiles {
		return nil, ErrSourceInvalid
	}
	out := append([]SourceSyncPlanEntryInput(nil), in...)
	for i := range out {
		out[i].Path = strings.TrimSpace(out[i].Path)
		out[i].Digest = strings.TrimSpace(out[i].Digest)
		if !canonicalSourcePath(out[i].Path) || digest.ValidateSHA256Identity(out[i].Digest) != nil || out[i].SizeBytes < 0 || out[i].SizeBytes > maxSourceBlobBytes {
			return nil, ErrSourceInvalid
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	var total int64
	for i := range out {
		if i > 0 && out[i].Path == out[i-1].Path {
			return nil, ErrSourceInvalid
		}
		if out[i].Ordinal != 0 && out[i].Ordinal != i {
			return nil, ErrSourceInvalid
		}
		out[i].Ordinal = i
		if total > maxBytes-out[i].SizeBytes {
			return nil, ErrSourceInvalid
		}
		total += out[i].SizeBytes
	}
	return out, nil
}
func normalizeBlob(in SourceBlobInput) (SourceBlobInput, error) {
	n := in
	var err error
	n.ProjectID, n.StorageSecurityDomain, err = normalizeProjectDomain(n.ProjectID, n.StorageSecurityDomain)
	if err != nil {
		return SourceBlobInput{}, err
	}
	n.Digest = strings.TrimSpace(n.Digest)
	n.ObjectKey = strings.TrimSpace(n.ObjectKey)
	n.ContentType = strings.TrimSpace(n.ContentType)
	n.MetadataDigest = strings.TrimSpace(n.MetadataDigest)
	n.OwnerID = strings.TrimSpace(n.OwnerID)
	if n.PlanID == uuid.Nil || n.OwnerID == "" || digest.ValidateSHA256Identity(n.Digest) != nil || digest.ValidateSHA256Identity(n.MetadataDigest) != nil || n.SizeBytes < 0 || n.SizeBytes > maxSourceBlobBytes || !validObjectKey(n.ObjectKey) || n.ContentType == "" || n.ProjectID == "" {
		return SourceBlobInput{}, ErrSourceInvalid
	}
	return n, nil
}
func normalizeCommit(in CommitSnapshotInput) (CommitSnapshotInput, []SourceSnapshotEntryInput, SourceAttestationInput, error) {
	n := in
	if n.PlanID == uuid.Nil || n.SnapshotID == uuid.Nil || n.OwnerID == "" {
		return CommitSnapshotInput{}, nil, SourceAttestationInput{}, ErrSourceInvalid
	}
	var err error
	n.ProjectID, n.StorageSecurityDomain, err = normalizeProjectDomain(n.ProjectID, n.StorageSecurityDomain)
	if err != nil {
		return CommitSnapshotInput{}, nil, SourceAttestationInput{}, err
	}
	n.OwnerID = strings.TrimSpace(n.OwnerID)
	n.ProjectFile = strings.TrimSpace(n.ProjectFile)
	if !canonicalSourcePath(n.ProjectFile) || digest.ValidateSHA256Identity(n.SourceDigest) != nil || digest.ValidateSHA256Identity(n.ProjectDigest) != nil || digest.ValidateSHA256Identity(n.ProjectArtifactDigest) != nil || digest.ValidateSHA256Identity(n.ManifestObjectDigest) != nil || n.ProjectArtifactSizeBytes < 0 || n.ManifestObjectSizeBytes < 0 || n.ProjectArtifactSizeBytes > maxSourceSnapshotBytes || n.ManifestObjectSizeBytes > maxSourceSnapshotBytes || !validObjectKey(n.ProjectArtifactObjectKey) || !validObjectKey(n.ManifestObjectKey) || strings.TrimSpace(n.CompilerVersion) == "" || n.SchemaVersion <= 0 {
		return CommitSnapshotInput{}, nil, SourceAttestationInput{}, ErrSourceInvalid
	}
	entries := n.Entries
	syncEntries := make([]SourceSyncPlanEntryInput, len(entries))
	for i, entry := range entries {
		syncEntries[i] = SourceSyncPlanEntryInput{Path: entry.Path, Digest: entry.Digest, SizeBytes: entry.SizeBytes, Ordinal: entry.Ordinal}
	}
	normalizedSync, err := normalizeEntries(syncEntries, maxSourceSnapshotFiles, maxSourceSnapshotBytes)
	if err != nil {
		return CommitSnapshotInput{}, nil, SourceAttestationInput{}, err
	}
	e := make([]SourceSnapshotEntryInput, len(normalizedSync))
	for i, entry := range normalizedSync {
		e[i] = SourceSnapshotEntryInput{Path: entry.Path, Digest: entry.Digest, SizeBytes: entry.SizeBytes, Ordinal: entry.Ordinal}
	}
	a := n.Attestation
	return n, e, a, nil
}
func entriesMatchPlan(entries []SourceSnapshotEntryInput, plan []SourceSyncPlanEntry) bool {
	if len(entries) != len(plan) {
		return false
	}
	for i := range entries {
		if entries[i].Path != plan[i].Path || entries[i].Digest != plan[i].Digest || entries[i].SizeBytes != plan[i].SizeBytes || entries[i].Ordinal != plan[i].Ordinal {
			return false
		}
	}
	return true
}
func normalizeProjectDomain(projectID, domain string) (string, string, error) {
	projectID = strings.TrimSpace(projectID)
	domain = strings.TrimSpace(domain)
	if projectID == "" || domain == "" || len(projectID) > 255 || len(domain) > 255 {
		return "", "", ErrSourceInvalid
	}
	return projectID, domain, nil
}

func normalizeSourceReadIdentity(projectID, domain string) (string, string, error) {
	if projectID == "" || strings.TrimSpace(projectID) != projectID || len(projectID) > maxProjectIDBytes {
		return "", "", ErrSourceInvalid
	}
	if _, err := projectgraph.NewResourceID(projectID); err != nil {
		return "", "", ErrSourceInvalid
	}
	if domain == "" || strings.TrimSpace(domain) != domain || len(domain) > 255 || !utf8.ValidString(domain) || strings.IndexFunc(domain, unicode.IsControl) >= 0 {
		return "", "", ErrSourceInvalid
	}
	return projectID, domain, nil
}
func canonicalSourcePath(v string) bool {
	return v != "" && !path.IsAbs(v) && path.Clean(v) == v && v != ".." && !strings.HasPrefix(v, "../") && !strings.Contains(v, `\`) && len(v) <= 1024
}
func validObjectKey(v string) bool {
	return v != "" && len(v) <= maxObjectKeyBytes && !strings.HasPrefix(v, "/") && !strings.Contains(v, `\`) && path.Clean(v) == v && v != ".." && !strings.HasPrefix(v, "../")
}
func sourceDigest(projectID, projectFile string, entries []SourceSnapshotEntryInput) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s:%d:%s:", len(projectID), projectID, len(projectFile), projectFile)
	for _, e := range entries {
		fmt.Fprintf(h, "%d:%s:%d:%s:%d:", len(e.Path), e.Path, len(e.Digest), e.Digest, e.SizeBytes)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// CanonicalSourceDigest returns the exact source identity enforced by snapshot
// admission. Application coordinators use this helper so the pre-object-write
// digest cannot drift from the repository's final transaction check.
func CanonicalSourceDigest(projectID, projectFile string, entries []SourceSnapshotEntryInput) string {
	return sourceDigest(projectID, projectFile, entries)
}
func sha256Identity(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func canonicalJSON(raw []byte) ([]byte, error) {
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return nil, ErrSourceInvalid
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, ErrSourceInvalid
	}
	b, err := json.Marshal(value)
	if err != nil || len(b) > maxSourceAttestationBytes {
		return nil, ErrSourceInvalid
	}
	return b, nil
}
func dbUUID(id uuid.UUID) pgtype.UUID     { return pgtype.UUID{Bytes: id, Valid: true} }
func uuidFromDB(id pgtype.UUID) uuid.UUID { return uuid.UUID(id.Bytes) }
func blobFromModel(row projectdb.ProjectSourceBlob) SourceBlob {
	return SourceBlob{ProjectID: row.ProjectID, StorageSecurityDomain: row.StorageSecurityDomain, Digest: row.Digest, SizeBytes: row.SizeBytes, ObjectKey: row.ObjectKey, ContentType: row.ContentType, MetadataDigest: row.MetadataDigest, CreatedAt: row.CreatedAt.Time}
}
func sameBlob(a SourceBlob, b SourceBlob) bool {
	return a.ProjectID == b.ProjectID && a.StorageSecurityDomain == b.StorageSecurityDomain && a.Digest == b.Digest && a.SizeBytes == b.SizeBytes && a.ObjectKey == b.ObjectKey && a.ContentType == b.ContentType && a.MetadataDigest == b.MetadataDigest
}
func snapshotFromGet(row projectdb.GetSourceSnapshotRow) SourceSnapshot {
	return sourceSnapshot(row.SnapshotID, row.ProjectID, row.StorageSecurityDomain, row.SourceDigest, row.ProjectFile, row.ProjectDigest, row.ProjectArtifactObjectKey, row.ProjectArtifactDigest, row.ProjectArtifactSizeBytes, row.ManifestObjectKey, row.ManifestObjectDigest, row.ManifestObjectSizeBytes, row.CompilerVersion, row.SchemaVersion, row.CreatedAt)
}
func snapshotFromInsert(row projectdb.InsertSourceSnapshotRow) SourceSnapshot {
	return sourceSnapshot(row.SnapshotID, row.ProjectID, row.StorageSecurityDomain, row.SourceDigest, row.ProjectFile, row.ProjectDigest, row.ProjectArtifactObjectKey, row.ProjectArtifactDigest, row.ProjectArtifactSizeBytes, row.ManifestObjectKey, row.ManifestObjectDigest, row.ManifestObjectSizeBytes, row.CompilerVersion, row.SchemaVersion, row.CreatedAt)
}
func sourceSnapshot(snapshotID pgtype.UUID, projectID, domain, sourceDigest, projectFile, projectDigest, artifactKey, artifactDigest string, artifactSize int64, manifestKey, manifestDigest string, manifestSize int64, compilerVersion string, schemaVersion int64, createdAt pgtype.Timestamptz) SourceSnapshot {
	return SourceSnapshot{SnapshotID: uuidFromDB(snapshotID), ProjectID: projectID, StorageSecurityDomain: domain, SourceDigest: sourceDigest, ProjectFile: projectFile, ProjectDigest: projectDigest, ProjectArtifactObjectKey: artifactKey, ProjectArtifactDigest: artifactDigest, ProjectArtifactSizeBytes: artifactSize, ManifestObjectKey: manifestKey, ManifestObjectDigest: manifestDigest, ManifestObjectSizeBytes: manifestSize, CompilerVersion: compilerVersion, SchemaVersion: schemaVersion, CreatedAt: createdAt.Time}
}
func sameSnapshot(a SourceSnapshot, b CommitSnapshotInput) bool {
	return a.ProjectID == b.ProjectID && a.StorageSecurityDomain == b.StorageSecurityDomain && a.SourceDigest == b.SourceDigest && a.ProjectFile == b.ProjectFile && a.ProjectDigest == b.ProjectDigest && a.ProjectArtifactObjectKey == b.ProjectArtifactObjectKey && a.ProjectArtifactDigest == b.ProjectArtifactDigest && a.ProjectArtifactSizeBytes == b.ProjectArtifactSizeBytes && a.ManifestObjectKey == b.ManifestObjectKey && a.ManifestObjectDigest == b.ManifestObjectDigest && a.ManifestObjectSizeBytes == b.ManifestObjectSizeBytes && a.CompilerVersion == b.CompilerVersion && a.SchemaVersion == b.SchemaVersion
}
func attestationFromModel(row projectdb.GetSourceAttestationRow) (SourceAttestation, error) {
	payload, err := canonicalJSON([]byte(row.Payload))
	if err != nil {
		return SourceAttestation{}, err
	}
	return SourceAttestation{AttestationID: uuidFromDB(row.AttestationID), SnapshotID: uuidFromDB(row.SnapshotID), SourceDigest: row.SourceDigest, AttestationDigest: row.AttestationDigest, Payload: payload, Revision: row.Revision, Repository: row.Repository, Ref: row.Ref, ChangeID: row.ChangeID, CreatedAt: row.CreatedAt.Time}, nil
}
func samePlanIdentity(row projectdb.ProjectSourceSyncPlan, in SyncPlanInput) bool {
	return uuidFromDB(row.PlanID) == in.PlanID && uuidFromDB(row.OperationID) == in.OperationID && row.ProjectID == in.ProjectID && row.StorageSecurityDomain == in.StorageSecurityDomain && row.OwnerID == in.OwnerID && row.CandidateKey == in.CandidateKey && row.SourceDigest == in.SourceDigest && row.ProjectFile == in.ProjectFile && row.RequestDigest == in.RequestDigest
}
