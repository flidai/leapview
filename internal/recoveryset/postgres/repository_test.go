package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/jackc/pgx/v5/pgxpool"
)

func recoverySetDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "recovery_set_test")
	p, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRecoverySetExactReplayAndFencedPublication(t *testing.T) {
	db := recoverySetDB(t)
	r := New(db)
	in := recoverySetFixture(t)
	prepublished := in
	prepublished.ID = "018f3f83-7b2f-7b37-9f9e-000000000107"
	prepublished.Status = recoveryset.StatusPublished
	prepublished.PublishedValidationAttemptID = "018f3f83-7b2f-7b37-9f9e-000000000108"
	if _, err := r.Create(t.Context(), prepublished); !errors.Is(err, recoveryset.ErrInvalid) {
		t.Fatalf("pre-published create = %v, want invalid", err)
	}
	created, err := r.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if created.FrontierDigest == "" {
		t.Fatal("frontier digest missing")
	}
	got, err := r.ReadExact(t.Context(), in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IdentityEqual(created) || len(got.ClusterPoints) != 2 || len(got.ObjectRoots) != len(in.ObjectRoots) {
		t.Fatalf("exact read=%#v", got)
	}
	if replay, err := r.Create(t.Context(), in); err != nil || !replay.IdentityEqual(created) {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	changed := in
	changed.AuditIdentity = "audit-other"
	if _, err := r.Create(t.Context(), changed); !errors.Is(err, recoveryset.ErrConflict) {
		t.Fatalf("metadata drift=%v", err)
	}
	validationID := "018f3f83-7b2f-7b37-9f9e-000000000109"
	started := time.Now().UTC().Truncate(time.Microsecond)
	attempt := recoveryset.ValidationAttempt{AttemptID: validationID, SetID: in.ID, OwnerID: "validator", FenceEpoch: in.FenceEpoch, AuditIdentity: "audit", Status: recoveryset.ValidationRunning, StartedAt: started}
	if _, err := r.BeginValidation(t.Context(), attempt); err != nil {
		t.Fatal(err)
	}
	envelope, err := recoveryset.NewValidationEvidenceEnvelope(in, validationID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := recoveryset.NewValidationResult(envelope, started.Add(500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RecordValidationResult(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	attempt.Status, attempt.ResultDigest, attempt.CompletedAt = recoveryset.ValidationPassed, result.ResultDigest, started.Add(time.Second)
	if err := r.CompleteValidation(t.Context(), attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Publish(t.Context(), in.ID, "publisher", in.FenceEpoch+1, validationID); !errors.Is(err, recoveryset.ErrFenced) {
		t.Fatalf("stale publish=%v", err)
	}
	if _, err := r.Publish(t.Context(), in.ID, "publisher", in.FenceEpoch, "018f3f83-7b2f-7b37-9f9e-000000000108"); !errors.Is(err, recoveryset.ErrFenced) {
		t.Fatalf("unrelated validation publish=%v", err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE recovery.recovery_set SET status='published', published_by='publisher', published_at=clock_timestamp(), published_validation_attempt_id=$2::uuid WHERE set_id=$1::uuid`, in.ID, "018f3f83-7b2f-7b37-9f9e-000000000108"); err == nil {
		t.Fatal("direct publication without exact passed evidence succeeded")
	}
	published, err := r.Publish(t.Context(), in.ID, "publisher", in.FenceEpoch, validationID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Status != recoveryset.StatusPublished {
		t.Fatalf("status=%s", published.Status)
	}
	if published.PublishedValidationAttemptID != validationID {
		t.Fatalf("published validation attempt = %q, want %q", published.PublishedValidationAttemptID, validationID)
	}
	if replay, err := r.Publish(t.Context(), in.ID, "publisher", in.FenceEpoch, validationID); err != nil || replay.Status != recoveryset.StatusPublished {
		t.Fatalf("publish replay=%#v err=%v", replay, err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO recovery.recovery_object_root(set_id,root_kind,root_uri,version_id,digest) VALUES ($1::uuid,'extra','objects/extra','1',$2)`, in.ID, "sha256:"+strings.Repeat("f", 64)); err == nil {
		t.Fatal("published frontier accepted appended object evidence")
	}
	if err := r.Supersede(t.Context(), in.ID, in.FenceEpoch); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Publish(t.Context(), in.ID, "publisher", in.FenceEpoch, validationID); !errors.Is(err, recoveryset.ErrFenced) {
		t.Fatalf("superseded publish=%v", err)
	}
}

func TestValidationEvidenceIsExactRepeatableAndFenced(t *testing.T) {
	db := recoverySetDB(t)
	r := New(db)
	set := recoverySetFixture(t)
	if _, err := r.Create(t.Context(), set); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := db.Exec(t.Context(), `INSERT INTO recovery.validation_attempt(attempt_id,set_id,owner_id,fence_epoch,audit_identity,status,result_digest,started_at,completed_at) VALUES ('018f3f83-7b2f-7b37-9f9e-000000000113'::uuid,$1::uuid,'validator',$2,'operator','passed',$3,clock_timestamp(),clock_timestamp())`, set.ID, set.FenceEpoch, "sha256:"+strings.Repeat("8", 64)); err == nil {
		t.Fatal("direct terminal validation attempt insertion succeeded")
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO recovery.validation_attempt(attempt_id,set_id,owner_id,fence_epoch,audit_identity,status,started_at) VALUES ('018f3f83-7b2f-7b37-9f9e-000000000114'::uuid,$1::uuid,'validator',$2,'operator','running',clock_timestamp())`, set.ID, set.FenceEpoch+1); err == nil {
		t.Fatal("validation attempt with a stale frontier fence succeeded")
	}
	attempt := recoveryset.ValidationAttempt{AttemptID: "018f3f83-7b2f-7b37-9f9e-000000000110", SetID: set.ID, OwnerID: "validator", FenceEpoch: set.FenceEpoch, AuditIdentity: "operator", Status: recoveryset.ValidationRunning, StartedAt: started}
	created, err := r.BeginValidation(t.Context(), attempt)
	if err != nil {
		t.Fatal(err)
	}
	retryWithFreshClock := attempt
	retryWithFreshClock.StartedAt = started.Add(9 * time.Second)
	if replay, err := r.BeginValidation(t.Context(), retryWithFreshClock); err != nil || replay != created {
		t.Fatalf("attempt replay = %#v, %v", replay, err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO recovery.validation_result(attempt_id,result_digest,evidence,recorded_at) VALUES ('018f3f83-7c7a-7f00-8a11-000000000110'::uuid,$1,'{}'::jsonb,clock_timestamp())`, "sha256:"+strings.Repeat("8", 64)); err == nil {
		t.Fatal("direct arbitrary validation evidence was accepted")
	}
	terminal := attempt
	terminal.Status = recoveryset.ValidationPassed
	terminal.ResultDigest = "sha256:" + strings.Repeat("8", 64)
	terminal.CompletedAt = started.Add(time.Second)
	if err := r.CompleteValidation(t.Context(), terminal); !errors.Is(err, recoveryset.ErrNotFound) {
		t.Fatalf("completion without evidence = %v", err)
	}
	envelope, err := recoveryset.NewValidationEvidenceEnvelope(set, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := recoveryset.NewValidationResult(envelope, started.Add(500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	// Structural equality alone is not enough: a maintenance-role SQL caller
	// must not mint a terminal attempt with a digest unrelated to the exact
	// canonical evidence. The trigger rejects this before the Go repository's
	// digest check or publication path can be reached.
	if _, err := db.Exec(t.Context(), `INSERT INTO recovery.validation_result(attempt_id,result_digest,evidence,recorded_at) VALUES ($1::uuid,$2,$3::jsonb,clock_timestamp())`, attempt.AttemptID, "sha256:"+strings.Repeat("8", 64), result.Evidence); err == nil {
		t.Fatal("validation result accepted an arbitrary canonical-looking digest")
	}
	terminal.ResultDigest = result.ResultDigest
	if err := r.RecordValidationResult(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordValidationResult(t.Context(), result); err != nil {
		t.Fatalf("result replay = %v", err)
	}
	retryResult := result
	retryResult.RecordedAt = retryResult.RecordedAt.Add(time.Second)
	if err := r.RecordValidationResult(t.Context(), retryResult); err != nil {
		t.Fatalf("result replay with caller timestamp = %v", err)
	}
	conflictingEnvelope := envelope
	conflictingEnvelope.ClosureDigest = "sha256:" + strings.Repeat("9", 64)
	conflictingResult, err := recoveryset.NewValidationResult(conflictingEnvelope, result.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RecordValidationResult(t.Context(), conflictingResult); !errors.Is(err, recoveryset.ErrInvalid) {
		t.Fatalf("unbound validation evidence = %v", err)
	}
	wrong := terminal
	wrong.ResultDigest = "sha256:" + strings.Repeat("9", 64)
	if err := r.CompleteValidation(t.Context(), wrong); !errors.Is(err, recoveryset.ErrConflict) {
		t.Fatalf("mismatched terminal digest = %v", err)
	}
	if err := r.CompleteValidation(t.Context(), terminal); err != nil {
		t.Fatal(err)
	}
	terminalRetry := terminal
	terminalRetry.CompletedAt = terminal.CompletedAt.Add(time.Second)
	if err := r.CompleteValidation(t.Context(), terminalRetry); err != nil {
		t.Fatalf("terminal replay with caller timestamp = %v", err)
	}
	if err := r.RecordValidationResult(t.Context(), result); err != nil {
		t.Fatalf("terminal exact replay = %v", err)
	}
	late := result
	late.AttemptID = "018f3f83-7b2f-7b37-9f9e-000000000111"
	lateEnvelope := envelope
	lateEnvelope.AttemptID = late.AttemptID
	lateResult, err := recoveryset.NewValidationResult(lateEnvelope, result.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	late.Evidence, late.ResultDigest = lateResult.Evidence, lateResult.ResultDigest
	if err := r.RecordValidationResult(t.Context(), late); !errors.Is(err, recoveryset.ErrFenced) {
		t.Fatalf("unknown attempt result = %v", err)
	}
	second := attempt
	second.AttemptID = "018f3f83-7b2f-7b37-9f9e-000000000112"
	if _, err := r.BeginValidation(t.Context(), second); err != nil {
		t.Fatalf("second attempt by same audit identity = %v", err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE recovery.validation_attempt SET owner_id='other' WHERE attempt_id=$1::uuid`, second.AttemptID); err == nil {
		t.Fatal("validation attempt identity mutation succeeded")
	}
}

func TestValidationResultRejectsIncompleteFrontierEvidence(t *testing.T) {
	db := recoverySetDB(t)
	set := recoverySetFixture(t)
	if _, err := New(db).Create(t.Context(), set); err != nil {
		t.Fatal(err)
	}
	attemptID := "018f3f83-7c7a-7f00-8a11-000000000120"
	started := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := New(db).BeginValidation(t.Context(), recoveryset.ValidationAttempt{AttemptID: attemptID, SetID: set.ID, OwnerID: "validator", FenceEpoch: set.FenceEpoch, AuditIdentity: set.AuditIdentity, Status: recoveryset.ValidationRunning, StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	envelope, err := recoveryset.NewValidationEvidenceEnvelope(set, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := recoveryset.NewValidationResult(envelope, started)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `ALTER TABLE recovery.recovery_cluster_point DISABLE TRIGGER recovery_cluster_point_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `DELETE FROM recovery.recovery_cluster_point WHERE set_id=$1::uuid AND database_role='control'`, set.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `ALTER TABLE recovery.recovery_cluster_point ENABLE TRIGGER recovery_cluster_point_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO recovery.validation_result(attempt_id,result_digest,evidence,recorded_at) VALUES ($1::uuid,$2,$3::jsonb,clock_timestamp())`, attemptID, result.ResultDigest, result.Evidence); err == nil {
		t.Fatal("validation result accepted with incomplete frontier children")
	}
}

func TestValidationResultSQLDigestMatchesGoJSONEscaping(t *testing.T) {
	db := recoverySetDB(t)
	set := recoverySetFixture(t)
	// encoding/json escapes HTML-sensitive characters and U+2028/U+2029. Keep
	// these values in otherwise-valid frontier fields so the SQL trigger's
	// canonical serializer is exercised against the same bytes as Go.
	set.ClusterPoints[0].ClusterIdentity = "cluster-<>&\u2028middle\u2029x"
	set.Serving.RelationNamespace = "candidate-<>&\u2028middle\u2029x"
	created, err := New(db).Create(t.Context(), set)
	if err != nil {
		t.Fatal(err)
	}
	attemptID := "018f3f83-7c7a-7f00-8a11-000000000130"
	started := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := New(db).BeginValidation(t.Context(), recoveryset.ValidationAttempt{
		AttemptID: attemptID, SetID: created.ID, OwnerID: "validator", FenceEpoch: created.FenceEpoch,
		AuditIdentity: created.AuditIdentity, Status: recoveryset.ValidationRunning, StartedAt: started,
	}); err != nil {
		t.Fatal(err)
	}
	envelope, err := recoveryset.NewValidationEvidenceEnvelope(created, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := recoveryset.NewValidationResult(envelope, started)
	if err != nil {
		t.Fatal(err)
	}
	if err := New(db).RecordValidationResult(t.Context(), result); err != nil {
		t.Fatalf("SQL canonical digest rejected Go evidence: %v", err)
	}
}

func recoverySetFixture(t *testing.T) recoveryset.RecoverySet {
	t.Helper()
	// Keep this fixture compact by reusing the domain fixture through its
	// exported construction contract rather than coupling to SQL rows.
	compat := recoveryset.CompatibilityTuple{DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "ducklake:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	compatDigest, _ := compat.Digest()
	d := func(ch byte) string {
		out := "sha256:"
		for i := 0; i < 64; i++ {
			out += string(ch)
		}
		return out
	}
	return recoveryset.RecoverySet{ID: "018f3f83-7b2f-7b37-9f9e-000000000100", SchemaVersion: recoveryset.SchemaVersion, ClusterPoints: []recoveryset.ClusterRecoveryPoint{{DatabaseRole: recoveryset.DatabaseControl, ClusterIdentity: "cluster", DatabaseIdentity: "control", RecoveryIdentity: "lsn:0/1"}, {DatabaseRole: recoveryset.DatabaseDuckLake, ClusterIdentity: "cluster", DatabaseIdentity: "ducklake", RecoveryIdentity: "lsn:0/1"}}, Delivery: recoveryset.DeliveryPointer{TargetID: "target", GenerationID: "018f3f83-7b2f-7b37-9f9e-000000000101", PublicationID: "018f3f83-7b2f-7b37-9f9e-000000000102", TargetRevision: 1}, Serving: recoveryset.SnapshotSeal{SealID: "018f3f83-7b2f-7b37-9f9e-000000000103", PhysicalPoolID: "pool", TenantDomain: "tenant", Region: "region", EncryptionDomain: "enc", ObjectNamespace: "objects/target", CatalogDatabase: "ducklake", CatalogID: "catalog", CatalogUUID: "catalog-uuid", CatalogVersion: 1, DuckLakeSnapshotID: 1, RelationManifestDigest: d('a'), RelationNamespace: "candidate", ClosureDigest: d('b'), ObjectRoot: "objects/target", ObjectRootDigest: d('c'), ArtifactRoot: "artifacts/target", ArtifactRootDigest: d('d'), ServingArtifactID: "artifact", ServingArtifactDigest: d('e'), CompiledGraphDigest: d('f'), CompiledConfigDigest: d('0'), SecurityDomainFingerprint: d('1'), RequestDigest: d('2'), PlanDigest: d('3'), CompatibilityDigest: compatDigest, DuckDBVersion: "1", RuntimeVersion: "1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1"}, Catalog: recoveryset.CatalogCommit{CatalogID: "catalog", CatalogDatabase: "ducklake", CatalogUUID: "catalog-uuid", CatalogVersion: 1, SnapshotID: 1}, ObjectRoots: []recoveryset.ObjectRoot{{Kind: recoveryset.ObjectRootDuckLake, URI: "objects/target", VersionID: "1", Digest: d('c')}, {Kind: recoveryset.ObjectRootServingArtifact, URI: "artifacts/target", VersionID: "1", Digest: d('d')}}, Compatibility: compat, FenceEpoch: 2, AuditIdentity: "audit", Status: recoveryset.StatusPrepared, CreatedBy: "operator", CreatedAt: time.Now().UTC()}
}
