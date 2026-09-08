package postgres

import (
	"context"
	"fmt"
	"time"

	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	servingstatepostgres "github.com/flidai/leapview/internal/servingstate/postgres"
)

// applyDuckLakeTestSchemas installs the canonical delivery authority before
// serving-state and DuckLake. DuckLake's retention and orphan checks consult
// serving-state reader leases, so every isolated control-database test must use
// the same capability ordering as production.
func applyDuckLakeTestSchemas(ctx context.Context, tx deploymentpostgres.Tx) error {
	if err := deploymentpostgres.ApplySchema(ctx, tx); err != nil {
		return err
	}
	if err := servingstatepostgres.ApplySchema(ctx, tx); err != nil {
		return err
	}
	return ApplySchema(ctx, tx)
}

// canonicalDeliveryAttemptInput describes the immutable identity that
// canonical delivery owns for a physical build. Tests seed this authority
// explicitly; DuckLake only retains catalog identity and external retention
// evidence.
type canonicalDeliveryAttemptInput struct {
	PlanID         string
	CandidateID    string
	TargetID       string
	AttemptID      string
	RequestDigest  string
	PlanDigest     string
	PhysicalPoolID string
	CatalogID      string
	OwnerID        string
	FencingEpoch   int64
	State          string
}

type canonicalDeliverySealInput struct {
	SealID      string
	SnapshotID  int64
	CatalogUUID string
}

func seedCanonicalDeliveryAttempt(ctx context.Context, db DBTX, in canonicalDeliveryAttemptInput) error {
	if db == nil {
		return ErrInvalid
	}
	if in.State == "" {
		in.State = "running"
	}
	if _, err := db.Exec(ctx, `
INSERT INTO delivery.delivery_target(target_id, project_id, environment)
VALUES ($1, $1, 'prod')
ON CONFLICT (target_id) DO NOTHING`, in.TargetID); err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `
INSERT INTO delivery.delivery_plan(
    plan_id, target_id, plan_revision, plan_digest, compiled_graph_digest,
    compiled_config_digest, security_domain_fingerprint, artifact_digest,
    qualification_digest, qualification_required, approval_required,
    approval_policy_revision, plan_document)
VALUES ($1::uuid, $2, 1, $3, $3, $3, $3, $3, $3, false, false, 1, '{}'::jsonb)
ON CONFLICT (plan_id) DO NOTHING`, in.PlanID, in.TargetID, in.PlanDigest); err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `
INSERT INTO delivery.delivery_candidate(
    candidate_id, target_id, plan_id, status, candidate_revision,
    artifact_digest)
VALUES ($1::uuid, $2, $3::uuid, 'building', 1, $4)
ON CONFLICT (candidate_id) DO NOTHING`, in.CandidateID, in.TargetID, in.PlanID, in.PlanDigest); err != nil {
		return err
	}
	leaseExpiresAt := time.Now().UTC().Add(time.Hour)
	if _, err := db.Exec(ctx, `
INSERT INTO delivery.delivery_build_attempt(
    attempt_id, plan_id, candidate_id, owner_id, physical_pool_id, catalog_id,
    fencing_epoch, request_digest, plan_digest, state, namespace,
    lease_expires_at, session_identity)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9, $10,
        $11, $12, $13)
ON CONFLICT (attempt_id) DO NOTHING`, in.AttemptID, in.PlanID, in.CandidateID,
		in.OwnerID, in.PhysicalPoolID, in.CatalogID, in.FencingEpoch,
		in.RequestDigest, in.PlanDigest, in.State,
		"ducklake-test-"+in.AttemptID, leaseExpiresAt, "ducklake-test-session"); err != nil {
		return err
	}
	return nil
}

func seedCanonicalDeliverySeal(ctx context.Context, db DBTX, attempt canonicalDeliveryAttemptInput, seal canonicalDeliverySealInput) error {
	if err := seedCanonicalDeliveryAttempt(ctx, db, attempt); err != nil {
		return err
	}
	if seal.CatalogUUID == "" {
		seal.CatalogUUID = "0198f2c0-7c7a-7f00-8a11-000000009901"
	}
	if _, err := db.Exec(ctx, `
INSERT INTO delivery.delivery_snapshot_seal(
    seal_id, attempt_id, candidate_id, physical_pool_id, tenant_domain, region,
    encryption_domain, object_namespace, catalog_database, catalog_id,
    catalog_uuid, catalog_version, ducklake_snapshot_id, relation_namespace,
    relation_manifest_digest, closure_digest, object_root, object_root_digest,
    artifact_root, artifact_root_digest, compiled_graph_digest,
    compiled_config_digest, security_domain_fingerprint, request_digest,
    plan_digest, compatibility_digest, serving_artifact_id,
    serving_artifact_digest, duckdb_version, runtime_version,
    ducklake_extension_version, ducklake_spec_version, catalog_schema_version)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'test-tenant', 'test-region',
    'test-encryption', 'test-namespace', 'ducklake', $5, $6, 1, $7,
    'test-relations', $8, $8, '/tmp/ducklake-test-objects', $8,
    '/tmp/ducklake-test-artifacts', $8, $8, $8, $8, $8, $9, $8, 'test-artifact',
    $8, 'duckdb-test', 'runtime-test', 'ducklake-test', 'spec-test', 'catalog-test')
ON CONFLICT (seal_id) DO NOTHING`, seal.SealID, attempt.AttemptID,
		attempt.CandidateID, attempt.PhysicalPoolID, attempt.CatalogID,
		seal.CatalogUUID, seal.SnapshotID, attempt.RequestDigest, attempt.PlanDigest); err != nil {
		return err
	}
	// Qualification is the canonical candidate transition associated with a
	// seal. The transition is legal from the seeded building state and makes
	// the test row conform to the delivery authority's lifecycle checks.
	_, err := db.Exec(ctx, `
UPDATE delivery.delivery_candidate
SET status='qualified', snapshot_seal_id=$2::uuid, qualification_digest=$3,
    qualified_at=clock_timestamp()
WHERE candidate_id=$1::uuid`, attempt.CandidateID, seal.SealID, attempt.PlanDigest)
	return err
}

func canonicalAttemptIDs(attemptID string) (planID, candidateID, targetID string) {
	// IDs are test-only and need only be valid UUIDs. Keeping them derived from
	// the attempt makes accidental cross-test reuse visible in the database.
	if len(attemptID) < 8 {
		return "", "", ""
	}
	planID = "0198f2c0-7c7a-7f00-8a11-" + attemptID[len(attemptID)-12:]
	candidateID = "0198f2c0-7c7a-7f00-8a12-" + attemptID[len(attemptID)-12:]
	targetID = fmt.Sprintf("ducklake-test-target-%s", attemptID[len(attemptID)-8:])
	return planID, candidateID, targetID
}
