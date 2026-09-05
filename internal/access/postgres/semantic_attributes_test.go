package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/trustedclaims"
	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/semanticvalue"
)

type semanticAttributeClaimsVerifier struct {
	claims trustedclaims.VerifiedClaims
}

func (v semanticAttributeClaimsVerifier) SourceKind() trustedclaims.SourceKind {
	return trustedclaims.SourceOIDC
}

func (v semanticAttributeClaimsVerifier) Verify(context.Context, []byte) (trustedclaims.VerifiedClaims, error) {
	return v.claims, nil
}

func TestSemanticAttributeRegistryDigestIsProfileQualifiedAndDeterministic(t *testing.T) {
	empty, err := semanticAttributeRegistryDigest(semanticvalue.Profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	const wantEmpty = "sha256:9362dbdb62923a10f67bc1da04b02e2bbad74dce5b5442aaa3fb5e0cc5851b9d"
	if empty != wantEmpty || !strings.Contains(migrations.TypedAttributeRegistrySQL(), wantEmpty) {
		t.Fatalf("empty registry digest = %q, want migration seed %q", empty, wantEmpty)
	}
	definitions := []access.SemanticAttributeDefinition{{
		ID: "10000000-0000-0000-0000-000000000001", Name: "region",
		Type: semanticvalue.TypeString, Shape: access.SemanticAttributeList,
		Profile: semanticvalue.Profile, DefinitionVersion: 1, Enabled: true, LifecycleState: access.SemanticAttributeActive,
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
	other := access.SemanticAttributeDefinition{
		ID: "10000000-0000-0000-0000-000000000002", Name: "Region",
		Type: semanticvalue.TypeBoolean, Shape: access.SemanticAttributeScalar,
		Profile: semanticvalue.Profile, DefinitionVersion: 1, Enabled: true,
		LifecycleState: access.SemanticAttributeActive,
		Metadata:       access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}},
	}
	forward, err := semanticAttributeRegistryDigest(semanticvalue.Profile, []access.SemanticAttributeDefinition{definitions[0], other})
	if err != nil {
		t.Fatal(err)
	}
	reverse, err := semanticAttributeRegistryDigest(semanticvalue.Profile, []access.SemanticAttributeDefinition{other, definitions[0]})
	if err != nil {
		t.Fatal(err)
	}
	if forward != reverse {
		t.Fatalf("registry digest depends on input order: %q / %q", forward, reverse)
	}
}

func TestSemanticAttributeDefinitionValidationUsesCanonicalValueContract(t *testing.T) {
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

func TestSemanticAttributeMetadataValidationCanonicalizesAndRejectsUnsafeValues(t *testing.T) {
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
		{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance, ID: "10000000-0000-0000-0000-000000000001"}},
		{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerPrincipal, ID: "not-a-uuid"}},
		{DocumentationURL: "http://docs.example.com/region"},
		{DocumentationURL: "https://user:secret@docs.example.com/region"},
	} {
		if _, err := canonicalSemanticAttributeMetadata(invalid); err == nil {
			t.Fatalf("accepted invalid metadata %#v", invalid)
		}
	}
}

func TestSemanticAttributeCompatibilityValidationRejectsIdentityChanges(t *testing.T) {
	current := access.SemanticAttributeDefinition{
		ID: "10000000-0000-0000-0000-000000000001", Name: "region",
		Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile,
	}
	candidate := current
	candidate.Type = semanticvalue.TypeBoolean
	if err := access.ValidateSemanticAttributeCompatibility(current, candidate); !errors.Is(err, access.ErrSemanticAttributeConflict) {
		t.Fatalf("type compatibility error = %v", err)
	}
}

func TestCanonicalSemanticAttributeValuePreservesExactValuesAndBounds(t *testing.T) {
	definition := access.SemanticAttributeDefinition{
		ID: "10000000-0000-0000-0000-000000000001", Name: "amounts",
		Type: semanticvalue.TypeInteger, Shape: access.SemanticAttributeScalar,
		Profile: semanticvalue.Profile, DefinitionVersion: 7, Enabled: true,
	}
	integer, err := canonicalSemanticAttributeValue(definition, json.Number("9007199254740993"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(integer.CanonicalValues, ","); got != "9007199254740993" {
		t.Fatalf("exact integer = %q", got)
	}

	definition.Type = semanticvalue.TypeDecimal
	decimal, err := canonicalSemanticAttributeValue(definition, json.Number("1.2300"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(decimal.CanonicalValues, ","); got != "1.23" {
		t.Fatalf("exact decimal = %q", got)
	}

	definition.Type, definition.Shape = semanticvalue.TypeString, access.SemanticAttributeList
	first, err := canonicalSemanticAttributeValue(definition, []string{"west", "east", "west"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonicalSemanticAttributeValue(definition, [3]string{"east", "west", "east"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(first.CanonicalValues, ",") != "east,west" || first.Digest != second.Digest {
		t.Fatalf("canonical sets differ: %#v / %#v", first, second)
	}

	tooMany := make([]string, semanticvalue.MaxSetValues+1)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("value-%04d", index)
	}
	for name, input := range map[string]any{
		"null":       nil,
		"empty":      []string{},
		"structured": map[string]any{"region": "west"},
		"nested":     [][]string{{"west"}},
		"too-many":   tooMany,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := canonicalSemanticAttributeValue(definition, input); err == nil {
				t.Fatalf("accepted invalid value %#v", input)
			}
		})
	}

	definition.Enabled = false
	if _, err := canonicalSemanticAttributeValue(definition, []string{"west"}); !errors.Is(err, access.ErrSemanticAttributeDisabled) {
		t.Fatalf("disabled definition error = %v", err)
	}
}

func TestCanonicalSemanticAttributeValueSupportsEveryV1ScalarType(t *testing.T) {
	for _, test := range []struct {
		typeName semanticvalue.Type
		input    any
		want     string
	}{
		{semanticvalue.TypeString, "west", "west"},
		{semanticvalue.TypeBoolean, true, "true"},
		{semanticvalue.TypeInteger, json.Number("42"), "42"},
		{semanticvalue.TypeDecimal, json.Number("42.500"), "42.5"},
		{semanticvalue.TypeDate, "2026-09-05", "2026-09-05"},
		{semanticvalue.TypeTimestamp, "2026-09-05T12:34:56.123456-04:00", "2026-09-05T16:34:56.123456Z"},
	} {
		t.Run(string(test.typeName), func(t *testing.T) {
			definition := access.SemanticAttributeDefinition{
				ID: "10000000-0000-0000-0000-000000000001", Name: "attribute",
				Type: test.typeName, Shape: access.SemanticAttributeScalar,
				Profile: semanticvalue.Profile, DefinitionVersion: 1, Enabled: true,
			}
			value, err := canonicalSemanticAttributeValue(definition, test.input)
			if err != nil {
				t.Fatal(err)
			}
			if len(value.CanonicalValues) != 1 || value.CanonicalValues[0] != test.want {
				t.Fatalf("canonical value = %#v, want %q", value.CanonicalValues, test.want)
			}
		})
	}
}

func TestSemanticAttributeRegistryPostgreSQL18DefinitionLifecycle(t *testing.T) {
	db := newAuditDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
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
	rollbackInput := input
	rollbackInput.Name = "rollback_only"
	rollbackInput.Mutation.RequestID = "not-a-uuid"
	if _, err := repo.RegisterSemanticAttribute(t.Context(), rollbackInput); err == nil {
		t.Fatal("register with invalid audit evidence unexpectedly succeeded")
	}
	afterRollback, err := repo.SemanticAttributeRegistry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if afterRollback.State.Revision != registered.State.Revision || afterRollback.State.Digest != registered.State.Digest || len(afterRollback.Definitions) != 1 {
		t.Fatalf("failed audit did not roll back registry mutation: %#v", afterRollback)
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
		SET disabled_at=disabled_at - interval '1 day', definition_version=definition_version + 1
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
	if _, err := db.admin.Exec(t.Context(), `
		UPDATE access.semantic_attribute_definition
		SET description='out-of-band change', definition_version=definition_version+1
		WHERE name='region'`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SemanticAttributeRegistry(t.Context()); err == nil || !strings.Contains(err.Error(), "registry digest mismatch") {
		t.Fatalf("out-of-band stale registry digest error = %v", err)
	}
}

func TestSemanticAttributeRegistryPostgreSQL18ConcurrentRegistration(t *testing.T) {
	db := newAuditDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	input := access.RegisterSemanticAttributeInput{
		Name: "concurrent_region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar,
		Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: auditActorID},
	}
	results := make([]access.SemanticAttributeDefinition, 2)
	errorsFound := make([]error, 2)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errorsFound[index] = repo.RegisterSemanticAttribute(t.Context(), input)
		}(index)
	}
	wait.Wait()
	for _, err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	if results[0].ID == "" || results[0].ID != results[1].ID {
		t.Fatalf("concurrent registration identities = %q / %q", results[0].ID, results[1].ID)
	}
	snapshot, err := repo.SemanticAttributeRegistry(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State.Revision != 1 || len(snapshot.Definitions) != 1 {
		t.Fatalf("concurrent registry = %#v", snapshot)
	}
}

func TestSemanticAttributeControlRowsRespectDefinitionVersionAcrossLifecycleChanges(t *testing.T) {
	db := newAuditDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	mutation := access.SemanticAttributeMutationContext{ActorPrincipalID: auditActorID}
	definition, err := repo.RegisterSemanticAttribute(t.Context(), access.RegisterSemanticAttributeInput{
		Name: "lifecycle_region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar,
		Mutation: mutation,
	})
	if err != nil {
		t.Fatal(err)
	}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: auditActorID}
	assignment, err := repo.SetSemanticAttributeAssignment(t.Context(), access.SemanticAttributeAssignmentInput{
		DefinitionID: definition.ID, Subject: subject, Values: "west", Mutation: mutation,
	})
	if err != nil {
		t.Fatal(err)
	}
	mapping, err := repo.SetTrustedClaimMapping(t.Context(), access.TrustedClaimMappingInput{
		SourceKind: access.TrustedClaimSourceOIDC, Provider: "corp", Issuer: "https://issuer.example",
		Audience: "dashboard", Claim: "region", DefinitionID: definition.ID, Mutation: mutation,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	envelope, err := trustedclaims.Verify(t.Context(), trustedclaims.RawEvidence{Source: trustedclaims.SourceOIDC, Raw: []byte("signed")}, semanticAttributeClaimsVerifier{claims: trustedclaims.VerifiedClaims{
		Provider: "corp", Issuer: "https://issuer.example", Audience: "dashboard", Subject: auditActorID,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(10 * time.Minute),
		CredentialFingerprint: strings.Repeat("a", 64), Claims: []trustedclaims.Claim{{Name: "region", Value: "west"}},
	}}, trustedclaims.VerifyOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}

	metadata := definition.Metadata
	metadata.DisplayName = "Lifecycle region"
	definition, err = repo.UpdateSemanticAttributeMetadata(t.Context(), access.UpdateSemanticAttributeMetadataInput{
		Name: definition.Name, Metadata: metadata, Mutation: mutation,
	})
	if err != nil || definition.DefinitionVersion != 2 {
		t.Fatalf("metadata version bump = %#v, %v", definition, err)
	}
	if _, err := repo.EffectiveDirectSemanticAttributeAssignments(t.Context(), subject); !errors.Is(err, access.ErrSemanticAttributeSourceConflict) {
		t.Fatalf("stale direct assignment resolution = %v, want fail-closed source conflict", err)
	}
	if _, err := repo.EffectiveSemanticAttributeAssignments(t.Context(), subject, envelope); !errors.Is(err, access.ErrSemanticAttributeSourceConflict) {
		t.Fatalf("stale trusted mapping resolution = %v, want fail-closed source conflict", err)
	}

	replacement, err := repo.SetSemanticAttributeAssignment(t.Context(), access.SemanticAttributeAssignmentInput{
		AssignmentID: assignment.ID, DefinitionID: definition.ID, Subject: subject, Values: "west",
		ExpectedVersion: assignment.AssignmentVersion, Mutation: mutation,
	})
	if err != nil {
		t.Fatalf("replace stale assignment: %v", err)
	}
	if replacement.ID == assignment.ID || replacement.DefinitionVersion != definition.DefinitionVersion || replacement.AssignmentVersion != 1 {
		t.Fatalf("assignment replacement = %#v, old=%#v", replacement, assignment)
	}
	mappingReplacement, err := repo.SetTrustedClaimMapping(t.Context(), access.TrustedClaimMappingInput{
		MappingID: mapping.ID, SourceKind: mapping.SourceKind, Provider: mapping.Provider, Issuer: mapping.Issuer,
		Audience: mapping.Audience, Claim: mapping.Claim, DefinitionID: definition.ID,
		ExpectedVersion: mapping.MappingVersion, Mutation: mutation,
	})
	if err != nil {
		t.Fatalf("replace stale trusted mapping: %v", err)
	}
	if mappingReplacement.ID == mapping.ID || mappingReplacement.DefinitionVersion != definition.DefinitionVersion || mappingReplacement.MappingVersion != 1 {
		t.Fatalf("mapping replacement = %#v, old=%#v", mappingReplacement, mapping)
	}
	if resolved, err := repo.EffectiveSemanticAttributeAssignments(t.Context(), subject, envelope); err != nil || len(resolved) != 1 || resolved[0].Source != "direct+trusted_claim" {
		t.Fatalf("resolved replacement = %#v, %v", resolved, err)
	}

	definition, err = repo.SetSemanticAttributeEnabledExpected(t.Context(), definition.Name, false, definition.DefinitionVersion, mutation)
	if err != nil || definition.Enabled || definition.DefinitionVersion != 3 {
		t.Fatalf("disable version bump = %#v, %v", definition, err)
	}
	definition, err = repo.SetSemanticAttributeEnabledExpected(t.Context(), definition.Name, true, definition.DefinitionVersion, mutation)
	if err != nil || !definition.Enabled || definition.DefinitionVersion != 4 {
		t.Fatalf("restore version bump = %#v, %v", definition, err)
	}
	if _, err := repo.EffectiveDirectSemanticAttributeAssignments(t.Context(), subject); !errors.Is(err, access.ErrSemanticAttributeSourceConflict) {
		t.Fatalf("restored stale direct assignment resolution = %v, want fail-closed source conflict", err)
	}
	if _, err := repo.EffectiveSemanticAttributeAssignments(t.Context(), subject, envelope); !errors.Is(err, access.ErrSemanticAttributeSourceConflict) {
		t.Fatalf("restored stale trusted mapping resolution = %v, want fail-closed source conflict", err)
	}

	if revoked, err := repo.TombstoneSemanticAttributeAssignment(t.Context(), replacement.ID, replacement.AssignmentVersion, mutation); err != nil || !revoked.Tombstoned {
		t.Fatalf("revoke stale assignment after lifecycle bump = %#v, %v", revoked, err)
	}
	if revoked, err := repo.TombstoneTrustedClaimMapping(t.Context(), mappingReplacement.ID, mappingReplacement.MappingVersion, mutation); err != nil || !revoked.Tombstoned {
		t.Fatalf("revoke stale mapping after lifecycle bump = %#v, %v", revoked, err)
	}
}
