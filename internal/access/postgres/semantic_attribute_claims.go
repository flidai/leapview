package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/flidai/leapview/internal/access/trustedclaims"
	"github.com/flidai/leapview/internal/semanticvalue"
	"github.com/jackc/pgx/v5"
)

func mappingFromActive(row accessdb.GetActiveTrustedClaimMappingRow) access.TrustedClaimMapping {
	return access.TrustedClaimMapping{ID: row.MappingID, SourceKind: access.TrustedClaimSourceKind(row.SourceKind), Provider: row.Provider, Issuer: row.Issuer, Audience: row.Audience, Claim: row.Claim, DefinitionID: row.DefinitionID, DefinitionName: row.DefinitionName, DefinitionVersion: row.DefinitionVersion, Type: semanticvalue.Type(row.ValueType), Shape: access.SemanticAttributeShape(row.ValueShape), MappingVersion: row.MappingVersion, Tombstoned: textValue(row.TombstonedAt) != "", TombstonedAt: textValue(row.TombstonedAt), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

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
	if input.DefinitionID != "" {
		definition, err = (&Repository{db: db}).SemanticAttributeDefinitionByID(ctx, input.DefinitionID)
	} else if input.DefinitionName != "" {
		definition, err = (&Repository{db: db}).SemanticAttributeDefinition(ctx, input.DefinitionName)
	} else {
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
	queries := accessdb.New(db)
	existing, err := queries.GetActiveTrustedClaimMapping(ctx, accessdb.GetActiveTrustedClaimMappingParams{SourceKind: string(source.Kind), Provider: source.Provider, Issuer: source.Issuer, Audience: source.Audience, Claim: claim, DefinitionID: mustPGUUID(definition.ID)})
	if err == nil {
		current := mappingFromActive(existing)
		if requestedID != "" && requestedID != current.ID {
			return access.TrustedClaimMapping{}, access.AuditEventInput{}, fmt.Errorf("%w: mapping identity does not match the source and claim", access.ErrSemanticAttributeMappingConflict)
		}
		if input.ExpectedVersion != current.MappingVersion {
			return access.TrustedClaimMapping{}, access.AuditEventInput{}, fmt.Errorf("%w: expected %d, current %d", access.ErrSemanticAttributeMappingConflict, input.ExpectedVersion, current.MappingVersion)
		}
		return current, semanticAttributeMappingAudit(input.Mutation, "semantic_attribute.claim_mapping.replay", current, state), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	if input.ExpectedVersion != 0 {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, fmt.Errorf("%w: mapping does not exist, expected %d", access.ErrSemanticAttributeMappingConflict, input.ExpectedVersion)
	}
	id, err := newUUID()
	if err != nil {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	row, err := queries.InsertTrustedClaimMapping(ctx, accessdb.InsertTrustedClaimMappingParams{MappingID: mustPGUUID(id), SourceKind: string(source.Kind), Provider: source.Provider, Issuer: source.Issuer, Audience: source.Audience, Claim: claim, DefinitionID: mustPGUUID(definition.ID), DefinitionVersion: definition.DefinitionVersion, ValueType: string(definition.Type), ValueShape: string(definition.Shape)})
	if err != nil {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	result := mappingFromInsert(row, definition.Name)
	next, err := advanceSemanticAttributeControl(ctx, db, state)
	if err != nil {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	return result, semanticAttributeMappingAudit(input.Mutation, "semantic_attribute.claim_mapping.set", result, next), nil
}

func (r *Repository) SetTrustedClaimMapping(ctx context.Context, input access.TrustedClaimMappingInput) (result access.TrustedClaimMapping, err error) {
	err = r.RunAuditedMutation(ctx, func(repo access.Repository) (access.AuditEventInput, error) {
		var audit access.AuditEventInput
		var coreErr error
		result, audit, coreErr = repo.(*Repository).setTrustedClaimMappingCore(ctx, repo.(*Repository).db, input)
		return audit, coreErr
	})
	return result, err
}

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
		var coreErr error
		result, audit, coreErr = tombstoneTrustedClaimMappingCore(ctx, repo.(*Repository).db, id, expected, mutation)
		return audit, coreErr
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
	row, err := accessdb.New(db).TombstoneTrustedClaimMapping(ctx, accessdb.TombstoneTrustedClaimMappingParams{MappingID: mustPGUUID(canonicalID), ExpectedVersion: expected})
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
	result := mappingFromTombstone(row, definition.Name)
	next, err := advanceSemanticAttributeControl(ctx, db, state)
	if err != nil {
		return access.TrustedClaimMapping{}, access.AuditEventInput{}, err
	}
	return result, semanticAttributeMappingAudit(mutation, "semantic_attribute.claim_mapping.tombstone", result, next), nil
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

// EffectiveDirectSemanticAttributeAssignments resolves only durable direct
// principal/group assignments. It intentionally has no claim argument, so a
// caller cannot smuggle unverified authentication evidence into resolution.
func (r *Repository) EffectiveDirectSemanticAttributeAssignments(ctx context.Context, subject access.SubjectRef) ([]access.EffectiveSemanticAttribute, error) {
	return r.effectiveSemanticAttributeAssignments(ctx, subject, access.TrustedClaimSource{}, nil)
}

// EffectiveSemanticAttributeAssignments accepts only a verifier-gated
// trustedclaims.Envelope. The envelope's exact source identity and claims are
// copied through immutable accessors and its temporal validity is checked at
// the point of authorization use.
func (r *Repository) EffectiveSemanticAttributeAssignments(ctx context.Context, subject access.SubjectRef, envelope trustedclaims.Envelope) ([]access.EffectiveSemanticAttribute, error) {
	if !envelope.Valid() {
		return nil, fmt.Errorf("%w: trusted claim envelope is invalid", trustedclaims.ErrInvalidEvidence)
	}
	now := time.Now().UTC()
	if now.Before(envelope.NotBefore()) {
		return nil, trustedclaims.ErrEvidenceNotYetValid
	}
	if !now.Before(envelope.NotAfter()) {
		return nil, trustedclaims.ErrEvidenceExpired
	}
	source := access.TrustedClaimSource{Kind: access.TrustedClaimSourceKind(envelope.Source()), Provider: envelope.Provider(), Issuer: envelope.Issuer(), Audience: envelope.Audience()}
	if !source.Kind.Valid() {
		return nil, fmt.Errorf("%w: unsupported envelope source", trustedclaims.ErrInvalidEvidence)
	}
	return r.effectiveSemanticAttributeAssignments(ctx, subject, source, envelope.Claims())
}

func (r *Repository) effectiveSemanticAttributeAssignments(ctx context.Context, subject access.SubjectRef, source access.TrustedClaimSource, claims []trustedclaims.Claim) ([]access.EffectiveSemanticAttribute, error) {
	if err := access.ValidateSemanticAttributeSubject(subject); err != nil {
		return nil, err
	}
	canonicalSubjectID, err := uuidID("semantic attribute subject id", subject.ID)
	if err != nil {
		return nil, err
	}
	subject.ID = canonicalSubjectID
	if source.Kind != "" {
		source, err = canonicalTrustedClaimSource(source)
		if err != nil {
			return nil, err
		}
	}
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	controlBefore, err := readSemanticAttributeControlState(ctx, db)
	if err != nil {
		return nil, err
	}
	registryBefore, err := r.SemanticAttributeRegistry(ctx)
	if err != nil {
		return nil, err
	}
	assignments, err := r.SemanticAttributeAssignments(ctx, access.SemanticAttributeAssignmentFilter{Subject: subject})
	if err != nil {
		return nil, err
	}
	if subject.Kind == access.SubjectKindPrincipal {
		groups, queryErr := accessdb.New(db).ListPrincipalSemanticAttributeGroups(ctx, mustPGUUID(subject.ID))
		if queryErr != nil {
			return nil, queryErr
		}
		for _, groupID := range groups {
			groupAssignments, listErr := r.SemanticAttributeAssignments(ctx, access.SemanticAttributeAssignmentFilter{Subject: access.SubjectRef{Kind: access.SubjectKindGroup, ID: groupID}})
			if listErr != nil {
				return nil, listErr
			}
			assignments = append(assignments, groupAssignments...)
		}
	}
	byDefinition := make(map[string]access.EffectiveSemanticAttribute)
	for _, assignment := range assignments {
		if assignment.Tombstoned {
			continue
		}
		definition, defErr := r.SemanticAttributeDefinitionByID(ctx, assignment.DefinitionID)
		if defErr != nil {
			return nil, defErr
		}
		if !definition.Enabled || definition.Type != assignment.Type || definition.Shape != assignment.Shape {
			return nil, fmt.Errorf("%w: assignment %s no longer matches its active definition", access.ErrSemanticAttributeSourceConflict, assignment.ID)
		}
		candidate := access.EffectiveSemanticAttribute{DefinitionID: assignment.DefinitionID, DefinitionName: definition.Name, DefinitionVersion: definition.DefinitionVersion, Type: definition.Type, Shape: definition.Shape, CanonicalValues: append([]string(nil), assignment.CanonicalValues...), ValueDigest: assignment.ValueDigest, Source: "direct"}
		if prior, ok := byDefinition[assignment.DefinitionID]; ok {
			if prior.ValueDigest != candidate.ValueDigest || !reflect.DeepEqual(prior.CanonicalValues, candidate.CanonicalValues) {
				return nil, fmt.Errorf("%w: definition %s has %s and %s values", access.ErrSemanticAttributeSourceConflict, assignment.DefinitionID, prior.Source, candidate.Source)
			}
			prior.Source = "direct"
			byDefinition[assignment.DefinitionID] = prior
		} else {
			byDefinition[assignment.DefinitionID] = candidate
		}
	}
	if source.Kind != "" {
		mappings, err := r.TrustedClaimMappings(ctx, access.TrustedClaimMappingFilter{SourceKind: source.Kind, Provider: source.Provider, Issuer: source.Issuer, Audience: source.Audience})
		if err != nil {
			return nil, err
		}
		for _, mapping := range mappings {
			var found bool
			var raw any
			for _, claim := range claims {
				if claim.Name == mapping.Claim {
					if found {
						return nil, fmt.Errorf("%w: claim %s is repeated", access.ErrSemanticAttributeSourceConflict, mapping.Claim)
					}
					found, raw = true, claim.Value
				}
			}
			if !found {
				continue
			}
			definition, defErr := r.SemanticAttributeDefinitionByID(ctx, mapping.DefinitionID)
			if defErr != nil {
				return nil, defErr
			}
			if !definition.Enabled || definition.Type != mapping.Type || definition.Shape != mapping.Shape {
				return nil, fmt.Errorf("%w: trusted mapping %s no longer matches its active definition", access.ErrSemanticAttributeSourceConflict, mapping.ID)
			}
			values, digest, valueErr := access.CanonicalSemanticAttributeValues(definition, raw)
			if valueErr != nil {
				return nil, valueErr
			}
			candidate := access.EffectiveSemanticAttribute{DefinitionID: mapping.DefinitionID, DefinitionName: definition.Name, DefinitionVersion: definition.DefinitionVersion, Type: definition.Type, Shape: definition.Shape, CanonicalValues: values, ValueDigest: digest, Source: "trusted_claim"}
			if prior, ok := byDefinition[mapping.DefinitionID]; ok {
				if prior.ValueDigest != candidate.ValueDigest || !reflect.DeepEqual(prior.CanonicalValues, candidate.CanonicalValues) {
					return nil, fmt.Errorf("%w: definition %s has %s and trusted_claim values", access.ErrSemanticAttributeSourceConflict, mapping.DefinitionID, prior.Source)
				}
				if prior.Source == "direct" {
					prior.Source = "direct+trusted_claim"
				}
				byDefinition[mapping.DefinitionID] = prior
			} else {
				byDefinition[mapping.DefinitionID] = candidate
			}
		}
	}
	controlAfter, err := readSemanticAttributeControlState(ctx, db)
	if err != nil {
		return nil, err
	}
	if controlBefore.Revision != controlAfter.Revision || controlBefore.Digest != controlAfter.Digest {
		return nil, fmt.Errorf("%w: control state changed during effective resolution", access.ErrSemanticAttributeSourceConflict)
	}
	registryAfter, err := r.SemanticAttributeRegistry(ctx)
	if err != nil {
		return nil, err
	}
	if registryBefore.State.Revision != registryAfter.State.Revision || registryBefore.State.Digest != registryAfter.State.Digest {
		return nil, fmt.Errorf("%w: definition registry changed during effective resolution", access.ErrSemanticAttributeSourceConflict)
	}
	result := make([]access.EffectiveSemanticAttribute, 0, len(byDefinition))
	for _, value := range byDefinition {
		value.CanonicalValues = append([]string(nil), value.CanonicalValues...)
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].DefinitionID < result[j].DefinitionID })
	return result, nil
}
