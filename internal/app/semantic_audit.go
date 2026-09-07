package app

import (
	"context"
	"errors"

	accessmodule "github.com/flidai/leapview/internal/access/module"
)

// semanticAuditActorFromContext reads authentication provenance only. Query
// metadata is not an identity source; delegated execution remains subject to
// the existing consumer's explicit ViewAs authorization boundary.
func semanticAuditActorFromContext(ctx context.Context) (string, error) {
	principal, ok := accessmodule.PrincipalFromContext(ctx)
	if !ok || principal.ID == "" {
		return "", errors.New("semantic audit authenticated actor is unavailable")
	}
	return principal.ID, nil
}
