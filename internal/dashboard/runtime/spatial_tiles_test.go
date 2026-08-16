package runtime

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard"
)

func TestSpatialTileRevisionTokensAreRandomAndScopeBound(t *testing.T) {
	registry := newSpatialTileRegistry()
	authToken, err := registry.register(spatialTileRevision{
		DashboardID: "orders", PageID: "map", VisualID: "density",
		PrincipalID: "principal-1", Filters: dashboard.Filters{ActivePageID: "map", ServingStateID: "serving-1"}, RawMinimumZoom: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	publicToken, err := registry.register(spatialTileRevision{
		DashboardID: "orders", PageID: "map", VisualID: "density", PublicID: "published-orders", PrincipalID: "publication-subject",
	})
	if err != nil {
		t.Fatal(err)
	}
	if authToken == publicToken || len(authToken) != 32 || strings.ContainsAny(authToken, "+/=") {
		t.Fatalf("tokens are not independent raw URL-safe 192-bit values: %q %q", authToken, publicToken)
	}
	replacement, err := registry.register(spatialTileRevision{DashboardID: "orders", PageID: "map", VisualID: "density", PrincipalID: "principal-1", Filters: dashboard.Filters{ActivePageID: "map", ServingStateID: "serving-1"}, RawMinimumZoom: 9})
	if err != nil {
		t.Fatal(err)
	}
	if replacement == authToken || registry.entries[authToken].ExpiresAt.IsZero() {
		t.Fatal("replaced revision did not receive an in-flight grace period")
	}
	expired := registry.entries[authToken]
	expired.ExpiresAt = time.Now().Add(-time.Second)
	registry.entries[authToken] = expired
	if _, err := registry.resolve(authToken, "orders", "density", "", "principal-1"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired revision error = %v", err)
	}
	authToken = replacement
	resolved, err := registry.resolve(authToken, "orders", "density", "", "principal-1")
	if err != nil || resolved.PageID != "map" || resolved.Filters.ServingStateID != "serving-1" || resolved.RawMinimumZoom != 9 {
		t.Fatalf("resolved authenticated revision = %#v, %v", resolved, err)
	}
	for _, request := range []struct {
		token, dashboard, visual, publicID, principalID string
	}{
		{authToken, "other", "density", "", "principal-1"},
		{authToken, "orders", "other", "", "principal-1"},
		{authToken, "orders", "density", "published-orders", "principal-1"},
		{authToken, "orders", "density", "", "principal-2"},
		{publicToken, "orders", "density", "", "publication-subject"},
		{"unknown", "orders", "density", "", "principal-1"},
	} {
		if _, err := registry.resolve(request.token, request.dashboard, request.visual, request.publicID, request.principalID); err == nil {
			t.Fatalf("cross-scope revision unexpectedly resolved: %#v", request)
		}
	}
}

func TestSpatialTileRevisionLifecycleIsBoundToItsStream(t *testing.T) {
	registry := newSpatialTileRegistry()
	first, err := registry.register(spatialTileRevision{DashboardID: "orders", PageID: "map", VisualID: "density", PrincipalID: "principal", StreamID: "stream-a"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.register(spatialTileRevision{DashboardID: "orders", PageID: "map", VisualID: "density", PrincipalID: "principal", StreamID: "stream-b"})
	if err != nil {
		t.Fatal(err)
	}
	if !registry.entries[first].ExpiresAt.IsZero() || !registry.entries[second].ExpiresAt.IsZero() {
		t.Fatal("one stream replaced another stream's active tile capability")
	}
	registry.expireStream("stream-a")
	if registry.entries[first].ExpiresAt.IsZero() {
		t.Fatal("closed stream capability did not receive its in-flight grace period")
	}
	if !registry.entries[second].ExpiresAt.IsZero() {
		t.Fatal("closing one stream retired an unrelated stream capability")
	}
}

func TestSpatialTileURLsAreSameOriginTemplates(t *testing.T) {
	if got, want := spatialTileURL("sales team", "orders/2026", "density map", "revision"), "/projects/sales%20team/dashboards/orders%2F2026/visuals/density%20map/tiles/revision/{z}/{x}/{y}.mvt"; got != want {
		t.Fatalf("authenticated tile URL = %q, want %q", got, want)
	}
	if got, want := publicSpatialTileURL("public/orders", "density map", "revision"), "/public/dashboards/public%2Forders/visuals/density%20map/tiles/revision/{z}/{x}/{y}.mvt"; got != want {
		t.Fatalf("public tile URL = %q, want %q", got, want)
	}
}

func TestSpatialTileFromRowsAcceptsDuckDBIntegerCoordinates(t *testing.T) {
	tile, features, found, err := spatialTileFromRows([]dataquery.Row{{
		"__tile_x": int32(3), "__tile_y": int32(2), "feature_count": int64(7), "mvt": []byte{1, 2, 3},
	}}, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !found || features != 7 || !bytes.Equal(tile, []byte{1, 2, 3}) {
		t.Fatalf("tile = found %t, features %d, bytes %v", found, features, tile)
	}
}

func TestSpatialRawMetatileFitsEveryChildBudget(t *testing.T) {
	rows := []dataquery.Row{
		{"__tile_x": int32(0), "__tile_y": int32(0), "feature_count": int64(4_999), "mvt": []byte{1, 2}},
		{"__tile_x": int32(1), "__tile_y": int32(0), "feature_count": int64(12), "mvt": []byte{3}},
	}
	if !spatialRawMetatileFits(rows, 2) {
		t.Fatal("bounded raw metatile was rejected")
	}
	rows[1]["mvt"] = nil
	if spatialRawMetatileFits(rows, 2) {
		t.Fatal("feature-cap overflow in one child did not reject the metatile")
	}
	rows[1]["mvt"] = []byte{1, 2, 3}
	if spatialRawMetatileFits(rows, 2) {
		t.Fatal("byte overflow in one child did not reject the metatile")
	}
	if !spatialRawMetatileFits(nil, 2) {
		t.Fatal("empty metatile should be a valid raw result")
	}
}

func TestSpatialAggregateTargetZoomJumpsToUsefulRefinement(t *testing.T) {
	for _, test := range []struct{ zoom, rawMinimum, maximum, want int }{
		{zoom: 2, rawMinimum: 5, maximum: 18, want: 5},
		{zoom: 4, rawMinimum: 5, maximum: 18, want: 6},
		{zoom: 5, rawMinimum: 5, maximum: 18, want: 7},
		{zoom: 17, rawMinimum: 5, maximum: 18, want: 18},
	} {
		if got := spatialAggregateTargetZoom(test.zoom, test.rawMinimum, test.maximum); got != test.want {
			t.Fatalf("target zoom for z%d = %d, want %d", test.zoom, got, test.want)
		}
	}
}

func TestSpatialTilePrecisionIsRevisionWideAtEachZoom(t *testing.T) {
	for _, test := range []struct {
		zoom, rawMinimum int
		want             dataquery.SpatialTilePrecision
	}{
		{zoom: 6, rawMinimum: 8, want: dataquery.SpatialTilePrecisionAggregated},
		{zoom: 7, rawMinimum: 8, want: dataquery.SpatialTilePrecisionAggregated},
		{zoom: 8, rawMinimum: 8, want: dataquery.SpatialTilePrecisionRaw},
		{zoom: 12, rawMinimum: 8, want: dataquery.SpatialTilePrecisionRaw},
	} {
		if got := spatialTilePrecision(test.zoom, test.rawMinimum); got != test.want {
			t.Fatalf("precision at z%d with raw transition z%d = %q, want %q", test.zoom, test.rawMinimum, got, test.want)
		}
	}
}

func TestSpatialRawZoomRequiresBothGlobalBudgets(t *testing.T) {
	if !spatialRawZoomFits(5_000, 512*1024, 5_000, 512*1024) {
		t.Fatal("raw zoom rejected exact feature and byte budgets")
	}
	if spatialRawZoomFits(5_001, 1, 5_000, 512*1024) {
		t.Fatal("raw zoom accepted feature overflow")
	}
	if spatialRawZoomFits(1, 512*1024+1, 5_000, 512*1024) {
		t.Fatal("raw zoom accepted encoded-byte overflow")
	}
}

type memoryImmutableByteCache struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (cache *memoryImmutableByteCache) LookupImmutableBytes(key string) ([]byte, bool, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	value, ok := cache.values[key]
	return append([]byte(nil), value...), ok, nil
}

func (cache *memoryImmutableByteCache) StoreImmutableBytes(key string, value []byte) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.values == nil {
		cache.values = map[string][]byte{}
	}
	cache.values[key] = append([]byte{}, value...)
	return true
}

func (cache *memoryImmutableByteCache) CoalesceImmutableBytes(_ context.Context, _ string, execute func() error) (bool, error) {
	return false, execute()
}

func TestSpatialChildTileByteCacheRoundTripsEmptyAndRawTiles(t *testing.T) {
	cache := &memoryImmutableByteCache{values: map[string][]byte{}}
	for _, result := range []SpatialTileResult{
		{Bytes: []byte{}, Precision: string(dataquery.SpatialTilePrecisionAggregated)},
		{Bytes: []byte{1, 2, 3}, Features: 17, Precision: string(dataquery.SpatialTilePrecisionRaw)},
	} {
		key := spatialTileByteCacheKey("revision", 8, result.Features, 2)
		if !storeSpatialTileBytes(cache, key, result) {
			t.Fatal("bounded child tile was not cached")
		}
		cached, found, err := lookupSpatialTileBytes(cache, key)
		if err != nil || !found || !bytes.Equal(cached.Bytes, result.Bytes) || cached.Features != result.Features || cached.Precision != result.Precision {
			t.Fatalf("cached child = %#v found=%v err=%v, want %#v", cached, found, err, result)
		}
	}
	if spatialTileByteCacheKey("revision-a", 8, 1, 2) == spatialTileByteCacheKey("revision-b", 8, 1, 2) {
		t.Fatal("tile byte cache key omitted revision scope")
	}
}
