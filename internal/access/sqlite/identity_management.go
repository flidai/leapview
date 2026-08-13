package sqlite

import (
	"context"
	"database/sql"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

func (r *Repository) PrincipalIdentityManagement(ctx context.Context, principalID string) (access.PrincipalIdentityManagement, error) {
	principalID = strings.TrimSpace(principalID)
	if _, err := r.PrincipalByID(ctx, principalID); err != nil {
		return access.PrincipalIdentityManagement{}, err
	}

	var provider string
	err := r.db.QueryRowContext(ctx, `
SELECT provider
FROM external_identities
WHERE principal_id = ?
ORDER BY CASE WHEN provider = 'scim' THEN 0 ELSE 1 END, provider
LIMIT 1`, principalID).Scan(&provider)
	switch {
	case err == nil:
	case err == sql.ErrNoRows:
		provider = ""
	default:
		return access.PrincipalIdentityManagement{}, err
	}

	var hasLocalPassword bool
	if err := r.db.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM local_user_credentials WHERE principal_id = ?)`, principalID).Scan(&hasLocalPassword); err != nil {
		return access.PrincipalIdentityManagement{}, err
	}

	source := access.IdentityManagementSystem
	if provider != "" {
		source = access.IdentityManagementExternal
	} else if hasLocalPassword {
		source = access.IdentityManagementLocal
	}
	return access.PrincipalIdentityManagement{
		Source: source, Provider: provider, HasLocalPassword: hasLocalPassword,
	}, nil
}
