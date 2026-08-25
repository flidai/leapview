package pagestream

import (
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestBrokerPublishesAndUnsubscribes(t *testing.T) {
	broker := NewBroker()
	updates, unsubscribe, err := broker.Subscribe("client:page")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	broker.Publish("client:page", SignalPatch{"status": "ready"})
	select {
	case patch := <-updates:
		if patch["status"] != "ready" {
			t.Fatalf("patch = %#v", patch)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for broker patch")
	}

	unsubscribe()
	select {
	case _, open := <-updates:
		if open {
			t.Fatal("subscription remained open after unsubscribe")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription did not close")
	}
}

func TestBrokerPublishDoesNotBlockOnSlowSubscriber(t *testing.T) {
	broker := NewBroker()
	_, unsubscribe, err := broker.Subscribe("client:page")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for sequence := range 100 {
			broker.Publish("client:page", SignalPatch{"sequence": sequence})
		}
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked on a slow subscriber")
	}
}

func TestBrokerPublishPreservesEveryPatchForSlowSubscriber(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	broker := NewBroker()
	updates, unsubscribe, err := broker.Subscribe("client:page")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()

	const count = 100
	for sequence := range count {
		broker.Publish("client:page", SignalPatch{"sequence": sequence})
	}

	for sequence := range count {
		select {
		case patch := <-updates:
			if patch["sequence"] != sequence {
				t.Fatalf("patch %d = %#v", sequence, patch)
			}
		case <-time.After(time.Second):
			t.Fatalf("received only %d/%d patches", sequence, count)
		}
	}
}

func TestBrokerDisconnectsSlowSubscriberAtPendingLimit(t *testing.T) {
	broker := NewBrokerWithPendingLimit(2)
	updates, unsubscribe, err := broker.Subscribe("client:page")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()

	broker.Publish("client:page", SignalPatch{"sequence": 0})
	broker.Publish("client:page", SignalPatch{"sequence": 1})
	broker.Publish("client:page", SignalPatch{"sequence": 2})

	for sequence := range 2 {
		patch, open := <-updates
		if !open {
			t.Fatalf("subscription closed before draining patch %d", sequence)
		}
		if patch["sequence"] != sequence {
			t.Fatalf("patch %d = %#v", sequence, patch)
		}
	}
	if _, open := <-updates; open {
		t.Fatal("slow subscription remained open after overflow")
	}
}

func TestBrokerSubscribeRejectsEmptyStreamID(t *testing.T) {
	updates, unsubscribe, err := NewBroker().Subscribe("")
	if !errors.Is(err, ErrEmptyStreamID) {
		t.Fatalf("subscribe error = %v, want ErrEmptyStreamID", err)
	}
	if updates != nil || unsubscribe != nil {
		t.Fatal("rejected subscription returned non-nil values")
	}
}
