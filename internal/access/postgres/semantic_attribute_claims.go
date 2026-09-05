package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	"github.com/jackc/pgx/v5"
)

const semanticAttributeMappingReturning = `
	mapping_id::text, source_kind, provider, issuer, audience, claim,
	definition_id::text, ''::text, definition_version, value_type, value_shape,
	mapping_version, tombstoned_at, created_at, updated_at`

func (r *Repository) setTrustedClaimMappingCore(ctx context.Context, db DBTX, input access.TrustedClaimMappingInput) (access.TrustedClaimMapping, access.AuditEventInput, error) {
	source, claim, err := canonicalTrustedClaim(access.TrustedClaimSource{Kind: input.SourceKind, Provider: input.Provider, Issuer: input.Issuer, Audience: input.Audience}, input.Claim)
	if err != nil {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	if input.ExpectedVersion < 0 {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, errors.New("trusted claim mapping expected version cannot be negative")
	}
	requestedID := ""
	if input.MappingID != "" {
		requestedID, err = uuidID("trusted claim mapping id", input.MappingID)
		if err != nil {
			return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
		}
	}
	var definition access.SemanticAttributeDefinition
	switch {
	case input.DefinitionID != "":
		definition, err = (&Repository{db: db}).SemanticAttributeDefinitionByID(ctx, input.DefinitionID)
	case input.DefinitionName != "":
		definition, err = (&Repository{db: db}).SemanticAttributeDefinition(ctx, input.DefinitionName)
	default:
		err = errors.New("trusted claim mapping definition id or name is required")
	}
	if err != nil {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	state, err := lockSemanticAttributeControlState(ctx, db)
	if err != nil {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	if _, _, err := validateSemanticAttributeControlState(ctx, db, state); err != nil {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}

	replaceStale := false
	var current access.TrustedClaimMapping
	current, err = scanSemanticAttributeControlMapping(db.QueryRow(ctx, `SELECT `+semanticAttributeControlMappingColumns+`
		FROM access.semantic_attribute_claim_mapping m
		JOIN access.semantic_attribute_definition d ON d.definition_id = m.definition_id
		WHERE m.source_kind = $1::text AND m.provider = $2::text AND m.issuer = $3::text
		  AND m.audience = $4::text AND m.claim = $5::text
		  AND m.definition_id = $6::uuid AND m.tombstoned_at IS NULL`,
		source.Kind, source.Provider, source.Issuer, source.Audience, claim, definition.ID))
	if err == nil {
		if requestedID != "" && requestedID != current.ID {
			return access.TrustedClaimMapping{}, access.AuditEventInput{}, fmt.Errorf("%w: mapping identity does not match the source and claim", access.ErrSemanticAttributeMappingConflict)
		}
		if input.ExpectedVersion != current.MappingVersion {
			return access.TrustedClaimMapping{}, access.AuditEventInput{}, fmt.Errorf("%w: expected %d, current %d", access.ErrSemanticAttributeMappingConflict, input.ExpectedVersion, current.MappingVersion)
		}
		if current.DefinitionVersion != definition.DefinitionVersion {
			// Definition changes create a new mapping incarnation. Keep the
			// stale row for history, but release the active identity key before
			// inserting the current-definition mapping below.
			commandTag, tombstoneErr := db.Exec(ctx, `UPDATE access.semantic_attribute_claim_mapping
				SET tombstoned_at = clock_timestamp(), mapping_version = mapping_version + 1
				WHERE mapping_id = $1::uuid AND mapping_version = $2::bigint AND tombstoned_at IS NULL`, current.ID, input.ExpectedVersion)
			if tombstoneErr != nil {
				return access.TrustedClaimMapping{}, access.AuditEventInput{}, tombstoneErr
			}
			if commandTag.RowsAffected() != 1 {
				return access.TrustedClaimMapping{}, access.AuditEventInput{}, fmt.Errorf("%w: expected %d", access.ErrSemanticAttributeMappingConflict, input.ExpectedVersion)
			}
			replaceStale = true
		} else {
			return current, semanticAttributeMappingAudit(input.Mutation, "semantic_attribute.claim_mapping.replay", current, state), nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	if !replaceStale && input.ExpectedVersion != 0 {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, fmt.Errorf("%w: mapping does not exist, expected %d", access.ErrSemanticAttributeMappingConflict, input.ExpectedVersion)
	}
	mappingID, err := newUUID()
	if err != nil {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	inserted, err := scanSemanticAttributeControlMapping(db.QueryRow(ctx, `INSERT INTO access.semantic_attribute_claim_mapping
		(mapping_id, source_kind, provider, issuer, audience, claim, definition_id,
		 definition_version, value_type, value_shape)
		VALUES ($1::uuid, $2::text, $3::text, $4::text, $5::text, $6::text, $7::uuid,
				$8::bigint, $9::text, $10::text)
		RETURNING `+semanticAttributeMappingReturning, mappingID, source.Kind, source.Provider, source.Issuer,
		source.Audience, claim, definition.ID, definition.DefinitionVersion, definition.Type, definition.Shape))
	if err != nil {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	inserted.DefinitionName = definition.Name
	next, err := advanceSemanticAttributeControl(ctx, db, state)
	if err != nil {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	return inserted, semanticAttributeMappingAudit(input.Mutation, "semantic_attribute.claim_mapping.set", inserted, next), nil
}

func (r *Repository) SetTrustedClaimMapping(ctx context.Context, input access.TrustedClaimMappingInput) (result access.TrustedClaimMapping, err error) {
	err = r.RunAuditedMutation(ctx, func(repo access.Repository) (access.AuditEventInput, error) {
		var audit access.AuditEventInput
		result, audit, err = repo.(*Repository).setTrustedClaimMappingCore(ctx, repo.(*Repository).db, input)
		return audit, err
	})
	return result, err
}

// SetTrustedClaimMappingTx composes mapping and audit insertion into an
// existing caller-owned pgx transaction without taking transaction ownership.
func SetTrustedClaimMappingTx(ctx context.Context, tx Tx, input access.TrustedClaimMappingInput) (access.TrustedClaimMapping, error) {
	if tx == nil {
		return access.TrustedClaimMapping{}, errors.New("trusted claim mapping PostgreSQL transaction is required")
	}
	result, audit, err := (&Repository{db: tx}).setTrustedClaimMappingCore(ctx, tx, input)
	if err != nil {
		return access.TrustedClaimMapping{}, err
	}
	if err := (&Repository{db: tx}).RecordAuditEvent(ctx, audit); err != nil {
		return access.TrustedClaimMapping{}, fmt.Errorf("record trusted claim mapping audit: %w", err)
	}
	return result, nil
}

func (r *Repository) TombstoneTrustedClaimMapping(ctx context.Context, id string, expected int64, mutation access.SemanticAttributeMutationContext) (result access.TrustedClaimMapping, err error) {
	err = r.RunAuditedMutation(ctx, func(repo access.Repository) (access.AuditEventInput, error) {
		var audit access.AuditEventInput
		result, audit, err = tombstoneTrustedClaimMappingCore(ctx, repo.(*Repository).db, id, expected, mutation)
		return audit, err
	})
	return result, err
}

func tombstoneTrustedClaimMappingCore(ctx context.Context, db DBTX, id string, expected int64, mutation access.SemanticAttributeMutationContext) (access.TrustedClaimMapping, access.AuditEventInput, error) {
	if expected <= 0 {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, errors.New("trusted claim mapping expected version must be positive")
	}
	canonicalID, err := uuidID("trusted claim mapping id", id)
	if err != nil {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	state, err := lockSemanticAttributeControlState(ctx, db)
	if err != nil {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	if _, _, err := validateSemanticAttributeControlState(ctx, db, state); err != nil {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	row, err := scanSemanticAttributeControlMapping(db.QueryRow(ctx, `UPDATE access.semantic_attribute_claim_mapping
		SET tombstoned_at = clock_timestamp(), mapping_version = mapping_version + 1
		WHERE mapping_id = $1::uuid AND mapping_version = $2::bigint AND tombstoned_at IS NULL
		RETURNING `+semanticAttributeMappingReturning, canonicalID, expected))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return access.TrustedClaimMapping{}, access.AuditEventInput{}, fmt.Errorf("%w: expected %d", access.ErrSemanticAttributeMappingConflict, expected)
		}
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	definition, err := (&Repository{db: db}).SemanticAttributeDefinitionByID(ctx, row.DefinitionID)
	if err != nil {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	row.DefinitionName = definition.Name
	next, err := advanceSemanticAttributeControl(ctx, db, state)
	if err != nil {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	return row, semanticAttributeMappingAudit(mutation, "semantic_attribute.claim_mapping.tombstone", row, next), nil
}

func TombstoneTrustedClaimMappingTx(ctx context.Context, tx Tx, id string, expected int64, mutation access.SemanticAttributeMutationContext) (access.TrustedClaimMapping, error) {
	if tx == nil {
		return access.TrustedClaimMapping{}, errors.New("trusted claim mapping PostgreSQL transaction is required")
	}
	result, audit, err := tombstoneTrustedClaimMappingCore(ctx, tx, id, expected, mutation)
	if err != nil {
		return access.TrustedClaimMapping{}, err
	}
	if err := (&Repository{db: tx}).RecordAuditEvent(ctx, audit); err != nil {
		return access.TrustedClaimMapping{}, fmt.Errorf("record trusted claim mapping audit: %w", err)
	}
	return result, nil
}

func semanticAttributeMappingAudit(mutation access.SemanticAttributeMutationContext, action string, row access.TrustedClaimMapping, state semanticAttributeControlStateRow) access.AuditEventInput {
	metadata, _ := json.Marshal(struct {
		SourceKind        string `json:"sourceKind"`
		Provider          string `json:"provider"`
		Issuer            string `json:"issuer"`
		Audience          string `json:"audience"`
		Claim             string `json:"claim"`
		DefinitionID      string `json:"definitionId"`
		DefinitionName    string `json:"definitionName"`
		DefinitionVersion int64  `json:"definitionVersion"`
		MappingVersion    int64  `json:"mappingVersion"`
		ControlRevision   int64  `json:"controlRevision"`
		Tombstoned        bool   `json:"tombstoned"`
		ControlDigest     string `json:"controlDigest"`
	}{string(row.SourceKind), row.Provider, row.Issuer, row.Audience, row.Claim, row.DefinitionID, row.DefinitionName, row.DefinitionVersion, row.MappingVersion, state.Revision, row.Tombstoned, state.Digest})
	return access.AuditEventInput{PrincipalID: mutation.ActorPrincipalID, Action: action, ResourceKind: "semantic_attribute_claim_mapping", ResourceID: row.ID, Capability: access.CapabilityProjectAdmin, Status: "success", RequestID: mutation.RequestID, CorrelationID: mutation.CorrelationID, MetadataJSON: string(metadata)}
}
