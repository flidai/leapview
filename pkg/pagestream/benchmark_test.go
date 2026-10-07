package pagestream

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

var benchmarkFanoutSubscriberCounts = []int{1, 10, 20, 100}
var benchmarkMailboxCapacities = []int{1, 10, 20, 100}

// BenchmarkBrokerPublishFanout measures one full, ordered burst delivered to
// every subscriber. Each operation publishes mailbox-capacity patches and
// drains all subscribers before the next burst. Payload maps are prepared
// outside the timer so the measurement isolates Broker.Publish and mailbox
// delivery from caller-side patch construction.
func BenchmarkBrokerPublishFanout(b *testing.B) {
	for _, subscriberCount := range benchmarkFanoutSubscriberCounts {
		for _, capacity := range benchmarkMailboxCapacities {
			b.Run(fmt.Sprintf("subscribers=%d/capacity=%d", subscriberCount, capacity), func(b *testing.B) {
				const streamID = "benchmark:page"
				broker := NewBrokerWithPendingLimit(capacity)
				subscriptions := benchmarkSubscribe(b, broker, streamID, subscriberCount)
				patches := benchmarkPatches(capacity)

				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					for _, patch := range patches {
						broker.Publish(streamID, patch)
					}
					for _, mailbox := range subscriptions.updates {
						for sequence, want := range patches {
							got, open := <-mailbox
							if !open {
								b.Fatalf("subscriber closed before patch %d", sequence)
							}
							if got["sequence"] != want["sequence"] {
								b.Fatalf("patch %d = %#v, want %#v", sequence, got, want)
							}
						}
					}
				}
				b.StopTimer()
				benchmarkUnsubscribe(subscriptions)
				benchmarkRequireNoStreams(b, broker)
				b.ReportMetric(float64(capacity*subscriberCount), "deliveries/op")
			})
		}
	}
}

// BenchmarkBrokerPublishSlowSubscriberOverflow measures capacity+1 publishes
// to a batch of subscribers that never read. The first capacity patches fill
// each mailbox; the last publish disconnects every slow subscriber. Fixture
// creation and post-run delivery/closure checks stay outside the timed region.
func BenchmarkBrokerPublishSlowSubscriberOverflow(b *testing.B) {
	const fixtureBatchSize = 32
	for _, subscriberCount := range benchmarkFanoutSubscriberCounts {
		for _, capacity := range benchmarkMailboxCapacities {
			b.Run(fmt.Sprintf("subscribers=%d/capacity=%d", subscriberCount, capacity), func(b *testing.B) {
				broker := NewBrokerWithPendingLimit(capacity)
				patches := benchmarkPatches(capacity + 1)

				b.ReportAllocs()
				b.StopTimer()
				fixtures := benchmarkPrepareOverflowBatch(b, broker, fixtureBatchSize, subscriberCount)
				defer func() {
					b.StopTimer()
					benchmarkCleanupOverflowBatch(fixtures)
				}()
				b.ResetTimer()
				b.StartTimer()
				for operation := 0; operation < b.N; operation++ {
					if operation > 0 && operation%fixtureBatchSize == 0 {
						b.StopTimer()
						benchmarkVerifyOverflowBatch(b, broker, fixtures, fixtureBatchSize, capacity)
						benchmarkCleanupOverflowBatch(fixtures)
						fixtures = benchmarkPrepareOverflowBatch(b, broker, fixtureBatchSize, subscriberCount)
						b.StartTimer()
					}
					fixture := fixtures[operation%fixtureBatchSize]
					for _, patch := range patches {
						broker.Publish(fixture.streamID, patch)
					}
				}
				b.StopTimer()
				processed := b.N % fixtureBatchSize
				if processed == 0 {
					if b.N > 0 {
						processed = fixtureBatchSize
					}
				}
				if processed > 0 {
					benchmarkVerifyOverflowBatch(b, broker, fixtures, processed, capacity)
				}
				benchmarkCleanupOverflowBatch(fixtures)
				benchmarkRequireNoStreams(b, broker)
				b.ReportMetric(float64(capacity*subscriberCount), "queued-deliveries/op")
				b.ReportMetric(float64(capacity+1), "publishes/op")
			})
		}
	}
}

// BenchmarkSignalStreamForwardCancellation measures context cancellation after
// Forward has registered its broker subscription. It includes any remaining
// forward-loop initialization, cancellation, exit, and deferred unsubscribe.
// Stream construction,
// goroutine startup, and the subscription-ready wait are outside the timer.
func BenchmarkSignalStreamForwardCancellation(b *testing.B) {
	for _, streamCount := range benchmarkFanoutSubscriberCounts {
		b.Run(fmt.Sprintf("streams=%d", streamCount), func(b *testing.B) {
			b.ReportAllocs()
			b.StopTimer()
			b.ResetTimer()
			for range b.N {
				benchmarkRunForwardCancellation(b, streamCount)
			}
			b.ReportMetric(float64(streamCount), "streams/op")
		})
	}
}

type benchmarkOverflowFixture struct {
	streamID      string
	subscriptions benchmarkSubscriptionSet
}

type benchmarkForward struct {
	streamID  string
	cancel    context.CancelFunc
	done      <-chan error
	completed bool
}

type benchmarkSubscriptionSet struct {
	updates      []<-chan SignalPatch
	unsubscribes []func()
}

func benchmarkSubscribe(b *testing.B, broker *Broker, streamID string, subscribers int) benchmarkSubscriptionSet {
	b.Helper()
	subscriptions := benchmarkSubscriptionSet{
		updates:      make([]<-chan SignalPatch, 0, subscribers),
		unsubscribes: make([]func(), 0, subscribers),
	}
	for range subscribers {
		mailbox, unsubscribe, err := broker.Subscribe(streamID)
		if err != nil {
			b.Fatalf("subscribe: %v", err)
		}
		subscriptions.updates = append(subscriptions.updates, mailbox)
		subscriptions.unsubscribes = append(subscriptions.unsubscribes, unsubscribe)
	}
	return subscriptions
}

func benchmarkUnsubscribe(subscriptions benchmarkSubscriptionSet) {
	for _, unsubscribe := range subscriptions.unsubscribes {
		unsubscribe()
	}
}

func benchmarkPatches(count int) []SignalPatch {
	patches := make([]SignalPatch, count)
	for sequence := range count {
		patches[sequence] = SignalPatch{"sequence": sequence}
	}
	return patches
}

func benchmarkPrepareOverflowBatch(b *testing.B, broker *Broker, batchSize, subscribers int) []benchmarkOverflowFixture {
	b.Helper()
	fixtures := make([]benchmarkOverflowFixture, batchSize)
	for index := range batchSize {
		streamID := fmt.Sprintf("benchmark:overflow:%d", index)
		fixtures[index] = benchmarkOverflowFixture{
			streamID:      streamID,
			subscriptions: benchmarkSubscribe(b, broker, streamID, subscribers),
		}
	}
	return fixtures
}

func benchmarkVerifyOverflowBatch(b *testing.B, broker *Broker, fixtures []benchmarkOverflowFixture, count, capacity int) {
	b.Helper()
	for _, fixture := range fixtures[:count] {
		for _, mailbox := range fixture.subscriptions.updates {
			for sequence := range capacity {
				patch, open := <-mailbox
				if !open {
					b.Fatalf("%s closed before queued patch %d", fixture.streamID, sequence)
				}
				if patch["sequence"] != sequence {
					b.Fatalf("%s patch %d = %#v", fixture.streamID, sequence, patch)
				}
			}
			if _, open := <-mailbox; open {
				b.Fatalf("%s remained open after overflow", fixture.streamID)
			}
		}
		broker.mu.Lock()
		_, retained := broker.clients[fixture.streamID]
		broker.mu.Unlock()
		if retained {
			b.Fatalf("broker retained overflowed stream %s", fixture.streamID)
		}
	}
}

func benchmarkCleanupOverflowBatch(fixtures []benchmarkOverflowFixture) {
	for _, fixture := range fixtures {
		benchmarkUnsubscribe(fixture.subscriptions)
	}
}

func benchmarkRequireNoStreams(b *testing.B, broker *Broker) {
	b.Helper()
	broker.mu.Lock()
	remaining := len(broker.clients)
	broker.mu.Unlock()
	if remaining != 0 {
		b.Fatalf("broker retained %d streams after benchmark cleanup", remaining)
	}
}

func benchmarkRunForwardCancellation(b *testing.B, streamCount int) {
	b.Helper()
	broker := NewBroker()
	forwards := make([]benchmarkForward, 0, streamCount)
	timed := false
	cleanup := func() {
		if timed {
			b.StopTimer()
			timed = false
		}
		for index := range forwards {
			forwards[index].cancel()
		}
		timeout := time.NewTimer(5 * time.Second)
		defer timeout.Stop()
		for index := range forwards {
			if forwards[index].completed {
				continue
			}
			select {
			case <-forwards[index].done:
				forwards[index].completed = true
			case <-timeout.C:
				b.Errorf("forward %s did not stop during bounded cleanup", forwards[index].streamID)
				return
			}
		}
	}
	defer cleanup()

	for index := range streamCount {
		streamID := fmt.Sprintf("benchmark:page:%d", index)
		ctx, cancel := context.WithCancel(context.Background())
		request := httptest.NewRequest(http.MethodGet, "/updates", nil).WithContext(ctx)
		stream := NewSignalStream(httptest.NewRecorder(), request)
		done := make(chan error, 1)
		forwards = append(forwards, benchmarkForward{streamID: streamID, cancel: cancel, done: done})
		go func() {
			done <- stream.Forward(ctx, broker, streamID)
		}()
	}
	for _, forward := range forwards {
		benchmarkWaitForSubscription(b, broker, forward.streamID)
	}

	timeout := time.NewTimer(5 * time.Second)
	b.StartTimer()
	timed = true
	for index := range forwards {
		forwards[index].cancel()
	}
	for index := range forwards {
		select {
		case err := <-forwards[index].done:
			forwards[index].completed = true
			if err != nil {
				b.Fatalf("forward %s returned an error: %v", forwards[index].streamID, err)
			}
		case <-timeout.C:
			b.Fatalf("forward %s did not stop after cancellation", forwards[index].streamID)
		}
	}
	b.StopTimer()
	timed = false
	timeout.Stop()
	benchmarkRequireNoStreams(b, broker)
}

func benchmarkWaitForSubscription(b *testing.B, broker *Broker, streamID string) {
	b.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		broker.mu.Lock()
		subscribers := len(broker.clients[streamID])
		broker.mu.Unlock()
		if subscribers == 1 {
			return
		}
		if !time.Now().Before(deadline) {
			b.Fatalf("forward %s did not subscribe", streamID)
		}
		runtime.Gosched()
	}
}
