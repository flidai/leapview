package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/analytics/queryaudit"
	"github.com/jackc/pgx/v5"
)

// GetQueryEventGlobal resolves one event without requiring a caller to guess
// which project currently owns it. The admin query-history surface is
// intentionally project-global; project-scoped API callers continue to use
// GetQueryEvent for authorization-by-project.
func (r *Repository) GetQueryEventGlobal(ctx context.Context, id string) (queryaudit.Event, bool, error) {
	if r == nil || r.db == nil {
		return queryaudit.Event{}, false, fmt.Errorf("%w: repository is unavailable", ErrInvalid)
	}
	eventID, err := parseUUID(id)
	if err != nil {
		return queryaudit.Event{}, false, err
	}
	stored, err := r.getStored(ctx, eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return queryaudit.Event{}, false, nil
	}
	if err != nil {
		return queryaudit.Event{}, false, err
	}
	event, err := toEvent(stored)
	return event, err == nil, err
}
