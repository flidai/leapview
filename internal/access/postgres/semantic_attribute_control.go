package postgres

// Durable semantic-access control state. Assignment and trusted-claim
// mutations live in focused sibling files; this file owns shared projections,
// deterministic control identity, and read-only interfaces.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/flidai/leapview/internal/access/trustedclaims"
	"github.com/flidai/leapview/internal/semanticvalue"
)

const semanticAttributeControlProfile = semanticvalue.Profile

type semanticAttributeControlStateRow struct {
	Profile, Digest, UpdatedAt string
	Revision                   int64
}

func assignmentFromRow(row accessdb.ListSemanticAttributeAssignmentsRow) access.SemanticAttributeAssignment {
	return access.SemanticAttributeAssignment{ID: row.AssignmentID, DefinitionID: row.DefinitionID,
		DefinitionName: row.DefinitionName, DefinitionVersion: row.DefinitionVersion,
		Type: semanticvalue.Type(row.ValueType), Shape: access.SemanticAttributeShape(row.ValueShape),
		Subject:         access.SubjectRef{Kind: access.SubjectKind(row.SubjectKind), ID: row.SubjectID},
		CanonicalValues: append([]string(nil), row.CanonicalValues...), ValueDigest: row.ValueDigest,
		AssignmentVersion: row.AssignmentVersion, Tombstoned: textValue(row.TombstonedAt) != "",
		TombstonedAt: timestampAPIValue(row.TombstonedAt), CreatedAt: timestampAPIValue(row.CreatedAt), UpdatedAt: timestampAPIValue(row.UpdatedAt)}
}

func assignmentFromInsert(row accessdb.InsertSemanticAttributeAssignmentRow, name string) access.SemanticAttributeAssignment {
	return access.SemanticAttributeAssignment{ID: row.AssignmentID, DefinitionID: row.DefinitionID,
		DefinitionName: name, DefinitionVersion: row.DefinitionVersion,
		Type: semanticvalue.Type(row.ValueType), Shape: access.SemanticAttributeShape(row.ValueShape),
		Subject:         access.SubjectRef{Kind: access.SubjectKind(row.SubjectKind), ID: row.SubjectID},
		CanonicalValues: append([]string(nil), row.CanonicalValues...), ValueDigest: row.ValueDigest,
		AssignmentVersion: row.AssignmentVersion, Tombstoned: textValue(row.TombstonedAt) != "",
		TombstonedAt: timestampAPIValue(row.TombstonedAt), CreatedAt: timestampAPIValue(row.CreatedAt), UpdatedAt: timestampAPIValue(row.UpdatedAt)}
}

func assignmentFromUpdate(row accessdb.UpdateSemanticAttributeAssignmentRow, name string) access.SemanticAttributeAssignment {
	return access.SemanticAttributeAssignment{ID: row.AssignmentID, DefinitionID: row.DefinitionID,
		DefinitionName: name, DefinitionVersion: row.DefinitionVersion,
		Type: semanticvalue.Type(row.ValueType), Shape: access.SemanticAttributeShape(row.ValueShape),
		Subject:         access.SubjectRef{Kind: access.SubjectKind(row.SubjectKind), ID: row.SubjectID},
		CanonicalValues: append([]string(nil), row.CanonicalValues...), ValueDigest: row.ValueDigest,
		AssignmentVersion: row.AssignmentVersion, Tombstoned: textValue(row.TombstonedAt) != "",
		TombstonedAt: timestampAPIValue(row.TombstonedAt), CreatedAt: timestampAPIValue(row.CreatedAt), UpdatedAt: timestampAPIValue(row.UpdatedAt)}
}

func assignmentFromTombstone(row accessdb.TombstoneSemanticAttributeAssignmentRow, name string) access.SemanticAttributeAssignment {
	return access.SemanticAttributeAssignment{ID: row.AssignmentID, DefinitionID: row.DefinitionID,
		DefinitionName: name, DefinitionVersion: row.DefinitionVersion,
		Type: semanticvalue.Type(row.ValueType), Shape: access.SemanticAttributeShape(row.ValueShape),
		Subject:         access.SubjectRef{Kind: access.SubjectKind(row.SubjectKind), ID: row.SubjectID},
		CanonicalValues: append([]string(nil), row.CanonicalValues...), ValueDigest: row.ValueDigest,
		AssignmentVersion: row.AssignmentVersion, Tombstoned: textValue(row.TombstonedAt) != "",
		TombstonedAt: timestampAPIValue(row.TombstonedAt), CreatedAt: timestampAPIValue(row.CreatedAt), UpdatedAt: timestampAPIValue(row.UpdatedAt)}
}

func mappingFromList(row accessdb.ListTrustedClaimMappingsRow) access.TrustedClaimMapping {
	return access.TrustedClaimMapping{ID: row.MappingID, SourceKind: access.TrustedClaimSourceKind(row.SourceKind),
		Provider: row.Provider, Issuer: row.Issuer, Audience: row.Audience, Claim: row.Claim,
		DefinitionID: row.DefinitionID, DefinitionName: row.DefinitionName,
		DefinitionVersion: row.DefinitionVersion, Type: semanticvalue.Type(row.ValueType),
		Shape: access.SemanticAttributeShape(row.ValueShape), MappingVersion: row.MappingVersion,
		Tombstoned: textValue(row.TombstonedAt) != "", TombstonedAt: timestampAPIValue(row.TombstonedAt),
		CreatedAt: timestampAPIValue(row.CreatedAt), UpdatedAt: timestampAPIValue(row.UpdatedAt)}
}

func mappingFromInsert(row accessdb.InsertTrustedClaimMappingRow, name string) access.TrustedClaimMapping {
	return access.TrustedClaimMapping{ID: row.MappingID, SourceKind: access.TrustedClaimSourceKind(row.SourceKind),
		Provider: row.Provider, Issuer: row.Issuer, Audience: row.Audience, Claim: row.Claim,
		DefinitionID: row.DefinitionID, DefinitionName: name, DefinitionVersion: row.DefinitionVersion,
		Type: semanticvalue.Type(row.ValueType), Shape: access.SemanticAttributeShape(row.ValueShape),
		MappingVersion: row.MappingVersion, Tombstoned: textValue(row.TombstonedAt) != "",
		TombstonedAt: timestampAPIValue(row.TombstonedAt), CreatedAt: timestampAPIValue(row.CreatedAt), UpdatedAt: timestampAPIValue(row.UpdatedAt)}
}

func mappingFromTombstone(row accessdb.TombstoneTrustedClaimMappingRow, name string) access.TrustedClaimMapping {
	return access.TrustedClaimMapping{ID: row.MappingID, SourceKind: access.TrustedClaimSourceKind(row.SourceKind),
		Provider: row.Provider, Issuer: row.Issuer, Audience: row.Audience, Claim: row.Claim,
		DefinitionID: row.DefinitionID, DefinitionName: name, DefinitionVersion: row.DefinitionVersion,
		Type: semanticvalue.Type(row.ValueType), Shape: access.SemanticAttributeShape(row.ValueShape),
		MappingVersion: row.MappingVersion, Tombstoned: textValue(row.TombstonedAt) != "",
		TombstonedAt: timestampAPIValue(row.TombstonedAt), CreatedAt: timestampAPIValue(row.CreatedAt), UpdatedAt: timestampAPIValue(row.UpdatedAt)}
}

func textValue(value interface{}) string {
	if value == nil {
		return ""
	}
	if bytes, ok := value.([]byte); ok {
		return string(bytes)
	}
	return fmt.Sprint(value)
}

// timestampAPIValue converts the control query's epoch-microsecond wire value
// into the stable UTC representation exposed by access APIs. The SQL query
// deliberately does not return timestamptz::text because that representation
// changes with the PostgreSQL session's TimeZone and DateStyle settings.
func timestampAPIValue(value interface{}) string {
	raw := textValue(value)
	if raw == "" {
		return ""
	}
	micros, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return ""
	}
	return time.UnixMicro(micros).UTC().Format(time.RFC3339Nano)
}

func semanticAttributeControlDigest(assignments []access.SemanticAttributeAssignment, mappings []access.TrustedClaimMapping) (string, error) {
	return access.SemanticAttributeControlDigest(assignments, mappings)
}

func lockSemanticAttributeControlState(ctx context.Context, db DBTX) (semanticAttributeControlStateRow, error) {
	row, err := accessdb.New(db).LockSemanticAttributeControlState(ctx)
	if err != nil {
		return semanticAttributeControlStateRow{}, fmt.Errorf("lock semantic attribute control state: %w", err)
	}
	return semanticAttributeControlStateRow{Profile: row.Profile, Revision: row.ControlRevision, Digest: row.ControlDigest, UpdatedAt: timestampAPIValue(row.UpdatedAt)}, nil
}

func readSemanticAttributeControlState(ctx context.Context, db DBTX) (semanticAttributeControlStateRow, error) {
	row, err := accessdb.New(db).GetSemanticAttributeControlState(ctx)
	if err != nil {
		return semanticAttributeControlStateRow{}, fmt.Errorf("read semantic attribute control state: %w", err)
	}
	return semanticAttributeControlStateRow{Profile: row.Profile, Revision: row.ControlRevision, Digest: row.ControlDigest, UpdatedAt: timestampAPIValue(row.UpdatedAt)}, nil
}

func allControlRows(ctx context.Context, db DBTX) ([]access.SemanticAttributeAssignment, []access.TrustedClaimMapping, error) {
	queries := accessdb.New(db)
	assignmentRows, err := queries.ListSemanticAttributeAssignments(ctx, accessdb.ListSemanticAttributeAssignmentsParams{IncludeTombstones: true})
	if err != nil {
		return nil, nil, fmt.Errorf("list semantic attribute assignments: %w", err)
	}
	mappingRows, err := queries.ListTrustedClaimMappings(ctx, accessdb.ListTrustedClaimMappingsParams{IncludeTombstones: true})
	if err != nil {
		return nil, nil, fmt.Errorf("list trusted claim mappings: %w", err)
	}
	assignments := make([]access.SemanticAttributeAssignment, len(assignmentRows))
	for i := range assignmentRows {
		assignments[i] = assignmentFromRow(assignmentRows[i])
	}
	mappings := make([]access.TrustedClaimMapping, len(mappingRows))
	for i := range mappingRows {
		mappings[i] = mappingFromList(mappingRows[i])
	}
	return assignments, mappings, nil
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
	row, err := accessdb.New(db).UpdateSemanticAttributeControlState(ctx, accessdb.UpdateSemanticAttributeControlStateParams{ControlRevision: current.Revision + 1, ControlDigest: digest})
	if err != nil {
		return semanticAttributeControlStateRow{}, fmt.Errorf("advance semantic attribute control state: %w", err)
	}
	return semanticAttributeControlStateRow{Profile: row.Profile, Revision: row.ControlRevision, Digest: row.ControlDigest, UpdatedAt: timestampAPIValue(row.UpdatedAt)}, nil
}

func (r *Repository) SemanticAttributeControl(ctx context.Context) (access.SemanticAttributeControlSnapshot, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.SemanticAttributeControlSnapshot{}, err
	}
	// READ COMMITTED gives each statement its own snapshot. Read the control
	// state on both sides of the row reads and fail closed if a concurrent
	// mutation could have produced a mixed projection.
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
		if before.Revision != after.Revision || before.Digest != after.Digest || before.Profile != after.Profile {
			continue
		}
		digest, err := semanticAttributeControlDigest(assignments, mappings)
		if err != nil {
			return access.SemanticAttributeControlSnapshot{}, err
		}
		if before.Profile != semanticAttributeControlProfile || before.Digest != digest {
			return access.SemanticAttributeControlSnapshot{}, fmt.Errorf("%w: stored digest %q, computed %q", access.ErrSemanticAttributeControlCorrupt, before.Digest, digest)
		}
		snapshot := access.SemanticAttributeControlSnapshot{State: access.SemanticAttributeControlState{Profile: before.Profile, Revision: before.Revision, Digest: before.Digest, UpdatedAt: before.UpdatedAt}, Assignments: assignments, Mappings: mappings}
		if err := access.ValidateSemanticAttributeControlSnapshot(snapshot); err != nil {
			return access.SemanticAttributeControlSnapshot{}, err
		}
		return snapshot, nil
	}
	return access.SemanticAttributeControlSnapshot{}, fmt.Errorf("%w: control state changed during read", access.ErrSemanticAttributeControlCorrupt)
}

func (r *Repository) SemanticAttributeAssignments(ctx context.Context, filter access.SemanticAttributeAssignmentFilter) ([]access.SemanticAttributeAssignment, error) {
	if filter.DefinitionID != "" {
		if _, err := uuidID("semantic attribute definition id", filter.DefinitionID); err != nil {
			return nil, err
		}
	}
	params := accessdb.ListSemanticAttributeAssignmentsParams{DefinitionID: filter.DefinitionID, IncludeTombstones: filter.IncludeTombstones}
	if filter.Subject.Kind != "" || filter.Subject.ID != "" {
		if err := access.ValidateSemanticAttributeSubject(filter.Subject); err != nil {
			return nil, err
		}
		params.SubjectKind, params.SubjectID = string(filter.Subject.Kind), filter.Subject.ID
		if _, err := uuidID("semantic attribute subject id", params.SubjectID); err != nil {
			return nil, err
		}
	}
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := accessdb.New(db).ListSemanticAttributeAssignments(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list semantic attribute assignments: %w", err)
	}
	result := make([]access.SemanticAttributeAssignment, len(rows))
	for i := range rows {
		result[i] = assignmentFromRow(rows[i])
	}
	return result, nil
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
	source, err := canonicalTrustedClaimSource(source)
	if err != nil {
		return access.TrustedClaimSource{}, "", err
	}
	if !validTrustedClaimText(claim, 1024) {
		return access.TrustedClaimSource{}, "", errors.New("trusted claim name is invalid")
	}
	return source, claim, nil
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
		label    string
		value    string
		validate func(string) error
	}{
		{label: "provider", value: source.Provider, validate: trustedclaims.ValidateProvider},
		{label: "issuer", value: source.Issuer, validate: trustedclaims.ValidateIssuer},
		{label: "audience", value: source.Audience, validate: trustedclaims.ValidateAudience},
	} {
		if field.value != "" && field.validate(field.value) != nil {
			return nil, fmt.Errorf("trusted claim %s is invalid", field.label)
		}
	}
	if filter.Claim != "" && !validTrustedClaimText(filter.Claim, 1024) {
		return nil, errors.New("trusted claim claim is invalid")
	}
	claim := filter.Claim
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := accessdb.New(db).ListTrustedClaimMappings(ctx, accessdb.ListTrustedClaimMappingsParams{SourceKind: string(source.Kind), Provider: source.Provider, Issuer: source.Issuer, Audience: source.Audience, Claim: claim, IncludeTombstones: filter.IncludeTombstones})
	if err != nil {
		return nil, fmt.Errorf("list trusted claim mappings: %w", err)
	}
	result := make([]access.TrustedClaimMapping, len(rows))
	for i := range rows {
		result[i] = mappingFromList(rows[i])
	}
	return result, nil
}

var _ access.SemanticAttributeControlReader = (*Repository)(nil)
var _ access.SemanticAttributeControlWriter = (*Repository)(nil)
