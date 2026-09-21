package postgres

import (
	"context"
	"fmt"

	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
)

// CountServicePrincipalSecrets returns non-revoked secret counts for every
// service principal in one query.
func (r *Repository) CountServicePrincipalSecrets(ctx context.Context) (map[string]int, error) {
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := accessdb.New(db).CountServicePrincipalSecrets(ctx)
	if err != nil {
		return nil, fmt.Errorf("count service principal secrets: %w", err)
	}
	counts := map[string]int{}
	for _, row := range rows {
		counts[principalUUID(row.ServicePrincipalID)] = int(row.SecretCount)
	}
	return counts, nil
}
