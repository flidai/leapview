package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	saveddb "github.com/flidai/leapview/internal/analytics/exploration/saved/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	savedExplorationAuditSource                = "analytics.exploration.saved"
	savedExplorationAuditMetadataSchemaVersion = 1
)

type lifecycleRow struct {
	ProjectID, ExplorationID, OwnerPrincipalID, Title, Slug   string
	Visibility, Status, SemanticModelID                       string
	CreatedAt, UpdatedAt                                      string
	ArchivedAt                                                *string
	RevisionID                                                string
	RevisionNumber                                            int64
	ContentHash, RevisionCreatedBy, RevisionCreatedAt         string
	ServingProjectID, ServingEnvironment, ServingGenerationID string
}

type revisionRow struct {
	ProjectID, ExplorationID, RevisionID                      string
	RevisionNumber                                            int64
	SpecEnvelopeVersion                                       int32
	SpecCanonicalJSON                                         []byte
	ContentHash, CreatedBy, CreatedAt                         string
	ServingProjectID, ServingEnvironment, ServingGenerationID string
}

type operationRow struct {
	ProjectID, ActorID, OperationKind, IdempotencyKey, RequestFingerprint       string
	ResultExplorationID, ResultOwnerPrincipalID, ResultTitle, ResultSlug        string
	ResultVisibility, ResultStatus, ResultSemanticModelID                       string
	ResultCreatedAt, ResultUpdatedAt                                            string
	ResultArchivedAt                                                            *string
	ResultRevisionID                                                            string
	ResultRevisionNumber                                                        int64
	ResultContentHash, ResultRevisionCreatedAt, ResultRevisionCreatedBy         string
	ResultServingProjectID, ResultServingEnvironment, ResultServingGenerationID string
	EvidenceVersion                                                             int32
	EvidenceRequestID, EvidenceCorrelationID                                    string
	EvidenceAdminOverride                                                       bool
	EvidenceAdminReason, EvidenceOccurredAt, CreatedAt                          string
}

func (r *Repository) beginMutation(ctx context.Context, projectID projectgraph.ResourceID, evidence saved.MutationEvidence) (pgx.Tx, operationRow, bool, error) {
	if r == nil || isNilInterface(r.db) || r.begin == nil {
		return nil, operationRow{}, false, saved.ErrUnavailable
	}
	if ctx == nil {
		return nil, operationRow{}, false, fmt.Errorf("%w: context is nil", saved.ErrUnavailable)
	}
	tx, err := r.begin.Begin(ctx)
	if err != nil {
		return nil, operationRow{}, false, mapStorageError(err)
	}
	// PostgreSQL aborts a transaction after a unique violation. Serialize only
	// the exact retry identity instead, so same-key requests can inspect and
	// replay the committed ledger row without an aborted-transaction recovery.
	lockIdentity := projectID.String() + "\x00" + evidence.ActorID + "\x00" + string(evidence.Action) + "\x00" + evidence.IdempotencyKey
	lockDigest := sha256.Sum256([]byte(lockIdentity))
	lockKey := hex.EncodeToString(lockDigest[:])
	if err := saveddb.New(tx).AcquireSavedExplorationRetryLock(ctx, lockKey); err != nil {
		_ = tx.Rollback(context.Background())
		return nil, operationRow{}, false, mapStorageError(err)
	}
	row, err := getOperationRow(ctx, tx, projectID.String(), evidence.ActorID, string(evidence.Action), evidence.IdempotencyKey)
	if err == nil {
		if row.RequestFingerprint != evidence.Fingerprint {
			_ = tx.Rollback(context.Background())
			return nil, operationRow{}, false, commandReuseError()
		}
		return tx, row, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(context.Background())
		return nil, operationRow{}, false, mapStorageError(err)
	}
	if isNilInterface(r.audit) {
		_ = tx.Rollback(context.Background())
		return nil, operationRow{}, false, fmt.Errorf("%w: audit intent recorder is required", saved.ErrUnavailable)
	}
	if _, ok := saved.AuditIntentFromContext(ctx); !ok {
		_ = tx.Rollback(context.Background())
		return nil, operationRow{}, false, fmt.Errorf("%w: typed audit intent is required", saved.ErrUnavailable)
	}
	return tx, operationRow{}, false, nil
}

func (r *Repository) recordAuditIntent(ctx context.Context, tx Tx, lifecycle saved.Lifecycle, metadata saved.RevisionMetadata, evidence saved.MutationEvidence) error {
	intent, ok := saved.AuditIntentFromContext(ctx)
	if !ok {
		return fmt.Errorf("%w: typed audit intent is required", saved.ErrUnavailable)
	}
	if r == nil || isNilInterface(r.audit) {
		return fmt.Errorf("%w: audit intent recorder is required", saved.ErrUnavailable)
	}
	if intent.PrincipalID != "" && intent.PrincipalID != evidence.ActorID {
		return fmt.Errorf("%w: audit principal does not match mutation actor", saved.ErrInvalid)
	}
	// Access stores the typed principal identity as UUID, while the canonical
	// audit contract permits saved-exploration actors from another authority.
	// Preserve an opaque identity in ActorID and leave the optional typed
	// PrincipalID unset rather than making the mutation unauditable at this
	// adapter boundary.
	if intent.PrincipalID != "" {
		if _, err := uuid.Parse(intent.PrincipalID); err != nil {
			intent.PrincipalID = ""
		}
	}
	operation, action, capability, ok := savedAuditClassification(evidence.Action)
	if !ok {
		return fmt.Errorf("%w: unsupported audit mutation action %q", saved.ErrInvalid, evidence.Action)
	}
	if metadata.Token() != lifecycle.CurrentRevision.Token() {
		return fmt.Errorf("%w: audit revision does not match mutation lifecycle", saved.ErrConflict)
	}
	intent.EventID = savedExplorationAuditEventID(lifecycle.ProjectID, evidence)
	// These fields are source-owned identity bindings. A transport-supplied
	// intent may carry descriptive values, but it cannot relabel the project,
	// actor, or request represented by the durable mutation.
	intent.ScopeID = lifecycle.ProjectID.String()
	intent.ActorID = evidence.ActorID
	intent.RequestDigest = evidence.Fingerprint
	intent.Source = savedExplorationAuditSource
	intent.Operation = operation
	intent.Action = action
	intent.Capability = capability
	intent.Outcome = "success"
	intent.RequestID = evidence.RequestID
	intent.CorrelationID = evidence.CorrelationID
	intent.ResourceKind = "saved_exploration"
	intent.ResourceID = lifecycle.ID.String()
	intent.AggregateKey = "saved_exploration:" + lifecycle.ProjectID.String() + ":" + lifecycle.ID.String()
	intent.AggregateSequence = 0
	metadataJSON, err := savedExplorationMutationAuditMetadataJSON(evidence, metadata)
	if err != nil {
		return err
	}
	intent.MetadataJSON = metadataJSON
	canonical, err := intent.Canonicalize()
	if err != nil {
		return fmt.Errorf("%w: audit intent: %v", saved.ErrInvalid, err)
	}
	if err := r.audit.RecordAuditEvent(ctx, tx, canonical); err != nil {
		return errors.Join(saved.ErrUnavailable, err)
	}
	return nil
}

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

func savedExplorationMutationAuditMetadataJSON(evidence saved.MutationEvidence, metadata saved.RevisionMetadata) (string, error) {
	envelope := savedExplorationMutationAuditMetadata{SchemaVersion: savedExplorationAuditMetadataSchemaVersion, Retention: "security", PayloadSchema: "SavedExplorationMutationAuditPayload"}
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
	id := uuid.UUID(sum[:16])
	id[6] = (id[6] & 0x0f) | 0x80 // RFC 9562 version 8 custom identity
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}

func insertRevision(ctx context.Context, db DBTX, projectID, explorationID string, revision saved.Revision) error {
	revisionNumber, err := postgresNumber(revision.Metadata.Number)
	if err != nil {
		return err
	}
	err = saveddb.New(db).InsertSavedExplorationRevision(ctx, saveddb.InsertSavedExplorationRevisionParams{
		ProjectID: projectID, ExplorationID: explorationID, RevisionID: revision.Metadata.ID.String(), RevisionNumber: revisionNumber,
		SpecEnvelopeVersion: int32(revision.Payload.Version()), SpecCanonicalJson: revision.Payload.Canonical(), ContentHash: revision.Metadata.ContentHash,
		CreatedBy: revision.Metadata.CreatedBy, CreatedAt: formatTime(revision.Metadata.CreatedAt), ServingProjectID: revision.Metadata.ServingIdentity.ProjectID.String(),
		ServingEnvironment: revision.Metadata.ServingIdentity.Environment, ServingGenerationID: revision.Metadata.ServingIdentity.GenerationID,
	})
	if err != nil {
		if isConstraint(err) {
			return fmt.Errorf("%w: revision identity or number already exists", saved.ErrConflict)
		}
		return mapStorageError(err)
	}
	return nil
}

func insertOperation(ctx context.Context, db DBTX, result saved.MutationResult, evidence saved.MutationEvidence) (bool, error) {
	metadata := result.Lifecycle.CurrentRevision
	revisionNumber, err := postgresNumber(result.AppliedRevision.Number)
	if err != nil {
		return false, err
	}
	archivedAt := nullableString(result.Lifecycle.ArchivedAt)
	commandTag, err := saveddb.New(db).InsertSavedExplorationOperation(ctx, saveddb.InsertSavedExplorationOperationParams{
		ProjectID: result.Lifecycle.ProjectID.String(), ActorID: evidence.ActorID, OperationKind: string(evidence.Action), IdempotencyKey: evidence.IdempotencyKey, RequestFingerprint: evidence.Fingerprint,
		ResultExplorationID: result.Lifecycle.ID.String(), ResultOwnerPrincipalID: result.Lifecycle.OwnerPrincipalID, ResultTitle: result.Lifecycle.Title, ResultSlug: result.Lifecycle.Slug,
		ResultVisibility: string(result.Lifecycle.Visibility), ResultStatus: string(result.Lifecycle.Status), ResultSemanticModelID: result.Lifecycle.SemanticModelID.String(),
		ResultCreatedAt: formatTime(result.Lifecycle.CreatedAt), ResultUpdatedAt: formatTime(result.Lifecycle.UpdatedAt), ResultArchivedAt: archivedAt,
		ResultRevisionID: result.AppliedRevision.RevisionID.String(), ResultRevisionNumber: revisionNumber, ResultContentHash: result.AppliedRevision.ContentHash,
		ResultRevisionCreatedAt: formatTime(metadata.CreatedAt), ResultRevisionCreatedBy: metadata.CreatedBy, ResultServingProjectID: metadata.ServingIdentity.ProjectID.String(), ResultServingEnvironment: metadata.ServingIdentity.Environment, ResultServingGenerationID: metadata.ServingIdentity.GenerationID,
		EvidenceVersion: int32(evidence.Version), EvidenceRequestID: evidence.RequestID, EvidenceCorrelationID: evidence.CorrelationID, EvidenceAdminOverride: evidence.AdminOverride,
		EvidenceAdminReason: evidence.AdminReason, EvidenceOccurredAt: formatTime(evidence.OccurredAt), CreatedAt: formatTime(evidence.OccurredAt),
	})
	if err != nil {
		return false, mapStorageError(err)
	}
	return commandTag == 1, nil
}

func (r *Repository) replayResult(ctx context.Context, db DBTX, row operationRow, concurrencyRevision ...saved.RevisionToken) (saved.MutationResult, error) {
	metadata, err := replayMetadata(row)
	if err != nil {
		return saved.MutationResult{}, err
	}
	result := saved.MutationResult{Lifecycle: metadata.Lifecycle, AppliedRevision: metadata.AppliedRevision, Evidence: metadata.Evidence, Replayed: true}
	if len(concurrencyRevision) > 0 {
		result.ConcurrencyRevision = concurrencyRevision[0]
	}
	if row.OperationKind != string(saved.MutationActionArchive) {
		revision, err := revisionByToken(ctx, db, row.ProjectID, row.ResultExplorationID, result.AppliedRevision)
		if err != nil {
			return saved.MutationResult{}, err
		}
		result.Revision = revisionPtr(revision)
	}
	if err := result.Validate(); err != nil {
		return saved.MutationResult{}, fmt.Errorf("validate durable mutation replay: %w", err)
	}
	return result, nil
}

func replayMetadata(row operationRow) (saved.MutationReplayMetadata, error) {
	archivedAt := row.ResultArchivedAt
	lifecycle, err := lifecycleFromValues(row.ProjectID, row.ResultExplorationID, row.ResultOwnerPrincipalID, row.ResultTitle, row.ResultSlug,
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
		CorrelationID: row.EvidenceCorrelationID, AdminOverride: row.EvidenceAdminOverride, AdminReason: row.EvidenceAdminReason, OccurredAt: occurredAt}
	if err := evidence.Validate(); err != nil {
		return saved.MutationReplayMetadata{}, fmt.Errorf("validate stored mutation evidence: %w", err)
	}
	metadata := saved.MutationReplayMetadata{Lifecycle: lifecycle, AppliedRevision: lifecycle.CurrentRevision.Token(), Evidence: evidence}
	if err := metadata.Validate(); err != nil {
		return saved.MutationReplayMetadata{}, fmt.Errorf("validate stored mutation replay metadata: %w", err)
	}
	return metadata, nil
}

func getLifecycleRow(ctx context.Context, db DBTX, projectID, explorationID string, forUpdate bool) (lifecycleRow, error) {
	q := saveddb.New(db)
	if forUpdate {
		row, err := q.GetSavedExplorationLifecycleForUpdate(ctx, saveddb.GetSavedExplorationLifecycleForUpdateParams{ProjectID: projectID, ExplorationID: explorationID})
		return lifecycleRowFromUpdate(row), err
	}
	row, err := q.GetSavedExplorationLifecycle(ctx, saveddb.GetSavedExplorationLifecycleParams{ProjectID: projectID, ExplorationID: explorationID})
	return lifecycleRowFromNormal(row), err
}

func lifecycleRowFromNormal(row saveddb.GetSavedExplorationLifecycleRow) lifecycleRow {
	return lifecycleRow{ProjectID: row.ProjectID, ExplorationID: row.ExplorationID, OwnerPrincipalID: row.OwnerPrincipalID, Title: row.Title, Slug: row.Slug,
		Visibility: row.Visibility, Status: row.Status, SemanticModelID: row.SemanticModelID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ArchivedAt: row.ArchivedAt,
		RevisionID: row.RevisionID, RevisionNumber: row.RevisionNumber, ContentHash: row.ContentHash, RevisionCreatedBy: row.CreatedBy, RevisionCreatedAt: row.RevisionCreatedAt,
		ServingProjectID: row.ServingProjectID, ServingEnvironment: row.ServingEnvironment, ServingGenerationID: row.ServingGenerationID}
}

func lifecycleRowFromUpdate(row saveddb.GetSavedExplorationLifecycleForUpdateRow) lifecycleRow {
	return lifecycleRow{ProjectID: row.ProjectID, ExplorationID: row.ExplorationID, OwnerPrincipalID: row.OwnerPrincipalID, Title: row.Title, Slug: row.Slug,
		Visibility: row.Visibility, Status: row.Status, SemanticModelID: row.SemanticModelID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ArchivedAt: row.ArchivedAt,
		RevisionID: row.RevisionID, RevisionNumber: row.RevisionNumber, ContentHash: row.ContentHash, RevisionCreatedBy: row.CreatedBy, RevisionCreatedAt: row.RevisionCreatedAt,
		ServingProjectID: row.ServingProjectID, ServingEnvironment: row.ServingEnvironment, ServingGenerationID: row.ServingGenerationID}
}

func listLifecycleRows(ctx context.Context, db DBTX, projectID string, includeArchived bool, cursor string, limit int) ([]lifecycleRow, error) {
	rows, err := saveddb.New(db).ListSavedExplorationLifecycles(ctx, saveddb.ListSavedExplorationLifecyclesParams{ProjectID: projectID, IncludeArchived: includeArchived, Cursor: cursor, PageLimit: int32(limit)})
	if err != nil {
		return nil, mapStorageError(err)
	}
	out := make([]lifecycleRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, lifecycleRowFromList(row))
	}
	return out, nil
}

func lifecycleRowFromList(row saveddb.ListSavedExplorationLifecyclesRow) lifecycleRow {
	return lifecycleRow{ProjectID: row.ProjectID, ExplorationID: row.ExplorationID, OwnerPrincipalID: row.OwnerPrincipalID, Title: row.Title, Slug: row.Slug,
		Visibility: row.Visibility, Status: row.Status, SemanticModelID: row.SemanticModelID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ArchivedAt: row.ArchivedAt,
		RevisionID: row.RevisionID, RevisionNumber: row.RevisionNumber, ContentHash: row.ContentHash, RevisionCreatedBy: row.CreatedBy, RevisionCreatedAt: row.RevisionCreatedAt,
		ServingProjectID: row.ServingProjectID, ServingEnvironment: row.ServingEnvironment, ServingGenerationID: row.ServingGenerationID}
}

func getRevisionRow(ctx context.Context, db DBTX, projectID, explorationID string, token saved.RevisionToken) (revisionRow, error) {
	revisionNumber, numberErr := postgresNumber(token.Number)
	if numberErr != nil {
		return revisionRow{}, numberErr
	}
	row, err := saveddb.New(db).GetSavedExplorationRevision(ctx, saveddb.GetSavedExplorationRevisionParams{ProjectID: projectID, ExplorationID: explorationID, RevisionID: token.RevisionID.String(), RevisionNumber: revisionNumber, ContentHash: token.ContentHash})
	return revisionRow{ProjectID: row.ProjectID, ExplorationID: row.ExplorationID, RevisionID: row.RevisionID, RevisionNumber: row.RevisionNumber, SpecEnvelopeVersion: row.SpecEnvelopeVersion, SpecCanonicalJSON: row.SpecCanonicalJson, ContentHash: row.ContentHash, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, ServingProjectID: row.ServingProjectID, ServingEnvironment: row.ServingEnvironment, ServingGenerationID: row.ServingGenerationID}, err
}

func getOperationRow(ctx context.Context, db DBTX, projectID, actorID, operationKind, idempotencyKey string) (operationRow, error) {
	row, err := saveddb.New(db).GetSavedExplorationOperation(ctx, saveddb.GetSavedExplorationOperationParams{ProjectID: projectID, ActorID: actorID, OperationKind: operationKind, IdempotencyKey: idempotencyKey})
	return operationRow{ProjectID: row.ProjectID, ActorID: row.ActorID, OperationKind: row.OperationKind, IdempotencyKey: row.IdempotencyKey, RequestFingerprint: row.RequestFingerprint,
		ResultExplorationID: row.ResultExplorationID, ResultOwnerPrincipalID: row.ResultOwnerPrincipalID, ResultTitle: row.ResultTitle, ResultSlug: row.ResultSlug, ResultVisibility: row.ResultVisibility, ResultStatus: row.ResultStatus, ResultSemanticModelID: row.ResultSemanticModelID,
		ResultCreatedAt: row.ResultCreatedAt, ResultUpdatedAt: row.ResultUpdatedAt, ResultArchivedAt: row.ResultArchivedAt, ResultRevisionID: row.ResultRevisionID, ResultRevisionNumber: row.ResultRevisionNumber, ResultContentHash: row.ResultContentHash,
		ResultRevisionCreatedAt: row.ResultRevisionCreatedAt, ResultRevisionCreatedBy: row.ResultRevisionCreatedBy, ResultServingProjectID: row.ResultServingProjectID, ResultServingEnvironment: row.ResultServingEnvironment, ResultServingGenerationID: row.ResultServingGenerationID,
		EvidenceVersion: row.EvidenceVersion, EvidenceRequestID: row.EvidenceRequestID, EvidenceCorrelationID: row.EvidenceCorrelationID, EvidenceAdminOverride: row.EvidenceAdminOverride, EvidenceAdminReason: row.EvidenceAdminReason, EvidenceOccurredAt: row.EvidenceOccurredAt, CreatedAt: row.CreatedAt}, err
}

func lifecycleByID(ctx context.Context, db DBTX, projectID, explorationID string, forUpdate bool) (saved.Lifecycle, error) {
	row, err := getLifecycleRow(ctx, db, projectID, explorationID, forUpdate)
	if errors.Is(err, pgx.ErrNoRows) {
		return saved.Lifecycle{}, saved.ErrNotFound
	}
	if err != nil {
		return saved.Lifecycle{}, mapStorageError(err)
	}
	return lifecycleFromRow(row)
}

func revisionByToken(ctx context.Context, db DBTX, projectID, explorationID string, token saved.RevisionToken) (saved.Revision, error) {
	row, err := getRevisionRow(ctx, db, projectID, explorationID, token)
	if errors.Is(err, pgx.ErrNoRows) {
		return saved.Revision{}, saved.ErrNotFound
	}
	if err != nil {
		return saved.Revision{}, mapStorageError(err)
	}
	return revisionFromRow(row)
}

func revisionFromRow(row revisionRow) (saved.Revision, error) {
	payload, err := saved.DecodeExplorationSpecPayload(row.SpecCanonicalJSON)
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
	revision := saved.Revision{Metadata: saved.RevisionMetadata{ID: saved.RevisionID(row.RevisionID), Number: number, ContentHash: row.ContentHash, CreatedAt: createdAt, CreatedBy: row.CreatedBy, ServingIdentity: serving}, Payload: payload}
	if revision.Metadata.ServingIdentity.ProjectID != projectID {
		return saved.Revision{}, fmt.Errorf("%w: stored serving project differs from revision project", saved.ErrInvalid)
	}
	if err := revision.Validate(); err != nil {
		return saved.Revision{}, fmt.Errorf("validate stored saved exploration revision: %w", err)
	}
	return revision, nil
}

func lifecycleFromRow(row lifecycleRow) (saved.Lifecycle, error) {
	return lifecycleFromValues(row.ProjectID, row.ExplorationID, row.OwnerPrincipalID, row.Title, row.Slug, row.Visibility, row.Status, row.SemanticModelID,
		row.CreatedAt, row.UpdatedAt, row.ArchivedAt, row.RevisionID, row.RevisionNumber, row.ContentHash, row.RevisionCreatedBy,
		row.RevisionCreatedAt, row.ServingProjectID, row.ServingEnvironment, row.ServingGenerationID)
}

func lifecycleFromValues(projectIDText, explorationIDText, owner, title, slug, visibility, status, semanticModelID, createdAtText, updatedAtText string,
	archivedAt *string, revisionIDText string, revisionNumber int64, contentHash, revisionCreatedBy, revisionCreatedAtText, servingProjectIDText, servingEnvironment, servingGenerationID string) (saved.Lifecycle, error) {
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
	if archivedAt != nil {
		value, err := parseTime(*archivedAt)
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
		Visibility: saved.Visibility(visibility), SemanticModelID: projectgraph.ResourceID(semanticModelID), Status: saved.Status(status), CreatedAt: createdAt, UpdatedAt: updatedAt, ArchivedAt: archived,
		CurrentRevision: saved.RevisionMetadata{ID: saved.RevisionID(revisionIDText), Number: number, ContentHash: contentHash, CreatedAt: revisionCreatedAt, CreatedBy: revisionCreatedBy, ServingIdentity: identity}}
	if err := lifecycle.Validate(); err != nil {
		return saved.Lifecycle{}, fmt.Errorf("validate stored saved exploration lifecycle: %w", err)
	}
	return lifecycle, nil
}

func classifyCASFailure(ctx context.Context, db DBTX, projectID, explorationID string, _ bool) error {
	lifecycle, err := lifecycleByID(ctx, db, projectID, explorationID, false)
	if errors.Is(err, saved.ErrNotFound) {
		return saved.ErrNotFound
	}
	if err != nil {
		return err
	}
	if lifecycle.Status == saved.StatusArchived {
		return saved.ErrArchived
	}
	return saved.ErrStaleRevision
}

func mapCreateConflict(ctx context.Context, db DBTX, projectID, explorationID string) error {
	exists, err := saveddb.New(db).HasSavedExplorationIdentity(ctx, saveddb.HasSavedExplorationIdentityParams{ProjectID: projectID, ExplorationID: explorationID})
	if err != nil {
		return mapStorageError(err)
	}
	if exists {
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
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505", "23503", "23514", "23502", "22001", "22P02":
			return fmt.Errorf("%w: durable saved exploration constraint: %v", saved.ErrConflict, err)
		case "40001", "40P01", "08000", "08003", "08006", "08007", "57P01":
			return fmt.Errorf("%w: PostgreSQL storage is unavailable: %v", saved.ErrUnavailable, err)
		}
	}
	return err
}

func isConstraint(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "23503" || pgErr.Code == "23514" || pgErr.Code == "23502")
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
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

func postgresNumber(value uint64) (int64, error) {
	if value == 0 || value > uint64(^uint64(0)>>1) {
		return 0, fmt.Errorf("%w: revision number cannot be represented by PostgreSQL bigint", saved.ErrInvalidRevision)
	}
	return int64(value), nil
}

func nullableString(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTime(*value)
	return &formatted
}

func revisionPtr(value saved.Revision) *saved.Revision {
	copy := value.Clone()
	return &copy
}
