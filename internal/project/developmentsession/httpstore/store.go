// Package httpstore adapts the authenticated development-session API to the
// project-owned Store port. It carries no candidate/publication authority.
package httpstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/flidai/leapview/internal/project/developmentsession"
)

const maxResponseBytes = 1 << 20

type Store struct {
	client *http.Client
	origin string
	token  string
	key    developmentsession.Key
}

func New(client *http.Client, origin, token string, key developmentsession.Key) (*Store, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if err := key.Validate(); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(origin), "/"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.EscapedPath() != "" && parsed.EscapedPath() != "/") {
		return nil, fmt.Errorf("development session target origin is invalid")
	}
	canonicalOrigin := parsed.Scheme + "://" + parsed.Host
	return &Store{client: client, origin: canonicalOrigin, token: strings.TrimSpace(token), key: key}, nil
}

func (s *Store) endpoint() string {
	return s.origin + "/api/v1/projects/" + url.PathEscape(s.key.ProjectID.String()) + "/targets/" + url.PathEscape(s.key.TargetID) + "/development-session/"
}

func (s *Store) Resolve(ctx context.Context, key developmentsession.Key) (developmentsession.Record, error) {
	if s == nil || s.client == nil {
		return developmentsession.Record{}, developmentsession.ErrInvalid
	}
	if err := key.Validate(); err != nil {
		return developmentsession.Record{}, err
	}
	if key.ID() != s.key.ID() {
		return developmentsession.Record{}, developmentsession.ErrOwnerMismatch
	}
	var record developmentsession.Record
	if err := s.do(ctx, http.MethodGet, s.endpoint(), nil, &record); err != nil {
		return developmentsession.Record{}, err
	}
	return validateResponse(record, key)
}

func (s *Store) Save(ctx context.Context, record developmentsession.Record, expected int64) (developmentsession.Record, error) {
	if s == nil || s.client == nil {
		return developmentsession.Record{}, developmentsession.ErrInvalid
	}
	normalized, err := record.Normalize()
	if err != nil {
		return developmentsession.Record{}, err
	}
	if normalized.Key.ID() != s.key.ID() {
		return developmentsession.Record{}, developmentsession.ErrOwnerMismatch
	}
	body, err := json.Marshal(struct {
		Revision    int64                           `json:"revision"`
		Attempted   developmentsession.Identity     `json:"attempted"`
		LastValid   developmentsession.Identity     `json:"lastValid"`
		Diagnostics []developmentsession.Diagnostic `json:"diagnostics"`
	}{expected, normalized.Attempted, normalized.LastValid, normalized.Diagnostics})
	if err != nil {
		return developmentsession.Record{}, err
	}
	var updated developmentsession.Record
	if err := s.do(ctx, http.MethodPut, s.endpoint(), bytes.NewReader(body), &updated); err != nil {
		return developmentsession.Record{}, err
	}
	return validateResponse(updated, s.key)
}

func validateResponse(record developmentsession.Record, key developmentsession.Key) (developmentsession.Record, error) {
	// Do not let Normalize manufacture a missing response identity. A server
	// response is accepted only when it proves the exact authenticated scope
	// requested by this client, including checkout/worktree isolation.
	if strings.TrimSpace(record.ID) == "" || record.ID != key.ID() {
		return developmentsession.Record{}, developmentsession.ErrOwnerMismatch
	}
	normalized, err := record.Normalize()
	if err != nil {
		return developmentsession.Record{}, err
	}
	if normalized.ID != key.ID() || normalized.Key != key {
		return developmentsession.Record{}, developmentsession.ErrOwnerMismatch
	}
	return normalized, nil
}

func (s *Store) do(ctx context.Context, method, endpoint string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	response, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		// Error details are intentionally discarded. They may contain an
		// upstream credential or arbitrary HTML; callers receive only a bounded
		// typed transport error.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes+1))
		switch response.StatusCode {
		case http.StatusNotFound:
			return developmentsession.ErrNotFound
		case http.StatusConflict:
			return developmentsession.ErrConflict
		case http.StatusUnauthorized, http.StatusForbidden:
			return developmentsession.ErrOwnerMismatch
		default:
			return fmt.Errorf("development session API returned HTTP %d", response.StatusCode)
		}
	}
	if out == nil {
		return nil
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(bodyBytes) > maxResponseBytes {
		return fmt.Errorf("development session API response exceeds %d bytes", maxResponseBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(bodyBytes))
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("development session API response contains trailing JSON")
		}
		return fmt.Errorf("development session API response contains trailing data: %w", err)
	}
	return nil
}

var _ developmentsession.Store = (*Store)(nil)
