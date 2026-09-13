package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/agent"
)

var _ agent.ModelRequestUsageStore = (*Repository)(nil)

func (r *Repository) ModelRequestUsage(ctx context.Context) (agent.ModelRequestUsage, error) {
	if r == nil || r.q == nil {
		return agent.ModelRequestUsage{}, errors.New("agent repository database is required")
	}
	row, err := r.q.GetModelRequestUsage(ctx)
	if err != nil {
		return agent.ModelRequestUsage{}, err
	}
	return mapModelRequestUsage(row.UsedRequests, row.ResetsAt)
}

func (r *Repository) ReserveModelRequest(ctx context.Context, limit int64) (agent.ModelRequestUsage, error) {
	if r == nil || r.q == nil {
		return agent.ModelRequestUsage{}, errors.New("agent repository database is required")
	}
	row, err := r.q.ReserveModelRequest(ctx, limit)
	if err == nil {
		return mapModelRequestUsageDay(row.UsedRequests, row.UsageDay)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return agent.ModelRequestUsage{}, err
	}
	usage, usageErr := r.ModelRequestUsage(ctx)
	if usageErr != nil {
		return agent.ModelRequestUsage{}, usageErr
	}
	return usage, agent.ErrModelRequestLimit
}

func mapModelRequestUsage(used int64, resetsAt string) (agent.ModelRequestUsage, error) {
	parsed, err := time.Parse(time.RFC3339Nano, resetsAt)
	if err != nil {
		return agent.ModelRequestUsage{}, fmt.Errorf("parse model request usage reset time: %w", err)
	}
	return agent.ModelRequestUsage{Used: used, ResetsAt: parsed.UTC()}, nil
}

func mapModelRequestUsageDay(used int64, usageDay string) (agent.ModelRequestUsage, error) {
	parsed, err := time.Parse("2006-01-02", usageDay)
	if err != nil {
		return agent.ModelRequestUsage{}, fmt.Errorf("parse model request usage day: %w", err)
	}
	return agent.ModelRequestUsage{Used: used, ResetsAt: parsed.AddDate(0, 0, 1).UTC()}, nil
}
