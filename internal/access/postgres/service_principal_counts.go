package postgres

import (
	"context"
	"fmt"
)

// CountServicePrincipalSecrets returns non-revoked secret counts for every
// service principal in one query.
func (r *Repository) CountServicePrincipalSecrets(ctx context.Context) (map[string]int, error) {
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `
SELECT service_principal_id::text, COUNT(*)
FROM access.service_principal_secret
WHERE revoked_at IS NULL
GROUP BY service_principal_id`)
	if err != nil {
		return nil, fmt.Errorf("count service principal secrets: %w", err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var principalID string
		var count int
		if err := rows.Scan(&principalID, &count); err != nil {
			return nil, err
		}
		counts[principalID] = count
	}
	return counts, rows.Err()
}
