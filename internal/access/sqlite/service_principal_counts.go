package sqlite

import (
	"context"
	"fmt"
)

// CountServicePrincipalSecrets returns non-revoked secret counts for every
// service principal in one query.
func (r *Repository) CountServicePrincipalSecrets(ctx context.Context) (map[string]int, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("access repository database is required")
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT service_principal_id, COUNT(*)
FROM service_principal_secrets
WHERE revoked_at IS NULL
GROUP BY service_principal_id`)
	if err != nil {
		return nil, err
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
