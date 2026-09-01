package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

const catalogBootstrapQualificationSeedPrefix = "leapview/ducklake/bootstrap-qualification/v1\x00"

// CatalogBootstrapQualificationInput establishes the first completed runtime
// qualification epoch for a newly registered catalog. Later tuple changes use
// the normal migration coordinator; bootstrap uses the same fenced lifecycle
// with an identical current and target tuple so runtime attach never bypasses
// qualification evidence.
type CatalogBootstrapQualificationInput struct {
	PhysicalPoolID     string
	CatalogID          string
	OwnerID            string
	Compatibility      RuntimeCompatibility
	BeginEvidence      json.RawMessage
	CompletionEvidence json.RawMessage
}

// QualifyCatalogBootstrap completes or exactly replays the initial fenced
// qualification epoch in the caller-owned transaction. No retained snapshots
// exist at catalog bootstrap, but the lifecycle still proves the empty-reader
// drain and the target-owned conformance/catalog evidence before attach is
// admitted.
func QualifyCatalogBootstrap(ctx context.Context, tx DBTX, in CatalogBootstrapQualificationInput) (CatalogRuntimeCompatibility, error) {
	if tx == nil || !validID(in.PhysicalPoolID) || !validID(in.CatalogID) || !validID(in.OwnerID) || in.Compatibility.validate() != nil {
		return CatalogRuntimeCompatibility{}, ErrInvalid
	}
	beginEvidence, err := canonicalBeginEvidence(in.BeginEvidence)
	if err != nil {
		return CatalogRuntimeCompatibility{}, err
	}
	completionEvidence, err := canonicalEvidence(in.CompletionEvidence)
	if err != nil {
		return CatalogRuntimeCompatibility{}, fmt.Errorf("%w: bootstrap completion evidence", ErrMigrationEvidenceRequired)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	current, err := LoadCatalogRuntimeCompatibility(ctx, tx, in.PhysicalPoolID)
	if err != nil {
		return CatalogRuntimeCompatibility{}, err
	}
	if current.CatalogID != in.CatalogID || !sameRuntimeCompatibility(current.RuntimeCompatibility, in.Compatibility) {
		return CatalogRuntimeCompatibility{}, fmt.Errorf("%w: bootstrap runtime compatibility", ErrCompatibilityMismatch)
	}
	migrationID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(
		catalogBootstrapQualificationSeedPrefix+in.PhysicalPoolID+"\x00"+in.CatalogID+"\x00"+
			in.Compatibility.CompatibilityDigest+"\x00"+in.Compatibility.CatalogSchemaVersion,
	)).String()
	if current.CurrentMigrationID != "" {
		if current.CurrentMigrationID != migrationID {
			return CatalogRuntimeCompatibility{}, fmt.Errorf("%w: bootstrap qualification epoch differs", ErrConflict)
		}
		migration, err := LoadCatalogMigration(ctx, tx, migrationID)
		if err != nil {
			return CatalogRuntimeCompatibility{}, err
		}
		if migration.State != "completed" || !evidenceEqual(migration.BeginEvidence, beginEvidence) || !evidenceEqual(migration.CompletionEvidence, completionEvidence) {
			return CatalogRuntimeCompatibility{}, fmt.Errorf("%w: bootstrap qualification evidence differs", ErrConflict)
		}
		eligibility, err := CheckRuntimeAttachEligibility(ctx, tx, RuntimeAttachInput{
			PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, Compatibility: in.Compatibility,
		})
		if err != nil || !eligibility.Eligible {
			return CatalogRuntimeCompatibility{}, fmt.Errorf("%w: replay bootstrap qualification: %v", ErrRuntimeAttachIneligible, err)
		}
		return eligibility.Current, nil
	}

	globalFence, err := AcquireMigrationFence(ctx, tx, AcquireMigrationFenceInput{
		Scope: MigrationFenceGlobal, OwnerID: in.OwnerID,
	})
	if err != nil {
		return CatalogRuntimeCompatibility{}, err
	}
	poolFence, err := AcquireMigrationFence(ctx, tx, AcquireMigrationFenceInput{
		Scope: MigrationFencePool, PhysicalPoolID: in.PhysicalPoolID, OwnerID: in.OwnerID,
	})
	if err != nil {
		return CatalogRuntimeCompatibility{}, err
	}
	if _, err := BeginCatalogMigration(ctx, tx, BeginCatalogMigrationInput{
		MigrationID: migrationID, PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID,
		GlobalFence: globalFence, PoolFence: poolFence, Current: in.Compatibility, Target: in.Compatibility,
		Evidence: in.BeginEvidence,
	}); err != nil {
		return CatalogRuntimeCompatibility{}, err
	}
	if _, err := CompleteCatalogMigration(ctx, tx, CompleteCatalogMigrationInput{
		MigrationID: migrationID, GlobalFence: globalFence, PoolFence: poolFence, Evidence: in.CompletionEvidence,
	}); err != nil {
		return CatalogRuntimeCompatibility{}, err
	}
	if err := ReleaseMigrationFence(ctx, tx, poolFence); err != nil {
		return CatalogRuntimeCompatibility{}, err
	}
	if err := ReleaseMigrationFence(ctx, tx, globalFence); err != nil {
		return CatalogRuntimeCompatibility{}, err
	}
	eligibility, err := CheckRuntimeAttachEligibility(ctx, tx, RuntimeAttachInput{
		PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, Compatibility: in.Compatibility,
	})
	if err != nil || !eligibility.Eligible || eligibility.Current.CurrentMigrationID != migrationID {
		return CatalogRuntimeCompatibility{}, fmt.Errorf("%w: bootstrap qualification did not become attach-eligible: %v", ErrRuntimeAttachIneligible, err)
	}
	return eligibility.Current, nil
}
