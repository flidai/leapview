package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/agent"
	agentdb "github.com/flidai/leapview/internal/agent/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func configurationRecord(revision int64, enabled bool, raw, credential []byte, version pgtype.Text, actor string) (agent.ConfigurationRevision, error) {
	r := agent.ConfigurationRevision{Revision: revision, Enabled: enabled, Credential: credential, CredentialVersionID: version.String, ActorID: actor}
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
	result, err := configurationRecord(row.Revision, row.Enabled, row.ConfigJson, row.Credential, row.CredentialVersionID, row.ActorID)
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
	result, err := configurationRecord(row.Revision, row.Enabled, row.ConfigJson, row.Credential, row.CredentialVersionID, row.ActorID)
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
	record, err = r.SaveConfigurationTx(ctx, tx, expected, record)
	if err != nil {
		return record, err
	}
	if err = tx.Commit(ctx); err != nil {
		return record, err
	}
	return record, nil
}

// SaveConfigurationTx appends the immutable configuration under the same lock
// as ordinary saves. The activation owner commits this pointer, operation state,
// and audit in its transaction; this method never commits caller-owned work.
func (r *Repository) SaveConfigurationTx(ctx context.Context, tx pgx.Tx, expected int64, record agent.ConfigurationRevision) (agent.ConfigurationRevision, error) {
	if tx == nil {
		return record, fmt.Errorf("agent configuration requires a transaction")
	}
	q := agentdb.New(tx)
	if err := q.LockAgentConfiguration(ctx); err != nil {
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
	row, err := q.InsertAgentConfiguration(ctx, agentdb.InsertAgentConfigurationParams{Revision: expected + 1, Enabled: record.Enabled, ConfigJson: raw, Credential: record.Credential, CredentialVersionID: pgtype.Text{String: record.CredentialVersionID, Valid: record.CredentialVersionID != ""}, ActorID: record.ActorID})
	if err != nil {
		return record, err
	}
	record.Revision = row.Revision
	record.CreatedAt = row.CreatedAt.Time
	return record, nil
}

// LockConfigurationTx returns current authority after serializing all agent
// configuration writers. Callers acquire the shared instance fence first.
func (r *Repository) LockConfigurationTx(ctx context.Context, tx pgx.Tx) (agent.ConfigurationRevision, error) {
	if tx == nil {
		return agent.ConfigurationRevision{}, fmt.Errorf("agent configuration requires a transaction")
	}
	q := agentdb.New(tx)
	if err := q.LockAgentConfiguration(ctx); err != nil {
		return agent.ConfigurationRevision{}, err
	}
	row, err := q.CurrentAgentConfiguration(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return agent.ConfigurationRevision{}, nil
	}
	if err != nil {
		return agent.ConfigurationRevision{}, err
	}
	result, err := configurationRecord(row.Revision, row.Enabled, row.ConfigJson, row.Credential, row.CredentialVersionID, row.ActorID)
	result.CreatedAt = row.CreatedAt.Time
	return result, err
}
