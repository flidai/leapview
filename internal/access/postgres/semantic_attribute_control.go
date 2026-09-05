package postgres

// Durable semantic-access control state. Assignments and trusted-claim
// mutations live in focused sibling files; this file owns shared projections,
// deterministic control identity, and read-only resolution.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/trustedclaims"
	"github.com/flidai/leapview/internal/semanticvalue"
)

const semanticAttributeControlProfile = semanticvalue.Profile

type semanticAttributeControlStateRow struct {
	Profile, Digest, UpdatedAt string
	Revision                   int64
}

// These wire structs intentionally keep their historical field names. Their
// JSON representation is the persisted control identity and must not drift.
type semanticAttributeAssignmentDigestWire struct {
	ID, DefinitionID, SubjectKind, SubjectID string
	DefinitionVersion                        int64
	Type                                     semanticvalue.Type
	Shape                                    access.SemanticAttributeShape
	Values                                   []string
	ValueDigest                              string
	Version                                  int64
	TombstonedAtMicros                       int64
}

type trustedClaimMappingDigestWire struct {
	ID, SourceKind, Provider, Issuer, Audience, Claim, DefinitionID string
	DefinitionVersion                                               int64
	Type                                                            semanticvalue.Type
	Shape                                                           access.SemanticAttributeShape
	Version                                                         int64
	TombstonedAtMicros                                              int64
}

type semanticAttributeControlDigestWire struct {
	Profile     string                                  `json:"profile"`
	Assignments []semanticAttributeAssignmentDigestWire `json:"assignments"`
	Mappings    []trustedClaimMappingDigestWire         `json:"mappings"`
}

type semanticAttributeControlAssignmentRow struct {
	ID, DefinitionID, DefinitionName, SubjectKind, SubjectID string
	DefinitionVersion                                        int64
	ValueType, ValueShape                                    string
	CanonicalValues                                          []string
	ValueDigest                                              string
	AssignmentVersion                                        int64
	TombstonedAt, CreatedAt, UpdatedAt                       *time.Time
}

type semanticAttributeControlMappingRow struct {
	ID, SourceKind, Provider, Issuer, Audience, Claim string
	DefinitionID, DefinitionName                      string
	DefinitionVersion                                 int64
	ValueType, ValueShape                             string
	MappingVersion                                    int64
	TombstonedAt, CreatedAt, UpdatedAt                *time.Time
}

func assignmentFromControlRow(row semanticAttributeControlAssignmentRow) access.SemanticAttributeAssignment {
	return access.SemanticAttributeAssignment{
		ID: row.ID, DefinitionID: row.DefinitionID, DefinitionName: row.DefinitionName,
		DefinitionVersion: row.DefinitionVersion, Type: semanticvalue.Type(row.ValueType),
		Shape:           access.SemanticAttributeShape(row.ValueShape),
		Subject:         access.SubjectRef{Kind: access.SubjectKind(row.SubjectKind), ID: row.SubjectID},
		CanonicalValues: append([]string(nil), row.CanonicalValues...), ValueDigest: row.ValueDigest,
		AssignmentVersion: row.AssignmentVersion, Tombstoned: row.TombstonedAt != nil,
		TombstonedAt: formatTimePtr(row.TombstonedAt), CreatedAt: formatTimePtr(row.CreatedAt),
		UpdatedAt: formatTimePtr(row.UpdatedAt),
	}
}

func mappingFromControlRow(row semanticAttributeControlMappingRow) access.TrustedClaimMapping {
	return access.TrustedClaimMapping{
		ID: row.ID, SourceKind: access.TrustedClaimSourceKind(row.SourceKind), Provider: row.Provider,
		Issuer: row.Issuer, Audience: row.Audience, Claim: row.Claim,
		DefinitionID: row.DefinitionID, DefinitionName: row.DefinitionName,
		DefinitionVersion: row.DefinitionVersion, Type: semanticvalue.Type(row.ValueType),
		Shape: access.SemanticAttributeShape(row.ValueShape), MappingVersion: row.MappingVersion,
		Tombstoned: row.TombstonedAt != nil, TombstonedAt: formatTimePtr(row.TombstonedAt),
		CreatedAt: formatTimePtr(row.CreatedAt), UpdatedAt: formatTimePtr(row.UpdatedAt),
	}
}

const semanticAttributeControlAssignmentColumns = `
a.assignment_id::text, a.definition_id::text, d.name, a.subject_kind,
a.subject_id::text, a.definition_version, a.value_type, a.value_shape,
a.canonical_values, a.value_digest, a.assignment_version, a.tombstoned_at,
a.created_at, a.updated_at`

const semanticAttributeControlMappingColumns = `
m.mapping_id::text, m.source_kind, m.provider, m.issuer, m.audience, m.claim,
m.definition_id::text, d.name, m.definition_version, m.value_type, m.value_shape,
m.mapping_version, m.tombstoned_at, m.created_at, m.updated_at`

func scanSemanticAttributeControlAssignment(row interface{ Scan(...any) error }) (access.SemanticAttributeAssignment, error) {
	var value semanticAttributeControlAssignmentRow
	if err := row.Scan(&value.ID, &value.DefinitionID, &value.DefinitionName, &value.SubjectKind,
		&value.SubjectID, &value.DefinitionVersion, &value.ValueType, &value.ValueShape,
		&value.CanonicalValues, &value.ValueDigest, &value.AssignmentVersion,
		&value.TombstonedAt, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return access.SemanticAttributeAssignment{}, err
	}
	return assignmentFromControlRow(value), nil
}

func scanSemanticAttributeControlMapping(row interface{ Scan(...any) error }) (access.TrustedClaimMapping, error) {
	var value semanticAttributeControlMappingRow
	if err := row.Scan(&value.ID, &value.SourceKind, &value.Provider, &value.Issuer, &value.Audience,
		&value.Claim, &value.DefinitionID, &value.DefinitionName, &value.DefinitionVersion,
		&value.ValueType, &value.ValueShape, &value.MappingVersion, &value.TombstonedAt,
		&value.CreatedAt, &value.UpdatedAt); err != nil {
		return access.TrustedClaimMapping{}, err
	}
	return mappingFromControlRow(value), nil
}

func controlStateFromRow(row interface{ Scan(...any) error }) (semanticAttributeControlStateRow, error) {
	var state semanticAttributeControlStateRow
	var updated time.Time
	if err := row.Scan(&state.Profile, &state.Revision, &state.Digest, &updated); err != nil {
		return semanticAttributeControlStateRow{}, err
	}
	state.UpdatedAt = formatTime(updated)
	return state, nil
}

func lockSemanticAttributeControlState(ctx context.Context, db DBTX) (semanticAttributeControlStateRow, error) {
	state, err := controlStateFromRow(db.QueryRow(ctx, `
		SELECT profile, control_revision, control_digest, updated_at
		FROM access.semantic_attribute_control_state
		WHERE singleton FOR UPDATE`))
	if err != nil {
		return semanticAttributeControlStateRow{}, fmt.Errorf("lock semantic attribute control state: %w", err)
	}
	return state, nil
}

func readSemanticAttributeControlState(ctx context.Context, db DBTX) (semanticAttributeControlStateRow, error) {
	state, err := controlStateFromRow(db.QueryRow(ctx, `
		SELECT profile, control_revision, control_digest, updated_at
		FROM access.semantic_attribute_control_state
		WHERE singleton`))
	if err != nil {
		return semanticAttributeControlStateRow{}, fmt.Errorf("read semantic attribute control state: %w", err)
	}
	return state, nil
}

func allControlRows(ctx context.Context, db DBTX) ([]access.SemanticAttributeAssignment, []access.TrustedClaimMapping, error) {
	assignmentRows, err := db.Query(ctx, `SELECT `+semanticAttributeControlAssignmentColumns+`
		FROM access.semantic_attribute_assignment a
		JOIN access.semantic_attribute_definition d ON d.definition_id = a.definition_id
		ORDER BY a.definition_id, a.subject_kind, a.subject_id, a.created_at, a.assignment_id`)
	if err != nil {
		return nil, nil, fmt.Errorf("list semantic attribute assignments: %w", err)
	}
	assignments := make([]access.SemanticAttributeAssignment, 0)
	for assignmentRows.Next() {
		row, scanErr := scanSemanticAttributeControlAssignment(assignmentRows)
		if scanErr != nil {
			assignmentRows.Close()
			return nil, nil, fmt.Errorf("scan semantic attribute assignment: %w", scanErr)
		}
		assignments = append(assignments, row)
	}
	if err := assignmentRows.Err(); err != nil {
		assignmentRows.Close()
		return nil, nil, fmt.Errorf("iterate semantic attribute assignments: %w", err)
	}
	assignmentRows.Close()

	mappingRows, err := db.Query(ctx, `SELECT `+semanticAttributeControlMappingColumns+`
		FROM access.semantic_attribute_claim_mapping m
		JOIN access.semantic_attribute_definition d ON d.definition_id = m.definition_id
		ORDER BY m.source_kind, m.provider, m.issuer, m.audience, m.claim,
		         m.definition_id, m.created_at, m.mapping_id`)
	if err != nil {
		return nil, nil, fmt.Errorf("list trusted claim mappings: %w", err)
	}
	mappings := make([]access.TrustedClaimMapping, 0)
	for mappingRows.Next() {
		row, scanErr := scanSemanticAttributeControlMapping(mappingRows)
		if scanErr != nil {
			mappingRows.Close()
			return nil, nil, fmt.Errorf("scan trusted claim mapping: %w", scanErr)
		}
		mappings = append(mappings, row)
	}
	if err := mappingRows.Err(); err != nil {
		mappingRows.Close()
		return nil, nil, fmt.Errorf("iterate trusted claim mappings: %w", err)
	}
	mappingRows.Close()
	return assignments, mappings, nil
}

func timestampMicroseconds(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	if micros, err := strconv.ParseInt(value, 10, 64); err == nil {
		return micros, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0, fmt.Errorf("invalid timestamp %q: %w", value, err)
	}
	return parsed.UTC().UnixMicro(), nil
}

func semanticAttributeControlDigest(assignments []access.SemanticAttributeAssignment, mappings []access.TrustedClaimMapping) (string, error) {
	assignments = append([]access.SemanticAttributeAssignment(nil), assignments...)
	mappings = append([]access.TrustedClaimMapping(nil), mappings...)
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].ID < assignments[j].ID })
	sort.Slice(mappings, func(i, j int) bool { return mappings[i].ID < mappings[j].ID })
	wire := semanticAttributeControlDigestWire{
		Profile:     semanticAttributeControlProfile,
		Assignments: make([]semanticAttributeAssignmentDigestWire, len(assignments)),
		Mappings:    make([]trustedClaimMappingDigestWire, len(mappings)),
	}
	for i, row := range assignments {
		tombstonedAt, err := timestampMicroseconds(row.TombstonedAt)
		if err != nil {
			return "", fmt.Errorf("assignment %s tombstoned timestamp: %w", row.ID, err)
		}
		wire.Assignments[i] = semanticAttributeAssignmentDigestWire{
			ID: row.ID, DefinitionID: row.DefinitionID, SubjectKind: string(row.Subject.Kind),
			SubjectID: row.Subject.ID, DefinitionVersion: row.DefinitionVersion, Type: row.Type,
			Shape: row.Shape, Values: append([]string(nil), row.CanonicalValues...), ValueDigest: row.ValueDigest,
			Version: row.AssignmentVersion, TombstonedAtMicros: tombstonedAt,
		}
	}
	for i, row := range mappings {
		tombstonedAt, err := timestampMicroseconds(row.TombstonedAt)
		if err != nil {
			return "", fmt.Errorf("mapping %s tombstoned timestamp: %w", row.ID, err)
		}
		wire.Mappings[i] = trustedClaimMappingDigestWire{
			ID: row.ID, SourceKind: string(row.SourceKind), Provider: row.Provider, Issuer: row.Issuer,
			Audience: row.Audience, Claim: row.Claim, DefinitionID: row.DefinitionID,
			DefinitionVersion: row.DefinitionVersion, Type: row.Type, Shape: row.Shape,
			Version: row.MappingVersion, TombstonedAtMicros: tombstonedAt,
		}
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return "", fmt.Errorf("encode semantic attribute control digest: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validateSemanticAttributeControlState(ctx context.Context, db DBTX, state semanticAttributeControlStateRow) ([]access.SemanticAttributeAssignment, []access.TrustedClaimMapping, error) {
	assignments, mappings, err := allControlRows(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	digest, err := semanticAttributeControlDigest(assignments, mappings)
	if err != nil {
		return nil, nil, err
	}
	if state.Profile != semanticAttributeControlProfile || state.Digest != digest {
		return nil, nil, fmt.Errorf("%w: stored digest %q, computed %q", access.ErrSemanticAttributeControlCorrupt, state.Digest, digest)
	}
	return assignments, mappings, nil
}

func advanceSemanticAttributeControl(ctx context.Context, db DBTX, current semanticAttributeControlStateRow) (semanticAttributeControlStateRow, error) {
	assignments, mappings, err := allControlRows(ctx, db)
	if err != nil {
		return semanticAttributeControlStateRow{}, err
	}
	digest, err := semanticAttributeControlDigest(assignments, mappings)
	if err != nil {
		return semanticAttributeControlStateRow{}, err
	}
	if digest == current.Digest {
		return current, nil
	}
	state, err := controlStateFromRow(db.QueryRow(ctx, `
		UPDATE access.semantic_attribute_control_state
		SET control_revision = $1::bigint, control_digest = $2::text
		WHERE singleton
		RETURNING profile, control_revision, control_digest, updated_at`, current.Revision+1, digest))
	if err != nil {
		return semanticAttributeControlStateRow{}, fmt.Errorf("advance semantic attribute control state: %w", err)
	}
	return state, nil
}

func (r *Repository) SemanticAttributeControl(ctx context.Context) (access.SemanticAttributeControlSnapshot, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.SemanticAttributeControlSnapshot{}, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		before, err := readSemanticAttributeControlState(ctx, db)
		if err != nil {
			return access.SemanticAttributeControlSnapshot{}, err
		}
		assignments, mappings, err := allControlRows(ctx, db)
		if err != nil {
			return access.SemanticAttributeControlSnapshot{}, err
		}
		after, err := readSemanticAttributeControlState(ctx, db)
		if err != nil {
			return access.SemanticAttributeControlSnapshot{}, err
		}
		if before.Revision != after.Revision || before.Digest != after.Digest {
			continue
		}
		digest, err := semanticAttributeControlDigest(assignments, mappings)
		if err != nil {
			return access.SemanticAttributeControlSnapshot{}, err
		}
		if before.Profile != semanticAttributeControlProfile || before.Digest != digest {
			return access.SemanticAttributeControlSnapshot{}, fmt.Errorf("%w: stored digest %q, computed %q", access.ErrSemanticAttributeControlCorrupt, before.Digest, digest)
		}
		return access.SemanticAttributeControlSnapshot{
			State:       access.SemanticAttributeControlState{Profile: before.Profile, Revision: before.Revision, Digest: before.Digest, UpdatedAt: before.UpdatedAt},
			Assignments: assignments, Mappings: mappings,
		}, nil
	}
	return access.SemanticAttributeControlSnapshot{}, fmt.Errorf("%w: control state changed during read", access.ErrSemanticAttributeControlCorrupt)
}

func (r *Repository) SemanticAttributeAssignments(ctx context.Context, filter access.SemanticAttributeAssignmentFilter) ([]access.SemanticAttributeAssignment, error) {
	definitionID := ""
	if filter.DefinitionID != "" {
		var err error
		definitionID, err = uuidID("semantic attribute definition id", filter.DefinitionID)
		if err != nil {
			return nil, err
		}
	}
	subjectKind, subjectID := "", ""
	if filter.Subject.Kind != "" || filter.Subject.ID != "" {
		if err := access.ValidateSemanticAttributeSubject(filter.Subject); err != nil {
			return nil, err
		}
		var err error
		subjectID, err = uuidID("semantic attribute subject id", filter.Subject.ID)
		if err != nil {
			return nil, err
		}
		subjectKind = string(filter.Subject.Kind)
	}
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `SELECT `+semanticAttributeControlAssignmentColumns+`
		FROM access.semantic_attribute_assignment a
		JOIN access.semantic_attribute_definition d ON d.definition_id = a.definition_id
		WHERE ($1::text = '' OR a.definition_id = $1::uuid)
		  AND ($2::text = '' OR a.subject_kind = $2::text)
		  AND ($3::text = '' OR a.subject_id = $3::uuid)
		  AND ($4::boolean OR a.tombstoned_at IS NULL)
		ORDER BY a.definition_id, a.subject_kind, a.subject_id, a.created_at, a.assignment_id`,
		definitionID, subjectKind, subjectID, filter.IncludeTombstones)
	if err != nil {
		return nil, fmt.Errorf("list semantic attribute assignments: %w", err)
	}
	defer rows.Close()
	result := make([]access.SemanticAttributeAssignment, 0)
	for rows.Next() {
		value, scanErr := scanSemanticAttributeControlAssignment(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan semantic attribute assignment: %w", scanErr)
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func canonicalTrustedClaimSource(source access.TrustedClaimSource) (access.TrustedClaimSource, error) {
	if !source.Kind.Valid() {
		return access.TrustedClaimSource{}, errors.New("trusted claim source kind is invalid")
	}
	if err := trustedclaims.ValidateSourceIdentity(source.Provider, source.Issuer, source.Audience); err != nil {
		return access.TrustedClaimSource{}, fmt.Errorf("trusted claim source identity is invalid: %w", err)
	}
	return source, nil
}

func canonicalTrustedClaim(source access.TrustedClaimSource, claim string) (access.TrustedClaimSource, string, error) {
	canonical, err := canonicalTrustedClaimSource(source)
	if err != nil {
		return access.TrustedClaimSource{}, "", err
	}
	if !validTrustedClaimText(claim, 1024) {
		return access.TrustedClaimSource{}, "", errors.New("trusted claim name is invalid")
	}
	return canonical, claim, nil
}

func validTrustedClaimText(value string, maxBytes int) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func (r *Repository) TrustedClaimMappings(ctx context.Context, filter access.TrustedClaimMappingFilter) ([]access.TrustedClaimMapping, error) {
	source := access.TrustedClaimSource{Kind: filter.SourceKind, Provider: filter.Provider, Issuer: filter.Issuer, Audience: filter.Audience}
	if source.Kind != "" && !source.Kind.Valid() {
		return nil, errors.New("trusted claim source kind is invalid")
	}
	for _, field := range []struct {
		label string
		value string
		check func(string) error
	}{
		{"provider", source.Provider, trustedclaims.ValidateProvider},
		{"issuer", source.Issuer, trustedclaims.ValidateIssuer},
		{"audience", source.Audience, trustedclaims.ValidateAudience},
	} {
		if field.value != "" {
			if err := field.check(field.value); err != nil {
				return nil, fmt.Errorf("trusted claim %s is invalid", field.label)
			}
		}
	}
	if filter.Claim != "" && !validTrustedClaimText(filter.Claim, 1024) {
		return nil, errors.New("trusted claim claim is invalid")
	}
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `SELECT `+semanticAttributeControlMappingColumns+`
		FROM access.semantic_attribute_claim_mapping m
		JOIN access.semantic_attribute_definition d ON d.definition_id = m.definition_id
		WHERE ($1::text = '' OR m.source_kind = $1::text)
		  AND ($2::text = '' OR m.provider = $2::text)
		  AND ($3::text = '' OR m.issuer = $3::text)
		  AND ($4::text = '' OR m.audience = $4::text)
		  AND ($5::text = '' OR m.claim = $5::text)
		  AND ($6::boolean OR m.tombstoned_at IS NULL)
		ORDER BY m.source_kind, m.provider, m.issuer, m.audience, m.claim,
		         m.definition_id, m.created_at, m.mapping_id`,
		source.Kind, source.Provider, source.Issuer, source.Audience, filter.Claim, filter.IncludeTombstones)
	if err != nil {
		return nil, fmt.Errorf("list trusted claim mappings: %w", err)
	}
	defer rows.Close()
	result := make([]access.TrustedClaimMapping, 0)
	for rows.Next() {
		value, scanErr := scanSemanticAttributeControlMapping(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan trusted claim mapping: %w", scanErr)
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

// EffectiveDirectSemanticAttributeAssignments resolves only durable direct
// principal/group assignments. It intentionally has no claim argument, so a
// caller cannot smuggle unverified authentication evidence into resolution.
func (r *Repository) EffectiveDirectSemanticAttributeAssignments(ctx context.Context, subject access.SubjectRef) ([]access.EffectiveSemanticAttribute, error) {
	return r.effectiveSemanticAttributeAssignments(ctx, subject, access.TrustedClaimSource{}, nil)
}

func (r *Repository) EffectiveSemanticAttributeAssignments(ctx context.Context, subject access.SubjectRef, envelope trustedclaims.Envelope) ([]access.EffectiveSemanticAttribute, error) {
	if !envelope.Valid() {
		return nil, fmt.Errorf("%w: trusted claim envelope is invalid", trustedclaims.ErrInvalidEvidence)
	}
	if err := access.ValidateSemanticAttributeSubject(subject); err != nil {
		return nil, err
	}
	if subject.Kind != access.SubjectKindPrincipal {
		return nil, fmt.Errorf("%w: trusted claims require a principal subject", trustedclaims.ErrInvalidEvidence)
	}
	if envelope.Subject() != subject.ID {
		return nil, fmt.Errorf("%w: trusted claim subject does not match requested principal", trustedclaims.ErrInvalidEvidence)
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
	if err := validateSemanticAttributeResolutionSubject(ctx, db, subject); err != nil {
		return nil, err
	}
	controlBefore, err := r.SemanticAttributeControl(ctx)
	if err != nil {
		return nil, err
	}
	registryBefore, err := r.SemanticAttributeRegistry(ctx)
	if err != nil {
		return nil, err
	}
	assignments := make([]access.SemanticAttributeAssignment, 0, len(controlBefore.Assignments))
	for _, assignment := range controlBefore.Assignments {
		if !assignment.Tombstoned && assignment.Subject == subject {
			assignments = append(assignments, assignment)
		}
	}
	if subject.Kind == access.SubjectKindPrincipal {
		groups, queryErr := db.Query(ctx, `
			SELECT pg.group_id::text
			FROM access.principal_group pg
			JOIN access.access_group g ON g.id = pg.group_id
			WHERE pg.principal_id = $1::uuid AND pg.revoked_at IS NULL AND g.revoked_at IS NULL
			ORDER BY pg.group_id`, subject.ID)
		if queryErr != nil {
			return nil, fmt.Errorf("list principal groups for semantic attributes: %w", queryErr)
		}
		groupIDs := make([]string, 0)
		for groups.Next() {
			var groupID string
			if scanErr := groups.Scan(&groupID); scanErr != nil {
				groups.Close()
				return nil, fmt.Errorf("scan principal group for semantic attributes: %w", scanErr)
			}
			groupIDs = append(groupIDs, groupID)
		}
		if err := groups.Err(); err != nil {
			groups.Close()
			return nil, err
		}
		groups.Close()
		for _, groupID := range groupIDs {
			groupSubject := access.SubjectRef{Kind: access.SubjectKindGroup, ID: groupID}
			for _, assignment := range controlBefore.Assignments {
				if !assignment.Tombstoned && assignment.Subject == groupSubject {
					assignments = append(assignments, assignment)
				}
			}
		}
	}
	byDefinition := make(map[string]access.EffectiveSemanticAttribute)
	for _, assignment := range assignments {
		definition, defErr := r.SemanticAttributeDefinitionByID(ctx, assignment.DefinitionID)
		if defErr != nil {
			return nil, defErr
		}
		if !definition.Enabled || definition.DefinitionVersion != assignment.DefinitionVersion || definition.Type != assignment.Type || definition.Shape != assignment.Shape {
			return nil, fmt.Errorf("%w: assignment %s no longer matches its active definition", access.ErrSemanticAttributeSourceConflict, assignment.ID)
		}
		candidate := access.EffectiveSemanticAttribute{DefinitionID: assignment.DefinitionID, DefinitionName: definition.Name,
			DefinitionVersion: definition.DefinitionVersion, Type: definition.Type, Shape: definition.Shape,
			CanonicalValues: append([]string(nil), assignment.CanonicalValues...), ValueDigest: assignment.ValueDigest, Source: "direct"}
		if prior, ok := byDefinition[assignment.DefinitionID]; ok {
			if prior.ValueDigest != candidate.ValueDigest || !reflect.DeepEqual(prior.CanonicalValues, candidate.CanonicalValues) {
				return nil, fmt.Errorf("%w: definition %s has %s and %s values", access.ErrSemanticAttributeSourceConflict, assignment.DefinitionID, prior.Source, candidate.Source)
			}
			byDefinition[assignment.DefinitionID] = prior
		} else {
			byDefinition[assignment.DefinitionID] = candidate
		}
	}
	if source.Kind != "" {
		for _, mapping := range controlBefore.Mappings {
			if mapping.Tombstoned || mapping.SourceKind != source.Kind || mapping.Provider != source.Provider || mapping.Issuer != source.Issuer || mapping.Audience != source.Audience {
				continue
			}
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
			if !definition.Enabled || definition.DefinitionVersion != mapping.DefinitionVersion || definition.Type != mapping.Type || definition.Shape != mapping.Shape {
				return nil, fmt.Errorf("%w: trusted mapping %s no longer matches its active definition", access.ErrSemanticAttributeSourceConflict, mapping.ID)
			}
			values, valueDigest, valueErr := access.CanonicalSemanticAttributeValues(definition, raw)
			if valueErr != nil {
				return nil, valueErr
			}
			candidate := access.EffectiveSemanticAttribute{DefinitionID: mapping.DefinitionID, DefinitionName: definition.Name,
				DefinitionVersion: definition.DefinitionVersion, Type: definition.Type, Shape: definition.Shape,
				CanonicalValues: values, ValueDigest: valueDigest, Source: "trusted_claim"}
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
	controlAfter, err := r.SemanticAttributeControl(ctx)
	if err != nil {
		return nil, err
	}
	if controlBefore.State.Revision != controlAfter.State.Revision || controlBefore.State.Digest != controlAfter.State.Digest {
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

// validateSemanticAttributeResolutionSubject makes subject liveness an
// explicit authorization input. Assignments are append-only and therefore a
// revoked principal/group can still have historical rows; resolution must not
// turn those rows into effective authority. Membership lookup below also
// excludes revoked groups, but validating a requested group here closes the
// direct-resolution path as well.
func validateSemanticAttributeResolutionSubject(ctx context.Context, db DBTX, subject access.SubjectRef) error {
	query := `SELECT EXISTS (
		SELECT 1 FROM access.principal
		WHERE id = $1::uuid AND status = 'active'
		  AND revoked_at IS NULL AND disabled_at IS NULL AND blocked_at IS NULL)`
	if subject.Kind == access.SubjectKindGroup {
		query = `SELECT EXISTS (
			SELECT 1 FROM access.access_group
			WHERE id = $1::uuid AND revoked_at IS NULL)`
	}
	var active bool
	if err := db.QueryRow(ctx, query, subject.ID).Scan(&active); err != nil {
		return fmt.Errorf("validate semantic attribute resolution subject: %w", err)
	}
	if !active {
		return fmt.Errorf("%w: semantic attribute resolution subject %s/%s is not active", access.ErrSemanticAttributeSourceConflict, subject.Kind, subject.ID)
	}
	return nil
}

var _ access.SemanticAttributeControlReader = (*Repository)(nil)
var _ access.SemanticAttributeControlWriter = (*Repository)(nil)
