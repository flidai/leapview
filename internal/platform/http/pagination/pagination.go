// Package pagination contains the shared signed cursor and query-limit
// contract used by LeapView HTTP list and query endpoints.
package pagination

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
)

// IndexDomain is the closed set of signed offset-cursor domains. Keeping the
// domain typed prevents an index codec from accidentally receiving a keyset
// domain at the call site.
type IndexDomain string

const (
	DashboardIndexDomain IndexDomain = "d1"
	QueryIndexDomain     IndexDomain = "q1"
)

// KeysetDomain is the closed set of signed keyset-cursor domains.
type KeysetDomain string

const (
	DashboardKeysetDomain KeysetDomain = "d2"
	QueryKeysetDomain     KeysetDomain = "q2"
)

var (
	// ErrInvalidCursor intentionally does not disclose whether a token was
	// malformed, expired, signed for another domain, or tampered with.
	ErrInvalidCursor = errors.New("invalid page token")
	// ErrSnapshotMismatch lets handlers preserve the HTTP 409 contract when a
	// valid cursor belongs to a different serving snapshot.
	ErrSnapshotMismatch = errors.New("cursor serving snapshot is unavailable")
)

type IndexCursor struct {
	Offset   int    `json:"offset"`
	Scope    string `json:"scope"`
	Snapshot string `json:"snapshot,omitempty"`
	Expires  int64  `json:"expires"`
}

type KeysetCursor struct {
	Key      string `json:"key"`
	Scope    string `json:"scope"`
	Snapshot string `json:"snapshot,omitempty"`
	Expires  int64  `json:"expires"`
}

func EncodeIndex(domain IndexDomain, cursor IndexCursor) (string, error) {
	if !validIndexDomain(domain) {
		return "", fmt.Errorf("%w: unknown index domain %q", ErrInvalidCursor, domain)
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode index cursor: %w", err)
	}
	return cursorsigning.Sign(string(domain), payload), nil
}

func DecodeIndex(domain IndexDomain, token, scope, snapshot string, now time.Time) (int, error) {
	if token == "" {
		return 0, nil
	}
	if !validIndexDomain(domain) {
		return 0, ErrInvalidCursor
	}
	payload, err := cursorsigning.Verify(string(domain), token)
	if err != nil {
		return 0, ErrInvalidCursor
	}
	var cursor IndexCursor
	if json.Unmarshal(payload, &cursor) != nil || cursor.Offset < 0 || cursor.Expires < now.Unix() {
		return 0, ErrInvalidCursor
	}
	if cursor.Snapshot != snapshot {
		return 0, ErrSnapshotMismatch
	}
	if cursor.Scope != scope {
		return 0, ErrInvalidCursor
	}
	return cursor.Offset, nil
}

func EncodeKeyset(domain KeysetDomain, cursor KeysetCursor) (string, error) {
	if !validKeysetDomain(domain) {
		return "", fmt.Errorf("%w: unknown keyset domain %q", ErrInvalidCursor, domain)
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode keyset cursor: %w", err)
	}
	return cursorsigning.Sign(string(domain), payload), nil
}

func DecodeKeyset(domain KeysetDomain, token, scope, snapshot string, now time.Time) (string, error) {
	if token == "" {
		return "", nil
	}
	if !validKeysetDomain(domain) {
		return "", ErrInvalidCursor
	}
	payload, err := cursorsigning.Verify(string(domain), token)
	if err != nil {
		return "", ErrInvalidCursor
	}
	var cursor KeysetCursor
	if json.Unmarshal(payload, &cursor) != nil || cursor.Key == "" || cursor.Expires < now.Unix() || cursor.Scope != scope {
		return "", ErrInvalidCursor
	}
	if cursor.Snapshot != snapshot {
		return "", ErrSnapshotMismatch
	}
	return cursor.Key, nil
}

func validIndexDomain(domain IndexDomain) bool {
	return domain == DashboardIndexDomain || domain == QueryIndexDomain
}

func validKeysetDomain(domain KeysetDomain) bool {
	return domain == DashboardKeysetDomain || domain == QueryKeysetDomain
}

// ScopeParts preserves the legacy list scope default while making the
// snapshot binding explicit for callers that have one.
func ScopeParts(scopes ...string) (string, string) {
	if len(scopes) == 0 || strings.TrimSpace(scopes[0]) == "" {
		return "list", ""
	}
	snapshot := ""
	if len(scopes) > 1 {
		snapshot = scopes[1]
	}
	return scopes[0], snapshot
}

// RequestScope hashes the normalized request shape and body while excluding
// only the continuation token, so a cursor cannot be replayed for another
// query, route, or filter selection.
func RequestScope(r *nethttp.Request, payload any) string {
	query := r.URL.Query()
	query.Del("pageToken")
	body, _ := json.Marshal(payload)
	digest := sha256.Sum256([]byte(r.Method + "\n" + r.URL.Path + "\n" + query.Encode() + "\n" + string(body)))
	return hex.EncodeToString(digest[:])
}

func PageItemKey(value any) string {
	payload, _ := json.Marshal(value)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

type LimitPolicy struct {
	Default int
	Maximum int
}

func ParseLimit(value string, policy LimitPolicy) (int, error) {
	if policy.Default < 1 || policy.Maximum < 1 || policy.Default > policy.Maximum {
		return 0, fmt.Errorf("invalid pagination limit policy")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return policy.Default, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("limit must be an integer")
	}
	if limit < 1 {
		return 0, fmt.Errorf("limit must be at least 1")
	}
	if limit > policy.Maximum {
		return 0, fmt.Errorf("limit must not exceed %d", policy.Maximum)
	}
	return limit, nil
}
