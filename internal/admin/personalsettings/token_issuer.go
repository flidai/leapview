package personalsettings

import (
	"context"

	"github.com/flidai/leapview/internal/access"
)

// TokenIssuer is an optional, request-bound initial-publication authority for
// browser token creation only. Create owns the credential mutation and audit in
// the same transaction as its authority checks. Returning handled=false keeps
// normal snapshot authority; errors never select that fallback.
type TokenIssuer interface {
	PermissionOptions(context.Context, string) ([]access.PermissionPair, bool, error)
	Create(context.Context, access.ScopedAPITokenInput) (string, bool, error)
}

func (s *Service) tokenCreationOptions(ctx context.Context, actor string) ([]access.PermissionPair, error) {
	if s.TokenIssuer != nil {
		pairs, handled, err := s.TokenIssuer.PermissionOptions(ctx, actor)
		if handled || err != nil {
			return pairs, err
		}
	}
	if s.CurrentEffectivePermissionOptions == nil {
		return []access.PermissionPair{}, nil
	}
	return s.CurrentEffectivePermissionOptions(ctx, actor)
}
