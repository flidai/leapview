package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	depdb "github.com/flidai/leapview/internal/deployment/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func createSeal(ctx context.Context, db DBTX, in SnapshotSealInput) (SnapshotSeal, error) {
	id, err := uuidID(in.SealID, "seal id", true)
	if err != nil {
		return SnapshotSeal{}, err
	}
	attempt, err := uuidID(in.AttemptID, "attempt id", false)
	if err != nil {
		return SnapshotSeal{}, err
	}
	candidate, err := uuidID(in.CandidateID, "candidate id", false)
	if err != nil {
		return SnapshotSeal{}, err
	}
	if in.PhysicalPoolID == "" || in.TenantDomain == "" || in.Region == "" || in.EncryptionDomain == "" || in.ObjectNamespace == "" || in.CatalogDatabase == "" || in.CatalogID == "" || in.CatalogUUID == "" || in.RelationNamespace == "" || in.ObjectRoot == "" || in.ArtifactRoot == "" || in.ObjectRootDigest == "" || in.ArtifactRootDigest == "" {
		return SnapshotSeal{}, ErrInvalid
	}
	canonicalCatalogUUID, err := uuidID(in.CatalogUUID, "catalog uuid", false)
	if err != nil {
		return SnapshotSeal{}, err
	}
	in.CatalogUUID = canonicalCatalogUUID
	for label, value := range map[string]string{"physical pool id": in.PhysicalPoolID, "catalog database": in.CatalogDatabase, "catalog id": in.CatalogID, "relation namespace": in.RelationNamespace, "object root": in.ObjectRoot, "artifact root": in.ArtifactRoot} {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
			return SnapshotSeal{}, fmt.Errorf("%w: %s", ErrInvalid, label)
		}
	}
	for label, value := range map[string]string{"tenant domain": in.TenantDomain, "region": in.Region, "encryption domain": in.EncryptionDomain, "object namespace": in.ObjectNamespace} {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 255 || strings.ContainsAny(value, "\x00\r\n") {
			return SnapshotSeal{}, fmt.Errorf("%w: %s", ErrInvalid, label)
		}
	}
	if _, err := digest(in.ObjectRootDigest, "object root digest"); err != nil {
		return SnapshotSeal{}, err
	}
	if _, err := digest(in.ArtifactRootDigest, "artifact root digest"); err != nil {
		return SnapshotSeal{}, err
	}
	if in.CatalogVersion <= 0 || in.DuckLakeSnapshotID <= 0 {
		return SnapshotSeal{}, ErrInvalid
	}
	if in.LegacyAuthorizationPolicy {
		if in.AuthorizationPolicyRevision != 0 || in.AuthorizationPolicyDigest != "" {
			return SnapshotSeal{}, fmt.Errorf("%w: legacy authorization policy evidence", ErrInvalid)
		}
	} else if in.AuthorizationPolicyRevision <= 0 {
		return SnapshotSeal{}, fmt.Errorf("%w: authorization policy revision", ErrInvalid)
	}
	digests := map[string]string{"relation manifest digest": in.RelationManifestDigest, "closure digest": in.ClosureDigest, "compiled graph digest": in.CompiledGraphDigest, "compiled config digest": in.CompiledConfigDigest, "security fingerprint": in.SecurityDomainFingerprint, "request digest": in.RequestDigest, "plan digest": in.PlanDigest, "compatibility digest": in.CompatibilityDigest, "serving artifact digest": in.ServingArtifactDigest}
	if !in.LegacyAuthorizationPolicy {
		digests["authorization policy digest"] = in.AuthorizationPolicyDigest
	}
	for n, v := range digests {
		if _, err := digest(v, n); err != nil {
			return SnapshotSeal{}, err
		}
	}
	if in.ServingArtifactID == "" || in.ServingArtifactID != strings.TrimSpace(in.ServingArtifactID) || len(in.ServingArtifactID) > 255 || strings.ContainsAny(in.ServingArtifactID, "\x00\r\n") {
		return SnapshotSeal{}, fmt.Errorf("%w: serving artifact id", ErrInvalid)
	}
	for n, v := range map[string]string{"DuckDB version": in.DuckDBVersion, "runtime version": in.RuntimeVersion, "DuckLake extension version": in.DuckLakeExtensionVersion, "DuckLake specification version": in.DuckLakeSpecVersion, "catalog schema version": in.CatalogSchemaVersion} {
		if v == "" || v != strings.TrimSpace(v) || len(v) > 128 {
			return SnapshotSeal{}, fmt.Errorf("%w: %s", ErrInvalid, n)
		}
	}
	evidence, err := canonicalObject(in.QualificationEvidence, maxEvidence, false)
	if err != nil {
		return SnapshotSeal{}, fmt.Errorf("%w: qualification evidence required", ErrInvalid)
	}
	at, err := loadAttempt(ctx, db, attempt)
	if err != nil {
		return SnapshotSeal{}, err
	}
	if at.State != AttemptCommitted || at.SnapshotID != in.DuckLakeSnapshotID || at.RequestDigest != in.RequestDigest || at.PlanDigest != in.PlanDigest || at.CandidateID != candidate || at.PhysicalPoolID != in.PhysicalPoolID || at.CatalogID != in.CatalogID || at.Namespace != in.RelationNamespace {
		return SnapshotSeal{}, fmt.Errorf("%w: attempt is not exact committed evidence", ErrNotQualified)
	}
	if !markerMatches(at.CommitMarker, attempt, at.PhysicalPoolID, in.RequestDigest, in.PlanDigest, at.FencingEpoch) {
		return SnapshotSeal{}, fmt.Errorf("%w: commit marker is incomplete", ErrNotQualified)
	}
	marker, _, markerErr := decodeCommitMarker(at.CommitMarker, false)
	if markerErr != nil {
		return SnapshotSeal{}, fmt.Errorf("%w: commit marker is incomplete", ErrNotQualified)
	}
	binding, bindingErr := loadBuildArtifactBinding(ctx, db, attempt)
	if errors.Is(bindingErr, ErrNotFound) {
		return SnapshotSeal{}, fmt.Errorf("%w: build artifact binding is missing", ErrNotQualified)
	}
	if bindingErr != nil {
		return SnapshotSeal{}, bindingErr
	}
	if binding.ServingArtifactID != in.ServingArtifactID || binding.ServingArtifactDigest != in.ServingArtifactDigest {
		return SnapshotSeal{}, fmt.Errorf("%w: build artifact binding differs", ErrConflict)
	}
	if binding.ServingStateID != marker.GenerationID {
		return SnapshotSeal{}, fmt.Errorf("%w: serving state differs from commit marker generation", ErrConflict)
	}
	ci, err := depdb.New(db).GetCandidateIdentity(ctx, dbUUID(candidate))
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotSeal{}, ErrNotFound
	} else if err != nil {
		return SnapshotSeal{}, err
	}
	if ci.Status == "rejected" || ci.Status == "retired" || ci.PlanID != at.PlanID || ci.ArtifactDigest != in.ServingArtifactDigest {
		return SnapshotSeal{}, fmt.Errorf("%w: candidate evidence differs", ErrNotQualified)
	}
	pi, err := depdb.New(db).GetPlanDigests(ctx, dbUUID(at.PlanID))
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotSeal{}, ErrNotFound
	} else if err != nil {
		return SnapshotSeal{}, err
	}
	if pi.PlanDigest != in.PlanDigest || pi.CompiledGraphDigest != in.CompiledGraphDigest || pi.CompiledConfigDigest != in.CompiledConfigDigest || pi.SecurityDomainFingerprint != in.SecurityDomainFingerprint || pi.ArtifactDigest != in.ServingArtifactDigest {
		return SnapshotSeal{}, fmt.Errorf("%w: plan evidence differs", ErrNotQualified)
	}
	if in.LegacyAuthorizationPolicy {
		planRow, planErr := depdb.New(db).GetPlan(ctx, dbUUID(at.PlanID))
		if errors.Is(planErr, pgx.ErrNoRows) {
			return SnapshotSeal{}, ErrNotFound
		}
		if planErr != nil {
			return SnapshotSeal{}, planErr
		}
		persistedPlan, planErr := mapPlanRow(planRow)
		if planErr != nil {
			return SnapshotSeal{}, planErr
		}
		richPlan, planErr := persistedPlan.RichPlan()
		if planErr != nil {
			return SnapshotSeal{}, planErr
		}
		if richPlan.Governance.PolicyRevision != 0 || richPlan.Governance.PolicyDigest != richPlan.Governance.AuthorizationDigest {
			return SnapshotSeal{}, fmt.Errorf("%w: persisted plan is not pre-017 authorization evidence", ErrConflict)
		}
	}
	err = depdb.New(db).InsertSnapshotSeal(ctx, depdb.InsertSnapshotSealParams{SealID: dbUUID(id), AttemptID: dbUUID(attempt), CandidateID: dbUUID(candidate), PhysicalPoolID: in.PhysicalPoolID, TenantDomain: in.TenantDomain, Region: in.Region, EncryptionDomain: in.EncryptionDomain, ObjectNamespace: in.ObjectNamespace, CatalogDatabase: in.CatalogDatabase, CatalogID: in.CatalogID, CatalogUuid: in.CatalogUUID, CatalogVersion: in.CatalogVersion, DucklakeSnapshotID: in.DuckLakeSnapshotID, RelationNamespace: in.RelationNamespace, RelationManifestDigest: in.RelationManifestDigest, ClosureDigest: in.ClosureDigest, ObjectRoot: in.ObjectRoot, ObjectRootDigest: in.ObjectRootDigest, ArtifactRoot: in.ArtifactRoot, ArtifactRootDigest: in.ArtifactRootDigest, CompiledGraphDigest: in.CompiledGraphDigest, CompiledConfigDigest: in.CompiledConfigDigest, SecurityDomainFingerprint: in.SecurityDomainFingerprint, AuthorizationPolicyRevision: pgtype.Int8{Int64: in.AuthorizationPolicyRevision, Valid: !in.LegacyAuthorizationPolicy}, AuthorizationPolicyDigest: pgtype.Text{String: in.AuthorizationPolicyDigest, Valid: !in.LegacyAuthorizationPolicy}, RequestDigest: in.RequestDigest, PlanDigest: in.PlanDigest, CompatibilityDigest: in.CompatibilityDigest, ServingArtifactID: in.ServingArtifactID, ServingArtifactDigest: in.ServingArtifactDigest, DuckdbVersion: in.DuckDBVersion, RuntimeVersion: in.RuntimeVersion, DucklakeExtensionVersion: in.DuckLakeExtensionVersion, DucklakeSpecVersion: in.DuckLakeSpecVersion, CatalogSchemaVersion: in.CatalogSchemaVersion, QualificationEvidence: evidence})
	if err != nil {
		return SnapshotSeal{}, err
	}
	s, err := loadSeal(ctx, db, id)
	if err != nil {
		return SnapshotSeal{}, err
	}
	if !sameSealIdentity(s, in) {
		return SnapshotSeal{}, ErrConflict
	}
	return s, nil
}
