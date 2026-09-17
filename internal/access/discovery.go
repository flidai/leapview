package access

// This file owns the common authorization boundary for discovery-shaped
// responses. Callers must pass the complete candidate set and an authorizer;
// pagination is deliberately applied only after authorization. This keeps
// denied resources out of items, totals, and every projection derived from
// the returned page (facets, autocomplete suggestions, and previews).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
)

const (
	DiscoveryCursorPrefix = "access-discovery-v1"
	DiscoveryDefaultLimit = 25
	DiscoveryMaxLimit     = 200
	DiscoveryMaxCursorLen = 4096
)

var (
	ErrInvalidDiscoveryRequest           = errors.New("invalid discovery request")
	ErrInvalidDiscoveryCursor            = errors.New("invalid discovery cursor")
	ErrDiscoveryContextChanged           = errors.New("discovery authorization context changed")
	ErrDiscoverySnapshotChanged          = errors.New("discovery serving snapshot changed")
	ErrDiscoveryAuthorizationUnavailable = errors.New("discovery authorization is unavailable")
)

// DiscoveryRequest is the normalized query shape used by one discovery
// collection. SecurityContext must identify every authorization input that
// can change visibility (for example principal, groups, credential scope, and
// policy revision). Snapshot is the serving generation/policy snapshot pin.
// Filter is an opaque, canonical representation of collection filters.
type DiscoveryRequest struct {
	Collection      string
	PrincipalID     string
	SecurityContext string
	Snapshot        string
	Query           string
	Filter          string
	Limit           int
	Cursor          string
	DefaultLimit    int
	MaxLimit        int
}

// DiscoveryPage contains only authorized values. Total is computed after the
// authorizer has accepted candidates, so callers can safely derive facets,
// autocomplete, and preview metadata from Items without a second filter pass.
type DiscoveryPage[T any] struct {
	Items      []T
	Total      int
	NextCursor string
}

// DiscoveryAuthorizer is evaluated for every candidate on every request,
// including continuation requests. Rechecking candidates before applying the
// cursor offset prevents a previously issued cursor from bypassing changed
// authorization.
type DiscoveryAuthorizer[T any] func(context.Context, T) (bool, error)

// AuthorizeAndPaginate filters candidates before calculating Total or applying
// the offset encoded by Cursor. Cursors are signed by the process cursor key
// ring and bound to the collection, query/filter shape, limit, serving
// snapshot, and authorization context. A nil authorizer fails closed.
func AuthorizeAndPaginate[T any](ctx context.Context, candidates []T, request DiscoveryRequest, authorize DiscoveryAuthorizer[T]) (DiscoveryPage[T], error) {
	if authorize == nil {
		return DiscoveryPage[T]{}, ErrDiscoveryAuthorizationUnavailable
	}
	normalized, err := normalizeDiscoveryRequest(request)
	if err != nil {
		return DiscoveryPage[T]{}, err
	}
	offset, err := decodeDiscoveryOffset(normalized)
	if err != nil {
		return DiscoveryPage[T]{}, err
	}

	authorized := make([]T, 0, len(candidates))
	for _, candidate := range candidates {
		allowed, err := authorize(ctx, candidate)
		if err != nil {
			return DiscoveryPage[T]{}, err
		}
		if allowed {
			authorized = append(authorized, candidate)
		}
	}

	if offset > len(authorized) {
		return DiscoveryPage[T]{}, ErrInvalidDiscoveryCursor
	}
	end := offset + normalized.Limit
	if end > len(authorized) {
		end = len(authorized)
	}
	page := DiscoveryPage[T]{
		Items: authorized[offset:end],
		Total: len(authorized),
	}
	if end < len(authorized) {
		cursor, err := encodeDiscoveryCursor(normalized, end)
		if err != nil {
			return DiscoveryPage[T]{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}

// FilterAndPaginate is a descriptive alias used by collection adapters whose
// primary concern is filtering rather than page construction.
func FilterAndPaginate[T any](ctx context.Context, candidates []T, request DiscoveryRequest, authorize DiscoveryAuthorizer[T]) (DiscoveryPage[T], error) {
	return AuthorizeAndPaginate(ctx, candidates, request, authorize)
}

type discoveryCursor struct {
	Context  string `json:"context"`
	Snapshot string `json:"snapshot,omitempty"`
	Request  string `json:"request"`
	Offset   int    `json:"offset"`
}

func normalizeDiscoveryRequest(request DiscoveryRequest) (DiscoveryRequest, error) {
	request.Collection = strings.TrimSpace(request.Collection)
	request.PrincipalID = strings.TrimSpace(request.PrincipalID)
	request.SecurityContext = strings.TrimSpace(request.SecurityContext)
	request.Snapshot = strings.TrimSpace(request.Snapshot)
	request.Query = strings.TrimSpace(request.Query)
	request.Filter = strings.TrimSpace(request.Filter)
	if request.Collection == "" || request.PrincipalID == "" || request.SecurityContext == "" {
		return DiscoveryRequest{}, ErrInvalidDiscoveryRequest
	}
	if request.DefaultLimit <= 0 {
		request.DefaultLimit = DiscoveryDefaultLimit
	}
	if request.MaxLimit <= 0 {
		request.MaxLimit = DiscoveryMaxLimit
	}
	if request.DefaultLimit > request.MaxLimit {
		return DiscoveryRequest{}, ErrInvalidDiscoveryRequest
	}
	if request.Limit == 0 {
		request.Limit = request.DefaultLimit
	}
	if request.Limit < 1 || request.Limit > request.MaxLimit {
		return DiscoveryRequest{}, ErrInvalidDiscoveryRequest
	}
	if len(request.Cursor) > DiscoveryMaxCursorLen {
		return DiscoveryRequest{}, ErrInvalidDiscoveryCursor
	}
	return request, nil
}

func decodeDiscoveryOffset(request DiscoveryRequest) (int, error) {
	if strings.TrimSpace(request.Cursor) == "" {
		return 0, nil
	}
	payload, err := cursorsigning.Verify(DiscoveryCursorPrefix, request.Cursor)
	if err != nil {
		return 0, ErrInvalidDiscoveryCursor
	}
	var cursor discoveryCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.Offset < 0 || cursor.Context == "" || cursor.Request == "" {
		return 0, ErrInvalidDiscoveryCursor
	}
	if cursor.Snapshot != digestDiscoveryPart(request.Snapshot) {
		return 0, ErrDiscoverySnapshotChanged
	}
	if cursor.Context != discoveryContextDigest(request) {
		return 0, ErrDiscoveryContextChanged
	}
	if cursor.Request != discoveryRequestDigest(request) {
		return 0, ErrInvalidDiscoveryCursor
	}
	return cursor.Offset, nil
}

func encodeDiscoveryCursor(request DiscoveryRequest, offset int) (string, error) {
	if offset < 0 {
		return "", ErrInvalidDiscoveryCursor
	}
	payload, err := json.Marshal(discoveryCursor{
		Context: discoveryContextDigest(request), Snapshot: digestDiscoveryPart(request.Snapshot),
		Request: discoveryRequestDigest(request), Offset: offset,
	})
	if err != nil {
		return "", fmt.Errorf("encode discovery cursor: %w", err)
	}
	return cursorsigning.Sign(DiscoveryCursorPrefix, payload), nil
}

func discoveryContextDigest(request DiscoveryRequest) string {
	return digestDiscoveryPart(request.PrincipalID + "\x00" + request.SecurityContext)
}

func discoveryRequestDigest(request DiscoveryRequest) string {
	return digestDiscoveryPart(request.Collection + "\x00" + request.Query + "\x00" + request.Filter + "\x00" + fmt.Sprint(request.Limit))
}

func digestDiscoveryPart(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
