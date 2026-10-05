package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/agent"
	agentdb "github.com/flidai/leapview/internal/agent/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

func configurationRecord(revision int64, enabled bool, raw, credential []byte, actor string) (agent.ConfigurationRevision, error) {
	r := agent.ConfigurationRevision{Revision: revision, Enabled: enabled, Credential: credential, ActorID: actor}
	err := json.Unmarshal(raw, &r.Config)
	return r, err
}
func (r *Repository) CurrentConfiguration(ctx context.Context) (agent.ConfigurationRevision, error) {
	row, err := agentdb.New(r.db).CurrentAgentConfiguration(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return agent.ConfigurationRevision{}, agent.ErrConfigurationNotFound
	}
	if err != nil {
		return agent.ConfigurationRevision{}, err
	}
	result, err := configurationRecord(row.Revision, row.Enabled, row.ConfigJson, row.Credential, row.ActorID)
	result.CreatedAt = row.CreatedAt.Time
	return result, err
}
func (r *Repository) ConfigurationByRevision(ctx context.Context, revision int64) (agent.ConfigurationRevision, error) {
	row, err := agentdb.New(r.db).AgentConfigurationByRevision(ctx, revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return agent.ConfigurationRevision{}, agent.ErrConfigurationNotFound
	}
	if err != nil {
		return agent.ConfigurationRevision{}, err
	}
	result, err := configurationRecord(row.Revision, row.Enabled, row.ConfigJson, row.Credential, row.ActorID)
	result.CreatedAt = row.CreatedAt.Time
	return result, err
}
func (r *Repository) SaveConfiguration(ctx context.Context, expected int64, record agent.ConfigurationRevision) (agent.ConfigurationRevision, error) {
	begin, ok := r.db.(beginner)
	if !ok {
		return record, fmt.Errorf("agent configuration requires a transactional database")
	}
	tx, err := begin.Begin(ctx)
	if err != nil {
		return record, err
	}
	defer tx.Rollback(ctx)
	q := agentdb.New(tx)
	if err = q.LockAgentConfiguration(ctx); err != nil {
		return record, err
	}
	current, err := q.CurrentAgentConfiguration(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return record, err
	}
	if current.Revision != expected {
		return record, agent.ErrConfigurationConflict
	}
	record.Config.APIKey = ""
	record.Config.Revision = 0
	raw, err := json.Marshal(record.Config)
	if err != nil {
		return record, err
	}
	if record.Credential == nil {
		record.Credential = []byte{}
	}
	row, err := q.InsertAgentConfiguration(ctx, agentdb.InsertAgentConfigurationParams{Revision: expected + 1, Enabled: record.Enabled, ConfigJson: raw, Credential: record.Credential, ActorID: record.ActorID})
	if err != nil {
		return record, err
	}
	if err = tx.Commit(ctx); err != nil {
		return record, err
	}
	record.Revision = row.Revision
	record.CreatedAt = row.CreatedAt.Time
	return record, nil
}
