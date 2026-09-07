package idempotency

import (
	"strings"
	"testing"
	"time"
)

func TestMemoryStoreExpiredPendingQuarantines(t *testing.T) {
	s := NewMemoryStore()
	digest := strings.Repeat("a", 64)
	first, execute, err := s.Claim(t.Context(), "scope", digest, "owner-1", 2*time.Millisecond, time.Hour)
	if err != nil || !execute || first.State != "pending" {
		t.Fatalf("first claim=%#v execute=%t err=%v", first, execute, err)
	}
	time.Sleep(10 * time.Millisecond)
	second, execute, err := s.Claim(t.Context(), "scope", digest, "owner-2", time.Second, time.Hour)
	if err != nil || execute || second.State != "indeterminate" || second.Status != 409 {
		t.Fatalf("expired claim=%#v execute=%t err=%v", second, execute, err)
	}
}

func TestMemoryStoreValidatesDigestAndBounds(t *testing.T) {
	s := NewMemoryStore()
	if _, _, err := s.Claim(t.Context(), "scope", "not-a-digest", "owner", time.Second, time.Hour); err != ErrInvalid {
		t.Fatalf("invalid digest err=%v", err)
	}
	if _, _, err := s.Claim(t.Context(), "scope", strings.Repeat("a", 64), "owner", maxMemoryLease+time.Second, time.Hour); err != ErrInvalid {
		t.Fatalf("oversized lease err=%v", err)
	}
	if _, _, err := s.Claim(t.Context(), "scope", strings.Repeat("a", 64), "owner", time.Second, maxMemoryLife+time.Second); err != ErrInvalid {
		t.Fatalf("oversized lifetime err=%v", err)
	}
}
