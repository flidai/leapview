package agent

import (
	"context"
	"fmt"
	"time"

	agentconfig "github.com/flidai/leapview/internal/agent/config"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

type DailyRequestLimitProvider func(context.Context) (int64, error)

type limitedModel struct {
	inner agentcore.Model
	store ModelRequestUsageStore
	limit DailyRequestLimitProvider
}

func (m *limitedModel) Complete(ctx context.Context, request agentcore.ModelRequest, stream agentcore.ModelStream) (agentcore.ModelResponse, error) {
	limit, err := m.limit(ctx)
	if err != nil {
		return agentcore.ModelResponse{}, fmt.Errorf("load Agent usage limit: %w", err)
	}
	if _, err := agentconfig.NormalizeDailyRequestLimit(limit); err != nil {
		return agentcore.ModelResponse{}, err
	}
	if _, err := m.store.ReserveModelRequest(ctx, limit); err != nil {
		return agentcore.ModelResponse{}, err
	}
	// Count provider attempts even when they fail; never refund a possibly billed request.
	return m.inner.Complete(ctx, request, stream)
}

// SetDailyRequestLimitProvider wraps the shared model once, covering normal
// turns, tool follow-ups, context compaction and automatic title generation.
func (s *Service) SetDailyRequestLimitProvider(provider DailyRequestLimitProvider) error {
	if s == nil || s.model == nil {
		return nil
	}
	if provider == nil {
		return fmt.Errorf("Agent daily request limit provider is required")
	}
	store, ok := s.repo.(ModelRequestUsageStore)
	if !ok {
		return fmt.Errorf("Agent repository must persist daily model request usage")
	}
	if model, ok := s.model.(*limitedModel); ok {
		model.limit = provider
		return nil
	}
	s.model = &limitedModel{inner: s.model, store: store, limit: provider}
	return nil
}

func (s *Service) ModelRequestUsage(ctx context.Context) (ModelRequestUsage, error) {
	if s != nil {
		if store, ok := s.repo.(ModelRequestUsageStore); ok {
			return store.ModelRequestUsage(ctx)
		}
	}
	now := time.Now().UTC()
	return ModelRequestUsage{ResetsAt: time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)}, nil
}
