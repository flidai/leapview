package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	saveddb "github.com/flidai/leapview/internal/analytics/internal/db"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const sqliteTimeLayout = time.RFC3339Nano

const savedExplorationAuditSource = "analytics.exploration.saved"

const savedExplorationAuditMetadataSchemaVersion = 1

// savedExplorationMutationAuditMetadata is deliberately a fixed struct rather
// than a caller-provided map. Its fields are bounded by MutationEvidence and
// Revision validation, and it contains no authored ExplorationSpec bytes.
type savedExplorationMutationAuditMetadata struct {
	SchemaVersion int    `json:"schemaVersion"`
	Retention     string `json:"retention"`
	PayloadSchema string `json:"payloadSchema"`
	Payload       struct {
		MutationEvidenceVersion uint32               `json:"mutationEvidenceVersion"`
		ActorID                 string               `json:"actorId"`
		Action                  saved.MutationAction `json:"action"`
		IdempotencyKey          string               `json:"idempotencyKey"`
		Fingerprint             string               `json:"fingerprint"`
		RequestID               string               `json:"requestId"`
		CorrelationID           string               `json:"correlationId"`
		AdminOverride           bool                 `json:"adminOverride"`
		AdminReason             string               `json:"adminReason"`
		OccurredAt              time.Time            `json:"occurredAt"`
		AppliedRevision         saved.RevisionToken  `json:"appliedRevision"`
	} `json:"payload"`
}

func (r *Repository) readQueries() (*saveddb.Queries, error) {
	if r == nil || r.db == nil || r.q == nil {
		return nil, saved.ErrUnavailable
	}
	return r.q, nil
}

// beginMutation looks up the durable operation before requiring audit wiring.
// This allows an exact retry to replay after a process is restarted with a
// read-only repository or a temporarily unavailable audit recorder.
func (r *Repository) beginMutation(ctx context.Context, projectID projectgraph.ResourceID, evidence saved.MutationEvidence) (*sql.Tx, *saveddb.Queries, saveddb.SavedExplorationOperation, bool, error) {
	if r == nil || r.db == nil || r.q == nil {
		return nil, nil, saveddb.SavedExplorationOperation{}, false, saved.ErrUnavailable
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, saveddb.SavedExplorationOperation{}, false, mapStorageError(err)
	}
	q := r.q.WithTx(tx)
	row, err := q.GetSavedExplorationOperation(ctx, saveddb.GetSavedExplorationOperationParams{
		ProjectID: projectID.String(), ActorID: evidence.ActorID, OperationKind: string(evidence.Action), IdempotencyKey: evidence.IdempotencyKey,
	})
	if err == nil {
		if row.RequestFingerprint != evidence.Fingerprint {
			_ = tx.Rollback()
			return nil, nil, saveddb.SavedExplorationOperation{}, false, commandReuseError()
		}
		return tx, q, row, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return nil, nil, saveddb.SavedExplorationOperation{}, false, mapStorageError(err)
	}
	if r.audit == nil {
		_ = tx.Rollback()
		return nil, nil, saveddb.SavedExplorationOperation{}, false, fmt.Errorf("%w: audit intent recorder is required", saved.ErrUnavailable)
	}
	if _, ok := saved.AuditIntentFromContext(ctx); !ok {
		_ = tx.Rollback()
		return nil, nil, saveddb.SavedExplorationOperation{}, false, fmt.Errorf("%w: typed audit intent is required", saved.ErrUnavailable)
	}
	return tx, q, saveddb.SavedExplorationOperation{}, false, nil
}

func (r *Repository) recordAuditIntent(ctx context.Context, tx transaction.Transaction, lifecycle saved.Lifecycle, metadata saved.RevisionMetadata, evidence saved.MutationEvidence) error {
	intent, ok := saved.AuditIntentFromContext(ctx)
	if !ok {
		return fmt.Errorf("%w: typed audit intent is required", saved.ErrUnavailable)
	}
	if r == nil || r.audit == nil {
		return fmt.Errorf("%w: audit intent recorder is required", saved.ErrUnavailable)
	}
	actor := evidence.ActorID
	if intent.PrincipalID != "" && intent.PrincipalID != actor {
		return fmt.Errorf("%w: audit principal does not match mutation actor", saved.ErrInvalid)
	}
	operation, action, capability, ok := savedAuditClassification(evidence.Action)
	if !ok {
		return fmt.Errorf("%w: unsupported audit mutation action %q", saved.ErrInvalid, evidence.Action)
	}
	// Classification is a repository invariant, not caller-provided context.
	// Scope the event identity to the exact durable retry identity so a replay
	// can never emit a second row while distinct projects/actors/commands cannot
	// collide. The digest also keeps the outbox key bounded independently of
	// user-controlled idempotency-key length.
	intent.EventID = savedExplorationAuditEventID(lifecycle.ProjectID, evidence)
	intent.Source = savedExplorationAuditSource
	intent.Operation = operation
	intent.Action = action
	intent.Capability = capability
	intent.Outcome = "success"
	intent.PrincipalID = actor
	intent.RequestID = evidence.RequestID
	intent.CorrelationID = evidence.CorrelationID
	intent.ResourceKind = "saved_exploration"
	intent.ResourceID = lifecycle.ID.String()
	intent.AggregateKey = "saved_exploration:" + lifecycle.ProjectID.String() + ":" + lifecycle.ID.String()
	intent.AggregateSequence = 0
	if metadata.Token() != lifecycle.CurrentRevision.Token() {
		return fmt.Errorf("%w: audit revision does not match mutation lifecycle", saved.ErrConflict)
	}
	// The caller's metadata is intentionally discarded. Revision identity is
	// bound by the immutable operation snapshot and lifecycle pointer. Keep
	// authored payload bytes outside audit metadata: specs are opaque and may
	// contain fields Access must never receive.
	metadataJSON, err := savedExplorationMutationAuditMetadataJSON(evidence, metadata)
	if err != nil {
		return err
	}
	intent.MetadataJSON = metadataJSON
	canonical, err := intent.Canonicalize()
	if err != nil {
		return fmt.Errorf("%w: audit intent: %v", saved.ErrInvalid, err)
	}
	if err := r.audit.RecordAuditIntent(ctx, tx, canonical); err != nil {
		// A durable audit handoff is part of the mutation's atomic commit. Any
		// recorder failure therefore makes the saved repository unavailable;
		// Join retains the recorder cause for errors.Is/debugging while the
		// caller's transaction is still rolled back by its owner.
		return errors.Join(saved.ErrUnavailable, err)
	}
	return nil
}

func savedExplorationMutationAuditMetadataJSON(evidence saved.MutationEvidence, metadata saved.RevisionMetadata) (string, error) {
	envelope := savedExplorationMutationAuditMetadata{
		SchemaVersion: savedExplorationAuditMetadataSchemaVersion,
		Retention:     "security",
		PayloadSchema: "SavedExplorationMutationAuditPayload",
	}
	envelope.Payload.MutationEvidenceVersion = evidence.Version
	envelope.Payload.ActorID = evidence.ActorID
	envelope.Payload.Action = evidence.Action
	envelope.Payload.IdempotencyKey = evidence.IdempotencyKey
	envelope.Payload.Fingerprint = evidence.Fingerprint
	envelope.Payload.RequestID = evidence.RequestID
	envelope.Payload.CorrelationID = evidence.CorrelationID
	envelope.Payload.AdminOverride = evidence.AdminOverride
	envelope.Payload.AdminReason = evidence.AdminReason
	envelope.Payload.OccurredAt = evidence.OccurredAt
	envelope.Payload.AppliedRevision = metadata.Token()
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode saved exploration audit metadata: %w", err)
	}
	if len(encoded) > access.MaxAuditIntentMetadataBytes {
		return "", fmt.Errorf("saved exploration audit metadata exceeds %d bytes", access.MaxAuditIntentMetadataBytes)
	}
	return string(encoded), nil
}

func savedAuditClassification(action saved.MutationAction) (operation, auditAction string, capability access.Capability, ok bool) {
	switch action {
	case saved.MutationActionCreate:
		return "createSavedExploration", "saved_exploration.created", access.CapabilityResourceEdit, true
	case saved.MutationActionUpdate:
		return "updateSavedExploration", "saved_exploration.updated", access.CapabilityResourceEdit, true
	case saved.MutationActionDuplicate:
		return "duplicateSavedExploration", "saved_exploration.duplicated", access.CapabilityResourceEdit, true
	case saved.MutationActionArchive:
		return "archiveSavedExploration", "saved_exploration.archived", access.CapabilityResourceManage, true
	default:
		return "", "", "", false
	}
}

func savedExplorationAuditEventID(projectID projectgraph.ResourceID, evidence saved.MutationEvidence) string {
	sum := sha256.Sum256([]byte(projectID.String() + "\x00" + evidence.ActorID + "\x00" + string(evidence.Action) + "\x00" + evidence.IdempotencyKey))
	return "saved-exploration:" + hex.EncodeToString(sum[:])
}

func insertRevision(ctx context.Context, q *saveddb.Queries, projectID, explorationID string, revision saved.Revision) error {
	revisionNumber, err := sqliteNumber(revision.Metadata.Number)
	if err != nil {
		return err
	}
	err = q.InsertSavedExplorationRevision(ctx, saveddb.InsertSavedExplorationRevisionParams{
		ProjectID: projectID, ExplorationID: explorationID, RevisionID: revision.Metadata.ID.String(), RevisionNumber: revisionNumber,
		SpecEnvelopeVersion: int64(revision.Payload.Version()), SpecCanonicalJson: string(revision.Payload.Canonical()), ContentHash: revision.Metadata.ContentHash,
		CreatedBy: revision.Metadata.CreatedBy, CreatedAt: formatTime(revision.Metadata.CreatedAt), ServingProjectID: revision.Metadata.ServingIdentity.ProjectID.String(),
		ServingEnvironment: revision.Metadata.ServingIdentity.Environment, ServingGenerationID: revision.Metadata.ServingIdentity.GenerationID,
	})
	if isConstraint(err) {
		return fmt.Errorf("%w: revision identity or number already exists", saved.ErrConflict)
	}
	return mapStorageError(err)
}

func insertOperation(ctx context.Context, q *saveddb.Queries, result saved.MutationResult, evidence saved.MutationEvidence) error {
	metadata := result.Lifecycle.CurrentRevision
	revisionNumber, err := sqliteNumber(result.AppliedRevision.Number)
	if err != nil {
		return err
	}
	return q.InsertSavedExplorationOperation(ctx, saveddb.InsertSavedExplorationOperationParams{
		ProjectID: result.Lifecycle.ProjectID.String(), ActorID: evidence.ActorID, OperationKind: string(evidence.Action), IdempotencyKey: evidence.IdempotencyKey, RequestFingerprint: evidence.Fingerprint,
		ResultExplorationID: result.Lifecycle.ID.String(), ResultOwnerPrincipalID: result.Lifecycle.OwnerPrincipalID, ResultTitle: result.Lifecycle.Title, ResultSlug: result.Lifecycle.Slug,
		ResultVisibility: string(result.Lifecycle.Visibility), ResultStatus: string(result.Lifecycle.Status), ResultSemanticModelID: result.Lifecycle.SemanticModelID.String(),
		ResultCreatedAt: formatTime(result.Lifecycle.CreatedAt), ResultUpdatedAt: formatTime(result.Lifecycle.UpdatedAt), ResultArchivedAt: nullTime(result.Lifecycle.ArchivedAt),
		ResultRevisionID: result.AppliedRevision.RevisionID.String(), ResultRevisionNumber: revisionNumber, ResultContentHash: result.AppliedRevision.ContentHash,
		ResultRevisionCreatedAt: formatTime(metadata.CreatedAt), ResultRevisionCreatedBy: metadata.CreatedBy, ResultServingProjectID: metadata.ServingIdentity.ProjectID.String(),
		ResultServingEnvironment: metadata.ServingIdentity.Environment, ResultServingGenerationID: metadata.ServingIdentity.GenerationID,
		EvidenceVersion: int64(evidence.Version), EvidenceRequestID: evidence.RequestID, EvidenceCorrelationID: evidence.CorrelationID,
		EvidenceAdminOverride: boolInt(evidence.AdminOverride), EvidenceAdminReason: evidence.AdminReason, EvidenceOccurredAt: formatTime(evidence.OccurredAt), CreatedAt: formatTime(evidence.OccurredAt),
	})
}

func (r *Repository) operationInsertFailure(ctx context.Context, q *saveddb.Queries, projectID string, evidence saved.MutationEvidence, err error, concurrencyRevision ...saved.RevisionToken) (saved.MutationResult, error) {
	if !isConstraint(err) {
		return saved.MutationResult{}, mapStorageError(err)
	}
	row, lookupErr := q.GetSavedExplorationOperation(ctx, saveddb.GetSavedExplorationOperationParams{
		ProjectID: projectID, ActorID: evidence.ActorID, OperationKind: string(evidence.Action), IdempotencyKey: evidence.IdempotencyKey,
	})
	if errors.Is(lookupErr, sql.ErrNoRows) {
		return saved.MutationResult{}, mapStorageError(err)
	}
	if lookupErr != nil {
		return saved.MutationResult{}, mapStorageError(lookupErr)
	}
	if row.RequestFingerprint != evidence.Fingerprint {
		return saved.MutationResult{}, commandReuseError()
	}
	return r.replayResult(ctx, q, row, concurrencyRevision...)
}

func (r *Repository) replayResult(ctx context.Context, q *saveddb.Queries, row saveddb.SavedExplorationOperation, concurrencyRevision ...saved.RevisionToken) (saved.MutationResult, error) {
	metadata, err := replayMetadata(row)
	if err != nil {
		return saved.MutationResult{}, err
	}
	result := saved.MutationResult{Lifecycle: metadata.Lifecycle, AppliedRevision: metadata.AppliedRevision, Evidence: metadata.Evidence}
	if len(concurrencyRevision) > 0 {
		result.ConcurrencyRevision = concurrencyRevision[0]
	}
	if row.OperationKind != string(saved.MutationActionArchive) {
		revision, err := revisionByToken(ctx, q, row.ProjectID, row.ResultExplorationID, result.AppliedRevision)
		if err != nil {
			return saved.MutationResult{}, err
		}
		result.Revision = revisionPtr(revision)
	}
	result.Replayed = true
	if err := result.Validate(); err != nil {
		return saved.MutationResult{}, fmt.Errorf("validate durable mutation replay: %w", err)
	}
	return result, nil
}

func replayMetadata(row saveddb.SavedExplorationOperation) (saved.MutationReplayMetadata, error) {
	archivedAt := row.ResultArchivedAt
	lifecycle, err := lifecycleFromProjection(row.ProjectID, row.ResultExplorationID, row.ResultOwnerPrincipalID, row.ResultTitle, row.ResultSlug,
		row.ResultVisibility, row.ResultStatus, row.ResultSemanticModelID, row.ResultCreatedAt, row.ResultUpdatedAt, archivedAt,
		row.ResultRevisionID, row.ResultRevisionNumber, row.ResultContentHash, row.ResultRevisionCreatedBy, row.ResultRevisionCreatedAt,
		row.ResultServingProjectID, row.ResultServingEnvironment, row.ResultServingGenerationID)
	if err != nil {
		return saved.MutationReplayMetadata{}, err
	}
	occurredAt, err := parseTime(row.EvidenceOccurredAt)
	if err != nil {
		return saved.MutationReplayMetadata{}, fmt.Errorf("decode stored mutation evidence: %w", err)
	}
	evidence := saved.MutationEvidence{Version: uint32(row.EvidenceVersion), ActorID: row.ActorID, IdempotencyKey: row.IdempotencyKey,
		Fingerprint: row.RequestFingerprint, Action: saved.MutationAction(row.OperationKind), RequestID: row.EvidenceRequestID,
		CorrelationID: row.EvidenceCorrelationID, AdminOverride: row.EvidenceAdminOverride != 0, AdminReason: row.EvidenceAdminReason, OccurredAt: occurredAt}
	if err := evidence.Validate(); err != nil {
		return saved.MutationReplayMetadata{}, fmt.Errorf("validate stored mutation evidence: %w", err)
	}
	metadata := saved.MutationReplayMetadata{Lifecycle: lifecycle, AppliedRevision: lifecycle.CurrentRevision.Token(), Evidence: evidence}
	if err := metadata.Validate(); err != nil {
		return saved.MutationReplayMetadata{}, fmt.Errorf("validate stored mutation replay metadata: %w", err)
	}
	return metadata, nil
}

func lifecycleByID(ctx context.Context, q *saveddb.Queries, projectID, explorationID string) (saved.Lifecycle, error) {
	row, err := q.GetSavedExplorationLifecycle(ctx, saveddb.GetSavedExplorationLifecycleParams{ProjectID: projectID, ExplorationID: explorationID})
	if errors.Is(err, sql.ErrNoRows) {
		return saved.Lifecycle{}, saved.ErrNotFound
	}
	if err != nil {
		return saved.Lifecycle{}, mapStorageError(err)
	}
	return lifecycleFromProjection(row.ProjectID, row.ExplorationID, row.OwnerPrincipalID, row.Title, row.Slug, row.Visibility, row.Status, row.SemanticModelID,
		row.CreatedAt, row.UpdatedAt, row.ArchivedAt, row.RevisionID, row.RevisionNumber, row.ContentHash, row.CreatedBy,
		row.RevisionCreatedAt, row.ServingProjectID, row.ServingEnvironment, row.ServingGenerationID)
}

func revisionByToken(ctx context.Context, q *saveddb.Queries, projectID, explorationID string, token saved.RevisionToken) (saved.Revision, error) {
	revisionNumber, err := sqliteNumber(token.Number)
	if err != nil {
		return saved.Revision{}, err
	}
	row, err := q.GetSavedExplorationRevision(ctx, saveddb.GetSavedExplorationRevisionParams{ProjectID: projectID, ExplorationID: explorationID,
		RevisionID: token.RevisionID.String(), RevisionNumber: revisionNumber, ContentHash: token.ContentHash})
	if errors.Is(err, sql.ErrNoRows) {
		return saved.Revision{}, saved.ErrNotFound
	}
	if err != nil {
		return saved.Revision{}, mapStorageError(err)
	}
	return revisionFromRow(row)
}

func revisionFromRow(row saveddb.SavedExplorationRevision) (saved.Revision, error) {
	payload, err := saved.DecodeExplorationSpecPayload([]byte(row.SpecCanonicalJson))
	if err != nil {
		return saved.Revision{}, err
	}
	if payload.Version() != uint32(row.SpecEnvelopeVersion) || payload.ContentHash() != row.ContentHash {
		return saved.Revision{}, fmt.Errorf("%w: stored revision payload identity does not match content hash", saved.ErrInvalidPayload)
	}
	projectID, err := projectgraph.NewResourceID(row.ProjectID)
	if err != nil {
		return saved.Revision{}, fmt.Errorf("%w: stored revision project identity: %v", saved.ErrInvalid, err)
	}
	servingProjectID, err := projectgraph.NewResourceID(row.ServingProjectID)
	if err != nil {
		return saved.Revision{}, fmt.Errorf("%w: stored serving project identity: %v", saved.ErrInvalid, err)
	}
	serving, err := projectgraph.NewServingIdentity(servingProjectID, row.ServingEnvironment, row.ServingGenerationID)
	if err != nil {
		return saved.Revision{}, fmt.Errorf("%w: stored serving identity: %v", saved.ErrInvalid, err)
	}
	number, err := positiveNumber(row.RevisionNumber)
	if err != nil {
		return saved.Revision{}, err
	}
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return saved.Revision{}, err
	}
	revision := saved.Revision{Metadata: saved.RevisionMetadata{ID: saved.RevisionID(row.RevisionID), Number: number, ContentHash: row.ContentHash,
		CreatedAt: createdAt, CreatedBy: row.CreatedBy, ServingIdentity: serving}, Payload: payload}
	if revision.Metadata.ServingIdentity.ProjectID != projectID {
		return saved.Revision{}, fmt.Errorf("%w: stored serving project differs from revision project", saved.ErrInvalid)
	}
	if err := revision.Validate(); err != nil {
		return saved.Revision{}, fmt.Errorf("validate stored saved exploration revision: %w", err)
	}
	return revision, nil
}

func lifecycleFromProjection(projectIDText, explorationIDText, owner, title, slug, visibility, status, semanticModelID, createdAtText, updatedAtText string,
	archivedAt sql.NullString, revisionIDText string, revisionNumber int64, contentHash, revisionCreatedBy, revisionCreatedAtText, servingProjectIDText, servingEnvironment, servingGenerationID string) (saved.Lifecycle, error) {
	projectID, err := projectgraph.NewResourceID(projectIDText)
	if err != nil {
		return saved.Lifecycle{}, fmt.Errorf("%w: stored lifecycle project identity: %v", saved.ErrInvalid, err)
	}
	createdAt, err := parseTime(createdAtText)
	if err != nil {
		return saved.Lifecycle{}, err
	}
	updatedAt, err := parseTime(updatedAtText)
	if err != nil {
		return saved.Lifecycle{}, err
	}
	revisionCreatedAt, err := parseTime(revisionCreatedAtText)
	if err != nil {
		return saved.Lifecycle{}, err
	}
	number, err := positiveNumber(revisionNumber)
	if err != nil {
		return saved.Lifecycle{}, err
	}
	var archived *time.Time
	if archivedAt.Valid {
		value, err := parseTime(archivedAt.String)
		if err != nil {
			return saved.Lifecycle{}, err
		}
		archived = &value
	}
	servingProjectID, err := projectgraph.NewResourceID(servingProjectIDText)
	if err != nil {
		return saved.Lifecycle{}, fmt.Errorf("%w: stored lifecycle serving project identity: %v", saved.ErrInvalid, err)
	}
	identity, err := projectgraph.NewServingIdentity(servingProjectID, servingEnvironment, servingGenerationID)
	if err != nil {
		return saved.Lifecycle{}, fmt.Errorf("%w: stored lifecycle serving identity: %v", saved.ErrInvalid, err)
	}
	lifecycle := saved.Lifecycle{ProjectID: projectID, ID: saved.ExplorationID(explorationIDText), OwnerPrincipalID: owner, Title: title, Slug: slug,
		Visibility: saved.Visibility(visibility), SemanticModelID: projectgraph.ResourceID(semanticModelID), Status: saved.Status(status), CreatedAt: createdAt, UpdatedAt: updatedAt,
		ArchivedAt: archived, CurrentRevision: saved.RevisionMetadata{ID: saved.RevisionID(revisionIDText), Number: number, ContentHash: contentHash,
			CreatedAt: revisionCreatedAt, CreatedBy: revisionCreatedBy, ServingIdentity: identity}}
	if err := lifecycle.Validate(); err != nil {
		return saved.Lifecycle{}, fmt.Errorf("validate stored saved exploration lifecycle: %w", err)
	}
	return lifecycle, nil
}

func classifyCASFailure(ctx context.Context, q *saveddb.Queries, projectID, explorationID string, archive bool) error {
	lifecycle, err := lifecycleByID(ctx, q, projectID, explorationID)
	if errors.Is(err, saved.ErrNotFound) {
		return saved.ErrNotFound
	}
	if err != nil {
		return err
	}
	if lifecycle.Status == saved.StatusArchived {
		return saved.ErrArchived
	}
	if archive {
		return saved.ErrStaleRevision
	}
	return saved.ErrStaleRevision
}

func mapCreateConstraint(ctx context.Context, tx *sql.Tx, projectID, explorationID, slug string, err error) error {
	if !isConstraint(err) {
		return mapStorageError(err)
	}
	var existing int
	if queryErr := tx.QueryRowContext(ctx, `SELECT 1 FROM saved_explorations WHERE project_id = ? AND exploration_id = ?`, projectID, explorationID).Scan(&existing); queryErr == nil {
		return fmt.Errorf("%w: exploration identity already exists", saved.ErrAlreadyExists)
	}
	return fmt.Errorf("%w: exploration identity or slug is already in use", saved.ErrConflict)
}

func commandReuseError() error {
	return fmt.Errorf("%w: mutation idempotency key was reused with a different request", saved.ErrConflict)
}

func mapStorageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrConnDone) {
		return fmt.Errorf("%w: sqlite connection is unavailable", saved.ErrUnavailable)
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "database is locked") || strings.Contains(message, "database is busy") || strings.Contains(message, "connection is closed") {
		return fmt.Errorf("%w: sqlite storage is unavailable", saved.ErrUnavailable)
	}
	if isConstraint(err) {
		return fmt.Errorf("%w: durable saved exploration constraint", saved.ErrConflict)
	}
	return err
}

func isConstraint(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "constraint") || strings.Contains(message, "unique") || strings.Contains(message, "foreign key")
}

func formatTime(value time.Time) string { return value.UTC().Format(sqliteTimeLayout) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(sqliteTimeLayout, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid stored timestamp %q", saved.ErrInvalid, value)
	}
	if parsed.Location() != time.UTC {
		return time.Time{}, fmt.Errorf("%w: stored timestamp is not UTC", saved.ErrInvalid)
	}
	return parsed, nil
}

func positiveNumber(value int64) (uint64, error) {
	if value <= 0 {
		return 0, fmt.Errorf("%w: stored revision number is invalid", saved.ErrInvalidRevision)
	}
	return uint64(value), nil
}

func sqliteNumber(value uint64) (int64, error) {
	if value == 0 || value > uint64(^uint64(0)>>1) {
		return 0, fmt.Errorf("%w: revision number cannot be represented by SQLite", saved.ErrInvalidRevision)
	}
	return int64(value), nil
}

func nullTime(value *time.Time) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: formatTime(*value), Valid: true}
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func revisionPtr(value saved.Revision) *saved.Revision {
	copy := value.Clone()
	return &copy
}
