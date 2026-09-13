package postgres

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/agent"
	agentdb "github.com/flidai/leapview/internal/agent/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var _ agent.ModelRequestUsageStore = (*Repository)(nil)

func (r *Repository) ModelRequestUsage(ctx context.Context) (agent.ModelRequestUsage, error) {
	if r == nil || r.db == nil {
		return agent.ModelRequestUsage{}, errors.New("agent PostgreSQL database is required")
	}
	row, err := agentdb.New(r.db).GetModelRequestUsage(ctx)
	if err != nil {
		return agent.ModelRequestUsage{}, err
	}
	return mapModelRequestUsage(row.UsedRequests, row.ResetsAt)
}

func (r *Repository) ReserveModelRequest(ctx context.Context, limit int64) (agent.ModelRequestUsage, error) {
	if r == nil || r.db == nil {
		return agent.ModelRequestUsage{}, errors.New("agent PostgreSQL database is required")
	}
	row, err := agentdb.New(r.db).ReserveModelRequest(ctx, limit)
	if err == nil {
		return mapModelRequestUsage(row.UsedRequests, row.ResetsAt)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return agent.ModelRequestUsage{}, err
	}
	usage, usageErr := r.ModelRequestUsage(ctx)
	if usageErr != nil {
		return agent.ModelRequestUsage{}, usageErr
	}
	return usage, agent.ErrModelRequestLimit
}

func mapModelRequestUsage(used int64, resetsAt pgtype.Timestamptz) (agent.ModelRequestUsage, error) {
	if !resetsAt.Valid {
		return agent.ModelRequestUsage{}, errors.New("model request usage reset time is invalid")
	}
	return agent.ModelRequestUsage{Used: used, ResetsAt: resetsAt.Time.UTC()}, nil
}
