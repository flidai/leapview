package runtimehost

import (
	"errors"
	"testing"
)

func TestAcquireBeforeFirstPublication(t *testing.T) {
	var manager Manager
	lease, err := manager.Acquire(t.Context())
	if lease != nil || !errors.Is(err, ErrNoActiveServingState) {
		t.Fatalf("fresh manager Acquire = %v, %v", lease, err)
	}
}
