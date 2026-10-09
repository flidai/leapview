package pagestream

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/testing/ssetest"
)

type httpFanoutFixture struct {
	broker  *Broker
	streams []string
	readers []*bufio.Reader
}

func newHTTPFanoutFixture(tb testing.TB, readers int, independent bool) httpFanoutFixture {
	tb.Helper()
	broker := NewBrokerWithPendingLimit(2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		updates, unsubscribe, err := broker.Subscribe(r.URL.Query().Get("stream"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer unsubscribe()
		stream := NewSignalStream(w, r)
		if err := stream.Patch(SignalPatch{"ready": true}); err != nil {
			return
		}
		_ = stream.ForwardUpdates(r.Context(), updates)
	}))
	ctx, cancel := context.WithCancel(tb.Context())
	responses := make([]*http.Response, 0, readers)
	tb.Cleanup(func() {
		cancel()
		for _, response := range responses {
			_ = response.Body.Close()
		}
		server.CloseClientConnections()
		finished := make(chan struct{})
		go func() { server.Close(); close(finished) }()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			tb.Error("HTTP SSE handlers did not drain after cancellation")
			return
		}
		broker.mu.Lock()
		defer broker.mu.Unlock()
		if len(broker.clients) != 0 {
			tb.Errorf("HTTP SSE cleanup left %d stream subscriptions", len(broker.clients))
		}
	})
	client := server.Client()
	client.Timeout = 30 * time.Second
	fixture := httpFanoutFixture{broker: broker}
	for index := range readers {
		streamID := "shared"
		if independent {
			streamID = "independent-" + strconv.Itoa(index)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"?stream="+streamID, nil)
		if err != nil {
			tb.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			tb.Fatal(err)
		}
		responses = append(responses, response)
		if response.StatusCode != http.StatusOK {
			tb.Fatalf("SSE status = %d", response.StatusCode)
		}
		reader := bufio.NewReader(response.Body)
		patch, err := readHTTPFanoutPatch(reader)
		if err != nil || patch["ready"] != true {
			tb.Fatalf("SSE readiness = %#v, %v", patch, err)
		}
		fixture.readers = append(fixture.readers, reader)
		if independent || index == 0 {
			fixture.streams = append(fixture.streams, streamID)
		}
	}
	return fixture
}

func readHTTPFanoutPatch(reader *bufio.Reader) (map[string]any, error) {
	var body strings.Builder
	bytesRead := 0
	for bytesRead < 64*1024 {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		body.WriteString(line)
		bytesRead += len(line)
		if line == "\n" {
			patches, err := ssetest.DecodePatchSignals(body.String())
			if err != nil {
				return nil, err
			}
			if len(patches) == 0 && len(ssetest.ParseEvents(body.String())) == 0 {
				// Datastar may separate frames with extra blank lines, and
				// keepalive comments carry no signal event.
				body.Reset()
				continue
			}
			if len(patches) != 1 {
				return nil, fmt.Errorf("expected one complete signal patch, got %d", len(patches))
			}
			return patches[0], nil
		}
	}
	return nil, fmt.Errorf("HTTP SSE frame exceeded bounded fixture size")
}

func (fixture httpFanoutFixture) exchange(sequence int) error {
	start := make(chan struct{})
	var publishers sync.WaitGroup
	publishers.Add(len(fixture.streams))
	for _, streamID := range fixture.streams {
		go func() {
			defer publishers.Done()
			<-start
			fixture.broker.Publish(streamID, SignalPatch{"sequence": sequence})
		}()
	}
	close(start)
	publishers.Wait()
	for index, reader := range fixture.readers {
		patch, err := readHTTPFanoutPatch(reader)
		if err != nil || patch["sequence"] != float64(sequence) {
			return fmt.Errorf("HTTP reader %d received %#v (%v), want sequence %d", index, patch, err, sequence)
		}
	}
	return nil
}

// One operation publishes an ordered signal patch through production Broker
// and SignalStream to every real HTTP client. Shared has one busy stream;
// independent has simultaneous publishers on separate streams. Timing includes
// coordination, encoding, loopback transport, reads and assertions. It is a
// complete exchange batch, not user p95 or a promise of network capacity.
func BenchmarkSignalStreamHTTPFanoutCapacity(b *testing.B) {
	for _, independent := range []bool{false, true} {
		mode := "shared"
		if independent {
			mode = "independent"
		}
		for _, readers := range []int{1, 10, 20, 100} {
			b.Run(fmt.Sprintf("%s/readers=%d", mode, readers), func(b *testing.B) {
				fixture := newHTTPFanoutFixture(b, readers, independent)
				b.ReportAllocs()
				b.ResetTimer()
				for sequence := range b.N {
					if err := fixture.exchange(sequence); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(readers), "deliveries/op")
				b.ReportMetric(float64(len(fixture.streams)), "publishes/op")
			})
		}
	}
}

func TestSignalStreamHTTPCapacityIsolatesIndependentStreamsAndDrains(t *testing.T) {
	for _, independent := range []bool{false, true} {
		t.Run(fmt.Sprintf("independent=%t", independent), func(t *testing.T) {
			fixture := newHTTPFanoutFixture(t, 4, independent)
			for sequence := range 3 {
				if err := fixture.exchange(sequence); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
