package l3

import (
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	analyticscache "github.com/flidai/leapview/internal/analytics/cache"
	cachepostgres "github.com/flidai/leapview/internal/analytics/cache/postgres"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/pkg/arrowresult"
)

func TestResultTierStoreLookupRoundTrip(t *testing.T) {
	cache, authority, _, _, _ := newTestCache(t)
	tier := NewResultTier(cache)
	key := resultTierTestKey(t)
	result := resultTierTestResult(t, "round-trip")
	defer result.Release()

	metadata := resultcache.Metadata{SQL: "select secret", TotalRows: 1, TotalRowsKnown: true, Warnings: []string{"slow"}}
	if err := tier.Store(t.Context(), key, result, metadata); err != nil {
		t.Fatalf("store: %v", err)
	}
	if len(authority.publishInputs) != 1 {
		t.Fatalf("publish calls = %d, want 1", len(authority.publishInputs))
	}
	if strings.Contains(string(authority.publishInputs[0].Metadata), "SQL") {
		t.Fatalf("request SQL leaked into durable metadata: %s", authority.publishInputs[0].Metadata)
	}

	decoded, gotMetadata, _, found, err := tier.Lookup(t.Context(), key)
	if err != nil || !found {
		t.Fatalf("lookup found=%v err=%v", found, err)
	}
	if decoded == nil || decoded.Rows() != 1 {
		t.Fatalf("decoded result = %#v", decoded)
	}
	if gotMetadata.SQL != "" || gotMetadata.TotalRows != metadata.TotalRows || !gotMetadata.TotalRowsKnown || len(gotMetadata.Warnings) != 1 || gotMetadata.Warnings[0] != "slow" {
		t.Fatalf("decoded metadata = %+v", gotMetadata)
	}
	decoded.Release()
}

func TestResultTierLookupMissAndCorruptObject(t *testing.T) {
	cache, authority, store, _, _ := newTestCache(t)
	tier := NewResultTier(cache)
	key := resultTierTestKey(t)

	if decoded, metadata, _, found, err := tier.Lookup(t.Context(), key); err != nil || found || decoded != nil || metadata.SQL != "" || metadata.TotalRows != 0 || metadata.TotalRowsKnown || len(metadata.Warnings) != 0 {
		t.Fatalf("cold lookup result=%#v metadata=%+v found=%v err=%v", decoded, metadata, found, err)
	}
	result := resultTierTestResult(t, "corrupt")
	defer result.Release()
	if err := tier.Store(t.Context(), key, result, resultcache.Metadata{}); err != nil {
		t.Fatalf("store: %v", err)
	}
	store.corrupt = true
	decoded, _, _, found, err := tier.Lookup(t.Context(), key)
	if err != nil || found || decoded != nil {
		t.Fatalf("corrupt lookup result=%#v found=%v err=%v", decoded, found, err)
	}
	if authority.retireCalls != 1 {
		t.Fatalf("corrupt lookup retirements = %d, want 1", authority.retireCalls)
	}
}

func TestResultTierStoreBorrowsResult(t *testing.T) {
	cache, _, _, _, _ := newTestCache(t)
	tier := NewResultTier(cache)
	key := resultTierTestKey(t)
	result := resultTierTestResult(t, "borrowed")
	if err := tier.Store(t.Context(), key, result, resultcache.Metadata{}); err != nil {
		t.Fatalf("store: %v", err)
	}
	// Store must not consume the caller's reference.
	lease, err := result.Acquire()
	if err != nil {
		t.Fatalf("result was released by Store: %v", err)
	}
	lease.Release()
	result.Release()
}

func TestResultTierRejectsInconsistentMetadataBeforeFill(t *testing.T) {
	cache, authority, _, _, _ := newTestCache(t)
	tier := NewResultTier(cache)
	result := resultTierTestResult(t, "metadata")
	defer result.Release()
	if err := tier.Store(t.Context(), resultTierTestKey(t), result, resultcache.Metadata{TotalRowsKnown: true}); err == nil {
		t.Fatal("store accepted known total smaller than Arrow row count")
	}
	if len(authority.publishInputs) != 0 {
		t.Fatalf("invalid metadata published %d manifests", len(authority.publishInputs))
	}
}

func TestResultTierRejectsUnknownTotalMetadataAndRetiresManifest(t *testing.T) {
	cache, authority, store, _, _ := newTestCache(t)
	tier := NewResultTier(cache)
	key := resultTierTestKey(t)
	result := resultTierTestResult(t, "unknown-total")
	defer result.Release()
	if err := tier.Store(t.Context(), key, result, resultcache.Metadata{}); err != nil {
		t.Fatalf("store: %v", err)
	}

	// Keep the object and manifest metadata internally consistent so L3's
	// object verification succeeds; the result-tier metadata invariant must
	// then retire the exact manifest when totalRows is unknown but non-zero.
	badMetadata := encodeResultTierMetadata(resultcache.Metadata{TotalRows: 1})
	authority.manifest.Metadata = append([]byte(nil), badMetadata...)
	store.objects[authority.manifest.ObjectKey] = memoryObject{
		info: func() ObjectInfo {
			info := store.objects[authority.manifest.ObjectKey].info
			info.Metadata = append([]byte(nil), badMetadata...)
			info.MetadataDigest = digestBytes(badMetadata)
			return info
		}(),
		body: append([]byte(nil), store.objects[authority.manifest.ObjectKey].body...),
	}
	decoded, _, _, found, err := tier.Lookup(t.Context(), key)
	if err != nil || found || decoded != nil {
		t.Fatalf("unknown-total lookup result=%#v found=%v err=%v", decoded, found, err)
	}
	if authority.retireCalls != 1 {
		t.Fatalf("unknown-total retirement count = %d, want 1", authority.retireCalls)
	}
}

func TestResultTierRejectsSemanticallyCorruptArrowAndRetiresManifest(t *testing.T) {
	cache, authority, store, _, _ := newTestCache(t)
	tier := NewResultTier(cache)
	key := resultTierTestKey(t)
	manifestKey, err := cachepostgres.ManifestKeyFromIdentity(key)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := cache.AcquireFill(t.Context(), manifestKey, "semantic-corrupt", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Publish(t.Context(), PublishInput{Key: manifestKey, Lease: lease, Body: strings.NewReader("not Arrow IPC"), Metadata: encodeResultTierMetadata(resultcache.Metadata{})}); err != nil {
		t.Fatal(err)
	}
	decoded, _, _, found, err := tier.Lookup(t.Context(), key)
	if err != nil || found || decoded != nil {
		t.Fatalf("semantic-corrupt lookup result=%#v found=%v err=%v", decoded, found, err)
	}
	if authority.retireCalls != 1 {
		t.Fatalf("semantic-corrupt retirement count = %d, want 1", authority.retireCalls)
	}
	if len(store.objects) != 1 {
		t.Fatalf("semantic-corrupt object unexpectedly removed: %d", len(store.objects))
	}
}

func TestResultTierRejectsMalformedMetadataAndRetiresManifest(t *testing.T) {
	cache, authority, store, _, _ := newTestCache(t)
	tier := NewResultTier(cache)
	key := resultTierTestKey(t)
	result := resultTierTestResult(t, "metadata-corrupt")
	defer result.Release()
	if err := tier.Store(t.Context(), key, result, resultcache.Metadata{}); err != nil {
		t.Fatalf("store: %v", err)
	}
	// Keep the object and manifest metadata internally consistent so L3's
	// object verification succeeds; the result-tier schema check must then
	// retire the exact manifest when it sees an unknown metadata field.
	badMetadata := []byte(`{"version":1,"unknown":true}`)
	authority.manifest.Metadata = append([]byte(nil), badMetadata...)
	store.objects[authority.manifest.ObjectKey] = memoryObject{
		info: func() ObjectInfo {
			info := store.objects[authority.manifest.ObjectKey].info
			info.Metadata = append([]byte(nil), badMetadata...)
			info.MetadataDigest = digestBytes(badMetadata)
			return info
		}(),
		body: append([]byte(nil), store.objects[authority.manifest.ObjectKey].body...),
	}
	decoded, _, _, found, err := tier.Lookup(t.Context(), key)
	if err != nil || found || decoded != nil {
		t.Fatalf("malformed-metadata lookup result=%#v found=%v err=%v", decoded, found, err)
	}
	if authority.retireCalls != 1 {
		t.Fatalf("malformed-metadata retirement count = %d, want 1", authority.retireCalls)
	}
}

func resultTierTestKey(t *testing.T) analyticscache.Key {
	t.Helper()
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{Kind: resultidentity.PartitionProduction, TargetID: "target", ProjectID: "project", Environment: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := analyticscache.NewKeyFromDigests(partition, testDigest('a'), testDigest('b'), testDigest('c'))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func resultTierTestResult(t *testing.T, value string) *arrowresult.Result {
	t.Helper()
	allocator := memory.DefaultAllocator
	builder := array.NewStringBuilder(allocator)
	builder.Append(value)
	values := builder.NewArray()
	builder.Release()
	record := array.NewRecordBatch(arrow.NewSchema([]arrow.Field{{Name: "value", Type: arrow.BinaryTypes.String}}, nil), []arrow.Array{values}, 1)
	values.Release()
	collector := arrowresult.NewBuilderWithAllocator(allocator)
	if err := collector.WriteSchema(record.Schema()); err != nil {
		record.Release()
		t.Fatal(err)
	}
	if err := collector.WriteRecord(record); err != nil {
		record.Release()
		t.Fatal(err)
	}
	record.Release()
	result, err := collector.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return result
}
