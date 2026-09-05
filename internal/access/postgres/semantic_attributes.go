package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/semanticvalue"
	"github.com/jackc/pgx/v5"
)

const maxSemanticAttributeSearchRows = 1000

var _ access.SemanticAttributeRegistry = (*Repository)(nil)

type registryDigestWire struct {
	Profile     string                         `json:"profile"`
	Definitions []registryDefinitionDigestWire `json:"definitions"`
}

type registryDefinitionDigestWire struct {
	ID                string                                 `json:"id"`
	Name              string                                 `json:"name"`
	Type              semanticvalue.Type                     `json:"type"`
	Shape             access.SemanticAttributeShape          `json:"shape"`
	DefinitionVersion int64                                  `json:"definitionVersion"`
	OwnerKind         access.SemanticAttributeOwnerKind      `json:"ownerKind"`
	OwnerID           string                                 `json:"ownerId"`
	DisplayName       string                                 `json:"displayName"`
	Description       string                                 `json:"description"`
	DocumentationURL  string                                 `json:"documentationUrl"`
	LifecycleState    access.SemanticAttributeLifecycleState `json:"lifecycleState"`
}

type semanticAttributeRow struct {
	ID, Name, ValueType, ValueShape, Profile   string
	Version                                    int64
	OwnerKind, OwnerID                         string
	DisplayName, Description, DocumentationURL string
	Enabled                                    bool
	DisabledAt                                 *time.Time
	CreatedAt, UpdatedAt                       time.Time
}

const semanticAttributeDefinitionColumns = `
definition_id::text AS definition_id, name, value_type, value_shape,
	profile, definition_version, owner_kind, COALESCE(owner_id::text, '') AS owner_id,
	display_name, description, documentation_url, enabled,
	disabled_at, created_at, updated_at`

func (r *Repository) SemanticAttributeRegistry(ctx context.Context) (access.SemanticAttributeRegistrySnapshot, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.SemanticAttributeRegistrySnapshot{}, err
	}
	state, definitions, err := readSemanticAttributeRegistrySnapshot(ctx, db)
	if err != nil {
		return access.SemanticAttributeRegistrySnapshot{}, err
	}
	digest, err := semanticAttributeRegistryDigest(state.Profile, definitions)
	if err != nil {
		return access.SemanticAttributeRegistrySnapshot{}, err
	}
	if digest != state.Digest {
		return access.SemanticAttributeRegistrySnapshot{}, fmt.Errorf("semantic attribute registry digest mismatch: stored %q, computed %q", state.Digest, digest)
	}
	return access.SemanticAttributeRegistrySnapshot{State: state, Definitions: definitions}, nil
}

// readSemanticAttributeRegistrySnapshot deliberately reads registry identity
// and definitions in one PostgreSQL statement. READ COMMITTED provides a
// statement snapshot, avoiding a false digest mismatch when a mutation commits
// between otherwise separate state and definition queries.
func readSemanticAttributeRegistrySnapshot(ctx context.Context, db DBTX) (access.SemanticAttributeRegistryState, []access.SemanticAttributeDefinition, error) {
	rows, err := db.Query(ctx, `
		SELECT r.profile, r.registry_revision, r.registry_digest, r.updated_at,
		       d.definition_id IS NOT NULL,
		       COALESCE(d.definition_id::text, ''), COALESCE(d.name, ''),
		       COALESCE(d.value_type, ''), COALESCE(d.value_shape, ''),
		       COALESCE(d.profile, ''), COALESCE(d.definition_version, 0),
		       COALESCE(d.owner_kind, ''), COALESCE(d.owner_id::text, ''),
		       COALESCE(d.display_name, ''), COALESCE(d.description, ''),
		       COALESCE(d.documentation_url, ''), COALESCE(d.enabled, false),
		       d.disabled_at,
		       COALESCE(d.created_at, r.updated_at), COALESCE(d.updated_at, r.updated_at)
		FROM access.semantic_attribute_registry r
		LEFT JOIN access.semantic_attribute_definition d ON true
		WHERE r.singleton
		ORDER BY d.name, d.definition_id`)
	if err != nil {
		return access.SemanticAttributeRegistryState{}, nil, fmt.Errorf("read semantic attribute registry snapshot: %w", err)
	}
	defer rows.Close()

	var state access.SemanticAttributeRegistryState
	definitions := make([]access.SemanticAttributeDefinition, 0)
	for rows.Next() {
		var stateUpdatedAt time.Time
		var hasDefinition bool
		var row semanticAttributeRow
		if err := rows.Scan(
			&state.Profile, &state.Revision, &state.Digest, &stateUpdatedAt,
			&hasDefinition,
			&row.ID, &row.Name, &row.ValueType, &row.ValueShape, &row.Profile,
			&row.Version, &row.OwnerKind, &row.OwnerID, &row.DisplayName,
			&row.Description, &row.DocumentationURL, &row.Enabled,
			&row.DisabledAt, &row.CreatedAt, &row.UpdatedAt,
		); err != nil {
			return access.SemanticAttributeRegistryState{}, nil, fmt.Errorf("scan semantic attribute registry snapshot: %w", err)
		}
		state.UpdatedAt = formatTime(stateUpdatedAt)
		if hasDefinition {
			definitions = append(definitions, semanticAttributeDefinition(row))
		}
	}
	if err := rows.Err(); err != nil {
		return access.SemanticAttributeRegistryState{}, nil, fmt.Errorf("iterate semantic attribute registry snapshot: %w", err)
	}
	if state.Profile == "" {
		return access.SemanticAttributeRegistryState{}, nil, errors.New("semantic attribute registry state is missing")
	}
	return state, definitions, nil
}

func (r *Repository) SemanticAttributeDefinition(ctx context.Context, name string) (access.SemanticAttributeDefinition, error) {
	if err := semanticvalue.ValidateAttributeName(name); err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	db, err := r.requireDB()
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	return querySemanticAttributeDefinition(ctx, db, `WHERE name = $1::text`, name)
}

func (r *Repository) SemanticAttributeDefinitionByID(ctx context.Context, id string) (access.SemanticAttributeDefinition, error) {
	canonicalID, err := uuidID("semantic attribute definition id", id)
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	db, err := r.requireDB()
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	return querySemanticAttributeDefinition(ctx, db, `WHERE definition_id = $1::uuid`, canonicalID)
}

func (r *Repository) SearchSemanticAttributes(ctx context.Context, filter access.SemanticAttributeSearch) ([]access.SemanticAttributeDefinition, error) {
	query := strings.TrimSpace(filter.Query)
	if len(query) > 255 || strings.ContainsRune(query, '\x00') {
		return nil, errors.New("semantic attribute search query is invalid")
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > maxSemanticAttributeSearchRows {
		limit = maxSemanticAttributeSearchRows
	}
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `
		SELECT `+semanticAttributeDefinitionColumns+`
		FROM access.semantic_attribute_definition
		WHERE $1::text = ''
		   OR strpos(lower(name), lower($1::text)) > 0
		   OR strpos(lower(display_name), lower($1::text)) > 0
		   OR strpos(lower(description), lower($1::text)) > 0
		ORDER BY name LIMIT $2::int`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search semantic attribute definitions: %w", err)
	}
	defer rows.Close()
	definitions := make([]access.SemanticAttributeDefinition, 0)
	for rows.Next() {
		row, err := scanSemanticAttributeRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan semantic attribute definition: %w", err)
		}
		definitions = append(definitions, semanticAttributeDefinition(row))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate semantic attribute definitions: %w", err)
	}
	return definitions, nil
}

func (r *Repository) RegisterSemanticAttribute(ctx context.Context, input access.RegisterSemanticAttributeInput) (access.SemanticAttributeDefinition, error) {
	metadata, err := validateSemanticAttributeDefinitionInput(input.Name, input.Type, input.Shape, input.Metadata)
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	actorID, err := uuidID("semantic attribute mutation actor principal id", input.Mutation.ActorPrincipalID)
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	var result access.SemanticAttributeDefinition
	err = r.RunAuditedMutation(ctx, func(repo access.Repository) (access.AuditEventInput, error) {
		transactional, ok := repo.(*Repository)
		if !ok {
			return access.AuditEventInput{}, errors.New("semantic attribute mutation requires PostgreSQL transaction")
		}
		locked, err := lockSemanticAttributeRegistry(ctx, transactional.db)
		if err != nil {
			return access.AuditEventInput{}, fmt.Errorf("lock semantic attribute registry: %w", err)
		}
		existing, err := querySemanticAttributeDefinition(ctx, transactional.db, `WHERE name = $1::text`, input.Name)
		if err == nil {
			result = existing
			if result.Type != input.Type || result.Shape != input.Shape {
				return access.AuditEventInput{}, fmt.Errorf("%w: %s is registered as %s/%s", access.ErrSemanticAttributeConflict, input.Name, result.Type, result.Shape)
			}
			return semanticAttributeAuditEvent(input.Mutation, actorID, "semantic_attribute.register_replay", result, locked.Revision, locked.Digest), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return access.AuditEventInput{}, fmt.Errorf("read semantic attribute definition: %w", err)
		}
		definitionID, err := newUUID()
		if err != nil {
			return access.AuditEventInput{}, fmt.Errorf("create semantic attribute definition id: %w", err)
		}
		result, err = insertSemanticAttributeDefinition(ctx, transactional.db, definitionID, input.Name, input.Type, input.Shape, metadata)
		if err != nil {
			return access.AuditEventInput{}, fmt.Errorf("insert semantic attribute definition: %w", err)
		}
		registry, err := refreshSemanticAttributeRegistry(ctx, transactional.db, locked.Revision+1)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		return semanticAttributeAuditEvent(input.Mutation, actorID, "semantic_attribute.register", result, registry.Revision, registry.Digest), nil
	})
	return result, err
}

func (r *Repository) UpdateSemanticAttributeMetadata(ctx context.Context, input access.UpdateSemanticAttributeMetadataInput) (access.SemanticAttributeDefinition, error) {
	if err := semanticvalue.ValidateAttributeName(input.Name); err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	metadata, err := canonicalSemanticAttributeMetadata(input.Metadata)
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	actorID, err := uuidID("semantic attribute mutation actor principal id", input.Mutation.ActorPrincipalID)
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	var result access.SemanticAttributeDefinition
	err = r.RunAuditedMutation(ctx, func(repo access.Repository) (access.AuditEventInput, error) {
		transactional, ok := repo.(*Repository)
		if !ok {
			return access.AuditEventInput{}, errors.New("semantic attribute mutation requires PostgreSQL transaction")
		}
		locked, err := lockSemanticAttributeRegistry(ctx, transactional.db)
		if err != nil {
			return access.AuditEventInput{}, fmt.Errorf("lock semantic attribute registry: %w", err)
		}
		existing, err := querySemanticAttributeDefinition(ctx, transactional.db, `WHERE name = $1::text`, input.Name)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		result = existing
		if reflect.DeepEqual(result.Metadata, metadata) {
			return semanticAttributeAuditEvent(input.Mutation, actorID, "semantic_attribute.metadata_replay", result, locked.Revision, locked.Digest), nil
		}
		result, err = updateSemanticAttributeMetadata(ctx, transactional.db, input.Name, metadata)
		if err != nil {
			return access.AuditEventInput{}, fmt.Errorf("update semantic attribute metadata: %w", err)
		}
		registry, err := refreshSemanticAttributeRegistry(ctx, transactional.db, locked.Revision+1)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		return semanticAttributeAuditEvent(input.Mutation, actorID, "semantic_attribute.metadata_update", result, registry.Revision, registry.Digest), nil
	})
	return result, err
}

func (r *Repository) SetSemanticAttributeEnabled(ctx context.Context, name string, enabled bool, mutation access.SemanticAttributeMutationContext) (access.SemanticAttributeDefinition, error) {
	if err := semanticvalue.ValidateAttributeName(name); err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	actorID, err := uuidID("semantic attribute mutation actor principal id", mutation.ActorPrincipalID)
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	var result access.SemanticAttributeDefinition
	err = r.RunAuditedMutation(ctx, func(repo access.Repository) (access.AuditEventInput, error) {
		transactional, ok := repo.(*Repository)
		if !ok {
			return access.AuditEventInput{}, errors.New("semantic attribute mutation requires PostgreSQL transaction")
		}
		locked, err := lockSemanticAttributeRegistry(ctx, transactional.db)
		if err != nil {
			return access.AuditEventInput{}, fmt.Errorf("lock semantic attribute registry: %w", err)
		}
		existing, err := querySemanticAttributeDefinition(ctx, transactional.db, `WHERE name = $1::text`, name)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		result = existing
		action := "semantic_attribute.disable"
		if enabled {
			action = "semantic_attribute.enable"
		}
		if result.Enabled == enabled {
			return semanticAttributeAuditEvent(mutation, actorID, action+"_replay", result, locked.Revision, locked.Digest), nil
		}
		result, err = setSemanticAttributeEnabled(ctx, transactional.db, name, enabled)
		if err != nil {
			return access.AuditEventInput{}, fmt.Errorf("set semantic attribute enabled state: %w", err)
		}
		registry, err := refreshSemanticAttributeRegistry(ctx, transactional.db, locked.Revision+1)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		return semanticAttributeAuditEvent(mutation, actorID, action, result, registry.Revision, registry.Digest), nil
	})
	return result, err
}

func (r *Repository) ValidateSemanticAttributeValue(ctx context.Context, name string, input any) (access.CanonicalSemanticAttributeValue, error) {
	definition, err := r.SemanticAttributeDefinition(ctx, name)
	if err != nil {
		return access.CanonicalSemanticAttributeValue{}, err
	}
	return canonicalSemanticAttributeValue(definition, input)
}

func canonicalSemanticAttributeValue(definition access.SemanticAttributeDefinition, input any) (access.CanonicalSemanticAttributeValue, error) {
	if !definition.Enabled {
		return access.CanonicalSemanticAttributeValue{}, fmt.Errorf("%w: %s", access.ErrSemanticAttributeDisabled, definition.Name)
	}
	result := access.CanonicalSemanticAttributeValue{
		DefinitionID: definition.ID, DefinitionVersion: definition.DefinitionVersion,
		Name: definition.Name, Type: definition.Type, Shape: definition.Shape,
	}
	if definition.Shape == access.SemanticAttributeScalar {
		value, err := semanticvalue.Canonicalize(definition.Type, input)
		if err != nil {
			return access.CanonicalSemanticAttributeValue{}, err
		}
		result.CanonicalValues = []string{value.Canonical()}
		result.Digest = value.Digest()
		return result, nil
	}
	values, err := semanticAttributeListInputs(input)
	if err != nil {
		return access.CanonicalSemanticAttributeValue{}, err
	}
	set, err := semanticvalue.CanonicalizeSet(definition.Type, values)
	if err != nil {
		return access.CanonicalSemanticAttributeValue{}, err
	}
	canonical := set.Values()
	result.CanonicalValues = make([]string, len(canonical))
	for index, value := range canonical {
		result.CanonicalValues[index] = value.Canonical()
	}
	result.Digest = set.Digest()
	return result, nil
}

func validateSemanticAttributeDefinitionInput(name string, valueType semanticvalue.Type, shape access.SemanticAttributeShape, metadata access.SemanticAttributeMetadata) (access.SemanticAttributeMetadata, error) {
	if err := semanticvalue.ValidateAttributeName(name); err != nil {
		return access.SemanticAttributeMetadata{}, err
	}
	switch valueType {
	case semanticvalue.TypeString, semanticvalue.TypeBoolean, semanticvalue.TypeInteger,
		semanticvalue.TypeDecimal, semanticvalue.TypeDate, semanticvalue.TypeTimestamp:
	default:
		return access.SemanticAttributeMetadata{}, fmt.Errorf("%w: %q is not supported by %s", semanticvalue.ErrInvalidType, valueType, semanticvalue.Profile)
	}
	if !shape.Valid() {
		return access.SemanticAttributeMetadata{}, fmt.Errorf("semantic attribute shape %q is invalid", shape)
	}
	return canonicalSemanticAttributeMetadata(metadata)
}

func canonicalSemanticAttributeMetadata(metadata access.SemanticAttributeMetadata) (access.SemanticAttributeMetadata, error) {
	if metadata.Owner.Kind == "" {
		metadata.Owner.Kind = access.SemanticAttributeOwnerInstance
	}
	if !metadata.Owner.Kind.Valid() {
		return access.SemanticAttributeMetadata{}, fmt.Errorf("semantic attribute owner kind %q is invalid", metadata.Owner.Kind)
	}
	metadata.Owner.ID = strings.TrimSpace(metadata.Owner.ID)
	if metadata.Owner.Kind == access.SemanticAttributeOwnerInstance {
		if metadata.Owner.ID != "" {
			return access.SemanticAttributeMetadata{}, errors.New("instance-owned semantic attribute cannot carry an owner id")
		}
	} else {
		ownerID, err := uuidID("semantic attribute owner id", metadata.Owner.ID)
		if err != nil {
			return access.SemanticAttributeMetadata{}, err
		}
		metadata.Owner.ID = ownerID
	}
	metadata.DisplayName = strings.TrimSpace(metadata.DisplayName)
	metadata.Description = strings.TrimSpace(metadata.Description)
	metadata.DocumentationURL = strings.TrimSpace(metadata.DocumentationURL)
	if err := validateSemanticAttributeText("display name", metadata.DisplayName, 255); err != nil {
		return access.SemanticAttributeMetadata{}, err
	}
	if err := validateSemanticAttributeText("description", metadata.Description, 4096); err != nil {
		return access.SemanticAttributeMetadata{}, err
	}
	if len(metadata.DocumentationURL) > 2048 || strings.ContainsAny(metadata.DocumentationURL, "\x00\r\n") {
		return access.SemanticAttributeMetadata{}, errors.New("semantic attribute documentation URL is invalid")
	}
	if metadata.DocumentationURL != "" {
		parsed, err := url.ParseRequestURI(metadata.DocumentationURL)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Scheme != "https" {
			return access.SemanticAttributeMetadata{}, errors.New("semantic attribute documentation URL must be an absolute HTTPS URL without credentials")
		}
	}
	return metadata, nil
}

func validateSemanticAttributeText(label, value string, max int) error {
	if !utf8.ValidString(value) || len(value) > max || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("semantic attribute %s is invalid", label)
	}
	return nil
}

func semanticAttributeListInputs(input any) ([]any, error) {
	if input == nil {
		return nil, fmt.Errorf("%w: null is not an access value list", semanticvalue.ErrInvalidValue)
	}
	value := reflect.ValueOf(input)
	if value.Kind() != reflect.Array && value.Kind() != reflect.Slice {
		return nil, fmt.Errorf("%w: value of type %T is not a list", semanticvalue.ErrInvalidValue, input)
	}
	values := make([]any, value.Len())
	for index := 0; index < value.Len(); index++ {
		values[index] = value.Index(index).Interface()
	}
	return values, nil
}

func lockSemanticAttributeRegistry(ctx context.Context, db DBTX) (access.SemanticAttributeRegistryState, error) {
	var state access.SemanticAttributeRegistryState
	var updatedAt time.Time
	err := db.QueryRow(ctx, `
		SELECT profile, registry_revision, registry_digest, updated_at
		FROM access.semantic_attribute_registry WHERE singleton FOR UPDATE`).
		Scan(&state.Profile, &state.Revision, &state.Digest, &updatedAt)
	state.UpdatedAt = formatTime(updatedAt)
	return state, err
}

func refreshSemanticAttributeRegistry(ctx context.Context, db DBTX, revision int64) (access.SemanticAttributeRegistryState, error) {
	definitions, err := listSemanticAttributeDefinitions(ctx, db)
	if err != nil {
		return access.SemanticAttributeRegistryState{}, fmt.Errorf("list semantic attribute definitions for digest: %w", err)
	}
	digest, err := semanticAttributeRegistryDigest(semanticvalue.Profile, definitions)
	if err != nil {
		return access.SemanticAttributeRegistryState{}, err
	}
	var state access.SemanticAttributeRegistryState
	var updatedAt time.Time
	if err := db.QueryRow(ctx, `
		UPDATE access.semantic_attribute_registry
		SET registry_revision = $1::bigint, registry_digest = $2::text, updated_at = clock_timestamp()
		WHERE singleton
		RETURNING profile, registry_revision, registry_digest, updated_at`, revision, digest).
		Scan(&state.Profile, &state.Revision, &state.Digest, &updatedAt); err != nil {
		return access.SemanticAttributeRegistryState{}, fmt.Errorf("update semantic attribute registry state: %w", err)
	}
	state.UpdatedAt = formatTime(updatedAt)
	return state, nil
}

func semanticAttributeRegistryDigest(profile string, definitions []access.SemanticAttributeDefinition) (string, error) {
	ordered := append([]access.SemanticAttributeDefinition(nil), definitions...)
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].Name == ordered[right].Name {
			return ordered[left].ID < ordered[right].ID
		}
		return ordered[left].Name < ordered[right].Name
	})
	wire := registryDigestWire{Profile: profile, Definitions: make([]registryDefinitionDigestWire, len(ordered))}
	for index, definition := range ordered {
		wire.Definitions[index] = registryDefinitionDigestWire{
			ID: definition.ID, Name: definition.Name, Type: definition.Type, Shape: definition.Shape,
			DefinitionVersion: definition.DefinitionVersion,
			OwnerKind:         definition.Metadata.Owner.Kind, OwnerID: definition.Metadata.Owner.ID,
			DisplayName: definition.Metadata.DisplayName, Description: definition.Metadata.Description,
			DocumentationURL: definition.Metadata.DocumentationURL, LifecycleState: definition.LifecycleState,
		}
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return "", fmt.Errorf("encode semantic attribute registry digest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func semanticAttributeAuditEvent(mutation access.SemanticAttributeMutationContext, actorID, action string, definition access.SemanticAttributeDefinition, revision int64, digest string) access.AuditEventInput {
	metadata, _ := json.Marshal(struct {
		Profile           string                                 `json:"profile"`
		Type              semanticvalue.Type                     `json:"type"`
		Shape             access.SemanticAttributeShape          `json:"shape"`
		DefinitionVersion int64                                  `json:"definitionVersion"`
		RegistryRevision  int64                                  `json:"registryRevision"`
		RegistryDigest    string                                 `json:"registryDigest"`
		OwnerKind         access.SemanticAttributeOwnerKind      `json:"ownerKind"`
		OwnerID           string                                 `json:"ownerId"`
		LifecycleState    access.SemanticAttributeLifecycleState `json:"lifecycleState"`
	}{
		Profile: definition.Profile, Type: definition.Type, Shape: definition.Shape,
		DefinitionVersion: definition.DefinitionVersion, RegistryRevision: revision,
		RegistryDigest: digest, OwnerKind: definition.Metadata.Owner.Kind,
		OwnerID: definition.Metadata.Owner.ID, LifecycleState: definition.LifecycleState,
	})
	return access.AuditEventInput{
		PrincipalID: actorID, Action: action, ResourceKind: "semantic_attribute", ResourceID: definition.Name,
		Capability: access.CapabilityProjectAdmin, Status: "success",
		RequestID: mutation.RequestID, CorrelationID: mutation.CorrelationID, MetadataJSON: string(metadata),
	}
}

func querySemanticAttributeDefinition(ctx context.Context, db DBTX, predicate string, arg any) (access.SemanticAttributeDefinition, error) {
	row := db.QueryRow(ctx, `SELECT `+semanticAttributeDefinitionColumns+`
		FROM access.semantic_attribute_definition `+predicate, arg)
	value, err := scanSemanticAttributeRow(row)
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	return semanticAttributeDefinition(value), nil
}

func listSemanticAttributeDefinitions(ctx context.Context, db DBTX) ([]access.SemanticAttributeDefinition, error) {
	rows, err := db.Query(ctx, `SELECT `+semanticAttributeDefinitionColumns+`
		FROM access.semantic_attribute_definition ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	definitions := make([]access.SemanticAttributeDefinition, 0)
	for rows.Next() {
		row, err := scanSemanticAttributeRow(rows)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, semanticAttributeDefinition(row))
	}
	return definitions, rows.Err()
}

func insertSemanticAttributeDefinition(ctx context.Context, db DBTX, definitionID, name string, valueType semanticvalue.Type, shape access.SemanticAttributeShape, metadata access.SemanticAttributeMetadata) (access.SemanticAttributeDefinition, error) {
	returning := ` RETURNING ` + semanticAttributeDefinitionColumns
	row := db.QueryRow(ctx, `INSERT INTO access.semantic_attribute_definition
		(definition_id, name, value_type, value_shape, profile, owner_kind, owner_id,
		 display_name, description, documentation_url)
		VALUES ($1::uuid, $2::text, $3::text, $4::text, $5::text, $6::text,
		 NULLIF($7::text, '')::uuid, $8::text, $9::text, $10::text)`+returning,
		definitionID, name, string(valueType), string(shape), semanticvalue.Profile,
		string(metadata.Owner.Kind), metadata.Owner.ID, metadata.DisplayName,
		metadata.Description, metadata.DocumentationURL)
	value, err := scanSemanticAttributeRow(row)
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	return semanticAttributeDefinition(value), nil
}

func updateSemanticAttributeMetadata(ctx context.Context, db DBTX, name string, metadata access.SemanticAttributeMetadata) (access.SemanticAttributeDefinition, error) {
	row := db.QueryRow(ctx, `UPDATE access.semantic_attribute_definition
		SET owner_kind = $1::text, owner_id = NULLIF($2::text, '')::uuid,
		    display_name = $3::text, description = $4::text, documentation_url = $5::text,
		    definition_version = definition_version + 1
		WHERE name = $6::text
		  AND (owner_kind, owner_id, display_name, description, documentation_url) IS DISTINCT FROM
		      ($1::text, NULLIF($2::text, '')::uuid, $3::text, $4::text, $5::text)
		RETURNING `+semanticAttributeDefinitionColumns,
		string(metadata.Owner.Kind), metadata.Owner.ID, metadata.DisplayName, metadata.Description,
		metadata.DocumentationURL, name)
	value, err := scanSemanticAttributeRow(row)
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	return semanticAttributeDefinition(value), nil
}

func setSemanticAttributeEnabled(ctx context.Context, db DBTX, name string, enabled bool) (access.SemanticAttributeDefinition, error) {
	row := db.QueryRow(ctx, `UPDATE access.semantic_attribute_definition
		SET enabled = $1::boolean, definition_version = definition_version + 1
		WHERE name = $2::text AND enabled <> $1::boolean
		RETURNING `+semanticAttributeDefinitionColumns, enabled, name)
	value, err := scanSemanticAttributeRow(row)
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	return semanticAttributeDefinition(value), nil
}

type semanticAttributeRowScanner interface {
	Scan(dest ...any) error
}

func scanSemanticAttributeRow(row semanticAttributeRowScanner) (semanticAttributeRow, error) {
	var value semanticAttributeRow
	err := row.Scan(&value.ID, &value.Name, &value.ValueType, &value.ValueShape, &value.Profile,
		&value.Version, &value.OwnerKind, &value.OwnerID, &value.DisplayName, &value.Description,
		&value.DocumentationURL, &value.Enabled, &value.DisabledAt, &value.CreatedAt, &value.UpdatedAt)
	return value, err
}

func semanticAttributeDefinition(row semanticAttributeRow) access.SemanticAttributeDefinition {
	lifecycle := access.SemanticAttributeActive
	if !row.Enabled {
		lifecycle = access.SemanticAttributeDisabled
	}
	return access.SemanticAttributeDefinition{
		ID: row.ID, Name: row.Name, Type: semanticvalue.Type(row.ValueType), Shape: access.SemanticAttributeShape(row.ValueShape),
		Profile: row.Profile, DefinitionVersion: row.Version,
		Metadata: access.SemanticAttributeMetadata{
			Owner:       access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerKind(row.OwnerKind), ID: row.OwnerID},
			DisplayName: row.DisplayName, Description: row.Description, DocumentationURL: row.DocumentationURL,
		},
		LifecycleState: lifecycle, Enabled: row.Enabled, DisabledAt: formatTimePtr(row.DisabledAt),
		CreatedAt: formatTime(row.CreatedAt), UpdatedAt: formatTime(row.UpdatedAt),
	}
}
