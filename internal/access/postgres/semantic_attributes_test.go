package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestSemanticAttributeRegistryDigestIsProfileQualifiedAndDeterministic(t *testing.T) {
	empty, err := semanticAttributeRegistryDigest(semanticvalue.Profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	const wantEmpty = "sha256:9362dbdb62923a10f67bc1da04b02e2bbad74dce5b5442aaa3fb5e0cc5851b9d"
	if empty != wantEmpty || !strings.Contains(SchemaSQL(), wantEmpty) {
		t.Fatalf("empty registry digest = %q, want migration seed %q", empty, wantEmpty)
	}
	definitions := []access.SemanticAttributeDefinition{{
		ID: "10000000-0000-0000-0000-000000000001", Name: "region",
		Type: semanticvalue.TypeString, Shape: access.SemanticAttributeList,
		DefinitionVersion: 1, Enabled: true, LifecycleState: access.SemanticAttributeActive,
		Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}},
	}}
	first, err := semanticAttributeRegistryDigest(semanticvalue.Profile, definitions)
	if err != nil {
		t.Fatal(err)
	}
	second, err := semanticAttributeRegistryDigest(semanticvalue.Profile, append([]access.SemanticAttributeDefinition(nil), definitions...))
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == empty {
		t.Fatalf("registry digests = %q/%q, empty %q", first, second, empty)
	}
	definitions[0].Enabled = false
	definitions[0].LifecycleState = access.SemanticAttributeDisabled
	definitions[0].DefinitionVersion++
	disabled, err := semanticAttributeRegistryDigest(semanticvalue.Profile, definitions)
	if err != nil {
		t.Fatal(err)
	}
	if disabled == first {
		t.Fatal("disablement and definition version did not change registry identity")
	}
}

func TestSemanticAttributeDefinitionInputUsesCanonicalValueContract(t *testing.T) {
	for _, test := range []struct {
		name      string
		valueType semanticvalue.Type
		shape     access.SemanticAttributeShape
		wantErr   bool
	}{
		{name: "region", valueType: semanticvalue.TypeString, shape: access.SemanticAttributeScalar},
		{name: "region_ids", valueType: semanticvalue.TypeInteger, shape: access.SemanticAttributeList},
		{name: "bad-name", valueType: semanticvalue.TypeString, shape: access.SemanticAttributeScalar, wantErr: true},
		{name: "region", valueType: semanticvalue.Type("Float"), shape: access.SemanticAttributeScalar, wantErr: true},
		{name: "region", valueType: semanticvalue.TypeString, shape: access.SemanticAttributeShape("map"), wantErr: true},
	} {
		_, err := validateSemanticAttributeDefinitionInput(test.name, test.valueType, test.shape, access.SemanticAttributeMetadata{})
		if (err != nil) != test.wantErr {
			t.Errorf("validate(%q, %q, %q) error = %v, wantErr %v", test.name, test.valueType, test.shape, err, test.wantErr)
		}
	}
}

func TestSemanticAttributeMetadataAndCompatibilityValidation(t *testing.T) {
	metadata, err := canonicalSemanticAttributeMetadata(access.SemanticAttributeMetadata{
		DisplayName: "  Region  ", DocumentationURL: "https://docs.example.com/region",
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Owner.Kind != access.SemanticAttributeOwnerInstance || metadata.DisplayName != "Region" {
		t.Fatalf("canonical metadata = %#v", metadata)
	}
	for _, invalid := range []access.SemanticAttributeMetadata{
		{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance, ID: auditActorID}},
		{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerPrincipal, ID: "not-a-uuid"}},
		{DocumentationURL: "http://docs.example.com/region"},
		{DocumentationURL: "https://user:secret@docs.example.com/region"},
	} {
		if _, err := canonicalSemanticAttributeMetadata(invalid); err == nil {
			t.Fatalf("accepted invalid metadata %#v", invalid)
		}
	}
	current := access.SemanticAttributeDefinition{ID: auditActorID, Name: "region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile}
	candidate := current
	candidate.Metadata.DisplayName = "Region"
	if err := access.ValidateSemanticAttributeCompatibility(current, candidate); err != nil {
		t.Fatalf("metadata-only compatibility error = %v", err)
	}
	candidate.Type = semanticvalue.TypeBoolean
	if err := access.ValidateSemanticAttributeCompatibility(current, candidate); !errors.Is(err, access.ErrSemanticAttributeConflict) {
		t.Fatalf("type compatibility error = %v", err)
	}
}

func TestSemanticAttributeRegistryPostgreSQL18DefinitionLifecycle(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	if _, err := db.admin.Exec(t.Context(), `
		INSERT INTO access.principal (id, principal_type, status)
		VALUES ($1::uuid, 'user', 'active')`, auditActorID); err != nil {
		t.Fatal(err)
	}
	repo, err := NewAccess(db.admin, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}

	empty, err := repo.SemanticAttributeRegistry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if empty.State.Revision != 0 || len(empty.Definitions) != 0 {
		t.Fatalf("empty registry = %#v", empty)
	}

	input := access.RegisterSemanticAttributeInput{
		Name: "region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeList,
		Metadata: access.SemanticAttributeMetadata{
			Owner:       access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance},
			DisplayName: "Region", Description: "Authorized sales regions",
			DocumentationURL: "https://docs.example.com/attributes/region",
		},
		Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: auditActorID},
	}
	definition, err := repo.RegisterSemanticAttribute(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Name != "region" || definition.Type != semanticvalue.TypeString || definition.Shape != access.SemanticAttributeList || !definition.Enabled || definition.LifecycleState != access.SemanticAttributeActive || definition.DefinitionVersion != 1 || definition.Metadata.DisplayName != "Region" {
		t.Fatalf("registered definition = %#v", definition)
	}
	registered, err := repo.SemanticAttributeRegistry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if registered.State.Revision != 1 || registered.State.Digest == empty.State.Digest || len(registered.Definitions) != 1 {
		t.Fatalf("registered registry = %#v", registered)
	}

	replayed, err := repo.RegisterSemanticAttribute(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != definition.ID {
		t.Fatalf("register replay id = %q, want %q", replayed.ID, definition.ID)
	}
	afterReplay, err := repo.SemanticAttributeRegistry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if afterReplay.State.Revision != registered.State.Revision || afterReplay.State.Digest != registered.State.Digest {
		t.Fatal("idempotent registration changed registry identity")
	}

	conflict := input
	conflict.Type = semanticvalue.TypeBoolean
	if _, err := repo.RegisterSemanticAttribute(t.Context(), conflict); !errors.Is(err, access.ErrSemanticAttributeConflict) {
		t.Fatalf("type mutation registration error = %v, want conflict", err)
	}

	byID, err := repo.SemanticAttributeDefinitionByID(t.Context(), definition.ID)
	if err != nil || byID.Name != "region" {
		t.Fatalf("fetch by id = %#v, %v", byID, err)
	}
	search, err := repo.SearchSemanticAttributes(t.Context(), access.SemanticAttributeSearch{Query: "sales regions", Limit: 10})
	if err != nil || len(search) != 1 || search[0].ID != definition.ID {
		t.Fatalf("search = %#v, %v", search, err)
	}
	canonical, err := repo.ValidateSemanticAttributeValue(t.Context(), "region", []string{"west", "east", "west"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(canonical.CanonicalValues, ",") != "east,west" || !strings.HasPrefix(canonical.Digest, "sha256:") {
		t.Fatalf("canonical value = %#v", canonical)
	}

	updatedMetadata := input.Metadata
	updatedMetadata.DisplayName = "Regional access"
	updated, err := repo.UpdateSemanticAttributeMetadata(t.Context(), access.UpdateSemanticAttributeMetadataInput{Name: "region", Metadata: updatedMetadata, Mutation: input.Mutation})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DefinitionVersion != 2 || updated.Metadata.DisplayName != "Regional access" || updated.ID != definition.ID {
		t.Fatalf("metadata update = %#v", updated)
	}

	disabled, err := repo.SetSemanticAttributeEnabled(t.Context(), "region", false, input.Mutation)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Enabled || disabled.LifecycleState != access.SemanticAttributeDisabled || disabled.DisabledAt == "" || disabled.DefinitionVersion != 3 {
		t.Fatalf("disabled definition = %#v", disabled)
	}
	afterDisable, err := repo.SemanticAttributeRegistry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if afterDisable.State.Revision != 3 || afterDisable.State.Digest == registered.State.Digest {
		t.Fatalf("disabled registry = %#v", afterDisable.State)
	}
	if _, err := repo.ValidateSemanticAttributeValue(t.Context(), "region", []string{"west"}); !errors.Is(err, access.ErrSemanticAttributeDisabled) {
		t.Fatalf("disabled value validation error = %v", err)
	}

	if _, err := db.admin.Exec(t.Context(), `UPDATE access.semantic_attribute_definition SET value_type='Boolean' WHERE name='region'`); err == nil {
		t.Fatal("database accepted semantic attribute type mutation")
	}
	if _, err := db.admin.Exec(t.Context(), `DELETE FROM access.semantic_attribute_definition WHERE name='region'`); err == nil {
		t.Fatal("database accepted semantic attribute deletion")
	}
	if _, err := db.admin.Exec(t.Context(), `
		UPDATE access.semantic_attribute_definition
		SET disabled_at=disabled_at - interval '1 day',
		    definition_version=definition_version + 1
		WHERE name='region'`); err == nil {
		t.Fatal("database accepted a caller-authored lifecycle timestamp")
	}

	var auditCount int
	if err := db.admin.QueryRow(t.Context(), `
		SELECT count(*) FROM audit.audit_event
		WHERE resource_kind='semantic_attribute' AND resource_id='region'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 4 {
		t.Fatalf("semantic attribute audit events = %d, want register, replay, metadata, disable", auditCount)
	}
}
