package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	analyticscache "github.com/flidai/leapview/internal/analytics/cache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func cacheTestDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

const cacheTestOriginSeal = "00000000-0000-0000-0000-000000000001"
const cacheTestOriginSeal2 = "00000000-0000-0000-0000-000000000002"
const cacheTestOriginSeal3 = "00000000-0000-0000-0000-000000000003"

func cacheTestEvidence(reason string) json.RawMessage {
	evidence, err := lifecycleEvidence(json.RawMessage(`{"version":1,"reason":"` + reason + `"}`))
	if err != nil {
		panic(err)
	}
	return evidence
}

func cacheTestPublish(t *testing.T, repo *Repository, ctx context.Context, in PublishInput) (Manifest, error) {
	t.Helper()
	fence, err := repo.AcquireL3ObjectFence(ctx, AcquireL3ObjectFenceInput{
		StorageSecurityDomain: in.StorageSecurityDomain, ObjectKey: in.ObjectKey,
		OwnerID: in.Lease.OwnerID, Lease: time.Minute,
	})
	if err != nil {
		t.Fatalf("acquire test object fence: %v", err)
	}
	in.ObjectFence = fence
	manifest, publishErr := repo.Publish(ctx, in)
	if releaseErr := repo.ReleaseL3ObjectFence(context.WithoutCancel(ctx), fence); releaseErr != nil {
		t.Fatalf("release test object fence: %v", releaseErr)
	}
	return manifest, publishErr
}

func cacheTestPrepareL3ObjectGC(t *testing.T, maintenance *Maintenance, ctx context.Context, storageDomain, objectKey string) (bool, error) {
	t.Helper()
	fence, err := maintenance.AcquireL3ObjectFence(ctx, AcquireL3ObjectFenceInput{
		StorageSecurityDomain: storageDomain, ObjectKey: objectKey, OwnerID: "gc-test", Lease: time.Minute,
	})
	if err != nil {
		t.Fatalf("acquire test GC object fence: %v", err)
	}
	eligible, prepareErr := maintenance.PrepareL3ObjectGC(ctx, fence)
	if releaseErr := maintenance.ReleaseL3ObjectFence(context.WithoutCancel(ctx), fence); releaseErr != nil {
		t.Fatalf("release test GC object fence: %v", releaseErr)
	}
	return eligible, prepareErr
}

func TestLifecycleEvidenceValidation(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"missing":   nil,
		"empty":     json.RawMessage(`{}`),
		"version":   json.RawMessage(`{"version":2,"reason":"refresh"}`),
		"reason":    json.RawMessage(`{"version":1,"reason":"   "}`),
		"duplicate": json.RawMessage(`{"version":1,"version":1,"reason":"refresh"}`),
		"oversized": json.RawMessage(strings.Repeat("x", maxEvidenceBytes+1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := lifecycleEvidence(raw); !errors.Is(err, ErrInvalid) {
				t.Fatalf("evidence error = %v, want invalid", err)
			}
		})
	}
	canonical, err := lifecycleEvidence(json.RawMessage(`{"reason":"refresh","version":1}`))
	if err != nil || !bytes.Equal(canonical, cacheTestEvidence("refresh")) {
		t.Fatalf("canonical evidence = %s, %v", canonical, err)
	}
}

func cacheTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "cache_test")
	p, err := pgxpool.New(t.Context(), database.AdminURL())
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

func TestManifestKeyIdentityConvergesWithL1CacheKey(t *testing.T) {
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{Kind: resultidentity.PartitionProduction, TargetID: "target_sales", ProjectID: "project_sales", Environment: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	dependency, err := resultidentity.NewDependency(resultidentity.DependencyInput{SemanticModelID: "semantic_sales", SemanticModelDigest: cacheTestDigest('a'), Relations: []resultidentity.RelationRevision{{RelationID: "orders", RevisionDigest: cacheTestDigest('b')}}, BindingFingerprint: cacheTestDigest('c'), Execution: resultidentity.ExecutionIdentity{PlannerDigest: cacheTestDigest('d'), RuntimeDigest: cacheTestDigest('e'), CapabilityDigest: cacheTestDigest('f'), SettingsDigest: cacheTestDigest('0')}, ResultFormat: resultidentity.ResultFormat{Name: "arrow-ipc", Version: 1}})
	if err != nil {
		t.Fatal(err)
	}
	queryDigest := cacheTestDigest('9')
	l1, err := analyticscache.NewKey(analyticscache.KeyInput{Partition: partition, Dependency: dependency, EffectivePolicyFingerprint: cacheTestDigest('1'), CanonicalQueryDigest: queryDigest})
	if err != nil {
		t.Fatal(err)
	}
	durable, err := ManifestKeyFromIdentity(l1)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := durable.CacheKeyDigest(); err != nil || got != l1.Digest() {
		t.Fatalf("L1/durable key digest = %q, %v; want %q", got, err, l1.Digest())
	}
}

func TestDatabaseNamespaceKeyMatchesCanonicalJSONEscaping(t *testing.T) {
	p := cacheTestDB(t)
	defer p.Close()
	for name, ns := range map[string]Namespace{
		"quotes and slashes": {PartitionKind: PartitionCandidate, TargetID: "target_sales", ProjectID: "project_sales", Environment: "prod", CandidateID: `candidate"\\quoted`},
		"HTML characters":    {PartitionKind: PartitionProduction, TargetID: "target<&>", ProjectID: "project_sales", Environment: "prod"},
		"line separator":     {PartitionKind: PartitionProduction, TargetID: "target\u2028one", ProjectID: "project_sales", Environment: "prod"},
	} {
		t.Run(name, func(t *testing.T) {
			var got string
			if err := p.QueryRow(t.Context(), `SELECT cache.namespace_key($1,$2,$3,$4,$5)`, ns.PartitionKind, ns.TargetID, ns.ProjectID, ns.Environment, nullableString(ns.CandidateID)).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != ns.Key() {
				t.Fatalf("database namespace key = %q, want Go canonical %q", got, ns.Key())
			}
		})
	}
}

func cacheTestKeyForTarget(target string) ManifestKey {
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{Kind: resultidentity.PartitionProduction, TargetID: target, ProjectID: "project_sales", Environment: "prod"})
	if err != nil {
		panic(err)
	}
	return ManifestKey{PartitionKind: PartitionProduction, TargetID: partition.TargetID(), ProjectID: partition.ProjectID().String(), Environment: partition.Environment(), PartitionFormatVersion: int64(partition.Version()), DependencyDigest: cacheTestDigest('a'), PolicyFingerprint: cacheTestDigest('b'), CanonicalQueryDigest: cacheTestDigest('c'), KeyFormatVersion: 2}
}

func cacheTestKey() ManifestKey { return cacheTestKeyForTarget("target_sales") }

func cacheTestNamespaceForTarget(target string) Namespace {
	return Namespace{PartitionKind: PartitionProduction, TargetID: target, ProjectID: "project_sales", Environment: "prod"}
}

func cacheTestNamespace() Namespace { return cacheTestNamespaceForTarget("target_sales") }

func TestRepositoryFillFencePublishLookupAndDependencyInvalidation(t *testing.T) {
	p := cacheTestDB(t)
	repo := New(p)
	ctx := context.Background()
	key := cacheTestKey()
	fillKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	first, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: fillKey, OwnerID: "node-a", Lease: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: fillKey, OwnerID: "node-b", Lease: time.Second}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second fill error = %v, want ErrBusy", err)
	}
	manifest, err := cacheTestPublish(t, repo, ctx, PublishInput{Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/" + cacheTestDigest('e'), ByteSize: 42, Metadata: []byte(`{"rows":1}`), Lease: first})
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := repo.Lookup(ctx, key)
	if err != nil || !found {
		t.Fatalf("Lookup() = %#v, %v, found=%v", got, err, found)
	}
	if got.ManifestID != manifest.ManifestID || got.ByteSize != 42 || string(got.Metadata) != `{"rows": 1}` {
		t.Fatalf("manifest = %#v", got)
	}
	released, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: fillKey, OwnerID: "node-c", Lease: time.Second})
	if err != nil {
		t.Fatalf("published fill was not released: %v", err)
	}
	if err := repo.ReleaseFill(ctx, released); err != nil {
		t.Fatal(err)
	}
	invalidation := NamespaceInvalidationInput{Namespace: cacheTestNamespace(), Kind: DependencyCustom, DependencyID: "orders", DependencyDigest: key.DependencyDigest, IdempotencyKey: "invalidate-test", Reason: "dependency refresh"}
	missingIdempotency := invalidation
	missingIdempotency.IdempotencyKey = ""
	if _, err := repo.InvalidateNamespace(ctx, missingIdempotency, cacheTestEvidence("dependency-refresh")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing invalidation idempotency key = %v, want invalid", err)
	}
	if _, err := repo.InvalidateNamespace(ctx, invalidation, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing invalidation evidence = %v, want invalid", err)
	}
	changed, err := repo.InvalidateNamespace(ctx, invalidation, cacheTestEvidence("dependency-refresh"))
	if err != nil || changed.RetiredManifests != 1 {
		t.Fatalf("InvalidateNamespace() = %#v, %v", changed, err)
	}
	replay, err := repo.InvalidateNamespace(ctx, invalidation, cacheTestEvidence("dependency-refresh"))
	if err != nil || replay.EventID != changed.EventID || replay.NamespaceEpoch != changed.NamespaceEpoch || replay.RetiredManifests != changed.RetiredManifests {
		t.Fatalf("invalidation replay = %#v, %v; want %#v", replay, err, changed)
	}
	if epoch, err := repo.CurrentEpoch(ctx, cacheTestNamespace()); err != nil || epoch != changed.NamespaceEpoch {
		t.Fatalf("epoch after replay = %d, %v; want %d", epoch, err, changed.NamespaceEpoch)
	}
	if _, found, err := repo.Lookup(ctx, key); err != nil || found {
		t.Fatalf("invalidated manifest lookup = found %v, err %v", found, err)
	}
}

func TestTargetIsolationAndOriginSealProvenance(t *testing.T) {
	p := cacheTestDB(t)
	defer p.Close()
	repo := New(p)
	ctx := t.Context()
	nsA := cacheTestNamespaceForTarget("target-a")
	nsB := cacheTestNamespaceForTarget("target-b")
	keyA := cacheTestKeyForTarget("target-a")
	keyB := cacheTestKeyForTarget("target-b")
	digestA, err := keyA.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	digestB, err := keyB.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	if digestA == digestB || nsA.Key() == nsB.Key() {
		t.Fatal("target identity did not separate cache key and namespace")
	}
	leaseA, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: nsA, CacheKey: digestA, OwnerID: "target-a", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	leaseB, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: nsB, CacheKey: digestB, OwnerID: "target-b", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	manifestA, err := cacheTestPublish(t, repo, ctx, PublishInput{Key: keyA, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/shared-object", ByteSize: 1, Lease: leaseA})
	if err != nil {
		t.Fatal(err)
	}
	manifestB, err := cacheTestPublish(t, repo, ctx, PublishInput{Key: keyB, OriginSnapshotSealID: cacheTestOriginSeal2, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/shared-object", ByteSize: 1, Lease: leaseB})
	if err != nil {
		t.Fatal(err)
	}
	if manifestA.ManifestID == manifestB.ManifestID || manifestA.OriginSnapshotSealID != cacheTestOriginSeal || manifestB.OriginSnapshotSealID != cacheTestOriginSeal2 {
		t.Fatalf("target manifests/provenance collided: A=%#v B=%#v", manifestA, manifestB)
	}
	gotA, found, err := repo.Lookup(ctx, keyA)
	if err != nil || !found || gotA.OriginSnapshotSealID != cacheTestOriginSeal {
		t.Fatalf("target A lookup = %#v, found=%v, err=%v", gotA, found, err)
	}
	gotB, found, err := repo.Lookup(ctx, keyB)
	if err != nil || !found || gotB.OriginSnapshotSealID != cacheTestOriginSeal2 {
		t.Fatalf("target B lookup = %#v, found=%v, err=%v", gotB, found, err)
	}
	// Provenance is intentionally not part of the lookup identity: an
	// equivalent result produced after a seal cutover reuses the admitted
	// manifest rather than creating a duplicate key.
	reuseLease, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: nsB, CacheKey: digestB, OwnerID: "target-b-reuse", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	reused, err := cacheTestPublish(t, repo, ctx, PublishInput{Key: keyB, OriginSnapshotSealID: cacheTestOriginSeal3, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/shared-object", ByteSize: 1, Lease: reuseLease})
	if err != nil || reused.ManifestID != manifestB.ManifestID || reused.OriginSnapshotSealID != cacheTestOriginSeal2 {
		t.Fatalf("equivalent result did not reuse manifest provenance: %#v, err=%v", reused, err)
	}
	listA, err := repo.ListByDependency(ctx, PartitionProduction, "target-a", "project_sales", "prod", "", keyA.DependencyDigest, 10)
	if err != nil || len(listA) != 1 || listA[0].Key.TargetID != "target-a" {
		t.Fatalf("target A dependency list = %#v, err=%v", listA, err)
	}
	listB, err := repo.ListByDependency(ctx, PartitionProduction, "target-b", "project_sales", "prod", "", keyB.DependencyDigest, 10)
	if err != nil || len(listB) != 1 || listB[0].Key.TargetID != "target-b" {
		t.Fatalf("target B dependency list = %#v, err=%v", listB, err)
	}
	if _, err := repo.InvalidateNamespace(ctx, NamespaceInvalidationInput{Namespace: nsA, Kind: DependencyCustom, DependencyID: "orders", DependencyDigest: keyA.DependencyDigest, IdempotencyKey: "target-a-invalidate", Reason: "target refresh"}, cacheTestEvidence("target-refresh")); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repo.Lookup(ctx, keyA); err != nil || found {
		t.Fatalf("target A remained admitted after invalidation: found=%v err=%v", found, err)
	}
	if _, found, err := repo.Lookup(ctx, keyB); err != nil || !found {
		t.Fatalf("target B was affected by target A invalidation: found=%v err=%v", found, err)
	}
	maintenance := NewMaintenance(p)
	eligible, err := cacheTestPrepareL3ObjectGC(t, maintenance, ctx, cacheTestDigest('d'), "cache/shared-object")
	if err != nil || eligible {
		t.Fatalf("pool-wide GC ignored foreign target manifest: eligible=%v, err=%v", eligible, err)
	}
	foreignEligible, err := cacheTestPrepareL3ObjectGC(t, maintenance, ctx, cacheTestDigest('f'), "cache/shared-object")
	if err != nil || !foreignEligible {
		t.Fatalf("pool-wide GC crossed security domain: eligible=%v, err=%v", foreignEligible, err)
	}
}

func TestRepositoryRetireThenRepublishPreservesManifestHistory(t *testing.T) {
	p := cacheTestDB(t)
	defer p.Close()
	repo := New(p)
	ctx := t.Context()
	key := cacheTestKey()
	cacheKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	firstLease, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: cacheKey, OwnerID: "history-a", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	first, err := cacheTestPublish(t, repo, ctx, PublishInput{Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/history-1", ByteSize: 1, Lease: firstLease})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InvalidateNamespace(ctx, NamespaceInvalidationInput{Namespace: cacheTestNamespace(), Kind: DependencyCustom, DependencyID: "history", DependencyDigest: key.DependencyDigest, IdempotencyKey: "history-invalidate", Reason: "history refresh"}, cacheTestEvidence("history-refresh")); err != nil {
		t.Fatal(err)
	}
	secondLease, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: cacheKey, OwnerID: "history-b", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	second, err := cacheTestPublish(t, repo, ctx, PublishInput{Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('f'), ObjectKey: "cache/history-2", ByteSize: 2, Lease: secondLease})
	if err != nil {
		t.Fatal(err)
	}
	if first.ManifestID == second.ManifestID {
		t.Fatalf("republish reused retired manifest %s", first.ManifestID)
	}
	var admitted, retiring int
	if err := p.QueryRow(ctx, `SELECT count(*) FILTER (WHERE state='admitted'),count(*) FILTER (WHERE state='retiring') FROM cache.cache_manifest WHERE partition_kind=$1 AND project_id=$2 AND environment=$3 AND candidate_id IS NULL AND dependency_digest=$4`, key.PartitionKind, key.ProjectID, key.Environment, key.DependencyDigest).Scan(&admitted, &retiring); err != nil {
		t.Fatal(err)
	}
	if admitted != 1 || retiring != 1 {
		t.Fatalf("manifest history admitted=%d retiring=%d, want 1/1", admitted, retiring)
	}
}

func TestPrepareL3ObjectGCTombstonesExpiredManifestAfterRootsDrain(t *testing.T) {
	p := cacheTestDB(t)
	repo := New(p)
	maintenance := NewMaintenance(p)
	ctx := t.Context()
	key := cacheTestKey()
	cacheKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: cacheKey, OwnerID: "gc-lifecycle", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().UTC().Add(time.Second)
	objectKey := "cache/l3/sd/" + cacheTestDigest('d') + "/" + cacheTestDigest('a') + "/" + cacheTestDigest('e')
	manifest, err := cacheTestPublish(t, repo, ctx, PublishInput{Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: objectKey, ByteSize: 1, Lease: lease, ExpiresAt: &expiresAt})
	if err != nil {
		t.Fatal(err)
	}
	rootID := uuid.New()
	if err := repo.AddRetentionRoot(ctx, rootID, manifest.ManifestID, "active reader"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(expiresAt) + 50*time.Millisecond)
	eligible, err := cacheTestPrepareL3ObjectGC(t, maintenance, ctx, cacheTestDigest('d'), objectKey)
	if err != nil || eligible {
		t.Fatalf("rooted expired manifest eligibility = %v, err=%v", eligible, err)
	}
	var state string
	if err := p.QueryRow(ctx, `SELECT state FROM cache.cache_manifest WHERE manifest_id=$1`, manifest.ManifestID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != StateRetiring {
		t.Fatalf("expired rooted manifest state = %q, want retiring", state)
	}
	if err := repo.RetireRetentionRoot(ctx, rootID, cacheTestEvidence("reader-drain")); err != nil {
		t.Fatal(err)
	}
	if err := repo.ExpireRetentionRoot(ctx, rootID, cacheTestEvidence("reader-drain")); err != nil {
		t.Fatal(err)
	}
	eligible, err = cacheTestPrepareL3ObjectGC(t, maintenance, ctx, cacheTestDigest('d'), objectKey)
	if err != nil || !eligible {
		t.Fatalf("drained manifest eligibility = %v, err=%v", eligible, err)
	}
	var reason string
	if err := p.QueryRow(ctx, `SELECT state,expire_evidence->>'reason' FROM cache.cache_manifest WHERE manifest_id=$1`, manifest.ManifestID).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != StateExpired || reason != "l3-orphan-gc" {
		t.Fatalf("terminal manifest state/reason = %q/%q", state, reason)
	}
}

func TestAdmitManifestRejectsChangedObjectBeforeBindingLease(t *testing.T) {
	p := cacheTestDB(t)
	defer p.Close()
	repo := New(p)
	ctx := t.Context()
	key := cacheTestKey()
	cacheKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	firstLease, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: cacheKey, OwnerID: "admit-a", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cacheTestPublish(t, repo, ctx, PublishInput{Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/admit", ByteSize: 1, Lease: firstLease}); err != nil {
		t.Fatal(err)
	}
	secondLease, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: cacheKey, OwnerID: "admit-b", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	manifestID, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	objectFence, err := repo.AcquireL3ObjectFence(ctx, AcquireL3ObjectFenceInput{StorageSecurityDomain: cacheTestDigest('d'), ObjectKey: "cache/admit-other", OwnerID: secondLease.OwnerID, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.ReleaseL3ObjectFence(context.WithoutCancel(ctx), objectFence) //nolint:errcheck // test cleanup
	_, err = p.Exec(ctx, `SELECT cache.admit_manifest($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21::uuid,$22,$23,$24,$25::jsonb,$26::uuid,$27)`, manifestID, secondLease.LeaseID, secondLease.CacheKey, secondLease.OwnerID, secondLease.FencingEpoch, secondLease.Namespace.Key(), secondLease.NamespaceEpoch, key.PartitionKind, key.TargetID, key.ProjectID, key.Environment, candidateArg(key.CandidateID), key.PartitionFormatVersion, key.DependencyDigest, key.PolicyFingerprint, key.CanonicalQueryDigest, key.KeyFormatVersion, cacheTestDigest('d'), cacheTestDigest('f'), "cache/admit-other", objectFence.LeaseID, objectFence.OwnerID, objectFence.FencingEpoch, 1, `{}`, cacheTestOriginSeal, nil)
	if err == nil || !strings.Contains(err.Error(), "cache manifest conflict") {
		t.Fatalf("changed direct admission error = %v, want manifest conflict", err)
	}
	var bound bool
	if err := p.QueryRow(ctx, `SELECT manifest_id IS NOT NULL FROM cache.cache_fill_lease WHERE lease_id=$1`, secondLease.LeaseID).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound {
		t.Fatal("conflicting admission bound the fill lease")
	}
}

func TestExpireManifestCapabilityHonorsRetentionRoots(t *testing.T) {
	p := cacheTestDB(t)
	defer p.Close()
	repo := New(p)
	ctx := t.Context()
	key := cacheTestKey()
	cacheKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: cacheKey, OwnerID: "expire-guard", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := cacheTestPublish(t, repo, ctx, PublishInput{Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/expire-guard", ByteSize: 1, Lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddRetentionRoot(ctx, rootID, manifest.ManifestID, "guard"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InvalidateNamespace(ctx, NamespaceInvalidationInput{Namespace: cacheTestNamespace(), Kind: DependencyCustom, DependencyID: "guard", DependencyDigest: key.DependencyDigest, IdempotencyKey: "guard-invalidate", Reason: "guard"}, cacheTestEvidence("guard")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `SELECT cache.expire_manifest($1,$2::jsonb)`, manifest.ManifestID, cacheTestEvidence("bypass")); err == nil {
		t.Fatal("direct manifest expiry bypassed live retention root")
	}
	var state string
	if err := p.QueryRow(ctx, `SELECT state FROM cache.cache_manifest WHERE manifest_id=$1`, manifest.ManifestID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != StateRetiring {
		t.Fatalf("manifest state after rejected expiry = %q, want retiring", state)
	}
}

func TestRepositoryPruneUsesOneTotalBudget(t *testing.T) {
	p := cacheTestDB(t)
	defer p.Close()
	repo := New(p)
	ctx := t.Context()
	ns := cacheTestNamespace()
	if _, err := repo.InvalidateNamespace(ctx, NamespaceInvalidationInput{Namespace: ns, Kind: DependencyCustom, DependencyID: "prune", IdempotencyKey: "prune-event", Reason: "prune test"}, cacheTestEvidence("prune-event")); err != nil {
		t.Fatal(err)
	}
	lease, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: ns, CacheKey: cacheTestDigest('8'), OwnerID: "prune-owner", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE cache.cache_fill_lease SET acquired_at=clock_timestamp()-interval '2 seconds',expires_at=clock_timestamp()-interval '1 second' WHERE lease_id=$1`, lease.LeaseID); err != nil {
		t.Fatal(err)
	}
	stats, err := NewMaintenance(p).Prune(ctx, PruneOptions{Before: time.Now().Add(time.Hour), Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Invalidations+stats.ExpiredLeases != 1 {
		t.Fatalf("prune consumed %d rows with limit 1: %#v", stats.Invalidations+stats.ExpiredLeases, stats)
	}
}

func TestPublishAdmissionRechecksNamespaceAfterConcurrentInvalidation(t *testing.T) {
	p := cacheTestDB(t)
	defer p.Close()
	repo := New(p)
	ctx := t.Context()
	key := cacheTestKey()
	cacheKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: cacheKey, OwnerID: "race-publish", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	objectFence, err := repo.AcquireL3ObjectFence(ctx, AcquireL3ObjectFenceInput{StorageSecurityDomain: cacheTestDigest('d'), ObjectKey: "cache/race-publish", OwnerID: lease.OwnerID, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.ReleaseL3ObjectFence(context.WithoutCancel(ctx), objectFence) //nolint:errcheck // test cleanup
	lockTx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(ctx)
	var epoch int64
	if err := lockTx.QueryRow(ctx, `SELECT epoch FROM cache.cache_namespace_epoch WHERE namespace_key=$1 FOR UPDATE`, cacheTestNamespace().Key()).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	pubTx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, publishErr := repo.PublishTx(ctx, pubTx, PublishInput{Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/race-publish", ByteSize: 1, Lease: lease, ObjectFence: objectFence})
		result <- publishErr
	}()
	pid := pubTx.Conn().PgConn().PID()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var blocked int
		if err := p.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1))`, pid).Scan(&blocked); err == nil && blocked > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if time.Now().After(deadline) {
		t.Fatal("publish admission did not block on namespace lock")
	}
	invalidID, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	var invalidEpoch int64
	if err := lockTx.QueryRow(ctx, `SELECT namespace_epoch FROM cache.invalidate_namespace($1,$2,'custom','concurrent','',1,'concurrent-race','concurrent race',$3::jsonb)`, invalidID, cacheTestNamespace().Key(), cacheTestEvidence("concurrent-race")).Scan(&invalidEpoch); err != nil {
		t.Fatal(err)
	}
	if invalidEpoch != epoch+1 {
		t.Fatalf("concurrent invalidation epoch=%d, want %d", invalidEpoch, epoch+1)
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrStaleFence) {
		t.Fatalf("publish after concurrent invalidation = %v, want stale fence", err)
	}
	_ = pubTx.Rollback(ctx)
	var manifests int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM cache.cache_manifest`).Scan(&manifests); err != nil {
		t.Fatal(err)
	}
	if manifests != 0 {
		t.Fatalf("stale concurrent publish admitted %d manifests", manifests)
	}
}

func TestPublishAdmissionRechecksObjectFenceAfterConcurrentTakeover(t *testing.T) {
	p := cacheTestDB(t)
	repo := New(p)
	ctx := t.Context()
	key := cacheTestKey()
	cacheKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: cacheKey, OwnerID: "race-object", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	objectKey := "cache/l3/sd/" + cacheTestDigest('d') + "/" + cacheTestDigest('a') + "/" + cacheTestDigest('e')
	objectFence, err := repo.AcquireL3ObjectFence(ctx, AcquireL3ObjectFenceInput{StorageSecurityDomain: cacheTestDigest('d'), ObjectKey: objectKey, OwnerID: lease.OwnerID, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	lockTx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(ctx) //nolint:errcheck // test cleanup
	var epoch int64
	if err := lockTx.QueryRow(ctx, `SELECT fencing_epoch FROM cache.cache_l3_object_fence WHERE storage_security_domain=$1 AND object_key=$2 FOR UPDATE`, cacheTestDigest('d'), objectKey).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	pubTx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer pubTx.Rollback(ctx) //nolint:errcheck // test cleanup
	result := make(chan error, 1)
	go func() {
		_, publishErr := repo.PublishTx(ctx, pubTx, PublishInput{Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: objectKey, ByteSize: 1, Lease: lease, ObjectFence: objectFence})
		result <- publishErr
	}()
	pid := pubTx.Conn().PgConn().PID()
	deadline := time.Now().Add(2 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		var blockers int
		if err := p.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1))`, pid).Scan(&blockers); err == nil && blockers > 0 {
			blocked = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("publish admission did not block on exact object-fence row")
	}
	successor := uuid.New()
	if _, err := lockTx.Exec(ctx, `UPDATE cache.cache_l3_object_fence SET lease_id=$1,owner_id='gc-successor',fencing_epoch=fencing_epoch+1,expires_at=clock_timestamp()+interval '1 minute',acquired_at=clock_timestamp() WHERE storage_security_domain=$2 AND object_key=$3`, successor, cacheTestDigest('d'), objectKey); err != nil {
		t.Fatal(err)
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrStaleFence) {
		t.Fatalf("publish after object-fence takeover = %v, want stale fence", err)
	}
	var manifests int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM cache.cache_manifest`).Scan(&manifests); err != nil {
		t.Fatal(err)
	}
	if manifests != 0 {
		t.Fatalf("stale object-fence publish admitted %d manifests", manifests)
	}
}

func TestRepositoryRejectsCrossNamespacePublish(t *testing.T) {
	p := cacheTestDB(t)
	repo := New(p)
	key := cacheTestKey()
	fillKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	wrongNamespace := Namespace{PartitionKind: PartitionProduction, TargetID: "target_sales", ProjectID: "project_other", Environment: "prod"}
	lease, err := repo.AcquireFill(t.Context(), AcquireFillInput{Namespace: wrongNamespace, CacheKey: fillKey, OwnerID: "wrong-scope", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cacheTestPublish(t, repo, t.Context(), PublishInput{Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/wrong-scope", ByteSize: 1, Lease: lease})
	if !errors.Is(err, ErrStaleFence) {
		t.Fatalf("cross-namespace publish error = %v, want stale fence", err)
	}
}

func TestRepositoryConcurrentFillAcquisitionHasOneOwnerAndAdvancesFence(t *testing.T) {
	p := cacheTestDB(t)
	repo := New(p)
	key := cacheTestDigest('f')
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan FillLease, 2)
	errs := make(chan error, 2)
	for _, owner := range []string{"node-a", "node-b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			lease, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: key, OwnerID: owner, Lease: time.Second})
			results <- lease
			errs <- err
		}(owner)
	}
	wg.Wait()
	close(results)
	close(errs)
	var acquired FillLease
	count := 0
	for err := range errs {
		if err == nil {
			count++
		} else if !errors.Is(err, ErrBusy) {
			t.Fatalf("concurrent acquisition error = %v", err)
		}
	}
	for lease := range results {
		if lease.LeaseID != uuid.Nil {
			acquired = lease
		}
	}
	if count != 1 || acquired.LeaseID == uuid.Nil {
		t.Fatalf("acquisition count=%d lease=%#v", count, acquired)
	}
	if err := repo.ReleaseFill(ctx, acquired); err != nil {
		t.Fatal(err)
	}
	next, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: key, OwnerID: "node-c", Lease: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if next.FencingEpoch <= acquired.FencingEpoch {
		t.Fatalf("fence epoch did not advance: old=%d new=%d", acquired.FencingEpoch, next.FencingEpoch)
	}
	if err := repo.ReleaseFill(ctx, next); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryExpiredOwnerIsFencedBeforePublish(t *testing.T) {
	p := cacheTestDB(t)
	repo := New(p)
	ctx := context.Background()
	key := cacheTestDigest('7')
	old, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: key, OwnerID: "node-old", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE cache.cache_fill_lease SET acquired_at=clock_timestamp()-interval '2 seconds', expires_at=clock_timestamp()-interval '1 second' WHERE lease_id=$1`, old.LeaseID); err != nil {
		t.Fatal(err)
	}
	next, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: key, OwnerID: "node-new", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if next.FencingEpoch <= old.FencingEpoch {
		t.Fatalf("fence did not advance: old=%d new=%d", old.FencingEpoch, next.FencingEpoch)
	}
	if err := repo.RenewFill(ctx, old, time.Minute); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale renew error=%v", err)
	}
	if err := repo.ReleaseFill(ctx, next); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryRejectsUnrelatedFenceOnPublish(t *testing.T) {
	p := cacheTestDB(t)
	repo := New(p)
	ctx := context.Background()
	key := cacheTestKey()
	other := key
	other.CanonicalQueryDigest = cacheTestDigest('9')
	fillKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: fillKey, OwnerID: "node-a", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cacheTestPublish(t, repo, ctx, PublishInput{Key: other, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/object", ByteSize: 1, Lease: lease})
	if !errors.Is(err, ErrStaleFence) {
		t.Fatalf("unrelated fence publish error=%v, want ErrStaleFence", err)
	}
	if err := repo.ReleaseFill(ctx, lease); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryPublishReplayIsIdempotentAndChangedContentsConflict(t *testing.T) {
	p := cacheTestDB(t)
	repo := New(p)
	ctx := context.Background()
	key := cacheTestKey()
	fillKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: fillKey, OwnerID: "node-replay", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	in := PublishInput{Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/object", ByteSize: 7, Metadata: []byte(`{"rows":1}`), Lease: lease}
	first, err := cacheTestPublish(t, repo, ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := cacheTestPublish(t, repo, ctx, in)
	if err != nil {
		t.Fatalf("lost-ACK replay: %v", err)
	}
	if replay.ManifestID != first.ManifestID {
		t.Fatalf("replay manifest = %s, want %s", replay.ManifestID, first.ManifestID)
	}
	changed := in
	changed.ObjectKey = "cache/other-object"
	if _, err := cacheTestPublish(t, repo, ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay error = %v, want conflict", err)
	}
	if _, err := cacheTestPublish(t, repo, ctx, PublishInput{Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/object", ByteSize: 7, Metadata: []byte(`{"rows":1,"rows":2}`), Lease: lease}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate metadata error = %v, want invalid", err)
	}
}

func TestRepositoryRetentionLifecycleAndManifestImmutability(t *testing.T) {
	p := cacheTestDB(t)
	repo := New(p)
	ctx := context.Background()
	key := cacheTestKey()
	fillKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := repo.AcquireFill(ctx, AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: fillKey, OwnerID: "node-retention", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := cacheTestPublish(t, repo, ctx, PublishInput{Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/object", ByteSize: 7, Lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddRetentionRoot(ctx, rootID, manifest.ManifestID, "dashboard"); err != nil {
		t.Fatal(err)
	}
	if err := repo.RetireRetentionRoot(ctx, rootID, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing root retirement evidence = %v, want invalid", err)
	}
	if err := repo.RetireRetentionRoot(ctx, rootID, json.RawMessage(strings.Repeat("x", maxEvidenceBytes+1))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized root retirement evidence = %v, want invalid", err)
	}
	if err := repo.AddRetentionRoot(ctx, rootID, manifest.ManifestID, "dashboard"); err != nil {
		t.Fatalf("idempotent root replay: %v", err)
	}
	if err := repo.AddRetentionRoot(ctx, rootID, manifest.ManifestID, "other"); !errors.Is(err, ErrConflict) {
		t.Fatalf("root identity conflict: %v", err)
	}
	if err := repo.RetireRetentionRoot(ctx, rootID, cacheTestEvidence("dashboard-closed")); err != nil {
		t.Fatal(err)
	}
	var retireEvidence []byte
	if err := p.QueryRow(ctx, `SELECT retire_evidence FROM cache.cache_retention_root WHERE root_id=$1`, rootID).Scan(&retireEvidence); err != nil {
		t.Fatal(err)
	}
	normalizedRootEvidence, err := lifecycleEvidence(retireEvidence)
	if err != nil || !bytes.Equal(normalizedRootEvidence, cacheTestEvidence("dashboard-closed")) {
		t.Fatalf("persisted root retirement evidence = %s", retireEvidence)
	}
	if err := repo.RetireRetentionRoot(ctx, rootID, cacheTestEvidence("different")); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched root retirement replay = %v, want conflict", err)
	}
	if err := repo.RetireRetentionRoot(ctx, rootID, cacheTestEvidence("dashboard-closed")); err != nil {
		t.Fatalf("idempotent root retirement replay: %v", err)
	}
	retentionInvalidation := NamespaceInvalidationInput{Namespace: cacheTestNamespace(), Kind: DependencyCustom, DependencyID: "orders", DependencyDigest: key.DependencyDigest, IdempotencyKey: "retention-invalidate", Reason: "dependency refresh"}
	if changed, err := repo.InvalidateNamespace(ctx, retentionInvalidation, cacheTestEvidence("dependency-refresh")); err != nil || changed.RetiredManifests != 1 {
		t.Fatalf("invalidate before retention expiry = %#v, %v", changed, err)
	}
	var manifestRetireEvidence []byte
	if err := p.QueryRow(ctx, `SELECT retire_evidence FROM cache.cache_manifest WHERE manifest_id=$1`, manifest.ManifestID).Scan(&manifestRetireEvidence); err != nil {
		t.Fatal(err)
	}
	normalizedManifestRetireEvidence, err := lifecycleEvidence(manifestRetireEvidence)
	if err != nil || !bytes.Equal(normalizedManifestRetireEvidence, cacheTestEvidence("dependency-refresh")) {
		t.Fatalf("persisted manifest retirement evidence = %s", manifestRetireEvidence)
	}
	if changed, err := repo.InvalidateNamespace(ctx, retentionInvalidation, cacheTestEvidence("different")); !errors.Is(err, ErrConflict) || changed.RetiredManifests != 0 {
		t.Fatalf("mismatched manifest retirement replay = %#v, %v", changed, err)
	}
	if err := repo.ExpireManifest(ctx, manifest.ManifestID, cacheTestEvidence("manifest-gc")); !errors.Is(err, ErrConflict) {
		t.Fatalf("manifest expired with retiring root: %v", err)
	}
	if err := repo.ExpireRetentionRoot(ctx, rootID, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing root expiry evidence = %v, want invalid", err)
	}
	if err := repo.ExpireRetentionRoot(ctx, rootID, cacheTestEvidence("root-gc")); err != nil {
		t.Fatal(err)
	}
	var rootExpireEvidence []byte
	if err := p.QueryRow(ctx, `SELECT expire_evidence FROM cache.cache_retention_root WHERE root_id=$1`, rootID).Scan(&rootExpireEvidence); err != nil {
		t.Fatal(err)
	}
	normalizedRootExpireEvidence, err := lifecycleEvidence(rootExpireEvidence)
	if err != nil || !bytes.Equal(normalizedRootExpireEvidence, cacheTestEvidence("root-gc")) {
		t.Fatalf("persisted root expiry evidence = %s", rootExpireEvidence)
	}
	if err := repo.ExpireRetentionRoot(ctx, rootID, cacheTestEvidence("different")); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched root expiry replay = %v, want conflict", err)
	}
	if err := repo.ExpireRetentionRoot(ctx, rootID, cacheTestEvidence("root-gc")); err != nil {
		t.Fatalf("idempotent root expiry replay: %v", err)
	}
	if err := repo.ExpireManifest(ctx, manifest.ManifestID, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing manifest expiry evidence = %v, want invalid", err)
	}
	if err := repo.ExpireManifest(ctx, manifest.ManifestID, cacheTestEvidence("manifest-gc")); err != nil {
		t.Fatal(err)
	}
	var expireEvidence []byte
	if err := p.QueryRow(ctx, `SELECT expire_evidence FROM cache.cache_manifest WHERE manifest_id=$1`, manifest.ManifestID).Scan(&expireEvidence); err != nil {
		t.Fatal(err)
	}
	normalizedExpireEvidence, err := lifecycleEvidence(expireEvidence)
	if err != nil || !bytes.Equal(normalizedExpireEvidence, cacheTestEvidence("manifest-gc")) {
		t.Fatalf("persisted manifest expiry evidence = %s", expireEvidence)
	}
	if err := repo.ExpireManifest(ctx, manifest.ManifestID, cacheTestEvidence("different")); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched manifest expiry replay = %v", err)
	}
	if err := repo.ExpireManifest(ctx, manifest.ManifestID, cacheTestEvidence("manifest-gc")); err != nil {
		t.Fatalf("idempotent manifest expiry replay: %v", err)
	}
	if changed, err := repo.InvalidateNamespace(ctx, retentionInvalidation, cacheTestEvidence("different")); !errors.Is(err, ErrConflict) || changed.RetiredManifests != 0 {
		t.Fatalf("mismatched invalidation after expiry = %#v, %v", changed, err)
	}
	if _, err := p.Exec(ctx, `UPDATE cache.cache_manifest SET object_key='cache/mutated' WHERE manifest_id=$1`, manifest.ManifestID); err == nil {
		t.Fatal("direct manifest object mutation succeeded")
	}
	if _, err := p.Exec(ctx, `DELETE FROM cache.cache_manifest WHERE manifest_id=$1`, manifest.ManifestID); err == nil {
		t.Fatal("direct manifest deletion succeeded")
	}
}

// TestRetentionRootManifestExpiryRaceLockOrdering exercises the two lock
// orderings that matter for retention safety. Both transactions lock the
// manifest row before inspecting or mutating roots, so a root cannot be added
// after expiry and expiry cannot overtake a root insert.
func TestRetentionRootManifestExpiryRaceLockOrdering(t *testing.T) {
	newFixture := func(t *testing.T) (*pgxpool.Pool, *Repository, Manifest, ManifestKey) {
		t.Helper()
		p := cacheTestDB(t)
		repo := New(p)
		key := cacheTestKey()
		cacheKey, err := key.CacheKeyDigest()
		if err != nil {
			t.Fatal(err)
		}
		lease, err := repo.AcquireFill(t.Context(), AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: cacheKey, OwnerID: "race-owner", Lease: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := cacheTestPublish(t, repo, t.Context(), PublishInput{Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/race", ByteSize: 1, Lease: lease})
		if err != nil {
			t.Fatal(err)
		}
		return p, repo, manifest, key
	}
	waitBlocked := func(t *testing.T, p *pgxpool.Pool, tx pgx.Tx) {
		t.Helper()
		pid := tx.Conn().PgConn().PID()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			var blocked int
			if err := p.QueryRow(t.Context(), `SELECT cardinality(pg_blocking_pids($1))`, pid).Scan(&blocked); err == nil && blocked > 0 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("transaction did not become lock-blocked")
	}

	t.Run("root lock first", func(t *testing.T) {
		p, repo, manifest, key := newFixture(t)
		defer p.Close()
		rootID, err := uuid.NewV7()
		if err != nil {
			t.Fatal(err)
		}
		rootTx, err := p.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer rootTx.Rollback(t.Context())
		var state string
		if err := rootTx.QueryRow(t.Context(), `SELECT state FROM cache.cache_manifest WHERE manifest_id=$1 FOR UPDATE`, manifest.ManifestID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != StateAdmitted {
			t.Fatalf("initial manifest state = %q", state)
		}
		if _, err := rootTx.Exec(t.Context(), `SELECT set_config('cache.capability','retention_root',true)`); err != nil {
			t.Fatal(err)
		}
		expireTx, err := p.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer expireTx.Rollback(t.Context())
		locked := make(chan error, 1)
		go func() {
			var next string
			locked <- expireTx.QueryRow(t.Context(), `SELECT state FROM cache.cache_manifest WHERE manifest_id=$1 FOR UPDATE`, manifest.ManifestID).Scan(&next)
		}()
		waitBlocked(t, p, expireTx)
		if _, err := rootTx.Exec(t.Context(), `INSERT INTO cache.cache_retention_root (root_id,manifest_id,state,reason) VALUES ($1,$2,'live','race')`, rootID, manifest.ManifestID); err != nil {
			t.Fatal(err)
		}
		if err := rootTx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := <-locked; err != nil {
			t.Fatal(err)
		}
		var roots bool
		if err := expireTx.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM cache.cache_retention_root WHERE manifest_id=$1 AND state IN ('live','retiring'))`, manifest.ManifestID).Scan(&roots); err != nil {
			t.Fatal(err)
		}
		if !roots {
			t.Fatal("expiry transaction missed committed retention root")
		}
		if err := expireTx.Rollback(t.Context()); err != nil {
			t.Fatal(err)
		}
		if changed, err := repo.InvalidateNamespace(t.Context(), NamespaceInvalidationInput{Namespace: cacheTestNamespace(), Kind: DependencyCustom, DependencyID: "orders", DependencyDigest: key.DependencyDigest, IdempotencyKey: "race-root", Reason: "dependency refresh"}, cacheTestEvidence("dependency-refresh")); err != nil || changed.RetiredManifests != 1 {
			t.Fatalf("invalidate after root race = %#v, %v", changed, err)
		}
		if err := repo.ExpireManifest(t.Context(), manifest.ManifestID, cacheTestEvidence("manifest-gc")); !errors.Is(err, ErrConflict) {
			t.Fatalf("expiry with committed root = %v, want conflict", err)
		}
	})

	t.Run("expiry lock first", func(t *testing.T) {
		p, repo, manifest, key := newFixture(t)
		defer p.Close()
		if changed, err := repo.InvalidateNamespace(t.Context(), NamespaceInvalidationInput{Namespace: cacheTestNamespace(), Kind: DependencyCustom, DependencyID: "orders", DependencyDigest: key.DependencyDigest, IdempotencyKey: "race-expiry", Reason: "dependency refresh"}, cacheTestEvidence("dependency-refresh")); err != nil || changed.RetiredManifests != 1 {
			t.Fatalf("initial invalidate = %#v, %v", changed, err)
		}
		expireTx, err := p.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer expireTx.Rollback(t.Context())
		var state string
		if err := expireTx.QueryRow(t.Context(), `SELECT state FROM cache.cache_manifest WHERE manifest_id=$1 FOR UPDATE`, manifest.ManifestID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != StateRetiring {
			t.Fatalf("retiring manifest state = %q", state)
		}
		rootID, err := uuid.NewV7()
		if err != nil {
			t.Fatal(err)
		}
		rootTx, err := p.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer rootTx.Rollback(t.Context())
		locked := make(chan error, 1)
		go func() {
			var next string
			locked <- rootTx.QueryRow(t.Context(), `SELECT state FROM cache.cache_manifest WHERE manifest_id=$1 FOR UPDATE`, manifest.ManifestID).Scan(&next)
		}()
		waitBlocked(t, p, rootTx)
		if _, err := expireTx.Exec(t.Context(), `UPDATE cache.cache_manifest SET state='expired', expired_at=clock_timestamp(), expire_evidence=$2::jsonb WHERE manifest_id=$1 AND state='retiring'`, manifest.ManifestID, cacheTestEvidence("race-expire")); err != nil {
			t.Fatal(err)
		}
		if err := expireTx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := <-locked; err != nil {
			t.Fatal(err)
		}
		var next string
		if err := rootTx.QueryRow(t.Context(), `SELECT state FROM cache.cache_manifest WHERE manifest_id=$1`, manifest.ManifestID).Scan(&next); err != nil {
			t.Fatal(err)
		}
		if next != StateExpired {
			t.Fatalf("root transaction observed state = %q, want expired", next)
		}
		if err := rootTx.Rollback(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := repo.AddRetentionRoot(t.Context(), rootID, manifest.ManifestID, "race"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("root after expiry = %v, want not found", err)
		}
	})
}

func TestNamespaceRevisionEpochInvalidationAndReconciliation(t *testing.T) {
	p := cacheTestDB(t)
	defer p.Close()
	repo := New(p)
	ns := Namespace{PartitionKind: PartitionProduction, TargetID: "target_sales", ProjectID: "project_sales", Environment: "prod"}
	if got, err := repo.CurrentEpoch(t.Context(), ns); err != nil || got != 1 {
		t.Fatalf("initial epoch = %d, %v; want 1", got, err)
	}
	digestA, digestB := cacheTestDigest('a'), cacheTestDigest('b')
	first, err := repo.RecordDependencyRevision(t.Context(), DependencyRevisionInput{Namespace: ns, Kind: DependencySource, DependencyID: "orders", RevisionDigest: digestA})
	if err != nil || first.Revision != 1 {
		t.Fatalf("initial dependency revision = %#v, %v", first, err)
	}
	initialReplay, err := repo.RecordDependencyRevision(t.Context(), DependencyRevisionInput{Namespace: ns, Kind: DependencySource, DependencyID: "orders", RevisionDigest: digestA, ExpectedRevision: 0})
	if err != nil || initialReplay.Revision != first.Revision {
		t.Fatalf("initial dependency revision replay = %#v, %v", initialReplay, err)
	}
	if _, err := repo.RecordDependencyRevision(t.Context(), DependencyRevisionInput{Namespace: ns, Kind: DependencySource, DependencyID: "orders", RevisionDigest: digestA, ExpectedRevision: 99}); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-digest wrong expected revision = %v, want conflict", err)
	}
	key := cacheTestKey()
	cacheKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	fence, err := repo.AcquireFill(t.Context(), AcquireFillInput{Namespace: ns, CacheKey: cacheKey, OwnerID: "revision-owner", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cacheTestPublish(t, repo, t.Context(), PublishInput{Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/revision", ByteSize: 1, Lease: fence}); err != nil {
		t.Fatal(err)
	}
	second, err := repo.RecordDependencyRevision(t.Context(), DependencyRevisionInput{Namespace: ns, Kind: DependencySource, DependencyID: "orders", RevisionDigest: digestB, ExpectedRevision: 1, Evidence: cacheTestEvidence("source-refresh")})
	if err != nil || second.Revision != 2 || second.RevisionDigest != digestB {
		t.Fatalf("changed dependency revision = %#v, %v", second, err)
	}
	replay, err := repo.RecordDependencyRevision(t.Context(), DependencyRevisionInput{Namespace: ns, Kind: DependencySource, DependencyID: "orders", RevisionDigest: digestB, ExpectedRevision: 1, Evidence: cacheTestEvidence("source-refresh")})
	if err != nil || replay.Revision != second.Revision || replay.RevisionDigest != second.RevisionDigest {
		t.Fatalf("dependency revision lost-ACK replay = %#v, %v", replay, err)
	}
	if _, err := repo.RecordDependencyRevision(t.Context(), DependencyRevisionInput{Namespace: ns, Kind: DependencySource, DependencyID: "orders", RevisionDigest: digestB, ExpectedRevision: 1, IdempotencyKey: "different-retry", Evidence: cacheTestEvidence("source-refresh")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("different dependency revision retry = %v, want conflict", err)
	}
	if got, err := repo.CurrentEpoch(t.Context(), ns); err != nil || got != 2 {
		t.Fatalf("changed epoch = %d, %v; want 2", got, err)
	}
	if _, found, err := repo.Lookup(t.Context(), key); err != nil || found {
		t.Fatalf("old dependency manifest found=%v err=%v", found, err)
	}
	events, err := repo.ReconcileInvalidations(t.Context(), ReconcileOptions{AfterEventID: 0, Limit: 10})
	if err != nil || len(events) != 1 || events[0].NamespaceEpoch != 2 || events[0].DependencyID != "orders" {
		t.Fatalf("reconciled invalidations = %#v, %v", events, err)
	}
	if _, err := repo.RecordDependencyRevision(t.Context(), DependencyRevisionInput{Namespace: ns, Kind: DependencySource, DependencyID: "orders", RevisionDigest: cacheTestDigest('c'), ExpectedRevision: 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision update = %v, want conflict", err)
	}
	staleKey := cacheTestKey()
	staleKey.CanonicalQueryDigest = cacheTestDigest('9')
	staleDigest, err := staleKey.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	stale, err := repo.AcquireFill(t.Context(), AcquireFillInput{Namespace: ns, CacheKey: staleDigest, OwnerID: "stale-owner", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InvalidateNamespace(t.Context(), NamespaceInvalidationInput{Namespace: ns, Kind: DependencyCustom, DependencyID: "stale-fence", IdempotencyKey: "stale-fence-epoch", Reason: "stale fence"}, cacheTestEvidence("stale-fence")); err != nil {
		t.Fatal(err)
	}
	if _, err := cacheTestPublish(t, repo, t.Context(), PublishInput{Key: staleKey, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/stale", ByteSize: 1, Lease: stale}); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale namespace publish = %v, want stale fence", err)
	}
	hint, err := ParseNotificationHint(`{"event_id":1,"namespace":"` + ns.Key() + `"}`)
	if err != nil || hint.EventID != 1 || hint.NamespaceKey != ns.Key() {
		t.Fatalf("notification hint = %#v, %v", hint, err)
	}
}

func TestL3ObjectAndScanFencesAreDurableAndFenced(t *testing.T) {
	db := cacheTestDB(t)
	repo := New(db)
	maintenance := NewMaintenance(db)
	domain := cacheTestDigest('d')
	objectKey := "cache/l3/sd/" + domain + "/" + cacheTestDigest('a') + "/" + cacheTestDigest('b')

	first, err := repo.AcquireL3ObjectFence(t.Context(), AcquireL3ObjectFenceInput{StorageSecurityDomain: domain, ObjectKey: objectKey, OwnerID: "runtime-a", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.AcquireL3ObjectFence(t.Context(), AcquireL3ObjectFenceInput{StorageSecurityDomain: domain, ObjectKey: objectKey, OwnerID: "gc-a", Lease: time.Minute}); !errors.Is(err, ErrBusy) {
		t.Fatalf("maintenance acquired live runtime fence: %v", err)
	}
	if err := repo.ReleaseL3ObjectFence(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	second, err := maintenance.AcquireL3ObjectFence(t.Context(), AcquireL3ObjectFenceInput{StorageSecurityDomain: domain, ObjectKey: objectKey, OwnerID: "gc-a", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if second.FencingEpoch <= first.FencingEpoch {
		t.Fatalf("object fencing epoch did not advance: first=%d second=%d", first.FencingEpoch, second.FencingEpoch)
	}
	if err := repo.RenewL3ObjectFence(t.Context(), first, time.Minute); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale object fence renewed: %v", err)
	}
	if err := maintenance.ReleaseL3ObjectFence(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.PrepareL3ObjectGC(t.Context(), second); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("GC preparation with released object fence error = %v, want stale fence", err)
	}

	lease, err := maintenance.AcquireL3GCLease(t.Context(), domain, "node-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if lease.CursorObjectKey != "" || lease.Cycle != 1 {
		t.Fatalf("initial GC lease = %+v", lease)
	}
	if _, err := maintenance.AcquireL3GCLease(t.Context(), domain, "node-b", time.Minute); !errors.Is(err, ErrBusy) {
		t.Fatalf("second GC owner acquired live lease: %v", err)
	}
	if err := maintenance.AdvanceL3GCCursor(t.Context(), lease, objectKey, false); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.AdvanceL3GCCursor(t.Context(), lease, objectKey, false); err != nil {
		t.Fatalf("idempotent GC cursor replay failed: %v", err)
	}
	if err := maintenance.AdvanceL3GCCursor(t.Context(), lease, objectKey[:len(objectKey)-1], false); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("regressing GC cursor error = %v, want stale fence", err)
	}
	if err := maintenance.AdvanceL3GCCursor(t.Context(), lease, "", true); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.ReleaseL3GCLease(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	next, err := maintenance.AcquireL3GCLease(t.Context(), domain, "node-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if next.CursorObjectKey != "" || next.Cycle != 2 || next.FencingEpoch <= lease.FencingEpoch {
		t.Fatalf("resumed GC lease = %+v", next)
	}
}

func TestPruneReclaimsExpiredL3ObjectFences(t *testing.T) {
	db := cacheTestDB(t)
	maintenance := NewMaintenance(db)
	domain := cacheTestDigest('d')
	objectKey := "cache/l3/prune/" + cacheTestDigest('a')
	fence, err := maintenance.AcquireL3ObjectFence(t.Context(), AcquireL3ObjectFenceInput{
		StorageSecurityDomain: domain,
		ObjectKey:             objectKey,
		OwnerID:               "prune-owner",
		Lease:                 time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.ReleaseL3ObjectFence(t.Context(), fence); err != nil {
		t.Fatal(err)
	}
	stats, err := maintenance.Prune(t.Context(), PruneOptions{Before: time.Now().Add(time.Minute), Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if stats.ExpiredLeases != 1 {
		t.Fatalf("pruned expired L3 object fences = %d, want 1", stats.ExpiredLeases)
	}
}

func TestManifestAdmissionRequiresCurrentExactObjectFence(t *testing.T) {
	db := cacheTestDB(t)
	repo := New(db)
	key := cacheTestKey()
	cacheKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := repo.AcquireFill(t.Context(), AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: cacheKey, OwnerID: "publisher", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	objectKey := "cache/l3/sd/" + cacheTestDigest('d') + "/" + cacheTestDigest('a') + "/" + cacheTestDigest('b')
	fence, err := repo.AcquireL3ObjectFence(t.Context(), AcquireL3ObjectFenceInput{StorageSecurityDomain: cacheTestDigest('d'), ObjectKey: objectKey, OwnerID: lease.OwnerID, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ReleaseL3ObjectFence(t.Context(), fence); err != nil {
		t.Fatal(err)
	}
	_, err = repo.Publish(t.Context(), PublishInput{Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: objectKey, ByteSize: 1, Lease: lease, ObjectFence: fence})
	if !errors.Is(err, ErrStaleFence) {
		t.Fatalf("publish with released object fence error = %v, want stale fence", err)
	}
}

func TestCacheRoleConformance(t *testing.T) {
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "runtime-secret", Login: true})
	maintenanceRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: "maintenance-secret", Login: true})
	readonlyRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Password: "readonly-secret", Login: true})
	database := h.NewDatabase(t, "cache_roles")
	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	tx, err := admin.Begin(t.Context())
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
	// Simulate a developer database prepared with the pre-object-fence
	// overload. Reapplying the clean-slate schema must remove both the function
	// and its role-specific grant rather than leaving a callable bypass.
	const staleAdmissionSignature = `cache.admit_manifest(uuid,uuid,text,text,bigint,text,bigint,text,text,text,text,text,bigint,text,text,text,bigint,text,text,text,bigint,jsonb,uuid,timestamptz)`
	if _, err := admin.Exec(t.Context(), `CREATE FUNCTION `+staleAdmissionSignature+` RETURNS uuid LANGUAGE sql AS 'SELECT NULL::uuid'; GRANT EXECUTE ON FUNCTION `+staleAdmissionSignature+` TO leapview_control_runtime`); err != nil {
		t.Fatal(err)
	}
	tx, err = admin.Begin(t.Context())
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
	var staleAdmissionExists bool
	if err := admin.QueryRow(t.Context(), `SELECT to_regprocedure($1) IS NOT NULL`, staleAdmissionSignature).Scan(&staleAdmissionExists); err != nil {
		t.Fatal(err)
	}
	if staleAdmissionExists {
		t.Fatal("pre-object-fence manifest admission overload survived schema reapplication")
	}
	var runtimeSchema, runtimeDelete, runtimePrune, runtimeInvalidate, runtimeRetire, runtimeObjectFence, runtimeGCLease, readonlyInsert, maintenancePrune, maintenanceObjectFence, maintenanceGCLease, maintenanceReachability, maintenanceStateDML bool
	if err := admin.QueryRow(t.Context(), `SELECT has_schema_privilege($1,'cache','USAGE'),has_table_privilege($1,'cache.cache_invalidation','DELETE'),has_function_privilege($1,'cache.prune_coordination(timestamptz,integer)','EXECUTE'),has_function_privilege($1,'cache.invalidate_namespace(uuid,text,text,text,text,bigint,text,text,jsonb)','EXECUTE'),has_function_privilege($1,'cache.retire_manifest(uuid,jsonb)','EXECUTE'),has_function_privilege($1,'cache.acquire_l3_object_fence(uuid,text,text,text,interval)','EXECUTE'),has_function_privilege($1,'cache.acquire_l3_gc_lease(uuid,text,text,interval)','EXECUTE'),has_table_privilege($2,'cache.cache_namespace_epoch','INSERT'),has_function_privilege($3,'cache.prune_coordination(timestamptz,integer)','EXECUTE'),has_function_privilege($3,'cache.acquire_l3_object_fence(uuid,text,text,text,interval)','EXECUTE'),has_function_privilege($3,'cache.acquire_l3_gc_lease(uuid,text,text,interval)','EXECUTE'),has_function_privilege($3,'cache.prepare_l3_object_gc(uuid,text,text,text,bigint)','EXECUTE'),has_table_privilege($3,'cache.cache_l3_gc_state','UPDATE')`, runtimeRole.Name, readonlyRole.Name, maintenanceRole.Name).Scan(&runtimeSchema, &runtimeDelete, &runtimePrune, &runtimeInvalidate, &runtimeRetire, &runtimeObjectFence, &runtimeGCLease, &readonlyInsert, &maintenancePrune, &maintenanceObjectFence, &maintenanceGCLease, &maintenanceReachability, &maintenanceStateDML); err != nil {
		t.Fatal(err)
	}
	if !runtimeSchema || runtimeDelete || runtimePrune || !runtimeInvalidate || !runtimeRetire || !runtimeObjectFence || runtimeGCLease || readonlyInsert || !maintenancePrune || !maintenanceObjectFence || !maintenanceGCLease || !maintenanceReachability || maintenanceStateDML {
		t.Fatalf("cache role grants schema=%v runtime_delete=%v runtime_prune=%v runtime_invalidate=%v runtime_retire=%v runtime_object_fence=%v runtime_gc=%v maintenance_prune=%v maintenance_object_fence=%v maintenance_gc=%v maintenance_reachability=%v maintenance_state_dml=%v readonly_insert=%v", runtimeSchema, runtimeDelete, runtimePrune, runtimeInvalidate, runtimeRetire, runtimeObjectFence, runtimeGCLease, maintenancePrune, maintenanceObjectFence, maintenanceGCLease, maintenanceReachability, maintenanceStateDML, readonlyInsert)
	}
	runtime, err := pgxpool.New(t.Context(), database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if _, err := runtime.Exec(t.Context(), `DELETE FROM cache.cache_invalidation`); err == nil {
		t.Fatal("runtime DELETE on durable invalidation history succeeded")
	}
	if _, err := runtime.Exec(t.Context(), `UPDATE cache.cache_invalidation SET reason='forged'`); err == nil {
		t.Fatal("runtime UPDATE on durable invalidation history succeeded")
	}
	if _, err := runtime.Exec(t.Context(), `SELECT * FROM cache.prune_coordination(clock_timestamp(),1)`); err == nil {
		t.Fatal("runtime prune capability unexpectedly granted")
	}
	if _, err := runtime.Exec(t.Context(), `SELECT cache.prepare_l3_object_gc($1,$2,'cache/object','runtime',1)`, uuid.New(), cacheTestDigest('d')); err == nil {
		t.Fatal("runtime L3 object-GC preparation capability unexpectedly granted")
	}
	if _, err := runtime.Exec(t.Context(), `SELECT * FROM cache.acquire_l3_object_fence($1,$2,$3,'runtime',interval '1 minute')`, uuid.New(), cacheTestDigest('d'), "cache/l3/sd/"+cacheTestDigest('e')+"/"+cacheTestDigest('a')+"/"+cacheTestDigest('b')); err == nil {
		t.Fatal("runtime acquired an L3 object fence outside its storage-security-domain path")
	}
	if _, err := runtime.Exec(t.Context(), `INSERT INTO cache.cache_namespace_epoch(namespace_key,partition_kind,target_id,project_id,environment,epoch) VALUES ('forged','production','target_sales','project_sales','prod',1)`); err == nil {
		t.Fatal("runtime direct namespace fabrication succeeded")
	}
	if _, err := runtime.Exec(t.Context(), `SELECT cache.advance_dependency_revision('forged','custom','orders',$1,0,NULL::jsonb,NULL)`, cacheTestDigest('a')); err == nil {
		t.Fatal("runtime intermediate dependency revision capability unexpectedly granted")
	}
	runtimeRepo := New(runtime)
	if _, err := runtimeRepo.InvalidateNamespace(t.Context(), NamespaceInvalidationInput{Namespace: Namespace{PartitionKind: PartitionProduction, TargetID: "runtime_target", ProjectID: "runtime_project", Environment: "prod"}, Kind: DependencyCustom, DependencyID: "runtime", IdempotencyKey: "runtime-invalidate", Reason: "runtime test"}, cacheTestEvidence("runtime")); err != nil {
		t.Fatalf("runtime invalidation capability failed: %v", err)
	}
	if _, err := admin.Exec(t.Context(), `INSERT INTO cache.cache_namespace_epoch(namespace_key,partition_kind,target_id,project_id,environment,epoch) VALUES ('forged','production','target_sales','project_sales','prod',1)`); err == nil {
		t.Fatal("forged namespace identity unexpectedly accepted by database guard")
	}
	maintenance, err := pgxpool.New(t.Context(), database.URL(maintenanceRole))
	if err != nil {
		t.Fatal(err)
	}
	defer maintenance.Close()
	if _, err := maintenance.Exec(t.Context(), `SELECT * FROM cache.prune_coordination(clock_timestamp(),1)`); err != nil {
		t.Fatalf("maintenance bounded prune function failed: %v", err)
	}
}
