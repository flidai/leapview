package protocol

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	"github.com/flidai/leapview/internal/platform/http/idempotency"
)

func legacyIdempotencyScope(r *http.Request, caller, credential, key string) string {
	return caller + ":" + credential + ":" + r.Method + ":" + r.URL.EscapedPath() + ":" + key
}

// rolloutIdempotencyScope preserves terminal and in-flight requests written by
// the pre-v2 protocol. A v2 record always wins. Legacy records are eligible
// only when the current server-owned Project agrees with the concrete route;
// the normal claim/replay path still verifies the exact request digest and
// current authorization before returning any stored response.
func (p *Protocol) rolloutIdempotencyScope(r *http.Request, current, legacy string, authoritative AuthoritativeScope) (string, error) {
	if p == nil || p.store == nil {
		return "", errors.New("idempotency store is unavailable")
	}
	if found, err := p.idempotencyScopeExists(r.Context(), current); err != nil {
		return "", fmt.Errorf("load current idempotency scope: %w", err)
	} else if found {
		return current, nil
	}
	found, err := p.idempotencyScopeExists(r.Context(), legacy)
	if err != nil {
		return "", fmt.Errorf("load legacy idempotency scope: %w", err)
	}
	if !found {
		return current, nil
	}
	if err := validateLegacyIdempotencyScope(r, authoritative); err != nil {
		return "", err
	}
	return legacy, nil
}

func (p *Protocol) idempotencyScopeExists(ctx context.Context, scope string) (bool, error) {
	record, err := p.store.Load(ctx, scope)
	if errors.Is(err, idempotency.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Package-local stores used by protocol tests historically represented a
	// miss as an empty record. Preserve that harmless fixture convention while
	// production stores return the explicit capability error above.
	return record.State != "" || record.Digest != "", nil
}

func validateLegacyIdempotencyScope(r *http.Request, authoritative AuthoritativeScope) error {
	if !legacyProjectScopedIdempotencyRequest(r) {
		return nil
	}
	if strings.TrimSpace(authoritative.ProjectID) == "" {
		return errors.New("legacy Project idempotency scope has no authoritative Project")
	}
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
		return nil
	}
	contract, ok := apiaggregate.GetAPIGenOperationContractForRequest(r.Method, r.URL.Path)
	if !ok || !strings.Contains(contract.Path, "{project}") {
		return errors.New("legacy Project idempotency route is unavailable")
	}
	projectID := pathParameter(contract.Path, r.URL.Path, "project")
	if projectID == "" || projectID != authoritative.ProjectID {
		return errors.New("legacy Project idempotency route disagrees with the authoritative Project")
	}
	return nil
}

func legacyProjectScopedIdempotencyRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
		return true
	}
	contract, ok := apiaggregate.GetAPIGenOperationContractForRequest(r.Method, r.URL.Path)
	return ok && strings.Contains(contract.Path, "{project}")
}
