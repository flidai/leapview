package stream

import (
	"fmt"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/flidai/leapview/pkg/pagestream"
)

func TestDeliveryBrokerCoalescesResultBurst(t *testing.T) {
	broker := NewDeliveryBroker()
	updates, unsubscribe := broker.Subscribe("client:page")
	defer unsubscribe()

	for index := range 100 {
		id := fmt.Sprintf("visual-%03d", index)
		broker.PublishEnvelope("client:page", Envelope{
			Signals: pagestream.SignalPatch{"visuals": map[string]any{id: index}},
			Delivery: DeliveryMetadata{
				CoalesceGroup: "visual-results",
				MergeRoots:    []string{"visuals"},
			},
		})
	}

	got := map[string]any{}
	deadline := time.After(time.Second)
	for len(got) < 100 {
		select {
		case patch := <-updates:
			for id, value := range patch["visuals"].(map[string]any) {
				got[id] = value
			}
		case <-deadline:
			t.Fatalf("received %d/100 visual results", len(got))
		}
	}
}

func TestDeliveryBrokerPreservesEveryBoundaryForSlowSubscriber(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	broker := NewDeliveryBroker()
	updates, unsubscribe := broker.Subscribe("client:page")
	defer unsubscribe()

	const count = 100
	for sequence := range count {
		broker.PublishEnvelope("client:page", Envelope{
			Signals:  pagestream.SignalPatch{"sequence": sequence},
			Delivery: DeliveryMetadata{Boundary: true},
		})
	}

	for sequence := range count {
		select {
		case patch := <-updates:
			if patch["sequence"] != sequence {
				t.Fatalf("patch %d = %#v", sequence, patch)
			}
		case <-time.After(time.Second):
			t.Fatalf("received only %d/%d boundary patches", sequence, count)
		}
	}
}

func TestDeliveryBrokerDropsPendingOlderGeneration(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	broker := NewDeliveryBroker()
	updates, unsubscribe := broker.Subscribe("client:page")
	defer unsubscribe()

	broker.PublishEnvelope("client:page", Envelope{
		Signals:  pagestream.SignalPatch{"generation": 1},
		Delivery: DeliveryMetadata{Generation: 1, Boundary: true},
	})
	broker.PublishEnvelope("client:page", Envelope{
		Signals:  pagestream.SignalPatch{"generation": 2},
		Delivery: DeliveryMetadata{Generation: 2, Boundary: true},
	})
	broker.PublishEnvelope("client:page", Envelope{
		Signals:  pagestream.SignalPatch{"generation": "stale"},
		Delivery: DeliveryMetadata{Generation: 1, Boundary: true},
	})

	select {
	case patch := <-updates:
		if patch["generation"] != 2 {
			t.Fatalf("patch = %#v, want generation 2", patch)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for generation 2")
	}
}

func TestDeliveryBrokerRetainsMergeRootsAcrossChain(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	broker := NewDeliveryBroker()
	updates, unsubscribe := broker.Subscribe("dashboard:page")
	defer unsubscribe()

	broker.PublishEnvelope("dashboard:page", Envelope{
		Signals: pagestream.SignalPatch{"visuals": map[string]any{"one": 1}},
		Delivery: DeliveryMetadata{
			CoalesceGroup: "dashboard-results",
			MergeRoots:    []string{"visuals"},
		},
	})
	broker.PublishEnvelope("dashboard:page", Envelope{
		Signals:  pagestream.SignalPatch{"status": "running"},
		Delivery: DeliveryMetadata{CoalesceGroup: "dashboard-results"},
	})
	broker.PublishEnvelope("dashboard:page", Envelope{
		Signals:  pagestream.SignalPatch{"visuals": map[string]any{"two": 2}},
		Delivery: DeliveryMetadata{CoalesceGroup: "dashboard-results"},
	})

	select {
	case patch := <-updates:
		want := pagestream.SignalPatch{
			"status":  "running",
			"visuals": map[string]any{"one": 1, "two": 2},
		}
		if !reflect.DeepEqual(patch, want) {
			t.Fatalf("patch = %#v, want %#v", patch, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for coalesced patch")
	}
}
