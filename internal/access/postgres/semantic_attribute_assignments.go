package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/flidai/leapview/internal/semanticvalue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

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
	if input.DefinitionID != "" {
		definition, err = (&Repository{db: db}).SemanticAttributeDefinitionByID(ctx, input.DefinitionID)
	} else if input.DefinitionName != "" {
		definition, err = (&Repository{db: db}).SemanticAttributeDefinition(ctx, input.DefinitionName)
	} else {
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
	queries := accessdb.New(db)
	existing, err := queries.GetActiveSemanticAttributeAssignment(ctx, accessdb.GetActiveSemanticAttributeAssignmentParams{DefinitionID: mustPGUUID(definition.ID), SubjectKind: string(input.Subject.Kind), SubjectID: mustPGUUID(input.Subject.ID)})
	if err == nil {
		current := assignmentFromActive(existing)
		if requestedID != "" && requestedID != current.ID {
			return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, fmt.Errorf("%w: assignment identity does not match the definition and subject", access.ErrSemanticAttributeAssignmentConflict)
		}
		if input.ExpectedVersion != current.AssignmentVersion {
			return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, fmt.Errorf("%w: expected %d, current %d", access.ErrSemanticAttributeAssignmentConflict, input.ExpectedVersion, current.AssignmentVersion)
		}
		if current.ValueDigest == digest && reflect.DeepEqual(current.CanonicalValues, values) {
			return current, semanticAttributeControlAudit(input.Mutation, access.SemanticAttributeAuditActionAssignmentReplay, current, state), nil
		}
		updated, updateErr := queries.UpdateSemanticAttributeAssignment(ctx, accessdb.UpdateSemanticAttributeAssignmentParams{CanonicalValues: values, ValueDigest: digest, AssignmentID: mustPGUUID(current.ID), ExpectedVersion: input.ExpectedVersion})
		if updateErr != nil {
			if errors.Is(updateErr, pgx.ErrNoRows) {
				return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, fmt.Errorf("%w: expected %d", access.ErrSemanticAttributeAssignmentConflict, input.ExpectedVersion)
			}
			return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, updateErr
		}
		result := assignmentFromUpdate(updated, definition.Name)
		next, advanceErr := advanceSemanticAttributeControl(ctx, db, state)
		if advanceErr != nil {
			return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, advanceErr
		}
		return result, semanticAttributeControlAudit(input.Mutation, access.SemanticAttributeAuditActionAssignmentSet, result, next), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	if input.ExpectedVersion != 0 {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, fmt.Errorf("%w: assignment does not exist, expected %d", access.ErrSemanticAttributeAssignmentConflict, input.ExpectedVersion)
	}
	id, err := newUUID()
	if err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	inserted, err := queries.InsertSemanticAttributeAssignment(ctx, accessdb.InsertSemanticAttributeAssignmentParams{AssignmentID: mustPGUUID(id), DefinitionID: mustPGUUID(definition.ID), SubjectKind: string(input.Subject.Kind), SubjectID: mustPGUUID(input.Subject.ID), DefinitionVersion: definition.DefinitionVersion, ValueType: string(definition.Type), ValueShape: string(definition.Shape), CanonicalValues: values, ValueDigest: digest})
	if err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	result := assignmentFromInsert(inserted, definition.Name)
	next, err := advanceSemanticAttributeControl(ctx, db, state)
	if err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	return result, semanticAttributeControlAudit(input.Mutation, access.SemanticAttributeAuditActionAssignmentSet, result, next), nil
}

func assignmentFromActive(row accessdb.GetActiveSemanticAttributeAssignmentRow) access.SemanticAttributeAssignment {
	return access.SemanticAttributeAssignment{ID: row.AssignmentID, DefinitionID: row.DefinitionID, DefinitionName: row.DefinitionName, DefinitionVersion: row.DefinitionVersion, Type: semanticvalue.Type(row.ValueType), Shape: access.SemanticAttributeShape(row.ValueShape), Subject: access.SubjectRef{Kind: access.SubjectKind(row.SubjectKind), ID: row.SubjectID}, CanonicalValues: append([]string(nil), row.CanonicalValues...), ValueDigest: row.ValueDigest, AssignmentVersion: row.AssignmentVersion, Tombstoned: textValue(row.TombstonedAt) != "", TombstonedAt: timestampAPIValue(row.TombstonedAt), CreatedAt: timestampAPIValue(row.CreatedAt), UpdatedAt: timestampAPIValue(row.UpdatedAt)}
}

func mustPGUUID(value string) pgtype.UUID {
	parsed, _ := pgUUID(value)
	return parsed
}

func (r *Repository) SetSemanticAttributeAssignment(ctx context.Context, input access.SemanticAttributeAssignmentInput) (result access.SemanticAttributeAssignment, err error) {
	err = r.RunAuditedMutation(ctx, func(repo access.Repository) (access.AuditEventInput, error) {
		var audit access.AuditEventInput
		var coreErr error
		result, audit, coreErr = repo.(*Repository).setSemanticAttributeAssignmentCore(ctx, repo.(*Repository).db, input)
		return audit, coreErr
	})
	return result, err
}

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
		var coreErr error
		result, audit, coreErr = tombstoneSemanticAttributeAssignmentCore(ctx, repo.(*Repository).db, id, expected, mutation)
		return audit, coreErr
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
	row, err := accessdb.New(db).TombstoneSemanticAttributeAssignment(ctx, accessdb.TombstoneSemanticAttributeAssignmentParams{AssignmentID: mustPGUUID(canonicalID), ExpectedVersion: expected})
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
	result := assignmentFromTombstone(row, definition.Name)
	next, err := advanceSemanticAttributeControl(ctx, db, state)
	if err != nil {
		return access.SemanticAttributeAssignment{}, access.AuditEventInput{}, err
	}
	return result, semanticAttributeControlAudit(mutation, access.SemanticAttributeAuditActionAssignmentTombstone, result, next), nil
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
