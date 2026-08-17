package http

import (
	"errors"
	"testing"
)

func TestFallbackStreamInstanceIDFailsClosedWhenRandomUnavailable(t *testing.T) {
	previous := readStreamInstanceRandom
	t.Cleanup(func() { readStreamInstanceRandom = previous })
	readStreamInstanceRandom = func([]byte) (int, error) { return 0, errors.New("entropy unavailable") }

	if got, err := fallbackStreamInstanceID(); err == nil || got != "" {
		t.Fatalf("fallback stream identity = %q, err=%v; want explicit failure", got, err)
	}
}
