package pagination

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCursorDomainsRejectCrossDomainReplay(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	index, err := EncodeIndex(DashboardIndexDomain, IndexCursor{Offset: 3, Scope: "scope", Snapshot: "snapshot", Expires: now.Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeIndex(QueryIndexDomain, index, "scope", "snapshot", now); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("dashboard index cursor accepted by query index domain: %v", err)
	}
	keyset, err := EncodeKeyset(DashboardKeysetDomain, KeysetCursor{Key: "item", Scope: "scope", Snapshot: "snapshot", Expires: now.Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeKeyset(QueryKeysetDomain, keyset, "scope", "snapshot", now); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("dashboard keyset cursor accepted by query keyset domain: %v", err)
	}
}

func TestCursorVerificationBindsHMACExpiryScopeAndSnapshot(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	token, err := EncodeIndex(QueryIndexDomain, IndexCursor{Offset: 4, Scope: "scope", Snapshot: "snapshot", Expires: now.Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecodeIndex(QueryIndexDomain, token, "scope", "snapshot", now); err != nil || got != 4 {
		t.Fatalf("DecodeIndex() = %d, %v", got, err)
	}
	if _, err := DecodeIndex(QueryIndexDomain, token+"x", "scope", "snapshot", now); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("tampered token error = %v", err)
	}
	if _, err := DecodeIndex(QueryIndexDomain, token, "other", "snapshot", now); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("scope mismatch error = %v", err)
	}
	if _, err := DecodeIndex(QueryIndexDomain, token, "scope", "other", now); !errors.Is(err, ErrSnapshotMismatch) {
		t.Fatalf("snapshot mismatch error = %v", err)
	}
	expired, err := EncodeIndex(QueryIndexDomain, IndexCursor{Offset: 4, Scope: "scope", Snapshot: "snapshot", Expires: now.Add(-time.Second).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeIndex(QueryIndexDomain, expired, "scope", "snapshot", now); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("expired token error = %v", err)
	}
}

func TestRequestScopeAndPageItemKeyAreStable(t *testing.T) {
	first := httptest.NewRequest(http.MethodPost, "/query?limit=100&pageToken=one", nil)
	second := httptest.NewRequest(http.MethodPost, "/query?pageToken=two&limit=100", nil)
	if RequestScope(first, map[string]string{"field": "revenue"}) != RequestScope(second, map[string]string{"field": "revenue"}) {
		t.Fatal("continuation token changed request scope")
	}
	if RequestScope(first, map[string]string{"field": "cost"}) == RequestScope(second, map[string]string{"field": "revenue"}) {
		t.Fatal("request body did not change request scope")
	}
	if PageItemKey(struct{ ID string }{"one"}) != PageItemKey(struct{ ID string }{"one"}) {
		t.Fatal("page item key is not stable")
	}
}

func TestParseLimitRejectsOverLimit(t *testing.T) {
	policy := LimitPolicy{Default: 50, Maximum: 200}
	if got, err := ParseLimit("", policy); err != nil || got != 50 {
		t.Fatalf("default limit = %d, %v", got, err)
	}
	if got, err := ParseLimit("200", policy); err != nil || got != 200 {
		t.Fatalf("maximum limit = %d, %v", got, err)
	}
	if _, err := ParseLimit("201", policy); err == nil {
		t.Fatal("over-limit request was accepted")
	}
}
