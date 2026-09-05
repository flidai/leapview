package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/flidai/leapview/internal/access"
	"github.com/jackc/pgx/v5"
)

const semanticAttributeAssignmentReturning = `
	assignment_id::text, definition_id::text, ''::text, subject_kind, subject_id::text,
	definition_version, value_type, value_shape, canonical_values, value_digest,
	assignment_version, tombstoned_at, created_at, updated_at`

func (r *Repository) setSemanticAttributeAssignmentCore(ctx context.Context, db DBTX, input access.SemanticAttributeAssignmentInput) (access.SemanticAttributeAssignment, access.AuditEventInput, error) {
	if err := access.ValidateSemanticAttributeSubject(input.Subject); err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	canonicalSubjectID, err := uuidID("semantic attribute subject id", input.Subject.ID)
	if err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	input.Subject.ID = canonicalSubjectID
	if input.ExpectedVersion < 0 {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, errors.New("semantic attribute assignment expected version cannot be negative")
	}
	requestedID := ""
	if input.AssignmentID != "" {
		requestedID, err = uuidID("semantic attribute assignment id", input.AssignmentID)
		if err != nil {
			return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
		}
	}
	var definition access.SemanticAttributeDefinition
	switch {
	case input.DefinitionID != "":
		definition, err = (&Repository{db: db}).SemanticAttributeDefinitionByID(ctx, input.DefinitionID)
	case input.DefinitionName != "":
		definition, err = (&Repository{db: db}).SemanticAttributeDefinition(ctx, input.DefinitionName)
	default:
		err = errors.New("semantic attribute assignment definition id or name is required")
	}
	if err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	values, digest, err := access.CanonicalSemanticAttributeValues(definition, input.Values)
	if err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	state, err := lockSemanticAttributeControlState(ctx, db)
	if err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	if _, _, err := validateSemanticAttributeControlState(ctx, db, state); err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}

	replaceStale := false
	var current access.SemanticAttributeAssignment
	currentRow := db.QueryRow(ctx, `SELECT `+semanticAttributeControlAssignmentColumns+`
		FROM access.semantic_attribute_assignment a
		JOIN access.semantic_attribute_definition d ON d.definition_id = a.definition_id
		WHERE a.definition_id = $1::uuid AND a.subject_kind = $2::text
		  AND a.subject_id = $3::uuid AND a.tombstoned_at IS NULL`, definition.ID, input.Subject.Kind, input.Subject.ID)
	current, err = scanSemanticAttributeControlAssignment(currentRow)
	if err == nil {
		if requestedID != "" && requestedID != current.ID {
			return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, fmt.Errorf("%w: assignment identity does not match the definition and subject", access.ErrSemanticAttributeAssignmentConflict)
		}
		if input.ExpectedVersion != current.AssignmentVersion {
			return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, fmt.Errorf("%w: expected %d, current %d", access.ErrSemanticAttributeAssignmentConflict, input.ExpectedVersion, current.AssignmentVersion)
		}
		if current.DefinitionVersion != definition.DefinitionVersion {
			// Definition changes create a new assignment incarnation. The stale
			// row remains durable, but must be tombstoned before the replacement
			// can claim the active uniqueness key.
			commandTag, tombstoneErr := db.Exec(ctx, `UPDATE access.semantic_attribute_assignment
				SET tombstoned_at = clock_timestamp(), assignment_version = assignment_version + 1
				WHERE assignment_id = $1::uuid AND assignment_version = $2::bigint AND tombstoned_at IS NULL`, current.ID, input.ExpectedVersion)
			if tombstoneErr != nil {
				return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, tombstoneErr
			}
			if commandTag.RowsAffected() != 1 {
				return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, fmt.Errorf("%w: expected %d", access.ErrSemanticAttributeAssignmentConflict, input.ExpectedVersion)
			}
			replaceStale = true
		} else {
			if current.ValueDigest == digest && reflect.DeepEqual(current.CanonicalValues, values) {
				return current, semanticAttributeControlAudit(input.Mutation, "semantic_attribute.assignment.replay", current, state), nil
			}
			updated, updateErr := scanSemanticAttributeControlAssignment(db.QueryRow(ctx, `UPDATE access.semantic_attribute_assignment
				SET canonical_values = $1::text[], value_digest = $2::text,
					assignment_version = assignment_version + 1
				WHERE assignment_id = $3::uuid AND assignment_version = $4::bigint
				  AND tombstoned_at IS NULL
				RETURNING `+semanticAttributeAssignmentReturning, values, digest, current.ID, input.ExpectedVersion))
			if updateErr != nil {
				if errors.Is(updateErr, pgx.ErrNoRows) {
					return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, fmt.Errorf("%w: expected %d", access.ErrSemanticAttributeAssignmentConflict, input.ExpectedVersion)
				}
				return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, updateErr
			}
			updated.DefinitionName = definition.Name
			next, advanceErr := advanceSemanticAttributeControl(ctx, db, state)
			if advanceErr != nil {
				return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, advanceErr
			}
			return updated, semanticAttributeControlAudit(input.Mutation, "semantic_attribute.assignment.set", updated, next), nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	if !replaceStale && input.ExpectedVersion != 0 {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, fmt.Errorf("%w: assignment does not exist, expected %d", access.ErrSemanticAttributeAssignmentConflict, input.ExpectedVersion)
	}
	assignmentID, err := newUUID()
	if err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	inserted, err := scanSemanticAttributeControlAssignment(db.QueryRow(ctx, `INSERT INTO access.semantic_attribute_assignment
		(assignment_id, definition_id, subject_kind, subject_id, definition_version,
		 value_type, value_shape, canonical_values, value_digest)
		VALUES ($1::uuid, $2::uuid, $3::text, $4::uuid, $5::bigint, $6::text,
				$7::text, $8::text[], $9::text)
		RETURNING `+semanticAttributeAssignmentReturning, assignmentID, definition.ID, input.Subject.Kind, input.Subject.ID,
		definition.DefinitionVersion, definition.Type, definition.Shape, values, digest))
	if err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	inserted.DefinitionName = definition.Name
	next, err := advanceSemanticAttributeControl(ctx, db, state)
	if err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	return inserted, semanticAttributeControlAudit(input.Mutation, "semantic_attribute.assignment.set", inserted, next), nil
}

func (r *Repository) SetSemanticAttributeAssignment(ctx context.Context, input access.SemanticAttributeAssignmentInput) (result access.SemanticAttributeAssignment, err error) {
	err = r.RunAuditedMutation(ctx, func(repo access.Repository) (access.AuditEventInput, error) {
		var audit access.AuditEventInput
		result, audit, err = repo.(*Repository).setSemanticAttributeAssignmentCore(ctx, repo.(*Repository).db, input)
		return audit, err
	})
	return result, err
}

// SetSemanticAttributeAssignmentTx composes assignment and audit insertion
// into an existing caller-owned pgx transaction. It never commits or rolls it
// back, allowing a source mutation and this control write to share one unit.
func SetSemanticAttributeAssignmentTx(ctx context.Context, tx Tx, input access.SemanticAttributeAssignmentInput) (access.SemanticAttributeAssignment, error) {
	if tx == nil {
		return access.SemanticAttributeAssignment{}, errors.New("semantic attribute assignment PostgreSQL transaction is required")
	}
	result, audit, err := (&Repository{db: tx}).setSemanticAttributeAssignmentCore(ctx, tx, input)
	if err != nil {
		return access.SemanticAttributeAssignment{}, err
	}
	if err := (&Repository{db: tx}).RecordAuditEvent(ctx, audit); err != nil {
		return access.SemanticAttributeAssignment{}, fmt.Errorf("record semantic attribute assignment audit: %w", err)
	}
	return result, nil
}

func (r *Repository) TombstoneSemanticAttributeAssignment(ctx context.Context, id string, expected int64, mutation access.SemanticAttributeMutationContext) (result access.SemanticAttributeAssignment, err error) {
	err = r.RunAuditedMutation(ctx, func(repo access.Repository) (access.AuditEventInput, error) {
		var audit access.AuditEventInput
		result, audit, err = tombstoneSemanticAttributeAssignmentCore(ctx, repo.(*Repository).db, id, expected, mutation)
		return audit, err
	})
	return result, err
}

func tombstoneSemanticAttributeAssignmentCore(ctx context.Context, db DBTX, id string, expected int64, mutation access.SemanticAttributeMutationContext) (access.SemanticAttributeAssignment, access.AuditEventInput, error) {
	if expected <= 0 {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, errors.New("semantic attribute assignment expected version must be positive")
	}
	canonicalID, err := uuidID("semantic attribute assignment id", id)
	if err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	state, err := lockSemanticAttributeControlState(ctx, db)
	if err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	if _, _, err := validateSemanticAttributeControlState(ctx, db, state); err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	row, err := scanSemanticAttributeControlAssignment(db.QueryRow(ctx, `UPDATE access.semantic_attribute_assignment
		SET tombstoned_at = clock_timestamp(), assignment_version = assignment_version + 1
		WHERE assignment_id = $1::uuid AND assignment_version = $2::bigint AND tombstoned_at IS NULL
		RETURNING `+semanticAttributeAssignmentReturning, canonicalID, expected))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, fmt.Errorf("%w: expected %d", access.ErrSemanticAttributeAssignmentConflict, expected)
		}
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	definition, err := (&Repository{db: db}).SemanticAttributeDefinitionByID(ctx, row.DefinitionID)
	if err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	row.DefinitionName = definition.Name
	next, err := advanceSemanticAttributeControl(ctx, db, state)
	if err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	return row, semanticAttributeControlAudit(mutation, "semantic_attribute.assignment.tombstone", row, next), nil
}

func TombstoneSemanticAttributeAssignmentTx(ctx context.Context, tx Tx, id string, expected int64, mutation access.SemanticAttributeMutationContext) (access.SemanticAttributeAssignment, error) {
	if tx == nil {
		return access.SemanticAttributeAssignment{}, errors.New("semantic attribute assignment PostgreSQL transaction is required")
	}
	result, audit, err := tombstoneSemanticAttributeAssignmentCore(ctx, tx, id, expected, mutation)
	if err != nil {
		return access.SemanticAttributeAssignment{}, err
	}
	if err := (&Repository{db: tx}).RecordAuditEvent(ctx, audit); err != nil {
		return access.SemanticAttributeAssignment{}, fmt.Errorf("record semantic attribute assignment audit: %w", err)
	}
	return result, nil
}

func semanticAttributeControlAudit(mutation access.SemanticAttributeMutationContext, action string, row access.SemanticAttributeAssignment, state semanticAttributeControlStateRow) access.AuditEventInput {
	metadata, _ := json.Marshal(struct {
		DefinitionID      string `json:"definitionId"`
		DefinitionName    string `json:"definitionName"`
		SubjectKind       string `json:"subjectKind"`
		SubjectID         string `json:"subjectId"`
		DefinitionVersion int64  `json:"definitionVersion"`
		AssignmentVersion int64  `json:"assignmentVersion"`
		ControlRevision   int64  `json:"controlRevision"`
		ValueCount        int    `json:"valueCount"`
		Tombstoned        bool   `json:"tombstoned"`
		ControlDigest     string `json:"controlDigest"`
	}{row.DefinitionID, row.DefinitionName, string(row.Subject.Kind), row.Subject.ID, row.DefinitionVersion, row.AssignmentVersion, state.Revision, len(row.CanonicalValues), row.Tombstoned, state.Digest})
	return access.AuditEventInput{PrincipalID: mutation.ActorPrincipalID, Action: action, ResourceKind: "semantic_attribute_assignment", ResourceID: row.ID, Capability: access.CapabilityProjectAdmin, Status: "success", RequestID: mutation.RequestID, CorrelationID: mutation.CorrelationID, MetadataJSON: string(metadata)}
}
