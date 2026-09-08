package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/app/adminpostgres"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/postgresbaseline"
	extensionfixture "github.com/flidai/leapview/internal/app/testing/extensionfixture"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/recoveryset"
	recoverypostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TestPostgres18RecoveryAdmissionChain qualifies the real admin operations,
// persistence and production recovery-readiness callback. The existing startup
// fixture supplies the already-restored delivery/physical projections; it does
// not supply recovery results. No provider restore is performed by this test.
// This validates the recovery admission contract. It does not prove successful physical disaster recovery.
// Provider restore, historical object recovery, key recovery, PITR and RPO/RTO
// measurement remain outside this qualification.
func TestPostgres18RecoveryAdmissionChain(t *testing.T) {
	t.Log("This validates the recovery admission contract. It does not prove successful physical disaster recovery.")
	h := postgrestest.StartTLS(t)
	roles := provisionPostgresOnboardingRoles(t, h)
	control := h.NewDatabase(t, "leapview_control")
	catalog := h.NewDatabase(t, "leapview_ducklake")
	grantPostgresOnboardingDatabases(t, h, control, catalog, roles)
	cfg := postgresOnboardingConfig(t, h, control, catalog, roles, extensionfixture.Fixture{})
	migrator, err := platformpostgres.Open(t.Context(), cfg.PostgresControlPlaneConfig().Migrator)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := migrator.SQLDB()
	if err != nil {
		migrator.Close()
		t.Fatal(err)
	}
	if err := postgresbaseline.ApplyWithMigrationFence(t.Context(), migrator.NativePool(), sqlDB); err != nil {
		sqlDB.Close()
		migrator.Close()
		t.Fatal(err)
	}
	sqlDB.Close()
	migrator.Close()
	ops := adminpostgres.New(adminpostgres.Dependencies{LoadConfig: func() (config.Config, error) { return cfg, nil }})
	pool, err := platformpostgres.Open(t.Context(), cfg.PostgresControlMaintenanceConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := recoverypostgres.New(pool)
	f := startupRecoveryFixture(t)
	// The general startup projection fixture uses a display-only catalog UUID.
	// A restored catalog authority row requires a canonical UUID shared by the
	// persisted seal, recovery frontier and startup projection.
	catalogUUID := uuid.NewString()
	f.set.Serving.CatalogUUID = catalogUUID
	f.set.Catalog.CatalogUUID = catalogUUID
	f.authority.seal.CatalogUUID = catalogUUID
	cfg.DeliveryPhysicalPoolID = f.set.Serving.PhysicalPoolID
	cfg.DeliveryPhysicalPoolCompatibilityDigest = f.set.Serving.CompatibilityDigest
	seedRecoveryAdmissionDelivery(t, control.AdminURL(), f.set)
	fixtureDB, err := pgx.Connect(t.Context(), control.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixtureDB.Close(context.Background()) })
	s := f.set.Serving
	if _, err := fixtureDB.Exec(t.Context(), `INSERT INTO ducklake.catalog_identity(physical_pool_id,catalog_database,catalog_id,catalog_uuid,metadata_schema) VALUES ($1,$2,$3,$4,'lake')`, s.PhysicalPoolID, s.CatalogDatabase, s.CatalogID, s.CatalogUUID); err != nil {
		t.Fatal(err)
	}
	// Retention belongs to preparation, not the evidence envelope. Missing
	// delivery metadata must also fail before leaving any usable recovery set.
	for i, name := range []string{"missing snapshot retention", "expired snapshot retention", "mismatched retention identity", "invalid snapshot binding", "expired recovery hold", "missing retention expiry", "missing publication identity", "missing target revision"} {
		t.Run(name, func(t *testing.T) {
			set := f.set
			set.ID, set.Status, set.PublishedValidationAttemptID, set.FrontierDigest = uuid.NewString(), recoveryset.StatusPrepared, "", ""
			// Retention identities are immutable, even in a restored fixture.
			// Give each case its own seal/generation/snapshot instead of deleting
			// or reviving another case's retention row.
			set.Delivery.TargetID = "retention-" + uuid.NewString()
			set.Delivery.GenerationID, set.Serving.SealID = uuid.NewString(), uuid.NewString()
			set.Serving.DuckLakeSnapshotID += int64(i+1) * 10
			set.Catalog.SnapshotID = set.Serving.DuckLakeSnapshotID
			seedRecoveryAdmissionDelivery(t, control.AdminURL(), set)
			snapshotID, state := set.Serving.DuckLakeSnapshotID, "live"
			if name == "expired snapshot retention" {
				state = "expired"
			}
			if name == "mismatched retention identity" {
				snapshotID++
			}
			if name != "missing snapshot retention" {
				seedRecoveryAdmissionRetention(t, fixtureDB, set, snapshotID, state)
			}
			if name == "invalid snapshot binding" {
				set.Serving.SealID = uuid.NewString()
			}
			expires := time.Now().Add(time.Hour)
			wantDiagnostic := "snapshot retention is not live"
			if name == "invalid snapshot binding" {
				wantDiagnostic = "target/generation/seal tuple is unknown"
			}
			if name == "expired recovery hold" {
				expires = time.Now().Add(-time.Hour)
				wantDiagnostic = "expiry must be in the future"
			}
			if name == "missing retention expiry" {
				expires = time.Time{}
				wantDiagnostic = "recovery retention root expiry is required"
			}
			if name == "missing publication identity" {
				set.Delivery.PublicationID = ""
				wantDiagnostic = "publication"
			}
			if name == "missing target revision" {
				set.Delivery.TargetRevision = 0
				wantDiagnostic = "revision"
			}
			if _, err := ops.PrepareRecovery(t.Context(), admincli.RecoveryPrepareRequest{Set: set, ExpiresAt: expires}); err == nil || !strings.Contains(err.Error(), wantDiagnostic) {
				t.Fatalf("retention denial = %v, want %s", err, wantDiagnostic)
			}
			if _, err := repo.ReadExact(t.Context(), set.ID); !errors.Is(err, recoveryset.ErrNotFound) {
				t.Fatalf("failed preparation left recovery set: %v", err)
			}
			var roots int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM delivery.delivery_retention_root WHERE root_id=$1`, set.ID).Scan(&roots); err != nil || roots != 0 {
				t.Fatalf("failed preparation left root: count=%d err=%v", roots, err)
			}
			attemptID := uuid.NewString()
			// A valid envelope cannot repair a preparation that never committed.
			evidenceSet := f.set
			evidenceSet.ID = set.ID
			envelope, err := recoveryset.NewValidationEvidenceEnvelope(evidenceSet, attemptID)
			if err != nil {
				t.Fatal(err)
			}
			evidence, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ops.ValidateRecovery(t.Context(), admincli.RecoveryValidateRequest{SetID: set.ID, AttemptID: attemptID, Validator: "qualification", Evidence: evidence}); err == nil {
				t.Fatal("unprepared retention state validated")
			}
			if _, err := ops.PublishRecovery(t.Context(), admincli.RecoveryPublishRequest{SetID: set.ID, ValidationAttemptID: attemptID, Publisher: "qualification", FenceEpoch: set.FenceEpoch}); err == nil {
				t.Fatal("unprepared retention state published")
			}
			checkConfig := f.config(nil)
			checkConfig.Recovery, checkConfig.RecoverySetID = repo, set.ID
			check, err := newPostgresDeliveryStartupCheck(checkConfig)
			if err != nil {
				t.Fatal(err)
			}
			if err := check(t.Context()); err == nil || !strings.Contains(err.Error(), "recovery_set_missing") {
				t.Fatalf("retention failure readiness = %v", err)
			}
		})
	}
	seedRecoveryAdmissionRetention(t, fixtureDB, f.set, f.set.Serving.DuckLakeSnapshotID, "live")

	type evidenceCase struct {
		name   string
		mutate func(*recoveryset.ValidationEvidenceEnvelope)
	}
	cases := []evidenceCase{
		{"valid", nil},
		{"control database identity", func(e *recoveryset.ValidationEvidenceEnvelope) { e.ClusterPoints[0].DatabaseIdentity = "wrong-control" }},
		{"catalog database identity", func(e *recoveryset.ValidationEvidenceEnvelope) { e.ClusterPoints[1].DatabaseIdentity = "wrong-catalog" }},
		{"control recovery identity", func(e *recoveryset.ValidationEvidenceEnvelope) { e.ClusterPoints[0].RecoveryIdentity = "lsn:0/999" }},
		{"catalog recovery identity", func(e *recoveryset.ValidationEvidenceEnvelope) { e.ClusterPoints[1].RecoveryIdentity = "lsn:0/999" }},
		{"object version", func(e *recoveryset.ValidationEvidenceEnvelope) { e.ObjectRoots[0].VersionID = "wrong-version" }},
		{"object provider frontier", func(e *recoveryset.ValidationEvidenceEnvelope) {
			e.ObjectRoots[0].ProviderRecoveryFrontier = "wrong-frontier"
		}},
		{"frontier digest", func(e *recoveryset.ValidationEvidenceEnvelope) {
			e.FrontierDigest = "sha256:" + strings.Repeat("a", 64)
		}},
		{"artifact identity", func(e *recoveryset.ValidationEvidenceEnvelope) {
			for i := range e.ObjectRoots {
				if e.ObjectRoots[i].Kind == recoveryset.ObjectRootServingArtifact {
					e.ObjectRoots[i].Digest = "sha256:" + strings.Repeat("b", 64)
				}
			}
		}},
		{"closure", func(e *recoveryset.ValidationEvidenceEnvelope) { e.ClosureDigest = "sha256:" + strings.Repeat("c", 64) }},
	}
	// Exercise actual omission at the wire boundary, not only empty values.
	for _, field := range []string{"frontier_digest", "cluster_points", "object_roots", "relation_manifest_digest", "closure_digest", "attempt_id"} {
		cases = append(cases, evidenceCase{name: "missing field " + field})
	}
	cases = append(cases, evidenceCase{"missing serving artifact", func(e *recoveryset.ValidationEvidenceEnvelope) {
		roots := e.ObjectRoots[:0]
		for _, root := range e.ObjectRoots {
			if root.Kind != recoveryset.ObjectRootServingArtifact {
				roots = append(roots, root)
			}
		}
		e.ObjectRoots = roots
	}})
	for _, role := range []recoveryset.DatabaseRole{recoveryset.DatabaseControl, recoveryset.DatabaseDuckLake} {
		cases = append(cases, evidenceCase{"missing database binding " + string(role), func(e *recoveryset.ValidationEvidenceEnvelope) {
			for i := range e.ClusterPoints {
				if e.ClusterPoints[i].DatabaseRole == role {
					e.ClusterPoints[i].DatabaseIdentity = ""
				}
			}
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := f.set
			set.ID = uuid.NewString()
			set.Status = recoveryset.StatusPrepared
			set.PublishedValidationAttemptID = ""
			set.FrontierDigest = ""
			prepared, err := ops.PrepareRecovery(t.Context(), admincli.RecoveryPrepareRequest{Set: set, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			set = prepared.Set
			if set.ID == "" || set.FrontierDigest == "" || !reflect.DeepEqual(set.ClusterPoints, f.set.ClusterPoints) || !reflect.DeepEqual(set.ObjectRoots, f.set.ObjectRoots) {
				t.Fatal("preparation lost frontier identities")
			}
			var matchingHold bool
			// Observe the fixture's cross-owner ancestry without broadening the
			// maintenance role; recovery operations still use that role exclusively.
			if err := fixtureDB.QueryRow(t.Context(), `SELECT EXISTS (
				SELECT 1 FROM delivery.delivery_retention_root r
				JOIN delivery.delivery_candidate c ON c.candidate_id=r.candidate_id AND c.target_id=r.target_id
				WHERE r.root_id=$1 AND r.target_id=$2 AND r.generation_id=$3 AND r.snapshot_seal_id=$4
				AND c.status='qualified' AND c.snapshot_seal_id=r.snapshot_seal_id
				AND r.root_kind='recovery' AND r.state='live' AND r.expires_at > clock_timestamp()
				AND r.evidence->>'recovery_set_id'=$5 AND r.evidence->>'frontier_digest'=$6
			)`, prepared.RootID, set.Delivery.TargetID, set.Delivery.GenerationID, set.Serving.SealID, set.ID, set.FrontierDigest).Scan(&matchingHold); err != nil || !matchingHold {
				t.Fatalf("prepared retention hold is not bound to exact recovery frontier: %v", err)
			}
			var matchingCatalog bool
			if err := fixtureDB.QueryRow(t.Context(), `SELECT EXISTS (
				SELECT 1 FROM ducklake.catalog_identity c
				JOIN ducklake.snapshot_retention r USING (physical_pool_id,catalog_id)
				WHERE c.physical_pool_id=$1 AND c.catalog_id=$2 AND c.catalog_database=$3
				AND c.catalog_uuid=$4 AND r.snapshot_id=$5 AND r.state='live'
			)`, set.Serving.PhysicalPoolID, set.Catalog.CatalogID, set.Catalog.CatalogDatabase, set.Catalog.CatalogUUID, set.Catalog.SnapshotID).Scan(&matchingCatalog); err != nil || !matchingCatalog {
				t.Fatalf("restored catalog/snapshot binding missing: %v", err)
			}
			checkConfig := f.config(nil)
			checkConfig.Recovery = repo
			checkConfig.RecoverySetID = set.ID
			check, err := newPostgresDeliveryStartupCheck(checkConfig)
			if err != nil {
				t.Fatal(err)
			}
			if err := check(t.Context()); err == nil || !strings.Contains(err.Error(), "recovery_set_not_published") {
				t.Fatalf("unpublished readiness diagnostic = %v", err)
			}
			attemptID := uuid.NewString()
			envelope, err := recoveryset.NewValidationEvidenceEnvelope(set, attemptID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.mutate != nil {
				tc.mutate(&envelope)
			}
			encoded, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			request := admincli.RecoveryValidateRequest{SetID: set.ID, AttemptID: attemptID, Validator: "qualification", Evidence: encoded}
			omittedField, omit := strings.CutPrefix(tc.name, "missing field ")
			if omit {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(encoded, &fields); err != nil {
					t.Fatal(err)
				}
				delete(fields, omittedField)
				request.Evidence, err = json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
			}
			first, validationErr := ops.ValidateRecovery(t.Context(), request)
			publish := admincli.RecoveryPublishRequest{SetID: set.ID, Publisher: "qualification", FenceEpoch: set.FenceEpoch, ValidationAttemptID: attemptID}
			if tc.mutate != nil || omit {
				if !errors.Is(validationErr, adminpostgres.ErrRecoveryValidationFailed) {
					t.Fatalf("validation error = %v", validationErr)
				}
				if _, err := ops.PublishRecovery(t.Context(), publish); err == nil {
					t.Fatal("failed attempt published")
				}
				if err := check(t.Context()); err == nil || !strings.Contains(err.Error(), "recovery_set_not_published") {
					t.Fatalf("invalid evidence readiness diagnostic = %v", err)
				}
				attempt, err := repo.ValidationAttempt(t.Context(), attemptID)
				if err != nil || attempt.Status != recoveryset.ValidationFailed || attempt.Error == "" {
					t.Fatalf("failed diagnostic missing: %v", err)
				}
				if _, err := ops.ValidateRecovery(t.Context(), request); !errors.Is(err, adminpostgres.ErrRecoveryValidationFailed) {
					t.Fatalf("failed retry = %v", err)
				}
				persisted, err := repo.ReadExact(t.Context(), set.ID)
				if err != nil || persisted.Status != recoveryset.StatusPrepared || persisted.PublishedValidationAttemptID != "" {
					t.Fatal("failure changed publication selection")
				}
				return
			}
			if validationErr != nil {
				t.Fatal(validationErr)
			}
			replay, err := ops.ValidateRecovery(t.Context(), request)
			if err != nil || !reflect.DeepEqual(first, replay) {
				t.Fatalf("validation replay changed immutable evidence: %v", err)
			}
			otherID := uuid.NewString()
			otherEnvelope, err := recoveryset.NewValidationEvidenceEnvelope(set, otherID)
			if err != nil {
				t.Fatal(err)
			}
			otherBytes, err := json.Marshal(otherEnvelope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ops.ValidateRecovery(t.Context(), admincli.RecoveryValidateRequest{SetID: set.ID, AttemptID: otherID, Validator: "qualification", Evidence: otherBytes}); err != nil {
				t.Fatalf("second valid attempt: %v", err)
			}
			// Even with two valid results, incomplete or fenced publication
			// requests must leave the set unselected and readiness closed.
			beforePublication, err := repo.ReadExact(t.Context(), set.ID)
			if err != nil || beforePublication.Status != recoveryset.StatusPrepared || beforePublication.PublishedValidationAttemptID != "" {
				t.Fatalf("validation selected a publication prematurely: %v", err)
			}
			for _, failure := range []struct {
				name   string
				mutate func(*admincli.RecoveryPublishRequest)
				want   error
			}{
				{"missing attempt", func(r *admincli.RecoveryPublishRequest) { r.ValidationAttemptID = "" }, recoveryset.ErrInvalid},
				{"missing publisher", func(r *admincli.RecoveryPublishRequest) { r.Publisher = "" }, recoveryset.ErrInvalid},
				{"missing fence", func(r *admincli.RecoveryPublishRequest) { r.FenceEpoch = 0 }, recoveryset.ErrInvalid},
				{"unknown attempt", func(r *admincli.RecoveryPublishRequest) { r.ValidationAttemptID = uuid.NewString() }, recoveryset.ErrFenced},
				{"wrong fence", func(r *admincli.RecoveryPublishRequest) { r.FenceEpoch++ }, recoveryset.ErrFenced},
			} {
				t.Run(failure.name, func(t *testing.T) {
					invalid := publish
					failure.mutate(&invalid)
					if _, err := ops.PublishRecovery(t.Context(), invalid); !errors.Is(err, failure.want) {
						t.Fatalf("publication denial = %v, want %v", err, failure.want)
					}
					persisted, err := repo.ReadExact(t.Context(), set.ID)
					if err != nil || !reflect.DeepEqual(persisted, beforePublication) {
						t.Fatalf("failed publication changed recovery state: %v", err)
					}
					if err := check(t.Context()); err == nil || !strings.Contains(err.Error(), "recovery_set_not_published") {
						t.Fatalf("failed publication readiness = %v", err)
					}
				})
			}
			published, err := ops.PublishRecovery(t.Context(), publish)
			if err != nil {
				t.Fatal(err)
			}
			again, err := ops.PublishRecovery(t.Context(), publish)
			if err != nil || !reflect.DeepEqual(published, again) {
				t.Fatalf("publication replay changed state: %v", err)
			}
			if published.Set.ID != set.ID || published.Set.FrontierDigest != set.FrontierDigest || published.Set.PublishedValidationAttemptID != attemptID {
				t.Fatal("publication binding drift")
			}
			if err := check(t.Context()); err != nil {
				t.Fatalf("published recovery readiness: %v", err)
			}
			// An existing successful result cannot be rewritten under its attempt ID.
			envelope.ClosureDigest = "sha256:" + strings.Repeat("d", 64)
			changed, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			conflict := request
			conflict.Evidence = changed
			if _, err := ops.ValidateRecovery(t.Context(), conflict); !errors.Is(err, recoveryset.ErrConflict) {
				t.Fatalf("conflicting validation replay = %v", err)
			}
			stored, err := repo.ValidationResult(t.Context(), attemptID)
			if err != nil || first.Result == nil || !reflect.DeepEqual(stored, *first.Result) {
				t.Fatalf("conflicting replay changed immutable result: %v", err)
			}
			publish.ValidationAttemptID = otherID
			if _, err := ops.PublishRecovery(t.Context(), publish); err == nil {
				t.Fatal("another attempt replaced immutable publication")
			}
			if err := check(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Seed only the pre-existing delivery ancestors needed by the production
// recovery retention FK. Recovery sets, holds, attempts and results are never
// seeded: all are written by the real operations under the maintenance role.
func seedRecoveryAdmissionDelivery(t *testing.T, url string, set recoveryset.RecoverySet) {
	t.Helper()
	db, err := pgx.Connect(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	plan, candidate, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString()
	s := set.Serving
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO delivery.delivery_target(target_id,project_id,environment) VALUES ($1,$2,'prod')`, set.Delivery.TargetID, "project-"+set.Delivery.TargetID)
	exec(`INSERT INTO delivery.delivery_plan(plan_id,target_id,plan_revision,plan_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,artifact_digest,qualification_digest,qualification_required,approval_required,approval_policy_revision,plan_document) VALUES ($1,$2,1,$3,$3,$3,$3,$3,$3,false,false,1,'{}')`, plan, set.Delivery.TargetID, s.PlanDigest)
	exec(`INSERT INTO delivery.delivery_candidate(candidate_id,target_id,plan_id,candidate_revision,artifact_digest) VALUES ($1,$2,$3,1,$4)`, candidate, set.Delivery.TargetID, plan, s.ServingArtifactDigest)
	exec(`INSERT INTO delivery.delivery_build_attempt(attempt_id,plan_id,candidate_id,owner_id,physical_pool_id,catalog_id,fencing_epoch,request_digest,plan_digest,state,namespace,lease_expires_at,session_identity) VALUES ($1,$2,$3,'fixture',$4,$5,1,$6,$7,'running','fixture',now()+interval '1 hour','fixture')`, attempt, plan, candidate, s.PhysicalPoolID, s.CatalogID, s.RequestDigest, s.PlanDigest)
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO delivery.delivery_snapshot_seal SELECT r.* FROM jsonb_populate_record(NULL::delivery.delivery_snapshot_seal, $1::jsonb || jsonb_build_object('attempt_id',$2::text,'candidate_id',$3::text,'qualification_evidence','{}'::jsonb,'qualified_at',now())) r`, string(encoded), attempt, candidate)
	exec(`UPDATE delivery.delivery_candidate SET status='qualified', snapshot_seal_id=$2, qualification_digest=$3, qualified_at=clock_timestamp() WHERE candidate_id=$1`, candidate, s.SealID, s.CompatibilityDigest)
	exec(`INSERT INTO delivery.delivery_generation(generation_id,target_id,candidate_id,snapshot_seal_id,plan_id,plan_digest,artifact_root,artifact_root_digest,serving_artifact_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,generation_revision) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,1)`, set.Delivery.GenerationID, set.Delivery.TargetID, candidate, s.SealID, plan, s.PlanDigest, s.ArtifactRoot, s.ArtifactRootDigest, s.ServingArtifactDigest, s.CompiledGraphDigest, s.CompiledConfigDigest, s.SecurityDomainFingerprint)
}

// Mirrors the existing physical-retention fixture: these are pre-existing
// restored catalog rows, not recovery holds manufactured by the test. The
// real PrepareRecovery operation alone creates delivery recovery holds.
func seedRecoveryAdmissionRetention(t *testing.T, db *pgx.Conn, set recoveryset.RecoverySet, snapshotID int64, state string) {
	t.Helper()
	if _, err := db.Exec(t.Context(), `INSERT INTO ducklake.snapshot_retention(physical_pool_id,catalog_id,snapshot_id,state,created_at,expired_at) VALUES ($1,$2,$3,$4,statement_timestamp()-interval '1 hour',CASE WHEN $4='expired' THEN statement_timestamp() ELSE NULL END)`, set.Serving.PhysicalPoolID, set.Serving.CatalogID, snapshotID, state); err != nil {
		t.Fatal(err)
	}
}
