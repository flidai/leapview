package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/agent"
	agentdb "github.com/flidai/leapview/internal/agent/postgres/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (r *Repository) SaveConfigurationCandidate(ctx context.Context, candidate agent.ConfigurationCandidate) error {
	id, err := uuid.Parse(candidate.ID)
	if err != nil || id == uuid.Nil || id.String() != candidate.ID || candidate.ExpectedRevision < 0 ||
		candidate.Config.APIKey != "" || len(candidate.Credential) != 0 || candidate.CredentialVersionID != "" {
		return fmt.Errorf("invalid non-secret agent configuration candidate")
	}
	candidate.Config.Revision = 0
	raw, err := json.Marshal(candidate.Config)
	if err != nil {
		return err
	}
	_, err = agentdb.New(r.db).InsertAgentConfigurationCandidate(ctx, agentdb.InsertAgentConfigurationCandidateParams{
		CandidateID: pgtype.UUID{Bytes: id, Valid: true}, ExpectedRevision: candidate.ExpectedRevision,
		Enabled: candidate.Enabled, ConfigJson: raw, ActorID: candidate.ActorID,
	})
	return err
}

func (r *Repository) ConfigurationCandidate(ctx context.Context, id string) (agent.ConfigurationCandidate, error) {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed == uuid.Nil || parsed.String() != id {
		return agent.ConfigurationCandidate{}, fmt.Errorf("invalid agent configuration candidate")
	}
	row, err := agentdb.New(r.db).AgentConfigurationCandidate(ctx, pgtype.UUID{Bytes: parsed, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return agent.ConfigurationCandidate{}, agent.ErrConfigurationNotFound
	}
	if err != nil {
		return agent.ConfigurationCandidate{}, err
	}
	record := agent.ConfigurationRevision{Enabled: row.Enabled, ActorID: row.ActorID, CreatedAt: row.CreatedAt.Time}
	if err = json.Unmarshal(row.ConfigJson, &record.Config); err != nil {
		return agent.ConfigurationCandidate{}, err
	}
	return agent.ConfigurationCandidate{ID: id, ExpectedRevision: row.ExpectedRevision, ConfigurationRevision: record}, nil
}
