package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// SelectCurrentExecutionGrantID resolves the one current execution grant for
// an exact process-bound pipeline target. A scheduled occurrence carries no
// grant ID itself, so selection is intentionally explicit and rejects both
// missing and ambiguous live grants. The caller still re-reads the selected
// row through CurrentExecutionGrant before queue admission.
func (r *Repository) SelectCurrentExecutionGrantID(ctx context.Context, instanceID string, projectID, pipelineID projectgraph.ResourceID) (string, error) {
	if r == nil {
		return "", errors.New("PostgreSQL access repository is unavailable")
	}
	if strings.TrimSpace(instanceID) == "" || strings.TrimSpace(instanceID) != instanceID {
		return "", errors.New("execution grant instance identity is required")
	}
	if err := projectID.Validate(); err != nil {
		return "", fmt.Errorf("execution grant project identity: %w", err)
	}
	if err := pipelineID.Validate(); err != nil {
		return "", fmt.Errorf("execution grant pipeline identity: %w", err)
	}
	db, err := r.requireDB()
	if err != nil {
		return "", err
	}
	rows, err := db.Query(ctx, `
		SELECT g.id
		FROM access.execution_grant g
		WHERE g.instance_id = $1
		  AND g.project_id = $2
		  AND g.resource_id = $3
		  AND g.resource_kind = 'pipeline'
		  AND g.revoked_at IS NULL
		  AND g.expires_at > clock_timestamp()
		  AND EXISTS (
			  SELECT 1
			  FROM project.resource_uid_registry target
			  WHERE target.instance_id = g.instance_id
			    AND target.project_id = g.project_id
			    AND target.resource_uid = g.resource_uid
			    AND target.authored_resource_id = g.resource_id
			    AND target.resource_kind = g.resource_kind
			    AND target.state = 'active'
		  )
		  AND EXISTS (
			  SELECT 1
			  FROM access.principal execution_principal
			  WHERE execution_principal.id = g.execution_principal_id
			    AND execution_principal.status = 'active'
			    AND execution_principal.revoked_at IS NULL
			    AND execution_principal.disabled_at IS NULL
			    AND execution_principal.blocked_at IS NULL
		  )
		ORDER BY g.id`, instanceID, projectID.String(), pipelineID.String())
	if err != nil {
		return "", fmt.Errorf("select current execution grants: %w", err)
	}
	defer rows.Close()

	var selected string
	count := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", fmt.Errorf("read current execution grant: %w", err)
		}
		count++
		if count == 1 {
			selected = id
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("read current execution grants: %w", err)
	}
	if count == 0 {
		return "", access.ErrGrantNotFound
	}
	if count != 1 {
		return "", fmt.Errorf("current execution grant selection is ambiguous: %d grants match the scheduled pipeline", count)
	}
	if strings.TrimSpace(selected) == "" || strings.TrimSpace(selected) != selected {
		return "", errors.New("current execution grant identity is invalid")
	}
	return selected, nil
}
