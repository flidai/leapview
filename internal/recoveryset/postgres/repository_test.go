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
	if _, err := r.Publish(t.Context(), in.ID, "publisher", in.FenceEpoch+1); !errors.Is(err, recoveryset.ErrFenced) {
		t.Fatalf("stale publish=%v", err)
	}
	published, err := r.Publish(t.Context(), in.ID, "publisher", in.FenceEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if published.Status != recoveryset.StatusPublished {
		t.Fatalf("status=%s", published.Status)
	}
	if replay, err := r.Publish(t.Context(), in.ID, "publisher", in.FenceEpoch); err != nil || replay.Status != recoveryset.StatusPublished {
		t.Fatalf("publish replay=%#v err=%v", replay, err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO recovery.recovery_object_root(set_id,root_kind,root_uri,version_id,digest) VALUES ($1::uuid,'extra','objects/extra','1',$2)`, in.ID, "sha256:"+strings.Repeat("f", 64)); err == nil {
		t.Fatal("published frontier accepted appended object evidence")
	}
	if err := r.Supersede(t.Context(), in.ID, in.FenceEpoch); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Publish(t.Context(), in.ID, "publisher", in.FenceEpoch); !errors.Is(err, recoveryset.ErrFenced) {
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
	attempt := recoveryset.ValidationAttempt{AttemptID: "018f3f83-7b2f-7b37-9f9e-000000000110", SetID: set.ID, OwnerID: "validator", FenceEpoch: set.FenceEpoch, AuditIdentity: "operator", Status: recoveryset.ValidationRunning, StartedAt: started}
	created, err := r.BeginValidation(t.Context(), attempt)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := r.BeginValidation(t.Context(), attempt); err != nil || replay != created {
		t.Fatalf("attempt replay = %#v, %v", replay, err)
	}
	terminal := attempt
	terminal.Status = recoveryset.ValidationPassed
	terminal.ResultDigest = "sha256:" + strings.Repeat("8", 64)
	terminal.CompletedAt = started.Add(time.Second)
	if err := r.CompleteValidation(t.Context(), terminal); !errors.Is(err, recoveryset.ErrNotFound) {
		t.Fatalf("completion without evidence = %v", err)
	}
	result := recoveryset.ValidationResult{AttemptID: attempt.AttemptID, ResultDigest: terminal.ResultDigest, Evidence: []byte(`{"seal":"exact", "ok":true}`), RecordedAt: started.Add(500 * time.Millisecond)}
	if err := r.RecordValidationResult(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordValidationResult(t.Context(), result); err != nil {
		t.Fatalf("result replay = %v", err)
	}
	conflict := result
	conflict.Evidence = []byte(`{"seal":"different"}`)
	if err := r.RecordValidationResult(t.Context(), conflict); !errors.Is(err, recoveryset.ErrConflict) {
		t.Fatalf("conflicting result replay = %v", err)
	}
	wrong := terminal
	wrong.ResultDigest = "sha256:" + strings.Repeat("9", 64)
	if err := r.CompleteValidation(t.Context(), wrong); !errors.Is(err, recoveryset.ErrConflict) {
		t.Fatalf("mismatched terminal digest = %v", err)
	}
	if err := r.CompleteValidation(t.Context(), terminal); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordValidationResult(t.Context(), result); err != nil {
		t.Fatalf("terminal exact replay = %v", err)
	}
	late := result
	late.AttemptID = "018f3f83-7b2f-7b37-9f9e-000000000111"
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
	return recoveryset.RecoverySet{ID: "018f3f83-7b2f-7b37-9f9e-000000000100", SchemaVersion: recoveryset.SchemaVersion, ClusterPoints: []recoveryset.ClusterRecoveryPoint{{DatabaseRole: recoveryset.DatabaseControl, ClusterIdentity: "cluster", DatabaseIdentity: "control", RecoveryIdentity: "lsn:0/1"}, {DatabaseRole: recoveryset.DatabaseDuckLake, ClusterIdentity: "cluster", DatabaseIdentity: "ducklake", RecoveryIdentity: "lsn:0/1"}}, Delivery: recoveryset.DeliveryPointer{TargetID: "target", GenerationID: "018f3f83-7b2f-7b37-9f9e-000000000101", PublicationID: "018f3f83-7b2f-7b37-9f9e-000000000102", TargetRevision: 1}, Serving: recoveryset.SnapshotSeal{SealID: "018f3f83-7b2f-7b37-9f9e-000000000103", PhysicalPoolID: "pool", TenantDomain: "tenant", Region: "region", EncryptionDomain: "enc", ObjectNamespace: "objects/target", CatalogDatabase: "ducklake", CatalogID: "catalog", CatalogUUID: "catalog-uuid", CatalogVersion: 1, DuckLakeSnapshotID: 1, RelationManifestDigest: d('a'), RelationNamespace: "candidate", ClosureDigest: d('b'), ObjectRoot: "objects/target", ObjectRootDigest: d('c'), ArtifactRoot: "artifacts/target", ArtifactRootDigest: d('d'), ServingArtifactID: "artifact", ServingArtifactDigest: d('e'), CompiledGraphDigest: d('f'), CompiledConfigDigest: d('0'), SecurityDomainFingerprint: d('1'), RequestDigest: d('2'), PlanDigest: d('3'), CompatibilityDigest: compatDigest, DuckDBVersion: "1", RuntimeVersion: "1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1"}, Catalog: recoveryset.CatalogCommit{CatalogID: "catalog", CatalogDatabase: "ducklake", CatalogUUID: "catalog-uuid", CatalogVersion: 1, SnapshotID: 1}, ObjectRoots: []recoveryset.ObjectRoot{{Kind: "catalog", URI: "s3://bucket/catalog", VersionID: "1", Digest: d('4'), ProviderRecoveryFrontier: "v1"}}, Compatibility: compat, FenceEpoch: 2, AuditIdentity: "audit", Status: recoveryset.StatusPrepared, CreatedBy: "operator", CreatedAt: time.Now().UTC()}
}
