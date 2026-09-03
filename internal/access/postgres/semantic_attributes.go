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
	"strings"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/flidai/leapview/internal/semanticvalue"
	"github.com/jackc/pgx/v5"
)

const maxSemanticAttributeSearchRows = 1000

var _ access.SemanticAttributeRegistry = (*Repository)(nil)
var _ access.VersionedSemanticAttributeRegistry = (*Repository)(nil)

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
	DisabledAt, CreatedAt, UpdatedAt           string
}

func (r *Repository) SemanticAttributeRegistry(ctx context.Context) (access.SemanticAttributeRegistrySnapshot, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.SemanticAttributeRegistrySnapshot{}, err
	}
	queries := accessdb.New(db)
	stateRow, err := queries.GetSemanticAttributeRegistry(ctx)
	if err != nil {
		return access.SemanticAttributeRegistrySnapshot{}, fmt.Errorf("read semantic attribute registry state: %w", err)
	}
	rows, err := queries.ListSemanticAttributeDefinitions(ctx)
	if err != nil {
		return access.SemanticAttributeRegistrySnapshot{}, fmt.Errorf("list semantic attribute definitions: %w", err)
	}
	definitions := make([]access.SemanticAttributeDefinition, len(rows))
	for index, row := range rows {
		definitions[index] = semanticAttributeDefinitionFromList(row)
	}
	digest, err := semanticAttributeRegistryDigest(stateRow.Profile, definitions)
	if err != nil {
		return access.SemanticAttributeRegistrySnapshot{}, err
	}
	if digest != stateRow.RegistryDigest {
		return access.SemanticAttributeRegistrySnapshot{}, fmt.Errorf("semantic attribute registry digest mismatch: stored %q, computed %q", stateRow.RegistryDigest, digest)
	}
	return access.SemanticAttributeRegistrySnapshot{
		State: access.SemanticAttributeRegistryState{
			Profile: stateRow.Profile, Revision: stateRow.RegistryRevision,
			Digest: stateRow.RegistryDigest, UpdatedAt: stateRow.UpdatedAt,
		},
		Definitions: definitions,
	}, nil
}

func (r *Repository) SemanticAttributeDefinition(ctx context.Context, name string) (access.SemanticAttributeDefinition, error) {
	if err := semanticvalue.ValidateAttributeName(name); err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	db, err := r.requireDB()
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	row, err := accessdb.New(db).GetSemanticAttributeDefinition(ctx, name)
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	return semanticAttributeDefinitionFromGet(row), nil
}

func (r *Repository) SemanticAttributeDefinitionByID(ctx context.Context, id string) (access.SemanticAttributeDefinition, error) {
	canonicalID, err := uuidID("semantic attribute definition id", id)
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	parsedID, err := pgUUID(canonicalID)
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	db, err := r.requireDB()
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	row, err := accessdb.New(db).GetSemanticAttributeDefinitionByID(ctx, parsedID)
	if err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	return semanticAttributeDefinitionFromGetByID(row), nil
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
	rows, err := accessdb.New(db).SearchSemanticAttributeDefinitions(ctx, accessdb.SearchSemanticAttributeDefinitionsParams{SearchQuery: query, PageSize: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("search semantic attribute definitions: %w", err)
	}
	definitions := make([]access.SemanticAttributeDefinition, len(rows))
	for index, row := range rows {
		definitions[index] = semanticAttributeDefinitionFromSearch(row)
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
		queries := accessdb.New(transactional.db)
		if err := validateSemanticAttributeOwnerExists(ctx, transactional.db, metadata); err != nil {
			return access.AuditEventInput{}, err
		}
		locked, err := queries.LockSemanticAttributeRegistry(ctx)
		if err != nil {
			return access.AuditEventInput{}, fmt.Errorf("lock semantic attribute registry: %w", err)
		}
		existing, err := queries.GetSemanticAttributeDefinition(ctx, input.Name)
		if err == nil {
			result = semanticAttributeDefinitionFromGet(existing)
			if result.Type != input.Type || result.Shape != input.Shape {
				return access.AuditEventInput{}, fmt.Errorf("%w: %s is registered as %s/%s", access.ErrSemanticAttributeConflict, input.Name, result.Type, result.Shape)
			}
			return semanticAttributeAuditEvent(input.Mutation, actorID, "semantic_attribute.register_replay", result, locked.RegistryRevision, locked.RegistryDigest), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return access.AuditEventInput{}, fmt.Errorf("read semantic attribute definition: %w", err)
		}
		definitionID, err := newUUID()
		if err != nil {
			return access.AuditEventInput{}, fmt.Errorf("create semantic attribute definition id: %w", err)
		}
		parsedDefinitionID, err := pgUUID(definitionID)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		inserted, err := queries.InsertSemanticAttributeDefinition(ctx, accessdb.InsertSemanticAttributeDefinitionParams{
			DefinitionID: parsedDefinitionID, Name: input.Name, ValueType: string(input.Type),
			ValueShape: string(input.Shape), Profile: semanticvalue.Profile,
			OwnerKind: string(metadata.Owner.Kind), OwnerID: metadata.Owner.ID,
			DisplayName: metadata.DisplayName, Description: metadata.Description, DocumentationUrl: metadata.DocumentationURL,
		})
		if err != nil {
			return access.AuditEventInput{}, fmt.Errorf("insert semantic attribute definition: %w", err)
		}
		result = semanticAttributeDefinitionFromInsert(inserted)
		registry, err := refreshSemanticAttributeRegistry(ctx, queries, locked.RegistryRevision+1)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		return semanticAttributeAuditEvent(input.Mutation, actorID, "semantic_attribute.register", result, registry.RegistryRevision, registry.RegistryDigest), nil
	})
	return result, err
}

func (r *Repository) UpdateSemanticAttributeMetadata(ctx context.Context, input access.UpdateSemanticAttributeMetadataInput) (access.SemanticAttributeDefinition, error) {
	return r.UpdateSemanticAttributeMetadataExpected(ctx, input)
}

func (r *Repository) UpdateSemanticAttributeMetadataExpected(ctx context.Context, input access.UpdateSemanticAttributeMetadataInput) (access.SemanticAttributeDefinition, error) {
	if err := semanticvalue.ValidateAttributeName(input.Name); err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	if input.ExpectedVersion < 0 {
		return access.SemanticAttributeDefinition{}, errors.New("semantic attribute expected version cannot be negative")
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
		queries := accessdb.New(transactional.db)
		locked, err := queries.LockSemanticAttributeRegistry(ctx)
		if err != nil {
			return access.AuditEventInput{}, fmt.Errorf("lock semantic attribute registry: %w", err)
		}
		existing, err := queries.GetSemanticAttributeDefinition(ctx, input.Name)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		result = semanticAttributeDefinitionFromGet(existing)
		if input.ExpectedVersion > 0 && result.DefinitionVersion != input.ExpectedVersion {
			return access.AuditEventInput{}, fmt.Errorf("%w: semantic attribute %s expected version %d, current %d", access.ErrSemanticAttributeConflict, input.Name, input.ExpectedVersion, result.DefinitionVersion)
		}
		if result.Metadata.Owner != metadata.Owner {
			if err := validateSemanticAttributeOwnerExists(ctx, transactional.db, metadata); err != nil {
				return access.AuditEventInput{}, err
			}
		}
		if reflect.DeepEqual(result.Metadata, metadata) {
			return semanticAttributeAuditEvent(input.Mutation, actorID, "semantic_attribute.metadata_replay", result, locked.RegistryRevision, locked.RegistryDigest), nil
		}
		updated, err := queries.UpdateSemanticAttributeDefinitionMetadata(ctx, accessdb.UpdateSemanticAttributeDefinitionMetadataParams{
			OwnerKind: string(metadata.Owner.Kind), OwnerID: metadata.Owner.ID,
			DisplayName: metadata.DisplayName, Description: metadata.Description,
			DocumentationUrl: metadata.DocumentationURL, Name: input.Name, ExpectedVersion: input.ExpectedVersion,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) && input.ExpectedVersion > 0 {
				return access.AuditEventInput{}, fmt.Errorf("%w: semantic attribute %s expected version %d", access.ErrSemanticAttributeConflict, input.Name, input.ExpectedVersion)
			}
			return access.AuditEventInput{}, fmt.Errorf("update semantic attribute metadata: %w", err)
		}
		result = semanticAttributeDefinitionFromMetadata(updated)
		registry, err := refreshSemanticAttributeRegistry(ctx, queries, locked.RegistryRevision+1)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		return semanticAttributeAuditEvent(input.Mutation, actorID, "semantic_attribute.metadata_update", result, registry.RegistryRevision, registry.RegistryDigest), nil
	})
	return result, err
}

func (r *Repository) SetSemanticAttributeEnabled(ctx context.Context, name string, enabled bool, mutation access.SemanticAttributeMutationContext) (access.SemanticAttributeDefinition, error) {
	return r.SetSemanticAttributeEnabledExpected(ctx, name, enabled, 0, mutation)
}

func (r *Repository) SetSemanticAttributeEnabledExpected(ctx context.Context, name string, enabled bool, expectedVersion int64, mutation access.SemanticAttributeMutationContext) (access.SemanticAttributeDefinition, error) {
	if err := semanticvalue.ValidateAttributeName(name); err != nil {
		return access.SemanticAttributeDefinition{}, err
	}
	if expectedVersion < 0 {
		return access.SemanticAttributeDefinition{}, errors.New("semantic attribute expected version cannot be negative")
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
		queries := accessdb.New(transactional.db)
		locked, err := queries.LockSemanticAttributeRegistry(ctx)
		if err != nil {
			return access.AuditEventInput{}, fmt.Errorf("lock semantic attribute registry: %w", err)
		}
		existing, err := queries.GetSemanticAttributeDefinition(ctx, name)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		result = semanticAttributeDefinitionFromGet(existing)
		if expectedVersion > 0 && result.DefinitionVersion != expectedVersion {
			return access.AuditEventInput{}, fmt.Errorf("%w: semantic attribute %s expected version %d, current %d", access.ErrSemanticAttributeConflict, name, expectedVersion, result.DefinitionVersion)
		}
		action := "semantic_attribute.disable"
		if enabled {
			action = "semantic_attribute.enable"
		}
		if result.Enabled == enabled {
			return semanticAttributeAuditEvent(mutation, actorID, action+"_replay", result, locked.RegistryRevision, locked.RegistryDigest), nil
		}
		updated, err := queries.SetSemanticAttributeDefinitionEnabled(ctx, accessdb.SetSemanticAttributeDefinitionEnabledParams{Enabled: enabled, Name: name, ExpectedVersion: expectedVersion})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) && expectedVersion > 0 {
				return access.AuditEventInput{}, fmt.Errorf("%w: semantic attribute %s expected version %d", access.ErrSemanticAttributeConflict, name, expectedVersion)
			}
			return access.AuditEventInput{}, fmt.Errorf("set semantic attribute enabled state: %w", err)
		}
		result = semanticAttributeDefinitionFromEnabled(updated)
		registry, err := refreshSemanticAttributeRegistry(ctx, queries, locked.RegistryRevision+1)
		if err != nil {
			return access.AuditEventInput{}, err
		}
		return semanticAttributeAuditEvent(mutation, actorID, action, result, registry.RegistryRevision, registry.RegistryDigest), nil
	})
	return result, err
}

func (r *Repository) ValidateSemanticAttributeValue(ctx context.Context, name string, input any) (access.CanonicalSemanticAttributeValue, error) {
	definition, err := r.SemanticAttributeDefinition(ctx, name)
	if err != nil {
		return access.CanonicalSemanticAttributeValue{}, err
	}
	if !definition.Enabled {
		return access.CanonicalSemanticAttributeValue{}, fmt.Errorf("%w: %s", access.ErrSemanticAttributeDisabled, name)
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

func refreshSemanticAttributeRegistry(ctx context.Context, queries *accessdb.Queries, revision int64) (accessdb.UpdateSemanticAttributeRegistryRow, error) {
	rows, err := queries.ListSemanticAttributeDefinitions(ctx)
	if err != nil {
		return accessdb.UpdateSemanticAttributeRegistryRow{}, fmt.Errorf("list semantic attribute definitions for digest: %w", err)
	}
	definitions := make([]access.SemanticAttributeDefinition, len(rows))
	for index, row := range rows {
		definitions[index] = semanticAttributeDefinitionFromList(row)
	}
	digest, err := semanticAttributeRegistryDigest(semanticvalue.Profile, definitions)
	if err != nil {
		return accessdb.UpdateSemanticAttributeRegistryRow{}, err
	}
	state, err := queries.UpdateSemanticAttributeRegistry(ctx, accessdb.UpdateSemanticAttributeRegistryParams{RegistryRevision: revision, RegistryDigest: digest})
	if err != nil {
		return accessdb.UpdateSemanticAttributeRegistryRow{}, fmt.Errorf("update semantic attribute registry state: %w", err)
	}
	return state, nil
}

func semanticAttributeRegistryDigest(profile string, definitions []access.SemanticAttributeDefinition) (string, error) {
	wire := registryDigestWire{Profile: profile, Definitions: make([]registryDefinitionDigestWire, len(definitions))}
	for index, definition := range definitions {
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

func semanticAttributeDefinitionFromGet(row accessdb.GetSemanticAttributeDefinitionRow) access.SemanticAttributeDefinition {
	return semanticAttributeDefinition(semanticAttributeRow{ID: row.DefinitionID, Name: row.Name, ValueType: row.ValueType, ValueShape: row.ValueShape, Profile: row.Profile, Version: row.DefinitionVersion, OwnerKind: row.OwnerKind, OwnerID: row.OwnerID, DisplayName: row.DisplayName, Description: row.Description, DocumentationURL: row.DocumentationUrl, Enabled: row.Enabled, DisabledAt: row.DisabledAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
}

func semanticAttributeDefinitionFromGetByID(row accessdb.GetSemanticAttributeDefinitionByIDRow) access.SemanticAttributeDefinition {
	return semanticAttributeDefinition(semanticAttributeRow{ID: row.DefinitionID, Name: row.Name, ValueType: row.ValueType, ValueShape: row.ValueShape, Profile: row.Profile, Version: row.DefinitionVersion, OwnerKind: row.OwnerKind, OwnerID: row.OwnerID, DisplayName: row.DisplayName, Description: row.Description, DocumentationURL: row.DocumentationUrl, Enabled: row.Enabled, DisabledAt: row.DisabledAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
}

func semanticAttributeDefinitionFromInsert(row accessdb.InsertSemanticAttributeDefinitionRow) access.SemanticAttributeDefinition {
	return semanticAttributeDefinition(semanticAttributeRow{ID: row.DefinitionID, Name: row.Name, ValueType: row.ValueType, ValueShape: row.ValueShape, Profile: row.Profile, Version: row.DefinitionVersion, OwnerKind: row.OwnerKind, OwnerID: row.OwnerID, DisplayName: row.DisplayName, Description: row.Description, DocumentationURL: row.DocumentationUrl, Enabled: row.Enabled, DisabledAt: row.DisabledAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
}

func semanticAttributeDefinitionFromList(row accessdb.ListSemanticAttributeDefinitionsRow) access.SemanticAttributeDefinition {
	return semanticAttributeDefinition(semanticAttributeRow{ID: row.DefinitionID, Name: row.Name, ValueType: row.ValueType, ValueShape: row.ValueShape, Profile: row.Profile, Version: row.DefinitionVersion, OwnerKind: row.OwnerKind, OwnerID: row.OwnerID, DisplayName: row.DisplayName, Description: row.Description, DocumentationURL: row.DocumentationUrl, Enabled: row.Enabled, DisabledAt: row.DisabledAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
}

func semanticAttributeDefinitionFromSearch(row accessdb.SearchSemanticAttributeDefinitionsRow) access.SemanticAttributeDefinition {
	return semanticAttributeDefinition(semanticAttributeRow{ID: row.DefinitionID, Name: row.Name, ValueType: row.ValueType, ValueShape: row.ValueShape, Profile: row.Profile, Version: row.DefinitionVersion, OwnerKind: row.OwnerKind, OwnerID: row.OwnerID, DisplayName: row.DisplayName, Description: row.Description, DocumentationURL: row.DocumentationUrl, Enabled: row.Enabled, DisabledAt: row.DisabledAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
}

func semanticAttributeDefinitionFromMetadata(row accessdb.UpdateSemanticAttributeDefinitionMetadataRow) access.SemanticAttributeDefinition {
	return semanticAttributeDefinition(semanticAttributeRow{ID: row.DefinitionID, Name: row.Name, ValueType: row.ValueType, ValueShape: row.ValueShape, Profile: row.Profile, Version: row.DefinitionVersion, OwnerKind: row.OwnerKind, OwnerID: row.OwnerID, DisplayName: row.DisplayName, Description: row.Description, DocumentationURL: row.DocumentationUrl, Enabled: row.Enabled, DisabledAt: row.DisabledAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
}

func semanticAttributeDefinitionFromEnabled(row accessdb.SetSemanticAttributeDefinitionEnabledRow) access.SemanticAttributeDefinition {
	return semanticAttributeDefinition(semanticAttributeRow{ID: row.DefinitionID, Name: row.Name, ValueType: row.ValueType, ValueShape: row.ValueShape, Profile: row.Profile, Version: row.DefinitionVersion, OwnerKind: row.OwnerKind, OwnerID: row.OwnerID, DisplayName: row.DisplayName, Description: row.Description, DocumentationURL: row.DocumentationUrl, Enabled: row.Enabled, DisabledAt: row.DisabledAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
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
		LifecycleState: lifecycle, Enabled: row.Enabled, DisabledAt: row.DisabledAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func validateSemanticAttributeOwnerExists(ctx context.Context, db DBTX, metadata access.SemanticAttributeMetadata) error {
	if metadata.Owner.Kind == access.SemanticAttributeOwnerInstance {
		return nil
	}
	ownerID, err := pgUUID(metadata.Owner.ID)
	if err != nil {
		return fmt.Errorf("validate semantic attribute owner: %w", err)
	}
	var exists bool
	queries := accessdb.New(db)
	switch metadata.Owner.Kind {
	case access.SemanticAttributeOwnerPrincipal:
		exists, err = queries.SemanticAttributePrincipalOwnerExists(ctx, ownerID)
	case access.SemanticAttributeOwnerGroup:
		exists, err = queries.SemanticAttributeGroupOwnerExists(ctx, ownerID)
	default:
		return fmt.Errorf("semantic attribute owner %s is invalid", metadata.Owner.Kind)
	}
	if err != nil {
		return fmt.Errorf("validate semantic attribute owner: %w", err)
	}
	if !exists {
		return fmt.Errorf("semantic attribute owner %s %q does not exist", metadata.Owner.Kind, metadata.Owner.ID)
	}
	return nil
}
