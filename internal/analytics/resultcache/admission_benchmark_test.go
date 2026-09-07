package resultcache

import (
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"
	"testing"
	"time"
)

type admissionWorkloadMode string

const (
	admissionWorkloadLRU       admissionWorkloadMode = "lru"
	admissionWorkloadFrequency admissionWorkloadMode = "frequency_aware"
)

type admissionWorkloadRequest struct {
	scope        string
	key          string
	payloadBytes int
	missLatency  time.Duration
}

func (r admissionWorkloadRequest) composite() string { return r.scope + "\x00" + r.key }
func (r admissionWorkloadRequest) retainedBytes() int64 {
	return int64(len(r.key) + r.payloadBytes)
}

type admissionWorkloadEntry struct {
	composite string
	scope     string
	bytes     int64
}

type admissionWorkloadUsage struct {
	entries int
	bytes   int64
}

// admissionWorkloadCache is an evaluation-only model. Its LRU path is
// calibrated against Pool below; the frequency-aware path deliberately does
// not participate in production behavior until the FAI-535 evidence is
// reviewed.
type admissionWorkloadCache struct {
	mode       admissionWorkloadMode
	limits     Limits
	entries    map[string]*list.Element
	lru        *list.List
	scopes     map[string]admissionWorkloadUsage
	bytes      int64
	frequency  *admissionFrequencySketch
	rejections uint64
}

func newAdmissionWorkloadCache(mode admissionWorkloadMode, limits Limits) *admissionWorkloadCache {
	cache := &admissionWorkloadCache{
		mode: mode, limits: limits, entries: map[string]*list.Element{}, lru: list.New(), scopes: map[string]admissionWorkloadUsage{},
	}
	if mode == admissionWorkloadFrequency {
		cache.frequency = newAdmissionFrequencySketch(limits.NodeEntries)
	}
	return cache
}

func (c *admissionWorkloadCache) access(request admissionWorkloadRequest) bool {
	composite := request.composite()
	if c.frequency != nil {
		c.frequency.increment(admissionWorkloadDigest(composite))
	}
	if element := c.entries[composite]; element != nil {
		c.lru.MoveToFront(element)
		return true
	}

	entry := admissionWorkloadEntry{composite: composite, scope: request.scope, bytes: request.retainedBytes()}
	if entry.bytes > c.limits.RuntimeBytes || entry.bytes > c.limits.NodeBytes {
		return false
	}
	victims := c.victims(entry)
	if c.frequency != nil && len(victims) > 0 {
		candidateFrequency := c.frequency.estimate(admissionWorkloadDigest(composite))
		for _, victim := range victims {
			retained := victim.Value.(admissionWorkloadEntry)
			if c.frequency.estimate(admissionWorkloadDigest(retained.composite)) > candidateFrequency {
				c.rejections++
				return false
			}
		}
	}
	for _, victim := range victims {
		c.remove(victim)
	}
	element := c.lru.PushFront(entry)
	c.entries[composite] = element
	usage := c.scopes[request.scope]
	usage.entries++
	usage.bytes += entry.bytes
	c.scopes[request.scope] = usage
	c.bytes += entry.bytes
	return false
}

func (c *admissionWorkloadCache) victims(candidate admissionWorkloadEntry) []*list.Element {
	usage := c.scopes[candidate.scope]
	runtimeEntries := usage.entries + 1
	runtimeBytes := usage.bytes + candidate.bytes
	nodeEntries := len(c.entries) + 1
	nodeBytes := c.bytes + candidate.bytes
	picked := map[*list.Element]struct{}{}
	victims := make([]*list.Element, 0, 4)

	for element := c.lru.Back(); element != nil && (runtimeEntries > c.limits.RuntimeEntries || runtimeBytes > c.limits.RuntimeBytes); element = element.Prev() {
		entry := element.Value.(admissionWorkloadEntry)
		if entry.scope != candidate.scope {
			continue
		}
		picked[element] = struct{}{}
		victims = append(victims, element)
		runtimeEntries--
		runtimeBytes -= entry.bytes
		nodeEntries--
		nodeBytes -= entry.bytes
	}
	for element := c.lru.Back(); element != nil && (nodeEntries > c.limits.NodeEntries || nodeBytes > c.limits.NodeBytes); element = element.Prev() {
		if _, ok := picked[element]; ok {
			continue
		}
		entry := element.Value.(admissionWorkloadEntry)
		picked[element] = struct{}{}
		victims = append(victims, element)
		nodeEntries--
		nodeBytes -= entry.bytes
	}
	return victims
}

func (c *admissionWorkloadCache) remove(element *list.Element) {
	entry := element.Value.(admissionWorkloadEntry)
	delete(c.entries, entry.composite)
	c.lru.Remove(element)
	usage := c.scopes[entry.scope]
	usage.entries--
	usage.bytes -= entry.bytes
	c.scopes[entry.scope] = usage
	c.bytes -= entry.bytes
}

func (c *admissionWorkloadCache) order() []string {
	result := make([]string, 0, c.lru.Len())
	for element := c.lru.Front(); element != nil; element = element.Next() {
		result = append(result, element.Value.(admissionWorkloadEntry).composite)
	}
	return result
}

type admissionFrequencySketch struct {
	width    uint64
	counters []uint8
	samples  uint64
	resetAt  uint64
}

func newAdmissionFrequencySketch(nodeEntries int) *admissionFrequencySketch {
	width := uint64(64)
	for width < uint64(max(nodeEntries, 1)*10) {
		width <<= 1
	}
	return &admissionFrequencySketch{width: width, counters: make([]uint8, width*4), resetAt: width * 10}
}

func (s *admissionFrequencySketch) increment(digest [sha256.Size]byte) {
	for row := range 4 {
		index := uint64(row)*s.width + binary.LittleEndian.Uint64(digest[row*8:row*8+8])&(s.width-1)
		if s.counters[index] < 15 {
			s.counters[index]++
		}
	}
	s.samples++
	if s.samples < s.resetAt {
		return
	}
	for index := range s.counters {
		s.counters[index] >>= 1
	}
	s.samples = 0
}

func (s *admissionFrequencySketch) estimate(digest [sha256.Size]byte) uint8 {
	estimate := uint8(15)
	for row := range 4 {
		index := uint64(row)*s.width + binary.LittleEndian.Uint64(digest[row*8:row*8+8])&(s.width-1)
		estimate = min(estimate, s.counters[index])
	}
	return estimate
}

func (s *admissionFrequencySketch) bytes() int64 { return int64(len(s.counters)) }

func admissionWorkloadDigest(key string) [sha256.Size]byte { return sha256.Sum256([]byte(key)) }

type admissionWorkloadResult struct {
	requests          uint64
	hits              uint64
	requestedBytes    uint64
	hitBytes          uint64
	rejections        uint64
	p95InitialLatency time.Duration
	policyBytes       int64
	retainedEntries   int
	retainedBytes     int64
}

func (r admissionWorkloadResult) hitPercent() float64 {
	return float64(r.hits) * 100 / float64(r.requests)
}

func (r admissionWorkloadResult) byteHitPercent() float64 {
	return float64(r.hitBytes) * 100 / float64(r.requestedBytes)
}

func runRepresentativeAdmissionWorkload(mode admissionWorkloadMode) admissionWorkloadResult {
	limits := Limits{RuntimeEntries: 20, RuntimeBytes: 1536 << 10, NodeEntries: 40, NodeBytes: 3 << 20}
	cache := newAdmissionWorkloadCache(mode, limits)
	result := admissionWorkloadResult{}
	latencies := make([]time.Duration, 0, 336)

	access := func(request admissionWorkloadRequest) bool {
		result.requests++
		result.requestedBytes += uint64(request.retainedBytes())
		hit := cache.access(request)
		if hit {
			result.hits++
			result.hitBytes += uint64(request.retainedBytes())
		}
		return hit
	}
	dashboard := func(scope string) {
		latency := time.Duration(0)
		for index := range 8 {
			request := admissionWorkloadRequest{scope: scope, key: fmt.Sprintf("visual-%02d", index), payloadBytes: 96 << 10, missLatency: 90 * time.Millisecond}
			if index < 2 {
				request.payloadBytes = 8 << 10
				request.missLatency = 35 * time.Millisecond
			}
			if access(request) {
				latency += 200 * time.Microsecond
			} else {
				latency += request.missLatency
			}
		}
		latencies = append(latencies, latency)
	}

	for range 16 {
		dashboard("project-west")
		dashboard("project-east")
	}
	for cycle := range 80 {
		for scan := range 64 {
			sequence := cycle*64 + scan
			scope := "project-west"
			if sequence%2 != 0 {
				scope = "project-east"
			}
			access(admissionWorkloadRequest{
				scope: scope, key: fmt.Sprintf("one-off-%05d", sequence), payloadBytes: (1 + sequence%4) * (64 << 10), missLatency: 150 * time.Millisecond,
			})
		}
		dashboard("project-west")
		dashboard("project-east")
		dashboard("project-west")
		dashboard("project-east")
	}

	slices.Sort(latencies)
	result.rejections = cache.rejections
	result.p95InitialLatency = latencies[(len(latencies)-1)*95/100]
	result.retainedEntries = len(cache.entries)
	result.retainedBytes = cache.bytes
	if cache.frequency != nil {
		result.policyBytes = cache.frequency.bytes()
	}
	if result.retainedEntries > limits.NodeEntries || result.retainedBytes > limits.NodeBytes {
		panic(fmt.Sprintf("admission workload exceeded node limits: entries=%d bytes=%d", result.retainedEntries, result.retainedBytes))
	}
	for scope, usage := range cache.scopes {
		if usage.entries > limits.RuntimeEntries || usage.bytes > limits.RuntimeBytes {
			panic(fmt.Sprintf("admission workload scope %q exceeded limits: %#v", scope, usage))
		}
	}
	return result
}

func TestAdmissionWorkloadLRUMatchesProductionPool(t *testing.T) {
	limits := Limits{RuntimeEntries: 3, RuntimeBytes: 256, NodeEntries: 4, NodeBytes: 360}
	model := newAdmissionWorkloadCache(admissionWorkloadLRU, limits)
	pool, err := New(limits)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	scopes := map[string]*Scope{}
	for _, name := range []string{"west", "east"} {
		scopes[name], err = pool.OpenScope(ScopeID{RuntimeID: name})
		if err != nil {
			t.Fatal(err)
		}
	}
	trace := []admissionWorkloadRequest{
		{scope: "west", key: "a", payloadBytes: 80},
		{scope: "west", key: "b", payloadBytes: 80},
		{scope: "east", key: "c", payloadBytes: 80},
		{scope: "west", key: "a", payloadBytes: 80},
		{scope: "east", key: "d", payloadBytes: 80},
		{scope: "west", key: "e", payloadBytes: 120},
		{scope: "east", key: "c", payloadBytes: 80},
	}
	for index, request := range trace {
		_, token, productionHit, err := scopes[request.scope].LookupBytes(request.key)
		if err != nil {
			t.Fatal(err)
		}
		modelHit := model.access(request)
		if modelHit != productionHit {
			t.Fatalf("trace step %d hit = model %v production %v", index, modelHit, productionHit)
		}
		if !productionHit {
			if outcome := scopes[request.scope].StoreBytes(request.key, token, make([]byte, request.payloadBytes)); outcome != StoreStored {
				t.Fatalf("trace step %d store = %q", index, outcome)
			}
		}
	}

	pool.mu.Lock()
	productionOrder := make([]string, 0, pool.lru.Len())
	for element := pool.lru.Front(); element != nil; element = element.Next() {
		productionOrder = append(productionOrder, element.Value.(entry).composite)
	}
	poolBytes := pool.bytes
	pool.mu.Unlock()
	if !slices.Equal(model.order(), productionOrder) {
		t.Fatalf("LRU order = model %q production %q", model.order(), productionOrder)
	}
	if len(model.entries) != pool.Stats().Entries || model.bytes != poolBytes {
		t.Fatalf("LRU accounting = model entries=%d bytes=%d production=%#v", len(model.entries), model.bytes, pool.Stats())
	}
}

func TestAdmissionWorkloadCandidateImprovesBurstResistance(t *testing.T) {
	lru := runRepresentativeAdmissionWorkload(admissionWorkloadLRU)
	candidate := runRepresentativeAdmissionWorkload(admissionWorkloadFrequency)
	if candidate.hitPercent() <= lru.hitPercent() {
		t.Fatalf("candidate hit rate %.2f%% did not improve LRU %.2f%%", candidate.hitPercent(), lru.hitPercent())
	}
	if candidate.byteHitPercent() <= lru.byteHitPercent() {
		t.Fatalf("candidate byte hit rate %.2f%% did not improve LRU %.2f%%", candidate.byteHitPercent(), lru.byteHitPercent())
	}
	if candidate.p95InitialLatency >= lru.p95InitialLatency {
		t.Fatalf("candidate p95 %s did not improve LRU %s", candidate.p95InitialLatency, lru.p95InitialLatency)
	}
	if candidate.rejections == 0 {
		t.Fatal("candidate admitted every one-off entry")
	}
	if candidate.policyBytes > 64*40 {
		t.Fatalf("candidate policy bytes = %d, want bounded by %d", candidate.policyBytes, 64*40)
	}
}

func TestAdmissionWorkloadCandidatePreservesColdOnlyLRU(t *testing.T) {
	limits := Limits{RuntimeEntries: 4, RuntimeBytes: 1 << 20, NodeEntries: 4, NodeBytes: 1 << 20}
	lru := newAdmissionWorkloadCache(admissionWorkloadLRU, limits)
	candidate := newAdmissionWorkloadCache(admissionWorkloadFrequency, limits)
	for index := range 16 {
		request := admissionWorkloadRequest{scope: "cold", key: fmt.Sprintf("key-%02d", index), payloadBytes: 64 << 10}
		if lru.access(request) != candidate.access(request) {
			t.Fatalf("cold-only trace step %d returned different hit outcomes", index)
		}
	}
	if !slices.Equal(lru.order(), candidate.order()) {
		t.Fatalf("cold-only retention = LRU %q candidate %q", lru.order(), candidate.order())
	}
	if candidate.rejections != 0 {
		t.Fatalf("cold-only candidate rejections = %d, want 0", candidate.rejections)
	}
}

func TestAdmissionWorkloadModelRejectsOversizedEntries(t *testing.T) {
	limits := Limits{RuntimeEntries: 2, RuntimeBytes: 128, NodeEntries: 2, NodeBytes: 256}
	for _, mode := range []admissionWorkloadMode{admissionWorkloadLRU, admissionWorkloadFrequency} {
		cache := newAdmissionWorkloadCache(mode, limits)
		cache.access(admissionWorkloadRequest{scope: "one", key: "retained", payloadBytes: 64})
		beforeOrder := cache.order()
		beforeBytes := cache.bytes
		if cache.access(admissionWorkloadRequest{scope: "one", key: "oversized", payloadBytes: 256}) {
			t.Fatalf("%s oversized access unexpectedly hit", mode)
		}
		if !slices.Equal(cache.order(), beforeOrder) || cache.bytes != beforeBytes {
			t.Fatalf("%s oversized access changed retained state: order=%q bytes=%d", mode, cache.order(), cache.bytes)
		}
	}
}

func BenchmarkCacheAdmissionWorkload(b *testing.B) {
	for _, mode := range []admissionWorkloadMode{admissionWorkloadLRU, admissionWorkloadFrequency} {
		b.Run(string(mode), func(b *testing.B) {
			var result admissionWorkloadResult
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				result = runRepresentativeAdmissionWorkload(mode)
			}
			b.StopTimer()
			b.ReportMetric(result.hitPercent(), "hit-%")
			b.ReportMetric(result.byteHitPercent(), "byte-hit-%")
			b.ReportMetric(float64(result.p95InitialLatency)/float64(time.Millisecond), "p95-initial-ms")
			b.ReportMetric(float64(result.rejections), "rejections/op")
			b.ReportMetric(float64(result.policyBytes), "policy-B/op")
		})
	}
}
