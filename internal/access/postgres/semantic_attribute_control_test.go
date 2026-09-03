package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/trustedclaims"
	"github.com/flidai/leapview/internal/semanticvalue"
)

const (
	controlSubjectID = "30000000-0000-0000-0000-000000000001"
	controlOwnerID   = "30000000-0000-0000-0000-000000000002"
	controlGroupID   = "30000000-0000-0000-0000-000000000003"
)

func TestSemanticAttributeControlDigestSeedAndOrdering(t *testing.T) {
	empty, err := semanticAttributeControlDigest(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	const wantEmpty = "sha256:e05005cdeee20cc98d9e8de8f32ed4b8da34a95f82872dc3b65a451ce7de4e37"
	if empty != wantEmpty || !strings.Contains(SemanticAttributeControlMigrationSQL(), wantEmpty) {
		t.Fatalf("empty control digest = %q, want %q", empty, wantEmpty)
	}
	rows := []access.SemanticAttributeAssignment{
		{ID: "20000000-0000-0000-0000-000000000002", DefinitionID: "10000000-0000-0000-0000-000000000002", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID}, Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, CanonicalValues: []string{"b"}, ValueDigest: "sha256:" + strings.Repeat("b", 64), AssignmentVersion: 1},
		{ID: "20000000-0000-0000-0000-000000000001", DefinitionID: "10000000-0000-0000-0000-000000000001", Subject: access.SubjectRef{Kind: access.SubjectKindGroup, ID: controlGroupID}, Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, CanonicalValues: []string{"a"}, ValueDigest: "sha256:" + strings.Repeat("a", 64), AssignmentVersion: 1},
	}
	first, err := semanticAttributeControlDigest(rows, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := semanticAttributeControlDigest([]access.SemanticAttributeAssignment{rows[1], rows[0]}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("control digest depends on input order: %q != %q", first, second)
	}
}

func TestTrustedClaimMappingIdentityRejectsNormalization(t *testing.T) {
	valid := access.TrustedClaimSource{Kind: access.TrustedClaimSourceOIDC, Provider: "corp", Issuer: "https://issuer.example", Audience: "dashboard"}
	for name, source := range map[string]access.TrustedClaimSource{
		"provider whitespace": {Kind: valid.Kind, Provider: " corp", Issuer: valid.Issuer, Audience: valid.Audience},
		"issuer whitespace":   {Kind: valid.Kind, Provider: valid.Provider, Issuer: valid.Issuer + " ", Audience: valid.Audience},
		"audience control":    {Kind: valid.Kind, Provider: valid.Provider, Issuer: valid.Issuer, Audience: "dashboard\u0007"},
	} {
		if _, err := canonicalTrustedClaimSource(source); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
	for _, claim := range []string{" claim", "claim ", "claim\u0007", ""} {
		if _, _, err := canonicalTrustedClaim(valid, claim); err == nil {
			t.Errorf("accepted invalid claim %q", claim)
		}
	}
}

type staticControlVerifier struct {
	source trustedclaims.SourceKind
	claims trustedclaims.VerifiedClaims
}

func (v staticControlVerifier) SourceKind() trustedclaims.SourceKind { return v.source }
func (v staticControlVerifier) Verify(context.Context, []byte) (trustedclaims.VerifiedClaims, error) {
	return v.claims, nil
}

func TestEffectiveSemanticAttributeAssignmentsRejectsEnvelopeSubjectMismatch(t *testing.T) {
	now := time.Now().UTC()
	envelope, err := trustedclaims.Verify(t.Context(), trustedclaims.NewRawEvidence(trustedclaims.SourceOIDC, []byte("signed")), staticControlVerifier{
		source: trustedclaims.SourceOIDC,
		claims: trustedclaims.VerifiedClaims{
			Provider: "corp", Issuer: "https://issuer.example", Audience: "dashboard", Subject: controlSubjectID,
			IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute), TokenFingerprint: "sha256:" + strings.Repeat("a", 64),
		},
	}, trustedclaims.VerifyOptions{Now: now})
	if err != nil {
		t.Fatalf("verify test envelope: %v", err)
	}

	// The identity binding is checked before any database access, so a
	// mismatched envelope cannot contribute claims to another principal.
	repo := &Repository{}
	_, err = repo.EffectiveSemanticAttributeAssignments(t.Context(), access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlOwnerID}, envelope)
	if !errors.Is(err, trustedclaims.ErrInvalidEvidence) {
		t.Fatalf("mismatched envelope subject error = %v, want invalid evidence", err)
	}
}

func hasEffectiveSemanticAttribute(rows []access.EffectiveSemanticAttribute, definitionID string, values []string) bool {
	for _, row := range rows {
		if row.DefinitionID != definitionID || len(row.CanonicalValues) != len(values) {
			continue
		}
		match := true
		for i := range values {
			if row.CanonicalValues[i] != values[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestSemanticAttributeControlAuditProjectionOmitsValues(t *testing.T) {
	assignment := access.SemanticAttributeAssignment{
		ID: "assignment-1", DefinitionID: "definition-1", DefinitionName: "region",
		Subject:         access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID},
		CanonicalValues: []string{"secret-region"}, ValueDigest: "sha256:" + strings.Repeat("a", 64), AssignmentVersion: 2,
	}
	event := semanticAttributeControlAudit(access.SemanticAttributeMutationContext{}, "semantic_attribute.assignment.set", assignment, semanticAttributeControlStateRow{Revision: 4, Digest: "sha256:" + strings.Repeat("b", 64)})
	var metadata map[string]any
	if err := json.Unmarshal([]byte(event.MetadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["valueCount"] != float64(1) || metadata["definitionId"] != assignment.DefinitionID {
		t.Fatalf("stable audit metadata = %#v", metadata)
	}
	for _, forbidden := range []string{"canonicalValues", "valueDigest", "secret-region"} {
		if strings.Contains(event.MetadataJSON, forbidden) {
			t.Fatalf("assignment audit contains %q: %s", forbidden, event.MetadataJSON)
		}
	}
}

func TestEffectiveSemanticAttributeAssignmentsRejectsOutOfBandRowCorruption(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	ctx := t.Context()
	if _, err := db.admin.Exec(ctx, AttributeRegistryMigrationSQL()); err != nil {
		t.Fatalf("apply attribute registry migration: %v", err)
	}
	if _, err := db.admin.Exec(ctx, SemanticAttributeControlMigrationSQL()); err != nil {
		t.Fatalf("apply semantic attribute control migration: %v", err)
	}
	for _, id := range []string{auditActorID, controlSubjectID} {
		if _, err := db.admin.Exec(ctx, `INSERT INTO access.principal (id, principal_type, status) VALUES ($1::uuid, 'user', 'active')`, id); err != nil {
			t.Fatalf("insert principal %s: %v", id, err)
		}
	}
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	mutation := access.SemanticAttributeMutationContext{ActorPrincipalID: auditActorID}
	definition, err := repo.RegisterSemanticAttribute(ctx, access.RegisterSemanticAttributeInput{
		Name: "integrity_region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Mutation: mutation,
	})
	if err != nil {
		t.Fatalf("register definition: %v", err)
	}
	assignment, err := repo.SetSemanticAttributeAssignment(ctx, access.SemanticAttributeAssignmentInput{
		DefinitionID: definition.ID, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID}, Values: "west", Mutation: mutation,
	})
	if err != nil {
		t.Fatalf("set assignment: %v", err)
	}
	if _, err := repo.SemanticAttributeControl(ctx); err != nil {
		t.Fatalf("validate initial control snapshot: %v", err)
	}

	// A privileged out-of-band writer can bypass the repository's control-state
	// advancement. Effective resolution must recompute the snapshot digest and
	// reject the row instead of trusting the unchanged revision/digest pair.
	if _, err := db.admin.Exec(ctx, `
		UPDATE access.semantic_attribute_assignment
		SET canonical_values = ARRAY['tampered'], assignment_version = assignment_version + 1
		WHERE assignment_id = $1::uuid`, assignment.ID); err != nil {
		t.Fatalf("corrupt assignment out of band: %v", err)
	}
	_, err = repo.EffectiveDirectSemanticAttributeAssignments(ctx, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID})
	if !errors.Is(err, access.ErrSemanticAttributeControlCorrupt) {
		t.Fatalf("out-of-band assignment corruption error = %v, want control corruption", err)
	}
}

func TestSemanticAttributeControlPostgreSQL18CanonicalTimestampsAreSessionIndependent(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	ctx := t.Context()
	if _, err := db.admin.Exec(ctx, AttributeRegistryMigrationSQL()); err != nil {
		t.Fatalf("apply attribute registry migration: %v", err)
	}
	if _, err := db.admin.Exec(ctx, SemanticAttributeControlMigrationSQL()); err != nil {
		t.Fatalf("apply semantic attribute control migration: %v", err)
	}
	for _, id := range []string{auditActorID, controlSubjectID} {
		if _, err := db.admin.Exec(ctx, `INSERT INTO access.principal (id, principal_type, status) VALUES ($1::uuid, 'user', 'active')`, id); err != nil {
			t.Fatalf("insert principal %s: %v", id, err)
		}
	}
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	mutation := access.SemanticAttributeMutationContext{ActorPrincipalID: auditActorID}
	definition, err := repo.RegisterSemanticAttribute(ctx, access.RegisterSemanticAttributeInput{
		Name: "timestamp_region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Mutation: mutation,
	})
	if err != nil {
		t.Fatalf("register definition: %v", err)
	}
	assignment, err := repo.SetSemanticAttributeAssignment(ctx, access.SemanticAttributeAssignmentInput{
		DefinitionID: definition.ID, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID}, Values: "west", Mutation: mutation,
	})
	if err != nil {
		t.Fatalf("set assignment: %v", err)
	}
	if _, err := repo.TombstoneSemanticAttributeAssignment(ctx, assignment.ID, assignment.AssignmentVersion, mutation); err != nil {
		t.Fatalf("tombstone assignment: %v", err)
	}
	baseline, err := repo.SemanticAttributeControl(ctx)
	if err != nil {
		t.Fatalf("read baseline control snapshot: %v", err)
	}

	conn, err := db.runtime.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire non-default session: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET TIME ZONE 'America/Los_Angeles'`); err != nil {
		t.Fatalf("set non-default timezone: %v", err)
	}
	if _, err := conn.Exec(ctx, `SET DateStyle = 'SQL, DMY'`); err != nil {
		t.Fatalf("set non-default datestyle: %v", err)
	}
	nonDefaultRepo, err := NewAccess(conn, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	nonDefault, err := nonDefaultRepo.SemanticAttributeControl(ctx)
	if err != nil {
		t.Fatalf("read non-default control snapshot: %v", err)
	}
	if nonDefault.State.Digest != baseline.State.Digest {
		t.Fatalf("control digest changed with session formatting: %q != %q", nonDefault.State.Digest, baseline.State.Digest)
	}
	if nonDefault.State.UpdatedAt == "" {
		t.Fatal("control state updated timestamp is empty")
	}
	assertRFC3339UTCTimestamp(t, nonDefault.State.UpdatedAt)
	var tombstoned access.SemanticAttributeAssignment
	for _, row := range nonDefault.Assignments {
		if row.ID == assignment.ID {
			tombstoned = row
			break
		}
	}
	if tombstoned.ID == "" {
		t.Fatalf("tombstoned assignment missing from control snapshot: %#v", nonDefault.Assignments)
	}
	assertRFC3339UTCTimestamp(t, tombstoned.CreatedAt)
	assertRFC3339UTCTimestamp(t, tombstoned.UpdatedAt)
	assertRFC3339UTCTimestamp(t, tombstoned.TombstonedAt)
	var tombstonedMicros int64
	if err := conn.QueryRow(ctx, `SELECT (extract(epoch FROM tombstoned_at) * 1000000)::bigint FROM access.semantic_attribute_assignment WHERE assignment_id = $1::uuid`, assignment.ID).Scan(&tombstonedMicros); err != nil {
		t.Fatalf("read tombstone microseconds: %v", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, tombstoned.TombstonedAt)
	if err != nil {
		t.Fatalf("parse canonical tombstone timestamp: %v", err)
	}
	if parsed.UTC().UnixMicro() != tombstonedMicros {
		t.Fatalf("tombstone timestamp lost microsecond precision: API=%d database=%d", parsed.UTC().UnixMicro(), tombstonedMicros)
	}
}

func assertRFC3339UTCTimestamp(t *testing.T, value string) {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("timestamp %q is not RFC3339: %v", value, err)
	}
	if parsed.Location() != time.UTC || !strings.HasSuffix(value, "Z") {
		t.Fatalf("timestamp %q is not UTC", value)
	}
}

func TestSemanticAttributeControlPostgreSQL18LifecycleAndTrust(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	ctx := t.Context()
	if _, err := db.admin.Exec(ctx, AttributeRegistryMigrationSQL()); err != nil {
		t.Fatalf("apply attribute registry migration: %v", err)
	}
	if _, err := db.admin.Exec(ctx, SemanticAttributeControlMigrationSQL()); err != nil {
		t.Fatalf("apply semantic attribute control migration: %v", err)
	}
	for _, id := range []string{auditActorID, controlSubjectID, controlOwnerID} {
		if _, err := db.admin.Exec(ctx, `
			INSERT INTO access.principal (id, principal_type, status)
			VALUES ($1::uuid, 'user', 'active')`, id); err != nil {
			t.Fatalf("insert principal %s: %v", id, err)
		}
	}
	if _, err := db.admin.Exec(ctx, `
		INSERT INTO access.access_group (id, name)
		VALUES ($1::uuid, 'Control Test Group')`, controlGroupID); err != nil {
		t.Fatalf("insert group: %v", err)
	}
	if _, err := db.admin.Exec(ctx, `
		INSERT INTO access.principal_group (principal_id, group_id)
		VALUES ($1::uuid, $2::uuid)`, controlSubjectID, controlGroupID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	mutation := access.SemanticAttributeMutationContext{ActorPrincipalID: auditActorID, RequestID: "40000000-0000-0000-0000-000000000001"}
	if _, err := repo.RegisterSemanticAttribute(ctx, access.RegisterSemanticAttributeInput{
		Name: "missing_owner", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar,
		Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerPrincipal, ID: "30000000-0000-0000-0000-000000000099"}}, Mutation: mutation,
	}); err == nil {
		t.Fatal("registered a definition with a missing owner")
	}
	ownerDefinition, err := repo.RegisterSemanticAttribute(ctx, access.RegisterSemanticAttributeInput{
		Name: "owned_region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar,
		Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerPrincipal, ID: controlOwnerID}}, Mutation: mutation,
	})
	if err != nil {
		t.Fatalf("register owner-scoped definition: %v", err)
	}
	if _, err := db.admin.Exec(ctx, `UPDATE access.principal SET status='disabled', disabled_at=clock_timestamp(), revoked_at=clock_timestamp() WHERE id=$1::uuid`, controlOwnerID); err != nil {
		t.Fatalf("revoke definition owner: %v", err)
	}
	if _, err := repo.UpdateSemanticAttributeMetadataExpected(ctx, access.UpdateSemanticAttributeMetadataInput{
		Name: ownerDefinition.Name, ExpectedVersion: ownerDefinition.DefinitionVersion,
		Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerPrincipal, ID: controlOwnerID}, DisplayName: "Owned Region"}, Mutation: mutation,
	}); err != nil {
		t.Fatalf("update definition after owner revocation: %v", err)
	}
	definition, err := repo.RegisterSemanticAttribute(ctx, access.RegisterSemanticAttributeInput{
		Name: "region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeList,
		Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}, DisplayName: "Region"},
		Mutation: mutation,
	})
	if err != nil {
		t.Fatalf("register definition: %v", err)
	}

	assignment, err := repo.SetSemanticAttributeAssignment(ctx, access.SemanticAttributeAssignmentInput{
		DefinitionID: definition.ID, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID},
		Values: []string{"west", "east"}, Mutation: mutation,
	})
	if err != nil || assignment.AssignmentVersion != 1 {
		t.Fatalf("create assignment = %#v, err=%v", assignment, err)
	}
	updatedAssignment, err := repo.SetSemanticAttributeAssignment(ctx, access.SemanticAttributeAssignmentInput{
		DefinitionID: definition.ID, Subject: assignment.Subject, Values: []string{"west"}, ExpectedVersion: 1, Mutation: mutation,
	})
	if err != nil || updatedAssignment.AssignmentVersion != 2 {
		t.Fatalf("update assignment = %#v, err=%v", updatedAssignment, err)
	}
	groupAssignment, err := repo.SetSemanticAttributeAssignment(ctx, access.SemanticAttributeAssignmentInput{
		DefinitionID: definition.ID, Subject: access.SubjectRef{Kind: access.SubjectKindGroup, ID: controlGroupID}, Values: []string{"west"}, Mutation: mutation,
	})
	if err != nil || groupAssignment.AssignmentVersion != 1 {
		t.Fatalf("create group assignment = %#v, err=%v", groupAssignment, err)
	}
	groupOnlyDefinition, err := repo.RegisterSemanticAttribute(ctx, access.RegisterSemanticAttributeInput{
		Name: "group_region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar,
		Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}}, Mutation: mutation,
	})
	if err != nil {
		t.Fatalf("register group-only definition: %v", err)
	}
	if _, err := repo.SetSemanticAttributeAssignment(ctx, access.SemanticAttributeAssignmentInput{
		DefinitionID: groupOnlyDefinition.ID, Subject: access.SubjectRef{Kind: access.SubjectKindGroup, ID: controlGroupID}, Values: "north", Mutation: mutation,
	}); err != nil {
		t.Fatalf("create group-only assignment: %v", err)
	}
	resolvedWithGroup, err := repo.EffectiveDirectSemanticAttributeAssignments(ctx, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID})
	if err != nil {
		t.Fatalf("resolve active group assignment: %v", err)
	}
	if !hasEffectiveSemanticAttribute(resolvedWithGroup, groupOnlyDefinition.ID, []string{"north"}) {
		t.Fatalf("active group assignment missing from resolution: %#v", resolvedWithGroup)
	}
	if _, err := db.admin.Exec(ctx, `UPDATE access.access_group SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, controlGroupID); err != nil {
		t.Fatalf("revoke group: %v", err)
	}
	resolvedWithoutGroup, err := repo.EffectiveDirectSemanticAttributeAssignments(ctx, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID})
	if err != nil {
		t.Fatalf("resolve revoked group assignment: %v", err)
	}
	if hasEffectiveSemanticAttribute(resolvedWithoutGroup, groupOnlyDefinition.ID, []string{"north"}) {
		t.Fatalf("revoked group assignment contributed to resolution: %#v", resolvedWithoutGroup)
	}
	if _, err := repo.SetSemanticAttributeAssignment(ctx, access.SemanticAttributeAssignmentInput{
		DefinitionID: definition.ID, Subject: assignment.Subject, Values: []string{"north"}, ExpectedVersion: 1, Mutation: mutation,
	}); !errors.Is(err, access.ErrSemanticAttributeAssignmentConflict) {
		t.Fatalf("stale assignment update error = %v", err)
	}

	claimName := "https://claims.example/region"
	mapping, err := repo.SetTrustedClaimMapping(ctx, access.TrustedClaimMappingInput{
		SourceKind: access.TrustedClaimSourceOIDC, Provider: "corp", Issuer: "https://issuer.example", Audience: "dashboard", Claim: claimName,
		DefinitionID: definition.ID, Mutation: mutation,
	})
	if err != nil || mapping.MappingVersion != 1 {
		t.Fatalf("create mapping = %#v, err=%v", mapping, err)
	}
	embedMapping, err := repo.SetTrustedClaimMapping(ctx, access.TrustedClaimMappingInput{
		SourceKind: access.TrustedClaimSourceEmbed, Provider: "corp", Issuer: "https://issuer.example", Audience: "dashboard", Claim: claimName,
		DefinitionID: definition.ID, Mutation: mutation,
	})
	if err != nil || embedMapping.ID == mapping.ID {
		t.Fatalf("create source-distinct mapping = %#v, err=%v", embedMapping, err)
	}
	if _, err := repo.TombstoneTrustedClaimMapping(ctx, mapping.ID, 99, mutation); !errors.Is(err, access.ErrSemanticAttributeMappingConflict) {
		t.Fatalf("stale mapping tombstone error = %v", err)
	}

	now := time.Now().UTC()
	envelope, err := trustedclaims.Verify(ctx, trustedclaims.NewRawEvidence(trustedclaims.SourceOIDC, []byte("signed")), staticControlVerifier{
		source: trustedclaims.SourceOIDC,
		claims: trustedclaims.VerifiedClaims{Provider: "corp", Issuer: "https://issuer.example", Audience: "dashboard", Subject: controlSubjectID, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute), TokenFingerprint: "sha256:" + strings.Repeat("a", 64), Claims: []trustedclaims.Claim{{Name: claimName, Value: []string{"east"}}}},
	}, trustedclaims.VerifyOptions{Now: now})
	if err != nil {
		t.Fatalf("verify test envelope: %v", err)
	}
	if _, err := repo.EffectiveDirectSemanticAttributeAssignments(ctx, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID}); err != nil {
		t.Fatalf("direct-only resolution: %v", err)
	}
	if _, err := repo.EffectiveSemanticAttributeAssignments(ctx, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID}, envelope); !errors.Is(err, access.ErrSemanticAttributeSourceConflict) {
		t.Fatalf("conflicting direct/trusted resolution error = %v", err)
	}

	// Metadata changes update the current definition identity but do not make
	// a still-compatible assignment unusable. Stale writes are checked after
	// the registry lock, and the SQL WHERE repeats the same version predicate.
	updatedDefinition, err := repo.UpdateSemanticAttributeMetadataExpected(ctx, access.UpdateSemanticAttributeMetadataInput{
		Name: definition.Name, ExpectedVersion: definition.DefinitionVersion,
		Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}, DisplayName: "Region Names"}, Mutation: mutation,
	})
	if err != nil {
		t.Fatalf("metadata update: %v", err)
	}
	if _, err := repo.UpdateSemanticAttributeMetadataExpected(ctx, access.UpdateSemanticAttributeMetadataInput{
		Name: definition.Name, ExpectedVersion: definition.DefinitionVersion,
		Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}, DisplayName: "stale"}, Mutation: mutation,
	}); !errors.Is(err, access.ErrSemanticAttributeConflict) {
		t.Fatalf("stale definition metadata error = %v", err)
	}
	resolved, err := repo.EffectiveDirectSemanticAttributeAssignments(ctx, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: controlSubjectID})
	if err != nil {
		t.Fatalf("resolve after metadata-only definition change: %v", err)
	}
	if len(resolved) != 1 || resolved[0].DefinitionVersion != updatedDefinition.DefinitionVersion {
		t.Fatalf("resolved current definition version = %#v, want %d", resolved, updatedDefinition.DefinitionVersion)
	}
	disabled, err := repo.SetSemanticAttributeEnabledExpected(ctx, definition.Name, false, updatedDefinition.DefinitionVersion, mutation)
	if err != nil {
		t.Fatalf("disable definition: %v", err)
	}
	if _, err := repo.SetSemanticAttributeEnabledExpected(ctx, definition.Name, true, updatedDefinition.DefinitionVersion, mutation); !errors.Is(err, access.ErrSemanticAttributeConflict) {
		t.Fatalf("stale lifecycle error = %v", err)
	}
	if disabled.DefinitionVersion <= updatedDefinition.DefinitionVersion {
		t.Fatalf("disable version = %d, metadata version = %d", disabled.DefinitionVersion, updatedDefinition.DefinitionVersion)
	}

	// Definition disablement and subject revocation must not strand the
	// immutable tombstone operation, while active value updates remain blocked.
	if _, err := db.admin.Exec(ctx, `UPDATE access.principal SET status='disabled', disabled_at=clock_timestamp(), revoked_at=clock_timestamp() WHERE id=$1::uuid`, controlSubjectID); err != nil {
		t.Fatalf("revoke subject: %v", err)
	}
	if _, err := db.admin.Exec(ctx, `
		UPDATE access.semantic_attribute_assignment
		SET canonical_values=ARRAY['tampered'], value_digest=$2, tombstoned_at=clock_timestamp(), assignment_version=assignment_version+1
		WHERE assignment_id=$1::uuid`, assignment.ID, "sha256:"+strings.Repeat("f", 64)); err == nil || !strings.Contains(err.Error(), "cannot rewrite") {
		t.Fatalf("payload rewrite during tombstone error = %v", err)
	}
	if _, err := repo.TombstoneSemanticAttributeAssignment(ctx, assignment.ID, updatedAssignment.AssignmentVersion, mutation); err != nil {
		t.Fatalf("tombstone revoked subject assignment: %v", err)
	}
	if _, err := repo.TombstoneSemanticAttributeAssignment(ctx, groupAssignment.ID, groupAssignment.AssignmentVersion, mutation); err != nil {
		t.Fatalf("tombstone disabled definition group assignment: %v", err)
	}
	if _, err := repo.TombstoneTrustedClaimMapping(ctx, mapping.ID, mapping.MappingVersion, mutation); err != nil {
		t.Fatalf("tombstone disabled definition mapping: %v", err)
	}
	if _, err := repo.TombstoneTrustedClaimMapping(ctx, embedMapping.ID, embedMapping.MappingVersion, mutation); err != nil {
		t.Fatalf("tombstone source-distinct mapping: %v", err)
	}

	var metadata string
	if err := db.admin.QueryRow(ctx, `SELECT metadata::text FROM audit.audit_event WHERE resource_kind='semantic_attribute_assignment' ORDER BY occurred_at DESC LIMIT 1`).Scan(&metadata); err != nil {
		t.Fatalf("read assignment audit: %v", err)
	}
	var auditObject map[string]any
	if err := json.Unmarshal([]byte(metadata), &auditObject); err != nil {
		t.Fatal(err)
	}
	if _, present := auditObject["value_digest"]; present {
		t.Fatalf("assignment audit leaked value digest: %s", metadata)
	}
	if strings.Contains(metadata, "west") || strings.Contains(metadata, "east") {
		t.Fatalf("assignment audit leaked raw value: %s", metadata)
	}
}

func TestSemanticAttributeControlPostgreSQL18TransactionRollback(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	ctx := t.Context()
	if _, err := db.admin.Exec(ctx, AttributeRegistryMigrationSQL()); err != nil {
		t.Fatalf("apply attribute registry migration: %v", err)
	}
	if _, err := db.admin.Exec(ctx, SemanticAttributeControlMigrationSQL()); err != nil {
		t.Fatalf("apply semantic attribute control migration: %v", err)
	}
	if _, err := db.admin.Exec(ctx, `INSERT INTO access.principal (id, principal_type, status) VALUES ($1::uuid, 'user', 'active')`, auditActorID); err != nil {
		t.Fatal(err)
	}
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	mutation := access.SemanticAttributeMutationContext{ActorPrincipalID: auditActorID}
	definition, err := repo.RegisterSemanticAttribute(ctx, access.RegisterSemanticAttributeInput{Name: "rollback_region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Mutation: mutation})
	if err != nil {
		t.Fatal(err)
	}
	before, err := repo.SemanticAttributeControl(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var auditCount int
	if err := db.admin.QueryRow(ctx, `SELECT count(*) FROM audit.audit_event`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	tx, err := db.runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetSemanticAttributeAssignmentTx(ctx, tx, access.SemanticAttributeAssignmentInput{DefinitionID: definition.ID, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: auditActorID}, Values: "temporary", Mutation: mutation}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("transactional assignment: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.SemanticAttributeAssignments(ctx, access.SemanticAttributeAssignmentFilter{DefinitionID: definition.ID, IncludeTombstones: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rolled-back assignment rows = %d", len(rows))
	}
	after, err := repo.SemanticAttributeControl(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.State.Revision != after.State.Revision || before.State.Digest != after.State.Digest {
		t.Fatalf("rolled-back control identity changed: before=%#v after=%#v", before.State, after.State)
	}
	var afterAuditCount int
	if err := db.admin.QueryRow(ctx, `SELECT count(*) FROM audit.audit_event`).Scan(&afterAuditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != afterAuditCount {
		t.Fatalf("rolled-back audit count = %d, before %d", afterAuditCount, auditCount)
	}
}
