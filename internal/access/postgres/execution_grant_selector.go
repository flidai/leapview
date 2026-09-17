package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
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
	ids, err := accessdb.New(db).SelectCurrentExecutionGrantIDs(ctx, accessdb.SelectCurrentExecutionGrantIDsParams{
		InstanceID: instanceID,
		ProjectID:  projectID.String(),
		ResourceID: pipelineID.String(),
	})
	if err != nil {
		return "", fmt.Errorf("select current execution grants: %w", err)
	}
	if len(ids) == 0 {
		return "", access.ErrGrantNotFound
	}
	if len(ids) != 1 {
		return "", fmt.Errorf("current execution grant selection is ambiguous: %d grants match the scheduled pipeline", len(ids))
	}
	selected := ids[0]
	if strings.TrimSpace(selected) == "" || strings.TrimSpace(selected) != selected {
		return "", errors.New("current execution grant identity is invalid")
	}
	return selected, nil
}
